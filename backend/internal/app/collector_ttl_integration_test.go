package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const collectorTTLTestKey = "sk-collector-ttl-private-key"

type collectorTTLFixture struct {
	mu           sync.Mutex
	facts        map[string]usageTTLExecutionMetadata
	status       int
	health       string
	unsupported  bool
	capabilities map[string]json.RawMessage
	calls        int
	lookupIDs    [][]string
	queueCalls   int
	onLookup     func()
	response     *usageTTLMetadataResponse
}

func newCollectorTTLFixture(t *testing.T) (*collectorTTLFixture, AppConfig) {
	t.Helper()
	f := &collectorTTLFixture{facts: map[string]usageTTLExecutionMetadata{}, health: "configured"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer collector-management-secret" {
			t.Error("metadata lookup omitted management authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/usage-queue":
			f.queueCalls++
			_, _ = w.Write([]byte("[]"))
		case "GET /v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		case "GET " + cpaHelperPluginManagementPrefix + "/capabilities":
			if f.capabilities != nil {
				_ = json.NewEncoder(w).Encode(f.capabilities)
				return
			}
			modules, fields := []string{"model_rules", "claude_cache_ttl"}, []string{"keys[].claude_cache_ttl"}
			version := "0.1.10"
			if f.unsupported {
				version, modules, fields = "0.1.9", []string{"model_rules"}, []string{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_version": version, "cpa_version": "v8.0.45", "contract_version": "v1", "abi_version": 1, "schema_version": 7, "modules": modules, "policy_fields": fields})
		case "GET " + cpaHelperPluginManagementPrefix + "/claude-ttl/requirements":
			_, _ = w.Write([]byte(ttlRequirementsFixture))
		case "GET " + cpaHelperPluginManagementPrefix + "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "claude_cache_ttl": map[string]any{"status": f.health, "ttl_source": "enforced_request", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload"}})
		case "POST " + cpaHelperPluginManagementPrefix + "/claude-ttl/executions":
			var request struct {
				IDs []string `json:"execution_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.calls++
			f.lookupIDs = append(f.lookupIDs, request.IDs)
			if f.onLookup != nil {
				f.onLookup()
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(`{"error":"observer unavailable"}`))
				return
			}
			if f.response != nil {
				_ = json.NewEncoder(w).Encode(f.response)
				return
			}
			response := usageTTLMetadataResponse{MetadataVersion: "v1", RetentionHours: 24, Records: []usageTTLExecutionMetadata{}, Missing: []string{}}
			for _, id := range request.IDs {
				if fact, ok := f.facts[id]; ok {
					response.Records = append(response.Records, fact)
				} else {
					response.Missing = append(response.Missing, id)
				}
			}
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return f, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "collector-management-secret"}}
}

func newCollectorTTLApp(t *testing.T) (*App, int, modelPriceBillingIndex) {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	owner := seedQuotaTestUser(t, a, "collector-ttl-owner")
	seedQuotaTestAPIKey(t, a, owner, collectorTTLTestKey)
	quota := 1000.0
	if _, err := a.updateUserQuota(context.Background(), owner, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	seedQuotaTestNativePrice(t, a, "claude", "collector-ttl-auth", "claude-test", 0)
	if _, err := a.db.Exec(`UPDATE model_prices SET cache_creation_usd_per_million = 2, cache_creation_1h_usd_per_million = 8 WHERE model = 'claude-test'`); err != nil {
		t.Fatal(err)
	}
	return a, owner, quotaTestBillingPriceIndex(t, a)
}

func collectorTTLID(number int) string { return fmt.Sprintf("aaaaaaaa-bbbb-4ccc-8ddd-%012d", number) }

func collectorTTLMessage(t *testing.T, id string, at time.Time, mutate func(map[string]any)) string {
	t.Helper()
	object := map[string]any{
		"execution_id": id, "request_id": "trace-is-not-execution-" + id, "api_key": collectorTTLTestKey,
		"provider": "claude", "model": "claude-test", "auth": "api_key", "auth_index": "collector-ttl-auth",
		"timestamp": at.Format(time.RFC3339Nano), "source": collectorTTLTestKey, "failed": false,
		"tokens": map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_creation_tokens": 1000000},
	}
	if mutate != nil {
		mutate(object)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func collectorTTLFact(id, ttl string, at time.Time) usageTTLExecutionMetadata {
	return usageTTLExecutionMetadata{ExecutionID: id, CallerScope: pluginCallerScope(collectorTTLTestKey), TTL: ttl, Source: "enforced_request", PolicyRevision: 7, ObservedAt: at}
}

func collectorTTLCount(t *testing.T, a *App, query string, args ...any) int {
	t.Helper()
	var count int
	if err := a.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func collectorTTLRecord(t *testing.T, a *App, raw string) UsageRecord {
	t.Helper()
	normalized, err := normalizeUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.usageRecordByDedupe(context.Background(), normalized.DedupeKey)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertCollectorTTLCharge(t *testing.T, a *App, id int, amount float64, unpriced bool) {
	t.Helper()
	var actual float64
	var unknown bool
	if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, id).Scan(&actual, &unknown); err != nil {
		t.Fatal(err)
	}
	if actual != amount || unknown != unpriced {
		t.Fatalf("quota charge = %g/%t, want %g/%t", actual, unknown, amount, unpriced)
	}
}

func TestCollectorTTLAsyncFreezeRedactionAndNoLateReprice(t *testing.T) {
	a, owner, pricing := newCollectorTTLApp(t)
	f, cfg := newCollectorTTLFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
	first := collectorTTLMessage(t, collectorTTLID(1), now, nil)
	second := collectorTTLMessage(t, collectorTTLID(2), now, func(raw map[string]any) { raw["failed"] = true })
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{first, second}, now); err != nil {
		t.Fatal(err)
	}
	f.onLookup = func() {
		if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox`); got != 2 {
			t.Errorf("metadata IO preceded durable whole batch: %d", got)
		}
	}
	if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || completed != 0 {
		t.Fatalf("initial missing lookup = %d, %v", completed, err)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`); got != 0 {
		t.Fatalf("pending usage counted as completed: %d", got)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`); got != 0 {
		t.Fatalf("pending usage was charged: %d", got)
	}
	state, err := a.collectorState(ctx)
	if err != nil || state.TTLPendingRecords != 2 || state.TTLPendingReason == nil || *state.TTLPendingReason != "metadata_missing" {
		t.Fatalf("pending collector status = %#v, %v", state, err)
	}
	var payload string
	if err := a.db.QueryRow(`SELECT payload_json FROM usage_collector_inbox ORDER BY id LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, collectorTTLTestKey) || strings.Contains(payload, "collector-management-secret") {
		t.Fatal("inbox copied a plaintext credential")
	}
	if _, err := a.db.Exec(`UPDATE users SET username = 'renamed-owner' WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`DELETE FROM user_api_keys WHERE user_id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	replacement := seedQuotaTestUser(t, a, "collector-ttl-owner")
	seedQuotaTestAPIKey(t, a, replacement, collectorTTLTestKey)
	f.mu.Lock()
	// The observer may run after the processing clock snapshot was taken.
	f.facts[collectorTTLID(1)] = collectorTTLFact(collectorTTLID(1), "1h", now.Add(11*time.Second))
	f.facts[collectorTTLID(2)] = collectorTTLFact(collectorTTLID(2), "5m", now.Add(time.Second))
	f.health = "disabled"
	f.mu.Unlock()
	if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(9*time.Second)); err != nil || completed != 0 {
		t.Fatalf("retry ran before scheduled ten seconds: %d/%v", completed, err)
	}
	if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(10*time.Second)); err != nil || completed != 2 {
		t.Fatalf("observer retry = %d/%v", completed, err)
	}
	for _, raw := range []string{first, second} {
		record := collectorTTLRecord(t, a, raw)
		metadata := record.CacheCreationMetadata
		if metadata.Source == nil || *metadata.Source != "enforced_request" || metadata.Status == nil || *metadata.Status != "matched" || metadata.PolicyRevision == nil || *metadata.PolicyRevision != 7 {
			t.Fatalf("execution provenance = %#v", metadata)
		}
		if record.UsageUsername == nil || *record.UsageUsername != "collector-ttl-owner" || record.CacheCreationTokens != 1000000 {
			t.Fatalf("frozen owner/queue tokens changed: %#v", record)
		}
		if record.RawJSON == raw || strings.Contains(record.RawJSON, collectorTTLTestKey) {
			t.Fatal("persisted raw usage was not protected")
		}
		amount := 8.0
		if record.Failed {
			amount = 2
		}
		assertCollectorTTLCharge(t, a, record.ID, amount, false)
		var chargedOwner int
		if err := a.db.QueryRow(`SELECT user_id FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&chargedOwner); err != nil || chargedOwner != owner {
			t.Fatalf("pending usage charged replacement username: %d/%v", chargedOwner, err)
		}
	}
	if !collectorTTLRecord(t, a, second).Failed {
		t.Fatal("failed flag was overwritten by successful metadata lookup")
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox WHERE payload_json != '' OR caller_scope IS NOT NULL OR usage_user_id IS NOT NULL OR usage_username IS NOT NULL OR api_key_description IS NOT NULL`); got != 0 {
		t.Fatalf("completed inbox retained working payload: %d", got)
	}
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{first, second}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.facts[collectorTTLID(1)] = collectorTTLFact(collectorTTLID(1), "5m", now)
	lookupCount := f.calls
	f.mu.Unlock()
	if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(time.Minute)); err != nil || count != 0 {
		t.Fatalf("completed duplicate = %d/%v", count, err)
	}
	assertCollectorTTLCharge(t, a, collectorTTLRecord(t, a, first).ID, 8, false)
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`); got != 2 {
		t.Fatalf("duplicate replayed ledger: %d", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != lookupCount {
		t.Fatal("late metadata queried and could reprice a completed record")
	}
}

func TestCollectorTTLStablePluginContractFirstCharge(t *testing.T) {
	for _, version := range []string{"0.1.11", "0.1.42"} {
		t.Run(version, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			f.capabilities = pluginStableCapabilitiesFixture(version, "v8.0.45", true)
			ctx := context.Background()
			now := time.Date(2026, 10, 29, 10, 0, 0, 0, appTimeLocation)
			id := collectorTTLID(91)
			raw := collectorTTLMessage(t, id, now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || completed != 0 {
				t.Fatalf("missing modern execution metadata completed early: %d/%v", completed, err)
			}
			if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`); got != 0 {
				t.Fatal("pending modern usage was charged")
			}
			f.mu.Lock()
			f.health = "disabled"
			f.facts[id] = collectorTTLFact(id, "1h", now.Add(time.Second))
			f.mu.Unlock()
			if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(10*time.Second)); err != nil || completed != 1 {
				t.Fatalf("retained modern metadata was unavailable after disable: %d/%v", completed, err)
			}
			record := collectorTTLRecord(t, a, raw)
			if record.CacheCreationMetadata.Status == nil || *record.CacheCreationMetadata.Status != "matched" || record.CacheCreationMetadata.Source == nil || *record.CacheCreationMetadata.Source != "enforced_request" {
				t.Fatalf("modern execution provenance was lost: %#v", record.CacheCreationMetadata)
			}
			assertCollectorTTLCharge(t, a, record.ID, 8, false)
			f.mu.Lock()
			f.facts[id] = collectorTTLFact(id, "5m", now)
			calls := f.calls
			f.mu.Unlock()
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(time.Minute)); err != nil || completed != 0 {
				t.Fatalf("completed modern usage was replayed: %d/%v", completed, err)
			}
			assertCollectorTTLCharge(t, a, record.ID, 8, false)
			if f.calls != calls || collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`) != 1 {
				t.Fatal("late modern metadata repriced or recharged a completed record")
			}
		})
	}
}

