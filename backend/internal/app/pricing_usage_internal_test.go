package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecordCostUsesClaudeCacheReadAndCreationTokens(t *testing.T) {
	provider := "claude"
	model := "claude-sonnet-test"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        10,
		CachedTokens:        999,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		TotalTokens:         110,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("anthropic", model): {
			Provider:                   "anthropic",
			Model:                      model,
			InputUSDPerMillion:         10,
			OutputUSDPerMillion:        20,
			CacheReadUSDPerMillion:     1,
			CacheCreationUSDPerMillion: 12,
		},
	}

	amount, unpriced := recordCost(record, prices)
	if unpriced {
		t.Fatal("record should be priced")
	}
	want := mathRound((100*10+20*1+30*12+10*20)/1_000_000.0, 8)
	if amount != want {
		t.Fatalf("cost = %v, want %v", amount, want)
	}
	breakdown := calculateRecordCostBreakdown(record, prices)
	if breakdown.NormalInputTokens != 100 || breakdown.CacheReadTokens != 20 || breakdown.CacheCreationTokens != 30 {
		t.Fatalf("token breakdown = input %d read %d creation %d, want 100/20/30", breakdown.NormalInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens)
	}
	if len(breakdown.Items) != 4 {
		t.Fatalf("cost breakdown item count = %d, want 4", len(breakdown.Items))
	}
}

func TestRecordCostBreakdownIncludesOpenAICacheWritesInInputWithoutSeparatePrice(t *testing.T) {
	provider := "codex"
	model := "gpt-5.5"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        20,
		CachedTokens:        30,
		CacheCreationTokens: 40,
		TotalTokens:         120,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("openai", model): {
			Provider:               "openai",
			Model:                  model,
			InputUSDPerMillion:     10,
			OutputUSDPerMillion:    20,
			CacheReadUSDPerMillion: 1,
		},
	}

	breakdown := calculateRecordCostBreakdown(record, prices)
	if breakdown.Unpriced {
		t.Fatal("record should be priced")
	}
	if breakdown.NormalInputTokens != 30 || breakdown.CacheReadTokens != 30 || breakdown.CacheCreationTokens != 40 || breakdown.OutputTokens != 20 {
		t.Fatalf("token breakdown = input %d read %d creation %d output %d, want 30/30/40/20", breakdown.NormalInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens, breakdown.OutputTokens)
	}
	if len(breakdown.Items) != 3 {
		t.Fatalf("cost breakdown item count = %d, want 3", len(breakdown.Items))
	}
	wantKinds := []string{usageCostKindInput, usageCostKindCacheRead, usageCostKindOutput}
	wantTokens := []int{70, 30, 20}
	wantSubtotals := []float64{0.0007, 0.00003, 0.0004}
	for index, item := range breakdown.Items {
		tokenItem, ok := item.(usageTokenCostBreakdownItem)
		if !ok {
			t.Fatalf("cost breakdown item %d type = %T, want usageTokenCostBreakdownItem", index, item)
		}
		if tokenItem.Kind != wantKinds[index] || tokenItem.Tokens != wantTokens[index] || tokenItem.SubtotalUSD != wantSubtotals[index] {
			t.Fatalf("cost breakdown item %d = kind %q tokens %d subtotal %v, want %q/%d/%v", index, tokenItem.Kind, tokenItem.Tokens, tokenItem.SubtotalUSD, wantKinds[index], wantTokens[index], wantSubtotals[index])
		}
	}
	if breakdown.TotalUSD != 0.00113 {
		t.Fatalf("total cost = %v, want 0.00113", breakdown.TotalUSD)
	}
	amount, unpriced := recordCost(record, prices)
	if amount != breakdown.TotalUSD || unpriced {
		t.Fatalf("recordCost = %v/%v, want %v/false", amount, unpriced, breakdown.TotalUSD)
	}
}

func TestRecordCostAppliesPriorityMultiplierForCodex(t *testing.T) {
	provider := "codex"
	model := "gpt-5.5"
	serviceTier := "priority"
	multiplier := 2.5
	record := UsageRecord{
		Provider:     &provider,
		Model:        &model,
		ServiceTier:  &serviceTier,
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		TotalTokens:  2_000_000,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("openai", model): {
			Provider:            "openai",
			Model:               model,
			InputUSDPerMillion:  2,
			OutputUSDPerMillion: 4,
			PriorityMultiplier:  &multiplier,
		},
	}

	breakdown := calculateRecordCostBreakdown(record, prices)
	if breakdown.Unpriced || breakdown.TotalUSD != 15 {
		t.Fatalf("priority breakdown = %#v, want priced total 15", breakdown)
	}
	if breakdown.TierMultiplier == nil || *breakdown.TierMultiplier != 2.5 {
		t.Fatalf("tier multiplier = %#v, want 2.5", breakdown.TierMultiplier)
	}
	if len(breakdown.Items) != 2 {
		t.Fatalf("priority item count = %d, want 2", len(breakdown.Items))
	}
	input, ok := breakdown.Items[0].(usageTokenCostBreakdownItem)
	if !ok || input.USDPerMillion != 5 || input.SubtotalUSD != 5 {
		t.Fatalf("priority input item = %#v, want 5 USD/MTok and subtotal", breakdown.Items[0])
	}
	output, ok := breakdown.Items[1].(usageTokenCostBreakdownItem)
	if !ok || output.USDPerMillion != 10 || output.SubtotalUSD != 10 {
		t.Fatalf("priority output item = %#v, want 10 USD/MTok and subtotal", breakdown.Items[1])
	}
	amount, unpriced := recordCost(record, prices)
	if unpriced || amount != breakdown.TotalUSD {
		t.Fatalf("priority recordCost = %v/%v, want %v/false", amount, unpriced, breakdown.TotalUSD)
	}
}

func TestRecordCostLongContextUsesStrictWholeRequestBand(t *testing.T) {
	provider := "openai"
	model := "gpt-long-context-test"
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:            provider,
			Model:               model,
			InputUSDPerMillion:  2,
			OutputUSDPerMillion: 4,
			LongContext: &ModelPriceLongContext{
				ThresholdInputTokens:       200_000,
				InputUSDPerMillion:         5,
				OutputUSDPerMillion:        12,
				CacheReadUSDPerMillion:     0.5,
				CacheCreationUSDPerMillion: 6,
			},
		},
	}

	for _, test := range []struct {
		name        string
		inputTokens int
		wantApplied bool
		wantInput   float64
		wantOutput  float64
	}{
		{name: "below threshold", inputTokens: 199_999, wantInput: 2, wantOutput: 4},
		{name: "equal threshold", inputTokens: 200_000, wantInput: 2, wantOutput: 4},
		{name: "above threshold", inputTokens: 200_001, wantApplied: true, wantInput: 5, wantOutput: 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			breakdown := calculateRecordCostBreakdown(UsageRecord{
				Provider:     &provider,
				Model:        &model,
				InputTokens:  test.inputTokens,
				OutputTokens: 1_000_000,
				TotalTokens:  test.inputTokens + 1_000_000,
			}, prices)
			if breakdown.Unpriced || breakdown.LongContextApplied != test.wantApplied || breakdown.ContextInputTokens != test.inputTokens {
				t.Fatalf("long-context breakdown = %#v", breakdown)
			}
			if breakdown.LongContextThresholdTokens == nil || *breakdown.LongContextThresholdTokens != 200_000 {
				t.Fatalf("threshold = %#v, want 200000", breakdown.LongContextThresholdTokens)
			}
			if len(breakdown.Items) != 2 {
				t.Fatalf("item count = %d, want 2", len(breakdown.Items))
			}
			input := breakdown.Items[0].(usageTokenCostBreakdownItem)
			output := breakdown.Items[1].(usageTokenCostBreakdownItem)
			if input.USDPerMillion != test.wantInput || output.USDPerMillion != test.wantOutput {
				t.Fatalf("selected prices = %v/%v, want %v/%v", input.USDPerMillion, output.USDPerMillion, test.wantInput, test.wantOutput)
			}
		})
	}
}

