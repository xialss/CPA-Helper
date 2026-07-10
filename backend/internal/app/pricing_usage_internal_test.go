package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			cache_read_usd_per_million, cache_creation_usd_per_million, source, updated_at
		) VALUES
			('openai', 'old-litellm-model', 1, 1, 1, 1, 'litellm', ?),
			('openai', 'manual-model', 9, 9, 9, 9, 'manual', ?)
	`, now, now); err != nil {
		t.Fatalf("seed prices: %v", err)
	}

	rawData := map[string]any{
		"gpt-new-model": map[string]any{
			"litellm_provider":            "openai",
			"input_cost_per_token":        0.000001,
			"output_cost_per_token":       0.000002,
			"cache_read_input_token_cost": 0.0000001,
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
	if result["imported"].(int) != 2 || result["skipped_manual"].(int) != 1 {
		t.Fatalf("sync result = %#v, want imported 2 skipped_manual 1", result)
	}

	var oldCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM model_prices WHERE source = 'litellm' AND model = 'old-litellm-model'`).Scan(&oldCount); err != nil {
		t.Fatalf("query old litellm count: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old litellm rows = %d, want 0", oldCount)
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
			cache_read_usd_per_million, cache_creation_usd_per_million, source,
			source_model, auto_synced, last_synced_at, updated_at
		) VALUES ('openai', 'gpt-priced', 1, 2, 0.1, 0, 'litellm', 'gpt-priced', 1, ?, ?)
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
	if len(catalog.Models[1].Sources) != 1 || catalog.Models[1].Sources[0].Description != "Admin Key" || catalog.Models[1].Sources[0].UserLabel != "管理员" {
		t.Fatalf("sources = %#v, want key description and user label", catalog.Models[1].Sources)
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
