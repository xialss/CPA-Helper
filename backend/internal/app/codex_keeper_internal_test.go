package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeeperUsageTimeoutDefaultIsThirtyButExistingValueIsPreserved(t *testing.T) {
	cfg, err := defaultConfig()
	if err != nil {
		t.Fatalf("defaultConfig: %v", err)
	}
	if cfg.CodexKeeper.UsageTimeoutSeconds != 30 {
		t.Fatalf("default usage_timeout_seconds = %d, want 30", cfg.CodexKeeper.UsageTimeoutSeconds)
	}
	normalized := normalizeKeeperConfig(KeeperConfig{UsageTimeoutSeconds: 15})
	if normalized.UsageTimeoutSeconds != 15 {
		t.Fatalf("normalized existing usage_timeout_seconds = %d, want 15", normalized.UsageTimeoutSeconds)
	}
}

func TestKeeperRequestRetriesTransientManagementFailures(t *testing.T) {
	attempts := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts <= 2 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer cpa.Close()

	cfg := AppConfig{
		Collector: CollectorConfig{
			CLIProxyURL:   cpa.URL,
			ManagementKey: "test-management-key",
		},
		CodexKeeper: KeeperConfig{MaxRetries: 2},
	}
	_, payload, err := (&App{}).keeperRequest(context.Background(), cfg, http.MethodGet, "/v0/management/auth-files", nil, nil, time.Second)
	if err != nil {
		t.Fatalf("keeperRequest: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("payload = %s, want ok response", payload)
	}
}

func TestKeeperRequestDoesNotRetryManagementClientErrors(t *testing.T) {
	attempts := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer cpa.Close()

	cfg := AppConfig{
		Collector: CollectorConfig{
			CLIProxyURL:   cpa.URL,
			ManagementKey: "test-management-key",
		},
		CodexKeeper: KeeperConfig{MaxRetries: 2},
	}
	_, _, err := (&App{}).keeperRequest(context.Background(), cfg, http.MethodGet, "/v0/management/auth-files", nil, nil, time.Second)
	if err == nil {
		t.Fatal("keeperRequest error is nil, want HTTP 401 error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

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
		"remote-disabled-detail-email.json": {
			"name":     "remote-disabled-detail-email.json",
			"type":     "codex",
			"email":    "disabled-detail@example.com",
			"disabled": true,
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
					{"name": "remote-disabled-list-email.json", "type": "codex", "email": "disabled-list@example.com", "disabled": true},
					{"name": "remote-disabled-detail-email.json", "type": "codex"},
					{"name": "email-match.json", "type": "codex"},
					{"name": "source-match.json", "type": "codex"},
					{"name": "cached-request.json", "type": "codex"},
					{"name": "cached-email.json", "type": "codex"},
					{"name": "quota-due.json", "type": "codex"},
					{"name": "quota-future.json", "type": "codex"},
					{"name": "quota-cached.json", "type": "codex"},
					{"name": "error-due.json", "type": "codex"},
					{"name": "error-cached.json", "type": "codex"},
					{"name": "normal-local.json", "type": "codex"},
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
	insertKeeperUsageRecord(t, app, "remote-disabled-list-email-request", now.Add(-time.Minute), `{"auth_index":"disabled-list@example.com"}`)
	insertKeeperUsageRecord(t, app, "remote-disabled-detail-email-request", now.Add(-time.Minute), `{"auth_index":"disabled-detail@example.com"}`)
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
	insertKeeperStateForCandidateWithError(t, app, "error-due.json", "network check failed", timePtrValue(now.Add(-20*time.Minute)))
	insertKeeperStateForCandidateWithError(t, app, "error-cached.json", "network check failed", timePtrValue(now.Add(-2*time.Minute)))
	insertKeeperStateForCandidate(t, app, "normal-local.json", nil, timePtrValue(now.Add(-20*time.Minute)))

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
		"error-due.json",
	})
}

func TestConditionalKeeperRefreshCandidatesSkipDisabledLocalAccounts(t *testing.T) {
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
	cfg.Collector.CLIProxyURL = ""
	cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	now := time.Now().In(appTimeLocation)

	insertKeeperUsageRecord(t, app, "enabled-request", now.Add(-time.Minute), `{"auth_index":"enabled-request.json","failed":true}`)
	insertKeeperUsageRecord(t, app, "disabled-request", now.Add(-time.Minute), `{"auth_index":"disabled-request.json","failed":true}`)
	insertKeeperUsageRecord(t, app, "disabled-email-request", now.Add(-time.Minute), `{"auth_index":"disabled@example.com","failed":true}`)

	insertKeeperStateForCandidate(t, app, "disabled-request.json", nil, nil)
	markKeeperStateDisabled(t, app, "disabled-request.json")
	insertKeeperStateForCandidateWithEmail(t, app, "disabled-email.json", stringPtr("disabled@example.com"), nil, nil)
	markKeeperStateDisabled(t, app, "disabled-email.json")
	insertKeeperStateForCandidate(t, app, "enabled-quota.json", timePtrValue(now.Add(-time.Minute)), nil)
	insertKeeperStateForCandidate(t, app, "disabled-quota.json", timePtrValue(now.Add(-time.Minute)), nil)
	markKeeperStateDisabled(t, app, "disabled-quota.json")
	insertKeeperStateForCandidateWithError(t, app, "enabled-error.json", "network check failed", timePtrValue(now.Add(-20*time.Minute)))
	insertKeeperStateForCandidateWithError(t, app, "disabled-error.json", "network check failed", timePtrValue(now.Add(-20*time.Minute)))
	markKeeperStateDisabled(t, app, "disabled-error.json")

	names, err := app.conditionalKeeperRefreshCandidates(ctx, cfg)
	if err != nil {
		t.Fatalf("conditionalKeeperRefreshCandidates: %v", err)
	}
	assertStringSet(t, names, []string{
		"enabled-request.json",
		"enabled-quota.json",
		"enabled-error.json",
	})
}

func TestConditionalKeeperRefreshCandidatesReconcileRemoteAuthStates(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"name": "kept.json", "type": "codex"},
					{"name": "new-remote.json", "type": "codex"},
					{"name": "disabled-remote.json", "type": "codex", "disabled": true},
					{"name": "not-codex.json", "type": "other"},
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

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	cfg.CodexKeeper.AccountRefreshCacheMinutes = 10

	insertKeeperStateForCandidate(t, app, "kept.json", nil, nil)
	insertKeeperStateForCandidate(t, app, "stale-local.json", nil, nil)

	names, err := app.conditionalKeeperRefreshCandidates(ctx, cfg)
	if err != nil {
		t.Fatalf("conditionalKeeperRefreshCandidates: %v", err)
	}
	assertStringSet(t, names, []string{"new-remote.json"})
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_auth_states WHERE auth_name = 'kept.json'`); got != 1 {
		t.Fatalf("kept state rows = %d, want 1", got)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_auth_states WHERE auth_name = 'stale-local.json'`); got != 0 {
		t.Fatalf("stale state rows = %d, want 0", got)
	}
}

