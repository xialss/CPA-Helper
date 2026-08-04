package app

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUsageAnalyticsCollectorMatchesSliceAggregates(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, appTimeLocation)
	end := start.AddDate(0, 0, 30)
	filters := UsageFilters{Start: &start, End: &end}

	provider := "custom"
	model := "model-a"
	channelBrand := string(aiProviderBrandOpenAICompatibility)
	channelKey := provider
	authType := modelPriceChannelAuthTypeAPIKey
	authIndex := "channel-a"
	primaryDescription := "primary"
	blankDescription := "  "
	alice := "alice"
	ttft := 42.5
	prices := modelPriceIndex{
		channelModelPriceKey(authType, channelBrand, channelKey, model): {
			ID:                         1,
			Provider:                   provider,
			Model:                      model,
			PriceScope:                 modelPriceScopeChannel,
			ChannelAuthType:            &authType,
			ChannelBrand:               &channelBrand,
			ChannelKey:                 &channelKey,
			InputUSDPerMillion:         1,
			OutputUSDPerMillion:        2,
			CacheReadUSDPerMillion:     0.5,
			CacheCreationUSDPerMillion: 1.5,
		},
	}
	channelIdentity := modelPriceChannelGroupIdentityKey(authType, aiProviderBrandOpenAICompatibility, channelKey)
	matchContext := modelPriceMatchContext{
		ChannelLabels: modelPriceChannelLabelIndex{
			channelIdentity: {Label: "Custom channel"},
		},
	}
	users := map[string]userInfo{
		alice: {ID: 7, Username: alice, Name: "Alice"},
	}
	records := []UsageRecord{
		{
			Timestamp:         start.Add(2 * time.Hour),
			UsageUsername:     &alice,
			APIKeyDescription: &primaryDescription,
			Provider:          &provider,
			Model:             &model,
			Endpoint:          stringPtr("/v1/chat"),
			Auth:              &authType,
			AuthIndex:         &authIndex,
			InputTokens:       1_000_000,
			OutputTokens:      100,
			CachedTokens:      100,
			CacheReadTokens:   100,
			TotalTokens:       1_000_100,
			TTFTMS:            &ttft,
		},
		{
			Timestamp:         start.Add(3 * time.Hour),
			APIKeyDescription: &blankDescription,
			Provider:          &provider,
			Model:             &model,
			Endpoint:          stringPtr("/v1/chat"),
			Auth:              &authType,
			AuthIndex:         &authIndex,
			Failed:            true,
			InputTokens:       500_000,
			OutputTokens:      50,
			TotalTokens:       500_050,
		},
		{
			Timestamp:    start.Add(26 * time.Hour),
			Provider:     stringPtr("other"),
			Model:        stringPtr("model-b"),
			Endpoint:     stringPtr("/v1/responses"),
			InputTokens:  200,
			OutputTokens: 20,
			TotalTokens:  220,
		},
	}
	collector := newUsageAnalyticsCollector(filters, prices, matchContext, users, usageAnalyticsCollectorOptions{
		Summary:       true,
		Trends:        true,
		Distributions: true,
		Rankings: map[string]string{
			"api_key_description": usageRankingSortTokens,
			"model":               usageRankingSortCost,
			"user":                usageRankingSortRecords,
		},
	})
	for _, record := range records {
		collector.Add(record)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{
			name: "summary",
			got:  collector.summaryResponse(),
			want: usageSummaryFromRecords(filters, records, prices, matchContext),
		},
		{
			name: "trends",
			got:  collector.trendResponse(),
			want: trendPointsFromRecords(filters, records, prices, matchContext),
		},
		{
			name: "API key ranking",
			got:  collector.rankingResponse("api_key_description"),
			want: rankingFromRecordsBySort(records, prices, "api_key_description", users, usageRankingSortTokens, matchContext),
		},
		{
			name: "model ranking",
			got:  collector.rankingResponse("model"),
			want: rankingFromRecordsBySort(records, prices, "model", users, usageRankingSortCost, matchContext),
		},
		{
			name: "user ranking",
			got:  collector.rankingResponse("user"),
			want: rankingFromRecordsBySort(records, prices, "user", users, usageRankingSortRecords, matchContext),
		},
		{
			name: "distributions",
			got:  collector.distributionResponse(),
			want: distributionsFromRecords(records, prices, matchContext),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("streamed result = %#v, want %#v", test.got, test.want)
			}
		})
	}
}