func TestCollectorTTLDeadlineOriginAndRetention(t *testing.T) {
	for _, test := range []struct {
		name, reason, status string
		elapsed              time.Duration
		unavailable, origin  bool
	}{
		{name: "missing", elapsed: 5 * time.Minute, reason: "metadata_missing", status: "missing"},
		{name: "service unavailable", unavailable: true, elapsed: 5 * time.Minute, reason: "metadata_unavailable", status: "error"},
		{name: "changed origin", origin: true, elapsed: 5 * time.Minute, reason: "collector_origin_changed", status: "missing"},
		{name: "retention expired while offline", elapsed: 25 * time.Hour, reason: "metadata_expired", status: "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
			raw := collectorTTLMessage(t, collectorTTLID(10), now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			if test.unavailable {
				f.status = http.StatusServiceUnavailable
			}
			if test.origin {
				cfg.Collector.CLIProxyURL += "/new-instance"
			}
			for _, at := range []time.Time{now, now.Add(10 * time.Second), now.Add(40 * time.Second), now.Add(100 * time.Second)} {
				if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, at); err != nil || count != 0 {
					t.Fatalf("pending attempt = %d/%v", count, err)
				}
			}
			if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(test.elapsed)); err != nil || count != 1 {
				t.Fatalf("deadline = %d/%v", count, err)
			}
			record := collectorTTLRecord(t, a, raw)
			if record.CacheCreationTTL == nil || *record.CacheCreationTTL != "unknown" || *record.CacheCreationMetadata.Status != test.status || *record.CacheCreationMetadata.Reason != test.reason {
				t.Fatalf("deadline provenance = %#v/%v", record.CacheCreationMetadata, record.CacheCreationTTL)
			}
			assertCollectorTTLCharge(t, a, record.ID, 2, false)
			if test.origin && f.calls != 0 {
				t.Fatal("old execution queried against a new origin")
			}
		})
	}
}

