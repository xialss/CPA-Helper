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
)

func TestNormalizeUsagePrefersLegacyTokensOverBreakdownContainers(t *testing.T) {
	raw := []byte(`{
		"provider":"codex",
		"model":"gpt-test",
		"token_breakdown":{
			"schema_version":2,
			"quality":"inconsistent",
			"total_tokens":999,
			"input":{"total_tokens":0,"uncached_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0},
			"output":{"total_tokens":0,"non_reasoning_tokens":0,"reasoning_tokens":0}
		},
		"tokens":{
			"input_tokens":80942,
			"output_tokens":546,
			"cached_tokens":70000,
			"cache_read_tokens":70000,
			"cache_creation_tokens":123,
			"reasoning_tokens":321,
			"total_tokens":81488
		}
	}`)

	for iteration := 0; iteration < 200; iteration++ {
		usage, err := normalizeUsage(raw)
		if err != nil {
			t.Fatalf("normalizeUsage iteration %d: %v", iteration, err)
		}
		if usage.InputTokens != 80942 || usage.OutputTokens != 546 || usage.CachedTokens != 70000 ||
			usage.CacheReadTokens != 70000 || usage.CacheCreationTokens != 123 ||
			usage.ReasoningTokens != 321 || usage.TotalTokens != 81488 {
			t.Fatalf("iteration %d tokens = input %d output %d cached %d read %d creation %d reasoning %d total %d",
				iteration, usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.CacheReadTokens,
				usage.CacheCreationTokens, usage.ReasoningTokens, usage.TotalTokens)
		}
	}
}

func TestNormalizeUsageSupportsV2TokenBreakdownWithoutLegacyTokens(t *testing.T) {
	raw := []byte(`{
		"provider":"codex",
		"model":"gpt-test",
		"token_breakdown":{
			"schema_version":2,
			"quality":"complete",
			"total_tokens":125,
			"input":{"total_tokens":100,"uncached_tokens":60,"cache_read_tokens":30,"cache_write_tokens":10},
			"output":{"total_tokens":25,"non_reasoning_tokens":20,"reasoning_tokens":5}
		}
	}`)

	usage, err := normalizeUsage(raw)
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 25 || usage.CachedTokens != 30 ||
		usage.CacheReadTokens != 30 || usage.CacheCreationTokens != 10 ||
		usage.ReasoningTokens != 5 || usage.TotalTokens != 125 {
		t.Fatalf("breakdown tokens = input %d output %d cached %d read %d creation %d reasoning %d total %d",
			usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.CacheReadTokens,
			usage.CacheCreationTokens, usage.ReasoningTokens, usage.TotalTokens)
	}
}