func TestKeeperQuotaWindowUsageAttributionPrefersSourceAccount(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := time.Date(2026, 5, 18, 12, 30, 0, 0, appTimeLocation)
	resetAt := now.Add(30 * time.Minute)
	windowSeconds := 3600
	accounts := []keeperAccount{
		{
			Name:                 "source.json",
			Email:                stringPtr("source@example.com"),
			AccountType:          stringPtr("plus"),
			PrimaryResetAt:       timePtrValue(resetAt),
			PrimaryWindowSeconds: intPtrValue(windowSeconds),
		},
		{
			Name:                 "auth.json",
			Email:                stringPtr("auth@example.com"),
			AccountType:          stringPtr("plus"),
			PrimaryResetAt:       timePtrValue(resetAt),
			PrimaryWindowSeconds: intPtrValue(windowSeconds),
		},
	}
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "source-wins",
		Timestamp:    now.Add(-10 * time.Minute),
		Source:       "source@example.com",
		AuthIndex:    "auth.json",
		InputTokens:  11,
		OutputTokens: 7,
		RawJSON:      `{"source":"source@example.com","auth_index":"auth.json"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "auth-fallback",
		Timestamp:    now.Add(-5 * time.Minute),
		Source:       "queue",
		AuthIndex:    "auth.json",
		InputTokens:  13,
		OutputTokens: 9,
		RawJSON:      `{"auth_index":"auth.json"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "unknown",
		Timestamp:    now.Add(-4 * time.Minute),
		Source:       "unknown@example.com",
		AuthIndex:    "auth.json",
		InputTokens:  17,
		OutputTokens: 3,
		RawJSON:      `{"source":"unknown@example.com","auth_index":"auth.json"}`,
	})

	usages, err := app.computeKeeperQuotaWindowUsages(context.Background(), accounts, now)
	if err != nil {
		t.Fatalf("compute window usages: %v", err)
	}
	if got := usages["source.json"].Primary.Records; got != 1 {
		t.Fatalf("source account records = %d, want 1", got)
	}
	if got := usages["source.json"].Primary.TotalTokens; got != 18 {
		t.Fatalf("source account tokens = %d, want 18", got)
	}
	if got := usages["auth.json"].Primary.Records; got != 1 {
		t.Fatalf("auth fallback records = %d, want 1", got)
	}
}

