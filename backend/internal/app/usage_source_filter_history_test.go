package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestUsageHistoricalSourceKeyRoutes(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	adminCookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin", "password": "test-password", "nickname": "Admin",
	}, nil, nil)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false,
	}, adminCookies, nil)
	memberCookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "member", "password": "member-password",
	}, nil, nil)
	memberCookies = latestCookiesByName(requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "member-password", "password": "member-new-password",
	}, memberCookies, nil))

	const model = "shared-history-model"
	provider, otherProvider := "history-vendor", "other-history-vendor"
	sourceA, sourceB := "sk-history-source-A-original-123456", "sk-history-source-B-original-654321"
	seedQuotaTestPrice(t, a, provider, model, 2)
	seedQuotaTestPrice(t, a, otherProvider, model, 3)
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{
		{Brand: aiProviderBrandOpenAICompatibility, Name: &provider, Models: []aiProviderModel{{Name: model}}},
		{Brand: aiProviderBrandOpenAICompatibility, Name: &otherProvider, Models: []aiProviderModel{{Name: model}}},
	})
	oldDay := time.Now().In(appTimeLocation).AddDate(0, 0, -30)
	hour := time.Date(oldDay.Year(), oldDay.Month(), oldDay.Day(), 8, 0, 0, 0, appTimeLocation)
	start, end := hour.Add(15*time.Minute), hour.Add(2*time.Hour+45*time.Minute)
	type fixture struct {
		name, source, provider, username string
		minute, units                    int
	}
	insert := func(row fixture) {
		t.Helper()
		timestamp := hour.Add(time.Duration(row.minute) * time.Minute)
		if _, err := a.db.Exec(`INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider, model,
			endpoint, source, auth, input_tokens, output_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, '/v1/responses', ?, 'api_key', ?, ?, ?, ?, '{"auth_type":"api_key"}')`,
			dbTime(timestamp), dbTime(timestamp), row.username, row.username+"-key", row.provider, model,
			row.source, row.units*1_000_000, row.units*10, row.units*1_000_010, row.name); err != nil {
			t.Fatal(err)
		}
	}
	baseline := []fixture{
		{"a-leading", sourceA, provider, "admin", 20, 1},
		{"b-leading", sourceB, provider, "member", 25, 2},
		{"a-hour", sourceA, provider, "admin", 70, 3},
		{"b-hour", sourceB, provider, "member", 75, 5},
		{"a-trailing", sourceA, provider, "admin", 140, 13},
		{"b-trailing", sourceB, provider, "member", 145, 17},
		{"other-leading", sourceA, otherProvider, "member", 30, 19},
		{"other-hour", sourceA, otherProvider, "member", 80, 23},
		{"other-trailing", sourceA, otherProvider, "member", 150, 29},
		{"outside-start", sourceA, provider, "admin", 10, 101},
		{"outside-end", sourceA, provider, "admin", 165, 103},
	}
	for _, row := range baseline {
		insert(row)
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rebuildUsageAnalyticsHourly(ctx, pricing); err != nil {
		t.Fatal(err)
	}
	baseState, err := loadUsageAnalyticsState(ctx, a.db)
	if err != nil {
		t.Fatal(err)
	}
	if baseState.hourlyNeedsRebuild || baseState.hourlyFactsVersion != baseState.factsVersion || baseState.hourlyMaxRecordID == 0 {
		t.Fatalf("incomplete hourly source-filter fixture: %#v", baseState)
	}
	var hourlySources int
	if err := a.db.QueryRow(`SELECT COUNT(DISTINCT source_key) FROM usage_analytics_hourly WHERE provider = ? AND model = ?`, provider, model).Scan(&hourlySources); err != nil || hourlySources != 2 {
		t.Fatalf("hourly sources = %d, error=%v, want separate A/B groups", hourlySources, err)
	}
	tail := []fixture{
		{"a-tail", sourceA, provider, "admin", 90, 7},
		{"b-tail", sourceB, provider, "member", 95, 11},
		{"other-tail", sourceA, otherProvider, "member", 100, 31},
	}
	for _, row := range tail {
		insert(row)
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if pruned, err := a.pruneExpiredUsageRawJSON(ctx, time.Now()); err != nil || pruned != int64(len(baseline)+len(tail)) {
		t.Fatalf("prune historical source-filter raw payloads: count=%d error=%v", pruned, err)
	}
	var retainedRaw int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE raw_json <> ''`).Scan(&retainedRaw); err != nil || retainedRaw != 0 {
		t.Fatalf("historical raw payload count = %d, error=%v", retainedRaw, err)
	}

	keyA, keyB := *usageSourceKey(&sourceA), *usageSourceKey(&sourceB)
	for _, test := range []struct {
		name, scope, sourceKey, provider string
		cookies                          []*http.Cookie
		start, end                       time.Time
		records, units                   int
		cost                             float64
		account                          bool
	}{
		{"hybrid source A across providers", "admin", keyA, "", adminCookies, start, end, 8, 126, 354, false},
		{"hybrid source B same provider and model", "admin", keyB, "", adminCookies, start, end, 4, 35, 70, false},
		{"provider intersects source A", "admin", keyA, provider, adminCookies, start, end, 4, 24, 48, false},
		{"other provider intersects source A", "admin", keyA, otherProvider, adminCookies, start, end, 4, 102, 306, false},
		{"provider and source have empty intersection", "admin", keyB, otherProvider, adminCookies, start, end, 0, 0, 0, false},
		{"cleared source keeps provider filter", "admin", "", provider, adminCookies, start, end, 8, 59, 118, false},
		{"unknown source does not widen query", "admin", "unknown-source-key", "", adminCookies, start, end, 0, 0, 0, false},
		{"exact partial hour uses source A facts", "admin", keyA, provider, adminCookies, start, hour.Add(45 * time.Minute), 1, 1, 2, false},
		{"complete hour includes only source A and its tail", "admin", keyA, provider, adminCookies, hour.Add(time.Hour), hour.Add(2 * time.Hour), 2, 10, 20, false},
		{"member account ignores forged source A", "account", keyA, provider, memberCookies, start, end, 4, 35, 70, true},
		{"member cannot obtain admin source scope", "admin", keyA, provider, memberCookies, start, end, 4, 35, 70, true},
		{"administrator account ignores forged source B", "account", keyB, provider, adminCookies, start, end, 4, 24, 48, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := url.Values{
				"scope": {test.scope}, "start": {test.start.Format(time.RFC3339)}, "end": {test.end.Format(time.RFC3339)},
				"model": {model},
			}
			if test.provider != "" {
				query.Set("provider", test.provider)
			}
			if test.sourceKey != "" {
				query.Set("source_key", test.sourceKey)
			}
			read := func(route string, response any) {
				t.Helper()
				params := query.Encode()
				if route == "rankings" {
					params += "&group_by=model"
				}
				requestJSONForPricingTest(t, handler, http.MethodGet, "/api/usage/"+route+"?"+params, nil, test.cookies, response)
			}
			want := usageSourceFilterMetrics{Records: test.records, TotalTokens: test.units * 1_000_010, Cost: test.cost}
			assertMetrics := func(path string, got usageSourceFilterMetrics) {
				t.Helper()
				if got != want {
					t.Fatalf("%s = %#v, want %#v", path, got, want)
				}
			}
			assertSummary := func(path string, summary usageSourceFilterSummary) {
				t.Helper()
				assertMetrics(path, usageSourceFilterMetrics{Records: summary.TotalRecords, TotalTokens: summary.TotalTokens, Cost: summary.Cost})
				if summary.InputTokens != test.units*1_000_000 || summary.OutputTokens != test.units*10 || summary.UnpricedRecords != 0 {
					t.Fatalf("%s token/cost classification = %#v", path, summary)
				}
			}
			assertDistributions := func(path string, response usageSourceFilterDistributions) {
				t.Helper()
				assertMetrics(path+".providers", sumUsageSourceFilterMetrics(response.Providers))
				assertMetrics(path+".models", sumUsageSourceFilterMetrics(response.Models))
				assertMetrics(path+".endpoints", sumUsageSourceFilterMetrics(response.Endpoints))
				if got := sumUsageSourceFilterMetrics(response.ChannelCosts).Cost; got != want.Cost {
					t.Fatalf("%s.channel_costs = %v, want %v", path, got, want.Cost)
				}
			}
			var summary usageSourceFilterSummary
			read("summary", &summary)
			assertSummary("summary", summary)
			var trends []usageSourceFilterMetrics
			read("trends", &trends)
			assertMetrics("trends", sumUsageSourceFilterMetrics(trends))
			var rankings usageSourceFilterRanking
			read("rankings", &rankings)
			assertMetrics("rankings", sumUsageSourceFilterMetrics(rankings.Items))
			var distributions usageSourceFilterDistributions
			read("distributions", &distributions)
			assertDistributions("distributions", distributions)
			var overview struct {
				Summary       usageSourceFilterSummary       `json:"summary"`
				Trends        []usageSourceFilterMetrics     `json:"trends"`
				ModelRanking  usageSourceFilterRanking       `json:"model_ranking"`
				UserRanking   usageSourceFilterRanking       `json:"user_ranking"`
				KeyRanking    usageSourceFilterRanking       `json:"api_key_description_ranking"`
				Distributions usageSourceFilterDistributions `json:"distributions"`
				Options       struct {
					Sources []map[string]string `json:"sources"`
				} `json:"options"`
			}
			read("overview", &overview)
			assertSummary("overview.summary", overview.Summary)
			assertMetrics("overview.trends", sumUsageSourceFilterMetrics(overview.Trends))
			assertMetrics("overview.model_ranking", sumUsageSourceFilterMetrics(overview.ModelRanking.Items))
			assertMetrics("overview.key_ranking", sumUsageSourceFilterMetrics(overview.KeyRanking.Items))
			assertDistributions("overview.distributions", overview.Distributions)
			if test.account {
				if len(overview.UserRanking.Items) != 0 || overview.Options.Sources == nil || len(overview.Options.Sources) != 0 {
					t.Fatal("account overview exposed administrator user/source metadata")
				}
			} else {
				assertMetrics("overview.user_ranking", sumUsageSourceFilterMetrics(overview.UserRanking.Items))
				// Options remain date/scope-only even for an empty provider/source
				// intersection or an unknown selected source.
				if len(overview.Options.Sources) != 2 {
					t.Fatalf("source options unexpectedly cascaded with data filters: %#v", overview.Options.Sources)
				}
			}
		})
	}
	for _, route := range []string{"summary", "trends", "rankings", "distributions", "overview"} {
		requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, fmt.Sprintf("/api/usage/%s?scope=admin&source_key=%s", route, keyA), nil, nil, http.StatusUnauthorized)
	}
	state, err := loadUsageAnalyticsState(ctx, a.db)
	if err != nil {
		t.Fatal(err)
	}
	if state.hourlyNeedsRebuild || state.factsVersion <= state.hourlyFactsVersion ||
		state.hourlyFactsVersion != baseState.hourlyFactsVersion || state.hourlyMaxRecordID != baseState.hourlyMaxRecordID ||
		state.lastHourlyRebuildAt != baseState.lastHourlyRebuildAt {
		t.Fatalf("source-filter reads rebuilt the hourly base instead of reading the appended tail: %#v", state)
	}
}

type usageSourceFilterMetrics struct {
	Records     int     `json:"records"`
	TotalTokens int     `json:"total_tokens"`
	Cost        float64 `json:"estimated_cost_usd"`
}

type usageSourceFilterSummary struct {
	TotalRecords    int     `json:"total_records"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	TotalTokens     int     `json:"total_tokens"`
	Cost            float64 `json:"estimated_cost_usd"`
	UnpricedRecords int     `json:"unpriced_records"`
}

type usageSourceFilterRanking struct {
	Items []usageSourceFilterMetrics `json:"items"`
}

type usageSourceFilterDistributions struct {
	Providers    []usageSourceFilterMetrics `json:"providers"`
	Models       []usageSourceFilterMetrics `json:"models"`
	Endpoints    []usageSourceFilterMetrics `json:"endpoints"`
	ChannelCosts []usageSourceFilterMetrics `json:"channel_costs"`
}

func sumUsageSourceFilterMetrics(items []usageSourceFilterMetrics) usageSourceFilterMetrics {
	var sum usageSourceFilterMetrics
	for _, item := range items {
		sum.Records += item.Records
		sum.TotalTokens += item.TotalTokens
		sum.Cost += item.Cost
	}
	return sum
}