func TestSaveUsageMessageUsesMatchedChannelBrandForBreakdownInput(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	const (
		provider  = "claude"
		model     = "claude-breakdown-input"
		authIndex = "native-claude.json"
	)
	nativeBrand := string(aiProviderBrandClaude)
	compatibleBrand := string(aiProviderBrandOpenAICompatibility)
	nativeIdentity := modelPriceChannelIdentityKey(aiProviderBrandClaude, authIndex, model)
	compatibleIdentity := modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)
	nativePricing := modelPriceBillingIndex{
		Prices: modelPriceIndex{
			nativeModelPriceKey(nativeBrand, authIndex, model): {
				ID:                         1,
				Provider:                   provider,
				Model:                      model,
				PriceScope:                 modelPriceScopeChannel,
				ChannelBrand:               &nativeBrand,
				ChannelKey:                 stringPtr(authIndex),
				InputUSDPerMillion:         1,
				OutputUSDPerMillion:        1,
				CacheReadUSDPerMillion:     1,
				CacheCreationUSDPerMillion: 1,
			},
		},
		MatchContext: modelPriceMatchContext{
			Selectors:          modelPriceChannelSelectorIndex{nativeIdentity: 1},
			SelectorsRequired:  true,
			SelectorsAvailable: true,
		},
	}
	compatiblePricing := modelPriceBillingIndex{
		Prices: modelPriceIndex{
			priceKey(provider, model): {
				ID:                         2,
				Provider:                   provider,
				Model:                      model,
				PriceScope:                 modelPriceScopeChannel,
				ChannelBrand:               &compatibleBrand,
				ChannelKey:                 stringPtr(provider),
				InputUSDPerMillion:         1,
				OutputUSDPerMillion:        1,
				CacheReadUSDPerMillion:     1,
				CacheCreationUSDPerMillion: 1,
			},
		},
		MatchContext: modelPriceMatchContext{
			Selectors:          modelPriceChannelSelectorIndex{compatibleIdentity: 1},
			SelectorsRequired:  true,
			SelectorsAvailable: true,
		},
	}
	payload := func(requestID, authIndex string) []byte {
		return []byte(`{"provider":"claude","model":"claude-breakdown-input","auth_type":"apikey","auth_index":"` + authIndex + `","request_id":"` + requestID + `","token_breakdown":{"total_tokens":125,"input":{"total_tokens":100,"uncached_tokens":60,"cache_read_tokens":30,"cache_write_tokens":10},"output":{"total_tokens":25,"non_reasoning_tokens":20,"reasoning_tokens":5}}}`)
	}

	tests := []struct {
		name             string
		pricing          modelPriceBillingIndex
		authIndex        string
		wantStoredInput  int
		wantNormalInput  int
		wantContextInput int
	}{
		{
			name:             "native Claude stores explicit uncached input",
			pricing:          nativePricing,
			authIndex:        authIndex,
			wantStoredInput:  60,
			wantNormalInput:  60,
			wantContextInput: 100,
		},
		{
			name:             "Claude-named OpenAI-compatible channel keeps inclusive input",
			pricing:          compatiblePricing,
			wantStoredInput:  100,
			wantNormalInput:  60,
			wantContextInput: 100,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, created, err := app.saveUsageMessage(context.Background(), payload(test.name+string(rune('0'+index)), test.authIndex), test.pricing)
			if err != nil || !created {
				t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
			}
			if record.InputTokens != test.wantStoredInput {
				t.Fatalf("stored input tokens = %d, want %d", record.InputTokens, test.wantStoredInput)
			}
			if record.OutputTokens != 25 || record.ReasoningTokens != 5 {
				t.Fatalf("stored output/reasoning tokens = %d/%d, want 25/5", record.OutputTokens, record.ReasoningTokens)
			}
			breakdown := calculateRecordCostBreakdown(record, test.pricing.Prices, test.pricing.MatchContext)
			if breakdown.NormalInputTokens != test.wantNormalInput || breakdown.CacheReadTokens != 30 ||
				breakdown.CacheCreationTokens != 10 || breakdown.ContextInputTokens != test.wantContextInput {
				t.Fatalf("normalized breakdown = %#v", breakdown)
			}
			if breakdown.Unpriced || breakdown.TotalUSD != 0.000125 {
				t.Fatalf("cost breakdown = %#v, want priced total 0.000125", breakdown)
			}
			if amount, unpriced := recordCost(record, test.pricing.Prices, test.pricing.MatchContext); unpriced || amount != breakdown.TotalUSD {
				t.Fatalf("shared record cost = %v unpriced=%v, want %v false", amount, unpriced, breakdown.TotalUSD)
			}
			outputItemTokens := -1
			for _, item := range breakdown.Items {
				if tokenItem, ok := item.(usageTokenCostBreakdownItem); ok && tokenItem.Kind == usageCostKindOutput {
					outputItemTokens = tokenItem.Tokens
				}
			}
			if outputItemTokens != 25 {
				t.Fatalf("output cost item tokens = %d, want 25", outputItemTokens)
			}

			summary := usageSummaryFromRecords(UsageFilters{}, []UsageRecord{record}, test.pricing.Prices, test.pricing.MatchContext)
			if summary["total_tokens"].(int) != 125 || summary["reasoning_tokens"].(int) != 5 {
				t.Fatalf("summary total/reasoning tokens = %v/%v, want 125/5", summary["total_tokens"], summary["reasoning_tokens"])
			}
			trends := trendPointsFromRecords(UsageFilters{}, []UsageRecord{record}, test.pricing.Prices, test.pricing.MatchContext)
			if len(trends) != 1 || trends[0]["total_tokens"].(int) != 125 {
				t.Fatalf("trend totals = %#v, want one item with total 125", trends)
			}
		})
	}

	t.Run("OpenAI-compatible missing uncached keeps inclusive input", func(t *testing.T) {
		raw := []byte(`{"provider":"claude","model":"claude-breakdown-input","auth_type":"apikey","request_id":"compatible-missing-uncached","token_breakdown":{"total_tokens":125,"input":{"total_tokens":100,"cache_read_tokens":30,"cache_write_tokens":10},"output":{"total_tokens":25}}}`)
		record, created, err := app.saveUsageMessage(context.Background(), raw, compatiblePricing)
		if err != nil || !created {
			t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
		}
		breakdown := calculateRecordCostBreakdown(record, compatiblePricing.Prices, compatiblePricing.MatchContext)
		if record.InputTokens != 100 || breakdown.NormalInputTokens != 60 || breakdown.ContextInputTokens != 100 || breakdown.TotalUSD != 0.000125 {
			t.Fatalf("compatible record/breakdown = input %d %#v", record.InputTokens, breakdown)
		}
	})
}

