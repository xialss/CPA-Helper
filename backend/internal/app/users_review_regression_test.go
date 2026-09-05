package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestUserUsageSummariesExcludePreCreationHistory(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million,
			cache_creation_usd_per_million, source, updated_at
		) VALUES ('codex', 'priced-model', 'channel', 'oauth', 'codex', 'oauth_pool',
			1, 0, 0, 0, 'manual', ?)
	`, dbTime(time.Now())); err != nil {
		t.Fatalf("insert price: %v", err)
	}

	todayStart, _ := defaultTodayRange()
	legacyCreatedAt := time.Now().In(appTimeLocation).Add(-time.Minute)
	if legacyCreatedAt.Before(todayStart) {
		legacyCreatedAt = todayStart.Add(time.Minute)
	}
	freshCreatedAt := legacyCreatedAt.Add(-time.Hour)
	insertUserUsageSummaryTestUser(t, app, "legacy", legacyCreatedAt)
	insertUserUsageSummaryTestUser(t, app, "fresh", freshCreatedAt)

	historic := legacyCreatedAt.Add(-48 * time.Hour)
	insertUserUsageSummaryTestRecord(t, app, historic, "legacy", "priced-model", 1_000_000, "legacy-priced")
	insertUserUsageSummaryTestRecord(t, app, historic.Add(time.Minute), "legacy", "unpriced-model", 10, "legacy-unpriced")
	insertUserUsageSummaryTestRecord(t, app, legacyCreatedAt, "legacy", "priced-model", 1_000_000, "legacy-today")
	insertUserUsageSummaryTestRecord(t, app, freshCreatedAt.Add(30*time.Minute), "fresh", "priced-model", 1_000_000, "fresh-priced")

	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	provider, model, auth := "codex", "priced-model", "oauth"
	if _, status := findMatchingChannelPrice(pricing.Prices, UsageRecord{
		Provider:    &provider,
		Model:       &model,
		Auth:        &auth,
		InputTokens: 1_000_000,
	}, pricing.MatchContext); status != priceMatchStatusMatched {
		t.Fatalf("test price match status = %q, prices = %#v", status, pricing.Prices)
	}
	if _, err := app.db.ExecContext(ctx, `
		CREATE TABLE user_usage_summary_hourly_audit (rebuild_deletions INTEGER NOT NULL);
		INSERT INTO user_usage_summary_hourly_audit (rebuild_deletions) VALUES (0);
		CREATE TRIGGER user_usage_summary_hourly_audit_delete
		AFTER DELETE ON usage_analytics_hourly
		BEGIN
			UPDATE user_usage_summary_hourly_audit
			SET rebuild_deletions = rebuild_deletions + 1;
		END;
	`); err != nil {
		t.Fatalf("create hourly rebuild audit: %v", err)
	}

	users, err := app.allUsers(ctx)
	if err != nil {
		t.Fatalf("allUsers: %v", err)
	}
	summaries, err := app.userUsageSummaries(ctx, users)
	if err != nil {
		t.Fatalf("userUsageSummaries: %v", err)
	}

	legacy := summaries["legacy"]
	if legacy.Records != 1 || legacy.SuccessRecords != 1 || legacy.TotalTokens != 1_000_000 {
		t.Fatalf("legacy base summary = %#v, want only post-creation records", legacy)
	}
	wantHistoric, ok := parseDBTime(dbTime(legacyCreatedAt))
	if !ok {
		t.Fatalf("parse created timestamp %q", dbTime(legacyCreatedAt))
	}
	if legacy.FirstSeenAt == nil || !legacy.FirstSeenAt.Equal(wantHistoric) {
		t.Fatalf("legacy first_seen_at = %v, want %v", legacy.FirstSeenAt, wantHistoric)
	}
	if legacy.TodayRecords != 1 || legacy.TodayEstimatedCostUSD != 1 || legacy.TodayUnpricedRecords != 0 {
		t.Fatalf("legacy today summary = %#v, want one priced record", legacy)
	}
	if legacy.TotalEstimatedCostUSD != 1 || legacy.TotalUnpricedRecords != 0 {
		t.Fatalf("legacy total summary = %#v, want only post-creation cost", legacy)
	}

	fresh := summaries["fresh"]
	if fresh.Records != 1 || fresh.TotalEstimatedCostUSD != 1 || fresh.TotalUnpricedRecords != 0 {
		t.Fatalf("fresh summary = %#v, want account-created-since total", fresh)
	}

	var rebuildDeletions int
	if err := app.db.QueryRowContext(ctx, `SELECT rebuild_deletions FROM user_usage_summary_hourly_audit`).Scan(&rebuildDeletions); err != nil {
		t.Fatalf("read hourly rebuild audit: %v", err)
	}
	if rebuildDeletions != 0 {
		t.Fatalf("hourly aggregate rebuilt %d times while collecting exact per-user totals", rebuildDeletions)
	}
}

func TestDisableEnableLifecycleSerializesRemoteKeySync(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	var remoteMu sync.Mutex
	remoteKeys := []string{"sk-lifecycle-key"}
	blockNextRead := true
	disableReadStarted := make(chan struct{})
	releaseDisableRead := make(chan struct{})
	patchStarted := make(chan struct{}, 1)
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodGet:
			remoteMu.Lock()
			keys := append([]string(nil), remoteKeys...)
			block := blockNextRead
			blockNextRead = false
			remoteMu.Unlock()
			if block {
				close(disableReadStarted)
				<-releaseDisableRead
			}
			_ = json.NewEncoder(w).Encode(keys)
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch:
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			select {
			case patchStarted <- struct{}{}:
			default:
			}
			remoteMu.Lock()
			found := false
			for _, key := range remoteKeys {
				if key == payload.New {
					found = true
					break
				}
			}
			if !found {
				remoteKeys = append(remoteKeys, payload.New)
			}
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPut:
			var keys []string
			if err := json.NewDecoder(r.Body).Decode(&keys); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			remoteKeys = append([]string(nil), keys...)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	ctx := context.Background()
	now := dbTime(time.Now().In(appTimeLocation))
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (username, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES ('root', 1, 1, 'Root', ?, ?), ('member', 0, 0, 'Member', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	member, err := app.userByUsername(ctx, "member")
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, ?, 'sk-lifecycle-key', 'Lifecycle', ?, ?)
	`, hashAPIKey("sk-lifecycle-key"), member.ID, now, now); err != nil {
		t.Fatalf("insert API key: %v", err)
	}
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	actor := &AuthUser{ID: 1, Username: "root", IsAdmin: true, IsSuperAdmin: true}
	disableDone := make(chan error, 1)
	go func() {
		disableDone <- app.disableUser(ctx, actor, member.ID)
	}()
	<-disableReadStarted

	enableStarted := make(chan struct{})
	enableDone := make(chan error, 1)
	go func() {
		close(enableStarted)
		enableDone <- app.enableUser(ctx, actor, member.ID)
	}()
	<-enableStarted
	remoteTouchedBeforeDisableCompleted := false
	select {
	case <-patchStarted:
		remoteTouchedBeforeDisableCompleted = true
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseDisableRead)
	if err := <-disableDone; err != nil {
		t.Fatalf("disableUser: %v", err)
	}
	if err := <-enableDone; err != nil {
		t.Fatalf("enableUser: %v", err)
	}
	if remoteTouchedBeforeDisableCompleted {
		t.Fatal("enable touched remote keys before the pending disable completed")
	}

	member, err = app.getUser(ctx, member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if member.DisabledAt != nil {
		t.Fatalf("member remains disabled at %v", member.DisabledAt)
	}
	remoteMu.Lock()
	finalKeys := append([]string(nil), remoteKeys...)
	remoteMu.Unlock()
	if len(finalKeys) != 1 || finalKeys[0] != "sk-lifecycle-key" {
		t.Fatalf("remote API keys = %#v, want restored member key", finalKeys)
	}
}