func TestCollectorTTLCrashRecoveryAndFrozenOwner(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"usage committed before quota", `CREATE TRIGGER fail_ttl_charge BEFORE INSERT ON user_quota_charges BEGIN SELECT RAISE(ABORT, 'test quota crash'); END`},
		{"charged before receipt complete", `CREATE TRIGGER fail_ttl_charge BEFORE UPDATE OF state ON usage_collector_inbox WHEN NEW.state = 'complete' BEGIN SELECT RAISE(ABORT, 'test receipt crash'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			now, ctx := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation), context.Background()
			raw := collectorTTLMessage(t, collectorTTLID(20), now, nil)
			f.facts[collectorTTLID(20)] = collectorTTLFact(collectorTTLID(20), "1h", now)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := a.db.Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err == nil {
				t.Fatal("database failure was reported as metadata/ingestion success")
			}
			if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`); count != 1 {
				t.Fatalf("crash boundary usage rows = %d", count)
			}
			if _, err := a.db.Exec(`DROP TRIGGER fail_ttl_charge`); err != nil {
				t.Fatal(err)
			}
			// Simulate restart by re-opening the same file and recovering the ready input.
			a.Close()
			restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restarted.Close)
			if count, err := restarted.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(time.Second)); err != nil || count != 1 {
				t.Fatalf("restart recovery = %d/%v", count, err)
			}
			record := collectorTTLRecord(t, restarted, raw)
			assertCollectorTTLCharge(t, restarted, record.ID, 8, false)
			if count := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); count != 1 {
				t.Fatalf("recovery duplicated charges: %d", count)
			}
			state, err := restarted.collectorState(ctx)
			if err != nil || state.RecordsCollected != 1 || state.TTLPendingRecords != 0 {
				t.Fatalf("recovery counted receipt incorrectly: %#v/%v", state, err)
			}
			if f.calls != 1 {
				t.Fatal("recovery fetched new metadata after facts were frozen")
			}
		})
	}
}

