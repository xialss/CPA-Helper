package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConditionalKeeperRefreshCandidatesUseUsageQuotaAndCache(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	remoteDetails := map[string]map[string]any{
		"remote-detail-email.json": {
			"name":  "remote-detail-email.json",
			"type":  "codex",
			"email": "remote@example.com",
		},
		"remote-short-index.json": {
			"name":       "remote-short-index.json",
			"type":       "codex",
			"auth_index": "short-auth-index",
		},
		"remote-list-email.json": {
			"name":  "remote-list-email.json",
			"type":  "codex",
			"email": "list@example.com",
		},
	}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"name": "remote-detail-email.json", "type": "codex"},
					{"name": "remote-short-index.json", "type": "codex"},
					{"name": "remote-list-email.json", "type": "codex", "email": "list@example.com"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			detail, ok := remoteDetails[r.URL.Query().Get("name")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	now := time.Now().In(appTimeLocation)

	insertKeeperUsageRecord(t, app, "active-request", now.Add(-time.Minute), `{"auth_index":"active-request.json","failed":true}`)
	insertKeeperUsageRecord(t, app, "email-request", now.Add(-time.Minute), `{"auth_index":"person@example.com"}`)
	insertKeeperUsageRecord(t, app, "source-email-request", now.Add(-time.Minute), `{"source":"source@example.com","auth_index":"source-short-index"}`)
	insertKeeperUsageRecord(t, app, "remote-detail-email-request", now.Add(-time.Minute), `{"auth_index":"remote@example.com"}`)
	insertKeeperUsageRecord(t, app, "remote-list-email-request", now.Add(-time.Minute), `{"auth_index":"list@example.com"}`)
	insertKeeperUsageRecord(t, app, "remote-short-index-request", now.Add(-time.Minute), `{"auth_index":"short-auth-index"}`)
	insertKeeperUsageRecord(t, app, "unknown-email-request", now.Add(-time.Minute), `{"auth_index":"unknown@example.com"}`)
	insertKeeperUsageRecord(t, app, "old-request", now.Add(-20*time.Minute), `{"auth_index":"old-request.json"}`)
	insertKeeperUsageRecord(t, app, "cached-request", now.Add(-time.Minute), `{"auth_index":"cached-request.json"}`)
	insertKeeperUsageRecord(t, app, "cached-email-request", now.Add(-time.Minute), `{"auth_index":"cached@example.com"}`)
	insertKeeperUsageRecord(t, app, "no-auth-index", now.Add(-time.Minute), `{"request_id":"missing-auth-index"}`)

	insertKeeperStateForCandidateWithEmail(t, app, "email-match.json", stringPtr("person@example.com"), nil, nil)
	insertKeeperStateForCandidateWithEmail(t, app, "source-match.json", stringPtr("source@example.com"), nil, nil)
	insertKeeperStateForCandidate(t, app, "cached-request.json", nil, timePtrValue(now.Add(-2*time.Minute)))
	insertKeeperStateForCandidateWithEmail(t, app, "cached-email.json", stringPtr("cached@example.com"), nil, timePtrValue(now.Add(-2*time.Minute)))
	insertKeeperStateForCandidate(t, app, "quota-due.json", timePtrValue(now.Add(-time.Minute)), nil)
	insertKeeperStateForCandidate(t, app, "quota-future.json", timePtrValue(now.Add(time.Minute)), nil)
	insertKeeperStateForCandidate(t, app, "quota-cached.json", timePtrValue(now.Add(-time.Minute)), timePtrValue(now.Add(-2*time.Minute)))

	names, err := app.conditionalKeeperRefreshCandidates(ctx, cfg)
	if err != nil {
		t.Fatalf("conditionalKeeperRefreshCandidates: %v", err)
	}
	assertStringSet(t, names, []string{
		"active-request.json",
		"email-match.json",
		"source-match.json",
		"remote-detail-email.json",
		"remote-list-email.json",
		"remote-short-index.json",
		"quota-due.json",
	})
}

func TestAutomaticKeeperRunsRespectCacheButManualRefreshBypasses(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	usageCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "cached.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         "cached.json",
				"type":         "codex",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			usageCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body": map[string]any{
					"plan_type": "free",
					"rate_limit": map[string]any{
						"primary_window": map[string]any{
							"used_percent":        10,
							"reset_after_seconds": 3600,
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL, func(cfg *AppConfig) {
		cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	})
	insertKeeperStateForCandidate(t, app, "cached.json", nil, timePtrValue(time.Now().In(appTimeLocation).Add(-time.Minute)))

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "daemon", nil, func(string) {})
	if err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("daemon skipped = %d, want 1", stats.Skipped)
	}
	if usageCalls != 0 {
		t.Fatalf("daemon usage calls = %d, want 0", usageCalls)
	}

	_, _, err = app.executeKeeperRunForAccounts(context.Background(), "accounts", []string{"cached.json"}, func(string) {})
	if err != nil {
		t.Fatalf("manual account refresh: %v", err)
	}
	if usageCalls != 1 {
		t.Fatalf("manual usage calls = %d, want 1", usageCalls)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_runs`); got != 1 {
		t.Fatalf("keeper run rows = %d, want 1 because account refresh is not persisted", got)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_run_accounts`); got != 0 {
		t.Fatalf("keeper run account rows = %d, want 0 because skipped daemon and manual refresh are not persisted", got)
	}
}