func insertUserUsageSummaryTestUser(t *testing.T, app *App, username string, createdAt time.Time) {
	t.Helper()
	if _, err := app.db.Exec(`
		INSERT INTO users (username, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES (?, 0, 0, ?, ?, ?)
	`, username, username, dbTime(createdAt), dbTime(createdAt)); err != nil {
		t.Fatalf("insert user %q: %v", username, err)
	}
}

func insertUserUsageSummaryTestRecord(t *testing.T, app *App, timestamp time.Time, username, model string, inputTokens int, dedupeKey string) {
	t.Helper()
	if _, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, provider, model, endpoint, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, ?, 'codex', ?, '/v1/chat/completions', 'oauth', 0,
			?, 0, 0, 0, 0, 0, ?, ?, '{}')
	`, dbTime(timestamp), dbTime(timestamp), username, model, inputTokens, inputTokens, dedupeKey); err != nil {
		t.Fatalf("insert usage record %q: %v", dedupeKey, err)
	}
}

func TestFirstActiveUserIDFiltersAdmin(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (id, username, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES (1, 'standard_first', 0, 0, 'Standard', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert standard user: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (id, username, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES (2, 'admin_second', 1, 0, 'Admin', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert admin user: %v", err)
	}

	firstID, err := app.firstActiveUserID(ctx)
	if err != nil {
		t.Fatalf("firstActiveUserID() error = %v", err)
	}
	if firstID == nil || *firstID != 2 {
		t.Fatalf("firstActiveUserID() = %v, want 2", firstID)
	}
}

func TestUnbindUserAPIKeyRequiresPlaintextWhenRemoteConfigured(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (id, username, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES (1, 'root', 1, 1, 'Root', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		UPDATE app_settings SET management_key = 'test-cpa-mgmt-key' WHERE id = 1
	`); err != nil {
		t.Fatalf("update app_settings: %v", err)
	}

	keyHash := hashAPIKey("sk-test-bind-key")
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, 1, NULL, 'test key', ?, ?)
	`, keyHash, now, now); err != nil {
		t.Fatalf("insert user_api_keys: %v", err)
	}

	actor := &AuthUser{ID: 1, Username: "root", IsAdmin: true, IsSuperAdmin: true}
	err = app.unbindUserAPIKey(ctx, actor, 1, keyHash)
	if err == nil {
		t.Fatalf("unbindUserAPIKey succeeded, want error for missing plaintext")
	}
	var apiErr *AppError
	if errors.As(err, &apiErr) && apiErr.Code != "conflict" {
		t.Fatalf("unbindUserAPIKey error code = %q, want conflict", apiErr.Code)
	}
}

func TestMaintenanceAutoRestoresCrossMonthExhaustedUser(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
	pastMonth := "2020-01"
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (
			id, username, is_admin, is_super_admin, nickname,
			quota_monthly_usd, quota_month, quota_month_used_usd,
			quota_paused_at, quota_pause_reason, quota_sync_error,
			created_at, updated_at
		) VALUES (
			10, 'paused_user', 0, 0, 'Paused',
			10.0, ?, 10.0,
			?, 'quota_exhausted', NULL,
			?, ?
		)
	`, pastMonth, now, now, now); err != nil {
		t.Fatalf("insert paused user: %v", err)
	}

	runner := newUsageMaintenanceRunner(app)
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	user, err := app.getUser(ctx, 10)
	if err != nil {
		t.Fatalf("getUser: %v", err)
	}
	if user.QuotaPausedAt != nil {
		t.Fatalf("user.QuotaPausedAt = %v, want nil", user.QuotaPausedAt)
	}
	if user.QuotaMonthUsedUSD != 0 {
		t.Fatalf("user.QuotaMonthUsedUSD = %v, want 0", user.QuotaMonthUsedUSD)
	}
	if user.QuotaMonth == pastMonth {
		t.Fatalf("user.QuotaMonth = %q, want current month", user.QuotaMonth)
	}
}