func TestKeeperQuotaWindowUsageAttributionUsesAuthIndexWhenSourceAccountIsShared(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := time.Date(2026, 5, 18, 12, 30, 0, 0, appTimeLocation)
	resetAt := now.Add(time.Hour)
	windowSeconds := keeperFiveHourWindowSeconds
	accounts := []keeperAccount{
		{
			Name:                 "shared-one.json",
			Email:                stringPtr("shared@example.com"),
			AuthIndex:            stringPtr("auth-one"),
			AccountType:          stringPtr("k12"),
			PrimaryResetAt:       timePtrValue(resetAt),
			PrimaryWindowSeconds: intPtrValue(windowSeconds),
		},
		{
			Name:                 "shared-two.json",
			Email:                stringPtr("shared@example.com"),
			AuthIndex:            stringPtr("auth-two"),
			AccountType:          stringPtr("k12"),
			PrimaryResetAt:       timePtrValue(resetAt),
			PrimaryWindowSeconds: intPtrValue(windowSeconds),
		},
	}
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "shared-one",
		Timestamp:    now.Add(-10 * time.Minute),
		Source:       "shared@example.com",
		AuthIndex:    "auth-one",
		InputTokens:  11,
		OutputTokens: 7,
		RawJSON:      `{"source":"shared@example.com","auth_index":"auth-one"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "shared-two",
		Timestamp:    now.Add(-5 * time.Minute),
		Source:       "shared@example.com",
		AuthIndex:    "auth-two",
		InputTokens:  13,
		OutputTokens: 9,
		RawJSON:      `{"source":"shared@example.com","auth_index":"auth-two"}`,
	})

	usages, err := app.computeKeeperQuotaWindowUsages(context.Background(), accounts, now)
	if err != nil {
		t.Fatalf("compute window usages: %v", err)
	}
	if got := usages["shared-one.json"].Primary.Records; got != 1 {
		t.Fatalf("shared-one records = %d, want 1", got)
	}
	if got := usages["shared-one.json"].Primary.TotalTokens; got != 18 {
		t.Fatalf("shared-one tokens = %d, want 18", got)
	}
	if got := usages["shared-two.json"].Primary.Records; got != 1 {
		t.Fatalf("shared-two records = %d, want 1", got)
	}
	if got := usages["shared-two.json"].Primary.TotalTokens; got != 22 {
		t.Fatalf("shared-two tokens = %d, want 22", got)
	}
}

func TestAccountTypeFromKeeperDetailNormalizesCodexProPlans(t *testing.T) {
	tests := []struct {
		name   string
		detail map[string]any
		usage  *keeperUsageInfo
		want   string
	}{
		{
			name:  "usage pro is pro 20x",
			usage: &keeperUsageInfo{PlanType: "pro"},
			want:  "pro_20x",
		},
		{
			name:  "usage prolite is pro 5x",
			usage: &keeperUsageInfo{PlanType: "prolite"},
			want:  "pro_5x",
		},
		{
			name:   "top level pro-lite is pro 5x",
			detail: map[string]any{"plan_type": "pro-lite"},
			want:   "pro_5x",
		},
		{
			name:   "nested id token plan is used",
			detail: map[string]any{"id_token": map[string]any{"plan_type": "pro_lite"}},
			want:   "pro_5x",
		},
		{
			name:  "usage k12 is k12",
			usage: &keeperUsageInfo{PlanType: "k12"},
			want:  "k12",
		},
		{
			name:   "nested id token k12 is used",
			detail: map[string]any{"id_token": map[string]any{"plan_type": "k12"}},
			want:   "k12",
		},
		{
			name:   "attributes plan is used",
			detail: map[string]any{"attributes": map[string]any{"plan_type": "pro"}},
			want:   "pro_20x",
		},
		{
			name:   "jwt chatgpt plan is used",
			detail: map[string]any{"metadata": map[string]any{"id_token": keeperTestCodexJWT(t, "prolite")}},
			want:   "pro_5x",
		},
		{
			name:   "pro file name suffix is fallback",
			detail: map[string]any{"name": "codex-user@example.com-pro.json"},
			want:   "pro_20x",
		},
		{
			name:   "prolite file name suffix is fallback",
			detail: map[string]any{"name": "codex-user@example.com-prolite.json"},
			want:   "pro_5x",
		},
		{
			name:   "legacy pro_20x remains supported",
			detail: map[string]any{"account_type": "pro_20x"},
			want:   "pro_20x",
		},
		{
			name:   "legacy pro_5x remains supported",
			detail: map[string]any{"account_type": "pro_5x"},
			want:   "pro_5x",
		},
		{
			name:   "plus remains supported",
			detail: map[string]any{"plan_type": "plus"},
			want:   "plus",
		},
		{
			name:   "unknown stays nil",
			detail: map[string]any{"name": "codex-user@example.com.json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountTypeFromKeeperDetail(tt.detail, tt.usage)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("accountTypeFromKeeperDetail() = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("accountTypeFromKeeperDetail() = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultKeeperPriorityRulesIncludeK12WithoutUnknown(t *testing.T) {
	rules := normalizePriorityRules(nil)
	if rules["k12"] != 2 {
		t.Fatalf("k12 priority = %d, want 2", rules["k12"])
	}
	if _, ok := rules["unknown"]; ok {
		t.Fatal("unknown priority rule should not be added")
	}
	if got := keeperPriorityForType(nil, rules); got != nil {
		t.Fatalf("nil account type priority = %v, want nil", got)
	}
}

func TestKeeperRunStoresRemoteAuthIndex(t *testing.T) {
	tests := []struct {
		name             string
		detailAuthFields map[string]any
		seedAuthIndex    string
		wantAuthIndex    *string
	}{
		{
			name:             "detail alias replaces list value",
			detailAuthFields: map[string]any{"authIndex": "detail-auth"},
			wantAuthIndex:    stringPtr("detail-auth"),
		},
		{
			name:          "detail omission clears stored value",
			seedAuthIndex: "stored-auth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

			cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"files": []map[string]any{{"name": "stored.json", "type": "codex", "auth_index": "list-auth"}},
					})
				case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
					detail := map[string]any{
						"name":         "stored.json",
						"type":         "codex",
						"email":        "stored@example.com",
						"account_type": "free",
						"disabled":     false,
						"priority":     0,
						"access_token": "test-token",
					}
					for key, value := range tt.detailAuthFields {
						detail[key] = value
					}
					_ = json.NewEncoder(w).Encode(detail)
				case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
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
			configureKeeperTestCPA(t, app, cpa.URL, nil)
			if tt.seedAuthIndex != "" {
				insertKeeperStateForCandidate(t, app, "stored.json", nil, nil)
				if _, err := app.db.Exec(`
					UPDATE codex_keeper_auth_states
					SET auth_index = ?
					WHERE auth_name = ?
				`, tt.seedAuthIndex, "stored.json"); err != nil {
					t.Fatalf("seed stored auth index: %v", err)
				}
			}

			if _, _, err := app.executeKeeperRunForAccounts(context.Background(), "manual", []string{"stored.json"}, func(string) {}); err != nil {
				t.Fatalf("keeper run: %v", err)
			}
			state, err := app.getKeeperState(context.Background(), "stored.json")
			if err != nil {
				t.Fatalf("get keeper state: %v", err)
			}
			if tt.wantAuthIndex == nil {
				if state.AuthIndex != nil {
					t.Fatalf("auth_index = %v, want nil", state.AuthIndex)
				}
				return
			}
			if state.AuthIndex == nil || *state.AuthIndex != *tt.wantAuthIndex {
				t.Fatalf("auth_index = %v, want %q", state.AuthIndex, *tt.wantAuthIndex)
			}
		})
	}
}

func keeperTestCodexJWT(t *testing.T, planType string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal test jwt payload: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestKeeperQuotaWindowUsageInfersAccountWindows(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 30, 0, 0, appTimeLocation)
	resetAt := now.Add(30 * time.Minute)

	freePair := keeperQuotaWindowPairForAccount(keeperAccount{
		Name:           "free.json",
		AccountType:    stringPtr("free"),
		PrimaryResetAt: timePtrValue(resetAt),
	}, now)
	if freePair.Primary == nil {
		t.Fatal("free primary window is nil, want monthly window")
	}
	if freePair.Primary.WindowSeconds != keeperMonthWindowSeconds || freePair.Primary.WindowSource != "inferred" {
		t.Fatalf("free window = %d/%s, want inferred monthly", freePair.Primary.WindowSeconds, freePair.Primary.WindowSource)
	}
	if freePair.Secondary != nil {
		t.Fatal("free secondary window is not nil, want single monthly window")
	}

	plusPair := keeperQuotaWindowPairForAccount(keeperAccount{
		Name:             "plus.json",
		AccountType:      stringPtr("plus"),
		PrimaryResetAt:   timePtrValue(resetAt),
		SecondaryResetAt: timePtrValue(resetAt.Add(2 * time.Hour)),
	}, now)
	if plusPair.Primary == nil || plusPair.Primary.WindowSeconds != keeperFiveHourWindowSeconds {
		t.Fatalf("plus primary window = %#v, want inferred 5h", plusPair.Primary)
	}
	if plusPair.Secondary == nil || plusPair.Secondary.WindowSeconds != keeperWeekWindowSeconds {
		t.Fatalf("plus secondary window = %#v, want inferred weekly", plusPair.Secondary)
	}

	k12Pair := keeperQuotaWindowPairForAccount(keeperAccount{
		Name:             "k12.json",
		AccountType:      stringPtr("k12"),
		PrimaryResetAt:   timePtrValue(resetAt),
		SecondaryResetAt: timePtrValue(resetAt.Add(2 * time.Hour)),
	}, now)
	if k12Pair.Primary == nil || k12Pair.Primary.WindowSeconds != keeperFiveHourWindowSeconds {
		t.Fatalf("k12 primary window = %#v, want inferred 5h", k12Pair.Primary)
	}
	if k12Pair.Secondary == nil || k12Pair.Secondary.WindowSeconds != keeperWeekWindowSeconds {
		t.Fatalf("k12 secondary window = %#v, want inferred weekly", k12Pair.Secondary)
	}

	usage := parseKeeperUsageInfo(map[string]any{
		"plan_type": "plus",
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":         20,
				"limit_window_seconds": float64(1234),
			},
			"secondary_window": map[string]any{
				"used_percent":         40,
				"limit_window_seconds": float64(5678),
			},
		},
	})
	if usage.PrimaryWindowSeconds == nil || *usage.PrimaryWindowSeconds != 1234 {
		t.Fatalf("primary limit_window_seconds = %v, want 1234", usage.PrimaryWindowSeconds)
	}
	if usage.SecondaryWindowSeconds == nil || *usage.SecondaryWindowSeconds != 5678 {
		t.Fatalf("secondary limit_window_seconds = %v, want 5678", usage.SecondaryWindowSeconds)
	}
	if usage.PrimaryUsedPercent == nil || *usage.PrimaryUsedPercent != 20 {
		t.Fatalf("primary used percent = %v, want 20", usage.PrimaryUsedPercent)
	}
	camelUsage := parseKeeperUsageInfo(map[string]any{"planType": "pro"})
	if camelUsage.PlanType != "pro" {
		t.Fatalf("camel planType = %q, want pro", camelUsage.PlanType)
	}
}

func TestKeeperQuotaWindowNormalization(t *testing.T) {
	primaryReset := time.Date(2026, 7, 23, 1, 0, 0, 0, appTimeLocation)
	secondaryReset := primaryReset.Add(2 * time.Hour)

	t.Run("weekly only plus moves to secondary", func(t *testing.T) {
		usage := normalizeKeeperUsageInfoWindows(keeperUsageInfo{
			PrimaryUsedPercent:   intPtrValue(0),
			PrimaryResetAt:       timePtrValue(primaryReset),
			PrimaryWindowSeconds: intPtrValue(keeperWeekWindowSeconds),
		}, stringPtr("plus"))
		if usage.PrimaryUsedPercent != nil || usage.PrimaryResetAt != nil || usage.PrimaryWindowSeconds != nil {
			t.Fatalf("primary window = %#v, want empty", usage)
		}
		if usage.SecondaryUsedPercent == nil || *usage.SecondaryUsedPercent != 0 {
			t.Fatalf("secondary used percent = %v, want 0", usage.SecondaryUsedPercent)
		}
		if usage.SecondaryResetAt == nil || !usage.SecondaryResetAt.Equal(primaryReset) {
			t.Fatalf("secondary reset = %v, want %v", usage.SecondaryResetAt, primaryReset)
		}
		if usage.SecondaryWindowSeconds == nil || *usage.SecondaryWindowSeconds != keeperWeekWindowSeconds {
			t.Fatalf("secondary seconds = %v, want weekly", usage.SecondaryWindowSeconds)
		}
	})

	t.Run("known reversed windows are reordered", func(t *testing.T) {
		usage := normalizeKeeperUsageInfoWindows(keeperUsageInfo{
			PrimaryUsedPercent:     intPtrValue(40),
			PrimaryResetAt:         timePtrValue(primaryReset),
			PrimaryWindowSeconds:   intPtrValue(keeperWeekWindowSeconds),
			SecondaryUsedPercent:   intPtrValue(20),
			SecondaryResetAt:       timePtrValue(secondaryReset),
			SecondaryWindowSeconds: intPtrValue(keeperFiveHourWindowSeconds),
		}, stringPtr("plus"))
		if usage.PrimaryUsedPercent == nil || *usage.PrimaryUsedPercent != 20 || usage.PrimaryResetAt == nil || !usage.PrimaryResetAt.Equal(secondaryReset) {
			t.Fatalf("primary window = %#v, want five-hour source window", usage)
		}
		if usage.SecondaryUsedPercent == nil || *usage.SecondaryUsedPercent != 40 || usage.SecondaryResetAt == nil || !usage.SecondaryResetAt.Equal(primaryReset) {
			t.Fatalf("secondary window = %#v, want weekly source window", usage)
		}
	})

	t.Run("missing durations preserve legacy order", func(t *testing.T) {
		usage := normalizeKeeperUsageInfoWindows(keeperUsageInfo{
			PrimaryUsedPercent:   intPtrValue(10),
			SecondaryUsedPercent: intPtrValue(30),
		}, stringPtr("plus"))
		if usage.PrimaryUsedPercent == nil || *usage.PrimaryUsedPercent != 10 || usage.SecondaryUsedPercent == nil || *usage.SecondaryUsedPercent != 30 {
			t.Fatalf("usage = %#v, want original slot order", usage)
		}
	})

	t.Run("free monthly window moves to primary", func(t *testing.T) {
		usage := normalizeKeeperUsageInfoWindows(keeperUsageInfo{
			SecondaryUsedPercent:   intPtrValue(15),
			SecondaryResetAt:       timePtrValue(secondaryReset),
			SecondaryWindowSeconds: intPtrValue(keeperMonthWindowSeconds),
		}, stringPtr("free"))
		if usage.PrimaryUsedPercent == nil || *usage.PrimaryUsedPercent != 15 || usage.PrimaryWindowSeconds == nil || *usage.PrimaryWindowSeconds != keeperMonthWindowSeconds {
			t.Fatalf("primary window = %#v, want monthly source window", usage)
		}
		if usage.SecondaryUsedPercent != nil || usage.SecondaryResetAt != nil || usage.SecondaryWindowSeconds != nil {
			t.Fatalf("secondary window = %#v, want empty", usage)
		}
	})

	t.Run("unknown durations are retained", func(t *testing.T) {
		usage := normalizeKeeperUsageInfoWindows(keeperUsageInfo{
			PrimaryUsedPercent:     intPtrValue(11),
			PrimaryWindowSeconds:   intPtrValue(1234),
			SecondaryUsedPercent:   intPtrValue(22),
			SecondaryWindowSeconds: intPtrValue(5678),
		}, stringPtr("plus"))
		if usage.PrimaryWindowSeconds == nil || *usage.PrimaryWindowSeconds != 1234 || usage.SecondaryWindowSeconds == nil || *usage.SecondaryWindowSeconds != 5678 {
			t.Fatalf("usage = %#v, want unknown durations in original slots", usage)
		}
	})
}

func TestKeeperStoredQuotaWindowNormalization(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	resetAt := time.Date(2026, 7, 23, 1, 0, 0, 0, appTimeLocation)
	insertKeeperStateForCandidate(t, app, "weekly-only.json", timePtrValue(resetAt), nil)
	_, err = app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET account_type = ?, primary_used_percent = ?, primary_window_seconds = ?, updated_at = ?
		WHERE auth_name = ?
	`, "plus", 0, keeperWeekWindowSeconds, dbTime(time.Now().In(appTimeLocation)), "weekly-only.json")
	if err != nil {
		t.Fatalf("seed weekly-only keeper state: %v", err)
	}

	state, err := app.getKeeperState(context.Background(), "weekly-only.json")
	if err != nil {
		t.Fatalf("get keeper state: %v", err)
	}
	if state.PrimaryUsedPercent != nil || state.PrimaryResetAt != nil || state.PrimaryWindowSeconds != nil {
		t.Fatalf("primary state = %#v, want empty", state.keeperAccount)
	}
	if state.SecondaryUsedPercent == nil || *state.SecondaryUsedPercent != 0 {
		t.Fatalf("secondary used percent = %v, want 0", state.SecondaryUsedPercent)
	}
	if state.SecondaryResetAt == nil || !state.SecondaryResetAt.Equal(resetAt) {
		t.Fatalf("secondary reset = %v, want %v", state.SecondaryResetAt, resetAt)
	}
	if state.SecondaryWindowSeconds == nil || *state.SecondaryWindowSeconds != keeperWeekWindowSeconds {
		t.Fatalf("secondary seconds = %v, want weekly", state.SecondaryWindowSeconds)
	}
}

