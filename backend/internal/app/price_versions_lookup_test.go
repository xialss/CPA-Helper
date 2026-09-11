package app

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestVersionedCostPreservesBillingUnitWithoutHistoricalPrice(t *testing.T) {
	for _, tc := range []struct {
		name, model, unit, historicalUnit string
		tokens                            int
		failed, baseline, atVersion       bool
		wantUnit                          string
		wantCost                          float64
		wantUnpriced                      bool
	}{
		{name: "successful request without tokens", model: "text-job", unit: modelBillingUnitRequest, wantUnit: modelBillingUnitRequest, wantUnpriced: true},
		{name: "failed request without tokens", model: "text-job", unit: modelBillingUnitRequest, failed: true, wantUnit: modelBillingUnitRequest},
		{name: "failed request with tokens", model: "text-job", unit: modelBillingUnitRequest, tokens: 1000, failed: true, wantUnit: modelBillingUnitRequest},
		{name: "token model without tokens", model: "text-job", unit: modelBillingUnitToken, wantUnit: modelBillingUnitToken},
		{name: "image explicitly billed by tokens", model: "image-job", unit: modelBillingUnitToken, wantUnit: modelBillingUnitToken},
		{name: "token usage without history", model: "image-job", unit: modelBillingUnitToken, tokens: 1000, wantUnit: modelBillingUnitToken, wantUnpriced: true},
		{name: "request at effective boundary", model: "text-job", unit: modelBillingUnitRequest, atVersion: true, wantUnit: modelBillingUnitRequest, wantCost: 0.04},
		{name: "request before baseline", model: "text-job", unit: modelBillingUnitRequest, baseline: true, wantUnit: modelBillingUnitRequest, wantCost: 0.04},
		{name: "historical request unit overrides current token unit", model: "text-job", unit: modelBillingUnitToken, historicalUnit: modelBillingUnitRequest, atVersion: true, wantUnit: modelBillingUnitRequest, wantCost: 0.04},
		{name: "historical token unit overrides current request unit", model: "text-job", unit: modelBillingUnitRequest, historicalUnit: modelBillingUnitToken, tokens: 1000, atVersion: true, wantUnit: modelBillingUnitToken, wantCost: 0.003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brand, provider, requestUSD := string(aiProviderBrandOpenAICompatibility), "jobs", 0.04
			price := ModelPrice{ID: 22, PriceScope: modelPriceScopeChannel, Provider: provider, Model: tc.model,
				ChannelBrand: &brand, ChannelKey: &provider, BillingUnit: tc.unit, RequestUSD: &requestUSD, InputUSDPerMillion: 3}
			at := time.Date(2026, 9, 11, 0, 0, 0, 123456000, time.UTC)
			key := channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, provider, tc.model)
			version := price
			if tc.historicalUnit != "" {
				version.BillingUnit = tc.historicalUnit
			}
			versions := modelPriceVersionIndex{}
			versions.add(key, modelPriceVersionFromPrice(version, at, tc.baseline))
			pricing := modelPriceBillingIndex{
				Prices: modelPriceIndex{key: price}, Versions: versions,
				MatchContext: modelPriceMatchContext{Selectors: modelPriceChannelSelectorIndex{}, SelectorsRequired: true, SelectorsAvailable: true},
			}
			record := UsageRecord{Provider: &provider, Model: &tc.model, Timestamp: at.Add(-time.Microsecond),
				InputTokens: tc.tokens, TotalTokens: tc.tokens, Failed: tc.failed}
			if tc.atVersion {
				record.Timestamp = at
			}
			for _, collectItems := range []bool{true, false} {
				breakdown := versionedCostBreakdown(record, pricing, collectItems)
				if breakdown.BillingUnit != tc.wantUnit || breakdown.TotalUSD != tc.wantCost || breakdown.Unpriced != tc.wantUnpriced {
					t.Errorf("breakdown (items=%v) = %#v, want unit/cost/unpriced %s/%v/%v", collectItems, breakdown, tc.wantUnit, tc.wantCost, tc.wantUnpriced)
				}
				if tc.wantUnpriced {
					if breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelUnpriced || len(breakdown.Items) != 0 {
						t.Errorf("missing historical price must expose its reason and no charge items: %#v", breakdown)
					}
				} else if breakdown.UnpricedReason != nil {
					t.Errorf("priced or exempt record has unpriced reason: %#v", breakdown)
				}
				if collectItems && breakdown.Items == nil {
					t.Error("detail items must be a non-null array")
				}
				if !collectItems && breakdown.Items != nil {
					t.Error("total-only calculation allocated detail items")
				}
			}
			if cost, unpriced := recordCostWithBilling(record, pricing); cost != tc.wantCost || unpriced != tc.wantUnpriced {
				t.Errorf("billing cost/unpriced = %v/%v, want %v/%v", cost, unpriced, tc.wantCost, tc.wantUnpriced)
			}
			if cost, unpriced := recordCostWithVersions(record, pricing.Prices, versions); cost != tc.wantCost || unpriced != tc.wantUnpriced {
				t.Errorf("context-free cost/unpriced = %v/%v, want %v/%v", cost, unpriced, tc.wantCost, tc.wantUnpriced)
			}
			item := listItemFromRecordVersioned(record, nil, pricing, usageRedactionOptions{})
			if item["estimated_cost_usd"] != tc.wantCost || item["unpriced"] != tc.wantUnpriced || item["cost_breakdown"].(usageCostBreakdown).BillingUnit != tc.wantUnit {
				t.Errorf("record response disagrees with expected billing: %#v", item)
			}
			wantUnpricedCount := 0
			if tc.wantUnpriced {
				wantUnpricedCount = 1
			}
			collector := newUsageAnalyticsCollector(UsageFilters{}, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
			collector.setBillingPriceIndex(pricing)
			collector.Add(record)
			if collector.summary.unpriced != wantUnpricedCount || collector.summary.estimated != tc.wantCost {
				t.Errorf("aggregate disagrees with expected billing: %#v", collector.summary)
			}
			builder := newUsageAnalyticsHourlyBuilder(pricing.Prices, pricing.Versions, pricing.MatchContext)
			builder.add(record)
			if len(builder.groups) != 1 {
				t.Fatalf("hourly groups = %d, want 1", len(builder.groups))
			}
			for _, group := range builder.groups {
				if group.unpricedRecords != int64(wantUnpricedCount) || group.estimatedCostUSD != tc.wantCost {
					t.Errorf("hourly aggregate disagrees with expected billing: %#v", group)
				}
			}
		})
	}
}

