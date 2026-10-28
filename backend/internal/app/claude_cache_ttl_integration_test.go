package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func TestClaudeCacheTTLLegacyPriceReadsPreserveExistingAttributes(t *testing.T) {
	dbPath := prepareMigrationTestDatabase(t, t.TempDir(), 202610190001)
	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	rule := testTimeRule()
	if _, err := db.ExecContext(ctx, `INSERT INTO model_prices (id, provider, model, request_usd, billing_unit, updated_at) VALUES (101, 'openai', 'chat-request', 0.7, 'request', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_prices (id, provider, model, price_scope, channel_brand, channel_key, billing_unit, time_pricing, updated_at) VALUES (102, 'deepseek', 'deepseek-v4-flash', 'channel', 'openai_compatibility', 'deepseek', 'token', ?, CURRENT_TIMESTAMP)`, priceTimeRuleJSON(rule)); err != nil {
		t.Fatal(err)
	}
	prices, err := listPricesWithQueryer(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 {
		t.Fatalf("legacy price count = %d, want 2", len(prices))
	}
	for _, listed := range prices {
		price, err := getPriceWithQuerier(ctx, db, listed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(listed, price) || price.CacheCreation1hUSDPerMillion != nil {
			t.Fatalf("legacy list/get mismatch: %#v / %#v", listed, price)
		}
		if price.ID == 101 && (price.BillingUnit != modelBillingUnitRequest || price.RequestUSD == nil || *price.RequestUSD != 0.7) {
			t.Fatalf("legacy request billing changed: %#v", price)
		}
		if price.ID == 102 && (price.TimePricing == nil || price.TimePricing.OffpeakMultiplier != 0.5 || price.TimePricing.Timezone != rule.Timezone) {
			t.Fatalf("legacy time pricing lost: %#v", price)
		}
	}
	// Defaults apply only when those older columns really do not exist.
	for _, column := range []string{"billing_unit", "time_pricing"} {
		if _, err := db.ExecContext(ctx, "ALTER TABLE model_prices DROP COLUMN "+column); err != nil {
			t.Fatal(err)
		}
	}
	prices, err = listPricesWithQueryer(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range prices {
		if price.BillingUnit != modelBillingUnitToken || price.TimePricing != nil {
			t.Fatalf("absent legacy columns did not use compatibility defaults: %#v", price)
		}
		if _, err := getPriceWithQuerier(ctx, db, price.ID); err != nil {
			t.Fatalf("read older price without billing/time columns: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listPricesWithQueryer(ctx, db); !errors.Is(err, sql.ErrConnDone) && (err == nil || err.Error() != "sql: database is closed") {
		t.Fatalf("unexpected database failure was not propagated: %v", err)
	}
}

func TestClaudeCacheTTLTimePricingQuotaAndHistoricalCosts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	userID := seedQuotaTestUser(t, a, "ttl-member")
	apiKey := "sk-ttl-integration"
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	quota := 1000.0
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	peak := time.Date(2026, 10, 26, 10, 0, 0, 0, appTimeLocation)
	offpeak := peak.AddDate(0, 0, -1)
	wantCosts := map[string]float64{}
	wantTotal, wantUnpriced := 0.0, 0
	for i, test := range []struct {
		name        string
		mode        string
		offpeak     bool
		long        bool
		ttl         string
		missing1h   bool
		zero1h      bool
		nonClaude   bool
		genericZero bool
		want        float64
		unpriced    bool
	}{
		{name: "multiplier-peak-1h", mode: "multiplier", ttl: "1h", want: 8},
		{name: "multiplier-offpeak-1h", mode: "multiplier", offpeak: true, ttl: "1h", want: 4},
		{name: "explicit-peak-1h", mode: "explicit", ttl: "1h", want: 8},
		{name: "explicit-offpeak-1h", mode: "explicit", offpeak: true, ttl: "1h", want: 3},
		{name: "explicit-offpeak-5m", mode: "explicit", offpeak: true, ttl: "5m", want: 1},
		{name: "explicit-offpeak-mixed", mode: "explicit", offpeak: true, ttl: "mixed", want: 2.2},
		{name: "explicit-offpeak-unset-1h", mode: "explicit", offpeak: true, ttl: "1h", missing1h: true, unpriced: true},
		{name: "explicit-offpeak-free-1h", mode: "explicit", offpeak: true, ttl: "1h", zero1h: true},
		{name: "long-peak-1h", mode: "multiplier", long: true, ttl: "1h", want: 19},
		{name: "long-multiplier-offpeak-1h", mode: "multiplier", offpeak: true, long: true, ttl: "1h", want: 4.75},
		{name: "long-explicit-offpeak-1h", mode: "explicit", offpeak: true, long: true, ttl: "1h", want: 4.75},
		{name: "legacy-unknown", mode: "multiplier", ttl: "unknown", want: 2},
		{name: "non-claude-separate-write", ttl: "1h", nonClaude: true, want: 2},
		{name: "non-claude-included-write", ttl: "1h", nonClaude: true, genericZero: true, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, selector := "claude-ttl-"+test.name, fmt.Sprintf("ttl-auth-%d", i)
			brand := "claude"
			if test.nonClaude {
				brand = "openai_compatibility"
			}
			seedQuotaTestNativePrice(t, a, brand, selector, model, 1)
			rule := testTimeRule()
			rule.OffpeakMode = test.mode
			if test.nonClaude {
				rule = nil
			} else if test.mode == "explicit" {
				oneHour := claudeTTLFloatPtr(3)
				if test.missing1h {
					oneHour = nil
				} else if test.zero1h {
					oneHour = claudeTTLFloatPtr(0)
				}
				rule.OffpeakRates = &PriceOffpeakRates{Input: 2, CacheCreation: 1, CacheCreation1h: oneHour}
			}
			fiveRate := 2.0
			if test.genericZero {
				fiveRate = 0
			}
			if _, err := a.db.ExecContext(ctx, `UPDATE model_prices SET
				cache_creation_usd_per_million = ?, cache_creation_1h_usd_per_million = 8,
				long_context_threshold_tokens = 1500000, long_context_input_usd_per_million = 3,
				long_context_output_usd_per_million = 0, long_context_cache_read_usd_per_million = 0,
				long_context_cache_creation_usd_per_million = 4, long_context_cache_creation_1h_usd_per_million = 16,
				time_pricing = ? WHERE model = ?`, fiveRate, priceTimeRuleJSON(rule), model); err != nil {
				t.Fatal(err)
			}
			pricing := quotaTestBillingPriceIndex(t, a)
			for _, price := range pricing.Prices {
				if price.Model != model {
					continue
				}
				// Exercise persisted version snapshots, including the time rule's
				// nullable off-peak 1h rate, in the monetary paths below.
				tx, err := a.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.appendModelPriceVersion(ctx, tx, price, peak.AddDate(-1, 0, 0), true); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			pricing.Versions, err = a.loadModelPriceVersions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			at, input := peak, 0
			if test.offpeak {
				at = offpeak
			}
			if test.long || test.nonClaude {
				input = 1000000
			}
			tokens := map[string]any{"input_tokens": input, "cache_creation_tokens": 1000000, "cache_creation_ttl": test.ttl}
			if test.ttl == "mixed" {
				tokens["cache_creation_5m_tokens"] = 400000
				tokens["cache_creation_1h_tokens"] = 600000
			}
			provider := brand
			if test.nonClaude {
				provider = selector
			}
			raw, err := json.Marshal(map[string]any{
				"api_key": apiKey, "provider": provider, "model": model, "auth": "api_key", "auth_index": selector,
				"request_id": test.name, "timestamp": at.Format(time.RFC3339), "tokens": tokens,
			})
			if err != nil {
				t.Fatal(err)
			}
			record, created, err := a.saveUsageMessage(ctx, raw, pricing)
			if err != nil || !created {
				t.Fatalf("save TTL usage created=%t err=%v", created, err)
			}
			stored, err := a.getUsageRecord(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			breakdown := versionedCostBreakdown(stored, pricing, true)
			if breakdown.TotalUSD != test.want || breakdown.Unpriced != test.unpriced || (!test.unpriced && breakdown.LongContextApplied != test.long) {
				t.Fatalf("detail cost = %#v, want %g, unpriced=%t, long=%t", breakdown, test.want, test.unpriced, test.long)
			}
			var amount float64
			var unpriced bool
			if err := a.db.QueryRowContext(ctx, `SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&amount, &unpriced); err != nil {
				t.Fatal(err)
			}
			if amount != test.want || unpriced != test.unpriced {
				t.Fatalf("quota charge = %g/%t, want %g/%t", amount, unpriced, test.want, test.unpriced)
			}
			if _, created, err := a.saveUsageMessage(ctx, raw, pricing); err != nil || created {
				t.Fatalf("duplicate usage created=%t err=%v", created, err)
			}
			wantCosts[model] = test.want
			wantTotal = mathRound(wantTotal+test.want, 8)
			if test.unpriced {
				wantUnpriced++
			}
		})
	}
	pricing := quotaTestBillingPriceIndex(t, a)
	pricing.Versions, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start, end := time.Date(2026, 10, 25, 0, 0, 0, 0, appTimeLocation), peak.AddDate(0, 0, 1)
	assertClaudeTTLHistoricalCosts(t, a, pricing, start, end, wantCosts, wantUnpriced)
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != quota-wantTotal {
		t.Fatalf("quota balance = %v, want %g", user.QuotaLifetimeUSD, quota-wantTotal)
	}
	var charges int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_quota_charges`).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if charges != len(wantCosts) {
		t.Fatalf("quota ledger count = %d, want %d", charges, len(wantCosts))
	}
}

func assertClaudeTTLHistoricalCosts(t *testing.T, a *App, pricing modelPriceBillingIndex, start, end time.Time, wantCosts map[string]float64, wantUnpriced int) {
	t.Helper()
	ctx := context.Background()
	wantTotal := 0.0
	for _, cost := range wantCosts {
		wantTotal = mathRound(wantTotal+cost, 8)
	}
	filters := UsageFilters{Start: &start, End: &end}
	if !usageAnalyticsHourlyReadPlanFor(filters).useHourly {
		t.Fatal("historical test did not exercise hourly reads")
	}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true, Rankings: map[string]string{"model": "cost"}})
	if err := a.collectUsageAnalytics(ctx, filters, pricing, collector, "timestamp ASC"); err != nil {
		t.Fatal(err)
	}
	summary := collector.summaryResponse()
	if summary["estimated_cost_usd"] != wantTotal || summary["unpriced_records"] != wantUnpriced || summary["total_records"] != len(wantCosts) {
		t.Fatalf("historical summary = %#v, want cost %g/unpriced %d/records %d", summary, wantTotal, wantUnpriced, len(wantCosts))
	}
	ranking := collector.rankingResponse("model")["items"].([]map[string]any)
	if len(ranking) != len(wantCosts) {
		t.Fatalf("historical ranking length = %d, want %d", len(ranking), len(wantCosts))
	}
	for _, item := range ranking {
		_, model, _ := strings.Cut(item["key"].(string), "::")
		want, exists := wantCosts[model]
		if !exists || item["estimated_cost_usd"] != want {
			t.Fatalf("historical ranking = %#v, want model %q cost %g", item, model, want)
		}
	}
	var hourlyCost float64
	var hourlyUnpriced int
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(estimated_cost_usd), 0), COALESCE(SUM(unpriced_records), 0) FROM usage_analytics_hourly WHERE hour_start >= ? AND hour_start < ?`, dbTime(start), dbTime(end)).Scan(&hourlyCost, &hourlyUnpriced); err != nil {
		t.Fatal(err)
	}
	if mathRound(hourlyCost, 8) != wantTotal || hourlyUnpriced != wantUnpriced {
		t.Fatalf("persisted hourly cost = %g/%d, want %g/%d", hourlyCost, hourlyUnpriced, wantTotal, wantUnpriced)
	}
	records, err := a.keeperUsageRecordsInRange(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	usage := &keeperQuotaWindowUsage{WindowStart: start, WindowEnd: end}
	for _, record := range records {
		addRecordToKeeperQuotaWindowUsageVersioned(usage, record, pricing.Prices, pricing.Versions, pricing.MatchContext)
	}
	if usage.Records != len(wantCosts) || usage.EstimatedCostUSD != wantTotal || usage.UnpricedRecords != wantUnpriced {
		t.Fatalf("keeper fact-backed cost = %#v, want %g/%d", usage, wantTotal, wantUnpriced)
	}
}

func TestClaudeCacheTTLScalarCorrectionsAndRollbackRepairHistoricalCosts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	userID := seedQuotaTestUser(t, a, "ttl-correction-member")
	apiKey := "sk-ttl-correction"
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	quota := 20.0
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	model := "claude-ttl-correction"
	seedQuotaTestNativePrice(t, a, "claude", "ttl-correction-auth", model, 0)
	if _, err := a.db.ExecContext(ctx, `UPDATE model_prices SET cache_creation_usd_per_million = 2, cache_creation_1h_usd_per_million = 8 WHERE model = ?`, model); err != nil {
		t.Fatal(err)
	}
	pricing := quotaTestBillingPriceIndex(t, a)
	raw := []byte(`{"api_key":"sk-ttl-correction","provider":"claude","model":"claude-ttl-correction","auth":"api_key","auth_index":"ttl-correction-auth","timestamp":"2026-10-26T10:00:00+08:00","request_id":"ttl-correction","tokens":{"cache_creation_tokens":1000000,"cache_creation_ttl":"5m"}}`)
	record, created, err := a.saveUsageMessage(ctx, raw, pricing)
	if err != nil || !created {
		t.Fatalf("save correction record created=%t err=%v", created, err)
	}
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, appTimeLocation)
	end := start.AddDate(0, 0, 1)
	assertClaudeTTLHistoricalCosts(t, a, pricing, start, end, map[string]float64{model: 2}, 0)
	before, err := loadUsageAnalyticsState(ctx, a.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE usage_records SET raw_json = '' WHERE id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := a.usageAnalyticsFactsPending(ctx); err != nil || pending {
		t.Fatalf("retention-only clear queued repair: pending=%t err=%v", pending, err)
	}
	after, err := loadUsageAnalyticsState(ctx, a.db)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("retention-only clear invalidated analytics: before=%#v after=%#v", before, after)
	}
	for _, test := range []struct {
		name     string
		set      string
		cost     float64
		unpriced int
	}{
		{"ttl only", "cache_creation_ttl = '1h'", 8, 0},
		{"mixed", "cache_creation_ttl = 'mixed', cache_creation_5m_tokens = 400000, cache_creation_1h_tokens = 600000", 5.6, 0},
		{"5m bucket only invalidates", "cache_creation_5m_tokens = 500000", 0, 1},
		{"1h bucket only repairs", "cache_creation_1h_tokens = 500000", 5, 0},
		{"null TTL keeps legacy price", "cache_creation_ttl = NULL", 2, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := a.db.ExecContext(ctx, "UPDATE usage_records SET "+test.set+" WHERE id = ?", record.ID); err != nil {
				t.Fatal(err)
			}
			if pending, err := a.usageAnalyticsFactsPending(ctx); err != nil || !pending {
				t.Fatalf("scalar correction did not queue repair: pending=%t err=%v", pending, err)
			}
			if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
				t.Fatal(err)
			}
			state, err := loadUsageAnalyticsState(ctx, a.db)
			if err != nil {
				t.Fatal(err)
			}
			if !state.hourlyNeedsRebuild {
				t.Fatal("fact correction did not invalidate hourly costs")
			}
			stored, err := a.getUsageRecord(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			detail := versionedCostBreakdown(stored, pricing, true)
			if detail.TotalUSD != test.cost || detail.Unpriced != (test.unpriced == 1) {
				t.Fatalf("corrected detail cost = %#v, want %g/%d", detail, test.cost, test.unpriced)
			}
			assertClaudeTTLHistoricalCosts(t, a, pricing, start, end, map[string]float64{model: test.cost}, test.unpriced)
			var amount float64
			var charges int
			if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(amount_usd) FROM user_quota_charges`).Scan(&charges, &amount); err != nil {
				t.Fatal(err)
			}
			user, err := a.getUser(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			if charges != 1 || amount != 2 || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 18 {
				t.Fatalf("historical correction replayed quota: charges=%d amount=%g balance=%v", charges, amount, user.QuotaLifetimeUSD)
			}
		})
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE usage_records SET cache_creation_ttl = cache_creation_ttl, cache_creation_5m_tokens = cache_creation_5m_tokens, cache_creation_1h_tokens = cache_creation_1h_tokens WHERE id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := a.usageAnalyticsFactsPending(ctx); err != nil || pending {
		t.Fatalf("unchanged scalar update queued repair: pending=%t err=%v", pending, err)
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE usage_records SET cache_creation_ttl = '1h' WHERE id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	assertClaudeTTLHistoricalCosts(t, a, pricing, start, end, map[string]float64{model: 8}, 0)
	state, err := loadUsageAnalyticsState(ctx, a.db)
	if err != nil || state.hourlyNeedsRebuild {
		t.Fatalf("1h baseline was not current before rollback: %#v, err=%v", state, err)
	}
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.DownToContext(ctx, a.db, ".", 202610190001); err != nil {
		t.Fatalf("roll back TTL-priced hourly baseline: %v", err)
	}
	state, err = loadUsageAnalyticsState(ctx, a.db)
	if err != nil || !state.hourlyNeedsRebuild {
		t.Fatalf("rollback did not invalidate 1h hourly cost: %#v, err=%v", state, err)
	}
	prices, err := listPricesWithQueryer(ctx, a.db)
	if err != nil || len(prices) != 1 {
		t.Fatalf("read rolled-back prices: %#v, err=%v", prices, err)
	}
	if prices[0].CacheCreationUSDPerMillion != 2 || prices[0].CacheCreation1hUSDPerMillion != nil {
		t.Fatalf("rollback price did not retain the generic rate: %#v", prices[0])
	}
	if err := a.rebuildUsageAnalyticsHourly(ctx, pricing); err != nil {
		t.Fatalf("rebuild rolled-back facts and price schema: %v", err)
	}
	var legacyCost float64
	if err := a.db.QueryRowContext(ctx, `SELECT SUM(estimated_cost_usd) FROM usage_analytics_hourly`).Scan(&legacyCost); err != nil {
		t.Fatal(err)
	}
	if legacyCost != 2 {
		t.Fatalf("rolled-back hourly cost = %g, want generic cache price 2", legacyCost)
	}
}
