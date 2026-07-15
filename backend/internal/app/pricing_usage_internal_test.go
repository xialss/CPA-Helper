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
	"strings"
	"sync"
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

func TestRankingFromRecordsSortsBeforeTwentyItemLimit(t *testing.T) {
	provider := "ranking-vendor"
	brand := string(aiProviderBrandOpenAICompatibility)
	authType := modelPriceChannelAuthTypeAPIKey
	prices := modelPriceIndex{}
	records := make([]UsageRecord, 0, 45)
	for index := 0; index < 20; index++ {
		model := fmt.Sprintf("token-heavy-image-%02d", index)
		requestUSD := 1.0
		prices[channelModelPriceKey(authType, brand, provider, model)] = ModelPrice{
			ID:              index + 1,
			Provider:        provider,
			Model:           model,
			PriceScope:      modelPriceScopeChannel,
			ChannelAuthType: &authType,
			ChannelBrand:    &brand,
			ChannelKey:      &provider,
			RequestUSD:      &requestUSD,
		}
		records = append(records, UsageRecord{
			Provider:    &provider,
			Model:       &model,
			TotalTokens: 1_000 + index,
		})
	}
	specialModel := "low-token-high-cost-and-records-image"
	specialRequestUSD := 4.0
	prices[channelModelPriceKey(authType, brand, provider, specialModel)] = ModelPrice{
		ID:              100,
		Provider:        provider,
		Model:           specialModel,
		PriceScope:      modelPriceScopeChannel,
		ChannelAuthType: &authType,
		ChannelBrand:    &brand,
		ChannelKey:      &provider,
		RequestUSD:      &specialRequestUSD,
	}
	for index := 0; index < 25; index++ {
		records = append(records, UsageRecord{
			Provider:    &provider,
			Model:       &specialModel,
			TotalTokens: 1,
		})
	}
	specialKey := provider + "::" + specialModel

	tokenItems := rankingFromRecords(records, prices, "model", nil)["items"].([]map[string]any)
	if len(tokenItems) != 20 {
		t.Fatalf("token ranking length = %d, want 20", len(tokenItems))
	}
	for _, item := range tokenItems {
		if item["key"] == specialKey {
			t.Fatalf("low-token group unexpectedly entered token top 20: %#v", item)
		}
	}
	for _, sortBy := range []string{usageRankingSortCost, usageRankingSortRecords} {
		items := rankingFromRecordsBySort(records, prices, "model", nil, sortBy)["items"].([]map[string]any)
		if len(items) != 20 || items[0]["key"] != specialKey {
			t.Fatalf("%s ranking = %#v, want special group first in capped top 20", sortBy, items)
		}
	}
	if _, ok := usageRankingSort("invalid"); ok {
		t.Fatal("invalid ranking sort was accepted")
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

func TestNamedOpenAICompatibleClaudeUsesMatchedChannelTokenSemantics(t *testing.T) {
	provider := "claude"
	model := "oai-claude-model"
	brand := string(aiProviderBrandOpenAICompatibility)
	channelKey := "claude"
	price := ModelPrice{
		ID:                         1,
		Provider:                   provider,
		Model:                      model,
		PriceScope:                 modelPriceScopeChannel,
		ChannelBrand:               &brand,
		ChannelKey:                 &channelKey,
		InputUSDPerMillion:         1,
		OutputUSDPerMillion:        0,
		CacheReadUSDPerMillion:     0,
		CacheCreationUSDPerMillion: 0,
		LongContext: &ModelPriceLongContext{
			ThresholdInputTokens:       120,
			InputUSDPerMillion:         10,
			OutputUSDPerMillion:        0,
			CacheReadUSDPerMillion:     0,
			CacheCreationUSDPerMillion: 0,
		},
	}
	prices := channelPricesByKey([]ModelPrice{price})
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		TotalTokens:         100,
	}

	breakdown := calculateRecordCostBreakdown(record, prices)
	if breakdown.Unpriced || breakdown.TotalUSD != 0.00008 {
		t.Fatalf("OpenAI-compatible Claude cost = %#v, want base total 0.00008", breakdown)
	}
	if breakdown.NormalInputTokens != 50 || breakdown.CacheReadTokens != 20 || breakdown.CacheCreationTokens != 30 {
		t.Fatalf("OpenAI-compatible Claude normalized tokens = %d/%d/%d, want 50/20/30", breakdown.NormalInputTokens, breakdown.CacheReadTokens, breakdown.CacheCreationTokens)
	}
	if breakdown.ContextInputTokens != 100 || breakdown.LongContextApplied {
		t.Fatalf("OpenAI-compatible Claude context = %d applied=%v, want 100/false", breakdown.ContextInputTokens, breakdown.LongContextApplied)
	}

	summary := usageSummaryFromRecords(UsageFilters{}, []UsageRecord{record}, prices)
	if summary["input_tokens"] != 100 || summary["total_tokens"] != 100 || summary["normal_input_tokens"] != 50 {
		t.Fatalf("OpenAI-compatible Claude summary = %#v", summary)
	}
	keeperUsage := keeperQuotaWindowUsage{}
	addRecordToKeeperQuotaWindowUsage(&keeperUsage, record, prices)
	if keeperUsage.InputTokens != 100 || keeperUsage.TotalTokens != 100 || keeperUsage.EstimatedCostUSD != 0.00008 || keeperUsage.UnpricedRecords != 0 {
		t.Fatalf("OpenAI-compatible Claude keeper usage = %#v", keeperUsage)
	}

	matchContext := modelPriceMatchContext{
		Selectors: modelPriceChannelSelectors([]aiProviderItem{{
			Brand:  aiProviderBrandOpenAICompatibility,
			Name:   &provider,
			Models: []aiProviderModel{{Name: model}},
		}}),
		SelectorsRequired:  true,
		SelectorsAvailable: true,
	}
	unpriced := calculateRecordCostBreakdown(record, modelPriceIndex{}, matchContext)
	if !unpriced.Unpriced || unpriced.UnpricedReason == nil || *unpriced.UnpricedReason != priceMatchStatusChannelUnpriced {
		t.Fatalf("unpriced OpenAI-compatible Claude breakdown = %#v", unpriced)
	}
	if unpriced.NormalInputTokens != 50 || unpriced.ContextInputTokens != 100 {
		t.Fatalf("unpriced OpenAI-compatible Claude token semantics = %#v, want normal/context 50/100", unpriced)
	}
}

func TestBillingPriceIndexUsesCachedSelectorsWithoutChannelPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	sharedAuthIndex := "shared-google-auth"
	sharedModel := "shared-google-model"
	openAIModel := "openai-claude-model"
	managementCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managementCalls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/gemini-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"auth-index": sharedAuthIndex,
				"models":     []map[string]any{{"name": sharedModel}},
			}})
		case "/v0/management/vertex-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"auth-index": sharedAuthIndex,
				"models":     []map[string]any{{"name": sharedModel}},
			}})
		case "/v0/management/openai-compatibility":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name":   "claude",
				"models": []map[string]any{{"name": openAIModel}},
			}})
		case "/v0/management/codex-api-key", "/v0/management/claude-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
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
	app.collector.Stop()
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
	if managementCalls != len(aiProviderBrandConfigs) {
		t.Fatalf("management calls after refresh = %d, want %d", managementCalls, len(aiProviderBrandConfigs))
	}

	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	if len(pricing.Prices) != 0 || !pricing.MatchContext.SelectorsAvailable {
		t.Fatalf("empty-price selector context = %#v prices=%d, want available selectors", pricing.MatchContext, len(pricing.Prices))
	}
	if _, err := app.billingPriceIndex(ctx); err != nil {
		t.Fatalf("second billingPriceIndex failed: %v", err)
	}
	if managementCalls != len(aiProviderBrandConfigs) {
		t.Fatalf("management calls after billing indexes = %d, want cached %d", managementCalls, len(aiProviderBrandConfigs))
	}

	googleProvider := "google"
	conflict := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &googleProvider,
		Model:       &sharedModel,
		AuthIndex:   &sharedAuthIndex,
		InputTokens: 1,
		TotalTokens: 1,
	}, pricing.Prices, pricing.MatchContext)
	if !conflict.Unpriced || conflict.UnpricedReason == nil || *conflict.UnpricedReason != priceMatchStatusChannelConflict {
		t.Fatalf("empty-price Google breakdown = %#v, want channel_conflict", conflict)
	}

	claudeProvider := "claude"
	openAICompatible := calculateRecordCostBreakdown(UsageRecord{
		Provider:            &claudeProvider,
		Model:               &openAIModel,
		InputTokens:         100,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		TotalTokens:         100,
	}, pricing.Prices, pricing.MatchContext)
	if !openAICompatible.Unpriced || openAICompatible.UnpricedReason == nil || *openAICompatible.UnpricedReason != priceMatchStatusChannelUnpriced {
		t.Fatalf("empty-price OpenAI-compatible Claude breakdown = %#v, want channel_unpriced", openAICompatible)
	}
	if openAICompatible.NormalInputTokens != 50 || openAICompatible.CacheReadTokens != 20 || openAICompatible.CacheCreationTokens != 30 || openAICompatible.ContextInputTokens != 100 {
		t.Fatalf("empty-price OpenAI-compatible Claude tokens = %#v, want 50/20/30 context 100", openAICompatible)
	}
}

