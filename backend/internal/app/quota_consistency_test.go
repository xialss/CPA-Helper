package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestDisableRemoteFailureRestoresRemovedKeysAndRollsBackState(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
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
	keys := []string{"sk-disable-compensation-a", "sk-disable-compensation-b"}
	for _, key := range keys {
		if _, err := app.db.ExecContext(ctx, `
			INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
			VALUES (?, ?, ?, 'test', ?, ?)
		`, hashAPIKey(key), member.ID, key, now, now); err != nil {
			t.Fatalf("insert API key %q: %v", key, err)
		}
	}

	var remoteMu sync.Mutex
	remoteKeys := append([]string(nil), keys...)
	putCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			remoteMu.Lock()
			current := append([]string(nil), remoteKeys...)
			remoteMu.Unlock()
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPatch:
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			found := false
			for _, existing := range remoteKeys {
				if existing == payload.New {
					found = true
					break
				}
			}
			if !found {
				remoteKeys = append(remoteKeys, payload.New)
			}
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			remoteMu.Lock()
			putCalls++
			call := putCalls
			remoteMu.Unlock()
			if call == 2 {
				http.Error(w, "forced remove failure", http.StatusInternalServerError)
				return
			}
			var next []string
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			remoteKeys = append([]string(nil), next...)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	actor := &AuthUser{ID: 1, Username: "root", IsAdmin: true, IsSuperAdmin: true}
	if err := app.disableUser(ctx, actor, member.ID); err == nil {
		t.Fatal("disableUser succeeded despite the forced second remote removal failure")
	}
	member, err = app.getUser(ctx, member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if member.DisabledAt != nil {
		t.Fatalf("member remains disabled after a compensatable failure: %v", member.DisabledAt)
	}
	if member.QuotaSyncError != nil {
		t.Fatalf("compensated remote failure left sync error on user: %q", *member.QuotaSyncError)
	}
	remoteMu.Lock()
	finalKeys := append([]string(nil), remoteKeys...)
	remoteMu.Unlock()
	sort.Strings(finalKeys)
	wantKeys := append([]string(nil), keys...)
	sort.Strings(wantKeys)
	if len(finalKeys) != len(wantKeys) || finalKeys[0] != wantKeys[0] || finalKeys[1] != wantKeys[1] {
		t.Fatalf("remote keys after compensation = %#v, want %#v", finalKeys, wantKeys)
	}
}

func TestEnableDatabaseFailureCleansRestoredRemoteKeys(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
	disabledAt := dbTime(time.Now().Add(-time.Minute))
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO users (username, is_admin, is_super_admin, nickname, disabled_at, created_at, updated_at)
		VALUES ('root', 1, 1, 'Root', NULL, ?, ?), ('member', 0, 0, 'Member', ?, ?, ?)
	`, now, now, disabledAt, now, now); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	member, err := app.userByUsername(ctx, "member")
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	const apiKey = "sk-enable-db-failure"
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, ?, ?, 'test', ?, ?)
	`, hashAPIKey(apiKey), member.ID, apiKey, now, now); err != nil {
		t.Fatalf("insert API key: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TRIGGER fail_enable_update
		BEFORE UPDATE OF disabled_at ON users
		WHEN NEW.id = %d AND OLD.disabled_at IS NOT NULL AND NEW.disabled_at IS NULL
		BEGIN
			SELECT RAISE(ABORT, 'forced enable database failure');
		END
	`, member.ID)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	var remoteMu sync.Mutex
	remoteKeys := []string{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			remoteMu.Lock()
			current := append([]string(nil), remoteKeys...)
			remoteMu.Unlock()
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPatch:
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			remoteKeys = append(remoteKeys, payload.New)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			var next []string
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			remoteKeys = append([]string(nil), next...)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	actor := &AuthUser{ID: 1, Username: "root", IsAdmin: true, IsSuperAdmin: true}
	if err := app.enableUser(ctx, actor, member.ID); err == nil {
		t.Fatal("enableUser succeeded despite the forced database failure")
	}
	member, err = app.getUser(ctx, member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if member.DisabledAt == nil {
		t.Fatal("member was enabled despite the database failure")
	}
	remoteMu.Lock()
	finalKeys := append([]string(nil), remoteKeys...)
	remoteMu.Unlock()
	if len(finalKeys) != 0 {
		t.Fatalf("restored remote keys were not cleaned up: %#v", finalKeys)
	}
}

func TestQuotaPauseRemoteFailureReturnsAndPersistsSyncError(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "quota-pause-failure")
	seedQuotaTestAPIKey(t, app, userID, "sk-pause-failure")
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodGet {
			http.Error(w, "forced read failure", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	err = app.pauseUserKeysForQuota(ctx, userID, quotaPauseReasonExhausted)
	if err == nil {
		t.Fatal("pauseUserKeysForQuota swallowed the remote synchronization failure")
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.QuotaPausedAt == nil {
		t.Fatal("quota pause state was not persisted after remote failure")
	}
	if user.QuotaSyncError == nil || *user.QuotaSyncError == "" {
		t.Fatal("quota synchronization failure was not persisted")
	}
}

func TestDeleteCurrentUserAPIKeyRestoresRemoteKeyAfterFailedWrite(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "delete-key-compensation")
	const apiKey = "sk-delete-compensation"
	seedQuotaTestAPIKey(t, app, userID, apiKey)

	var remoteMu sync.Mutex
	remoteKeys := []string{apiKey}
	putCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			remoteMu.Lock()
			current := append([]string(nil), remoteKeys...)
			remoteMu.Unlock()
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPut:
			var next []string
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			putCalls++
			call := putCalls
			remoteKeys = append([]string(nil), next...)
			remoteMu.Unlock()
			if call == 1 {
				// Simulate an ambiguous CPA failure: the write has taken effect,
				// but the client receives an error and must compensate.
				http.Error(w, "forced delete write failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPatch:
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			found := false
			for _, existing := range remoteKeys {
				if existing == payload.New {
					found = true
					break
				}
			}
			if !found {
				remoteKeys = append(remoteKeys, payload.New)
			}
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	actor := &AuthUser{ID: user.ID, Username: user.Username}
	if err := app.deleteCurrentUserAPIKey(ctx, actor, hashAPIKey(apiKey)); err == nil {
		t.Fatal("deleteCurrentUserAPIKey succeeded despite the forced remote write failure")
	}
	if _, err := app.getAPIKey(ctx, hashAPIKey(apiKey)); err != nil {
		t.Fatalf("local API key binding was removed after failed delete: %v", err)
	}
	remoteMu.Lock()
	finalKeys := append([]string(nil), remoteKeys...)
	remoteMu.Unlock()
	if len(finalKeys) != 1 || finalKeys[0] != apiKey {
		t.Fatalf("remote keys after delete compensation = %#v, want [%q]", finalKeys, apiKey)
	}
}

func TestQuotaStatusRetriesFailedPauseWhileStillExhausted(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	userID := seedQuotaTestUser(t, app, "quota-pause-retry")
	const apiKey = "sk-pause-retry"
	seedQuotaTestAPIKey(t, app, userID, apiKey)
	now := dbTime(time.Now())
	if _, err := app.db.ExecContext(ctx, `
		UPDATE users
		SET quota_lifetime_usd = 0, quota_started_at = ?, quota_month = ?, quota_sync_error = NULL
		WHERE id = ?
	`, now, quotaMonth(time.Now()), userID); err != nil {
		t.Fatalf("set exhausted quota: %v", err)
	}

	var remoteMu sync.Mutex
	remoteKeys := []string{apiKey}
	failReads := true
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			remoteMu.Lock()
			shouldFail := failReads
			current := append([]string(nil), remoteKeys...)
			remoteMu.Unlock()
			if shouldFail {
				http.Error(w, "forced first pause read failure", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPut:
			var next []string
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteMu.Lock()
			remoteKeys = append([]string(nil), next...)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	if err := app.pauseUserKeysForQuota(ctx, userID, quotaPauseReasonExhausted); err == nil {
		t.Fatal("initial quota pause succeeded despite the forced remote read failure")
	}
	user, err := app.getUser(ctx, userID)
	if err != nil {
		t.Fatalf("reload paused user: %v", err)
	}
	if user.QuotaPausedAt == nil || user.QuotaSyncError == nil || *user.QuotaSyncError == "" {
		t.Fatalf("failed pause state = paused=%v sync_error=%v, want persisted pause and error", user.QuotaPausedAt, user.QuotaSyncError)
	}

	remoteMu.Lock()
	failReads = false
	remoteMu.Unlock()
	status, err := app.userQuotaStatus(ctx, userID)
	if err != nil {
		t.Fatalf("quota status retry failed: %v", err)
	}
	if !status.Paused {
		t.Fatal("quota status retry unexpectedly cleared the exhausted pause")
	}
	if status.SyncError != nil && *status.SyncError != "" {
		t.Fatalf("quota status retry kept a stale sync error: %v", *status.SyncError)
	}
	remoteMu.Lock()
	finalKeys := append([]string(nil), remoteKeys...)
	remoteMu.Unlock()
	if len(finalKeys) != 0 {
		t.Fatalf("remote keys after quota status retry = %#v, want empty", finalKeys)
	}
}

func configureQuotaConsistencyCPA(t *testing.T, app *App, url string) {
	t.Helper()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Collector.CLIProxyURL = url
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func TestDisableRejectsInvalidRemoteAPIKeyList(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := dbTime(time.Now())
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
	const apiKey = "sk-invalid-remote-list"
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, ?, ?, 'test', ?, ?)
	`, hashAPIKey(apiKey), member.ID, apiKey, now, now); err != nil {
		t.Fatalf("insert API key: %v", err)
	}

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"api-keys": [`))
			return
		}
		http.Error(w, "unexpected request", http.StatusMethodNotAllowed)
	}))
	defer cpa.Close()
	configureQuotaConsistencyCPA(t, app, cpa.URL)

	actor := &AuthUser{ID: 1, Username: "root", IsAdmin: true, IsSuperAdmin: true}
	if err := app.disableUser(ctx, actor, member.ID); err == nil {
		t.Fatal("disableUser succeeded despite an invalid remote API-key list")
	}
	member, err = app.getUser(ctx, member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if member.DisabledAt != nil {
		t.Fatal("member was disabled after an invalid remote API-key list")
	}
}