func TestKeeperRunSkipsInFlightAuthBeforeProcessing(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	downloadCalls := 0
	usageCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "busy.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			downloadCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         "busy.json",
				"type":         "codex",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			usageCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL, nil)

	stats, _, err := app.executeKeeperRunWithOptions(context.Background(), keeperRunOptions{
		Mode:            "accounts",
		AuthNames:       []string{"busy.json"},
		ManualRefresh:   true,
		UseRefreshCache: false,
		PersistRun:      false,
		TryLockAuthName: func(mode string, name string) bool {
			if mode != "accounts" || name != "busy.json" {
				t.Fatalf("TryLockAuthName(%q, %q), want accounts/busy.json", mode, name)
			}
			return false
		},
		UnlockAuthName: func(name string) {
			t.Fatalf("UnlockAuthName(%q) called after a failed lock", name)
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("account refresh: %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", stats.Skipped)
	}
	if downloadCalls != 0 {
		t.Fatalf("download calls = %d, want 0", downloadCalls)
	}
	if usageCalls != 0 {
		t.Fatalf("usage calls = %d, want 0", usageCalls)
	}
}

func TestConditionalKeeperRunUsesAutomaticPriorityPolicy(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	priorityPatches := []int{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "quota.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         "quota.json",
				"type":         "codex",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body": map[string]any{
					"plan_type": "free",
					"rate_limit": map[string]any{
						"primary_window": map[string]any{
							"used_percent":        100,
							"reset_after_seconds": 3600,
						},
					},
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			var payload struct {
				Priority *int `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Priority != nil {
				priorityPatches = append(priorityPatches, *payload.Priority)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL, func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
		cfg.CodexKeeper.QuotaThreshold = 50
	})

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "conditional", []string{"quota.json"}, func(string) {})
	if err != nil {
		t.Fatalf("conditional run: %v", err)
	}
	if stats.PriorityDegraded != 1 {
		t.Fatalf("priority_degraded = %d, want 1", stats.PriorityDegraded)
	}
	if len(priorityPatches) != 1 || priorityPatches[0] != -1 {
		t.Fatalf("priority patches = %#v, want [-1]", priorityPatches)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_runs`); got != 0 {
		t.Fatalf("keeper run rows = %d, want 0 because conditional refresh is not persisted", got)
	}
}

func TestFullKeeperRunPrunesLocalStatesMissingFromCPA(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "kept.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         "kept.json",
				"type":         "codex",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body": map[string]any{
					"plan_type": "free",
					"rate_limit": map[string]any{
						"primary_window": map[string]any{"used_percent": 10, "reset_after_seconds": 3600},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL, nil)
	insertKeeperStateForCandidate(t, app, "kept.json", nil, nil)
	insertKeeperStateForCandidate(t, app, "stale.json", nil, nil)

	if _, _, err := app.executeKeeperRunForAccounts(context.Background(), "daemon", nil, func(string) {}); err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_auth_states WHERE auth_name = 'kept.json'`); got != 1 {
		t.Fatalf("kept state rows = %d, want 1", got)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_auth_states WHERE auth_name = 'stale.json'`); got != 0 {
		t.Fatalf("stale state rows = %d, want 0", got)
	}
}

func TestKeeperStatusStatsUseLatestDaemonRunOnly(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	daemonRunID, err := app.createKeeperRun(ctx, "daemon")
	if err != nil {
		t.Fatalf("create daemon run: %v", err)
	}
	if err := app.finishKeeperRun(ctx, daemonRunID, "completed", "daemon", keeperStats{
		Total:          7,
		Healthy:        6,
		StatusDisabled: 1,
	}); err != nil {
		t.Fatalf("finish daemon run: %v", err)
	}
	onceRunID, err := app.createKeeperRun(ctx, "once")
	if err != nil {
		t.Fatalf("create once run: %v", err)
	}
	if err := app.finishKeeperRun(ctx, onceRunID, "completed", "once", keeperStats{
		Total:            2,
		Healthy:          1,
		NetworkError:     1,
		PriorityRestored: 1,
	}); err != nil {
		t.Fatalf("finish once run: %v", err)
	}

	app.keeper.LoadPersistedState(ctx)
	status := app.keeper.Status()
	if status.Stats.Total != 7 || status.Stats.Healthy != 6 || status.Stats.StatusDisabled != 1 || status.Stats.NetworkError != 0 {
		t.Fatalf("status stats = %#v, want latest daemon stats only", status.Stats)
	}
}

func configureKeeperTestCPA(t *testing.T, app *App, url string, mutate func(*AppConfig)) {
	t.Helper()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg.Collector.CLIProxyURL = url
	cfg.Collector.ManagementKey = "test-management-key"
	cfg.Collector.Enabled = false
	cfg.CodexKeeper.ScheduleCron = "0 0 29 2 *"
	cfg.CodexKeeper.CPATimeoutSeconds = 1
	cfg.CodexKeeper.UsageTimeoutSeconds = 1
	if mutate != nil {
		mutate(&cfg)
	}
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
}

func insertKeeperUsageRecord(t *testing.T, app *App, dedupe string, timestamp time.Time, rawJSON string) {
	t.Helper()
	now := dbTime(time.Now().In(appTimeLocation))
	source := "test"
	if value := rawJSONStringField(rawJSON, "source"); value != nil {
		source = *value
	}
	_, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider,
			model, endpoint, source, request_id, auth, latency_ms, failed,
			input_tokens, output_tokens, cached_tokens, reasoning_tokens,
			total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, NULL, NULL, 'codex', 'gpt-test', '/v1/responses',
			?, ?, 'api_key', 10, 1, 1, 1, 0, 0, 2, ?, ?)
	`, now, dbTime(timestamp), source, dedupe, "conditional-"+dedupe, rawJSON)
	if err != nil {
		t.Fatalf("insert usage record %s: %v", dedupe, err)
	}
}

func insertKeeperStateForCandidate(t *testing.T, app *App, name string, primaryResetAt *time.Time, lastCheckedAt *time.Time) {
	t.Helper()
	insertKeeperStateForCandidateWithEmail(t, app, name, nil, primaryResetAt, lastCheckedAt)
}

func insertKeeperStateForCandidateWithEmail(t *testing.T, app *App, name string, email *string, primaryResetAt *time.Time, lastCheckedAt *time.Time) {
	t.Helper()
	now := dbTime(time.Now().In(appTimeLocation))
	_, err := app.db.Exec(`
		INSERT INTO codex_keeper_auth_states (
			auth_name, email, disabled, primary_reset_at, last_checked_at, created_at, updated_at
		) VALUES (?, ?, 0, ?, ?, ?, ?)
		ON CONFLICT(auth_name) DO UPDATE SET
			email = excluded.email,
			primary_reset_at = excluded.primary_reset_at,
			last_checked_at = excluded.last_checked_at,
			updated_at = excluded.updated_at
	`, name, email, dbTimePtr(primaryResetAt), dbTimePtr(lastCheckedAt), now, now)
	if err != nil {
		t.Fatalf("insert keeper state %s: %v", name, err)
	}
}

func stringPtr(value string) *string {
	return &value
}

func timePtrValue(value time.Time) *time.Time {
	return &value
}

func countKeeperRows(t *testing.T, app *App, query string) int {
	t.Helper()
	var count int
	if err := app.db.QueryRow(query).Scan(&count); err != nil {
		t.Fatalf("count rows with %q: %v", query, err)
	}
	return count
}

func assertStringSet(t *testing.T, got []string, want []string) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, item := range got {
		gotSet[item] = true
	}
	if len(gotSet) != len(want) {
		t.Fatalf("names = %#v, want set %#v", got, want)
	}
	for _, item := range want {
		if !gotSet[item] {
			t.Fatalf("names = %#v, want set %#v", got, want)
		}
	}
}
