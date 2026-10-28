package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func TestClaudeCacheTTLCostUsesTTLPriceAndValidatesMixedBuckets(t *testing.T) {
	price := ModelPrice{
		Provider:                     "anthropic",
		Model:                        "claude-ttl-test",
		PriceScope:                   modelPriceScopeLibrary,
		InputUSDPerMillion:           0,
		OutputUSDPerMillion:          0,
		CacheCreationUSDPerMillion:   2,
		CacheCreation1hUSDPerMillion: claudeTTLFloatPtr(6),
		BillingUnit:                  modelBillingUnitToken,
	}
	makeRecord := func(ttl string) UsageRecord {
		model := price.Model
		provider := "claude"
		return UsageRecord{
			Provider:            &provider,
			Model:               &model,
			CacheCreationTokens: 10,
			CacheCreationTTL:    &ttl,
		}
	}
	tests := []struct {
		name         string
		record       UsageRecord
		missing1h    bool
		wantKind     string
		wantTotal    float64
		wantUnpriced bool
	}{
		{name: "5m", record: makeRecord("5m"), wantKind: usageCostKindCacheCreation5m, wantTotal: 0.00002},
		{name: "1h", record: makeRecord("1h"), wantKind: usageCostKindCacheCreation1h, wantTotal: 0.00006},
		{name: "unknown keeps legacy rate", record: makeRecord("unknown"), wantKind: usageCostKindCacheCreation, wantTotal: 0.00002},
		{name: "missing 1h price", record: makeRecord("1h"), missing1h: true, wantUnpriced: true},
		{name: "mixed missing 5m bucket", record: func() UsageRecord { r := makeRecord("mixed"); r.CacheCreation1hTokens = claudeTTLIntPtr(10); return r }(), wantUnpriced: true},
		{name: "mixed missing 1h bucket", record: func() UsageRecord { r := makeRecord("mixed"); r.CacheCreation5mTokens = claudeTTLIntPtr(10); return r }(), wantUnpriced: true},
		{name: "mixed bucket sum mismatch", record: func() UsageRecord {
			r := makeRecord("mixed")
			r.CacheCreation5mTokens = claudeTTLIntPtr(4)
			r.CacheCreation1hTokens = claudeTTLIntPtr(5)
			return r
		}(), wantUnpriced: true},
		{name: "mixed negative bucket", record: func() UsageRecord {
			r := makeRecord("mixed")
			r.CacheCreation5mTokens = claudeTTLIntPtr(-1)
			r.CacheCreation1hTokens = claudeTTLIntPtr(11)
			return r
		}(), wantUnpriced: true},
		{name: "mixed exact buckets", record: func() UsageRecord {
			r := makeRecord("mixed")
			r.CacheCreation5mTokens = claudeTTLIntPtr(5)
			r.CacheCreation1hTokens = claudeTTLIntPtr(5)
			return r
		}(), wantTotal: 0.00004},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			currentPrice := price
			if test.missing1h {
				currentPrice.CacheCreation1hUSDPerMillion = nil
			}
			breakdown := calculateRecordCostForMatch(test.record, &currentPrice, priceMatchStatusMatched, nil, true)
			if breakdown.Unpriced != test.wantUnpriced {
				t.Fatalf("unpriced = %t, want %t; breakdown=%#v", breakdown.Unpriced, test.wantUnpriced, breakdown)
			}
			if test.wantUnpriced {
				if breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
					t.Fatalf("invalid TTL cost = %v with items %#v, want zero and no items", breakdown.TotalUSD, breakdown.Items)
				}
				return
			}
			if breakdown.TotalUSD != test.wantTotal {
				t.Fatalf("total USD = %v, want %v", breakdown.TotalUSD, test.wantTotal)
			}
			if test.wantKind == "" {
				if len(breakdown.Items) != 2 {
					t.Fatalf("mixed cost items = %#v, want two buckets", breakdown.Items)
				}
				for i, wantKind := range []string{usageCostKindCacheCreation5m, usageCostKindCacheCreation1h} {
					item, ok := breakdown.Items[i].(usageTokenCostBreakdownItem)
					if !ok || item.Kind != wantKind {
						t.Fatalf("mixed item %d = %#v, want %q", i, breakdown.Items[i], wantKind)
					}
				}
				return
			}
			if len(breakdown.Items) != 1 {
				t.Fatalf("cost items = %#v, want one cache-write item", breakdown.Items)
			}
			item, ok := breakdown.Items[0].(usageTokenCostBreakdownItem)
			if !ok || item.Kind != test.wantKind {
				t.Fatalf("cache-write item = %#v, want kind %q", breakdown.Items[0], test.wantKind)
			}
		})
	}

	zeroPrice := ModelPrice{
		Provider: "claude", Model: price.Model, PriceScope: modelPriceScopeLibrary,
		CacheCreationUSDPerMillion: 2, CacheCreation1hUSDPerMillion: claudeTTLFloatPtr(0),
		BillingUnit: modelBillingUnitToken,
	}
	zero := calculateRecordCostForMatch(makeRecord("1h"), &zeroPrice, priceMatchStatusMatched, nil, true)
	if zero.Unpriced || zero.TotalUSD != 0 || len(zero.Items) != 1 {
		t.Fatalf("explicit zero 1h price = %#v, want priced zero with item", zero)
	}
	zeroItem, ok := zero.Items[0].(usageTokenCostBreakdownItem)
	if !ok || zeroItem.Kind != usageCostKindCacheCreation1h || zeroItem.SubtotalUSD != 0 {
		t.Fatalf("explicit zero 1h item = %#v", zero.Items[0])
	}

	noCache := makeRecord("1h")
	noCache.CacheCreationTokens = 0
	noCache.CacheCreationTTL = nil
	if result := calculateRecordCostForMatch(noCache, &zeroPrice, priceMatchStatusMatched, nil, true); result.Unpriced || result.TotalUSD != 0 {
		t.Fatalf("no-cache record cost = %#v, want no charge", result)
	}
}