func TestAIProviderConfigSnapshotRejectsInvalidatedInFlightStore(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	oldAuthIndex := "old-auth"
	oldModel := "old-model"
	newAuthIndex := "new-auth"
	newModel := "new-model"
	var providerMu sync.Mutex
	geminiProviders := []map[string]any{{
		"auth-index": oldAuthIndex,
		"models":     []map[string]any{{"name": oldModel}},
	}}
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	var releaseSnapshotOnce sync.Once
	releaseSnapshotRequest := func() {
		releaseSnapshotOnce.Do(func() { close(releaseSnapshot) })
	}
	var blockSnapshot sync.Once
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/gemini-api-key":
			switch r.Method {
			case http.MethodGet:
				providerMu.Lock()
				payload := append([]map[string]any(nil), geminiProviders...)
				providerMu.Unlock()
				blockSnapshot.Do(func() {
					close(snapshotStarted)
					<-releaseSnapshot
				})
				_ = json.NewEncoder(w).Encode(payload)
			case http.MethodPut:
				var payload []map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				providerMu.Lock()
				geminiProviders = payload
				providerMu.Unlock()
				_ = json.NewEncoder(w).Encode(payload)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "/v0/management/codex-api-key", "/v0/management/claude-api-key", "/v0/management/openai-compatibility", "/v0/management/vertex-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()
	defer releaseSnapshotRequest()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
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

	type snapshotResult struct {
		providers []aiProviderItem
		err       error
	}
	resultCh := make(chan snapshotResult, 1)
	go func() {
		providers, snapshotErr := app.aiProviderConfigSnapshot(ctx)
		resultCh <- snapshotResult{providers: providers, err: snapshotErr}
	}()
	select {
	case <-snapshotStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("provider snapshot did not start")
	}

	brandConfig, err := aiProviderConfigFor(string(aiProviderBrandGemini))
	if err != nil {
		releaseSnapshotRequest()
		t.Fatalf("aiProviderConfigFor failed: %v", err)
	}
	putErr := app.putAIProviderList(ctx, cfg, brandConfig, []map[string]any{{
		"auth-index": newAuthIndex,
		"models":     []map[string]any{{"name": newModel}},
	}})
	releaseSnapshotRequest()
	if putErr != nil {
		t.Fatalf("putAIProviderList failed: %v", putErr)
	}
	var staleResult snapshotResult
	select {
	case staleResult = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("provider snapshot did not finish")
	}
	if staleResult.err != nil {
		t.Fatalf("stale provider snapshot failed: %v", staleResult.err)
	}
	if _, available := app.priceSelectors.snapshot(); available {
		t.Fatal("invalidated in-flight snapshot repopulated the selector cache")
	}

	if err := app.refreshModelPriceSelectorsIfStale(ctx, cfg); err != nil {
		t.Fatalf("refresh selectors failed: %v", err)
	}
	selectors, available := app.priceSelectors.snapshot()
	if !available {
		t.Fatal("selector cache is unavailable after refresh")
	}
	if selectors[modelPriceChannelIdentityKey(aiProviderBrandGemini, oldAuthIndex, oldModel)] != 0 {
		t.Fatalf("stale selector remained after refresh: %#v", selectors)
	}
	if selectors[modelPriceChannelIdentityKey(aiProviderBrandGemini, newAuthIndex, newModel)] != 1 {
		t.Fatalf("new selector missing after refresh: %#v", selectors)
	}
}

func TestModelPriceSelectorSnapshotCacheRejectsPreviousConfigWriters(t *testing.T) {
	cache := modelPriceSelectorSnapshotCache{}
	cache.retainConfig("old-config")
	generation, current := cache.currentGeneration("old-config")
	if !current {
		t.Fatal("old config generation is not current")
	}
	cache.retainConfig("new-config")
	selectors := modelPriceChannelSelectorIndex{
		modelPriceChannelIdentityKey(aiProviderBrandGemini, "old-auth", "old-model"): 1,
	}
	if cache.store("old-config", generation, time.Now(), selectors) {
		t.Fatal("previous config store unexpectedly succeeded")
	}
	cache.invalidate("old-config")
	if _, current := cache.currentGeneration("new-config"); !current {
		t.Fatal("previous config invalidation replaced the current config")
	}
	load, _, wait, _ := cache.beginRefresh("old-config", time.Now())
	if load || wait != nil {
		t.Fatalf("previous config refresh was accepted: load=%v wait=%v", load, wait != nil)
	}
}

func TestModelPriceSelectorSnapshotCacheRefreshesBeforeExpiry(t *testing.T) {
	assertRefreshWindow := func(t *testing.T, cache *modelPriceSelectorSnapshotCache) {
		t.Helper()
		cache.mu.RLock()
		refreshAfter := cache.refreshAfter
		expiresAt := cache.expiresAt
		cache.mu.RUnlock()
		if !refreshAfter.Before(expiresAt) {
			t.Fatalf("refresh_after = %v, expires_at = %v, want proactive refresh", refreshAfter, expiresAt)
		}
		refreshWindow := expiresAt.Sub(refreshAfter)
		if want := modelPriceSelectorSnapshotTTL - modelPriceSelectorSnapshotRefreshInterval; refreshWindow != want {
			t.Fatalf("refresh window = %v, want %v", refreshWindow, want)
		}
		maxSequentialRefreshDuration := time.Duration(len(aiProviderBrandConfigs)) * aiProviderManagementTimeout
		maxRenewalDuration := modelPriceSelectorSnapshotRefreshInterval + maxSequentialRefreshDuration
		if refreshWindow <= maxRenewalDuration {
			t.Fatalf("refresh window = %v, want greater than heartbeat delay plus max sequential refresh duration %v", refreshWindow, maxRenewalDuration)
		}
	}

	t.Run("store", func(t *testing.T) {
		cache := modelPriceSelectorSnapshotCache{}
		cache.retainConfig("config")
		generation, current := cache.currentGeneration("config")
		if !current || !cache.store("config", generation, time.Now(), modelPriceChannelSelectorIndex{}) {
			t.Fatal("failed to store selector snapshot")
		}
		assertRefreshWindow(t, &cache)
	})

	t.Run("refresh", func(t *testing.T) {
		cache := modelPriceSelectorSnapshotCache{}
		cache.retainConfig("config")
		startedAt := time.Now()
		load, generation, wait, done := cache.beginRefresh("config", startedAt)
		if !load || wait != nil || done == nil {
			t.Fatalf("beginRefresh = load:%v wait:%v done:%v", load, wait != nil, done != nil)
		}
		cache.finishRefresh("config", generation, done, startedAt, modelPriceChannelSelectorIndex{}, nil)
		assertRefreshWindow(t, &cache)
	})
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

func TestRecordCostUsesExactNativeChannelAuthIndex(t *testing.T) {
	provider := "gemini"
	model := "gemini-channel-test"
	authIndexA := "auth-a"
	authIndexB := "auth-b"
	brand := string(aiProviderBrandGemini)
	prices := modelPriceIndex{
		nativeModelPriceKey(brand, authIndexA, model): {
			ID:                 1,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &brand,
			ChannelKey:         &authIndexA,
			InputUSDPerMillion: 1,
		},
		nativeModelPriceKey(brand, authIndexB, model): {
			ID:                 2,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &brand,
			ChannelKey:         &authIndexB,
			InputUSDPerMillion: 4,
		},
	}

	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndexB,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices)
	if breakdown.Unpriced || breakdown.TotalUSD != 4 {
		t.Fatalf("exact channel breakdown = %#v, want priced total 4", breakdown)
	}

	missingAuth := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		InputTokens: 1,
		TotalTokens: 1,
	}, prices)
	if !missingAuth.Unpriced || missingAuth.UnpricedReason == nil || *missingAuth.UnpricedReason != priceMatchStatusMissingAuthIndex {
		t.Fatalf("missing auth breakdown = %#v, want missing_auth_index", missingAuth)
	}
}

func TestRecordCostDoesNotUseSameNamedCompatibleChannelWhenNativeAuthIndexIsMissing(t *testing.T) {
	provider := "gemini"
	model := "gemini-missing-auth-compatible-conflict"
	authIndex := "native-auth"
	nativeBrand := string(aiProviderBrandGemini)
	compatibleBrand := string(aiProviderBrandOpenAICompatibility)
	prices := modelPriceIndex{
		priceKey(provider, model): {
			ID:                 1,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &compatibleBrand,
			ChannelKey:         &provider,
			InputUSDPerMillion: 9,
		},
		nativeModelPriceKey(nativeBrand, authIndex, model): {
			ID:                 2,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &nativeBrand,
			ChannelKey:         &authIndex,
			InputUSDPerMillion: 2,
		},
	}
	nativeIdentity := modelPriceChannelIdentityKey(aiProviderBrandGemini, authIndex, model)
	compatibleIdentity := modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)
	matchContext := modelPriceMatchContext{
		Selectors: modelPriceChannelSelectorIndex{
			nativeIdentity:     1,
			compatibleIdentity: 1,
		},
		SelectorsRequired:  true,
		SelectorsAvailable: true,
	}
	record := UsageRecord{
		Provider:    &provider,
		Model:       &model,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}

	breakdown := calculateRecordCostBreakdown(record, prices, matchContext)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
		t.Fatalf("missing auth with native and compatible channels = %#v, want channel_conflict", breakdown)
	}

	matchContext.Selectors = modelPriceChannelSelectorIndex{nativeIdentity: 1}
	breakdown = calculateRecordCostBreakdown(record, prices, matchContext)
	if !breakdown.Unpriced || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusMissingAuthIndex {
		t.Fatalf("missing auth with native channel = %#v, want missing_auth_index", breakdown)
	}

	matchContext.Selectors = modelPriceChannelSelectorIndex{compatibleIdentity: 1}
	breakdown = calculateRecordCostBreakdown(record, prices, matchContext)
	if breakdown.Unpriced || breakdown.TotalUSD != 9 {
		t.Fatalf("compatible-only channel breakdown = %#v, want priced total 9", breakdown)
	}
}

func TestSelectorlessNativeCandidateBlocksCompatibleBillingAndTokenSemantics(t *testing.T) {
	provider := "claude"
	model := "claude-selectorless-native-conflict"
	compatibleBrand := string(aiProviderBrandOpenAICompatibility)
	providers := []aiProviderItem{
		{
			Brand:  aiProviderBrandClaude,
			Models: []aiProviderModel{{Name: model}},
		},
		{
			Brand:  aiProviderBrandOpenAICompatibility,
			Name:   &provider,
			Models: []aiProviderModel{{Name: model}},
		},
	}
	selectors := modelPriceChannelSelectors(providers)
	nativeIdentity := modelPriceChannelIdentityKey(aiProviderBrandClaude, "", model)
	if selectors[nativeIdentity] != 1 {
		t.Fatalf("selectorless native count = %d, want 1", selectors[nativeIdentity])
	}
	prices := modelPriceIndex{
		priceKey(provider, model): {
			ID:                 1,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &compatibleBrand,
			ChannelKey:         &provider,
			InputUSDPerMillion: 9,
		},
	}
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		TotalTokens:         150,
	}, prices, modelPriceMatchContext{
		Selectors:          selectors,
		SelectorsRequired:  true,
		SelectorsAvailable: true,
	})
	if !breakdown.Unpriced || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
		t.Fatalf("selectorless native conflict breakdown = %#v, want channel_conflict", breakdown)
	}
	if breakdown.NormalInputTokens != 100 || breakdown.CacheReadTokens != 20 || breakdown.CacheCreationTokens != 30 || breakdown.ContextInputTokens != 150 {
		t.Fatalf("selectorless native token semantics = %#v, want runtime Claude semantics", breakdown)
	}
}

