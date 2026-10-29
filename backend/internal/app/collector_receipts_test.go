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

const collectorTTLTestKey = "sk-collector-private-key"

type collectorTTLFixture struct {
	mu         sync.Mutex
	queue      []string
	queueCalls int
	calls      int
	status     int
}

func newCollectorTTLFixture(t *testing.T) (*collectorTTLFixture, AppConfig) {
	t.Helper()
	f := &collectorTTLFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer collector-management-secret" {
			t.Error("collector omitted management authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/usage-queue":
			f.queueCalls++
			batch := append([]string{}, f.queue...)
			f.queue = nil
			_ = json.NewEncoder(w).Encode(batch)
		case "GET /v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		case "GET " + cpaHelperPluginManagementPrefix + "/claude-ttl/requirements", "POST " + cpaHelperPluginManagementPrefix + "/claude-ttl/executions":
			f.calls++
			t.Error("collector requested a retired TTL endpoint")
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			_, _ = w.Write([]byte(`{"records":[],"missing_execution_ids":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return f, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "collector-management-secret", BatchSize: 10}}
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
		"execution_id": id, "request_id": "trace-" + id, "api_key": collectorTTLTestKey,
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

func TestCollectorRequestsAppearInSamePollWithoutTTLMetadata(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a, owner, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Now().In(appTimeLocation)
			// The list handler requires the same selector context as collection.
			index := "collector-ttl-auth"
			installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{Brand: aiProviderBrandClaude, AuthIndex: &index, Models: []aiProviderModel{{Name: "claude-test"}}}})
			claude := collectorTTLMessage(t, collectorTTLID(1), now, func(raw map[string]any) {
				// Retired metadata can have any shape without changing ingestion.
				raw["cache_creation_metadata"] = []any{false, map[string]any{"observed_at": "invalid"}}
				raw["tokens"].(map[string]any)["cache_creation_ttl"] = map[string]any{"invalid": true}
			})
			openai := collectorTTLMessage(t, collectorTTLID(2), now, func(raw map[string]any) {
				raw["provider"], raw["model"] = "openai", "gpt-test"
				raw["tokens"] = map[string]any{"input_tokens": 25, "output_tokens": 5}
			})
			f.status, f.queue = status, []string{claude, openai}
			batch, err := consumeRespQueue(ctx, cfg.Collector)
			if err != nil || len(batch) != 2 {
				t.Fatalf("dequeue batch = %v/%v", batch, err)
			}
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, batch, now); err != nil {
				t.Fatal(err)
			}
			if completed, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || completed != 2 {
				t.Fatalf("same-poll accounting = %d/%v", completed, err)
			}
			record := collectorTTLRecord(t, a, claude)
			assertCollectorTTLCharge(t, a, record.ID, 2, false)
			if record.CacheCreationTokens != 1000000 || record.CacheCreationTTL != nil || len(record.CacheCreationMetadata) != 0 {
				t.Fatalf("retired evidence changed the stored request: %#v", record)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/usage/records", nil)
			query := request.URL.Query()
			query.Set("start", now.Add(-time.Hour).Format(time.RFC3339Nano))
			query.Set("end", now.Add(time.Hour).Format(time.RFC3339Nano))
			request.URL.RawQuery = query.Encode()
			filters, err := parseUsageFilters(request)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			if err := a.usageRecords(response, request, filters, &AuthUser{ID: owner, Username: "collector-ttl-owner"}); err != nil {
				t.Fatal(err)
			}
			var list struct {
				Total int
				Items []map[string]json.RawMessage
			}
			if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || list.Total != 2 || len(list.Items) != 2 {
				t.Fatalf("same-poll usage list = %s/%v", response.Body.String(), err)
			}
			for _, item := range list.Items {
				for _, field := range []string{"cache_creation_ttl", "cache_creation_5m_tokens", "cache_creation_1h_tokens", "execution_id", "cache_creation_ttl_status"} {
					if _, exists := item[field]; exists {
						t.Fatalf("usage API still exposes %s", field)
					}
				}
			}
			f.mu.Lock()
			calls, queueCalls := f.calls, f.queueCalls
			f.mu.Unlock()
			if calls != 0 || queueCalls != 1 {
				t.Fatalf("same poll used TTL or dequeued again: %d/%d", calls, queueCalls)
			}
		})
	}
}

func TestCollectorLegacyPendingAndReadyRecoverImmediatelyWithoutPluginConfig(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Now().In(appTimeLocation)
	for index, state := range []string{"pending", "ready"} {
		raw := collectorTTLMessage(t, collectorTTLID(10+index), now, nil)
		if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
			t.Fatal(err)
		}
		recordInput, err := normalizeUsage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec(`UPDATE usage_collector_inbox SET state = ?, next_attempt_at = ?, deadline_at = ?,
			payload_json = json_set(payload_json, '$.claude_applicable', json('true'), '$.usage.ExecutionID', ?, '$.usage.CacheCreationTTL', '1h', '$.usage.CacheCreationMetadata', json('{"version":"future","observed_at":"invalid","extra":true}')) WHERE dedupe_key = ?`,
			state, dbTime(now.Add(time.Hour)), dbTime(now.Add(5*time.Minute)), collectorTTLID(10+index), recordInput.DedupeKey); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if count, err := restarted.processCollectorUsageInbox(ctx, AppConfig{}, pricing, now); err != nil || count != 2 {
		t.Fatalf("old receipts waited for TTL or Plugin config: %d/%v", count, err)
	}
	if count, err := restarted.processCollectorUsageInbox(ctx, AppConfig{}, pricing, now); err != nil || count != 0 {
		t.Fatalf("completed receipts replayed: %d/%v", count, err)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges WHERE amount_usd = 2`); got != 2 {
		t.Fatalf("old receipts did not use one cache creation price once each: %d", got)
	}
}

func TestCollectorCrashRecoveryKeepsHistoricalChargeAndCountsOnce(t *testing.T) {
	for _, failure := range []string{"quota", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			_, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Now().In(appTimeLocation)
			raw := collectorTTLMessage(t, collectorTTLID(20), now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_receipt BEFORE UPDATE OF state ON usage_collector_inbox WHEN NEW.state = 'complete' BEGIN SELECT RAISE(ABORT, 'receipt crash'); END`
			if failure == "quota" {
				trigger = `CREATE TRIGGER fail_receipt BEFORE INSERT ON user_quota_charges BEGIN SELECT RAISE(ABORT, 'quota crash'); END`
			}
			if _, err := a.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err == nil {
				t.Fatal("fixture did not reach the persistence failure")
			}
			record := collectorTTLRecord(t, a, raw)
			if failure == "receipt" {
				// An old completed quota ledger must never be rewritten to today's price.
				if _, err := a.db.Exec(`UPDATE user_quota_charges SET amount_usd = 8 WHERE usage_record_id = ?`, record.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.db.Exec(`DROP TRIGGER fail_receipt`); err != nil {
				t.Fatal(err)
			}
			a.Close()
			restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restarted.Close)
			if count, err := restarted.processCollectorUsageInbox(ctx, AppConfig{}, pricing, now); err != nil || count != 1 {
				t.Fatalf("crash recovery = %d/%v", count, err)
			}
			want := 2.0
			if failure == "receipt" {
				want = 8
			}
			assertCollectorTTLCharge(t, restarted, record.ID, want, false)
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); got != 1 {
				t.Fatalf("quota replayed after restart: %d", got)
			}
			state, err := restarted.collectorState(ctx)
			if err != nil || state.RecordsCollected != 1 {
				t.Fatalf("receipt count = %#v/%v", state, err)
			}
		})
	}
}

func TestCollectorRetiredMalformedStoredMetadataDoesNotHideUsage(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Now().In(appTimeLocation)
	raw := collectorTTLMessage(t, collectorTTLID(30), now, nil)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil {
		t.Fatal(err)
	}
	record := collectorTTLRecord(t, a, raw)
	for _, metadata := range []string{`{`, `{"version":"v2"}`, `{"observed_at":"invalid","status":99}`} {
		if _, err := a.db.Exec(`UPDATE usage_records SET cache_creation_metadata = ? WHERE id = ?`, metadata, record.ID); err != nil {
			t.Fatal(err)
		}
		if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
			t.Fatalf("retired evidence blocked analytics repair: %v", err)
		}
		stored, err := a.getUsageRecord(ctx, record.ID)
		if err != nil || stored.ID != record.ID {
			t.Fatalf("retired evidence hid request: %#v/%v", stored, err)
		}
		assertCollectorTTLCharge(t, a, record.ID, 2, false)
	}
}

func TestCollectorHandledRecoveryPassesFullUnpricedBatch(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Now().In(appTimeLocation)
	prefix := make([]string, collectorUsageBatchSize)
	for index := range prefix {
		prefix[index] = collectorTTLMessage(t, collectorTTLID(10000+index), now, nil)
	}
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, prefix, now); err != nil {
		t.Fatal(err)
	}
	raw := collectorTTLMessage(t, collectorTTLID(20000), now, nil)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash window after usage/quota commit but before receipt
	// completion, behind a full batch whose prices are currently unavailable.
	var item collectorUsageInboxItem
	var payload, username string
	var owner int
	if err := a.db.QueryRow(`SELECT id, collector_origin, dedupe_key, payload_json, usage_user_id, usage_username FROM usage_collector_inbox ORDER BY id DESC LIMIT 1`).Scan(&item.ID, &item.Origin, &item.DedupeKey, &payload, &owner, &username); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(payload), &item.Envelope); err != nil {
		t.Fatal(err)
	}
	item.Owner = frozenUsageOwner{InboxID: item.ID, UserID: &owner, Username: &username}
	record, _, err := a.saveNormalizedUsage(ctx, item.Envelope.normalized(), pricing, item.Origin, &item.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := a.processCollectorUsageInboxWithPricing(ctx, AppConfig{}, nil, now); err != nil || count != 1 {
		t.Fatalf("handled recovery stayed behind unpriced inputs: %d/%v", count, err)
	}
	assertCollectorTTLCharge(t, a, record.ID, 2, false)
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`); got != 1 {
		t.Fatalf("pricing outage performed first accounting: %d", got)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox WHERE state = 'ready'`); got != collectorUsageBatchSize {
		t.Fatalf("pricing outage discarded unhandled receipts: %d", got)
	}
}

func TestCollectorReceiptQueryUsesPendingIndex(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	rows, err := a.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM usage_collector_inbox WHERE state IN ('pending','ready') AND (1 OR quota_handled = 1) ORDER BY id LIMIT 1000`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if plan := strings.Join(plans, "\n"); !strings.Contains(plan, "idx_usage_collector_inbox_due") {
		t.Fatalf("recovery scans completed receipts: %s", plan)
	}
}