func TestRecordCostLongContextUsesFullClaudePromptTokens(t *testing.T) {
	model := "claude-long-context-test"
	for _, test := range []struct {
		name          string
		provider      string
		cacheCreation int
		wantApplied   bool
		wantContext   int
		wantPrices    []float64
	}{
		{
			name:          "claude equal threshold uses base price",
			provider:      "claude",
			cacheCreation: 10_000,
			wantContext:   200_000,
			wantPrices:    []float64{2, 0.2, 3, 4},
		},
		{
			name:          "anthropic cached prompt exceeds threshold",
			provider:      "anthropic",
			cacheCreation: 10_001,
			wantApplied:   true,
			wantContext:   200_001,
			wantPrices:    []float64{5, 0.5, 6, 12},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			breakdown := calculateRecordCostBreakdown(UsageRecord{
				Provider:            &test.provider,
				Model:               &model,
				InputTokens:         150_000,
				CacheReadTokens:     40_000,
				CacheCreationTokens: test.cacheCreation,
				OutputTokens:        100_000,
			}, map[[2]string]ModelPrice{
				priceKey(test.provider, model): {
					Provider:                   test.provider,
					Model:                      model,
					InputUSDPerMillion:         2,
					OutputUSDPerMillion:        4,
					CacheReadUSDPerMillion:     0.2,
					CacheCreationUSDPerMillion: 3,
					LongContext: &ModelPriceLongContext{
						ThresholdInputTokens:       200_000,
						InputUSDPerMillion:         5,
						OutputUSDPerMillion:        12,
						CacheReadUSDPerMillion:     0.5,
						CacheCreationUSDPerMillion: 6,
					},
				},
			})
			if breakdown.Unpriced || breakdown.LongContextApplied != test.wantApplied || breakdown.ContextInputTokens != test.wantContext {
				t.Fatalf("Claude long-context breakdown = %#v", breakdown)
			}
			if len(breakdown.Items) != len(test.wantPrices) {
				t.Fatalf("Claude item count = %d, want %d", len(breakdown.Items), len(test.wantPrices))
			}
			for index, item := range breakdown.Items {
				tokenItem := item.(usageTokenCostBreakdownItem)
				if tokenItem.USDPerMillion != test.wantPrices[index] {
					t.Fatalf("Claude item %d price = %v, want %v", index, tokenItem.USDPerMillion, test.wantPrices[index])
				}
			}
		})
	}
}

func TestRecordCostLongContextAppliesBeforeFastAndUsesSelectedCacheSemantics(t *testing.T) {
	provider := "codex"
	model := "gpt-long-fast-test"
	tier := "priority"
	multiplier := 2.0
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:            &provider,
		Model:               &model,
		ServiceTier:         &tier,
		InputTokens:         300_000,
		OutputTokens:        100_000,
		CachedTokens:        100_000,
		CacheCreationTokens: 50_000,
		TotalTokens:         400_000,
	}, map[[2]string]ModelPrice{
		priceKey("openai", model): {
			Provider:                   "openai",
			Model:                      model,
			InputUSDPerMillion:         2,
			OutputUSDPerMillion:        4,
			CacheReadUSDPerMillion:     0.2,
			CacheCreationUSDPerMillion: 0,
			PriorityMultiplier:         &multiplier,
			LongContext: &ModelPriceLongContext{
				ThresholdInputTokens:       200_000,
				InputUSDPerMillion:         5,
				OutputUSDPerMillion:        12,
				CacheReadUSDPerMillion:     0.5,
				CacheCreationUSDPerMillion: 6,
			},
		},
	})
	if breakdown.Unpriced || !breakdown.LongContextApplied || breakdown.TierMultiplier == nil || *breakdown.TierMultiplier != 2 {
		t.Fatalf("combined tier breakdown = %#v", breakdown)
	}
	if len(breakdown.Items) != 4 {
		t.Fatalf("combined tier item count = %d, want 4", len(breakdown.Items))
	}
	wantPrices := []float64{10, 1, 12, 24}
	for index, item := range breakdown.Items {
		tokenItem := item.(usageTokenCostBreakdownItem)
		if tokenItem.USDPerMillion != wantPrices[index] {
			t.Fatalf("item %d price = %v, want %v", index, tokenItem.USDPerMillion, wantPrices[index])
		}
	}
}

func TestRecordCostLongContextInvalidLegacyConfigurationFailsClosedOnlyWhenHit(t *testing.T) {
	provider := "openai"
	model := "gpt-long-invalid-test"
	price := ModelPrice{
		Provider:           provider,
		Model:              model,
		InputUSDPerMillion: 2,
		LongContext: &ModelPriceLongContext{
			ThresholdInputTokens: 200_000,
			InputUSDPerMillion:   math.Inf(1),
		},
		longContextInvalid: true,
	}
	prices := map[[2]string]ModelPrice{priceKey(provider, model): price}
	base := calculateRecordCostBreakdown(UsageRecord{Provider: &provider, Model: &model, InputTokens: 200_000, TotalTokens: 200_000}, prices)
	if base.Unpriced || base.TotalUSD != 0.4 {
		t.Fatalf("base tier with invalid future band = %#v, want priced 0.4", base)
	}
	long := calculateRecordCostBreakdown(UsageRecord{Provider: &provider, Model: &model, InputTokens: 200_001, TotalTokens: 200_001}, prices)
	if !long.Unpriced || long.LongContextApplied || long.TotalUSD != 0 || len(long.Items) != 0 {
		t.Fatalf("invalid long-context hit = %#v, want unpriced", long)
	}
	negative := calculateRecordCostBreakdown(UsageRecord{Provider: &provider, Model: &model, InputTokens: -1}, prices)
	if negative.Unpriced || negative.ContextInputTokens != 0 || negative.LongContextApplied {
		t.Fatalf("negative input breakdown = %#v, want base zero-cost", negative)
	}
}

func TestRecordCostUnpricedClearsLongContextApplied(t *testing.T) {
	provider := "openai"
	model := "gpt-long-invalid-fast-test"
	tier := "priority"
	invalidMultiplier := math.Inf(1)
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:     &provider,
		Model:        &model,
		ServiceTier:  &tier,
		InputTokens:  300_000,
		OutputTokens: 100_000,
	}, map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:           provider,
			Model:              model,
			InputUSDPerMillion: 2,
			PriorityMultiplier: &invalidMultiplier,
			LongContext: &ModelPriceLongContext{
				ThresholdInputTokens:       200_000,
				InputUSDPerMillion:         5,
				OutputUSDPerMillion:        12,
				CacheReadUSDPerMillion:     0.5,
				CacheCreationUSDPerMillion: 6,
			},
		},
	})
	if !breakdown.Unpriced || breakdown.LongContextApplied || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
		t.Fatalf("unpriced long-context breakdown = %#v, want unpriced and not applied", breakdown)
	}
}