func TestRecordCostRejectsAmbiguousGoogleNativeSelectorBeforePriceLookup(t *testing.T) {
	provider := "google"
	model := "gemini-shared-model"
	authIndex := "shared-auth-index"
	geminiBrand := string(aiProviderBrandGemini)
	prices := modelPriceIndex{
		nativeModelPriceKey(geminiBrand, authIndex, model): {
			ID:                 1,
			Provider:           "gemini",
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &geminiBrand,
			ChannelKey:         &authIndex,
			InputUSDPerMillion: 2,
		},
	}
	providers := []aiProviderItem{
		{
			Brand:     aiProviderBrandGemini,
			AuthIndex: &authIndex,
			Models:    []aiProviderModel{{Name: model}},
		},
		{
			Brand:     aiProviderBrandVertex,
			AuthIndex: &authIndex,
			Models:    []aiProviderModel{{Name: model}},
		},
	}
	matchContext := modelPriceMatchContext{
		Selectors:          modelPriceChannelSelectors(providers),
		SelectorsRequired:  true,
		SelectorsAvailable: true,
	}

	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices, matchContext)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
		t.Fatalf("ambiguous Google breakdown = %#v, want channel_conflict", breakdown)
	}

	matchContext.Selectors = modelPriceChannelSelectors(providers[:1])
	breakdown = calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices, matchContext)
	if breakdown.Unpriced || breakdown.TotalUSD != 2 {
		t.Fatalf("unique Google breakdown = %#v, want priced total 2", breakdown)
	}

	unavailable := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndex,
		InputTokens: 1,
		TotalTokens: 1,
	}, prices, modelPriceMatchContext{SelectorsRequired: true})
	if !unavailable.Unpriced || unavailable.UnpricedReason == nil || *unavailable.UnpricedReason != priceMatchStatusChannelConfigUnavailable {
		t.Fatalf("unavailable selector breakdown = %#v, want channel_config_unavailable", unavailable)
	}
}

func TestRecordCostRejectsStoredChannelPricesWhenSelectorsUnavailable(t *testing.T) {
	model := "exact-selector-model"
	tests := []struct {
		name       string
		brand      aiProviderBrand
		provider   string
		channelKey string
		authIndex  *string
	}{
		{
			name:       "named OpenAI-compatible",
			brand:      aiProviderBrandOpenAICompatibility,
			provider:   "Vendor Exact",
			channelKey: "vendor exact",
		},
		{
			name:       "native Codex",
			brand:      aiProviderBrandCodex,
			provider:   "codex",
			channelKey: "Codex-Auth.json",
			authIndex:  stringPtr("Codex-Auth.json"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			brand := string(test.brand)
			price := ModelPrice{
				ID:                 1,
				Provider:           test.provider,
				Model:              model,
				PriceScope:         modelPriceScopeChannel,
				ChannelBrand:       &brand,
				ChannelKey:         &test.channelKey,
				InputUSDPerMillion: 2,
			}
			key := priceKey(test.channelKey, model)
			if test.brand != aiProviderBrandOpenAICompatibility {
				key = nativeModelPriceKey(brand, test.channelKey, model)
			}
			prices := modelPriceIndex{key: price}
			if !modelPriceIndexNeedsConfiguredSelectors(prices) {
				t.Fatalf("channel price index must require configured selectors: %#v", prices)
			}

			breakdown := calculateRecordCostBreakdown(UsageRecord{
				Provider:    &test.provider,
				Model:       &model,
				AuthIndex:   test.authIndex,
				InputTokens: 1_000_000,
				TotalTokens: 1_000_000,
			}, prices, modelPriceMatchContext{SelectorsRequired: true})
			if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil ||
				*breakdown.UnpricedReason != priceMatchStatusChannelConfigUnavailable {
				t.Fatalf("selector-unavailable breakdown = %#v, want channel_config_unavailable", breakdown)
			}
		})
	}
}

func TestBillingPriceIndexRejectsExactPriceWhenProviderSnapshotFails(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	provider := "Vendor Exact"
	channelKey := "vendor exact"
	model := "exact-snapshot-model"
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, updated_at
		) VALUES (?, ?, 'channel', 'openai_compatibility', ?, 2, 0, 0, 0, ?)
	`, provider, model, channelKey, dbTime(time.Now())); err != nil {
		t.Fatalf("seed exact channel price: %v", err)
	}

	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	if !pricing.MatchContext.SelectorsRequired || pricing.MatchContext.SelectorsAvailable {
		t.Fatalf("exact selector context = %#v, want required unavailable snapshot", pricing.MatchContext)
	}
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices, pricing.MatchContext)
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil ||
		*breakdown.UnpricedReason != priceMatchStatusChannelConfigUnavailable {
		t.Fatalf("exact snapshot-failure breakdown = %#v, want channel_config_unavailable", breakdown)
	}
}

func TestRecordCostRejectsDuplicateConfiguredChannelSelectorsBeforePriceLookup(t *testing.T) {
	tests := []struct {
		name       string
		brand      aiProviderBrand
		provider   string
		channelKey string
	}{
		{name: "gemini", brand: aiProviderBrandGemini, provider: "gemini", channelKey: "gemini-auth"},
		{name: "codex", brand: aiProviderBrandCodex, provider: "codex", channelKey: "codex-auth"},
		{name: "claude", brand: aiProviderBrandClaude, provider: "claude", channelKey: "claude-auth"},
		{name: "vertex", brand: aiProviderBrandVertex, provider: "vertex", channelKey: "vertex-auth"},
		{name: "openai-compatible", brand: aiProviderBrandOpenAICompatibility, provider: "Vendor Duplicate", channelKey: "vendor duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := "duplicate-selector-model"
			brand := string(test.brand)
			price := ModelPrice{
				ID:                 1,
				Provider:           test.provider,
				Model:              model,
				PriceScope:         modelPriceScopeChannel,
				ChannelBrand:       &brand,
				ChannelKey:         &test.channelKey,
				InputUSDPerMillion: 2,
			}
			var key [2]string
			if test.brand == aiProviderBrandOpenAICompatibility {
				key = priceKey(test.channelKey, model)
			} else {
				key = nativeModelPriceKey(brand, test.channelKey, model)
			}
			prices := modelPriceIndex{key: price}
			providers := make([]aiProviderItem, 2)
			for index := range providers {
				providers[index] = aiProviderItem{
					Brand:  test.brand,
					Models: []aiProviderModel{{Name: model}},
				}
				if test.brand == aiProviderBrandOpenAICompatibility {
					name := test.provider
					providers[index].Name = &name
				} else {
					authIndex := test.channelKey
					providers[index].AuthIndex = &authIndex
				}
			}
			matchContext := modelPriceMatchContext{
				Selectors:          modelPriceChannelSelectors(providers),
				SelectorsRequired:  true,
				SelectorsAvailable: true,
			}
			record := UsageRecord{
				Provider:    &test.provider,
				Model:       &model,
				InputTokens: 1_000_000,
				TotalTokens: 1_000_000,
			}
			if test.brand != aiProviderBrandOpenAICompatibility {
				record.AuthIndex = &test.channelKey
			}

			breakdown := calculateRecordCostBreakdown(record, prices, matchContext)
			if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
				t.Fatalf("duplicate selector breakdown = %#v, want channel_conflict", breakdown)
			}
		})
	}
}

func TestBillingPriceIndexLoadsDuplicateCodexSelectors(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	authIndex := "shared-codex-auth"
	model := "gpt-shared-codex"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/codex-api-key": []map[string]any{
			{
				"api-key":    "codex-secret-a",
				"auth-index": authIndex,
				"models":     []map[string]any{{"name": model}},
			},
			{
				"api-key":    "codex-secret-b",
				"auth-index": authIndex,
				"models":     []map[string]any{{"name": model}},
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, updated_at
		) VALUES ('codex', ?, 'channel', 'codex', ?, 2, 0, 0, 0, ?)
	`, model, authIndex, dbTime(time.Now())); err != nil {
		t.Fatalf("seed Codex channel price: %v", err)
	}

	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	selector := modelPriceChannelIdentityKey(aiProviderBrandCodex, authIndex, model)
	if !pricing.MatchContext.SelectorsRequired || !pricing.MatchContext.SelectorsAvailable || pricing.MatchContext.Selectors[selector] != 2 {
		t.Fatalf("Codex selector snapshot = %#v, want available count 2", pricing.MatchContext)
	}
	provider := "codex"
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices, pricing.MatchContext)
	if !breakdown.Unpriced || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
		t.Fatalf("duplicate Codex breakdown = %#v, want channel_conflict", breakdown)
	}
}