func TestLoginRateLimiterStateReset(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
	salt, _ := createSalt()
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, password_salt, is_admin, is_super_admin, nickname, created_at, updated_at)
		VALUES (1, 'root', ?, ?, 1, 1, 'Root', ?, ?)
	`, hashPassword("correct-password", salt), salt, now, now); err != nil {
		t.Fatalf("insert root: %v", err)
	}

	handler := app.Routes()

	loginKey := "127.0.0.1:root"
	app.loginAttemptsMu.Lock()
	if app.loginFailures == nil {
		app.loginFailures = map[string]loginFailureRecord{}
	}
	app.loginFailures[loginKey] = loginFailureRecord{
		count:       10,
		lastAttempt: time.Now().Add(-6 * time.Minute),
	}
	app.loginAttemptsMu.Unlock()

	payload, _ := json.Marshal(map[string]string{
		"username": "root",
		"password": "wrong-password",
	})
	loginReq, _ := http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(payload))
	loginReq.RemoteAddr = "127.0.0.1:12345"
	loginReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, loginReq)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("login response code = %d, want 401 (not 403 locked out)", w.Code)
	}

	app.loginAttemptsMu.Lock()
	rec := app.loginFailures[loginKey]
	app.loginAttemptsMu.Unlock()
	if rec.count != 1 {
		t.Fatalf("login failure count = %d, want 1", rec.count)
	}
}