func TestVersionedCostPreservesRemovedCompatibleChannelTokenSemantics(t *testing.T) {
	for _, provider := range []string{"claude", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			brand, model := string(aiProviderBrandOpenAICompatibility), "historical-compatible"
			price := ModelPrice{ID: 22, PriceScope: modelPriceScopeChannel, Provider: provider, Model: model,
				ChannelBrand: &brand, ChannelKey: &provider, BillingUnit: modelBillingUnitToken,
				InputUSDPerMillion: 4, CacheReadUSDPerMillion: 1, CacheCreationUSDPerMillion: 2}
			at := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
			key := channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, provider, model)
			versions := modelPriceVersionIndex{}
			versions.add(key, modelPriceVersionFromPrice(price, at, false))
			pricing := modelPriceBillingIndex{
				Prices:   modelPriceIndex{key: price},
				Versions: versions,
				// The current provider snapshot confirms that this channel has been
				// removed. Its stored price still proves the compatible brand.
				MatchContext: modelPriceMatchContext{Selectors: modelPriceChannelSelectorIndex{}, SelectorsRequired: true, SelectorsAvailable: true},
			}
			record := UsageRecord{Provider: &provider, Model: &model, Timestamp: at.Add(-time.Microsecond),
				InputTokens: 1000, CacheReadTokens: 200, CacheCreationTokens: 100, TotalTokens: 1000}
			breakdown := versionedCostBreakdown(record, pricing, true)
			if breakdown.NormalInputTokens != 700 || breakdown.ContextInputTokens != 1000 || breakdown.CacheReadTokens != 200 || breakdown.CacheCreationTokens != 100 {
				t.Errorf("pre-version token semantics = normal/context/read/write %d/%d/%d/%d, want 700/1000/200/100",
					breakdown.NormalInputTokens, breakdown.ContextInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens)
			}
			if !breakdown.Unpriced || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 ||
				breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelUnpriced {
				t.Errorf("request before first non-baseline version must stay unpriced: %#v", breakdown)
			}
			if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
				t.Errorf("pre-version billing cost = %v/%v, want 0/true", cost, unpriced)
			}
			collector := newUsageAnalyticsCollector(UsageFilters{}, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
			collector.setBillingPriceIndex(pricing)
			collector.Add(record)
			if collector.summary.normalInput != breakdown.NormalInputTokens || collector.summary.input != breakdown.ContextInputTokens || collector.summary.unpriced != 1 {
				t.Errorf("detail and aggregate token semantics disagree: detail=%#v summary=%#v", breakdown, collector.summary)
			}
			builder := newUsageAnalyticsHourlyBuilder(pricing.Prices, pricing.Versions, pricing.MatchContext)
			builder.add(record)
			for _, group := range builder.groups {
				if group.normalInputTokens != int64(breakdown.NormalInputTokens) || group.aggregateInputTokens != int64(breakdown.ContextInputTokens) || group.unpricedRecords != 1 {
					t.Errorf("detail and hourly token semantics disagree: detail=%#v hourly=%#v", breakdown, group)
				}
			}

			// Omitting a match context must remain different from supplying an
			// unavailable selector context, including in the versioned entry point.
			record.Timestamp = at
			if cost, unpriced := recordCostWithVersions(record, pricing.Prices, pricing.Versions); cost != 0.0032 || unpriced {
				t.Errorf("context-free versioned cost = %v/%v, want 0.0032/false", cost, unpriced)
			}
			if cost, unpriced := recordCostWithVersions(record, pricing.Prices, pricing.Versions, modelPriceMatchContext{}); cost != 0 || !unpriced {
				t.Errorf("explicit unavailable selector cost = %v/%v, want 0/true", cost, unpriced)
			}
		})
	}
}