func TestChannelPriceIndexExcludesLibraryPrices(t *testing.T) {
	provider := "vendor-a"
	model := "shared-model"
	brand := string(aiProviderBrandOpenAICompatibility)
	channelKey := provider
	prices := channelPricesByKey([]ModelPrice{
		{
			ID:                 1,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeLibrary,
			InputUSDPerMillion: 1,
		},
		{
			ID:                 2,
			Provider:           provider,
			Model:              model,
			PriceScope:         modelPriceScopeChannel,
			ChannelBrand:       &brand,
			ChannelKey:         &channelKey,
			InputUSDPerMillion: 7,
		},
	})
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, prices)
	if breakdown.Unpriced || breakdown.TotalUSD != 7 {
		t.Fatalf("channel breakdown = %#v, want exact channel total 7", breakdown)
	}

	otherProvider := "vendor-b"
	unpriced := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &otherProvider,
		Model:       &model,
		InputTokens: 1,
		TotalTokens: 1,
	}, prices)
	if !unpriced.Unpriced || unpriced.UnpricedReason == nil || *unpriced.UnpricedReason != priceMatchStatusChannelUnpriced {
		t.Fatalf("other channel breakdown = %#v, want channel_unpriced", unpriced)
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

func TestNamedOpenAICompatibleChannelSupportsPriorityMultiplier(t *testing.T) {
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
	brand := string(aiProviderBrandOpenAICompatibility)
	channelKey := "vendor a"
	result, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, updated_at
		) VALUES ('Vendor A', 'gpt-vendor-fast', 'channel', ?, ?, 2, 4, 0, 0, ?)
	`, brand, channelKey, dbTime(time.Now()))
	if err != nil {
		t.Fatalf("seed named channel price: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("price id: %v", err)
	}

	var updated ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d/priority-multiplier", id), map[string]any{
		"priority_multiplier": 3,
	}, cookies, &updated)
	if updated.PriorityMultiplier == nil || *updated.PriorityMultiplier != 3 || !modelPriceSupportsPriority(updated) {
		t.Fatalf("updated named channel price = %#v, want supported multiplier 3", updated)
	}

	provider := "Vendor A"
	model := "gpt-vendor-fast"
	tier := "priority"
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:     &provider,
		Model:        &model,
		ServiceTier:  &tier,
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		TotalTokens:  2_000_000,
	}, channelPricesByKey([]ModelPrice{updated}))
	if breakdown.Unpriced || breakdown.TotalUSD != 18 || breakdown.TierMultiplier == nil || *breakdown.TierMultiplier != 3 {
		t.Fatalf("named channel Fast breakdown = %#v, want total 18 with multiplier 3", breakdown)
	}
}

func TestNamedOpenAICompatibleChannelDoesNotSeedPriorityMultiplier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	model := "gpt-5.5"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "openai", "models": []map[string]any{{"name": model}}},
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

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if len(catalog.Models) != 1 || catalog.Models[0].ChannelStatus != modelPriceChannelStatusReady {
		t.Fatalf("catalog models = %#v, want one ready OpenAI-compatible model", catalog.Models)
	}
	item := catalog.Models[0]
	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       item.SuggestedProvider,
		"model":                          item.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  item.ChannelBrand,
		"channel_key":                    item.ChannelKey,
		"channel_identity_hash":          item.ChannelIdentityHash,
		"input_usd_per_million":          1,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, &created)
	if created.PriorityMultiplier != nil || !modelPriceSupportsPriority(created) {
		t.Fatalf("created named OpenAI channel = %#v, want supported Fast pricing with no default multiplier", created)
	}

	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	provider := "openai"
	tier := serviceTierPriority
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &tier,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices, pricing.MatchContext)
	if breakdown.Unpriced || breakdown.TotalUSD != 1 || breakdown.TierMultiplier != nil {
		t.Fatalf("named OpenAI priority breakdown = %#v, want base cost 1 without multiplier", breakdown)
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
	if summary["normal_input_tokens"].(int) != 10 {
		t.Fatalf("summary normal input = %v, want 10", summary["normal_input_tokens"])
	}
	if summary["cache_read_tokens"].(int) != 20 {
		t.Fatalf("summary cache read = %v, want 20", summary["cache_read_tokens"])
	}
	if summary["cache_creation_tokens"].(int) != 30 {
		t.Fatalf("summary cache creation = %v, want 30", summary["cache_creation_tokens"])
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

func TestUsageSummaryAggregatesNormalizedTokensAcrossProviders(t *testing.T) {
	openAIProvider := "openai"
	codexProvider := "codex"
	claudeProvider := "anthropic"
	records := []UsageRecord{
		{
			Provider:            &openAIProvider,
			InputTokens:         100,
			CachedTokens:        90,
			CacheReadTokens:     30,
			CacheCreationTokens: 40,
			TotalTokens:         100,
		},
		{
			Provider:            &codexProvider,
			InputTokens:         50,
			CachedTokens:        70,
			CacheCreationTokens: 20,
			TotalTokens:         50,
		},
		{
			Provider:            &claudeProvider,
			InputTokens:         10,
			CacheReadTokens:     20,
			CacheCreationTokens: 30,
			TotalTokens:         10,
		},
	}

	summary := usageSummaryFromRecords(UsageFilters{}, records, nil)
	if summary["normal_input_tokens"].(int) != 40 {
		t.Fatalf("summary normal input = %v, want 40", summary["normal_input_tokens"])
	}
	if summary["cache_read_tokens"].(int) != 100 {
		t.Fatalf("summary cache read = %v, want 100", summary["cache_read_tokens"])
	}
	if summary["cache_creation_tokens"].(int) != 70 {
		t.Fatalf("summary cache creation = %v, want 70", summary["cache_creation_tokens"])
	}
	if summary["input_tokens"].(int) != 210 {
		t.Fatalf("legacy summary input = %v, want 210", summary["input_tokens"])
	}
	if summary["cached_tokens"].(int) != 160 {
		t.Fatalf("legacy summary cached = %v, want 160", summary["cached_tokens"])
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
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("load synced prices: %v", err)
	}
	if len(pricing.Prices) != 0 {
		t.Fatalf("billing price map length = %d, want LiteLLM library prices excluded", len(pricing.Prices))
	}
	provider, model, serviceTier := "openai", "gpt-5.5", "priority"
	amount, unpriced := recordCost(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		ServiceTier: &serviceTier,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices, pricing.MatchContext)
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
	}, pricing.Prices, pricing.MatchContext)
	if amount != 0 || !unpriced {
		t.Fatalf("synced library Fast cost = %v/%v, want 0/true", amount, unpriced)
	}
}

func TestSyncLiteLLMPricesPreservesUnresolvedConflictSelectedPrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	const selectedID = 910001
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (?, 'OpenAI', 'Conflict-Sync-Model', 'library', NULL, NULL,
			1, 2, 0.1, 0, 'litellm', 'conflict-sync-model', 1,
			'2026-07-14T10:00:00Z', '2026-07-14T10:00:00Z')
	`, selectedID); err != nil {
		t.Fatalf("seed selected LiteLLM price: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (910002, ?, 'case_insensitive_library_identity', 'openai', 'conflict-sync-model',
			3, 4, 0.3, 0, 'litellm', 'conflict-sync-model-legacy', 1,
			'2026-07-14T09:00:00Z', '2026-07-14T09:00:00Z')
	`, selectedID); err != nil {
		t.Fatalf("seed unresolved conflict: %v", err)
	}

	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", map[string]any{
		"conflict-sync-model": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000009,
		},
	}); err != nil {
		t.Fatalf("syncLiteLLMPrices failed: %v", err)
	}
	selected, err := app.getPrice(context.Background(), selectedID)
	if err != nil {
		t.Fatalf("selected conflict price was removed: %v", err)
	}
	if selected.Source != "litellm" || selected.InputUSDPerMillion != 1 {
		t.Fatalf("selected conflict price = %#v, want pinned original row", selected)
	}

	replaced, err := app.replaceActiveModelPriceLibraryConflict(context.Background(), 910002)
	if err != nil {
		t.Fatalf("replaceActiveModelPriceLibraryConflict failed after sync: %v", err)
	}
	if replaced.ID != selectedID || replaced.Source != "manual" || replaced.InputUSDPerMillion != 3 {
		t.Fatalf("replaced conflict price = %#v, want active ID %d and archived values", replaced, selectedID)
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

func TestModelPriceCatalogListsConfiguredChannelsWithExactPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/openai-compatibility": []map[string]any{
			{
				"name": "Vendor A",
				"models": []map[string]any{
					{"name": "gpt-priced", "alias": "GPT Priced"},
					{"name": "missing/model"},
					{"name": "models/gpt-prefixed"},
					{"name": "publishers/google/models/gpt-publisher"},
				},
			},
		},
	})
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
	cfg.Collector.ManagementKey = "test-management-key"
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			long_context_threshold_tokens, long_context_input_usd_per_million,
			long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
			long_context_cache_creation_usd_per_million, source,
			source_model, auto_synced, last_synced_at, updated_at
		) VALUES
			('openai', 'gpt-priced', 'library', NULL, NULL, 1, 2, 0.1, 0, 200000, 3, 6, 0.3, 0, 'litellm', 'gpt-priced', 1, ?, ?),
			('Vendor A', 'gpt-priced', 'channel', 'openai_compatibility', 'vendor a', 4, 8, 0.4, 0, 200000, 5, 10, 0.5, 0, 'manual', NULL, 0, NULL, ?),
			('openai', 'models/gpt-prefixed', 'library', NULL, NULL, 6, 12, 0.6, 0, NULL, NULL, NULL, NULL, NULL, 'manual', NULL, 0, NULL, ?),
			('openai', 'gpt-publisher', 'library', NULL, NULL, 7, 14, 0.7, 0, NULL, NULL, NULL, NULL, NULL, 'manual', NULL, 0, NULL, ?)
	`, now, now, now, now, now); err != nil {
		t.Fatalf("seed model price: %v", err)
	}

	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if catalog.APIKeyCount != 1 || catalog.QueryableAPIKeyCount != 1 {
		t.Fatalf("key counts = %d/%d, want 1/1", catalog.APIKeyCount, catalog.QueryableAPIKeyCount)
	}
	if !catalog.ChannelsAvailable || catalog.ChannelError != nil {
		t.Fatalf("channel availability = %v/%v, want available", catalog.ChannelsAvailable, catalog.ChannelError)
	}
	if catalog.PricedModels != 1 || catalog.UnpricedModels != 3 {
		t.Fatalf("priced/unpriced = %d/%d, want 1/3", catalog.PricedModels, catalog.UnpricedModels)
	}
	if len(catalog.Models) != 4 {
		t.Fatalf("models length = %d, want 4", len(catalog.Models))
	}
	var priced, missing, prefixed, publisher *ModelPriceCatalogItem
	for index := range catalog.Models {
		item := &catalog.Models[index]
		switch item.Name {
		case "gpt-priced":
			priced = item
		case "missing/model":
			missing = item
		case "gpt-prefixed":
			prefixed = item
		case "gpt-publisher":
			publisher = item
		}
	}
	if missing == nil || missing.Price != nil || missing.ChannelLabel != "Vendor A" || missing.ChannelKey != "vendor a" {
		t.Fatalf("missing model = %#v, want unpriced Vendor A channel", missing)
	}
	if priced == nil || priced.Price == nil || priced.Price.PriceScope != modelPriceScopeChannel || priced.Price.InputUSDPerMillion != 4 {
		t.Fatalf("priced model = %#v, want exact channel price", priced)
	}
	if priced.TemplatePrice == nil || priced.TemplatePrice.PriceScope != modelPriceScopeLibrary || priced.TemplatePrice.Source != "litellm" {
		t.Fatalf("template price = %#v, want library LiteLLM price", priced.TemplatePrice)
	}
	if prefixed == nil || prefixed.TemplatePrice == nil || prefixed.TemplatePrice.Model != "models/gpt-prefixed" {
		t.Fatalf("prefixed template price = %#v, want preserved models/ library price", prefixed)
	}
	if publisher == nil || publisher.TemplatePrice == nil || publisher.TemplatePrice.Model != "gpt-publisher" {
		t.Fatalf("publisher template price = %#v, want normalized library price", publisher)
	}
	if priced.Alias == nil || *priced.Alias != "GPT Priced" {
		t.Fatalf("catalog alias = %#v, want GPT Priced", priced.Alias)
	}
}

