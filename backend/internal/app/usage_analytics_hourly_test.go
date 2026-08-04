package app

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestUsageAnalyticsHourlyPreservesPersistedSourceKey(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-2 * time.Hour)
	end := start.Add(time.Hour)
	source := "hourly-source-key@example.com"
	if _, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, source, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-hourly', '/v1/responses', ?, 'api_key', 0,
			10, 2, 0, 0, 0, 0, 12, 'hourly-source-key', '{}')
	`, dbTime(start), dbTime(start.Add(5*time.Minute)), source); err != nil {
		t.Fatalf("insert usage record: %v", err)
	}
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		t.Fatal("usageSourceKey returned nil")
	}

	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	filters := UsageFilters{Start: &start, End: &end, SourceKey: sourceKey}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, collector, ""); err != nil {
		t.Fatalf("collectUsageAnalytics: %v", err)
	}
	if got := collector.summaryResponse()["total_records"]; got != 1 {
		t.Fatalf("hourly source-key summary records = %#v, want 1", got)
	}

	var factKey, hourlyKey string
	if err := app.db.QueryRow(`SELECT source_key FROM usage_analytics_facts`).Scan(&factKey); err != nil {
		t.Fatalf("read fact source key: %v", err)
	}
	if err := app.db.QueryRow(`SELECT source_key FROM usage_analytics_hourly`).Scan(&hourlyKey); err != nil {
		t.Fatalf("read hourly source key: %v", err)
	}
	if factKey != *sourceKey || hourlyKey != *sourceKey {
		t.Fatalf("source keys = fact %q / hourly %q, want %q", factKey, hourlyKey, *sourceKey)
	}
}

func TestUsageAnalyticsHourlyRebuildsAfterPricingVersionChanges(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-2 * time.Hour)
	end := start.Add(time.Hour)
	provider := "hourly-pricing-provider"
	model := "hourly-pricing-model"
	if _, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, auth, auth_index, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, ?, ?, '/v1/responses', 'api_key', 'hourly-pricing-provider', 0,
			1000000, 0, 0, 0, 0, 0, 1000000, 'hourly-pricing-version', '{}')
	`, dbTime(start), dbTime(start.Add(5*time.Minute)), provider, model); err != nil {
		t.Fatalf("insert usage record: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million,
			cache_creation_usd_per_million, source, updated_at
		) VALUES (?, ?, 'channel', 'apikey', 'openai_compatibility', ?, 1, 0, 0, 0, 'manual', ?)
	`, provider, model, provider, dbTime(start)); err != nil {
		t.Fatalf("insert model price: %v", err)
	}

	identity := modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)
	priceFor := func(inputPrice float64) modelPriceBillingIndex {
		brand := string(aiProviderBrandOpenAICompatibility)
		authType := modelPriceChannelAuthTypeAPIKey
		channelKey := provider
		return modelPriceBillingIndex{
			Prices: modelPriceIndex{
				channelModelPriceKey(authType, brand, channelKey, model): {
					Provider:                   provider,
					Model:                      model,
					PriceScope:                 modelPriceScopeChannel,
					ChannelAuthType:            &authType,
					ChannelBrand:               &brand,
					ChannelKey:                 &channelKey,
					InputUSDPerMillion:         inputPrice,
					OutputUSDPerMillion:        0,
					CacheReadUSDPerMillion:     0,
					CacheCreationUSDPerMillion: 0,
				},
			},
			MatchContext: modelPriceMatchContext{
				SelectorsRequired:  true,
				SelectorsAvailable: true,
				Selectors:          modelPriceChannelSelectorIndex{identity: 1},
			},
		}
	}
	filters := UsageFilters{Start: &start, End: &end}
	collectCost := func(pricing modelPriceBillingIndex) float64 {
		t.Helper()
		collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
		if err := app.collectUsageAnalytics(context.Background(), filters, pricing, collector, ""); err != nil {
			t.Fatalf("collectUsageAnalytics: %v", err)
		}
		return collector.summaryResponse()["estimated_cost_usd"].(float64)
	}
	if got := collectCost(priceFor(1)); got != 1 {
		t.Fatalf("initial hourly cost = %v, want 1", got)
	}
	// Take a pricing snapshot before the edit. Rebuilding with that stale
	// snapshot used to stamp the new state version onto the old cost.
	stalePricing, err := app.billingPriceIndexWithoutSelectors(context.Background())
	if err != nil {
		t.Fatalf("capture stale billing price index: %v", err)
	}
	stalePricing.MatchContext = priceFor(1).MatchContext
	if _, err := app.db.Exec(`UPDATE model_prices SET input_usd_per_million = 2 WHERE provider = ? AND model = ?`, provider, model); err != nil {
		t.Fatalf("update model price: %v", err)
	}
	if err := app.rebuildUsageAnalyticsHourly(context.Background(), stalePricing); err != nil {
		t.Fatalf("rebuild hourly with stale pricing snapshot: %v", err)
	}
	var rebuiltCost float64
	if err := app.db.QueryRow(`SELECT estimated_cost_usd FROM usage_analytics_hourly`).Scan(&rebuiltCost); err != nil {
		t.Fatalf("read rebuilt hourly cost: %v", err)
	}
	if rebuiltCost != 2 {
		t.Fatalf("stale-snapshot rebuild cost = %v, want current price cost 2", rebuiltCost)
	}
	if got := collectCost(priceFor(2)); got != 2 {
		t.Fatalf("rebuilt hourly cost = %v, want 2", got)
	}
	var stateVersion, hourlyVersion int64
	if err := app.db.QueryRow(`SELECT pricing_version FROM usage_analytics_state WHERE id = 1`).Scan(&stateVersion); err != nil {
		t.Fatalf("read pricing state version: %v", err)
	}
	if err := app.db.QueryRow(`SELECT DISTINCT pricing_version FROM usage_analytics_hourly`).Scan(&hourlyVersion); err != nil {
		t.Fatalf("read hourly pricing version: %v", err)
	}
	if hourlyVersion != stateVersion {
		t.Fatalf("hourly/state pricing versions = %d/%d, want match", hourlyVersion, stateVersion)
	}
}

func TestUsageAnalyticsHourlyRebuildsFactEdgesWithCurrentPricing(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	hour := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-4 * time.Hour)
	start := hour.Add(10 * time.Minute)
	end := hour.Add(2*time.Hour + 50*time.Minute)
	fullStart := usageAnalyticsCeilHour(start)
	fullEnd := usageAnalyticsFloorHour(end)
	if !fullStart.Before(fullEnd) {
		t.Fatalf("hybrid range has no complete hour: %s - %s", start, end)
	}

	provider := "hourly-edge-pricing-provider"
	model := "hourly-edge-pricing-model"
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million,
			cache_creation_usd_per_million, source, updated_at
		) VALUES (?, ?, 'channel', 'apikey', 'openai_compatibility', ?, 1, 0, 0, 0, 'manual', ?)
	`, provider, model, provider, dbTime(hour)); err != nil {
		t.Fatalf("insert model price: %v", err)
	}
	for index, timestamp := range []time.Time{
		hour.Add(20 * time.Minute),
		hour.Add(time.Hour + 20*time.Minute),
		hour.Add(2*time.Hour + 20*time.Minute),
	} {
		if _, err := app.db.Exec(`
			INSERT INTO usage_records (
				created_at, timestamp, provider, model, endpoint, auth, auth_index, failed,
				input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
				reasoning_tokens, total_tokens, dedupe_key, raw_json
			) VALUES (?, ?, ?, ?, '/v1/responses', 'api_key', ?, 0,
				1000000, 0, 0, 0, 0, 0, 1000000, ?, '{}')
		`, dbTime(timestamp), dbTime(timestamp), provider, model, provider, "hourly-edge-pricing-"+strconv.Itoa(index)); err != nil {
			t.Fatalf("insert usage record %d: %v", index, err)
		}
	}

	authType := modelPriceChannelAuthTypeAPIKey
	brand := string(aiProviderBrandOpenAICompatibility)
	channelKey := provider
	identity := modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)
	pricingFor := func(inputPrice float64) modelPriceBillingIndex {
		return modelPriceBillingIndex{
			Prices: modelPriceIndex{
				channelModelPriceKey(authType, brand, channelKey, model): {
					Provider:                   provider,
					Model:                      model,
					PriceScope:                 modelPriceScopeChannel,
					ChannelAuthType:            &authType,
					ChannelBrand:               &brand,
					ChannelKey:                 &channelKey,
					InputUSDPerMillion:         inputPrice,
					OutputUSDPerMillion:        0,
					CacheReadUSDPerMillion:     0,
					CacheCreationUSDPerMillion: 0,
				},
			},
			MatchContext: modelPriceMatchContext{
				SelectorsRequired:  true,
				SelectorsAvailable: true,
				Selectors:          modelPriceChannelSelectorIndex{identity: 1},
			},
		}
	}
	initialPricing := pricingFor(1)
	filters := UsageFilters{Start: &start, End: &end}
	baseline := newUsageAnalyticsCollector(filters, initialPricing.Prices, initialPricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, initialPricing, baseline, "timestamp ASC"); err != nil {
		t.Fatalf("collect hourly baseline: %v", err)
	}
	if got := baseline.summaryResponse()["estimated_cost_usd"]; got != float64(3) {
		t.Fatalf("hourly baseline cost = %#v, want 3", got)
	}
	baseState, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load hourly baseline state: %v", err)
	}
	if baseState.hourlyFactsVersion != baseState.factsVersion || baseState.hourlyNeedsRebuild {
		t.Fatalf("hourly baseline state = %#v, want current aggregate", baseState)
	}

	stalePricing, err := app.billingPriceIndexWithoutSelectors(context.Background())
	if err != nil {
		t.Fatalf("capture stale billing price index: %v", err)
	}
	stalePricing.MatchContext = initialPricing.MatchContext
	collector := newUsageAnalyticsCollector(filters, stalePricing.Prices, stalePricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	plan := usageAnalyticsHourlyReadPlanFor(filters)
	if !plan.useHourly || plan.leading == nil || plan.trailing == nil {
		t.Fatalf("hybrid plan = %#v, want hourly rows with both fact edges", plan)
	}
	var leadingRecord UsageRecord
	leadingRecords := 0
	if err := app.visitFilteredUsageAnalyticsRecords(context.Background(), *plan.leading, "timestamp ASC", func(record UsageRecord) {
		leadingRecord = record
		leadingRecords++
	}); err != nil {
		t.Fatalf("visit leading fact edge: %v", err)
	}
	if leadingRecords != 1 {
		t.Fatalf("leading fact records = %d, want 1", leadingRecords)
	}
	matchedPrice, matchStatus := findMatchingChannelPrice(stalePricing.Prices, leadingRecord, stalePricing.MatchContext)
	if matchStatus != priceMatchStatusMatched || matchedPrice == nil || matchedPrice.InputUSDPerMillion != 1 {
		t.Fatalf("stale fact-edge match = %#v / %q, want price 1", matchedPrice, matchStatus)
	}
	matchKey := usageAnalyticsPriceMatchKeyForRecord(leadingRecord)
	collector.priceMatches[matchKey] = usageAnalyticsPriceMatch{
		price:  matchedPrice,
		status: matchStatus,
		brand:  matchedModelPriceChannelBrand(matchedPrice, leadingRecord, stalePricing.MatchContext),
	}

	if _, err := app.db.Exec(`UPDATE model_prices SET input_usd_per_million = 2 WHERE provider = ? AND model = ?`, provider, model); err != nil {
		t.Fatalf("update model price: %v", err)
	}
	// Simulate a concurrent maintenance/request rebuild finishing after this
	// handler captured its old pricing index but before it starts its hybrid read.
	if err := app.rebuildUsageAnalyticsHourly(context.Background(), pricingFor(2)); err != nil {
		t.Fatalf("rebuild hourly after price update: %v", err)
	}
	if err := app.collectUsageAnalytics(context.Background(), filters, stalePricing, collector, "timestamp ASC"); err != nil {
		t.Fatalf("collect hybrid usage after price update: %v", err)
	}
	if got := collector.summaryResponse()["estimated_cost_usd"]; got != float64(6) {
		t.Fatalf("hybrid cost after price update = %#v, want all three records at price 2", got)
	}
	currentMatch, ok := collector.priceMatches[matchKey]
	if !ok || currentMatch.price == nil || currentMatch.price.InputUSDPerMillion != 2 {
		t.Fatalf("collector match cache = %#v, want rebuilt price 2", currentMatch)
	}
}