func TestClaudeCacheTTLIsRejectedForNonClaudeAndGenericPriceIsUnchanged(t *testing.T) {
	rate1h := 88.0
	payload := modelPricePayload{
		Provider: "openai", Model: "gpt-ttl-isolation", PriceScope: modelPriceScopeLibrary,
		BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 1, OutputUSDPerMillion: 1,
		CacheCreationUSDPerMillion: 2, CacheCreation1hUSDPerMillion: &rate1h,
	}
	if _, err := validatePricePayload(payload); err == nil {
		t.Fatal("non-Claude 1h cache price was accepted")
	}
	payload.CacheCreation1hUSDPerMillion = nil
	payload.LongContext = &modelPriceLongContextPayload{
		ThresholdInputTokens: claudeTTLInt64Ptr(1000), InputUSDPerMillion: claudeTTLFloatPtr(1),
		OutputUSDPerMillion: claudeTTLFloatPtr(1), CacheReadUSDPerMillion: claudeTTLFloatPtr(0),
		CacheCreationUSDPerMillion: claudeTTLFloatPtr(2), CacheCreation1hUSDPerMillion: &rate1h,
	}
	if _, err := validatePricePayload(payload); err == nil {
		t.Fatal("non-Claude long-context 1h cache price was accepted")
	}

	provider, model, ttl := "openai", "gpt-ttl-isolation", "1h"
	price := ModelPrice{
		Provider: provider, Model: model, PriceScope: modelPriceScopeLibrary,
		InputUSDPerMillion: 1, CacheCreationUSDPerMillion: 2,
		CacheCreation1hUSDPerMillion: &rate1h, BillingUnit: modelBillingUnitToken,
	}
	record := UsageRecord{
		Provider: &provider, Model: &model, InputTokens: 10, CacheCreationTokens: 4,
		CacheCreationTTL: &ttl, TotalTokens: 10,
	}
	breakdown := calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
	if breakdown.Unpriced || breakdown.TotalUSD != 0.000014 {
		t.Fatalf("non-Claude generic cache-write cost = %#v, want 0.000014 priced", breakdown)
	}
	var cacheWriteItems int
	for _, raw := range breakdown.Items {
		item, ok := raw.(usageTokenCostBreakdownItem)
		if ok && strings.HasPrefix(item.Kind, "cache_creation") {
			cacheWriteItems++
			if item.Kind != usageCostKindCacheCreation || item.USDPerMillion != 2 {
				t.Fatalf("non-Claude cache-write item = %#v, want generic rate 2", item)
			}
		}
	}
	if cacheWriteItems != 1 {
		t.Fatalf("non-Claude cache-write item count = %d, want one generic item: %#v", cacheWriteItems, breakdown.Items)
	}
}