func TestRecordCostKeepsBasePriceForUnreportedUnknownAndUnsupportedTiers(t *testing.T) {
	model := "gpt-tier-test"
	multiplier := 2.0
	prices := map[[2]string]ModelPrice{
		priceKey("openai", model): {
			Provider:           "openai",
			Model:              model,
			InputUSDPerMillion: 3,
			PriorityMultiplier: &multiplier,
		},
		priceKey("anthropic", model): {
			Provider:           "anthropic",
			Model:              model,
			InputUSDPerMillion: 3,
			PriorityMultiplier: &multiplier,
		},
	}

	tests := []struct {
		name        string
		provider    string
		serviceTier *string
	}{
		{name: "unreported", provider: "openai"},
		{name: "default", provider: "openai", serviceTier: stringPtr("default")},
		{name: "unknown", provider: "openai", serviceTier: stringPtr("flex")},
		{name: "unsupported provider", provider: "anthropic", serviceTier: stringPtr("priority")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := UsageRecord{
				Provider:    &test.provider,
				Model:       &model,
				ServiceTier: test.serviceTier,
				InputTokens: 1_000_000,
				TotalTokens: 1_000_000,
			}
			breakdown := calculateRecordCostBreakdown(record, prices)
			if breakdown.Unpriced || breakdown.TotalUSD != 3 || breakdown.TierMultiplier != nil {
				t.Fatalf("base breakdown = %#v, want total 3 without tier multiplier", breakdown)
			}
		})
	}
}

func TestRecordCostAppliesPriorityMultiplierToRequestPrice(t *testing.T) {
	provider := "openai"
	model := "gpt-image-tier-test"
	serviceTier := "priority"
	requestUSD := 1.25
	multiplier := 2.0
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &serviceTier,
	}, map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:           provider,
			Model:              model,
			RequestUSD:         &requestUSD,
			PriorityMultiplier: &multiplier,
		},
	})
	if breakdown.Unpriced || breakdown.TotalUSD != 2.5 || breakdown.TierMultiplier == nil || *breakdown.TierMultiplier != 2 {
		t.Fatalf("priority request breakdown = %#v, want total 2.5 and multiplier 2", breakdown)
	}
	item, ok := breakdown.Items[0].(usageRequestCostBreakdownItem)
	if !ok || item.USDPerRequest != 2.5 {
		t.Fatalf("priority request item = %#v, want 2.5 USD/request", breakdown.Items[0])
	}
}

func TestRecordCostMarksUnroundablePriorityMultiplierUnpriced(t *testing.T) {
	provider := "openai"
	model := "gpt-priority-overflow-test"
	serviceTier := "priority"
	multiplier := 1e308
	record := UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &serviceTier,
		InputTokens: 1,
		TotalTokens: 1,
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:           provider,
			Model:              model,
			InputUSDPerMillion: 1,
			PriorityMultiplier: &multiplier,
		},
	}
	breakdown := calculateRecordCostBreakdown(record, prices)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.TierMultiplier != nil || len(breakdown.Items) != 0 {
		t.Fatalf("overflow priority breakdown = %#v, want unpriced zero-cost breakdown", breakdown)
	}
	amount, unpriced := recordCost(record, prices)
	if !unpriced || amount != 0 {
		t.Fatalf("overflow priority record cost = %v/%v, want 0/true", amount, unpriced)
	}
}

func TestRecordCostBreakdownPricesOpenAICacheWritesSeparatelyWhenPriceExists(t *testing.T) {
	provider := "openai"
	model := "gpt-5.6"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        20,
		CachedTokens:        30,
		CacheCreationTokens: 40,
		TotalTokens:         120,
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:                   provider,
			Model:                      model,
			InputUSDPerMillion:         10,
			OutputUSDPerMillion:        20,
			CacheReadUSDPerMillion:     1,
			CacheCreationUSDPerMillion: 12,
		},
	}

	breakdown := calculateRecordCostBreakdown(record, prices)
	if breakdown.Unpriced {
		t.Fatal("record should be priced")
	}
	if len(breakdown.Items) != 4 {
		t.Fatalf("cost breakdown item count = %d, want 4", len(breakdown.Items))
	}
	wantKinds := []string{usageCostKindInput, usageCostKindCacheRead, usageCostKindCacheCreation, usageCostKindOutput}
	wantTokens := []int{30, 30, 40, 20}
	wantSubtotals := []float64{0.0003, 0.00003, 0.00048, 0.0004}
	for index, item := range breakdown.Items {
		tokenItem, ok := item.(usageTokenCostBreakdownItem)
		if !ok {
			t.Fatalf("cost breakdown item %d type = %T, want usageTokenCostBreakdownItem", index, item)
		}
		if tokenItem.Kind != wantKinds[index] || tokenItem.Tokens != wantTokens[index] || tokenItem.SubtotalUSD != wantSubtotals[index] {
			t.Fatalf("cost breakdown item %d = kind %q tokens %d subtotal %v, want %q/%d/%v", index, tokenItem.Kind, tokenItem.Tokens, tokenItem.SubtotalUSD, wantKinds[index], wantTokens[index], wantSubtotals[index])
		}
	}
	if breakdown.TotalUSD != 0.00121 {
		t.Fatalf("total cost = %v, want 0.00121", breakdown.TotalUSD)
	}
}

func TestUsageCostAggregatesRemainFiniteWhenRoundingScaleOverflows(t *testing.T) {
	provider := "openai"
	model := "gpt-image-aggregate-overflow"
	requestUSD := 1e300
	timestamp := time.Now().In(appTimeLocation)
	records := []UsageRecord{
		{ID: 1, Provider: &provider, Model: &model, Timestamp: timestamp},
		{ID: 2, Provider: &provider, Model: &model, Timestamp: timestamp},
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:   provider,
			Model:      model,
			RequestUSD: &requestUSD,
		},
	}
	want := 2e300
	assertFiniteCost := func(name string, value float64) {
		t.Helper()
		if math.IsNaN(value) || math.IsInf(value, 0) || value != want {
			t.Fatalf("%s cost = %v, want finite %v", name, value, want)
		}
	}

	summary := usageSummaryFromRecords(UsageFilters{}, records, prices)
	assertFiniteCost("summary", summary["estimated_cost_usd"].(float64))
	trends := trendPointsFromRecords(UsageFilters{}, records, prices)
	if len(trends) != 1 {
		t.Fatalf("trend points = %d, want 1", len(trends))
	}
	assertFiniteCost("trend", trends[0]["estimated_cost_usd"].(float64))
	ranking := rankingFromRecords(records, prices, "model", nil)
	rankingItems := ranking["items"].([]map[string]any)
	if len(rankingItems) != 1 {
		t.Fatalf("ranking items = %d, want 1", len(rankingItems))
	}
	assertFiniteCost("ranking", rankingItems[0]["estimated_cost_usd"].(float64))
	distributions := distributionsFromRecords(records, prices)
	modelDistribution := distributions["models"].([]map[string]any)
	if len(modelDistribution) != 1 {
		t.Fatalf("model distribution items = %d, want 1", len(modelDistribution))
	}
	assertFiniteCost("distribution", modelDistribution[0]["estimated_cost_usd"].(float64))
	keeperUsage := &keeperQuotaWindowUsage{}
	for _, record := range records {
		addRecordToKeeperQuotaWindowUsage(keeperUsage, record, prices)
	}
	assertFiniteCost("keeper", keeperUsage.EstimatedCostUSD)

	if _, err := json.Marshal(apiJSONValue(map[string]any{
		"summary":       summary,
		"trends":        trends,
		"ranking":       ranking,
		"distributions": distributions,
		"keeper":        keeperUsage.EstimatedCostUSD,
	})); err != nil {
		t.Fatalf("marshal finite aggregate response: %v", err)
	}
}