func TestModelPriceLibraryConflictsAreVisibleAndResolvable(t *testing.T) {
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES
			(900001, 'OpenAI', 'Case-Model', 'library', NULL, NULL, 10, 11, 1, 2, 'manual', 0, '2026-07-13T15:00:00Z'),
			(900002, 'Other', 'Reserved-ID', 'library', NULL, NULL, 1, 1, 0, 0, 'manual', 0, '2026-07-13T15:00:00Z')
	`); err != nil {
		t.Fatalf("seed active library price: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES
			(900002, 900001, 'case_insensitive_library_identity', 'openai', 'case-model',
			 20, 21, 2, 3, 0.5, 2.5, 300000, 30, 31, 3, 4,
			 'litellm', 'legacy-source', 1, '2026-07-13T14:00:00Z', '2026-07-13T16:00:00Z'),
			(900003, 900001, 'case_insensitive_library_identity', 'OPENAI', 'CASE-MODEL',
			 30, 31, 3, 4, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
			 'litellm', 'replacement-source', 1, '2026-07-13T14:30:00Z', '2026-07-13T17:00:00Z')
	`); err != nil {
		t.Fatalf("seed library conflicts: %v", err)
	}

	var conflicts []ModelPriceLibraryConflict
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/library-conflicts", nil, cookies, &conflicts)
	if len(conflicts) != 2 {
		t.Fatalf("library conflicts = %#v, want two rows", conflicts)
	}
	first := conflicts[0]
	if first.OriginalID != 900002 || first.SelectedPriceID != 900001 || first.Price.RequestUSD == nil || *first.Price.RequestUSD != 0.5 || first.Price.PriorityMultiplier == nil || *first.Price.PriorityMultiplier != 2.5 || first.Price.LongContext == nil || first.Price.LongContext.ThresholdInputTokens != 300000 {
		t.Fatalf("first library conflict = %#v, want complete archived price", first)
	}

	var promoted ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/library-conflicts/900002/promote", map[string]any{
		"provider": "OpenAI Legacy",
		"model":    "case-model",
	}, cookies, &promoted)
	if promoted.ID == 900002 || promoted.Provider != "OpenAI Legacy" || promoted.Source != "manual" || promoted.AutoSynced || promoted.SourceModel != nil || promoted.LastSyncedAt != nil || promoted.RequestUSD == nil || *promoted.RequestUSD != 0.5 || promoted.LongContext == nil || promoted.LongContext.ThresholdInputTokens != 300000 {
		t.Fatalf("promoted library price = %#v, want preserved archived row", promoted)
	}

	var replacement ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/library-conflicts/900003/replace-active", nil, cookies, &replacement)
	if replacement.ID != 900001 || replacement.InputUSDPerMillion != 30 || replacement.Provider != "OPENAI" || replacement.Source != "manual" || replacement.AutoSynced || replacement.SourceModel != nil || replacement.LastSyncedAt != nil {
		t.Fatalf("replacement price = %#v, want selected conflict active", replacement)
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/library-conflicts", nil, cookies, &conflicts)
	if len(conflicts) != 1 || conflicts[0].OriginalID != 900003 || conflicts[0].SelectedPriceID != 900001 || conflicts[0].Price.InputUSDPerMillion != 10 {
		t.Fatalf("post-replacement conflicts = %#v, want displaced active row", conflicts)
	}
	if _, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", map[string]any{
		"unrelated-sync-model": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
	}); err != nil {
		t.Fatalf("syncLiteLLMPrices failed: %v", err)
	}
	for _, id := range []int{promoted.ID, replacement.ID} {
		price, err := app.getPrice(context.Background(), id)
		if err != nil {
			t.Fatalf("resolved price %d was removed by LiteLLM sync: %v", id, err)
		}
		if price.Source != "manual" || price.AutoSynced {
			t.Fatalf("resolved price %d ownership = %#v, want manual", id, price)
		}
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/library-conflicts", nil, cookies, &conflicts)
	if len(conflicts) != 1 || conflicts[0].SelectedPriceID != replacement.ID {
		t.Fatalf("post-sync conflicts = %#v, want selected manual price preserved", conflicts)
	}
	requestJSONForPricingTest(t, handler, http.MethodDelete, "/api/model-prices/library-conflicts/900003", nil, cookies, nil)
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/library-conflicts", nil, cookies, &conflicts)
	if len(conflicts) != 0 {
		t.Fatalf("library conflicts after delete = %#v, want empty", conflicts)
	}
}

func TestModelPriceLibraryConflictProtectsSelectedActivePrice(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (920001, 'OpenAI', 'Protected-Model', 'library', NULL, NULL,
			10, 11, 1, 2, 'manual', 0, '2026-07-15T10:00:00Z')
	`); err != nil {
		t.Fatalf("seed selected price: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (920002, 920001, 'case_insensitive_library_identity', 'openai', 'protected-model',
			20, 21, 2, 3, 'manual', 0, '2026-07-15T11:00:00Z')
	`); err != nil {
		t.Fatalf("seed library conflict: %v", err)
	}

	ctx := context.Background()
	updated, err := app.updatePrice(ctx, 920001, modelPricePayload{
		Provider:                   "OpenAI",
		Model:                      "Protected-Model",
		PriceScope:                 modelPriceScopeLibrary,
		InputUSDPerMillion:         12,
		OutputUSDPerMillion:        13,
		CacheReadUSDPerMillion:     1,
		CacheCreationUSDPerMillion: 2,
	})
	if err != nil {
		t.Fatalf("numeric update failed: %v", err)
	}
	if updated.InputUSDPerMillion != 12 || updated.OutputUSDPerMillion != 13 {
		t.Fatalf("numeric update = %#v, want updated values", updated)
	}

	_, err = app.updatePrice(ctx, 920001, modelPricePayload{
		Provider:                   "OpenAI Legacy",
		Model:                      "Protected-Model",
		PriceScope:                 modelPriceScopeLibrary,
		InputUSDPerMillion:         12,
		OutputUSDPerMillion:        13,
		CacheReadUSDPerMillion:     1,
		CacheCreationUSDPerMillion: 2,
	})
	if appErr, ok := err.(*AppError); !ok || appErr.Status != http.StatusConflict {
		t.Fatalf("identity update error = %#v, want conflict", err)
	}
	if err := app.deletePrice(ctx, 920001); err == nil {
		t.Fatal("referenced price delete unexpectedly succeeded")
	} else if appErr, ok := err.(*AppError); !ok || appErr.Status != http.StatusConflict {
		t.Fatalf("referenced price delete error = %#v, want conflict", err)
	}
	if err := app.deleteModelPriceLibraryConflict(ctx, 920002); err != nil {
		t.Fatalf("delete conflict failed: %v", err)
	}
	if err := app.deletePrice(ctx, 920001); err != nil {
		t.Fatalf("delete price after resolving conflict failed: %v", err)
	}
}

func TestModelPriceLibraryConflictAPIPreservesPartialLongContextFields(t *testing.T) {
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (930001, 'OpenAI', 'Partial-Context', 'library', NULL, NULL,
			10, 11, 1, 2, 'manual', 0, '2026-07-15T10:00:00Z')
	`); err != nil {
		t.Fatalf("seed selected price: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			long_context_threshold_tokens, long_context_input_usd_per_million,
			long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
			long_context_cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (930002, 930001, 'case_insensitive_library_identity', 'openai', 'partial-context',
			20, 21, 2, 3, 250000, NULL, 31, NULL, 4, 'manual', 0, '2026-07-15T11:00:00Z')
	`); err != nil {
		t.Fatalf("seed partial long-context conflict: %v", err)
	}

	var conflicts []ModelPriceLibraryConflict
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/library-conflicts", nil, cookies, &conflicts)
	if len(conflicts) != 1 {
		t.Fatalf("library conflicts = %#v, want one row", conflicts)
	}
	conflict := conflicts[0]
	if conflict.Price.LongContext != nil {
		t.Fatalf("sanitized price long context = %#v, want nil for incomplete config", conflict.Price.LongContext)
	}
	raw := conflict.ArchivedLongContext
	if raw == nil || raw.ThresholdInputTokens == nil || *raw.ThresholdInputTokens != 250000 || raw.InputUSDPerMillion != nil || raw.OutputUSDPerMillion == nil || *raw.OutputUSDPerMillion != 31 || raw.CacheReadUSDPerMillion != nil || raw.CacheCreationUSDPerMillion == nil || *raw.CacheCreationUSDPerMillion != 4 {
		t.Fatalf("archived long context = %#v, want exact nullable fields", raw)
	}

	var promoted ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/library-conflicts/930002/promote", map[string]any{
		"provider": "openai-manual",
		"model":    "partial-context-promoted",
	}, cookies, &promoted)
	if promoted.LongContext != nil {
		t.Fatalf("promoted long context = %#v, want JSON-safe nil", promoted.LongContext)
	}
	preserved := promoted.PreservedLongContext
	if preserved == nil || preserved.ThresholdInputTokens == nil || *preserved.ThresholdInputTokens != 250000 || preserved.InputUSDPerMillion != nil || preserved.OutputUSDPerMillion == nil || *preserved.OutputUSDPerMillion != 31 || preserved.CacheReadUSDPerMillion != nil || preserved.CacheCreationUSDPerMillion == nil || *preserved.CacheCreationUSDPerMillion != 4 {
		t.Fatalf("promoted preserved long context = %#v, want exact nullable fields", preserved)
	}

	updatePayload := map[string]any{
		"provider":                       promoted.Provider,
		"model":                          promoted.Model,
		"price_scope":                    modelPriceScopeLibrary,
		"channel_brand":                  nil,
		"channel_key":                    nil,
		"channel_identity_hash":          nil,
		"input_usd_per_million":          22,
		"output_usd_per_million":         23,
		"cache_read_usd_per_million":     2,
		"cache_creation_usd_per_million": 3,
		"request_usd":                    nil,
		"long_context":                   nil,
		"preserve_invalid_long_context":  true,
	}
	var updated ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d", promoted.ID), updatePayload, cookies, &updated)
	if updated.PreservedLongContext == nil || updated.PreservedLongContext.OutputUSDPerMillion == nil || *updated.PreservedLongContext.OutputUSDPerMillion != 31 {
		t.Fatalf("updated preserved long context = %#v, want explicit preserve flag to retain raw fields", updated.PreservedLongContext)
	}

	updatePayload["preserve_invalid_long_context"] = false
	requestJSONForPricingTest(t, handler, http.MethodPut, fmt.Sprintf("/api/model-prices/%d", promoted.ID), updatePayload, cookies, &updated)
	if updated.PreservedLongContext != nil || updated.LongContext != nil {
		t.Fatalf("cleared long context = %#v/%#v, want explicit clear", updated.LongContext, updated.PreservedLongContext)
	}
	var threshold sql.NullInt64
	var input, output, cacheRead, cacheCreation sql.NullFloat64
	if err := app.db.QueryRow(`
		SELECT long_context_threshold_tokens, long_context_input_usd_per_million,
		       long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
		       long_context_cache_creation_usd_per_million
		FROM model_prices WHERE id = ?
	`, promoted.ID).Scan(&threshold, &input, &output, &cacheRead, &cacheCreation); err != nil {
		t.Fatalf("query cleared promoted long context: %v", err)
	}
	if threshold.Valid || input.Valid || output.Valid || cacheRead.Valid || cacheCreation.Valid {
		t.Fatalf("cleared promoted long context = %#v/%#v/%#v/%#v/%#v, want all NULL", threshold, input, output, cacheRead, cacheCreation)
	}
}