func TestKeeperQuotaWindowUsageUsesCurrentWindowBoundariesAndPricing(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/codex-api-key": []map[string]any{
			{
				"api-key":    "codex-priced-key",
				"auth-index": "priced.json",
				"models":     []map[string]any{{"name": "gpt-test"}},
			},
		},
	})
	defer cpa.Close()
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if err := app.refreshModelPriceSelectorsIfStale(ctx, cfg); err != nil {
		t.Fatalf("refresh selectors failed: %v", err)
	}

	insertKeeperTestPrice(t, app)
	now := time.Date(2026, 5, 18, 12, 30, 0, 0, appTimeLocation)
	resetAt := time.Date(2026, 5, 18, 13, 0, 0, 0, appTimeLocation)
	windowSeconds := 3600
	windowStart := resetAt.Add(-time.Duration(windowSeconds) * time.Second)
	accounts := []keeperAccount{
		{
			Name:                 "priced.json",
			Email:                stringPtr("priced@example.com"),
			AccountType:          stringPtr("plus"),
			PrimaryUsedPercent:   intPtrValue(100),
			PrimaryResetAt:       timePtrValue(resetAt),
			PrimaryWindowSeconds: intPtrValue(windowSeconds),
		},
	}
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "at-start",
		Timestamp:    windowStart,
		Source:       "priced@example.com",
		AuthIndex:    "priced.json",
		InputTokens:  10,
		OutputTokens: 5,
		RawJSON:      `{"source":"priced@example.com"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "near-before-start",
		Timestamp:    windowStart.Add(-3 * time.Second),
		Source:       "priced@example.com",
		AuthIndex:    "priced.json",
		InputTokens:  4,
		OutputTokens: 1,
		RawJSON:      `{"source":"priced@example.com"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "before-end",
		Timestamp:    resetAt.Add(-time.Second),
		Source:       "priced@example.com",
		AuthIndex:    "priced.json",
		Failed:       true,
		InputTokens:  20,
		OutputTokens: 10,
		RawJSON:      `{"source":"priced@example.com"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "at-end",
		Timestamp:    resetAt,
		Source:       "priced@example.com",
		AuthIndex:    "priced.json",
		InputTokens:  100,
		OutputTokens: 100,
		RawJSON:      `{"source":"priced@example.com"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "before-start",
		Timestamp:    windowStart.Add(-time.Minute),
		Source:       "priced@example.com",
		AuthIndex:    "priced.json",
		InputTokens:  100,
		OutputTokens: 100,
		RawJSON:      `{"source":"priced@example.com"}`,
	})

	usages, err := app.computeKeeperQuotaWindowUsages(ctx, accounts, now)
	if err != nil {
		t.Fatalf("compute window usages: %v", err)
	}
	usage := usages["priced.json"].Primary
	if usage == nil {
		t.Fatal("primary window usage is nil")
	}
	if usage.Records != 2 || usage.SuccessRecords != 1 || usage.FailedRecords != 1 {
		t.Fatalf("records = %d/%d/%d, want 2/1/1", usage.Records, usage.SuccessRecords, usage.FailedRecords)
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 15 || usage.TotalTokens != 45 {
		t.Fatalf("tokens = input %d output %d total %d, want 30/15/45", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
	}
	if math.Abs(usage.EstimatedCostUSD-0.00006) > 0.00000001 {
		t.Fatalf("estimated cost = %.8f, want 0.00006000", usage.EstimatedCostUSD)
	}
	if usage.UnpricedRecords != 0 {
		t.Fatalf("unpriced records = %d, want 0", usage.UnpricedRecords)
	}
}

func TestKeeperQuotaWindowUsageUsesFreeMonthlyWindowBoundaries(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := time.Date(2026, 5, 18, 12, 30, 0, 0, appTimeLocation)
	resetAt := now.Add(time.Hour)
	windowStart := resetAt.Add(-time.Duration(keeperMonthWindowSeconds) * time.Second)
	accounts := []keeperAccount{
		{
			Name:               "free-month.json",
			Email:              stringPtr("free-month@example.com"),
			AccountType:        stringPtr("free"),
			PrimaryUsedPercent: intPtrValue(0),
			PrimaryResetAt:     timePtrValue(resetAt),
		},
	}
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "inside-month-outside-week",
		Timestamp:    now.Add(-8 * 24 * time.Hour),
		Source:       "free-month@example.com",
		InputTokens:  20,
		OutputTokens: 10,
		RawJSON:      `{"source":"free-month@example.com"}`,
	})
	insertKeeperWindowUsageRecord(t, app, keeperWindowUsageSeed{
		Dedupe:       "previous-cycle-boundary",
		Timestamp:    windowStart.Add(-3 * time.Second),
		Source:       "free-month@example.com",
		InputTokens:  100,
		OutputTokens: 50,
		RawJSON:      `{"source":"free-month@example.com"}`,
	})

	usages, err := app.computeKeeperQuotaWindowUsages(context.Background(), accounts, now)
	if err != nil {
		t.Fatalf("compute window usages: %v", err)
	}
	usage := usages["free-month.json"].Primary
	if usage == nil {
		t.Fatal("primary window usage is nil")
	}
	if usage.WindowSeconds != keeperMonthWindowSeconds {
		t.Fatalf("window seconds = %d, want monthly", usage.WindowSeconds)
	}
	if usage.Records != 1 || usage.TotalTokens != 30 {
		t.Fatalf("usage = records %d tokens %d, want monthly-window record only", usage.Records, usage.TotalTokens)
	}
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
	if stats.Healthy != 1 {
		t.Fatalf("daemon healthy = %d, want clean cached checked state to count", stats.Healthy)
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

func TestKeeperCredentialWebsocketsAppliesToRefreshModes(t *testing.T) {
	modes := []struct {
		name      string
		mode      string
		authNames []string
	}{
		{name: "daemon", mode: "daemon"},
		{name: "run-once", mode: "once"},
		{name: "accounts", mode: "accounts", authNames: []string{"accounts-auth.json"}},
		{name: "conditional", mode: "conditional", authNames: []string{"conditional-auth.json"}},
	}

	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

			authName := tc.name + "-auth.json"
			if len(tc.authNames) > 0 {
				authName = tc.authNames[0]
			}
			websocketPatches := 0
			priorityPatches := 0
			authDetail := map[string]any{
				"name":         authName,
				"type":         "codex",
				"email":        "ws@example.com",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
				"websockets":   false,
			}
			cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"files": []map[string]any{{"name": authName, "type": "codex", "websockets": false}},
					})
				case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
					if r.URL.Query().Get("name") != authName {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(authDetail)
				case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
					_ = json.NewEncoder(w).Encode(keeperWebsocketUsageSuccessPayload(10))
				case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if payload["name"] != authName {
						http.Error(w, "unexpected auth name", http.StatusBadRequest)
						return
					}
					if value, ok := payload["websockets"].(bool); ok && value {
						websocketPatches++
						authDetail["websockets"] = true
						_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
						return
					}
					if _, ok := payload["priority"]; ok {
						priorityPatches++
						_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
						return
					}
					http.Error(w, "missing supported field", http.StatusBadRequest)
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
				cfg.CodexKeeper.EnableCredentialWebsockets = true
				cfg.CodexKeeper.WorkerThreads = 1
			})

			stats, _, err := app.executeKeeperRunForAccounts(context.Background(), tc.mode, tc.authNames, func(string) {})
			if err != nil {
				t.Fatalf("%s run: %v", tc.mode, err)
			}
			if websocketPatches != 1 {
				t.Fatalf("websocket patches = %d, want 1", websocketPatches)
			}
			if priorityPatches != 0 {
				t.Fatalf("priority patches = %d, want 0", priorityPatches)
			}
			if stats.Total != 1 || stats.Healthy != 1 || stats.NetworkError != 0 {
				t.Fatalf("stats = %#v, want one healthy account", stats)
			}
		})
	}
}