func TestRecordCostTotalOnlySkipsBreakdownItems(t *testing.T) {
	provider := "openai"
	model := "gpt-5.6"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        20,
		CachedTokens:        30,
		CacheCreationTokens: 40,
		TotalTokens:         120,
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:                   provider,
			Model:                      model,
			InputUSDPerMillion:         10,
			OutputUSDPerMillion:        20,
			CacheReadUSDPerMillion:     1,
			CacheCreationUSDPerMillion: 12,
		},
	}

	totalOnly := calculateRecordCost(record, prices, false)
	full := calculateRecordCostBreakdown(record, prices)
	if totalOnly.Items != nil {
		t.Fatalf("total-only cost items = %#v, want nil", totalOnly.Items)
	}
	if totalOnly.TotalUSD != full.TotalUSD || totalOnly.Unpriced != full.Unpriced {
		t.Fatalf("total-only cost = %v/%v, want %v/%v", totalOnly.TotalUSD, totalOnly.Unpriced, full.TotalUSD, full.Unpriced)
	}
	if len(full.Items) != 4 {
		t.Fatalf("full cost breakdown item count = %d, want 4", len(full.Items))
	}
}

func TestRecordCostBreakdownPrefersExplicitCacheReadTokens(t *testing.T) {
	provider := "openai"
	model := "gpt-test"
	breakdown := normalizedUsageTokenBreakdown(UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		CachedTokens:        90,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
	})
	if breakdown.NormalInputTokens != 50 || breakdown.CacheReadTokens != 20 || breakdown.CacheCreationTokens != 30 {
		t.Fatalf("token breakdown = input %d read %d creation %d, want 50/20/30", breakdown.NormalInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens)
	}
}

func TestRecordCostTruncatesGenericCachedTokens(t *testing.T) {
	provider := "openai"
	model := "gpt-test"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        10,
		CachedTokens:        150,
		CacheCreationTokens: 50,
		TotalTokens:         110,
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:               provider,
			Model:                  model,
			InputUSDPerMillion:     10,
			OutputUSDPerMillion:    20,
			CacheReadUSDPerMillion: 1,
		},
	}

	amount, unpriced := recordCost(record, prices)
	if unpriced {
		t.Fatal("record should be priced")
	}
	want := mathRound((100*1+10*20)/1_000_000.0, 8)
	if amount != want {
		t.Fatalf("cost = %v, want %v", amount, want)
	}
	breakdown := normalizedUsageTokenBreakdown(record)
	if breakdown.NormalInputTokens != 0 || breakdown.CacheReadTokens != 100 || breakdown.CacheCreationTokens != 0 {
		t.Fatalf("token breakdown = input %d read %d creation %d, want 0/100/0", breakdown.NormalInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens)
	}
}

func TestRecordCostUsesRequestPriceForImageModels(t *testing.T) {
	provider := "openai"
	model := "gpt-image-2"
	requestUSD := 1.25
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:           provider,
			Model:              model,
			InputUSDPerMillion: 5,
			RequestUSD:         &requestUSD,
		},
	}

	amount, unpriced := recordCost(UsageRecord{
		Provider:     &provider,
		Model:        &model,
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		TotalTokens:  2_000_000,
	}, prices)
	if unpriced || amount != 1.25 {
		t.Fatalf("image cost = %v unpriced=%v, want 1.25 false", amount, unpriced)
	}
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider: &provider,
		Model:    &model,
	}, prices)
	if breakdown.BillingUnit != modelBillingUnitRequest || breakdown.TotalUSD != 1.25 || len(breakdown.Items) != 1 {
		t.Fatalf("image breakdown = unit %q total %v items %d, want request/1.25/1", breakdown.BillingUnit, breakdown.TotalUSD, len(breakdown.Items))
	}
	requestItem, ok := breakdown.Items[0].(usageRequestCostBreakdownItem)
	if !ok || requestItem.Requests != 1 || requestItem.USDPerRequest != 1.25 || requestItem.SubtotalUSD != 1.25 {
		t.Fatalf("image request item = %#v, want one request at 1.25", breakdown.Items[0])
	}

	amount, unpriced = recordCost(UsageRecord{
		Provider: &provider,
		Model:    &model,
		Failed:   true,
	}, prices)
	if unpriced || amount != 0 {
		t.Fatalf("failed image cost = %v unpriced=%v, want 0 false", amount, unpriced)
	}
}

func TestRecordCostTreatsImageWithoutRequestPriceAsUnpriced(t *testing.T) {
	provider := "openai"
	model := "custom-image-model"
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:               provider,
			Model:                  model,
			InputUSDPerMillion:     100,
			OutputUSDPerMillion:    100,
			CacheReadUSDPerMillion: 100,
		},
	}

	amount, unpriced := recordCost(UsageRecord{
		Provider: &provider,
		Model:    &model,
	}, prices)
	if amount != 0 || !unpriced {
		t.Fatalf("image without request price cost = %v unpriced=%v, want 0 true", amount, unpriced)
	}
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider: &provider,
		Model:    &model,
	}, prices)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
		t.Fatalf("unpriced image breakdown = unpriced %v total %v items %d, want true/0/0", breakdown.Unpriced, breakdown.TotalUSD, len(breakdown.Items))
	}
}

func TestRecordCostBreakdownTreatsMissingTokenPriceAsUnpriced(t *testing.T) {
	provider := "openai"
	model := "missing-model"
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:     &provider,
		Model:        &model,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	}, nil)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
		t.Fatalf("unpriced token breakdown = unpriced %v total %v items %d, want true/0/0", breakdown.Unpriced, breakdown.TotalUSD, len(breakdown.Items))
	}
}

func TestModelPriceAPIUpdatesImageRequestPrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)

	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-image-2",
		"input_usd_per_million":          0,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
		"request_usd":                    1,
	}, cookies, &created)
	if created.RequestUSD == nil || *created.RequestUSD != 1 || created.BillingUnit != modelBillingUnitRequest {
		t.Fatalf("created image price = %#v, want request_usd=1 request billing", created)
	}

	var updated ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d", created.ID), map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-image-2",
		"input_usd_per_million":          0,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
		"request_usd":                    2.5,
	}, cookies, &updated)
	if updated.RequestUSD == nil || *updated.RequestUSD != 2.5 || updated.BillingUnit != modelBillingUnitRequest {
		t.Fatalf("updated image price = %#v, want request_usd=2.5 request billing", updated)
	}
}

func TestModelPriceAPIRoundTripsAndClearsLongContextPrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	basePayload := map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-long-api-test",
		"input_usd_per_million":          2,
		"output_usd_per_million":         4,
		"cache_read_usd_per_million":     0.2,
		"cache_creation_usd_per_million": 0,
		"long_context": map[string]any{
			"threshold_input_tokens":         272000,
			"input_usd_per_million":          5,
			"output_usd_per_million":         12,
			"cache_read_usd_per_million":     0.5,
			"cache_creation_usd_per_million": 6.25,
		},
	}
	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", basePayload, cookies, &created)
	if created.LongContext == nil || created.LongContext.ThresholdInputTokens != 272000 || created.LongContext.CacheCreationUSDPerMillion != 6.25 {
		t.Fatalf("created long-context price = %#v", created.LongContext)
	}

	basePayload["long_context"] = nil
	var updated ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d", created.ID), basePayload, cookies, &updated)
	if updated.LongContext != nil {
		t.Fatalf("cleared long-context price = %#v, want nil", updated.LongContext)
	}
	var configuredColumns int
	if err := app.db.QueryRow(`
		SELECT
			(long_context_threshold_tokens IS NOT NULL) +
			(long_context_input_usd_per_million IS NOT NULL) +
			(long_context_output_usd_per_million IS NOT NULL) +
			(long_context_cache_read_usd_per_million IS NOT NULL) +
			(long_context_cache_creation_usd_per_million IS NOT NULL)
		FROM model_prices WHERE id = ?
	`, created.ID).Scan(&configuredColumns); err != nil {
		t.Fatalf("query cleared long-context columns: %v", err)
	}
	if configuredColumns != 0 {
		t.Fatalf("configured long-context columns = %d, want 0", configuredColumns)
	}
}