func TestReplaceActiveModelPriceLibraryConflictPreservesPartialLongContextFields(t *testing.T) {
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (940001, 'OpenAI', 'Partial-Replace', 'library', NULL, NULL,
			10, 11, 1, 2, 'manual', 0, '2026-07-15T10:00:00Z')
	`); err != nil {
		t.Fatalf("seed selected price: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			long_context_threshold_tokens, long_context_input_usd_per_million,
			long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
			long_context_cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES (940002, 940001, 'case_insensitive_library_identity', 'openai', 'partial-replace',
			20, 21, 2, 3, NULL, 30, NULL, 4, NULL, 'manual', 0, '2026-07-15T11:00:00Z')
	`); err != nil {
		t.Fatalf("seed partial replacement conflict: %v", err)
	}

	var replaced ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/library-conflicts/940002/replace-active", map[string]any{}, cookies, &replaced)
	if replaced.ID != 940001 || replaced.LongContext != nil {
		t.Fatalf("replaced price = %#v, want active ID and JSON-safe nil long context", replaced)
	}
	preserved := replaced.PreservedLongContext
	if preserved == nil || preserved.ThresholdInputTokens != nil || preserved.InputUSDPerMillion == nil || *preserved.InputUSDPerMillion != 30 || preserved.OutputUSDPerMillion != nil || preserved.CacheReadUSDPerMillion == nil || *preserved.CacheReadUSDPerMillion != 4 || preserved.CacheCreationUSDPerMillion != nil {
		t.Fatalf("replacement preserved long context = %#v, want exact nullable fields", preserved)
	}
}

func TestNativeLibraryTemplatePrefersExactProviderBeforeFallback(t *testing.T) {
	model := "shared-template-model"
	tests := []struct {
		name     string
		brand    aiProviderBrand
		exact    string
		fallback string
	}{
		{name: "Codex", brand: aiProviderBrandCodex, exact: "codex", fallback: "openai"},
		{name: "Claude", brand: aiProviderBrandClaude, exact: "claude", fallback: "anthropic"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exact := ModelPrice{ID: 1, Provider: test.exact, Model: model, PriceScope: modelPriceScopeLibrary, InputUSDPerMillion: 1}
			fallback := ModelPrice{ID: 2, Provider: test.fallback, Model: model, PriceScope: modelPriceScopeLibrary, InputUSDPerMillion: 2}
			allPrices := []ModelPrice{fallback, exact}
			prices := libraryPricesByKey(allPrices)
			suggested := suggestedPriceProviderForChannel(test.brand, "", model)
			if suggested != test.exact {
				t.Fatalf("suggested provider = %q, want exact %q", suggested, test.exact)
			}
			matched := findCatalogPrice(prices, allPrices, suggested, nil, model)
			if matched == nil || matched.ID != exact.ID {
				t.Fatalf("exact template = %#v, want %#v", matched, exact)
			}

			fallbackOnly := []ModelPrice{fallback}
			matched = findCatalogPrice(libraryPricesByKey(fallbackOnly), fallbackOnly, suggested, nil, model)
			if matched == nil || matched.ID != fallback.ID {
				t.Fatalf("fallback template = %#v, want %#v", matched, fallback)
			}
		})
	}
}

func TestGeminiLibraryTemplatePrefersPrefixedGeminiPriceOverVertex(t *testing.T) {
	model := "gemini-2.0-flash-001"
	gemini := ModelPrice{
		ID:                 1,
		Provider:           "gemini",
		Model:              "gemini/" + model,
		PriceScope:         modelPriceScopeLibrary,
		InputUSDPerMillion: 1,
	}
	vertex := ModelPrice{
		ID:                 2,
		Provider:           "vertex_ai",
		Model:              model,
		PriceScope:         modelPriceScopeLibrary,
		InputUSDPerMillion: 2,
	}
	google := ModelPrice{
		ID:                 3,
		Provider:           "google",
		Model:              model,
		PriceScope:         modelPriceScopeLibrary,
		InputUSDPerMillion: 3,
	}

	suggested := suggestedPriceProviderForChannel(aiProviderBrandGemini, "", model)
	if suggested != "gemini" {
		t.Fatalf("Gemini suggested provider = %q, want gemini", suggested)
	}
	allPrices := []ModelPrice{vertex, gemini, google}
	matched := findCatalogPrice(libraryPricesByKey(allPrices), allPrices, suggested, nil, model)
	if matched == nil || matched.ID != gemini.ID {
		t.Fatalf("Gemini template = %#v, want prefixed exact price %#v", matched, gemini)
	}

	vertexOnly := []ModelPrice{vertex}
	if matched := findCatalogPrice(libraryPricesByKey(vertexOnly), vertexOnly, suggested, nil, model); matched != nil {
		t.Fatalf("Gemini template = %#v, want no cross-provider Vertex fallback", matched)
	}

	googleOnly := []ModelPrice{google}
	matched = findCatalogPrice(libraryPricesByKey(googleOnly), googleOnly, suggested, nil, model)
	if matched == nil || matched.ID != google.ID {
		t.Fatalf("Gemini fallback template = %#v, want explicit Google fallback %#v", matched, google)
	}
}

func TestCreateNativeChannelPriceResolvesExactConfiguredAuthIndex(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{
				"api-key":    "gemini-secret-key",
				"auth-index": "gemini-auth-index",
				"models":     []map[string]any{{"name": "models/gemini-2.5-pro"}},
			},
		},
	})
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
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if len(catalog.Models) != 1 {
		t.Fatalf("catalog models = %#v, want one Gemini model", catalog.Models)
	}
	item := catalog.Models[0]
	if item.ChannelBrand != string(aiProviderBrandGemini) || item.ChannelKey != "gemini-auth-index" || !strings.Contains(item.ChannelLabel, "...") {
		t.Fatalf("catalog channel identity = %#v", item)
	}

	createPayload := map[string]any{
		"provider":                       "google",
		"model":                          "gemini-2.5-pro",
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  string(aiProviderBrandGemini),
		"channel_key":                    "stale-browser-value",
		"channel_identity_hash":          item.ChannelIdentityHash,
		"input_usd_per_million":          2,
		"output_usd_per_million":         8,
		"cache_read_usd_per_million":     0.2,
		"cache_creation_usd_per_million": 0,
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", createPayload, cookies, http.StatusConflict)

	createPayload["channel_key"] = item.ChannelKey
	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", createPayload, cookies, &created)
	if created.Provider != "gemini" || created.Model != "gemini-2.5-pro" || created.ChannelKey == nil || *created.ChannelKey != "gemini-auth-index" {
		t.Fatalf("created channel price = %#v, want live configured identity", created)
	}

	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	provider, model, authIndex := "gemini", "gemini-2.5-pro", "gemini-auth-index"
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices, pricing.MatchContext)
	if breakdown.Unpriced || breakdown.TotalUSD != 2 {
		t.Fatalf("created channel cost = %#v, want exact total 2", breakdown)
	}
}

func TestModelPriceCatalogDistinguishesSharedNativeIdentityBySelector(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	model := "shared-native-model"
	authUpper := "Auth.json"
	authLower := "auth.json"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{
				"api-key":    "shared-native-secret",
				"auth-index": authUpper,
				"models":     []map[string]any{{"name": model}},
			},
			{
				"api-key":    "shared-native-secret",
				"auth-index": authLower,
				"models":     []map[string]any{{"name": model}},
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

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if len(catalog.Models) != 2 {
		t.Fatalf("catalog models = %#v, want two selector-distinct rows", catalog.Models)
	}
	items := map[string]ModelPriceCatalogItem{}
	for _, item := range catalog.Models {
		items[item.ChannelKey] = item
		if item.ChannelStatus != modelPriceChannelStatusReady {
			t.Fatalf("catalog item = %#v, want ready", item)
		}
	}
	upper, upperOK := items[authUpper]
	lower, lowerOK := items[authLower]
	if !upperOK || !lowerOK {
		t.Fatalf("catalog selectors = %#v, want %q and %q", items, authUpper, authLower)
	}
	if upper.ChannelIdentityHash != lower.ChannelIdentityHash {
		t.Fatalf("shared API key identity hashes differ: %q != %q", upper.ChannelIdentityHash, lower.ChannelIdentityHash)
	}
	if upper.ID == lower.ID {
		t.Fatalf("selector-distinct catalog IDs collided: %q", upper.ID)
	}

	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       lower.SuggestedProvider,
		"model":                          lower.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  lower.ChannelBrand,
		"channel_key":                    lower.ChannelKey,
		"channel_identity_hash":          lower.ChannelIdentityHash,
		"input_usd_per_million":          2,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, &created)
	if created.ChannelKey == nil || *created.ChannelKey != authLower {
		t.Fatalf("created channel price = %#v, want selector %q", created, authLower)
	}
}