func TestClaudeCacheTTLPriceProjectionAndVersionRoundTrip(t *testing.T) {
	base1h, long1h, multiplier := 0.0, 9.5, 1.75
	nonClaudeAPI := modelPriceForAPI(ModelPrice{
		Provider: "openai", Model: "gpt-ttl-api", PriceScope: modelPriceScopeLibrary,
		CacheCreation1hUSDPerMillion: &base1h,
		LongContext:                  &ModelPriceLongContext{ThresholdInputTokens: 1000, InputUSDPerMillion: 1, OutputUSDPerMillion: 1, CacheReadUSDPerMillion: 1, CacheCreationUSDPerMillion: 1, CacheCreation1hUSDPerMillion: &long1h},
		PreservedLongContext:         &ModelPriceLibraryConflictLongContext{CacheCreation1hUSDPerMillion: &long1h, NonFiniteFields: map[string]string{"cache_creation_1h_usd_per_million": "NaN", "input_usd_per_million": "NaN"}},
	})
	if nonClaudeAPI.CacheCreation1hUSDPerMillion != nil || nonClaudeAPI.LongContext.CacheCreation1hUSDPerMillion != nil || nonClaudeAPI.PreservedLongContext.CacheCreation1hUSDPerMillion != nil {
		t.Fatalf("non-Claude model-price API projection exposed 1h price: %#v", nonClaudeAPI)
	}
	if _, exists := nonClaudeAPI.PreservedLongContext.NonFiniteFields["cache_creation_1h_usd_per_million"]; exists {
		t.Fatalf("non-Claude preserved audit exposed 1h field: %#v", nonClaudeAPI.PreservedLongContext.NonFiniteFields)
	}
	nonClaudeProjection := availableModelPriceFromPrice(&ModelPrice{
		Provider: "openai", Model: "gpt-ttl-projection", PriceScope: modelPriceScopeLibrary,
		CacheCreation1hUSDPerMillion: &base1h,
		LongContext:                  &ModelPriceLongContext{ThresholdInputTokens: 1000, InputUSDPerMillion: 1, OutputUSDPerMillion: 1, CacheCreation1hUSDPerMillion: &long1h},
	})
	if nonClaudeProjection.CacheCreation1hUSDPerMillion != nil || nonClaudeProjection.LongContext.CacheCreation1hUSDPerMillion != nil {
		t.Fatalf("non-Claude available-model projection exposed 1h price: %#v", nonClaudeProjection)
	}
	claudeProjection := availableModelPriceFromPrice(&ModelPrice{
		Provider: "anthropic", Model: "claude-ttl-projection", PriceScope: modelPriceScopeLibrary,
		CacheCreation1hUSDPerMillion: &base1h,
		LongContext:                  &ModelPriceLongContext{ThresholdInputTokens: 1000, InputUSDPerMillion: 1, OutputUSDPerMillion: 1, CacheCreation1hUSDPerMillion: &long1h},
	})
	if claudeProjection.CacheCreation1hUSDPerMillion == nil || *claudeProjection.CacheCreation1hUSDPerMillion != 0 || claudeProjection.LongContext.CacheCreation1hUSDPerMillion == nil || *claudeProjection.LongContext.CacheCreation1hUSDPerMillion != long1h {
		t.Fatalf("Claude available-model projection lost explicit 1h prices: %#v", claudeProjection)
	}

	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	provider, model, brand, key, auth := "anthropic", "claude-ttl-version", "claude", "account.json", modelPriceChannelAuthTypeAPIKey
	price := ModelPrice{
		Provider: provider, Model: model, PriceScope: modelPriceScopeChannel,
		ChannelAuthType: &auth, ChannelBrand: &brand, ChannelKey: &key,
		InputUSDPerMillion: 1, OutputUSDPerMillion: 2, CacheReadUSDPerMillion: 0.5,
		CacheCreationUSDPerMillion: 3, CacheCreation1hUSDPerMillion: &base1h,
		PriorityMultiplier: &multiplier, BillingUnit: modelBillingUnitToken,
		LongContext: &ModelPriceLongContext{
			ThresholdInputTokens: 200000, InputUSDPerMillion: 4, OutputUSDPerMillion: 5,
			CacheReadUSDPerMillion: 0.6, CacheCreationUSDPerMillion: 7,
			CacheCreation1hUSDPerMillion: &long1h,
		},
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.appendModelPriceVersion(ctx, tx, price, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), true); err != nil {
		_ = tx.Rollback()
		t.Fatalf("append Claude price version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	versions, err := a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatalf("load Claude price versions: %v", err)
	}
	history := versions[channelModelPriceKey(auth, brand, key, model)]
	if history == nil || len(history.all) != 1 {
		t.Fatalf("Claude version history = %#v, want one version", history)
	}
	loaded := history.all[0].Price
	if loaded.CacheCreation1hUSDPerMillion == nil || *loaded.CacheCreation1hUSDPerMillion != 0 || loaded.LongContext == nil || loaded.LongContext.CacheCreation1hUSDPerMillion == nil || *loaded.LongContext.CacheCreation1hUSDPerMillion != long1h || loaded.PriorityMultiplier == nil || *loaded.PriorityMultiplier != multiplier {
		t.Fatalf("Claude 1h/long-context/multiplier round-trip = %#v", loaded)
	}
	ttl := "1h"
	usage := UsageRecord{
		Provider: &provider, Model: &model, InputTokens: 200001,
		CacheCreationTokens: 10, CacheCreationTTL: &ttl,
	}
	brandValue := aiProviderBrandClaude
	longContextCost := calculateRecordCostForMatch(usage, &loaded, priceMatchStatusMatched, &brandValue, true)
	if longContextCost.Unpriced || !longContextCost.LongContextApplied {
		t.Fatalf("long-context Claude 1h calculation = %#v, want priced long-context tier", longContextCost)
	}
	var longContextCacheWrite *usageTokenCostBreakdownItem
	for _, raw := range longContextCost.Items {
		item, ok := raw.(usageTokenCostBreakdownItem)
		if ok && item.Kind == usageCostKindCacheCreation1h {
			longContextCacheWrite = &item
			break
		}
	}
	if longContextCacheWrite == nil || longContextCacheWrite.USDPerMillion != long1h || longContextCacheWrite.SubtotalUSD != 0.000095 {
		t.Fatalf("long-context 1h cache-write cost item = %#v, want %g/0.000095", longContextCacheWrite, long1h)
	}

	large := math.MaxFloat64 / 10
	if priorityMultiplierProducesRoundableCost(ModelPrice{
		Provider: "openai", Model: "gpt-ttl-multiplier", BillingUnit: modelBillingUnitToken,
		CacheCreation1hUSDPerMillion: &large,
	}, 2) {
		t.Fatal("multiplier safety check ignored the 1h cache rate")
	}
	if priorityMultiplierProducesRoundableCost(ModelPrice{
		Provider: "openai", Model: "gpt-ttl-multiplier", BillingUnit: modelBillingUnitToken,
		LongContext: &ModelPriceLongContext{ThresholdInputTokens: 1000, InputUSDPerMillion: 0, OutputUSDPerMillion: 0, CacheReadUSDPerMillion: 0, CacheCreationUSDPerMillion: 0, CacheCreation1hUSDPerMillion: &large},
	}, 2) {
		t.Fatal("multiplier safety check ignored long-context 1h cache rate")
	}
}

func TestNonClaudeCacheTTLFieldsStayHiddenFromAPIProjections(t *testing.T) {
	oneHour := 1.0
	price := modelPriceForAPI(ModelPrice{
		Provider:                     "openai",
		Model:                        "gpt-ttl-hidden",
		CacheCreation1hUSDPerMillion: &oneHour,
		TimePricing: &PriceTimeRule{OffpeakRates: &PriceOffpeakRates{
			CacheCreation1h: &oneHour,
		}},
	})
	if price.CacheCreation1hUSDPerMillion != nil || price.TimePricing == nil || price.TimePricing.OffpeakRates == nil || price.TimePricing.OffpeakRates.CacheCreation1h != nil {
		t.Fatalf("non-Claude price projection leaked 1h cache fields: %#v", price)
	}

	archived := &ModelPriceLibraryConflictLongContext{
		CacheCreation1hUSDPerMillion: &oneHour,
		NonFiniteFields:              map[string]string{"cache_creation_1h_usd_per_million": "+Inf", "input_usd_per_million": "+Inf"},
	}
	conflicts := modelPriceLibraryConflictsForAPI([]ModelPriceLibraryConflict{
		{Price: ModelPrice{Provider: "openai", Model: "gpt-ttl-hidden"}, ArchivedLongContext: archived},
	})
	if conflicts[0].ArchivedLongContext == nil || conflicts[0].ArchivedLongContext.CacheCreation1hUSDPerMillion != nil {
		t.Fatalf("non-Claude conflict projection leaked 1h cache field: %#v", conflicts[0])
	}
	if _, exists := conflicts[0].ArchivedLongContext.NonFiniteFields["cache_creation_1h_usd_per_million"]; exists {
		t.Fatal("non-Claude conflict projection leaked 1h audit field")
	}

	version := ModelPriceVersion{
		CacheCreation1hUSDPerMillion: &oneHour,
		LongContext:                  &ModelPriceLongContext{CacheCreation1hUSDPerMillion: &oneHour},
		PreservedLongContext:         archived,
		TimePricing:                  &PriceTimeRule{OffpeakRates: &PriceOffpeakRates{CacheCreation1h: &oneHour}},
	}
	stripNonClaudeVersionCacheTTL(&version)
	if version.CacheCreation1hUSDPerMillion != nil || version.LongContext.CacheCreation1hUSDPerMillion != nil || version.PreservedLongContext.CacheCreation1hUSDPerMillion != nil || version.TimePricing.OffpeakRates.CacheCreation1h != nil {
		t.Fatalf("non-Claude version projection leaked 1h cache fields: %#v", version)
	}
	if _, exists := version.PreservedLongContext.NonFiniteFields["cache_creation_1h_usd_per_million"]; exists {
		t.Fatal("non-Claude version projection leaked 1h audit field")
	}
	version.PreservedLongContext.NonFiniteFields["input_usd_per_million"] = "changed projection"
	if archived.CacheCreation1hUSDPerMillion != &oneHour || archived.NonFiniteFields["input_usd_per_million"] != "+Inf" || archived.NonFiniteFields["cache_creation_1h_usd_per_million"] != "+Inf" {
		t.Fatalf("API projection mutated shared audit values: %#v", archived)
	}
}

func TestClaudeCacheTTLUsagePersistsToFactsAndAPIProjection(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw := []byte(`{"provider":"claude","model":"claude-usage-ttl","timestamp":"2026-10-27T12:00:00Z","api_key":"usage-test-key","tokens":{"input_tokens":100,"output_tokens":7,"cache_creation_tokens":10,"cache_creation_ttl":"mixed","cache_creation_5m_tokens":4,"cache_creation_1h_tokens":6,"total_tokens":117}}`)
	record, created, err := a.saveUsageMessage(ctx, raw, modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%t err=%v", created, err)
	}
	stored, err := a.getUsageRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("getUsageRecord: %v", err)
	}
	if stored.CacheCreationTTL == nil || *stored.CacheCreationTTL != "mixed" || stored.CacheCreation5mTokens == nil || *stored.CacheCreation5mTokens != 4 || stored.CacheCreation1hTokens == nil || *stored.CacheCreation1hTokens != 6 {
		t.Fatalf("stored usage TTL buckets = %#v", stored)
	}
	var factTTL sql.NullString
	var fact5m, fact1h sql.NullInt64
	if err := a.db.QueryRowContext(ctx, `SELECT cache_creation_ttl, cache_creation_5m_tokens, cache_creation_1h_tokens FROM usage_analytics_facts WHERE usage_record_id = ?`, stored.ID).Scan(&factTTL, &fact5m, &fact1h); err != nil {
		t.Fatalf("query usage facts TTL: %v", err)
	}
	if !factTTL.Valid || factTTL.String != "mixed" || !fact5m.Valid || fact5m.Int64 != 4 || !fact1h.Valid || fact1h.Int64 != 6 {
		t.Fatalf("usage facts TTL = %v/%v/%v, want mixed/4/6", factTTL, fact5m, fact1h)
	}
	projected := listItemFromRecordWithBreakdown(stored, nil, usageRedactionOptions{}, usageCostBreakdown{BillingUnit: modelBillingUnitToken, CacheCreationTokens: 10})
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal usage API projection: %v", err)
	}
	var api map[string]any
	if err := json.Unmarshal(encoded, &api); err != nil {
		t.Fatalf("decode usage API projection: %v", err)
	}
	if api["cache_creation_ttl"] != "mixed" || api["cache_creation_5m_tokens"] != float64(4) || api["cache_creation_1h_tokens"] != float64(6) {
		t.Fatalf("usage API TTL projection = %#v", api)
	}
	if got := usageRecordCacheCreationTTL(UsageRecord{CacheCreationTokens: 5}); got == nil || *got != "unknown" {
		t.Fatalf("legacy aggregate-only TTL projection = %v, want unknown", got)
	}
	if got := usageRecordCacheCreationTTL(UsageRecord{}); got != nil {
		t.Fatalf("no-cache TTL projection = %v, want null", got)
	}

	invalid, err := normalizeUsage([]byte(`{"provider":"claude","tokens":{"cache_creation_tokens":10,"cache_creation_ttl":"mixed","cache_creation_5m_tokens":-1,"cache_creation_1h_tokens":11}}`))
	if err != nil {
		t.Fatalf("normalize invalid canonical bucket: %v", err)
	}
	if invalid.CacheCreationTTL == nil || *invalid.CacheCreationTTL != "mixed" || invalid.CacheCreation5mTokens != nil || invalid.CacheCreation1hTokens == nil || *invalid.CacheCreation1hTokens != 11 {
		t.Fatalf("invalid canonical buckets were not kept non-billable: %#v", invalid)
	}
}