func TestCollectorTTLUpstreamScopeAndApplicability(t *testing.T) {
	for _, test := range []struct {
		name, ttl, source, status, reason string
		amount                            float64
		unpriced                          bool
		mutate                            func(map[string]any)
		fact                              func(*usageTTLExecutionMetadata)
		unsupported                       bool
	}{
		{name: "upstream wins", ttl: "5m", source: "upstream", status: "conflict", reason: "upstream_policy_conflict", amount: 2, mutate: func(raw map[string]any) { raw["tokens"].(map[string]any)["cache_creation_ttl"] = "5m" }},
		{name: "valid mixed wins", ttl: "mixed", source: "upstream", status: "conflict", reason: "upstream_policy_conflict", amount: 5.6, mutate: func(raw map[string]any) {
			tokens := raw["tokens"].(map[string]any)
			tokens["cache_creation_ttl"], tokens["cache_creation_5m_tokens"], tokens["cache_creation_1h_tokens"] = "mixed", 400000, 600000
		}},
		{name: "invalid mixed stays unpriced", ttl: "mixed", source: "upstream", status: "conflict", reason: "invalid_upstream_buckets", unpriced: true, mutate: func(raw map[string]any) { raw["tokens"].(map[string]any)["cache_creation_ttl"] = "mixed" }},
		{name: "cross key metadata rejected", ttl: "unknown", source: "unknown", status: "conflict", reason: "caller_scope_mismatch", amount: 2, fact: func(fact *usageTTLExecutionMetadata) { fact.CallerScope = pluginCallerScope("another-key") }},
		{name: "request id is not execution", ttl: "unknown", source: "unknown", status: "missing", reason: "execution_id_missing", amount: 2, mutate: func(raw map[string]any) { delete(raw, "execution_id") }},
		{name: "count tokens", ttl: "unknown", source: "unknown", status: "not_applicable", reason: "count_tokens", amount: 2, mutate: func(raw map[string]any) { raw["endpoint"] = "/v1/messages/count_tokens" }},
		{name: "no write", source: "", status: "not_applicable", reason: "no_cache_creation", mutate: func(raw map[string]any) { raw["tokens"].(map[string]any)["cache_creation_tokens"] = 0 }},
		{name: "unsupported plugin", ttl: "unknown", source: "unknown", status: "unsupported", reason: "metadata_unsupported", amount: 2, unsupported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			now, ctx := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation), context.Background()
			raw := collectorTTLMessage(t, collectorTTLID(30), now, test.mutate)
			fact := collectorTTLFact(collectorTTLID(30), "1h", now)
			if test.fact != nil {
				test.fact(&fact)
			}
			f.facts[fact.ExecutionID], f.unsupported = fact, test.unsupported
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || count != 1 {
				t.Fatalf("resolve = %d/%v", count, err)
			}
			record := collectorTTLRecord(t, a, raw)
			metadata := record.CacheCreationMetadata
			if !sameOptionalUsageText(metadata.Status, &test.status) || !sameOptionalUsageText(metadata.Reason, &test.reason) {
				t.Fatalf("provenance = %#v", metadata)
			}
			if test.source == "" {
				if metadata.Source != nil || record.CacheCreationTTL != nil {
					t.Fatal("zero cache creation carried TTL attribution")
				}
			} else if !sameOptionalUsageText(metadata.Source, &test.source) || !sameOptionalUsageText(record.CacheCreationTTL, &test.ttl) {
				t.Fatalf("TTL attribution = %v/%#v", record.CacheCreationTTL, metadata)
			}
			assertCollectorTTLCharge(t, a, record.ID, test.amount, test.unpriced)
		})
	}
}

func TestCollectorTTLReceiptIndexPlans(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	for _, query := range []string{
		`SELECT COUNT(*) FROM usage_collector_inbox WHERE state IN ('pending','ready')`,
		`SELECT last_error_code FROM usage_collector_inbox WHERE state IN ('pending','ready') ORDER BY id LIMIT 1`,
		`SELECT id FROM usage_collector_inbox WHERE state IN ('pending','ready') AND (state = 'ready' OR next_attempt_at <= '2026' OR deadline_at <= '2026') ORDER BY id LIMIT 1000`,
	} {
		rows, err := a.db.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(plans, "\n")
		if !strings.Contains(plan, "SEARCH usage_collector_inbox") || !strings.Contains(plan, "idx_usage_collector_inbox_due") {
			t.Fatalf("pending query scans historical receipts: %s", plan)
		}
	}
}