func TestKeeperCredentialWebsocketsRespectsDryRun(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	websocketPatches := 0
	authName := "dry-run-auth.json"
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": authName, "type": "codex", "websockets": false}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         authName,
				"type":         "codex",
				"account_type": "free",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
				"websockets":   false,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			_ = json.NewEncoder(w).Encode(keeperWebsocketUsageSuccessPayload(10))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			websocketPatches++
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
		cfg.CodexKeeper.DryRun = true
		cfg.CodexKeeper.EnableCredentialWebsockets = true
	})

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "once", nil, func(string) {})
	if err != nil {
		t.Fatalf("once run: %v", err)
	}
	if websocketPatches != 0 {
		t.Fatalf("websocket patches = %d, want 0 in dry run", websocketPatches)
	}
	if stats.Total != 1 || stats.Healthy != 1 {
		t.Fatalf("stats = %#v, want one healthy account", stats)
	}
}

func TestAutomaticKeeperRunCountsCachedBadCredentialState(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	downloadCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "cached-bad.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			downloadCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{})
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
	checkedAt := time.Now().In(appTimeLocation).Add(-time.Minute)
	insertKeeperStateForCandidate(t, app, "cached-bad.json", nil, timePtrValue(checkedAt))
	if _, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET disabled = 1, last_status_code = 401, last_error = ?, latest_action = ?
		WHERE auth_name = ?
	`, "凭证不可用：HTTP 401", "禁用凭证：凭证不可用：HTTP 401", "cached-bad.json"); err != nil {
		t.Fatalf("mark cached bad credential state: %v", err)
	}

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "daemon", nil, func(string) {})
	if err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("daemon skipped = %d, want 1", stats.Skipped)
	}
	if stats.StatusDisabled != 1 {
		t.Fatalf("daemon status_disabled = %d, want cached bad credential to count", stats.StatusDisabled)
	}
	if stats.Healthy != 0 {
		t.Fatalf("daemon healthy = %d, want 0", stats.Healthy)
	}
	if downloadCalls != 0 {
		t.Fatalf("download calls = %d, want 0", downloadCalls)
	}
}

func TestAutomaticKeeperRunReenablesRecoverableUnauthorizedDisabledAccount(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	const authName = "recoverable-auto.json"
	cpa := newKeeperRecoveryTestCPA(t, map[string]map[string]any{
		authName: keeperRecoveryAuthDetail(authName, true),
	}, map[string]int{authName: http.StatusOK})
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL(), func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
		cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	})
	markKeeperStateRecoverableUnauthorizedDisabled(t, app, authName, timePtrValue(time.Now().In(appTimeLocation).Add(-20*time.Minute)))

	stats, detail, err := app.executeKeeperRunForAccounts(context.Background(), "daemon", nil, func(string) {})
	if err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	if stats.StatusEnabled != 1 {
		t.Fatalf("status_enabled = %d, want 1", stats.StatusEnabled)
	}
	if !strings.Contains(detail, "恢复启用 1") {
		t.Fatalf("detail = %q, want recovery count", detail)
	}
	assertKeeperRecoveredState(t, app, authName)
	if got := cpa.statusPatchCount(authName, false); got != 1 {
		t.Fatalf("enable status patch count = %d, want 1", got)
	}
	if got := cpa.usageCallCount(authName); got != 1 {
		t.Fatalf("usage calls = %d, want 1", got)
	}
}

func TestManualKeeperRefreshReenablesRecoverableUnauthorizedDisabledAccount(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	const authName = "recoverable-manual.json"
	cpa := newKeeperRecoveryTestCPA(t, map[string]map[string]any{
		authName: keeperRecoveryAuthDetail(authName, true),
	}, map[string]int{authName: http.StatusOK})
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL(), func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
		cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	})
	markKeeperStateRecoverableUnauthorizedDisabled(t, app, authName, timePtrValue(time.Now().In(appTimeLocation)))

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "accounts", []string{authName}, func(string) {})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if stats.StatusEnabled != 1 {
		t.Fatalf("status_enabled = %d, want 1", stats.StatusEnabled)
	}
	assertKeeperRecoveredState(t, app, authName)
	if got := cpa.statusPatchCount(authName, false); got != 1 {
		t.Fatalf("enable status patch count = %d, want 1", got)
	}
}

func TestConditionalKeeperRefreshSkipsDisabledAccounts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	const oldRecoverable = "recoverable-conditional.json"
	const recentRecoverable = "recoverable-cached.json"
	const manualDisabled = "manual-disabled.json"
	cpa := newKeeperRecoveryTestCPA(t, map[string]map[string]any{
		oldRecoverable:    keeperRecoveryAuthDetail(oldRecoverable, true),
		recentRecoverable: keeperRecoveryAuthDetail(recentRecoverable, true),
		manualDisabled:    keeperRecoveryAuthDetail(manualDisabled, true),
	}, map[string]int{
		oldRecoverable:    http.StatusOK,
		recentRecoverable: http.StatusOK,
		manualDisabled:    http.StatusOK,
	})
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL(), func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
		cfg.CodexKeeper.AccountRefreshCacheMinutes = 10
	})
	now := time.Now().In(appTimeLocation)
	markKeeperStateRecoverableUnauthorizedDisabled(t, app, oldRecoverable, timePtrValue(now.Add(-20*time.Minute)))
	markKeeperStateRecoverableUnauthorizedDisabled(t, app, recentRecoverable, timePtrValue(now.Add(-time.Minute)))
	insertKeeperStateForCandidate(t, app, manualDisabled, nil, timePtrValue(now.Add(-20*time.Minute)))
	markKeeperStateDisabled(t, app, manualDisabled)

	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	names, err := app.conditionalKeeperRefreshCandidates(context.Background(), cfg)
	if err != nil {
		t.Fatalf("conditionalKeeperRefreshCandidates: %v", err)
	}
	assertStringSet(t, names, nil)
	assertKeeperStillDisabled(t, app, oldRecoverable)
	if got := cpa.usageCallCount(recentRecoverable); got != 0 {
		t.Fatalf("recent recoverable usage calls = %d, want 0 because cache skipped", got)
	}
	if got := cpa.usageCallCount(oldRecoverable); got != 0 {
		t.Fatalf("old recoverable usage calls = %d, want 0 because conditional refresh skips disabled accounts", got)
	}
	if got := cpa.usageCallCount(manualDisabled); got != 0 {
		t.Fatalf("manual disabled usage calls = %d, want 0", got)
	}
	if got := cpa.statusPatchCount(oldRecoverable, false); got != 0 {
		t.Fatalf("enable status patch count = %d, want 0", got)
	}
}

func TestManualKeeperRefreshDoesNotReenableNonRecoverableDisabledAccounts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	const manualDisabled = "manual-disabled-refresh.json"
	const paymentDisabled = "payment-disabled-refresh.json"
	cpa := newKeeperRecoveryTestCPA(t, map[string]map[string]any{
		manualDisabled:  keeperRecoveryAuthDetail(manualDisabled, true),
		paymentDisabled: keeperRecoveryAuthDetail(paymentDisabled, true),
	}, map[string]int{
		manualDisabled:  http.StatusOK,
		paymentDisabled: http.StatusOK,
	})
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL(), func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
	})
	insertKeeperStateForCandidate(t, app, manualDisabled, nil, nil)
	markKeeperStateDisabled(t, app, manualDisabled)
	insertKeeperStateForCandidate(t, app, paymentDisabled, nil, nil)
	markKeeperStatePaymentRequiredDisabled(t, app, paymentDisabled)

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "accounts", []string{manualDisabled, paymentDisabled}, func(string) {})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if stats.StatusEnabled != 0 {
		t.Fatalf("status_enabled = %d, want 0", stats.StatusEnabled)
	}
	assertKeeperStillDisabled(t, app, manualDisabled)
	assertKeeperStillDisabled(t, app, paymentDisabled)
	if got := cpa.statusPatchCount(manualDisabled, false) + cpa.statusPatchCount(paymentDisabled, false); got != 0 {
		t.Fatalf("enable status patch count = %d, want 0", got)
	}
	if got := cpa.usageCallCount(manualDisabled); got != 1 {
		t.Fatalf("manual disabled usage calls = %d, want 1", got)
	}
	if got := cpa.usageCallCount(paymentDisabled); got != 1 {
		t.Fatalf("payment disabled usage calls = %d, want 1", got)
	}
}

func TestKeeperRunKeepsRecoverableUnauthorizedAccountDisabledWhenStillUnauthorized(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	const authName = "still-unauthorized.json"
	cpa := newKeeperRecoveryTestCPA(t, map[string]map[string]any{
		authName: keeperRecoveryAuthDetail(authName, true),
	}, map[string]int{authName: http.StatusUnauthorized})
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	configureKeeperTestCPA(t, app, cpa.URL(), func(cfg *AppConfig) {
		cfg.CodexKeeper.DryRun = false
	})
	markKeeperStateRecoverableUnauthorizedDisabled(t, app, authName, timePtrValue(time.Now().In(appTimeLocation).Add(-20*time.Minute)))

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "accounts", []string{authName}, func(string) {})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if stats.StatusDisabled != 1 {
		t.Fatalf("status_disabled = %d, want 1", stats.StatusDisabled)
	}
	assertKeeperStillDisabled(t, app, authName)
	state, err := app.getKeeperState(context.Background(), authName)
	if err != nil {
		t.Fatalf("get keeper state: %v", err)
	}
	if state.LastStatusCode == nil || *state.LastStatusCode != http.StatusUnauthorized {
		t.Fatalf("last_status_code = %v, want 401", state.LastStatusCode)
	}
	if state.LastError == nil || !strings.Contains(*state.LastError, "凭证不可用") {
		t.Fatalf("last_error = %v, want credential error", state.LastError)
	}
	if state.LatestAction == nil || !strings.Contains(*state.LatestAction, "禁用凭证") {
		t.Fatalf("latest_action = %v, want keeper disable action", state.LatestAction)
	}
	if got := cpa.statusPatchCount(authName, false); got != 0 {
		t.Fatalf("enable status patch count = %d, want 0", got)
	}
}

func TestKeeperAuthDetailRequestFailureCountsAsNetworkError(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	usageCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"name": "download-fails.json", "type": "codex"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			http.Error(w, "temporary management failure", http.StatusBadGateway)
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
	insertKeeperStateForCandidate(t, app, "download-fails.json", nil, nil)
	if _, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET auth_index = ?
		WHERE auth_name = ?
	`, "stored-auth", "download-fails.json"); err != nil {
		t.Fatalf("seed stored auth index: %v", err)
	}

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "daemon", nil, func(string) {})
	if err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	if stats.NetworkError != 1 {
		t.Fatalf("network_error = %d, want 1", stats.NetworkError)
	}
	if stats.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", stats.Skipped)
	}
	if stats.Healthy != 0 || stats.StatusDisabled != 0 {
		t.Fatalf("stats = %#v, want only network error", stats)
	}
	if usageCalls != 0 {
		t.Fatalf("usage calls = %d, want 0", usageCalls)
	}
	state, err := app.getKeeperState(context.Background(), "download-fails.json")
	if err != nil {
		t.Fatalf("get keeper state: %v", err)
	}
	if state.LastError == nil || !strings.Contains(*state.LastError, "读取 auth file 详情失败") {
		t.Fatalf("last_error = %v, want auth detail failure", state.LastError)
	}
	if state.AuthIndex == nil || *state.AuthIndex != "stored-auth" {
		t.Fatalf("auth_index = %v, want stored-auth preserved after detail failure", state.AuthIndex)
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
				"account_type": "plus",
				"disabled":     false,
				"priority":     0,
				"access_token": "test-token",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body": map[string]any{
					"plan_type": "plus",
					"rate_limit": map[string]any{
						"primary_window": map[string]any{
							"used_percent":         100,
							"reset_after_seconds":  3600,
							"limit_window_seconds": keeperWeekWindowSeconds,
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
	state, err := app.getKeeperState(context.Background(), "quota.json")
	if err != nil {
		t.Fatalf("get normalized keeper state: %v", err)
	}
	if state.PrimaryUsedPercent != nil || state.SecondaryUsedPercent == nil || *state.SecondaryUsedPercent != 100 {
		t.Fatalf("stored quota windows = %#v, want weekly usage in secondary", state.keeperAccount)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_runs`); got != 0 {
		t.Fatalf("keeper run rows = %d, want 0 because conditional refresh is not persisted", got)
	}
}

func TestManualKeeperRefreshUsesAutomaticPriorityPolicy(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	authDetails := map[string]map[string]any{
		"quota.json": {
			"name":         "quota.json",
			"type":         "codex",
			"account_type": "free",
			"disabled":     false,
			"priority":     0,
			"access_token": "test-token",
		},
		"default.json": {
			"name":         "default.json",
			"type":         "codex",
			"account_type": "plus",
			"disabled":     false,
			"priority":     0,
			"access_token": "test-token",
		},
		"restore.json": {
			"name":         "restore.json",
			"type":         "codex",
			"account_type": "plus",
			"disabled":     false,
			"priority":     -1,
			"access_token": "test-token",
		},
	}
	usagePercents := map[string]int{
		"quota.json":   100,
		"default.json": 10,
		"restore.json": 10,
	}
	priorityPatches := map[string][]int{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"name": "quota.json", "type": "codex"},
					{"name": "default.json", "type": "codex"},
					{"name": "restore.json", "type": "codex"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			detail, ok := authDetails[r.URL.Query().Get("name")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(detail)
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			var payload struct {
				AuthIndex string `json:"auth_index"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			usedPercent := usagePercents[payload.AuthIndex]
			planType, _ := authDetails[payload.AuthIndex]["account_type"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body": map[string]any{
					"plan_type": planType,
					"rate_limit": map[string]any{
						"primary_window": map[string]any{
							"used_percent":        usedPercent,
							"reset_after_seconds": 3600,
						},
					},
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			var payload struct {
				Name     string `json:"name"`
				Priority *int   `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Priority == nil {
				http.Error(w, "priority is required", http.StatusBadRequest)
				return
			}
			priorityPatches[payload.Name] = append(priorityPatches[payload.Name], *payload.Priority)
			authDetails[payload.Name]["priority"] = *payload.Priority
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
	insertKeeperStateForCandidate(t, app, "restore.json", nil, nil)
	_, err = app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET restore_priority = ?, updated_at = ?
		WHERE auth_name = ?
	`, 21, dbTime(time.Now().In(appTimeLocation)), "restore.json")
	if err != nil {
		t.Fatalf("seed restore priority: %v", err)
	}

	stats, _, err := app.executeKeeperRunForAccounts(context.Background(), "accounts", []string{"quota.json", "default.json", "restore.json"}, func(string) {})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if stats.PriorityDegraded != 1 {
		t.Fatalf("priority_degraded = %d, want 1", stats.PriorityDegraded)
	}
	if stats.PriorityRestored != 2 {
		t.Fatalf("priority_restored = %d, want 2", stats.PriorityRestored)
	}
	expectedPatches := map[string][]int{
		"quota.json":   {-1},
		"default.json": {4},
		"restore.json": {21},
	}
	if !reflect.DeepEqual(priorityPatches, expectedPatches) {
		t.Fatalf("priority patches = %#v, want %#v", priorityPatches, expectedPatches)
	}
	if got := countKeeperRows(t, app, `SELECT COUNT(*) FROM codex_keeper_runs`); got != 0 {
		t.Fatalf("keeper run rows = %d, want 0 because account refresh is not persisted", got)
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

type keeperWindowUsageSeed struct {
	Dedupe       string
	Timestamp    time.Time
	Source       string
	AuthIndex    string
	Failed       bool
	InputTokens  int
	OutputTokens int
	RawJSON      string
}

func insertKeeperWindowUsageRecord(t *testing.T, app *App, seed keeperWindowUsageSeed) {
	t.Helper()
	now := dbTime(time.Now().In(appTimeLocation))
	source := seed.Source
	if strings.TrimSpace(source) == "" {
		source = "test"
	}
	rawJSON := seed.RawJSON
	if strings.TrimSpace(rawJSON) == "" {
		rawJSON = `{}`
	}
	authIndex := strings.TrimSpace(seed.AuthIndex)
	sourceAccount := sourceAccountFromUsageSource(&source)
	inputTokens := seed.InputTokens
	outputTokens := seed.OutputTokens
	totalTokens := inputTokens + outputTokens
	_, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider,
			model, endpoint, source, source_account, request_id, auth, auth_index, latency_ms,
			failed, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
			total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, NULL, NULL, 'codex', 'gpt-test', '/v1/responses',
			?, ?, ?, 'api_key', ?, 10, ?, ?, ?, 0, 0, ?, ?, ?)
	`, now, dbTime(seed.Timestamp), source, nullableTestString(sourceAccount), seed.Dedupe, nullableBlankTestString(authIndex), seed.Failed, inputTokens, outputTokens, totalTokens, "quota-"+seed.Dedupe, rawJSON)
	if err != nil {
		t.Fatalf("insert quota usage record %s: %v", seed.Dedupe, err)
	}
}

func insertKeeperTestPrice(t *testing.T, app *App) {
	t.Helper()
	_, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source, updated_at
		) VALUES ('codex', 'gpt-test', 'channel', 'codex', 'priced.json', 1, 2, 0, 0, 'manual', ?)
	`, dbTime(time.Now().In(appTimeLocation)))
	if err != nil {
		t.Fatalf("insert test price: %v", err)
	}
}

func nullableTestString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return *value
}

func nullableBlankTestString(value string) any {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return normalized
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

func insertKeeperStateForCandidateWithError(t *testing.T, app *App, name string, lastError string, lastCheckedAt *time.Time) {
	t.Helper()
	insertKeeperStateForCandidate(t, app, name, nil, lastCheckedAt)
	_, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET last_error = ?, updated_at = ?
		WHERE auth_name = ?
	`, lastError, dbTime(time.Now().In(appTimeLocation)), name)
	if err != nil {
		t.Fatalf("mark keeper state %s error: %v", name, err)
	}
}

func markKeeperStateDisabled(t *testing.T, app *App, name string) {
	t.Helper()
	_, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET disabled = 1, updated_at = ?
		WHERE auth_name = ?
	`, dbTime(time.Now().In(appTimeLocation)), name)
	if err != nil {
		t.Fatalf("mark keeper state %s disabled: %v", name, err)
	}
}

func markKeeperStateRecoverableUnauthorizedDisabled(t *testing.T, app *App, name string, lastCheckedAt *time.Time) {
	t.Helper()
	insertKeeperStateForCandidate(t, app, name, nil, lastCheckedAt)
	_, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET disabled = 1,
		    last_status_code = ?,
		    last_error = ?,
		    latest_action = ?,
		    last_checked_at = ?,
		    updated_at = ?
		WHERE auth_name = ?
	`, http.StatusUnauthorized, "凭证不可用：HTTP 401", "禁用凭证：凭证不可用：HTTP 401", dbTimePtr(lastCheckedAt), dbTime(time.Now().In(appTimeLocation)), name)
	if err != nil {
		t.Fatalf("mark keeper state %s recoverable unauthorized disabled: %v", name, err)
	}
}

func markKeeperStatePaymentRequiredDisabled(t *testing.T, app *App, name string) {
	t.Helper()
	_, err := app.db.Exec(`
		UPDATE codex_keeper_auth_states
		SET disabled = 1,
		    last_status_code = ?,
		    last_error = ?,
		    latest_action = ?,
		    updated_at = ?
		WHERE auth_name = ?
	`, http.StatusPaymentRequired, "凭证不可用：HTTP 402", "禁用凭证：凭证不可用：HTTP 402", dbTime(time.Now().In(appTimeLocation)), name)
	if err != nil {
		t.Fatalf("mark keeper state %s payment required disabled: %v", name, err)
	}
}

type keeperRecoveryStatusPatch struct {
	Name     string
	Disabled bool
}

type keeperRecoveryTestCPA struct {
	server          *httptest.Server
	mu              sync.Mutex
	authDetails     map[string]map[string]any
	usageStatuses   map[string]int
	usageCalls      map[string]int
	statusPatches   []keeperRecoveryStatusPatch
	priorityPatches map[string]int
}

func newKeeperRecoveryTestCPA(t *testing.T, authDetails map[string]map[string]any, usageStatuses map[string]int) *keeperRecoveryTestCPA {
	t.Helper()
	cpa := &keeperRecoveryTestCPA{
		authDetails:     authDetails,
		usageStatuses:   usageStatuses,
		usageCalls:      map[string]int{},
		priorityPatches: map[string]int{},
	}
	cpa.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			cpa.mu.Lock()
			files := make([]map[string]any, 0, len(cpa.authDetails))
			for name := range cpa.authDetails {
				files = append(files, map[string]any{"name": name, "type": "codex"})
			}
			cpa.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			name := r.URL.Query().Get("name")
			cpa.mu.Lock()
			detail, ok := cpa.authDetails[name]
			if !ok {
				cpa.mu.Unlock()
				http.NotFound(w, r)
				return
			}
			copied := map[string]any{}
			for key, value := range detail {
				copied[key] = value
			}
			cpa.mu.Unlock()
			_ = json.NewEncoder(w).Encode(copied)
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			var payload struct {
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cpa.mu.Lock()
			cpa.usageCalls[payload.AuthIndex]++
			status := cpa.usageStatuses[payload.AuthIndex]
			if status == 0 {
				status = http.StatusOK
			}
			planType := "free"
			if detail, ok := cpa.authDetails[payload.AuthIndex]; ok {
				if value, ok := detail["account_type"].(string); ok && strings.TrimSpace(value) != "" {
					planType = value
				}
			}
			cpa.mu.Unlock()
			if status >= 200 && status < 300 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status_code": status,
					"body": map[string]any{
						"plan_type": planType,
						"rate_limit": map[string]any{
							"primary_window": map[string]any{
								"used_percent":        10,
								"reset_after_seconds": 3600,
							},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": status,
				"body": map[string]any{
					"error": map[string]any{
						"message": "test credential error",
						"code":    "test_credential_error",
					},
					"status": status,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cpa.mu.Lock()
			cpa.statusPatches = append(cpa.statusPatches, keeperRecoveryStatusPatch{Name: payload.Name, Disabled: payload.Disabled})
			if detail, ok := cpa.authDetails[payload.Name]; ok {
				detail["disabled"] = payload.Disabled
			}
			cpa.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			var payload struct {
				Name     string `json:"name"`
				Priority *int   `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cpa.mu.Lock()
			cpa.priorityPatches[payload.Name]++
			if detail, ok := cpa.authDetails[payload.Name]; ok {
				if payload.Priority == nil {
					delete(detail, "priority")
				} else {
					detail["priority"] = *payload.Priority
				}
			}
			cpa.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	return cpa
}

func keeperRecoveryAuthDetail(name string, disabled bool) map[string]any {
	return map[string]any{
		"name":         name,
		"type":         "codex",
		"email":        name + "@example.com",
		"account_type": "free",
		"disabled":     disabled,
		"priority":     4,
		"access_token": "test-token",
		"auth_index":   name,
	}
}

func (c *keeperRecoveryTestCPA) URL() string {
	return c.server.URL
}

func (c *keeperRecoveryTestCPA) Close() {
	c.server.Close()
}

func (c *keeperRecoveryTestCPA) usageCallCount(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usageCalls[name]
}

func (c *keeperRecoveryTestCPA) statusPatchCount(name string, disabled bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, patch := range c.statusPatches {
		if patch.Name == name && patch.Disabled == disabled {
			count++
		}
	}
	return count
}

func assertKeeperRecoveredState(t *testing.T, app *App, name string) {
	t.Helper()
	state, err := app.getKeeperState(context.Background(), name)
	if err != nil {
		t.Fatalf("get keeper state %s: %v", name, err)
	}
	if state.Disabled {
		t.Fatalf("%s disabled = true, want false", name)
	}
	if state.LastStatusCode == nil || *state.LastStatusCode != http.StatusOK {
		t.Fatalf("%s last_status_code = %v, want 200", name, state.LastStatusCode)
	}
	if state.LastError != nil {
		t.Fatalf("%s last_error = %v, want nil", name, state.LastError)
	}
	if state.LatestAction == nil || !strings.Contains(*state.LatestAction, "恢复启用") {
		t.Fatalf("%s latest_action = %v, want recovery action", name, state.LatestAction)
	}
	if state.LastHealthyAt == nil {
		t.Fatalf("%s last_healthy_at is nil, want recovery to mark healthy", name)
	}
}

func assertKeeperStillDisabled(t *testing.T, app *App, name string) {
	t.Helper()
	state, err := app.getKeeperState(context.Background(), name)
	if err != nil {
		t.Fatalf("get keeper state %s: %v", name, err)
	}
	if !state.Disabled {
		t.Fatalf("%s disabled = false, want true", name)
	}
}

func stringPtr(value string) *string {
	return &value
}

func timePtrValue(value time.Time) *time.Time {
	return &value
}

func intPtrValue(value int) *int {
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

func keeperWebsocketUsageSuccessPayload(usedPercent int) map[string]any {
	return map[string]any{
		"status_code": 200,
		"body": map[string]any{
			"plan_type": "free",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        usedPercent,
					"reset_after_seconds": 3600,
				},
			},
		},
	}
}