func TestLoadModelPriceVersionsPreservesLifecycleLookupSemantics(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	at := time.Date(2026, 9, 10, 0, 0, 0, 123456000, time.UTC)
	brand, channel := string(aiProviderBrandOpenAICompatibility), "lookup"
	for _, fixture := range []struct {
		model    string
		priceID  any
		offset   time.Duration
		input    float64
		baseline bool
	}{
		{"legacy", nil, 0, 1, true},
		{"mixed", 11, -2 * time.Hour, 2, true},
		{"mixed", 22, time.Hour, 5, false}, // Insert out of time order.
		{"mixed", nil, -3 * time.Hour, 1, true},
		{"mixed", 22, 0, 3, false},
		{"mixed", 22, 0, 4, false}, // Equal times use the larger version ID.
	} {
		if _, err := a.db.Exec(`INSERT INTO model_price_versions (
			price_id, provider, model, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
			billing_unit, effective_at, baseline) VALUES (?, 'openai', ?, NULL, ?, ?, ?, 0, 0, 0, 'token', ?, ?)`,
			fixture.priceID, fixture.model, brand, channel, fixture.input, dbTime(at.Add(fixture.offset)), fixture.baseline); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := a.loadModelPriceVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		model   string
		priceID int
		offset  time.Duration
		want    float64
		missing bool
	}{
		{"legacy baseline before start", "legacy", 22, -time.Microsecond, 1, false},
		{"legacy chain without lifecycle IDs", "legacy", 22, time.Hour, 1, false},
		{"prior lifecycle baseline", "mixed", 11, -4 * time.Hour, 2, false},
		{"prior lifecycle after recreation", "mixed", 11, 2 * time.Hour, 2, false},
		{"recreated lifecycle cannot inherit baseline", "mixed", 22, -time.Microsecond, 0, true},
		{"equal-time latest version", "mixed", 22, 0, 4, false},
		{"future version stays inactive", "mixed", 22, time.Hour - time.Microsecond, 4, false},
		{"future version exact boundary", "mixed", 22, time.Hour, 5, false},
		{"new lifecycle with no version", "mixed", 33, 2 * time.Hour, 99, false},
		{"unidentified legacy caller keeps full chain", "mixed", 0, 0, 4, false},
		{"unversioned channel", "absent", 22, 0, 99, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price := ModelPrice{ID: tc.priceID, Model: tc.model, PriceScope: modelPriceScopeChannel,
				ChannelBrand: &brand, ChannelKey: &channel, InputUSDPerMillion: 99}
			record := UsageRecord{Timestamp: at.Add(tc.offset)}
			got := resolveVersionedPrice(record, &price, versions)
			if tc.missing {
				if got != nil {
					t.Fatalf("expected no historical price, got %#v", got)
				}
				return
			}
			if got == nil || got.InputUSDPerMillion != tc.want {
				t.Fatalf("resolved price = %#v, want input %v", got, tc.want)
			}
			if got != &price {
				got.InputUSDPerMillion++
				again := resolveVersionedPrice(record, &price, versions)
				if again.InputUSDPerMillion != tc.want {
					t.Fatal("changing a selected value mutated the shared version index")
				}
			}
		})
	}
}

func BenchmarkResolveVersionedPrice(b *testing.B) {
	for _, count := range []int{1, 365, 3650} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			brand, channel := string(aiProviderBrandOpenAICompatibility), "version-benchmark"
			price := ModelPrice{ID: 42, Model: "gpt-version-benchmark", PriceScope: modelPriceScopeChannel,
				ChannelBrand: &brand, ChannelKey: &channel, BillingUnit: modelBillingUnitToken}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			key := channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, channel, price.Model)
			versions := modelPriceVersionIndex{}
			for i := 0; i < count; i++ {
				price.InputUSDPerMillion = float64(i + 1)
				versions.add(key, modelPriceVersionFromPrice(price, start.Add(time.Duration(i)*24*time.Hour), i == 0))
			}
			record := UsageRecord{Timestamp: start.Add(time.Duration(count) * 24 * time.Hour)}
			var selected *ModelPrice
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				selected = resolveVersionedPrice(record, &price, versions)
			}
			b.StopTimer()
			if selected == nil || selected.InputUSDPerMillion != float64(count) {
				b.Fatalf("wrong selected price: %#v", selected)
			}
		})
	}
}