func TestUsageAnalyticsCollectorAppliesPriorityCost(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, appTimeLocation)
	end := start.AddDate(0, 0, 30)
	filters := UsageFilters{Start: &start, End: &end}
	provider := "codex"
	model := "gpt-priority"
	tier := serviceTierPriority
	multiplier := 2.5
	record := UsageRecord{
		Timestamp:    start,
		Provider:     &provider,
		Model:        &model,
		ServiceTier:  &tier,
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		TotalTokens:  2_000_000,
	}
	prices := modelPriceIndex{
		priceKey(provider, model): {
			Provider:            provider,
			Model:               model,
			InputUSDPerMillion:  2,
			OutputUSDPerMillion: 4,
			PriorityMultiplier:  &multiplier,
		},
	}
	matchContext := modelPriceMatchContext{SelectorsAvailable: true}
	collector := newUsageAnalyticsCollector(filters, prices, matchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	collector.Add(record)

	got := collector.summaryResponse()
	want := usageSummaryFromRecords(filters, []UsageRecord{record}, prices, matchContext)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed summary = %#v, want %#v", got, want)
	}
	if got["estimated_cost_usd"] != 15.0 {
		t.Fatalf("priority estimated cost = %#v, want 15", got["estimated_cost_usd"])
	}
}

func TestUsageAnalyticsProjectionPreservesPriorityTier(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := []byte(`{"timestamp":"2026-07-01T00:00:00+08:00","api_key":"sk-priority","provider":"codex","model":"gpt-priority","service_tier":"priority","input_tokens":1000000}`)
	if _, created, err := app.saveUsageMessage(context.Background(), raw, modelPriceBillingIndex{}); err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}

	records, err := app.filteredUsageAnalyticsRecords(context.Background(), UsageFilters{}, "")
	if err != nil {
		t.Fatalf("filteredUsageAnalyticsRecords: %v", err)
	}
	if len(records) != 1 || records[0].ServiceTier == nil || *records[0].ServiceTier != serviceTierPriority {
		t.Fatalf("analytics records = %#v, want priority service tier", records)
	}
}

func TestUsageAnalyticsFactsPendingTracksAndReconcilesFactQueue(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	record, created, err := app.saveUsageMessage(ctx, []byte(`{"timestamp":"2026-07-01T00:00:00+08:00","provider":"codex","model":"gpt-test","input_tokens":10}`), modelPriceBillingIndex{})
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	pending, err := app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("check empty pending queue: %v", err)
	}
	if pending {
		t.Fatal("newly saved usage record left a pending fact")
	}

	if _, err := app.db.ExecContext(ctx, `UPDATE usage_records SET input_tokens = input_tokens + 1 WHERE id = ?`, record.ID); err != nil {
		t.Fatalf("mark usage record pending: %v", err)
	}
	pending, err = app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("check pending queue: %v", err)
	}
	if !pending {
		t.Fatal("updated usage record did not enter the pending fact queue")
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile pending facts: %v", err)
	}
	pending, err = app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("check reconciled queue: %v", err)
	}
	if pending {
		t.Fatal("reconciled usage record remained pending")
	}
}

