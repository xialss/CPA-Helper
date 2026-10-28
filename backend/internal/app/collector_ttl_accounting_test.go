package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectorTTLAccountingAmountsAcrossAllReaders(t *testing.T) {
	for _, test := range []struct {
		name          string
		rate          *float64
		long, offpeak bool
		want          float64
		unpriced      bool
	}{
		{name: "unset 1h is unpriced", unpriced: true},
		{name: "zero 1h is free", rate: claudeTTLFloatPtr(0)},
		{name: "positive 1h", rate: claudeTTLFloatPtr(8), want: 8},
		{name: "offpeak explicit 1h", rate: claudeTTLFloatPtr(8), offpeak: true, want: 3},
		{name: "long context", rate: claudeTTLFloatPtr(8), long: true, want: 19},
		{name: "offpeak long context", rate: claudeTTLFloatPtr(8), long: true, offpeak: true, want: 4.75},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, owner, _ := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			ctx := context.Background()
			at := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
			if test.offpeak {
				at = time.Date(2026, 10, 25, 10, 0, 0, 0, appTimeLocation)
			}
			var rule *PriceTimeRule
			if test.offpeak {
				rule = testTimeRule()
				rule.OffpeakMode = "explicit"
				rule.OffpeakRates = &PriceOffpeakRates{Input: 2, CacheCreation: 1, CacheCreation1h: claudeTTLFloatPtr(3)}
			}
			if _, err := a.db.Exec(`UPDATE model_prices SET cache_creation_1h_usd_per_million = ?, long_context_threshold_tokens = 1500000,
				long_context_input_usd_per_million = 3, long_context_output_usd_per_million = 0, long_context_cache_read_usd_per_million = 0,
				long_context_cache_creation_usd_per_million = 4, long_context_cache_creation_1h_usd_per_million = 16, time_pricing = ? WHERE model = 'claude-test'`, test.rate, priceTimeRuleJSON(rule)); err != nil {
				t.Fatal(err)
			}
			pricing := quotaTestBillingPriceIndex(t, a)
			for _, price := range pricing.Prices {
				tx, err := a.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.appendModelPriceVersion(ctx, tx, price, at.AddDate(-1, 0, 0), true); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			pricing.Versions, err = a.loadModelPriceVersions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			id := collectorTTLID(70)
			raw := collectorTTLMessage(t, id, at, func(raw map[string]any) {
				if test.long {
					raw["tokens"].(map[string]any)["input_tokens"] = 1000000
				}
			})
			f.facts[id] = collectorTTLFact(id, "1h", at)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, at); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, at); err != nil {
				t.Fatal(err)
			}
			record := collectorTTLRecord(t, a, raw)
			cost := versionedCostBreakdown(record, pricing, true)
			if cost.TotalUSD != test.want || cost.Unpriced != test.unpriced || cost.LongContextApplied != test.long {
				t.Fatalf("attributed detail cost = %#v, want %g/%t", cost, test.want, test.unpriced)
			}
			assertCollectorTTLCharge(t, a, record.ID, test.want, test.unpriced)
			unpriced := 0
			if test.unpriced {
				unpriced = 1
			}
			start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, appTimeLocation)
			assertClaudeTTLHistoricalCosts(t, a, pricing, start, start.AddDate(0, 0, 1), map[string]float64{"claude-test": test.want}, unpriced)
			user, err := a.getUser(ctx, owner)
			if err != nil || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 1000-test.want {
				t.Fatalf("attributed quota balance = %#v/%v", user, err)
			}
		})
	}
}

func TestCollectorTTLRunnerRetainsDequeuedBatchOnInboxFailure(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw := collectorTTLMessage(t, collectorTTLID(80), time.Now(), func(raw map[string]any) { raw["tokens"].(map[string]any)["cache_creation_tokens"] = 0 })
	var calls atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/usage-queue":
			if calls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode([]string{raw})
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		case "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer cpa.Close()
	cfg, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector = CollectorConfig{Enabled: true, CLIProxyURL: cpa.URL, ManagementKey: "test-management", BatchSize: 10, PollIntervalSeconds: .2, RetryIntervalSeconds: 1}
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER fail_inbox_persist BEFORE INSERT ON usage_collector_inbox BEGIN SELECT RAISE(ABORT, 'inbox unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	runner := NewCollectorRunner(a)
	runner.Start()
	defer runner.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for {
		state, err := a.collectorState(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.LastError != nil && strings.Contains(*state.LastError, "persist dequeued usage batch") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector did not report inbox persistence failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("collector dequeued another batch while original was not durable: %d", calls.Load())
	}
	if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`); count != 0 {
		t.Fatalf("failed persistence counted usage: %d", count)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_inbox_persist`); err != nil {
		t.Fatal(err)
	}
	for collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("collector did not retry retained original batch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.Stop()
	record := collectorTTLRecord(t, a, raw)
	if record.CacheCreationMetadata.ExecutionID == nil || *record.CacheCreationMetadata.ExecutionID != collectorTTLID(80) {
		t.Fatal("retry processed a different input")
	}
	if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox`); count != 1 {
		t.Fatalf("retry inserted duplicate inbox receipts: %d", count)
	}
}