func TestModelPriceCatalogRejectsAmbiguousGoogleNativeSelectors(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	authIndex := "shared-google-auth"
	model := "gemini-shared-model"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{
				"api-key":    "gemini-secret-key",
				"auth-index": authIndex,
				"models":     []map[string]any{{"name": model}},
			},
		},
		"/v0/management/vertex-api-key": []map[string]any{
			{
				"api-key":    "vertex-secret-key",
				"auth-index": authIndex,
				"models":     []map[string]any{{"name": model}},
			},
		},
	})
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
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES ('gemini', ?, 'channel', 'gemini', ?, 1, 2, 0, 0, 'manual', 0, ?)
	`, model, authIndex, dbTime(time.Now())); err != nil {
		t.Fatalf("seed conflicted channel price: %v", err)
	}

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if len(catalog.Models) != 2 {
		t.Fatalf("catalog models = %#v, want Gemini and Vertex rows", catalog.Models)
	}
	if catalog.PricedModels != 0 || catalog.UnpricedModels != 2 {
		t.Fatalf("priced/unpriced = %d/%d, want 0/2 conflicted rows", catalog.PricedModels, catalog.UnpricedModels)
	}
	pricedConflictCount := 0
	for _, item := range catalog.Models {
		if item.ChannelStatus != modelPriceChannelStatusConflict {
			t.Fatalf("catalog item = %#v, want conflict status", item)
		}
		if item.Price != nil {
			pricedConflictCount++
			if item.ChannelBrand != string(aiProviderBrandGemini) || item.Price.ChannelBrand == nil || *item.Price.ChannelBrand != string(aiProviderBrandGemini) {
				t.Fatalf("attached conflicted price = %#v, want exact Gemini channel price", item)
			}
		}
	}
	if pricedConflictCount != 1 {
		t.Fatalf("conflicted catalog prices = %d, want one exact attached price", pricedConflictCount)
	}

	item := catalog.Models[0]
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       item.SuggestedProvider,
		"model":                          item.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  item.ChannelBrand,
		"channel_key":                    item.ChannelKey,
		"channel_identity_hash":          item.ChannelIdentityHash,
		"input_usd_per_million":          1,
		"output_usd_per_million":         2,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusConflict)
}

func TestModelPriceCatalogUsesBillingLabelNormalizationForNativeConflicts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	authIndex := "shared-gemini-auth"
	model := "shared-gemini-model"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{
				"api-key":    "gemini-secret-key",
				"auth-index": authIndex,
				"models":     []map[string]any{{"name": model}},
			},
		},
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "gemini_api_key", "models": []map[string]any{{"name": model}}},
			{"name": "geminiapikey", "models": []map[string]any{{"name": model}}},
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

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	var native *ModelPriceCatalogItem
	for index := range catalog.Models {
		if catalog.Models[index].ChannelBrand == string(aiProviderBrandGemini) {
			native = &catalog.Models[index]
			break
		}
	}
	if native == nil || native.ChannelStatus != modelPriceChannelStatusConflict {
		t.Fatalf("native Gemini catalog item = %#v, want normalized-name conflict", native)
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       native.SuggestedProvider,
		"model":                          native.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  native.ChannelBrand,
		"channel_key":                    native.ChannelKey,
		"channel_identity_hash":          native.ChannelIdentityHash,
		"input_usd_per_million":          1,
		"output_usd_per_million":         2,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusConflict)
}

func TestModelPriceCatalogRejectsMissingAuthCompatibleNativeAmbiguity(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	model := "shared-missing-auth-model"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{
				"api-key": "gemini-secret-key",
				"models":  []map[string]any{{"name": model}},
			},
		},
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "gemini", "models": []map[string]any{{"name": model}}},
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

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	var compatible *ModelPriceCatalogItem
	for index := range catalog.Models {
		item := &catalog.Models[index]
		if item.ChannelBrand == string(aiProviderBrandOpenAICompatibility) {
			compatible = item
			if item.ChannelStatus != modelPriceChannelStatusConflict {
				t.Fatalf("compatible catalog item = %#v, want conflict status", item)
			}
		} else if item.ChannelStatus != modelPriceChannelStatusMissingSelector {
			t.Fatalf("selectorless native catalog item = %#v, want missing_selector", item)
		}
	}
	if compatible == nil {
		t.Fatal("missing OpenAI-compatible catalog row")
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       compatible.SuggestedProvider,
		"model":                          compatible.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_brand":                  compatible.ChannelBrand,
		"channel_key":                    compatible.ChannelKey,
		"channel_identity_hash":          compatible.ChannelIdentityHash,
		"input_usd_per_million":          1,
		"output_usd_per_million":         2,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusConflict)
}

func TestModelPriceCatalogAllowsNativeNamedOpenAICompatibilityWithoutNativeCandidates(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "google", "models": []map[string]any{{"name": "oai-google-model"}}},
			{"name": "gemini", "models": []map[string]any{{"name": "oai-gemini-model"}}},
			{"name": "claude", "models": []map[string]any{{"name": "oai-claude-model"}}},
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

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if len(catalog.Models) != 3 {
		t.Fatalf("catalog models = %#v, want three OpenAI-compatible rows", catalog.Models)
	}
	for _, item := range catalog.Models {
		if item.ChannelBrand != string(aiProviderBrandOpenAICompatibility) || item.ChannelStatus != modelPriceChannelStatusReady {
			t.Fatalf("catalog item = %#v, want ready OpenAI-compatible channel", item)
		}
		var created ModelPrice
		requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
			"provider":                       item.SuggestedProvider,
			"model":                          item.Name,
			"price_scope":                    modelPriceScopeChannel,
			"channel_brand":                  item.ChannelBrand,
			"channel_key":                    item.ChannelKey,
			"channel_identity_hash":          item.ChannelIdentityHash,
			"input_usd_per_million":          1,
			"output_usd_per_million":         0,
			"cache_read_usd_per_million":     0,
			"cache_creation_usd_per_million": 0,
		}, cookies, &created)
		if created.ChannelBrand == nil || *created.ChannelBrand != string(aiProviderBrandOpenAICompatibility) || created.ChannelKey == nil || *created.ChannelKey != strings.ToLower(item.ChannelLabel) {
			t.Fatalf("created native-named OpenAI-compatible price = %#v", created)
		}
	}

	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndex failed: %v", err)
	}
	for _, item := range catalog.Models {
		provider := item.ChannelLabel
		model := item.Name
		breakdown := calculateRecordCostBreakdown(UsageRecord{
			Provider:    &provider,
			Model:       &model,
			InputTokens: 1_000_000,
			TotalTokens: 1_000_000,
		}, pricing.Prices, pricing.MatchContext)
		if breakdown.Unpriced || breakdown.TotalUSD != 1 {
			t.Fatalf("native-named OpenAI-compatible breakdown for %q = %#v, want priced total 1", provider, breakdown)
		}
	}
}

func TestChannelCostItemsGroupConcreteChannelsAcrossModels(t *testing.T) {
	provider := "codex"
	apiKeyAuthType := modelPriceChannelAuthTypeAPIKey
	oauthAuthType := modelPriceChannelAuthTypeOAuth
	brand := string(aiProviderBrandCodex)
	apiKeyChannel := "codex-api-key-auth-index"
	oauthChannel := modelPriceOAuthPoolChannelKey
	modelA := "gpt-channel-a"
	modelB := "gpt-channel-b"
	unpricedModel := "gpt-unpriced"
	apiKeyPriceA := 1.0
	apiKeyPriceB := 2.0
	oauthPrice := 4.0

	prices := modelPriceIndex{
		channelModelPriceKey(apiKeyAuthType, brand, apiKeyChannel, modelA): {
			ID:                 1,
			Provider:           provider,
			Model:              modelA,
			PriceScope:         modelPriceScopeChannel,
			ChannelAuthType:    &apiKeyAuthType,
			ChannelBrand:       &brand,
			ChannelKey:         &apiKeyChannel,
			InputUSDPerMillion: apiKeyPriceA,
		},
		channelModelPriceKey(apiKeyAuthType, brand, apiKeyChannel, modelB): {
			ID:                 2,
			Provider:           provider,
			Model:              modelB,
			PriceScope:         modelPriceScopeChannel,
			ChannelAuthType:    &apiKeyAuthType,
			ChannelBrand:       &brand,
			ChannelKey:         &apiKeyChannel,
			InputUSDPerMillion: apiKeyPriceB,
		},
		channelModelPriceKey(oauthAuthType, brand, oauthChannel, modelA): {
			ID:                 3,
			Provider:           provider,
			Model:              modelA,
			PriceScope:         modelPriceScopeChannel,
			ChannelAuthType:    &oauthAuthType,
			ChannelBrand:       &brand,
			ChannelKey:         &oauthChannel,
			InputUSDPerMillion: oauthPrice,
		},
	}
	matchContext := modelPriceMatchContext{
		Selectors: modelPriceChannelSelectorIndex{
			modelPriceChannelIdentityKey(aiProviderBrandCodex, apiKeyChannel, modelA): 1,
			modelPriceChannelIdentityKey(aiProviderBrandCodex, apiKeyChannel, modelB): 1,
		},
		ChannelLabels: modelPriceChannelLabelIndex{
			modelPriceChannelGroupIdentityKey(apiKeyAuthType, aiProviderBrandCodex, apiKeyChannel): {
				Label: "sk-...cafe",
			},
		},
		SelectorsAvailable: true,
	}
	apiKeyAuth := "apikey"
	oauthAuth := "oauth"
	records := []UsageRecord{
		{Provider: &provider, Model: &modelA, Auth: &apiKeyAuth, AuthIndex: &apiKeyChannel, InputTokens: 1_000_000, TotalTokens: 1_000_000},
		{Provider: &provider, Model: &modelB, Auth: &apiKeyAuth, AuthIndex: &apiKeyChannel, InputTokens: 1_000_000, TotalTokens: 1_000_000},
		{Provider: &provider, Model: &modelA, Auth: &oauthAuth, AuthIndex: &apiKeyChannel, InputTokens: 1_000_000, TotalTokens: 1_000_000},
		{Provider: &provider, Model: &unpricedModel, Auth: &apiKeyAuth, AuthIndex: &apiKeyChannel, InputTokens: 1_000_000, TotalTokens: 1_000_000},
	}

	distributions := distributionsFromRecords(records, prices, matchContext)
	items := distributions["channel_costs"].([]usageChannelCostItem)
	if len(items) != 2 {
		t.Fatalf("channel cost items = %#v, want OAuth and API Key groups", items)
	}
	if items[0].Label != "Codex OAuth" || items[0].EstimatedCostUSD != 4 {
		t.Fatalf("first channel cost = %#v, want Codex OAuth cost 4", items[0])
	}
	if items[1].Label != "sk-...cafe" || items[1].EstimatedCostUSD != 3 {
		t.Fatalf("second channel cost = %#v, want grouped API Key cost 3", items[1])
	}
	itemTotal := mathRound(items[0].EstimatedCostUSD+items[1].EstimatedCostUSD, 8)
	summary := usageSummaryFromRecords(UsageFilters{}, records, prices, matchContext)
	if itemTotal != summary["estimated_cost_usd"].(float64) {
		t.Fatalf("channel cost total = %v, summary = %v", itemTotal, summary["estimated_cost_usd"])
	}
	if summary["unpriced_records"].(int) != 1 {
		t.Fatalf("unpriced records = %v, want 1", summary["unpriced_records"])
	}
	for _, item := range items {
		if strings.Contains(item.Key, apiKeyChannel) || strings.Contains(item.Label, apiKeyChannel) {
			t.Fatalf("channel cost item exposed raw selector: %#v", item)
		}
	}
	fallbackContext := matchContext
	fallbackContext.ChannelLabels = modelPriceChannelLabelIndex{
		modelPriceChannelGroupIdentityKey(apiKeyAuthType, aiProviderBrandCodex, apiKeyChannel): {
			Label:         "密钥 identityhash",
			LabelFallback: true,
		},
	}
	fallbackItems := channelCostItems(records[:3], prices, fallbackContext)
	var fallbackItem *usageChannelCostItem
	for index := range fallbackItems {
		if fallbackItems[index].ChannelAuthType == modelPriceChannelAuthTypeAPIKey {
			fallbackItem = &fallbackItems[index]
			break
		}
	}
	if fallbackItem == nil || !fallbackItem.LabelFallback || fallbackItem.Label != "" || fallbackItem.ChannelBrand != brand {
		t.Fatalf("unavailable channel cost item = %#v, want language-neutral Codex API Key fallback metadata", fallbackItem)
	}
	if emptyItems := channelCostItems(nil, prices, matchContext); len(emptyItems) != 0 {
		t.Fatalf("empty channel cost items = %#v, want empty", emptyItems)
	}
	allUnpricedRecords := []UsageRecord{{
		Provider:    &provider,
		Model:       &unpricedModel,
		Auth:        &apiKeyAuth,
		AuthIndex:   &apiKeyChannel,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}}
	if unpricedItems := channelCostItems(allUnpricedRecords, prices, matchContext); len(unpricedItems) != 0 {
		t.Fatalf("all-unpriced channel cost items = %#v, want empty", unpricedItems)
	}
	allUnpricedSummary := usageSummaryFromRecords(UsageFilters{}, allUnpricedRecords, prices, matchContext)
	if allUnpricedSummary["estimated_cost_usd"].(float64) != 0 || allUnpricedSummary["unpriced_records"].(int) != 1 {
		t.Fatalf("all-unpriced summary = %#v, want zero cost and one unpriced record", allUnpricedSummary)
	}
}

func TestModelPriceChannelSelectorUsesNameOnlyForOpenAICompatibility(t *testing.T) {
	masked := "sk-...1234"
	authIndex := "auth-index"
	name := "Named Vendor"
	for _, brand := range []aiProviderBrand{aiProviderBrandGemini, aiProviderBrandCodex, aiProviderBrandClaude, aiProviderBrandVertex} {
		key, label, fallback := modelPriceChannelSelector(aiProviderItem{
			Brand:        brand,
			IdentityHash: "identity",
			AuthIndex:    &authIndex,
			APIKeyMasked: &masked,
		})
		if key != authIndex || label != masked || fallback {
			t.Fatalf("%s selector = %q/%q/%v, want auth index and masked key", brand, key, label, fallback)
		}
	}
	key, label, fallback := modelPriceChannelSelector(aiProviderItem{
		Brand:        aiProviderBrandGemini,
		IdentityHash: "identity",
		AuthIndex:    &authIndex,
	})
	if key != authIndex || label != "密钥 identity" || !fallback {
		t.Fatalf("auth-index-only selector = %q/%q/%v, want non-secret identity fallback label", key, label, fallback)
	}
	key, label, fallback = modelPriceChannelSelector(aiProviderItem{
		Brand:        aiProviderBrandOpenAICompatibility,
		IdentityHash: "identity",
		Name:         &name,
		AuthIndex:    &authIndex,
		APIKeyMasked: &masked,
	})
	if key != "named vendor" || label != name || fallback {
		t.Fatalf("OpenAI-compatible selector = %q/%q/%v, want normalized name/original name", key, label, fallback)
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
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "Image Vendor", "models": []map[string]any{{"name": "gpt-image-2"}}},
		},
	})
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
	cfg.Collector.ManagementKey = "test-management-key"
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
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			source_model, auto_synced, last_synced_at, updated_at
		) VALUES ('Image Vendor', 'gpt-image-2', 'channel', 'openai_compatibility', 'image vendor', 5, 10, 1.25, 0, 'manual', NULL, 0, NULL, ?)
	`, now); err != nil {
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

func newModelPriceCatalogManagementServer(t *testing.T, responses map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			t.Fatalf("management Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if response, ok := responses[r.URL.Path]; ok {
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		switch r.URL.Path {
		case "/v0/management/gemini-api-key", "/v0/management/codex-api-key", "/v0/management/claude-api-key", "/v0/management/openai-compatibility", "/v0/management/vertex-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestOAuthPoolCatalogAndBillingIgnoreAccountAuthIndex(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			t.Fatalf("management Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/gemini-api-key", "/v0/management/codex-api-key", "/v0/management/claude-api-key", "/v0/management/openai-compatibility", "/v0/management/vertex-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"name": "codex-a.json", "type": "codex", "status": "active"},
				{"name": "codex-b.json", "type": "codex", "status": "active"},
			}})
		case "/v0/management/auth-files/models":
			if name := r.URL.Query().Get("name"); name != "codex-a.json" && name != "codex-b.json" {
				t.Fatalf("auth file model query name = %q", name)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"id": "gpt-oauth-test"}}})
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
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "管理员",
	}, nil, nil)
	var catalog ModelPriceCatalogResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
	if !catalog.OAuthChannelsAvailable || catalog.OAuthChannelError != nil || len(catalog.Models) != 1 {
		t.Fatalf("OAuth catalog = %#v", catalog)
	}
	item := catalog.Models[0]
	if item.ChannelAuthType != modelPriceChannelAuthTypeOAuth || item.ChannelBrand != "codex" || item.ChannelKey != modelPriceOAuthPoolChannelKey || item.ChannelAccountCount != 2 {
		t.Fatalf("OAuth catalog item = %#v", item)
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       item.SuggestedProvider,
		"model":                          item.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_auth_type":              "oauth2",
		"channel_brand":                  item.ChannelBrand,
		"channel_key":                    item.ChannelKey,
		"channel_identity_hash":          item.ChannelIdentityHash,
		"input_usd_per_million":          2,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, http.StatusUnprocessableEntity)

	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"provider":                       item.SuggestedProvider,
		"model":                          item.Name,
		"price_scope":                    modelPriceScopeChannel,
		"channel_auth_type":              item.ChannelAuthType,
		"channel_brand":                  item.ChannelBrand,
		"channel_key":                    item.ChannelKey,
		"channel_identity_hash":          item.ChannelIdentityHash,
		"input_usd_per_million":          2,
		"output_usd_per_million":         0,
		"cache_read_usd_per_million":     0,
		"cache_creation_usd_per_million": 0,
	}, cookies, &created)
	if created.ChannelAuthType == nil || *created.ChannelAuthType != modelPriceChannelAuthTypeOAuth {
		t.Fatalf("created OAuth price = %#v", created)
	}

	pricing, err := app.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatalf("billingPriceIndexWithoutSelectors failed: %v", err)
	}
	if pricing.MatchContext.SelectorsRequired {
		t.Fatal("OAuth-only price index should not require API key selectors")
	}
	provider, model, authType, authIndex := "codex", item.Name, "oauth", "different-account.json"
	breakdown := calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		Auth:        &authType,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices)
	if breakdown.Unpriced || breakdown.TotalUSD != 2 {
		t.Fatalf("OAuth pool breakdown = %#v, want priced total 2", breakdown)
	}

	apiKeyAuth := "apikey"
	breakdown = calculateRecordCostBreakdown(UsageRecord{
		Provider:    &provider,
		Model:       &model,
		Auth:        &apiKeyAuth,
		AuthIndex:   &authIndex,
		InputTokens: 1_000_000,
		TotalTokens: 1_000_000,
	}, pricing.Prices)
	if !breakdown.Unpriced {
		t.Fatalf("API key record must not use OAuth pool price: %#v", breakdown)
	}
}