func TestModelPriceAPIRejectsPartialAndRequestLongContextPrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-long-partial-test",
		"input_usd_per_million":          1,
		"output_usd_per_million":         2,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
		"long_context": map[string]any{
			"threshold_input_tokens": 200000,
			"input_usd_per_million":  3,
		},
	}, cookies, http.StatusUnprocessableEntity)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-image-long-test",
		"input_usd_per_million":          0,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
		"request_usd":                    1,
		"long_context": map[string]any{
			"threshold_input_tokens":         200000,
			"input_usd_per_million":          3,
			"output_usd_per_million":         6,
			"cache_read_usd_per_million":     0.3,
			"cache_creation_usd_per_million": 0,
		},
	}, cookies, http.StatusUnprocessableEntity)
}

func TestModelPriceAPIUpdatesPriorityMultiplierWithoutChangingSyncSource(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			auto_synced, updated_at
		) VALUES ('openai', 'gpt-5.5', 1, 2, 0, 0, 'litellm', 1, ?)
	`, dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed price: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}

	var updated ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d/priority-multiplier", id), map[string]any{
		"priority_multiplier": 3,
	}, cookies, &updated)
	if updated.PriorityMultiplier == nil || *updated.PriorityMultiplier != 3 || updated.Source != "litellm" || !updated.AutoSynced {
		t.Fatalf("updated priority price = %#v, want multiplier 3 and retained LiteLLM source", updated)
	}
}

func TestModelPriceAPIKeepsLegacyInfiniteMultiplierJSONSafe(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			priority_multiplier, source, auto_synced, updated_at
		) VALUES ('openai', 'gpt-json-safe-multiplier', 1, 2, 0, 0, ?, 'litellm', 1, ?)
	`, math.Inf(1), dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed infinite priority multiplier: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}
	stored, err := app.getPrice(context.Background(), int(id))
	if err != nil {
		t.Fatalf("get stored price: %v", err)
	}
	if stored.PriorityMultiplier == nil || !math.IsInf(*stored.PriorityMultiplier, 1) {
		t.Fatalf("stored priority multiplier = %#v, want +Inf retained internally", stored.PriorityMultiplier)
	}

	var prices []ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices", nil, cookies, &prices)
	for _, price := range prices {
		if price.ID == int(id) {
			if price.PriorityMultiplier != nil {
				t.Fatalf("API priority multiplier = %v, want JSON-safe null", *price.PriorityMultiplier)
			}
			catalog := modelPriceCatalogForAPI(ModelPriceCatalogResponse{
				Models: []ModelPriceCatalogItem{{ID: price.Model, Price: &stored}},
			})
			if catalog.Models[0].Price == nil || catalog.Models[0].Price.PriorityMultiplier != nil {
				t.Fatalf("catalog priority multiplier = %#v, want JSON-safe null", catalog.Models[0].Price)
			}
			return
		}
	}
	t.Fatalf("API response missing price id %d", id)
}

func TestModelPriceAPIRejectsUnroundablePriorityMultiplier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			auto_synced, updated_at
		) VALUES ('openai', 'gpt-priority-overflow-test', 1, 2, 0, 0, 'manual', 0, ?)
	`, dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed price: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}

	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d/priority-multiplier", id), map[string]any{
		"priority_multiplier": 1e301,
	}, cookies, http.StatusUnprocessableEntity)
	stored, err := app.getPrice(context.Background(), int(id))
	if err != nil {
		t.Fatalf("get stored price: %v", err)
	}
	if stored.PriorityMultiplier != nil {
		t.Fatalf("stored priority multiplier = %v, want NULL after rejection", *stored.PriorityMultiplier)
	}
}

func TestModelPriceAPIRejectsPriceUpdateThatInvalidatesPriorityMultiplier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			priority_multiplier, source, auto_synced, updated_at
		) VALUES ('openai', 'gpt-retained-multiplier-test', 1, 0, 0, 0, 1e299, 'manual', 0, ?)
	`, dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed price: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}

	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d", id), map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-retained-multiplier-test",
		"input_usd_per_million":          1e10,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusUnprocessableEntity)
	stored, err := app.getPrice(context.Background(), int(id))
	if err != nil {
		t.Fatalf("get stored price: %v", err)
	}
	if stored.InputUSDPerMillion != 1 || stored.PriorityMultiplier == nil || *stored.PriorityMultiplier != 1e299 {
		t.Fatalf("stored price after rejected update = %#v, want original price and multiplier", stored)
	}
}

func TestModelPriceUpdatesSerializeCandidateValidation(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			priority_multiplier, source, auto_synced, updated_at
		) VALUES ('openai', 'gpt-concurrent-multiplier-test', 1, 0, 0, 0, 1, 'manual', 0, ?)
	`, dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed price: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}

	for iteration := 0; iteration < 20; iteration++ {
		if _, err := app.db.Exec(`
			UPDATE model_prices
			SET input_usd_per_million = 1, output_usd_per_million = 0,
			    cache_read_usd_per_million = 0, cache_creation_usd_per_million = 0,
			    priority_multiplier = 1, updated_at = ?
			WHERE id = ?
		`, dbTime(time.Now()), id); err != nil {
			t.Fatalf("reset price at iteration %d: %v", iteration, err)
		}

		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := app.updatePrice(context.Background(), int(id), modelPricePayload{
				Provider:           "openai",
				Model:              "gpt-concurrent-multiplier-test",
				InputUSDPerMillion: 1e100,
			})
			results <- err
		}()
		go func() {
			<-start
			multiplier := 1e208
			_, err := app.updatePriorityMultiplier(context.Background(), int(id), priorityMultiplierPayload{
				PriorityMultiplier: &multiplier,
			})
			results <- err
		}()
		close(start)

		successes, validationFailures := 0, 0
		for resultIndex := 0; resultIndex < 2; resultIndex++ {
			err := <-results
			if err == nil {
				successes++
				continue
			}
			appErr, ok := err.(*AppError)
			if !ok || appErr.Status != http.StatusUnprocessableEntity {
				t.Fatalf("concurrent update error at iteration %d = %v, want validation error", iteration, err)
			}
			validationFailures++
		}
		if successes != 1 || validationFailures != 1 {
			t.Fatalf("concurrent results at iteration %d = %d success/%d validation, want 1/1", iteration, successes, validationFailures)
		}
		stored, err := app.getPrice(context.Background(), int(id))
		if err != nil {
			t.Fatalf("get concurrent price at iteration %d: %v", iteration, err)
		}
		if err := validatePriorityMultiplierForPrice(stored); err != nil {
			t.Fatalf("unsafe concurrent price at iteration %d: %#v (%v)", iteration, stored, err)
		}
	}
}

func TestModelPriceAPIRejectsUnsafeDefaultPriorityMultiplier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       "openai",
		"model":                          "gpt-5.5",
		"input_usd_per_million":          1e308,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusUnprocessableEntity)
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.5'`).Scan(&count); err != nil {
		t.Fatalf("count rejected price: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected unsafe default price count = %d, want 0", count)
	}
}