func TestUsageAnalyticsHourlyUsesFullHoursForRollingRange(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	end := time.Now().In(appTimeLocation).Truncate(time.Minute)
	if end.Minute() == 0 {
		end = end.Add(-time.Minute)
	}
	start := end.Add(-30 * 24 * time.Hour).Add(17 * time.Minute)
	if start.Minute() == 0 {
		start = start.Add(time.Minute)
	}
	fullStart := usageAnalyticsCeilHour(start)
	fullEnd := usageAnalyticsFloorHour(end)
	if !fullStart.Before(fullEnd) {
		t.Fatalf("rolling range has no complete hour: %s - %s", start, end)
	}

	provider := "rolling-hourly-provider"
	model := "rolling-hourly-model"
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million,
			cache_creation_usd_per_million, source, updated_at
		) VALUES (?, ?, 'channel', 'apikey', 'openai_compatibility', ?, 1, 0, 0, 0, 'manual', ?)
	`, provider, model, provider, dbTime(end)); err != nil {
		t.Fatalf("insert rolling hourly model price: %v", err)
	}
	for index, timestamp := range []time.Time{
		start.Add(time.Second),
		fullStart.Add(30 * time.Minute),
		fullStart.Add(2*time.Hour + 30*time.Minute),
		end.Add(-time.Second),
	} {
		if _, err := app.db.Exec(`
			INSERT INTO usage_records (
				created_at, timestamp, provider, model, endpoint, auth, auth_index, failed,
				input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
				reasoning_tokens, total_tokens, dedupe_key, raw_json
			) VALUES (?, ?, ?, ?, '/v1/responses', 'api_key', ?, 0,
				1000000, 0, 0, 0, 0, 0, 1000000, ?, '{}')
		`, dbTime(timestamp), dbTime(timestamp), provider, model, provider, "rolling-hourly-"+strconv.Itoa(index)); err != nil {
			t.Fatalf("insert usage record %d: %v", index, err)
		}
	}

	authType := modelPriceChannelAuthTypeAPIKey
	brand := string(aiProviderBrandOpenAICompatibility)
	channelKey := provider
	identity := modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)
	pricing := modelPriceBillingIndex{
		Prices: modelPriceIndex{
			channelModelPriceKey(authType, brand, channelKey, model): {
				Provider:                   provider,
				Model:                      model,
				PriceScope:                 modelPriceScopeChannel,
				ChannelAuthType:            &authType,
				ChannelBrand:               &brand,
				ChannelKey:                 &channelKey,
				InputUSDPerMillion:         1,
				OutputUSDPerMillion:        0,
				CacheReadUSDPerMillion:     0,
				CacheCreationUSDPerMillion: 0,
			},
		},
		MatchContext: modelPriceMatchContext{
			SelectorsRequired:  true,
			SelectorsAvailable: true,
			Selectors:          modelPriceChannelSelectorIndex{identity: 1},
		},
	}
	filters := UsageFilters{Start: &start, End: &end}
	plan := usageAnalyticsHourlyReadPlanFor(filters)
	if !plan.useHourly || plan.leading == nil || plan.trailing == nil || plan.hourly.Start == nil || plan.hourly.End == nil {
		t.Fatalf("rolling hourly plan = %#v, want complete hours with both fact edges", plan)
	}
	if !plan.hourly.Start.Equal(fullStart) || !plan.hourly.End.Equal(fullEnd) {
		t.Fatalf("rolling hourly range = %s - %s, want %s - %s", plan.hourly.Start, plan.hourly.End, fullStart, fullEnd)
	}

	options := usageAnalyticsCollectorOptions{
		Summary:       true,
		Trends:        true,
		Distributions: true,
		Rankings:      map[string]string{"model": usageRankingSortTokens},
	}
	hybrid := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, options)
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, hybrid, "timestamp ASC"); err != nil {
		t.Fatalf("collectUsageAnalytics hybrid: %v", err)
	}
	factsOnly := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, options)
	if err := app.visitFilteredUsageAnalyticsRecords(context.Background(), filters, "timestamp ASC", factsOnly.Add); err != nil {
		t.Fatalf("visitFilteredUsageAnalyticsRecords: %v", err)
	}
	if !reflect.DeepEqual(hybrid.summaryResponse(), factsOnly.summaryResponse()) ||
		!reflect.DeepEqual(hybrid.trendResponse(), factsOnly.trendResponse()) ||
		!reflect.DeepEqual(hybrid.rankingResponse("model"), factsOnly.rankingResponse("model")) ||
		!reflect.DeepEqual(hybrid.distributionResponse(), factsOnly.distributionResponse()) {
		t.Fatalf("hybrid hourly response differs from exact facts: summary=%#v facts=%#v", hybrid.summaryResponse(), factsOnly.summaryResponse())
	}

	result, err := app.db.Exec(`
		UPDATE usage_analytics_hourly
		SET aggregate_total_tokens = aggregate_total_tokens + 700
		WHERE hour_start = ?
	`, dbTime(fullStart))
	if err != nil {
		t.Fatalf("mark hourly contribution: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("read marked hourly contribution count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("marked hourly contribution rows = %d, want 1", rows)
	}

	sentinel := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, sentinel, ""); err != nil {
		t.Fatalf("collectUsageAnalytics sentinel: %v", err)
	}
	wantTotal := factsOnly.summaryResponse()["total_tokens"].(int) + 700
	if got := sentinel.summaryResponse()["total_tokens"]; got != wantTotal {
		t.Fatalf("rolling range total_tokens = %#v, want hourly contribution result %d", got, wantTotal)
	}
}

func TestUsageAnalyticsHourlyReadsOnlyCapturedStateGeneration(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-4 * time.Hour)
	end := start.Add(time.Hour)
	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(10*time.Minute), "hourly-generation", 10)
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	filters := UsageFilters{Start: &start, End: &end}
	baseline := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, baseline, "timestamp ASC"); err != nil {
		t.Fatalf("collect base usage: %v", err)
	}
	state, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load hourly state: %v", err)
	}
	if state.hourlyFactsVersion != state.factsVersion || state.hourlyNeedsRebuild {
		t.Fatalf("base hourly state = %#v, want complete aggregate", state)
	}

	insertGeneration := func(factsVersion, pricingVersion, selectorGeneration int64) {
		t.Helper()
		if _, err := app.db.Exec(`
			INSERT INTO usage_analytics_hourly (
				hour_start, usage_username, api_key_description, provider, model, endpoint, source_key, failed,
				channel_auth_type, channel_brand, channel_key, channel_label, channel_label_fallback,
				facts_version, pricing_version, selector_generation,
				record_count, failed_records, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
				normal_input_tokens, cache_read_tokens, cache_creation_tokens, aggregate_input_tokens,
				aggregate_total_tokens, estimated_cost_usd, unpriced_records, ttft_total_ms, ttft_count, created_at
			)
			SELECT
				hour_start, usage_username, api_key_description, provider, model, endpoint, source_key, failed,
				channel_auth_type, channel_brand, channel_key, channel_label, channel_label_fallback,
				?, ?, ?,
				record_count, failed_records, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
				normal_input_tokens, cache_read_tokens, cache_creation_tokens, aggregate_input_tokens,
				aggregate_total_tokens, estimated_cost_usd, unpriced_records, ttft_total_ms, ttft_count, created_at
			FROM usage_analytics_hourly
			WHERE facts_version = ? AND pricing_version = ? AND selector_generation = ?
			LIMIT 1
		`, factsVersion, pricingVersion, selectorGeneration,
			state.hourlyFactsVersion, state.pricingVersion, state.selectorGeneration); err != nil {
			t.Fatalf("insert mismatched hourly generation: %v", err)
		}
	}
	insertGeneration(state.hourlyFactsVersion+1, state.pricingVersion, state.selectorGeneration)
	insertGeneration(state.hourlyFactsVersion, state.pricingVersion+1, state.selectorGeneration)
	insertGeneration(state.hourlyFactsVersion, state.pricingVersion, state.selectorGeneration+1)

	var records int64
	if err := visitFilteredUsageAnalyticsHourly(context.Background(), app.db, filters, state, func(value usageAnalyticsHourlyContribution) {
		records += value.recordCount
	}); err != nil {
		t.Fatalf("read captured hourly generation: %v", err)
	}
	if records != 1 {
		t.Fatalf("captured generation records = %d, want 1", records)
	}

	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, collector, "timestamp ASC"); err != nil {
		t.Fatalf("collect usage with mismatched hourly generations: %v", err)
	}
	if got := collector.summaryResponse()["total_records"]; got != 1 {
		t.Fatalf("collected records = %#v, want 1", got)
	}
}

func TestUsageAnalyticsHourlySnapshotRejectsChangedStateBeforeAccumulating(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-4 * time.Hour)
	end := start.Add(2 * time.Hour)
	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(10*time.Minute), "hourly-snapshot-base", 10)
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	filters := UsageFilters{Start: &start, End: &end}
	baseline := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, baseline, "timestamp ASC"); err != nil {
		t.Fatalf("collect base usage: %v", err)
	}
	plan := usageAnalyticsHourlyReadPlanFor(filters)
	if !plan.useHourly {
		t.Fatalf("hourly read plan = %#v, want hourly", plan)
	}
	hourlyPricing, expectedState, err := app.ensureUsageAnalyticsHourlyWithState(context.Background(), pricing)
	if err != nil {
		t.Fatalf("prepare hourly snapshot: %v", err)
	}

	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(time.Hour+10*time.Minute), "hourly-snapshot-tail", 20)
	if err := app.ensureUsageAnalyticsFacts(context.Background()); err != nil {
		t.Fatalf("reconcile appended fact: %v", err)
	}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	err = app.collectUsageAnalyticsHourlySnapshot(context.Background(), plan, hourlyPricing, expectedState, collector, "timestamp ASC")
	if !errors.Is(err, errUsageAnalyticsHourlyReadGenerationChanged) {
		t.Fatalf("snapshot error = %v, want generation change", err)
	}
	if got := collector.summaryResponse()["total_records"]; got != 0 {
		t.Fatalf("changed snapshot accumulated records = %#v, want 0", got)
	}

	fresh := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, fresh, "timestamp ASC"); err != nil {
		t.Fatalf("collect current usage: %v", err)
	}
	if got := fresh.summaryResponse()["total_records"]; got != 2 {
		t.Fatalf("current collected records = %#v, want 2", got)
	}
}

func TestUsageAnalyticsHourlyReadsAppendedFactsWithoutFullRebuild(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-4 * time.Hour)
	end := start.Add(4 * time.Hour)
	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(10*time.Minute), "hourly-base", 10)

	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	filters := UsageFilters{Start: &start, End: &end}
	first := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, first, "timestamp ASC"); err != nil {
		t.Fatalf("collect base usage: %v", err)
	}
	baseState, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load base analytics state: %v", err)
	}
	if baseState.hourlyFactsVersion != baseState.factsVersion || baseState.hourlyMaxRecordID == 0 || baseState.hourlyNeedsRebuild {
		t.Fatalf("base hourly state = %#v, want complete aggregate", baseState)
	}

	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(2*time.Hour+10*time.Minute), "hourly-tail", 20)
	second := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, second, "timestamp ASC"); err != nil {
		t.Fatalf("collect appended usage: %v", err)
	}
	if got := second.summaryResponse()["total_records"]; got != 2 {
		t.Fatalf("appended summary records = %#v, want 2", got)
	}
	state, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load appended analytics state: %v", err)
	}
	if state.factsVersion <= state.hourlyFactsVersion || state.hourlyFactsVersion != baseState.hourlyFactsVersion || state.hourlyMaxRecordID != baseState.hourlyMaxRecordID || state.hourlyNeedsRebuild {
		t.Fatalf("appended hourly state = %#v, want unchanged aggregate base plus fact tail", state)
	}
}

func TestUsageAnalyticsHourlyRebuildsAfterHistoricalFactCorrection(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	start := time.Now().In(appTimeLocation).Truncate(time.Hour).Add(-3 * time.Hour)
	end := start.Add(3 * time.Hour)
	insertUsageAnalyticsHourlyTestRecord(t, app, start.Add(10*time.Minute), "hourly-correction", 10)
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("billingPriceIndex: %v", err)
	}
	filters := UsageFilters{Start: &start, End: &end}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, collector, "timestamp ASC"); err != nil {
		t.Fatalf("collect base usage: %v", err)
	}
	baseState, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load base analytics state: %v", err)
	}
	if _, err := app.db.Exec(`UPDATE usage_records SET total_tokens = 25 WHERE dedupe_key = 'hourly-correction'`); err != nil {
		t.Fatalf("correct raw usage tokens: %v", err)
	}

	corrected := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := app.collectUsageAnalytics(context.Background(), filters, pricing, corrected, "timestamp ASC"); err != nil {
		t.Fatalf("collect corrected usage: %v", err)
	}
	if got := corrected.summaryResponse()["total_tokens"]; got != 25 {
		t.Fatalf("corrected summary tokens = %#v, want 25", got)
	}
	state, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load corrected analytics state: %v", err)
	}
	if state.hourlyFactsVersion != state.factsVersion || state.hourlyFactsVersion <= baseState.hourlyFactsVersion || state.hourlyNeedsRebuild {
		t.Fatalf("corrected hourly state = %#v, want rebuilt current aggregate", state)
	}
}

func insertUsageAnalyticsHourlyTestRecord(t *testing.T, app *App, timestamp time.Time, dedupeKey string, totalTokens int) {
	t.Helper()
	if _, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-hourly-tail', '/v1/responses', 'api_key', 0,
			?, 0, 0, 0, 0, 0, ?, ?, '{}')
	`, dbTime(timestamp), dbTime(timestamp), totalTokens, totalTokens, dedupeKey); err != nil {
		t.Fatalf("insert hourly test record %q: %v", dedupeKey, err)
	}
}