func TestOAuthPoolBillingRejectsAmbiguousGoogleProviderAlias(t *testing.T) {
	provider := "google"
	model := "google-oauth-ambiguous"
	authType := modelPriceChannelAuthTypeOAuth
	poolKey := modelPriceOAuthPoolChannelKey

	for _, brand := range []aiProviderBrand{aiProviderBrandGemini, aiProviderBrandVertex} {
		t.Run(string(brand), func(t *testing.T) {
			brandValue := string(brand)
			prices := modelPriceIndex{
				channelModelPriceKey(authType, brandValue, poolKey, model): {
					ID:                 1,
					Provider:           brandValue,
					Model:              model,
					PriceScope:         modelPriceScopeChannel,
					ChannelAuthType:    &authType,
					ChannelBrand:       &brandValue,
					ChannelKey:         &poolKey,
					InputUSDPerMillion: 2,
				},
			}
			breakdown := calculateRecordCostBreakdown(UsageRecord{
				Provider:    &provider,
				Model:       &model,
				Auth:        &authType,
				InputTokens: 1_000_000,
				TotalTokens: 1_000_000,
			}, prices)
			if !breakdown.Unpriced || breakdown.TotalUSD != 0 || breakdown.UnpricedReason == nil || *breakdown.UnpricedReason != priceMatchStatusChannelConflict {
				t.Fatalf("ambiguous Google OAuth breakdown = %#v, want channel conflict with zero cost", breakdown)
			}
		})
	}
}

func TestValidatePricePayloadDefaultsOnlyMissingOrBlankChannelAuthType(t *testing.T) {
	brand := string(aiProviderBrandCodex)
	channelKey := "api-key-channel"
	basePayload := modelPricePayload{
		Provider:     "codex",
		Model:        "gpt-auth-type-test",
		PriceScope:   modelPriceScopeChannel,
		ChannelBrand: &brand,
		ChannelKey:   &channelKey,
	}

	blank := "  "
	for name, authType := range map[string]*string{"missing": nil, "blank": &blank} {
		t.Run(name, func(t *testing.T) {
			payload := basePayload
			payload.ChannelAuthType = authType
			validated, err := validatePricePayload(payload)
			if err != nil {
				t.Fatalf("validatePricePayload failed: %v", err)
			}
			if validated.ChannelAuthType == nil || *validated.ChannelAuthType != modelPriceChannelAuthTypeAPIKey {
				t.Fatalf("channel auth type = %#v, want apikey", validated.ChannelAuthType)
			}
		})
	}

	unknown := "oauth2"
	invalidPayload := basePayload
	invalidPayload.ChannelAuthType = &unknown
	if _, err := validatePricePayload(invalidPayload); err == nil {
		t.Fatal("unknown channel auth type was accepted")
	}
}

func TestModelPriceCatalogRejectsMalformedOAuthFileListItems(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/auth-files": map[string]any{
			"files": []any{
				map[string]any{"name": "codex-a.json", "type": "codex"},
				"invalid-auth-file-item",
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

	catalog, err := app.modelPriceCatalog(ctx)
	if err != nil {
		t.Fatalf("modelPriceCatalog failed: %v", err)
	}
	if catalog.OAuthChannelsAvailable || catalog.OAuthChannelError == nil || !strings.Contains(*catalog.OAuthChannelError, "auth-files 响应不是有效列表") {
		t.Fatalf("OAuth catalog availability = %v/%v, want explicit malformed-list error", catalog.OAuthChannelsAvailable, catalog.OAuthChannelError)
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