func TestUsageAggregatesClaudeCacheReadAndCreationTokens(t *testing.T) {
	provider := "claude"
	model := "claude-sonnet-test"
	record := UsageRecord{
		Timestamp:           time.Date(2026, 5, 19, 10, 0, 0, 0, appTimeLocation),
		Provider:            &provider,
		Model:               &model,
		InputTokens:         10,
		OutputTokens:        5,
		CachedTokens:        20,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		ReasoningTokens:     7,
		TotalTokens:         15,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("anthropic", model): {
			Provider:                   "anthropic",
			Model:                      model,
			InputUSDPerMillion:         1,
			OutputUSDPerMillion:        2,
			CacheReadUSDPerMillion:     0.5,
			CacheCreationUSDPerMillion: 1.25,
		},
	}
	filters := UsageFilters{}
	start := time.Date(2026, 5, 19, 0, 0, 0, 0, appTimeLocation)
	end := start.Add(24 * time.Hour)
	filters.Start = &start
	filters.End = &end

	summary := usageSummaryFromRecords(filters, []UsageRecord{record}, prices)
	if summary["input_tokens"].(int) != 60 {
		t.Fatalf("summary input = %v, want 60", summary["input_tokens"])
	}
	if summary["total_tokens"].(int) != 72 {
		t.Fatalf("summary total = %v, want 72", summary["total_tokens"])
	}
	trends := trendPointsFromRecords(filters, []UsageRecord{record}, prices)
	if len(trends) != 1 || trends[0]["total_tokens"].(int) != 72 {
		t.Fatalf("trend totals = %#v, want one item with total 72", trends)
	}
	ranking := rankingFromRecords([]UsageRecord{record}, prices, "model", nil)
	items := ranking["items"].([]map[string]any)
	if len(items) != 1 || items[0]["total_tokens"].(int) != 72 {
		t.Fatalf("ranking totals = %#v, want one item with total 72", items)
	}
	distributions := distributionsFromRecords([]UsageRecord{record}, prices)
	models := distributions["models"].([]map[string]any)
	if len(models) != 1 || models[0]["total_tokens"].(int) != 72 {
		t.Fatalf("distribution totals = %#v, want one item with total 72", models)
	}
}

func TestSyncLiteLLMPricesReplacesLiteLLMSource(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := dbTime(time.Now().In(appTimeLocation))
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, priority_multiplier, source, updated_at
		) VALUES
			('openai', 'old-litellm-model', 1, 1, 1, 1, NULL, 'litellm', ?),
			('openai', 'gpt-new-model', 1, 1, 1, 1, 3, 'litellm', ?),
			('openai', 'gpt-5.5', 1, 1, 1, 1, 0, 'litellm', ?),
			('openai', 'gpt-invalid-negative', 1, 1, 1, 1, -2, 'litellm', ?),
			('openai', 'manual-model', 9, 9, 9, 9, NULL, 'manual', ?)
	`, now, now, now, now, now); err != nil {
		t.Fatalf("seed prices: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, priority_multiplier, source, updated_at
		) VALUES ('openai', 'gpt-invalid-infinite', 1, 1, 1, 1, ?, 'litellm', ?)
	`, math.Inf(1), now); err != nil {
		t.Fatalf("seed infinite priority multiplier: %v", err)
	}

	rawData := map[string]any{
		"gpt-new-model": map[string]any{
			"litellm_provider":            "openai",
			"input_cost_per_token":        0.000001,
			"output_cost_per_token":       0.000002,
			"cache_read_input_token_cost": 0.0000001,
		},
		"gpt-5.5": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
		"gpt-invalid-negative": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
		"gpt-invalid-infinite": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
		"gpt-5.4": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 1e294,
		},
		"gpt-invalid-base-overflow": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 1e308,
		},
		"gpt-invalid-base-nan": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": "NaN",
		},
		"gpt-invalid-base-infinity": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": "Inf",
		},
		"claude-new-model": map[string]any{
			"litellm_provider":                "anthropic",
			"input_cost_per_token":            0.000003,
			"output_cost_per_token":           0.000015,
			"cache_read_input_token_cost":     0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
		},
		"manual-model": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
	}
	result, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData)
	if err != nil {
		t.Fatalf("syncLiteLLMPrices failed: %v", err)
	}
	if result["imported"].(int) != 6 || result["skipped_manual"].(int) != 1 || result["skipped_invalid"].(int) != 3 {
		t.Fatalf("sync result = %#v, want imported 6 skipped_manual 1 skipped_invalid 3", result)
	}

	var oldCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM model_prices WHERE source = 'litellm' AND model = 'old-litellm-model'`).Scan(&oldCount); err != nil {
		t.Fatalf("query old litellm count: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old litellm rows = %d, want 0", oldCount)
	}
	var invalidBasePriceCount int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM model_prices
		WHERE model IN ('gpt-invalid-base-overflow', 'gpt-invalid-base-nan', 'gpt-invalid-base-infinity')
	`).Scan(&invalidBasePriceCount); err != nil {
		t.Fatalf("query invalid base price count: %v", err)
	}
	if invalidBasePriceCount != 0 {
		t.Fatalf("invalid base price rows = %d, want 0", invalidBasePriceCount)
	}
	var manualInput float64
	if err := app.db.QueryRow(`SELECT input_usd_per_million FROM model_prices WHERE source = 'manual' AND model = 'manual-model'`).Scan(&manualInput); err != nil {
		t.Fatalf("query manual price: %v", err)
	}
	if manualInput != 9 {
		t.Fatalf("manual price = %v, want preserved 9", manualInput)
	}
	var cacheRead, cacheCreation float64
	if err := app.db.QueryRow(`SELECT cache_read_usd_per_million, cache_creation_usd_per_million FROM model_prices WHERE source = 'litellm' AND model = 'claude-new-model'`).Scan(&cacheRead, &cacheCreation); err != nil {
		t.Fatalf("query claude price: %v", err)
	}
	if cacheRead != 0.3 || cacheCreation != 3.75 {
		t.Fatalf("claude cache prices = read %v creation %v, want 0.3 and 3.75", cacheRead, cacheCreation)
	}
	var priorityMultiplier float64
	if err := app.db.QueryRow(`SELECT priority_multiplier FROM model_prices WHERE source = 'litellm' AND model = 'gpt-new-model'`).Scan(&priorityMultiplier); err != nil {
		t.Fatalf("query synced priority multiplier: %v", err)
	}
	if priorityMultiplier != 3 {
		t.Fatalf("synced priority multiplier = %v, want preserved 3", priorityMultiplier)
	}
	var zeroMultiplier, negativeMultiplier, infiniteMultiplier float64
	if err := app.db.QueryRow(`SELECT priority_multiplier FROM model_prices WHERE source = 'litellm' AND model = 'gpt-5.5'`).Scan(&zeroMultiplier); err != nil {
		t.Fatalf("query zero priority multiplier: %v", err)
	}
	if err := app.db.QueryRow(`SELECT priority_multiplier FROM model_prices WHERE source = 'litellm' AND model = 'gpt-invalid-negative'`).Scan(&negativeMultiplier); err != nil {
		t.Fatalf("query negative priority multiplier: %v", err)
	}
	if err := app.db.QueryRow(`SELECT priority_multiplier FROM model_prices WHERE source = 'litellm' AND model = 'gpt-invalid-infinite'`).Scan(&infiniteMultiplier); err != nil {
		t.Fatalf("query infinite priority multiplier: %v", err)
	}
	if zeroMultiplier != 0 || negativeMultiplier != -2 || !math.IsInf(infiniteMultiplier, 1) {
		t.Fatalf("synced invalid priority multipliers = %v/%v/%v, want 0/-2/+Inf", zeroMultiplier, negativeMultiplier, infiniteMultiplier)
	}
	var unsafeDefaultInput float64
	var unsafeDefaultMultiplier sql.NullFloat64
	if err := app.db.QueryRow(`
		SELECT input_usd_per_million, priority_multiplier
		FROM model_prices
		WHERE source = 'litellm' AND model = 'gpt-5.4'
	`).Scan(&unsafeDefaultInput, &unsafeDefaultMultiplier); err != nil {
		t.Fatalf("query unsafe default price: %v", err)
	}
	if unsafeDefaultInput != 1e300 || unsafeDefaultMultiplier.Valid {
		t.Fatalf("unsafe default price = %v/%v, want 1e300/NULL", unsafeDefaultInput, unsafeDefaultMultiplier)
	}
	prices, err := app.priceMap(context.Background())
	if err != nil {
		t.Fatalf("load synced prices: %v", err)
	}
	provider, model, serviceTier := "openai", "gpt-5.5", "priority"
	amount, unpriced := recordCost(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &serviceTier,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices)
	if amount != 0 || !unpriced {
		t.Fatalf("synced zero multiplier cost = %v/%v, want 0/true", amount, unpriced)
	}
	model = "gpt-5.4"
	amount, unpriced = recordCost(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &serviceTier,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices)
	if amount != 1e300 || unpriced {
		t.Fatalf("synced unsafe-default Fast cost = %v/%v, want base 1e300/false", amount, unpriced)
	}
}