func TestSaveUsageMessageUsesRuntimeClaudeFallbackWithoutPriceContext(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := []byte(`{"provider":"claude","model":"claude-breakdown-unpriced","request_id":"runtime-claude-fallback","token_breakdown":{"total_tokens":125,"input":{"total_tokens":100,"uncached_tokens":60,"cache_read_tokens":30,"cache_write_tokens":10},"output":{"total_tokens":25,"reasoning_tokens":5}}}`)
	record, created, err := app.saveUsageMessage(context.Background(), raw, modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.InputTokens != 60 {
		t.Fatalf("stored input tokens = %d, want runtime Claude uncached input 60", record.InputTokens)
	}
	breakdown := calculateRecordCostBreakdown(record, nil)
	if !breakdown.Unpriced || breakdown.NormalInputTokens != 60 || breakdown.CacheReadTokens != 30 ||
		breakdown.CacheCreationTokens != 10 || breakdown.ContextInputTokens != 100 {
		t.Fatalf("unpriced runtime Claude breakdown = %#v", breakdown)
	}
}

func TestSaveUsageMessageDoesNotInventMissingBreakdownUncachedInput(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	const (
		provider  = "claude"
		model     = "claude-breakdown-invalid-uncached"
		authIndex = "native-invalid.json"
	)
	brand := string(aiProviderBrandClaude)
	pricing := modelPriceBillingIndex{
		Prices: modelPriceIndex{
			nativeModelPriceKey(brand, authIndex, model): {
				ID:                         1,
				Provider:                   provider,
				Model:                      model,
				PriceScope:                 modelPriceScopeChannel,
				ChannelBrand:               &brand,
				ChannelKey:                 stringPtr(authIndex),
				InputUSDPerMillion:         1,
				CacheReadUSDPerMillion:     1,
				CacheCreationUSDPerMillion: 1,
			},
		},
		MatchContext: modelPriceMatchContext{
			Selectors: modelPriceChannelSelectorIndex{
				modelPriceChannelIdentityKey(aiProviderBrandClaude, authIndex, model): 1,
			},
			SelectorsRequired:  true,
			SelectorsAvailable: true,
		},
	}
	tests := []struct {
		name        string
		inputFields string
		wantInput   int
		wantError   bool
	}{
		{name: "missing derives from complete components", inputFields: `"total_tokens":100,"cache_read_tokens":30,"cache_write_tokens":10`, wantInput: 60},
		{name: "invalid derives from complete components", inputFields: `"total_tokens":100,"uncached_tokens":"invalid","cache_read_tokens":30,"cache_write_tokens":10`, wantInput: 60},
		{name: "explicit zero remains authoritative", inputFields: `"total_tokens":100,"uncached_tokens":0,"cache_read_tokens":30,"cache_write_tokens":10`, wantInput: 0},
		{name: "negative clamps to zero", inputFields: `"total_tokens":100,"uncached_tokens":-1,"cache_read_tokens":30,"cache_write_tokens":10`, wantInput: 0},
		{name: "missing cache component fails", inputFields: `"total_tokens":100,"cache_read_tokens":30`, wantError: true},
		{name: "cache components exceed total fails", inputFields: `"total_tokens":20,"cache_read_tokens":30,"cache_write_tokens":10`, wantError: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var recordsBefore int
			if err := app.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&recordsBefore); err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"provider":"claude","model":"claude-breakdown-invalid-uncached","auth_type":"apikey","auth_index":"native-invalid.json","request_id":"uncached-` + string(rune('0'+index)) + `","token_breakdown":{"input":{` + test.inputFields + `},"output":{"total_tokens":0}}}`)
			record, created, err := app.saveUsageMessage(context.Background(), raw, pricing)
			if test.wantError {
				if err == nil || created {
					t.Fatalf("saveUsageMessage created=%v err=%v, want explicit rejection", created, err)
				}
				var recordsAfter int
				if queryErr := app.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&recordsAfter); queryErr != nil {
					t.Fatal(queryErr)
				}
				if recordsAfter != recordsBefore {
					t.Fatalf("usage records after rejected payload = %d, want %d", recordsAfter, recordsBefore)
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
			}
			if record.InputTokens != test.wantInput {
				t.Fatalf("stored input tokens = %d, want %d", record.InputTokens, test.wantInput)
			}
			breakdown := calculateRecordCostBreakdown(record, pricing.Prices, pricing.MatchContext)
			wantContextInput := test.wantInput + 40
			wantCost := float64(wantContextInput) / 1_000_000
			if breakdown.NormalInputTokens != test.wantInput || breakdown.ContextInputTokens != wantContextInput ||
				breakdown.CacheReadTokens != 30 || breakdown.CacheCreationTokens != 10 || breakdown.TotalUSD != wantCost {
				t.Fatalf("breakdown = %#v, want input/context/cost %d/%d/%v", breakdown, test.wantInput, wantContextInput, wantCost)
			}
			summary := usageSummaryFromRecords(UsageFilters{}, []UsageRecord{record}, pricing.Prices, pricing.MatchContext)
			if summary["total_tokens"].(int) != wantContextInput {
				t.Fatalf("summary total = %v, want %d", summary["total_tokens"], wantContextInput)
			}
		})
	}
}

func TestSaveUsageMessagePreservesAuthoritativeAndLegacyInputSources(t *testing.T) {
	usageTests := []struct {
		name      string
		raw       string
		wantInput int
	}{
		{
			name:      "authoritative tokens input",
			raw:       `{"tokens":{"input_tokens":90},"token_breakdown":{"input":{"total_tokens":100,"uncached_tokens":60}}}`,
			wantInput: 90,
		},
		{
			name:      "legacy scalar input",
			raw:       `{"input_tokens":80,"token_breakdown":{"input":{"uncached_tokens":60}}}`,
			wantInput: 80,
		},
	}
	for _, test := range usageTests {
		t.Run(test.name, func(t *testing.T) {
			usage, err := normalizeUsage([]byte(test.raw))
			if err != nil {
				t.Fatalf("normalizeUsage: %v", err)
			}
			brand := string(aiProviderBrandClaude)
			usage.Provider = stringPtr("claude")
			usage.Model = stringPtr("claude-source-priority")
			if err := usage.applyBreakdownInputSemantics(modelPriceBillingIndex{
				Prices: modelPriceIndex{
					priceKey("claude", "claude-source-priority"): {
						PriceScope:   modelPriceScopeChannel,
						ChannelBrand: &brand,
					},
				},
			}); err != nil {
				t.Fatalf("applyBreakdownInputSemantics: %v", err)
			}
			if usage.InputTokens != test.wantInput {
				t.Fatalf("input tokens = %d, want %d", usage.InputTokens, test.wantInput)
			}
		})
	}
}

func TestNormalizeUsageKeepsLegacyScalarTokenAliases(t *testing.T) {
	raw := []byte(`{
		"provider":"legacy",
		"usage":{"input":"12","output":3,"cached_input_tokens":2,"reasoning":1,"totalTokens":15}
	}`)

	usage, err := normalizeUsage(raw)
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 3 || usage.CachedTokens != 2 ||
		usage.ReasoningTokens != 1 || usage.TotalTokens != 15 {
		t.Fatalf("legacy tokens = input %d output %d cached %d reasoning %d total %d",
			usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.ReasoningTokens, usage.TotalTokens)
	}
}

func TestNormalizeUsageTokenScalarBoundaries(t *testing.T) {
	raw := []byte(`{
		"provider":"codex",
		"tokens":{
			"input_tokens":{"total_tokens":900},
			"output_tokens":0,
			"cached_tokens":-3,
			"cache_read_tokens":true,
			"cache_creation_tokens":"invalid"
		},
		"token_breakdown":{
			"total_tokens":141,
			"input":{"total_tokens":42,"cache_read_tokens":7,"cache_write_tokens":8},
			"output":{"total_tokens":99}
		}
	}`)

	usage, err := normalizeUsage(raw)
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}
	if usage.InputTokens != 42 || usage.OutputTokens != 0 || usage.CachedTokens != 0 ||
		usage.CacheReadTokens != 7 || usage.CacheCreationTokens != 8 || usage.TotalTokens != 141 {
		t.Fatalf("boundary tokens = input %d output %d cached %d read %d creation %d total %d",
			usage.InputTokens, usage.OutputTokens, usage.CachedTokens, usage.CacheReadTokens,
			usage.CacheCreationTokens, usage.TotalTokens)
	}
}

func TestNormalizeUsagePreservesExplicitTotalTokens(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantTotal int
	}{
		{
			name:      "primary explicit zero",
			raw:       `{"tokens":{"input_tokens":100,"output_tokens":20,"cache_creation_tokens":4,"reasoning_tokens":3,"total_tokens":0}}`,
			wantTotal: 0,
		},
		{
			name:      "breakdown explicit zero with primary missing",
			raw:       `{"tokens":{"input_tokens":7,"output_tokens":3},"token_breakdown":{"total_tokens":0}}`,
			wantTotal: 0,
		},
		{
			name:      "breakdown explicit zero with primary invalid",
			raw:       `{"tokens":{"input_tokens":7,"output_tokens":3,"total_tokens":true},"token_breakdown":{"total_tokens":0}}`,
			wantTotal: 0,
		},
		{
			name:      "legacy explicit zero",
			raw:       `{"input_tokens":9,"output_tokens":2,"total":0}`,
			wantTotal: 0,
		},
		{
			name:      "negative total clamps to zero without fallback",
			raw:       `{"tokens":{"input_tokens":5,"output_tokens":6,"total_tokens":-1}}`,
			wantTotal: 0,
		},
		{
			name:      "missing total falls back to input and output",
			raw:       `{"tokens":{"input_tokens":7,"output_tokens":3}}`,
			wantTotal: 10,
		},
		{
			name:      "nested breakdown component totals are not aggregate aliases",
			raw:       `{"token_breakdown":{"input":{"total_tokens":7},"output":{"total_tokens":3}}}`,
			wantTotal: 10,
		},
		{
			name:      "invalid explicit totals do not expose nested breakdown totals",
			raw:       `{"tokens":{"input_tokens":7,"output_tokens":3,"total_tokens":true},"token_breakdown":{"total_tokens":"invalid","input":{"total_tokens":70},"output":{"total_tokens":30}}}`,
			wantTotal: 10,
		},
		{
			name:      "genuine nested legacy aggregate remains compatible",
			raw:       `{"usage":{"total_tokens":12},"token_breakdown":{"input":{"total_tokens":7},"output":{"total_tokens":3}}}`,
			wantTotal: 12,
		},
		{
			name:      "invalid total falls back to input and output",
			raw:       `{"tokens":{"input_tokens":7,"output_tokens":3,"total_tokens":"invalid"}}`,
			wantTotal: 10,
		},
		{
			name:      "missing total keeps cache and reasoning fallback",
			raw:       `{"tokens":{"cached_tokens":4,"reasoning_tokens":3}}`,
			wantTotal: 7,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, err := normalizeUsage([]byte(test.raw))
			if err != nil {
				t.Fatalf("normalizeUsage: %v", err)
			}
			if usage.TotalTokens != test.wantTotal {
				t.Fatalf("total tokens = %d, want %d", usage.TotalTokens, test.wantTotal)
			}
		})
	}
}

func TestUsageTokenIntRejectsNonFiniteAndContainerValues(t *testing.T) {
	for name, value := range map[string]any{
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
		"not a number":      math.NaN(),
		"map":               map[string]any{"total_tokens": float64(1)},
		"array":             []any{float64(1)},
		"boolean":           true,
	} {
		t.Run(name, func(t *testing.T) {
			if token, ok := usageTokenInt(value); ok {
				t.Fatalf("usageTokenInt(%v) = %d, true; want invalid", value, token)
			}
		})
	}
}

func TestUsageTokenIntAcceptsOnlySafeIntegralValues(t *testing.T) {
	maxIntString := "2147483647"
	beyondMaxIntString := "2147483648"
	if strconv.IntSize == 64 {
		maxIntString = "9223372036854775807"
		beyondMaxIntString = "9223372036854775808"
	}
	floatUpperBound := math.Ldexp(1, strconv.IntSize-1)
	largestIntegralFloat := floatUpperBound - 1
	if strconv.IntSize == 64 {
		largestIntegralFloat = math.Nextafter(floatUpperBound, 0)
	}

	tests := []struct {
		name      string
		value     any
		wantToken int
		wantOK    bool
	}{
		{name: "integer float", value: float64(42), wantToken: 42, wantOK: true},
		{name: "largest in-range integral float", value: largestIntegralFloat, wantToken: int(largestIntegralFloat), wantOK: true},
		{name: "fractional float", value: 42.5, wantOK: false},
		{name: "platform float upper bound", value: floatUpperBound, wantOK: false},
		{name: "very large float", value: math.MaxFloat64, wantOK: false},
		{name: "platform max integer string", value: maxIntString, wantToken: int(^uint(0) >> 1), wantOK: true},
		{name: "beyond platform max integer string", value: beyondMaxIntString, wantOK: false},
		{name: "very large integer string", value: "1e100", wantOK: false},
		{name: "integral exponent string", value: "1e3", wantToken: 1000, wantOK: true},
		{name: "fractional exponent string", value: "1e-1", wantOK: false},
		{name: "fractional decimal string", value: "42.5", wantOK: false},
		{name: "negative fractional float clamps", value: -42.5, wantToken: 0, wantOK: true},
		{name: "negative fractional string clamps", value: "-42.5", wantToken: 0, wantOK: true},
		{name: "high precision JSON fraction", value: json.Number("1.0000000000000000001"), wantOK: false},
		{name: "large high precision JSON fraction", value: json.Number("9007199254740991.9"), wantOK: false},
		{name: "fractional JSON exponent", value: json.Number("1.5e0"), wantOK: false},
		{name: "integral JSON exponent", value: json.Number("1e3"), wantToken: 1000, wantOK: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotToken, gotOK := usageTokenInt(test.value)
			if gotToken != test.wantToken || gotOK != test.wantOK {
				t.Fatalf("usageTokenInt(%v) = %d, %v; want %d, %v", test.value, gotToken, gotOK, test.wantToken, test.wantOK)
			}
		})
	}
}

func TestNormalizeUsageFallsThroughUnsafeTokenCandidates(t *testing.T) {
	beyondMaxIntString := "2147483648"
	if strconv.IntSize == 64 {
		beyondMaxIntString = "9223372036854775808"
	}

	tests := []struct {
		name      string
		raw       string
		wantInput int
	}{
		{
			name:      "overflowing primary falls through to breakdown",
			raw:       `{"tokens":{"input_tokens":"` + beyondMaxIntString + `"},"token_breakdown":{"input":{"total_tokens":42}}}`,
			wantInput: 42,
		},
		{
			name:      "fractional primary and overflowing breakdown fall through to legacy",
			raw:       `{"tokens":{"input_tokens":"1.5"},"token_breakdown":{"input":{"total_tokens":"` + beyondMaxIntString + `"}},"input":17}`,
			wantInput: 17,
		},
		{
			name:      "high precision JSON fraction falls through",
			raw:       `{"tokens":{"input_tokens":1.0000000000000000001},"token_breakdown":{"input":{"total_tokens":42}}}`,
			wantInput: 42,
		},
		{
			name:      "large high precision JSON fraction falls through",
			raw:       `{"tokens":{"input_tokens":9007199254740991.9},"token_breakdown":{"input":{"total_tokens":43}}}`,
			wantInput: 43,
		},
		{
			name:      "fractional JSON exponent falls through",
			raw:       `{"tokens":{"input_tokens":1.5e0},"token_breakdown":{"input":{"total_tokens":44}}}`,
			wantInput: 44,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, err := normalizeUsage([]byte(test.raw))
			if err != nil {
				t.Fatalf("normalizeUsage: %v", err)
			}
			if usage.InputTokens != test.wantInput {
				t.Fatalf("input tokens = %d, want %d", usage.InputTokens, test.wantInput)
			}
		})
	}
}

func TestNormalizeUsagePreservesExactLargeJSONInteger(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("exact integer above 2^53 requires a 64-bit int")
	}
	const want = 9007199254740993
	usage, err := normalizeUsage([]byte(`{"tokens":{"input_tokens":9007199254740993}}`))
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}
	if usage.InputTokens != want {
		t.Fatalf("input tokens = %d, want exact %d", usage.InputTokens, want)
	}
	if !strings.Contains(usage.RawJSON, `"input_tokens":9007199254740993`) {
		t.Fatalf("raw JSON lost exact integer text: %s", usage.RawJSON)
	}
}

func TestNormalizeUsageKeepsCanonicalDedupeAndMalformedFallback(t *testing.T) {
	first, err := normalizeUsage([]byte(`{"model":"gpt-test","input_tokens":1}`))
	if err != nil {
		t.Fatalf("normalize first canonical payload: %v", err)
	}
	second, err := normalizeUsage([]byte(" { \n \t\"input_tokens\" : 1, \"model\" : \"gpt-test\" } \n"))
	if err != nil {
		t.Fatalf("normalize second canonical payload: %v", err)
	}
	if first.RawJSON != second.RawJSON || first.DedupeKey != second.DedupeKey {
		t.Fatalf("canonical payload mismatch raw=%q/%q dedupe=%q/%q", first.RawJSON, second.RawJSON, first.DedupeKey, second.DedupeKey)
	}

	malformed := []byte(`{"input_tokens":1} trailing`)
	fallback, err := normalizeUsage(malformed)
	if err != nil {
		t.Fatalf("normalize malformed payload: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(fallback.RawJSON), &decoded); err != nil {
		t.Fatalf("decode malformed fallback: %v", err)
	}
	if decoded["message"] != string(malformed) || fallback.InputTokens != 0 {
		t.Fatalf("malformed fallback = raw %q input %d", fallback.RawJSON, fallback.InputTokens)
	}
}

func TestNormalizeUsagePreservesNumericMetadataWithUseNumber(t *testing.T) {
	usage, err := normalizeUsage([]byte(`{
		"api_key":7,
		"provider":123,
		"model":456,
		"service_tier":1,
		"endpoint":2,
		"source":3,
		"request_id":4,
		"auth_type":5,
		"auth_index":6,
		"reasoning_effort":7.5,
		"latency_ms":12.5,
		"ttft_ms":8.25,
		"timestamp":1700000000000,
		"failed":false
	}`))
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}

	assertString := func(name string, got *string, want string) {
		t.Helper()
		if got == nil || *got != want {
			t.Fatalf("%s = %#v, want %q", name, got, want)
		}
	}
	if usage.APIKeyHash != hashAPIKey("7") {
		t.Fatalf("api key hash = %q, want numeric API key preserved", usage.APIKeyHash)
	}
	assertString("provider", usage.Provider, "123")
	assertString("model", usage.Model, "456")
	assertString("service tier", usage.ServiceTier, "1")
	assertString("endpoint", usage.Endpoint, "2")
	assertString("source", usage.Source, "3")
	assertString("request ID", usage.RequestID, "4")
	assertString("auth", usage.Auth, "5")
	assertString("auth index", usage.AuthIndex, "6")
	assertString("reasoning effort", usage.ReasoningEffort, "7.5")
	if usage.LatencyMS == nil || *usage.LatencyMS != 12.5 || usage.TTFTMS == nil || *usage.TTFTMS != 8.25 {
		t.Fatalf("latency/ttft = %#v/%#v, want 12.5/8.25", usage.LatencyMS, usage.TTFTMS)
	}
	wantTimestamp := time.Unix(1700000000, 0).In(appTimeLocation)
	if !usage.Timestamp.Equal(wantTimestamp) {
		t.Fatalf("timestamp = %v, want %v", usage.Timestamp, wantTimestamp)
	}
	if usage.Failed {
		t.Fatal("failed = true, want false")
	}
}

func TestFilteredUsageAnalyticsRecordsUsesEffectiveAuthWithoutRawPayload(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-analytics-auth","provider":"codex","model":"gpt-test","source":"analytics-source","auth_type":"apikey","auth_index":"account.json","request_id":"analytics-auth","input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if _, err := app.db.Exec(`UPDATE usage_records SET auth = 'bearer' WHERE id = ?`, record.ID); err != nil {
		t.Fatalf("seed conflicting stored auth: %v", err)
	}

	records, err := app.filteredUsageAnalyticsRecords(context.Background(), UsageFilters{}, "")
	if err != nil {
		t.Fatalf("filteredUsageAnalyticsRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("analytics records = %d, want 1", len(records))
	}
	got := records[0]
	if got.Auth == nil || *got.Auth != "apikey" {
		t.Fatalf("analytics auth = %#v, want raw auth_type apikey", got.Auth)
	}
	if effective := usageRecordAuth(got); effective == nil || *effective != "apikey" {
		t.Fatalf("effective analytics auth = %#v, want apikey", effective)
	}
	if !got.authResolved || got.resolvedAuth == nil || *got.resolvedAuth != "apikey" {
		t.Fatalf("analytics auth cache = resolved %v value %#v, want cached apikey", got.authResolved, got.resolvedAuth)
	}
	if got.RawJSON != "" || got.RequestID != nil || got.SourceAccount != nil || got.LatencyMS != nil || got.DedupeKey != "" {
		t.Fatalf("analytics-only omitted fields were populated: %#v", got)
	}
	if got.Source == nil || *got.Source != "analytics-source" || got.AuthIndex == nil || *got.AuthIndex != "account.json" {
		t.Fatalf("analytics matching fields = source %#v auth_index %#v", got.Source, got.AuthIndex)
	}
}

func TestFilteredUsageAnalyticsRecordsFallsBackToStoredAuth(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	tests := []struct {
		requestID  string
		rawJSON    string
		storedAuth string
		wantAuth   string
	}{
		{requestID: "analytics-malformed-auth", rawJSON: `{`, storedAuth: "oauth", wantAuth: "oauth"},
		{requestID: "analytics-missing-auth", rawJSON: `{}`, storedAuth: "oauth", wantAuth: "oauth"},
		{requestID: "analytics-ascii-whitespace-auth", rawJSON: `{"auth_type":"\t\n\r"}`, storedAuth: "oauth", wantAuth: "oauth"},
		{requestID: "analytics-unicode-whitespace-auth", rawJSON: `{"auth_type":"\u00a0"}`, storedAuth: "apikey", wantAuth: "apikey"},
		{requestID: "analytics-padded-auth", rawJSON: `{"auth_type":"\toauth\n"}`, storedAuth: "apikey", wantAuth: "oauth"},
	}
	for _, test := range tests {
		raw := `{"api_key":"sk-analytics-fallback","provider":"codex","model":"` + test.requestID + `","request_id":"` + test.requestID + `","input_tokens":1}`
		record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
		if err != nil || !created {
			t.Fatalf("saveUsageMessage %s created=%v err=%v", test.requestID, created, err)
		}
		if _, err := app.db.Exec(`UPDATE usage_records SET auth = ?, raw_json = ? WHERE id = ?`, test.storedAuth, test.rawJSON, record.ID); err != nil {
			t.Fatalf("seed %s legacy auth: %v", test.requestID, err)
		}
	}

	records, err := app.filteredUsageAnalyticsRecords(context.Background(), UsageFilters{}, "timestamp ASC")
	if err != nil {
		t.Fatalf("filteredUsageAnalyticsRecords failed: %v", err)
	}
	if len(records) != len(tests) {
		t.Fatalf("analytics records = %d, want %d", len(records), len(tests))
	}
	recordsByModel := map[string]UsageRecord{}
	for _, record := range records {
		if record.Model != nil {
			recordsByModel[*record.Model] = record
		}
	}
	for _, test := range tests {
		record := recordsByModel[test.requestID]
		if record.Auth == nil || *record.Auth != test.wantAuth {
			t.Fatalf("analytics fallback auth for %s = %#v, want %q", test.requestID, record.Auth, test.wantAuth)
		}
		if record.RawJSON != "" {
			t.Fatalf("analytics raw_json = %q, want omitted", record.RawJSON)
		}
	}
}

func TestFilteredUsageRecordsDoesNotResolveAuthBeforeSourceFiltering(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-source-filter-auth","provider":"codex","model":"gpt-test","source":"selected-source","auth_type":"apikey","request_id":"source-filter-auth","input_tokens":1}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if _, err := app.db.Exec(`UPDATE usage_records SET auth = 'bearer' WHERE id = ?`, record.ID); err != nil {
		t.Fatalf("seed conflicting stored auth: %v", err)
	}

	source := "selected-source"
	sourceKey := usageSourceKey(&source)
	records, err := app.filteredUsageRecords(context.Background(), UsageFilters{SourceKey: sourceKey}, "")
	if err != nil {
		t.Fatalf("filteredUsageRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("filtered records = %d, want 1", len(records))
	}
	got := records[0]
	if got.authResolved || got.resolvedAuth != nil {
		t.Fatalf("full record auth was resolved before a consumer needed it: %#v", got.resolvedAuth)
	}
	if got.Auth == nil || *got.Auth != "bearer" {
		t.Fatalf("stored auth = %#v, want bearer before lazy resolution", got.Auth)
	}
	if effective := usageRecordAuth(got); effective == nil || *effective != "apikey" {
		t.Fatalf("effective auth = %#v, want raw auth_type apikey", effective)
	}
}

func TestSaveUsageMessageStoresReasoningEffortAndTTFT(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-ttft","provider":"openai","model":"gpt-5.5","request_id":"usage-ttft","reasoning_effort":"xhigh","ttft_ms":710,"input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.ReasoningEffort == nil || *record.ReasoningEffort != "xhigh" {
		t.Fatalf("record reasoning_effort = %#v, want xhigh", record.ReasoningEffort)
	}
	if record.TTFTMS == nil || *record.TTFTMS != 710 {
		t.Fatalf("record ttft_ms = %#v, want 710", record.TTFTMS)
	}

	var reasoningEffort sql.NullString
	var ttftMS sql.NullFloat64
	if err := app.db.QueryRow(`SELECT reasoning_effort, ttft_ms FROM usage_records WHERE id = ?`, record.ID).Scan(&reasoningEffort, &ttftMS); err != nil {
		t.Fatal(err)
	}
	if !reasoningEffort.Valid || reasoningEffort.String != "xhigh" || !ttftMS.Valid || ttftMS.Float64 != 710 {
		t.Fatalf("stored reasoning/ttft = %#v/%#v, want xhigh/710", reasoningEffort, ttftMS)
	}
}

func TestSaveUsageMessageIgnoresZeroTTFT(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-ttft-zero","provider":"openai","model":"gpt-5.5","request_id":"usage-ttft-zero","ttft_ms":0,"input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.TTFTMS != nil {
		t.Fatalf("record ttft_ms = %#v, want nil", record.TTFTMS)
	}

	var ttftMS sql.NullFloat64
	if err := app.db.QueryRow(`SELECT ttft_ms FROM usage_records WHERE id = ?`, record.ID).Scan(&ttftMS); err != nil {
		t.Fatal(err)
	}
	if ttftMS.Valid {
		t.Fatalf("stored ttft_ms = %v, want NULL", ttftMS.Float64)
	}
}

func TestSaveUsageMessageStoresServiceTier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-tier","provider":"codex","model":"gpt-5.5","request_id":"usage-tier","service_tier":"priority","input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.ServiceTier == nil || *record.ServiceTier != "priority" {
		t.Fatalf("record service_tier = %#v, want priority", record.ServiceTier)
	}

	var serviceTier sql.NullString
	if err := app.db.QueryRow(`SELECT service_tier FROM usage_records WHERE id = ?`, record.ID).Scan(&serviceTier); err != nil {
		t.Fatal(err)
	}
	if !serviceTier.Valid || serviceTier.String != "priority" {
		t.Fatalf("stored service_tier = %#v, want priority", serviceTier)
	}

	withoutTier, created, err := app.saveUsageMessage(context.Background(), []byte(`{"api_key":"sk-usage-tier","provider":"codex","model":"gpt-5.5","request_id":"usage-tier-unreported","input_tokens":1}`), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage without tier created=%v err=%v", created, err)
	}
	if withoutTier.ServiceTier != nil {
		t.Fatalf("record without service_tier = %#v, want nil", withoutTier.ServiceTier)
	}
}

func TestSaveUsageMessageExposesCodexCacheCostBreakdown(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-cache","provider":"codex","model":"gpt-test","request_id":"usage-cache","tokens":{"input_tokens":100,"output_tokens":20,"cached_tokens":30,"cache_read_tokens":0,"cache_creation_tokens":40,"reasoning_tokens":5,"total_tokens":120}}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.InputTokens != 100 || record.OutputTokens != 20 || record.CachedTokens != 30 || record.CacheReadTokens != 0 || record.CacheCreationTokens != 40 {
		t.Fatalf("stored tokens = input %d output %d cached %d read %d creation %d, want 100/20/30/0/40", record.InputTokens, record.OutputTokens, record.CachedTokens, record.CacheReadTokens, record.CacheCreationTokens)
	}

	prices := map[[2]string]ModelPrice{
		priceKey("openai", "gpt-test"): {
			Provider:               "openai",
			Model:                  "gpt-test",
			InputUSDPerMillion:     10,
			OutputUSDPerMillion:    20,
			CacheReadUSDPerMillion: 1,
		},
	}
	item := listItemFromRecord(record, map[string]userInfo{}, prices, usageRedactionOptions{})
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal usage item: %v", err)
	}
	var response struct {
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		Unpriced         bool    `json:"unpriced"`
		CostBreakdown    struct {
			BillingUnit         string `json:"billing_unit"`
			NormalInputTokens   int    `json:"normal_input_tokens"`
			CacheReadTokens     int    `json:"cache_read_tokens"`
			CacheCreationTokens int    `json:"cache_creation_tokens"`
			OutputTokens        int    `json:"output_tokens"`
			Items               []struct {
				Kind          string  `json:"kind"`
				Tokens        int     `json:"tokens"`
				USDPerMillion float64 `json:"usd_per_million"`
				SubtotalUSD   float64 `json:"subtotal_usd"`
			} `json:"items"`
			TotalUSD float64 `json:"total_usd"`
			Unpriced bool    `json:"unpriced"`
		} `json:"cost_breakdown"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("unmarshal usage item: %v", err)
	}
	if response.Unpriced || response.CostBreakdown.Unpriced || response.CostBreakdown.BillingUnit != modelBillingUnitToken {
		t.Fatalf("pricing state = top unpriced %v breakdown %#v", response.Unpriced, response.CostBreakdown)
	}
	if response.CostBreakdown.NormalInputTokens != 30 || response.CostBreakdown.CacheReadTokens != 30 || response.CostBreakdown.CacheCreationTokens != 40 || response.CostBreakdown.OutputTokens != 20 {
		t.Fatalf("API token breakdown = input %d read %d creation %d output %d, want 30/30/40/20", response.CostBreakdown.NormalInputTokens, response.CostBreakdown.CacheReadTokens, response.CostBreakdown.CacheCreationTokens, response.CostBreakdown.OutputTokens)
	}
	if len(response.CostBreakdown.Items) != 3 || response.CostBreakdown.Items[0].Kind != usageCostKindInput || response.CostBreakdown.Items[0].Tokens != 70 || response.CostBreakdown.TotalUSD != 0.00113 || response.EstimatedCostUSD != response.CostBreakdown.TotalUSD {
		t.Fatalf("API cost breakdown = items %#v total %v estimated %v, want three items with 70 input tokens and total 0.00113", response.CostBreakdown.Items, response.CostBreakdown.TotalUSD, response.EstimatedCostUSD)
	}
}

func TestUsageItemExposesLongContextSelectionAndSelectedPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-long-context","provider":"openai","model":"gpt-long-usage","request_id":"usage-long-context","input_tokens":300000,"output_tokens":100000}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	prices := map[[2]string]ModelPrice{
		priceKey("openai", "gpt-long-usage"): {
			Provider:            "openai",
			Model:               "gpt-long-usage",
			InputUSDPerMillion:  1,
			OutputUSDPerMillion: 2,
			LongContext: &ModelPriceLongContext{
				ThresholdInputTokens:       200000,
				InputUSDPerMillion:         3,
				OutputUSDPerMillion:        6,
				CacheReadUSDPerMillion:     0.3,
				CacheCreationUSDPerMillion: 0,
			},
		},
	}
	item := listItemFromRecord(record, map[string]userInfo{}, prices, usageRedactionOptions{})
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal long-context usage item: %v", err)
	}
	var response struct {
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		CostBreakdown    struct {
			ContextInputTokens         int    `json:"context_input_tokens"`
			LongContextThresholdTokens *int64 `json:"long_context_threshold_tokens"`
			LongContextApplied         bool   `json:"long_context_applied"`
			Unpriced                   bool   `json:"unpriced"`
			Items                      []struct {
				Kind          string  `json:"kind"`
				USDPerMillion float64 `json:"usd_per_million"`
			} `json:"items"`
		} `json:"cost_breakdown"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("unmarshal long-context usage item: %v", err)
	}
	if response.CostBreakdown.ContextInputTokens != 300000 || response.CostBreakdown.LongContextThresholdTokens == nil ||
		*response.CostBreakdown.LongContextThresholdTokens != 200000 || !response.CostBreakdown.LongContextApplied {
		t.Fatalf("long-context API selection = %#v", response.CostBreakdown)
	}
	if len(response.CostBreakdown.Items) != 2 || response.CostBreakdown.Items[0].Kind != usageCostKindInput ||
		response.CostBreakdown.Items[0].USDPerMillion != 3 || response.CostBreakdown.Items[1].USDPerMillion != 6 ||
		response.EstimatedCostUSD != 1.5 {
		t.Fatalf("long-context API prices = %#v total=%v, want 3/6 and 1.5", response.CostBreakdown.Items, response.EstimatedCostUSD)
	}

	invalidPrices := map[[2]string]ModelPrice{
		priceKey("openai", "gpt-long-usage"): {
			Provider:            "openai",
			Model:               "gpt-long-usage",
			InputUSDPerMillion:  1,
			OutputUSDPerMillion: 2,
			LongContext: &ModelPriceLongContext{
				ThresholdInputTokens: 200000,
				InputUSDPerMillion:   3,
			},
			longContextInvalid: true,
		},
	}
	encoded, err = json.Marshal(listItemFromRecord(record, map[string]userInfo{}, invalidPrices, usageRedactionOptions{}))
	if err != nil {
		t.Fatalf("marshal invalid long-context usage item: %v", err)
	}
	response = struct {
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		CostBreakdown    struct {
			ContextInputTokens         int    `json:"context_input_tokens"`
			LongContextThresholdTokens *int64 `json:"long_context_threshold_tokens"`
			LongContextApplied         bool   `json:"long_context_applied"`
			Unpriced                   bool   `json:"unpriced"`
			Items                      []struct {
				Kind          string  `json:"kind"`
				USDPerMillion float64 `json:"usd_per_million"`
			} `json:"items"`
		} `json:"cost_breakdown"`
	}{}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("unmarshal invalid long-context usage item: %v", err)
	}
	if !response.CostBreakdown.Unpriced || response.CostBreakdown.LongContextApplied ||
		response.EstimatedCostUSD != 0 || len(response.CostBreakdown.Items) != 0 {
		t.Fatalf("invalid long-context API selection = %#v, want unpriced and not applied", response)
	}
}