func TestClaudeCacheTTLMigrationBackfillsOnlyCanonicalRawUsage(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202610190001)
	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	const timestamp = "2026-10-27T12:00:00Z"
	maxInt := int(^uint(0) >> 1)
	overflow := strconv.FormatUint(uint64(maxInt)+1, 10)
	records := []struct {
		id         int
		provider   string
		model      string
		cacheWrite int
		raw        string
	}{
		{41, "claude", "claude-migration-mixed", 10, `{"tokens":{"cache_creation_tokens":10,"cache_creation_ttl":"mixed","cache_creation_5m_tokens":4,"cache_creation_1h_tokens":6}}`},
		{42, "claude", "claude-migration-legacy", 10, `{"cache_ttl_setting":"1h","tokens":{"cache_creation_tokens":10}}`},
		{43, "claude", "claude-migration-empty", 7, ""},
		{44, "claude", "claude-migration-malformed", 7, "{"},
		{45, "claude", "claude-migration-integral-numbers", 10, `{"tokens":{"cache_creation_ttl":" mixed ","cache_creation_5m_tokens":4.0,"cache_creation_1h_tokens":6e0}}`},
		{46, "claude", "claude-migration-fractional", 10, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":4.5,"cache_creation_1h_tokens":6}}`},
		{47, "claude", "claude-migration-overflow", 10, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":` + overflow + `,"cache_creation_1h_tokens":0}}`},
		{48, "claude", "claude-migration-max-int", maxInt, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":` + strconv.Itoa(maxInt) + `,"cache_creation_1h_tokens":0}}`},
		{49, "claude", "claude-migration-negative", 10, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":-1,"cache_creation_1h_tokens":11}}`},
		{50, "claude", "claude-migration-nonnumeric", 10, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":"4","cache_creation_1h_tokens":true}}`},
		{51, "claude", "claude-migration-no-cache", 0, `{"tokens":{"cache_creation_ttl":"mixed","cache_creation_5m_tokens":4,"cache_creation_1h_tokens":6}}`},
		{52, "claude", "claude-migration-single-ttl", 10, `{"tokens":{"cache_creation_ttl":"1h","cache_creation_5m_tokens":4,"cache_creation_1h_tokens":6}}`},
		{53, "claude", "claude-migration-unknown-ttl", 10, `{"tokens":{"cache_creation_ttl":"unrecognized","cache_creation_5m_tokens":4,"cache_creation_1h_tokens":6}}`},
	}
	for _, record := range records {
		if _, err := db.ExecContext(ctx, `INSERT INTO usage_records (id, created_at, timestamp, provider, model, cache_creation_tokens, dedupe_key, raw_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, record.id, timestamp, timestamp, record.provider, record.model, record.cacheWrite, "ttl-migration-"+record.model, record.raw); err != nil {
			t.Fatalf("seed usage record %d: %v", record.id, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO usage_analytics_facts (usage_record_id, timestamp, provider, model, cache_creation_tokens) VALUES (?, ?, ?, ?, ?)`, record.id, timestamp, record.provider, record.model, record.cacheWrite); err != nil {
			t.Fatalf("seed usage fact %d: %v", record.id, err)
		}
	}
	for _, model := range []struct {
		provider string
		model    string
		cache    float64
	}{
		{"anthropic", "claude-ttl-migration-price", 2},
		{"openai", "gpt-legacy-cache-price", 5},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO model_prices (provider, model, cache_creation_usd_per_million, updated_at) VALUES (?, ?, ?, ?)`, model.provider, model.model, model.cache, timestamp); err != nil {
			t.Fatalf("seed existing cache price %s: %v", model.model, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE usage_analytics_state SET facts_version = 7, hourly_needs_rebuild = 0 WHERE id = 1`); err != nil {
		t.Fatalf("seed analytics state: %v", err)
	}
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade usage database with Claude TTL migration: %v", err)
	}
	for _, test := range []struct {
		id      int
		wantTTL string
		want5m  sql.NullInt64
		want1h  sql.NullInt64
	}{
		{41, "mixed", sql.NullInt64{Int64: 4, Valid: true}, sql.NullInt64{Int64: 6, Valid: true}},
		{42, "", sql.NullInt64{}, sql.NullInt64{}},
		{43, "", sql.NullInt64{}, sql.NullInt64{}},
		{44, "", sql.NullInt64{}, sql.NullInt64{}},
		{45, "mixed", sql.NullInt64{Int64: 4, Valid: true}, sql.NullInt64{Int64: 6, Valid: true}},
		{46, "mixed", sql.NullInt64{}, sql.NullInt64{Int64: 6, Valid: true}},
		{47, "mixed", sql.NullInt64{}, sql.NullInt64{Int64: 0, Valid: true}},
		{48, "mixed", sql.NullInt64{Int64: int64(maxInt), Valid: true}, sql.NullInt64{Int64: 0, Valid: true}},
		{49, "mixed", sql.NullInt64{}, sql.NullInt64{Int64: 11, Valid: true}},
		{50, "mixed", sql.NullInt64{}, sql.NullInt64{}},
		{51, "", sql.NullInt64{}, sql.NullInt64{}},
		{52, "1h", sql.NullInt64{}, sql.NullInt64{}},
		{53, "", sql.NullInt64{}, sql.NullInt64{}},
	} {
		var ttl sql.NullString
		var five, oneHour sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT cache_creation_ttl, cache_creation_5m_tokens, cache_creation_1h_tokens FROM usage_records WHERE id = ?`, test.id).Scan(&ttl, &five, &oneHour); err != nil {
			t.Fatalf("query migrated usage %d: %v", test.id, err)
		}
		if ttl.Valid != (test.wantTTL != "") || (ttl.Valid && ttl.String != test.wantTTL) || five != test.want5m || oneHour != test.want1h {
			t.Errorf("migrated usage %d = %v/%v/%v, want %q/%v/%v", test.id, ttl, five, oneHour, test.wantTTL, test.want5m, test.want1h)
		}
		var factTTL sql.NullString
		var fact5m, fact1h sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT cache_creation_ttl, cache_creation_5m_tokens, cache_creation_1h_tokens FROM usage_analytics_facts WHERE usage_record_id = ?`, test.id).Scan(&factTTL, &fact5m, &fact1h); err != nil {
			t.Fatalf("query migrated usage fact %d: %v", test.id, err)
		}
		if factTTL != ttl || fact5m != five || fact1h != oneHour {
			t.Errorf("migrated usage fact %d = %v/%v/%v, want record projection %v/%v/%v", test.id, factTTL, fact5m, fact1h, ttl, five, oneHour)
		}
		var raw string
		var aggregate int
		if err := db.QueryRowContext(ctx, `SELECT raw_json, cache_creation_tokens FROM usage_records WHERE id = ?`, test.id).Scan(&raw, &aggregate); err != nil {
			t.Fatal(err)
		}
		var payload any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err == nil {
			_, live5m, live1h := normalizedCacheCreationTTL(payload, aggregate)
			if five != sqlNullIntForClaudeTTLTest(live5m) || oneHour != sqlNullIntForClaudeTTLTest(live1h) {
				t.Errorf("migration/live bucket mismatch for %d: migration %v/%v, live %v/%v", test.id, five, oneHour, live5m, live1h)
			}
		}
	}
	var factsVersion int
	var hourlyNeedsRebuild bool
	if err := db.QueryRowContext(ctx, `SELECT facts_version, hourly_needs_rebuild FROM usage_analytics_state WHERE id = 1`).Scan(&factsVersion, &hourlyNeedsRebuild); err != nil {
		t.Fatalf("query migrated analytics state: %v", err)
	}
	if factsVersion != 8 || !hourlyNeedsRebuild {
		t.Fatalf("migrated analytics state = version %d rebuild %t, want 8/true", factsVersion, hourlyNeedsRebuild)
	}
	for _, test := range []struct {
		provider string
		model    string
		cache    float64
	}{
		{"anthropic", "claude-ttl-migration-price", 2},
		{"openai", "gpt-legacy-cache-price", 5},
	} {
		var cache, oneHour sql.NullFloat64
		if err := db.QueryRowContext(ctx, `SELECT cache_creation_usd_per_million, cache_creation_1h_usd_per_million FROM model_prices WHERE provider = ? AND model = ?`, test.provider, test.model).Scan(&cache, &oneHour); err != nil {
			t.Fatalf("query migrated model price %s: %v", test.model, err)
		}
		if !cache.Valid || cache.Float64 != test.cache || oneHour.Valid {
			t.Errorf("migrated model price %s = %v/%v, want unchanged generic/5m %v and unconfigured 1h", test.model, cache, oneHour, test.cache)
		}
	}
	if err := goose.DownToContext(ctx, db, ".", 202610190001); err != nil {
		t.Fatalf("roll back Claude TTL migration: %v", err)
	}
	var trigger string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'usage_analytics_pending_facts_update'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(trigger, "cache_creation_ttl") || strings.Contains(trigger, "cache_creation_5m_tokens") || strings.Contains(trigger, "cache_creation_1h_tokens") {
		t.Fatal("rollback retained TTL references in the pending-fact trigger")
	}
}

func sqlNullIntForClaudeTTLTest(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func claudeTTLFloatPtr(value float64) *float64 { return &value }
func claudeTTLIntPtr(value int) *int           { return &value }
func claudeTTLInt64Ptr(value int64) *int64     { return &value }