func TestLiteLLMSyncAppliesAndPreservesLongContextOverrides(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	rawData := map[string]any{
		"gpt-5.6-terra": map[string]any{
			"litellm_provider":            "openai",
			"input_cost_per_token":        0.0000025,
			"output_cost_per_token":       0.000015,
			"cache_read_input_token_cost": 0.00000025,
		},
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	prices, err := app.listPrices(context.Background())
	if err != nil || len(prices) != 1 {
		t.Fatalf("initial synced prices = %#v/%v", prices, err)
	}
	price := prices[0]
	if price.LongContext == nil || price.LongContext.ThresholdInputTokens != 272000 || price.LongContext.InputUSDPerMillion != 5 {
		t.Fatalf("default synced long-context price = %#v", price.LongContext)
	}

	if _, err := app.db.Exec(`
		UPDATE model_prices
		SET long_context_threshold_tokens = 300000,
		    long_context_input_usd_per_million = 7,
		    long_context_output_usd_per_million = 20,
		    long_context_cache_read_usd_per_million = 0.7,
		    long_context_cache_creation_usd_per_million = 8
		WHERE id = ?
	`, price.ID); err != nil {
		t.Fatalf("set local override: %v", err)
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData); err != nil {
		t.Fatalf("override sync: %v", err)
	}
	prices, err = app.listPrices(context.Background())
	if err != nil || len(prices) != 1 || prices[0].LongContext == nil {
		t.Fatalf("preserved synced prices = %#v/%v", prices, err)
	}
	if prices[0].LongContext.ThresholdInputTokens != 300000 || prices[0].LongContext.InputUSDPerMillion != 7 || prices[0].LongContext.CacheCreationUSDPerMillion != 8 {
		t.Fatalf("preserved long-context override = %#v", prices[0].LongContext)
	}

	if _, err := app.db.Exec(`
		UPDATE model_prices
		SET priority_multiplier = NULL,
		    long_context_output_usd_per_million = 1e300
		WHERE id = ?
	`, prices[0].ID); err != nil {
		t.Fatalf("set multiplier-unsafe local override: %v", err)
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData); err != nil {
		t.Fatalf("multiplier-unsafe override sync: %v", err)
	}
	prices, err = app.listPrices(context.Background())
	if err != nil || len(prices) != 1 || prices[0].LongContext == nil {
		t.Fatalf("multiplier-unsafe synced price = %#v/%v", prices, err)
	}
	if prices[0].PriorityMultiplier != nil || prices[0].LongContext.OutputUSDPerMillion != 1e300 {
		t.Fatalf("multiplier-unsafe override = %#v, want preserved tier and NULL default multiplier", prices[0])
	}

	if _, err := app.db.Exec(`
		UPDATE model_prices
		SET long_context_output_usd_per_million = NULL
		WHERE id = ?
	`, prices[0].ID); err != nil {
		t.Fatalf("set invalid legacy override: %v", err)
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData); err != nil {
		t.Fatalf("invalid override sync: %v", err)
	}
	var threshold sql.NullInt64
	var input, output sql.NullFloat64
	if err := app.db.QueryRow(`
		SELECT long_context_threshold_tokens, long_context_input_usd_per_million,
		       long_context_output_usd_per_million
		FROM model_prices WHERE model = 'gpt-5.6-terra'
	`).Scan(&threshold, &input, &output); err != nil {
		t.Fatalf("query invalid legacy override: %v", err)
	}
	if !threshold.Valid || threshold.Int64 != 300000 || !input.Valid || input.Float64 != 7 || output.Valid {
		t.Fatalf("invalid legacy override = %#v/%#v/%#v, want preserved partial values", threshold, input, output)
	}
	prices, err = app.listPrices(context.Background())
	if err != nil || len(prices) != 1 || !prices[0].longContextInvalid {
		t.Fatalf("invalid legacy internal price = %#v/%v", prices, err)
	}
	if modelPriceForAPI(prices[0]).LongContext != nil {
		t.Fatal("invalid legacy long-context price should be JSON-safe null")
	}

	if _, err := app.updatePrice(context.Background(), prices[0].ID, modelPricePayload{
		Provider:                   "openai",
		Model:                      "gpt-5.6-terra",
		InputUSDPerMillion:         2.5,
		OutputUSDPerMillion:        15,
		CacheReadUSDPerMillion:     0.25,
		CacheCreationUSDPerMillion: 0,
	}); err != nil {
		t.Fatalf("convert synced price to manual with disabled long context: %v", err)
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData); err != nil {
		t.Fatalf("manual preservation sync: %v", err)
	}
	prices, err = app.listPrices(context.Background())
	if err != nil || len(prices) != 1 || prices[0].Source != "manual" || prices[0].LongContext != nil {
		t.Fatalf("manual disabled long-context price = %#v/%v", prices, err)
	}
}

func TestListPricesOrdersManualBeforeSynced(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := dbTime(time.Now().In(appTimeLocation))
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			auto_synced, updated_at
		) VALUES
			('aaa-synced', 'aaa-model', 1, 1, 0, 0, 'litellm', 1, ?),
			('zzz-manual', 'zzz-model', 1, 1, 0, 0, 'manual', 0, ?)
	`, now, now); err != nil {
		t.Fatalf("seed prices: %v", err)
	}

	prices, err := app.listPrices(context.Background())
	if err != nil {
		t.Fatalf("listPrices failed: %v", err)
	}
	if len(prices) != 2 {
		t.Fatalf("prices length = %d, want 2", len(prices))
	}
	if prices[0].AutoSynced || prices[0].Source != "manual" || prices[0].Model != "zzz-model" {
		t.Fatalf("first price = %#v, want manual price first", prices[0])
	}
	if !prices[1].AutoSynced || prices[1].Source != "litellm" || prices[1].Model != "aaa-model" {
		t.Fatalf("second price = %#v, want synced price second", prices[1])
	}
}

func TestModelPriceCatalogListsCPAModelsWithMatchedPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var seenAuth string
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-priced", "name": "GPT Priced", "owner": "openai", "object": "model"},
				{"id": "missing/model", "object": "model"},
			},
		})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)

	now := dbTime(time.Now().In(appTimeLocation))
	apiKey := "sk-catalog-test"
	if _, err := app.db.Exec(`
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, 1, ?, 'Admin Key', ?, ?)
	`, hashAPIKey(apiKey), apiKey, now, now); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			long_context_threshold_tokens, long_context_input_usd_per_million,
			long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
			long_context_cache_creation_usd_per_million, source,
			source_model, auto_synced, last_synced_at, updated_at
		) VALUES ('openai', 'gpt-priced', 1, 2, 0.1, 0, 200000, 3, 6, 0.3, 0, 'litellm', 'gpt-priced', 1, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed model price: %v", err)
	}

	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if seenAuth != "Bearer "+apiKey {
		t.Fatalf("Authorization = %q, want bearer api key", seenAuth)
	}
	if catalog.APIKeyCount != 1 || catalog.QueryableAPIKeyCount != 1 {
		t.Fatalf("key counts = %d/%d, want 1/1", catalog.APIKeyCount, catalog.QueryableAPIKeyCount)
	}
	if catalog.PricedModels != 1 || catalog.UnpricedModels != 1 {
		t.Fatalf("priced/unpriced = %d/%d, want 1/1", catalog.PricedModels, catalog.UnpricedModels)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models length = %d, want 2", len(catalog.Models))
	}
	if catalog.Models[0].ID != "missing/model" || catalog.Models[0].Price != nil || catalog.Models[0].SuggestedProvider != "missing" {
		t.Fatalf("first model = %#v, want missing/model unpriced with suggested provider", catalog.Models[0])
	}
	if catalog.Models[1].ID != "gpt-priced" || catalog.Models[1].Price == nil || catalog.Models[1].Price.Source != "litellm" {
		t.Fatalf("second model = %#v, want gpt-priced with litellm price", catalog.Models[1])
	}
	if catalog.Models[1].Price.LongContext == nil || catalog.Models[1].Price.LongContext.ThresholdInputTokens != 200000 || catalog.Models[1].Price.LongContext.OutputUSDPerMillion != 6 {
		t.Fatalf("catalog long-context price = %#v", catalog.Models[1].Price.LongContext)
	}
	if len(catalog.Models[1].Sources) != 1 || catalog.Models[1].Sources[0].Description != "Admin Key" || catalog.Models[1].Sources[0].UserLabel != "管理员" {
		t.Fatalf("sources = %#v, want key description and user label", catalog.Models[1].Sources)
	}
}

func TestAvailableModelPriceProjectionIncludesJSONSafeLongContext(t *testing.T) {
	projected := availableModelPriceFromPrice(&ModelPrice{
		Provider: "openai",
		Model:    "gpt-available-long",
		LongContext: &ModelPriceLongContext{
			ThresholdInputTokens:   200000,
			InputUSDPerMillion:     3,
			OutputUSDPerMillion:    6,
			CacheReadUSDPerMillion: 0.3,
		},
		BillingUnit: modelBillingUnitToken,
	})
	if projected == nil || projected.LongContext == nil || projected.LongContext.InputUSDPerMillion != 3 {
		t.Fatalf("available model price = %#v", projected)
	}
	invalid := availableModelPriceFromPrice(&ModelPrice{
		Provider: "openai",
		Model:    "gpt-available-invalid-long",
		LongContext: &ModelPriceLongContext{
			ThresholdInputTokens: 200000,
			InputUSDPerMillion:   math.Inf(1),
		},
		longContextInvalid: true,
	})
	if invalid == nil || invalid.LongContext != nil {
		t.Fatalf("invalid available model price = %#v, want JSON-safe null long context", invalid)
	}
}

func TestModelPriceCatalogTreatsImageWithoutRequestPriceAsUnpriced(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-image-2", "name": "GPT Image", "owner": "openai", "object": "model"},
			},
		})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)

	now := dbTime(time.Now().In(appTimeLocation))
	apiKey := "sk-catalog-image-test"
	if _, err := app.db.Exec(`
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, 1, ?, 'Admin Key', ?, ?)
	`, hashAPIKey(apiKey), apiKey, now, now); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			source_model, auto_synced, last_synced_at, updated_at
		) VALUES ('openai', 'gpt-image-2', 5, 10, 1.25, 0, 'litellm', 'gpt-image-2', 1, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed model price: %v", err)
	}

	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if catalog.PricedModels != 0 || catalog.UnpricedModels != 1 {
		t.Fatalf("priced/unpriced = %d/%d, want 0/1", catalog.PricedModels, catalog.UnpricedModels)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].Price == nil {
		t.Fatalf("catalog models = %#v, want matched unpriced image price", catalog.Models)
	}
	if catalog.Models[0].Price.BillingUnit != modelBillingUnitRequest || catalog.Models[0].Price.RequestUSD != nil {
		t.Fatalf("image price = %#v, want request billing with nil request_usd", catalog.Models[0].Price)
	}
}

func TestLiteLLMSyncUsesConfiguredHTTPProxy(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		http.Error(w, "direct request should not be used", http.StatusBadGateway)
	}))
	defer target.Close()

	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls++
		if r.URL.String() != target.URL+"/prices.json" {
			t.Errorf("proxied request URL = %q, want %q", r.URL.String(), target.URL+"/prices.json")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxy-model": map[string]any{
				"litellm_provider":     "openai",
				"input_cost_per_token": 0.000001,
			},
		})
	}))
	defer proxy.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)

	var settings struct {
		Enabled  bool   `json:"enabled"`
		ProxyURL string `json:"proxy_url"`
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/litellm-proxy", nil, cookies, &settings)
	if settings.Enabled || settings.ProxyURL != "" {
		t.Fatalf("default proxy settings = %#v, want disabled empty proxy", settings)
	}
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/litellm-proxy", map[string]any{
		"enabled":   true,
		"proxy_url": proxy.URL,
	}, cookies, &settings)
	if !settings.Enabled || settings.ProxyURL != proxy.URL {
		t.Fatalf("saved proxy settings = %#v, want enabled %q", settings, proxy.URL)
	}

	var syncResult struct {
		Imported int `json:"imported"`
	}
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices/sync/litellm", map[string]any{
		"source_url": target.URL + "/prices.json",
	}, cookies, &syncResult)
	if syncResult.Imported != 1 {
		t.Fatalf("imported = %d, want 1", syncResult.Imported)
	}
	if proxyCalls != 1 || targetCalls != 0 {
		t.Fatalf("proxy/direct calls = %d/%d, want 1/0", proxyCalls, targetCalls)
	}
}

func TestNormalizeLiteLLMProxyURLAcceptsSock5Alias(t *testing.T) {
	normalized, err := normalizeLiteLLMProxyURL("sock5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("normalizeLiteLLMProxyURL failed: %v", err)
	}
	if normalized != "socks5://127.0.0.1:1080" {
		t.Fatalf("normalized proxy URL = %q, want socks5://127.0.0.1:1080", normalized)
	}
}

func requestJSONForPricingTest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	cookies []*http.Cookie,
	target any,
) []*http.Cookie {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return append(cookies, recorder.Result().Cookies()...)
}

func requestJSONForPricingTestExpectStatus(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	cookies []*http.Cookie,
	wantStatus int,
) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s returned %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
}