func TestUsageAnalyticsFactsPersistRawSourceAccountAliases(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	timestamp := time.Date(2026, time.July, 1, 12, 0, 0, 0, appTimeLocation)
	aliases := []struct {
		field string
		value string
	}{
		{field: "email", value: "EMAIL@example.com"},
		{field: "account_email", value: "ACCOUNT_EMAIL@example.com"},
		{field: "accountEmail", value: "ACCOUNT_CAMEL@example.com"},
		{field: "user_email", value: "USER_EMAIL@example.com"},
		{field: "userEmail", value: "USER_CAMEL@example.com"},
	}
	for index, alias := range aliases {
		source := "opaque-normal-" + alias.field
		raw := `{"timestamp":"` + timestamp.Format(time.RFC3339) + `","api_key":"sk-normal-` + strconv.Itoa(index) + `","provider":"codex","model":"gpt-test","source":"` + source + `","` + alias.field + `":"` + alias.value + `","input_tokens":1}`
		record, created, err := app.saveUsageMessage(ctx, []byte(raw), modelPriceBillingIndex{})
		if err != nil || !created {
			t.Fatalf("save normal %s created=%v err=%v", alias.field, created, err)
		}
		want := strings.ToLower(alias.value)
		assertUsageAnalyticsSourceAccount(t, app, record.ID, source, want)
		if _, err := app.db.ExecContext(ctx, `UPDATE usage_records SET raw_json = '' WHERE id = ?`, record.ID); err != nil {
			t.Fatalf("prune normal raw payload %s: %v", alias.field, err)
		}
	}

	deferredSource := "opaque-deferred"
	deferredRaw := `{"source":"` + deferredSource + `","accountEmail":"DEFERRED@example.com","auth_type":"oauth"}`
	result, err := app.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, source, request_id, auth,
			failed, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
			total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'deferred-alias', 'oauth',
			0, 1, 0, 0, 0, 1, 'deferred-source-account-alias', ?)
	`, dbTime(timestamp.Add(time.Minute)), dbTime(timestamp.Add(time.Minute)), deferredSource, deferredRaw)
	if err != nil {
		t.Fatalf("insert deferred usage record: %v", err)
	}
	deferredID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read deferred usage record id: %v", err)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile deferred usage fact: %v", err)
	}
	assertUsageAnalyticsSourceAccount(t, app, int(deferredID), deferredSource, "deferred@example.com")
	if _, err := app.db.ExecContext(ctx, `UPDATE usage_records SET raw_json = '' WHERE id = ?`, deferredID); err != nil {
		t.Fatalf("prune deferred raw payload: %v", err)
	}

	start := timestamp.Add(-time.Hour)
	end := timestamp.Add(2 * time.Hour)
	analytics, err := app.filteredUsageAnalyticsRecords(ctx, UsageFilters{Start: &start, End: &end}, "")
	if err != nil {
		t.Fatalf("load analytics facts after pruning: %v", err)
	}
	if len(analytics) != len(aliases)+1 {
		t.Fatalf("analytics fact count after pruning = %d, want %d", len(analytics), len(aliases)+1)
	}
	for _, record := range analytics {
		if record.SourceAccount == nil || strings.TrimSpace(*record.SourceAccount) == "" {
			t.Fatalf("analytics record %d lost source account after pruning: %#v", record.ID, record)
		}
	}
}

func assertUsageAnalyticsSourceAccount(t *testing.T, app *App, recordID int, source, want string) {
	t.Helper()
	var recordAccount, factAccount, catalogAccount string
	if err := app.db.QueryRow(`SELECT source_account FROM usage_records WHERE id = ?`, recordID).Scan(&recordAccount); err != nil {
		t.Fatalf("read usage record source_account: %v", err)
	}
	if err := app.db.QueryRow(`SELECT source_account FROM usage_analytics_facts WHERE usage_record_id = ?`, recordID).Scan(&factAccount); err != nil {
		t.Fatalf("read usage fact source_account: %v", err)
	}
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		t.Fatal("source key is nil")
	}
	if err := app.db.QueryRow(`SELECT source_account FROM usage_source_catalog WHERE source_key = ?`, *sourceKey).Scan(&catalogAccount); err != nil {
		t.Fatalf("read source catalog source_account: %v", err)
	}
	for _, got := range []string{recordAccount, factAccount, catalogAccount} {
		if got != want {
			t.Fatalf("source account = %q, want %q", got, want)
		}
	}
}
