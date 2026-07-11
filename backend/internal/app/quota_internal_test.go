package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuotaChargesMonthlyBeforeLifetimeBalanceAndDedupesUsage(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-monthly-lifetime"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	seedQuotaTestPrice(t, app, "openai", "gpt-quota", 1)
	lifetime := 2.0
	monthly := 1.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime, MonthlyQuotaUSD: &monthly}); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"openai","model":"gpt-quota","input_tokens":1500000,"request_id":"quota-1"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || !created {
		t.Fatalf("first usage created=%v err=%v", created, err)
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaMonthUsedUSD != 1 || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 1.5 {
		t.Fatalf("quota after first charge month=%v lifetime=%v", user.QuotaMonthUsedUSD, user.QuotaLifetimeUSD)
	}
	var monthlyDeducted, lifetimeDeducted float64
	if err := app.db.QueryRow(`SELECT monthly_deducted_usd, lifetime_deducted_usd FROM user_quota_charges WHERE usage_username = 'member'`).Scan(&monthlyDeducted, &lifetimeDeducted); err != nil {
		t.Fatal(err)
	}
	if monthlyDeducted != 1 || lifetimeDeducted != 0.5 {
		t.Fatalf("deductions = monthly %.2f lifetime %.2f, want 1.00 and 0.50", monthlyDeducted, lifetimeDeducted)
	}

	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || created {
		t.Fatalf("duplicate usage created=%v err=%v", created, err)
	}
	var charges int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM user_quota_charges`).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if charges != 1 {
		t.Fatalf("charge count = %d, want 1", charges)
	}

	raw2 := `{"api_key":"` + apiKey + `","provider":"openai","model":"gpt-quota","input_tokens":1500000,"request_id":"quota-2"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw2)); err != nil || !created {
		t.Fatalf("second usage created=%v err=%v", created, err)
	}
	user, err = app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 0 || user.QuotaPausedAt == nil {
		t.Fatalf("quota after exhaustion lifetime=%v paused=%v", user.QuotaLifetimeUSD, user.QuotaPausedAt)
	}
}

func TestQuotaChargesImageUsageByRequestPrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-image"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	seedQuotaTestRequestPrice(t, app, "openai", "gpt-image-2", 1.25)
	lifetime := 2.0
	monthly := 1.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime, MonthlyQuotaUSD: &monthly}); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"openai","model":"gpt-image-2","request_id":"quota-image"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || !created {
		t.Fatalf("image usage created=%v err=%v", created, err)
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaMonthUsedUSD != 1 || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 1.75 {
		t.Fatalf("quota after image charge month=%v lifetime=%v", user.QuotaMonthUsedUSD, user.QuotaLifetimeUSD)
	}
	var amount, monthlyDeducted, lifetimeDeducted float64
	var unpriced bool
	if err := app.db.QueryRow(`SELECT amount_usd, monthly_deducted_usd, lifetime_deducted_usd, unpriced FROM user_quota_charges`).Scan(&amount, &monthlyDeducted, &lifetimeDeducted, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 1.25 || monthlyDeducted != 1 || lifetimeDeducted != 0.25 || unpriced {
		t.Fatalf("image charge amount=%v monthly=%v lifetime=%v unpriced=%v, want 1.25/1/0.25/false", amount, monthlyDeducted, lifetimeDeducted, unpriced)
	}
}

func TestQuotaChargesFastUsageWithConfiguredMultiplier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-fast"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	seedQuotaTestPrice(t, app, "openai", "gpt-quota-fast", 1)
	if _, err := app.db.Exec(`UPDATE model_prices SET priority_multiplier = 2 WHERE provider = 'openai' AND model = 'gpt-quota-fast'`); err != nil {
		t.Fatalf("configure Fast multiplier: %v", err)
	}
	lifetime := 5.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"openai","model":"gpt-quota-fast","service_tier":"priority","input_tokens":1000000,"request_id":"quota-fast"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || !created {
		t.Fatalf("fast usage created=%v err=%v", created, err)
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 3 {
		t.Fatalf("quota after Fast charge lifetime=%v, want 3", user.QuotaLifetimeUSD)
	}
	var amount float64
	if err := app.db.QueryRow(`SELECT amount_usd FROM user_quota_charges WHERE usage_username = 'member'`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 2 {
		t.Fatalf("Fast charge amount=%v, want 2", amount)
	}
}

func TestQuotaDoesNotChargeUnroundableFastCost(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-fast-overflow"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	seedQuotaTestPrice(t, app, "openai", "gpt-quota-fast-overflow", 1)
	if _, err := app.db.Exec(`UPDATE model_prices SET priority_multiplier = 1e308 WHERE provider = 'openai' AND model = 'gpt-quota-fast-overflow'`); err != nil {
		t.Fatalf("configure unroundable Fast multiplier: %v", err)
	}
	lifetime := 5.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"openai","model":"gpt-quota-fast-overflow","service_tier":"priority","input_tokens":1000000,"request_id":"quota-fast-overflow"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || !created {
		t.Fatalf("overflow Fast usage created=%v err=%v", created, err)
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 5 || user.QuotaUnpricedRecords != 1 {
		t.Fatalf("quota after unroundable Fast charge lifetime=%v unpriced=%d, want 5/1", user.QuotaLifetimeUSD, user.QuotaUnpricedRecords)
	}
	var amount float64
	var unpriced bool
	if err := app.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_username = 'member'`).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 0 || !unpriced {
		t.Fatalf("unroundable Fast charge amount=%v unpriced=%v, want 0/true", amount, unpriced)
	}
}

func TestQuotaUnpricedUsageDoesNotDeductBalance(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-unpriced"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	lifetime := 1.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatalf("update quota: %v", err)
	}
	raw := `{"api_key":"` + apiKey + `","provider":"unknown","model":"missing","input_tokens":1000,"request_id":"quota-unpriced"}`
	if _, created, err := app.saveUsageMessage(ctx, []byte(raw)); err != nil || !created {
		t.Fatalf("usage created=%v err=%v", created, err)
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 1 || user.QuotaUnpricedRecords != 1 {
		t.Fatalf("quota after unpriced lifetime=%v unpriced=%d", user.QuotaLifetimeUSD, user.QuotaUnpricedRecords)
	}
	var amount float64
	var unpriced bool
	if err := app.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges`).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 0 || !unpriced {
		t.Fatalf("charge amount=%v unpriced=%v, want 0 true", amount, unpriced)
	}
}

func TestQuotaMonthlyResetRestoresPausedKeys(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	remoteKeys := []string{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteKeys = append(remoteKeys, payload.New)
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		}
	}))
	defer cpa.Close()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	userID := seedQuotaTestUser(t, app, "member")
	apiKey := "sk-quota-restore"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	monthly := 1.0
	if _, err := app.updateUserQuota(ctx, userID, userQuotaPayload{MonthlyQuotaUSD: &monthly}); err != nil {
		t.Fatalf("update quota: %v", err)
	}
	if _, err := app.db.Exec(`UPDATE users SET quota_month = '2026-04', quota_month_used_usd = 1, quota_paused_at = ?, quota_pause_reason = ? WHERE id = ?`, dbTime(time.Now()), quotaPauseReasonExhausted, userID); err != nil {
		t.Fatal(err)
	}

	status, err := app.userQuotaStatus(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Paused || status.MonthlyUsedUSD != 0 {
		t.Fatalf("status after reset paused=%v monthly_used=%v", status.Paused, status.MonthlyUsedUSD)
	}
	if len(remoteKeys) != 1 || remoteKeys[0] != apiKey {
		t.Fatalf("remote keys = %#v, want restored key", remoteKeys)
	}
}

func TestQuotaDefaultsToUnlimited(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	userID := seedQuotaTestUser(t, app, "member")
	status, err := app.userQuotaStatus(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Unlimited || !status.CanCreateKeys {
		t.Fatalf("default quota status = %#v, want unlimited and creatable", status)
	}
}

func seedQuotaTestUser(t *testing.T, app *App, username string) int {
	t.Helper()
	now := dbTime(time.Now())
	result, err := app.db.Exec(`
		INSERT INTO users (username, is_admin, nickname, created_at, updated_at)
		VALUES (?, 0, ?, ?, ?)
	`, username, username, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return int(id)
}

func seedQuotaTestAPIKey(t *testing.T, app *App, userID int, apiKey string) {
	t.Helper()
	now := dbTime(time.Now())
	if _, err := app.db.Exec(`
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, ?, ?, 'VSCode', ?, ?)
	`, hashAPIKey(apiKey), userID, apiKey, now, now); err != nil {
		t.Fatal(err)
	}
}

func seedQuotaTestPrice(t *testing.T, app *App, provider, model string, inputUSDPerMillion float64) {
	t.Helper()
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, updated_at
		) VALUES (?, ?, ?, 0, 0, 0, ?)
	`, provider, model, inputUSDPerMillion, dbTime(time.Now())); err != nil {
		t.Fatal(err)
	}
}

func seedQuotaTestRequestPrice(t *testing.T, app *App, provider, model string, requestUSD float64) {
	t.Helper()
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd, updated_at
		) VALUES (?, ?, 0, 0, 0, 0, ?, ?)
	`, provider, model, requestUSD, dbTime(time.Now())); err != nil {
		t.Fatal(err)
	}
}
