package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPriceTimeRuleJSONPresenceAndRoutes(t *testing.T) {
	for _, raw := range []string{"", `null`, `{}`, `{"timezone":"UTC","peak_windows":[],"offpeak_mode":"explicit","long_context_multiplier":1,"offpeak_rates":{}}`} {
		if _, err := parsePriceTimeRule(sql.NullString{String: raw, Valid: true}); err == nil {
			t.Fatalf("accepted incomplete rule %s", raw)
		}
	}
	var batch priceTimeBatchPayload
	if json.Unmarshal([]byte(`{"price_ids":[1]}`), &batch) == nil {
		t.Fatal("omitted rule treated as disable")
	}
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	path := "/api/model-prices/deepseek-template"
	for _, route := range []struct{ method, path string }{{http.MethodGet, path}, {http.MethodPut, path}, {http.MethodPost, path + "/preview"}, {http.MethodPost, path + "/apply"}} {
		requestJSONForPricingTestExpectStatus(t, handler, route.method, route.path, nil, nil, http.StatusUnauthorized)
	}
	admin := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, admin, nil)
	member := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	member = latestCookiesByName(requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, member, nil))
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, path, testTimeRule(), member, http.StatusForbidden)
	var rule PriceTimeRule
	requestJSONForPricingTest(t, handler, http.MethodGet, path, nil, admin, &rule)
	if rule.Timezone != "Asia/Shanghai" || rule.OffpeakMultiplier != .5 {
		t.Fatal("migration template seed wrong")
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, path, map[string]any{}, admin, http.StatusUnprocessableEntity)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodDelete, path, nil, admin, http.StatusMethodNotAllowed)
	id := seedCorrectionTestChannelPrice(t, a, "deepseek-v4-pro", 8)
	future := time.Now().Add(time.Hour).Truncate(time.Second).Add(123456 * time.Microsecond)
	var applied struct {
		EffectiveAt time.Time `json:"effective_at"`
	}
	requestJSONForPricingTest(t, handler, http.MethodPost, path+"/apply", priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule(), EffectiveAt: &future}, admin, &applied)
	if !applied.EffectiveAt.Equal(future) {
		t.Fatalf("batch boundary lost precision: %v != %v", applied.EffectiveAt, future)
	}
	var disablePreview struct {
		Applied bool                 `json:"applied"`
		Items   []priceTimeBatchItem `json:"items"`
	}
	requestJSONForPricingTest(t, handler, http.MethodPost, path+"/preview", priceTimeBatchPayload{PriceIDs: []int{id}, Rule: nil}, admin, &disablePreview)
	if disablePreview.Applied || len(disablePreview.Items) != 1 || disablePreview.Items[0].After.TimePricing != nil {
		t.Fatalf("disable preview = %#v, want one unsaved rule removal", disablePreview)
	}
	var versions []ModelPriceVersion
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/"+strconv.Itoa(id)+"/versions", nil, admin, &versions)
	if len(versions) != 2 || !versions[1].EffectiveAt.Equal(future) {
		t.Fatalf("version API lost boundary precision: %#v", versions)
	}
}

func TestPriceTimeFastAndLongContextAppliedOnce(t *testing.T) {
	rule := testTimeRule()
	if err := validatePriceTimeRule(rule); err != nil {
		t.Fatal(err)
	}
	fast := 2.0
	brand, key, model := "openai_compatibility", "deepseek", "deepseek-v4-flash"
	p := &ModelPrice{PriceScope: modelPriceScopeChannel, ChannelBrand: &brand, ChannelKey: &key, Model: model, BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 8, PriorityMultiplier: &fast, TimePricing: rule, LongContext: &ModelPriceLongContext{ThresholdInputTokens: 100, InputUSDPerMillion: 40}}
	at, _ := time.Parse(time.RFC3339, "2026-09-06T10:00:00+08:00")
	record := UsageRecord{Timestamp: at, Provider: testStringPtr("deepseek"), Model: &model, ServiceTier: testStringPtr("priority"), InputTokens: 1000000}
	got := calculateRecordCostForMatch(record, p, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 20 || !got.LongContextApplied {
		t.Fatalf("long-context offpeak x Fast = %#v, want 20", got)
	}
	p.LongContext = nil
	got = calculateRecordCostForMatch(record, p, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 8 {
		t.Fatalf("base offpeak x Fast = %#v, want 8", got)
	}
}

func TestPriceTimeBatchProjectsLegacyFieldsWithoutChangingSnapshots(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	admin := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin", "password": "test-password", "nickname": "Admin",
	}, nil, nil)
	id := seedCorrectionTestChannelPrice(t, a, "deepseek-v4-flash", 8)
	if _, err := a.db.Exec(`UPDATE model_prices SET priority_multiplier=?,
		long_context_threshold_tokens=100, long_context_input_usd_per_million=?,
		long_context_output_usd_per_million=NULL, long_context_cache_read_usd_per_million=0,
		long_context_cache_creation_usd_per_million=0 WHERE id=?`, math.Inf(1), math.Inf(1), id); err != nil {
		t.Fatal(err)
	}
	payload := priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule()}
	for _, action := range []string{"preview", "apply"} {
		var response struct {
			Items []priceTimeBatchItem `json:"items"`
		}
		requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices/deepseek-template/"+action, payload, admin, &response)
		if len(response.Items) != 1 {
			t.Fatalf("%s items = %#v", action, response.Items)
		}
		for _, price := range []ModelPrice{response.Items[0].Before, response.Items[0].After} {
			if price.PriorityMultiplier != nil || price.LongContext != nil || price.PreservedLongContext == nil ||
				price.PreservedLongContext.ThresholdInputTokens == nil || *price.PreservedLongContext.ThresholdInputTokens != 100 ||
				price.PreservedLongContext.InputUSDPerMillion != nil || price.PreservedLongContext.NonFiniteFields["input_usd_per_million"] != "+Inf" ||
				price.PreservedLongContext.OutputUSDPerMillion != nil || len(price.PreservedLongContext.NonFiniteFields) != 1 {
				t.Fatalf("%s exposed invalid billing fields instead of audit metadata: %#v", action, price)
			}
		}
		if action == "preview" {
			var count int
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM model_price_versions WHERE price_id=?`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("preview wrote %d versions", count)
			}
		}
	}
	versions, err := a.loadModelPriceVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key := channelModelPriceKey(modelPriceChannelAuthTypeOAuth, "codex", "oauth_pool", "deepseek-v4-flash")
	if len(versions[key].all) != 2 {
		t.Fatalf("applied versions = %#v", versions[key].all)
	}
	for _, version := range versions[key].all {
		price := version.Price
		if price.PriorityMultiplier == nil || !math.IsInf(*price.PriorityMultiplier, 1) || !price.longContextInvalid ||
			price.PreservedLongContext == nil || price.PreservedLongContext.InputUSDPerMillion == nil || !math.IsInf(*price.PreservedLongContext.InputUSDPerMillion, 1) ||
			price.PreservedLongContext.OutputUSDPerMillion != nil || len(price.PreservedLongContext.NonFiniteFields) != 0 {
			t.Fatalf("API projection changed the persisted fail-closed snapshot: %#v", price)
		}
	}
}

func TestPriceTimeExplicitZeroCacheWrite(t *testing.T) {
	rule := testTimeRule()
	rule.OffpeakMode = "explicit"
	rule.OffpeakRates = &PriceOffpeakRates{Input: 3, Output: 4, CacheRead: 1, CacheCreation: 0}
	if err := validatePriceTimeRule(rule); err != nil {
		t.Fatal(err)
	}
	p := &ModelPrice{BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 8, CacheCreationUSDPerMillion: 6, TimePricing: rule}
	at, _ := time.Parse(time.RFC3339, "2026-09-06T10:00:00+08:00")
	record := UsageRecord{Timestamp: at, InputTokens: 1000000, CacheCreationTokens: 1000000}
	got := calculateRecordCostForMatch(record, p, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0 {
		t.Fatalf("explicit free cache write charged: %#v", got)
	}
	record.Timestamp = at.Add(24 * time.Hour)
	if got := calculateRecordCostForMatch(record, p, priceMatchStatusMatched, nil, false); got.TotalUSD != 6 {
		t.Fatalf("peak cache write changed: %#v", got)
	}
}

func TestPriceTimeUpdatePreservesActiveRuleAndRejectsIncompatibleBilling(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "deepseek-v4-flash"
	id := seedCorrectionTestChannelPrice(t, a, model, 8)
	if _, err := a.applyPriceTimeBatch(ctx, priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule()}, true); err != nil {
		t.Fatal(err)
	}
	fast := 2.0
	p, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: &fast})
	if err != nil || p.TimePricing == nil {
		t.Fatalf("Fast edit lost active rule: %#v %v", p, err)
	}
	payload := correctionTestPayload(model, 8, false)
	payload.BillingUnit = modelBillingUnitRequest
	payload.RequestUSD = &fast
	if _, err := a.updatePrice(ctx, id, payload); err == nil {
		t.Fatal("inherited token rule accepted on request price")
	}
	payload = correctionTestPayload(model, 8, false)
	payload.TimePricingSet = true
	if _, err := a.updatePrice(ctx, id, payload); err != nil {
		t.Fatal(err)
	}
	versions, err := a.listModelPriceVersionsForPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 4 || versions[2].TimePricing == nil || versions[3].TimePricing != nil {
		t.Fatalf("disable must retain prior rule history: %#v", versions)
	}
}

func TestPriceTimeRuleRejectsLibraryPricesAndPermitsLegacyCleanup(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	admin := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin", "password": "test-password", "nickname": "Admin",
	}, nil, nil)
	base := map[string]any{
		"provider":                       "deepseek",
		"model":                          "deepseek-v4-flash",
		"price_scope":                    modelPriceScopeLibrary,
		"billing_unit":                   modelBillingUnitToken,
		"input_usd_per_million":          2,
		"output_usd_per_million":         4,
		"cache_read_usd_per_million":     1,
		"cache_creation_usd_per_million": 2,
	}
	withRule := make(map[string]any, len(base)+2)
	for key, value := range base {
		withRule[key] = value
	}
	withRule["time_pricing"] = testTimeRule()
	withRule["time_pricing_set"] = true
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices", withRule, admin, http.StatusUnprocessableEntity)

	var created ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", base, admin, &created)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, "/api/model-prices/"+strconv.Itoa(created.ID), withRule, admin, http.StatusUnprocessableEntity)

	// A prior malformed library row must not silently retain the rule through a
	// legacy update. Explicit null is the supported repair path.
	if _, err := a.db.Exec(`UPDATE model_prices SET time_pricing=? WHERE id=?`, priceTimeRuleJSON(testTimeRule()), created.ID); err != nil {
		t.Fatal(err)
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, "/api/model-prices/"+strconv.Itoa(created.ID), base, admin, http.StatusUnprocessableEntity)
	cleanup := make(map[string]any, len(base)+2)
	for key, value := range base {
		cleanup[key] = value
	}
	cleanup["time_pricing"] = nil
	cleanup["time_pricing_set"] = true
	var cleaned ModelPrice
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/"+strconv.Itoa(created.ID), cleanup, admin, &cleaned)
	if cleaned.TimePricing != nil {
		t.Fatalf("explicit legacy cleanup retained library rule: %#v", cleaned)
	}
}

func testTimeRule() *PriceTimeRule {
	return &PriceTimeRule{Timezone: "Asia/Shanghai", PeakWindows: []PricePeakWindow{{Weekdays: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "12:00"}, {Weekdays: []int{1, 2, 3, 4, 5}, Start: "14:00", End: "18:00"}}, OffpeakMode: "multiplier", OffpeakMultiplier: .5, LongContextMultiplier: .25}
}

func TestPriceTimeUserSummariesResolveHistoricalVersions(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "deepseek-v4-flash"
	id := seedCorrectionTestChannelPrice(t, a, model, 8)
	p, err := a.getPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.RFC3339, "2026-09-06T10:00:00+08:00")
	p.TimePricing = testTimeRule()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := a.appendModelPriceVersion(ctx, tx, p, at, true); err != nil {
		t.Fatal(err)
	}
	p.InputUSDPerMillion = 20
	if err := a.appendModelPriceVersion(ctx, tx, p, at.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	insertUserUsageSummaryTestUser(t, a, "time-member", at.Add(-time.Hour))
	insertUserUsageSummaryTestRecord(t, a, at, "time-member", model, 1000000, "time-old")
	insertUserUsageSummaryTestRecord(t, a, at.Add(time.Hour), "time-member", model, 1000000, "time-new")
	users, err := a.allUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := a.userUsageSummaries(ctx, users)
	if err != nil {
		t.Fatal(err)
	}
	got := summaries["time-member"]
	if got.TotalEstimatedCostUSD != 14 || got.TotalUnpricedRecords != 0 {
		t.Fatalf("user summaries ignored version/time rules: %#v", got)
	}
}
func TestPriceTimeRuleBoundariesAndIndependentRates(t *testing.T) {
	rule := testTimeRule()
	if err := validatePriceTimeRule(rule); err != nil {
		t.Fatal(err)
	}
	p := &ModelPrice{InputUSDPerMillion: 8, OutputUSDPerMillion: 16, CacheReadUSDPerMillion: 4, CacheCreationUSDPerMillion: 12, TimePricing: rule, LongContext: &ModelPriceLongContext{ThresholdInputTokens: 100, InputUSDPerMillion: 40, OutputUSDPerMillion: 80, CacheReadUSDPerMillion: 20, CacheCreationUSDPerMillion: 60}}
	for _, tc := range []struct {
		at   string
		want float64
	}{{"2026-09-07T08:59:59+08:00", 4}, {"2026-09-07T09:00:00+08:00", 8}, {"2026-09-07T12:00:00+08:00", 4}, {"2026-09-07T14:00:00+08:00", 8}, {"2026-09-07T18:00:00+08:00", 4}, {"2026-09-06T10:00:00+08:00", 4}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		got := priceForRequestTime(p, at)
		if got.InputUSDPerMillion != tc.want {
			t.Fatalf("%s got %v want %v", tc.at, got.InputUSDPerMillion, tc.want)
		}
		if tc.want == 4 && (got.LongContext.InputUSDPerMillion != 10 || got.CacheCreationUSDPerMillion != 6) {
			t.Fatal("independent tier/cache multiplier incorrect")
		}
	}
	if p.InputUSDPerMillion != 8 || p.LongContext.InputUSDPerMillion != 40 {
		t.Fatal("snapshot mutated")
	}
	rule.OffpeakMode = "explicit"
	rule.OffpeakRates = &PriceOffpeakRates{Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4}
	if err := validatePriceTimeRule(rule); err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.RFC3339, "2026-09-06T10:00:00+08:00")
	got := priceForRequestTime(p, at)
	if got.InputUSDPerMillion != 1 || got.CacheReadUSDPerMillion != 3 || got.LongContext.InputUSDPerMillion != 10 {
		t.Fatal("explicit base rates must not alter independent tier discount")
	}
}
func TestPriceTimeRuleOvernightOverlapAndTimezone(t *testing.T) {
	rule := testTimeRule()
	rule.Timezone = "UTC"
	rule.PeakWindows = []PricePeakWindow{{Weekdays: []int{6}, Start: "23:00", End: "02:00"}}
	if err := validatePriceTimeRule(rule); err != nil {
		t.Fatal(err)
	}
	p := &ModelPrice{InputUSDPerMillion: 2, TimePricing: rule}
	for _, tc := range []struct {
		at   string
		want float64
	}{{"2026-09-05T22:59:00Z", 1}, {"2026-09-05T23:00:00Z", 2}, {"2026-09-06T01:59:00Z", 2}, {"2026-09-06T02:00:00Z", 1}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := priceForRequestTime(p, at); got.InputUSDPerMillion != tc.want {
			t.Fatalf("%s = %v", tc.at, got.InputUSDPerMillion)
		}
	}
	rule.PeakWindows = append(rule.PeakWindows, PricePeakWindow{Weekdays: []int{0}, Start: "01:00", End: "03:00"})
	if validatePriceTimeRule(rule) == nil {
		t.Fatal("cross-week overlap accepted")
	}
	rule = testTimeRule()
	rule.Timezone = "Local"
	if validatePriceTimeRule(rule) == nil {
		t.Fatal("host timezone accepted")
	}
	rule = testTimeRule()
	rule.OffpeakRates = &PriceOffpeakRates{Input: math.Inf(1)}
	if validatePriceTimeRule(rule) == nil {
		t.Fatal("inactive nonfinite field accepted")
	}
}
func TestPriceTimeBatchScheduleAtomicityAndCorrections(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "deepseek-v4-flash"
	id := seedCorrectionTestChannelPrice(t, a, model, 8)
	original, err := a.getPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.appendModelPriceVersion(ctx, tx, original, time.Now().Add(-24*time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM model_price_versions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	future := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	payload := priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule(), EffectiveAt: &future}
	if _, err := a.applyPriceTimeBatch(ctx, payload, false); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("preview wrote versions")
	}
	bad := payload
	bad.PriceIDs = []int{id, 999999}
	if _, err := a.applyPriceTimeBatch(ctx, bad, true); err == nil {
		t.Fatal("missing target accepted")
	}
	if count() != 1 {
		t.Fatal("partial batch write")
	}
	if _, err := a.applyPriceTimeBatch(ctx, payload, true); err != nil {
		t.Fatal(err)
	}
	current, err := a.getPrice(ctx, id)
	if err != nil || current.TimePricing != nil {
		t.Fatalf("scheduled rule active early: %v %v", current.TimePricing, err)
	}
	idx, err := a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := resolveVersionedPrice(UsageRecord{Timestamp: future.Add(-time.Nanosecond)}, &current, idx)
	after := resolveVersionedPrice(UsageRecord{Timestamp: future}, &current, idx)
	if before.TimePricing != nil || after.TimePricing == nil {
		t.Fatal("schedule boundary wrong")
	}
	// Template editing cannot mutate the already persisted snapshot.
	changed := testTimeRule()
	changed.OffpeakMultiplier = .8
	if _, err := a.db.Exec(`UPDATE model_price_time_templates SET rule=? WHERE name='deepseek'`, priceTimeRuleJSON(changed)); err != nil {
		t.Fatal(err)
	}
	idx, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after = resolveVersionedPrice(UsageRecord{Timestamp: future}, &current, idx)
	if after.TimePricing.OffpeakMultiplier != .5 {
		t.Fatal("template changed channel snapshot")
	}
	// A present-day edit preserves the scheduled rule and never copies it backwards.
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 10, false)); err != nil {
		t.Fatal(err)
	}
	current, err = a.getPrice(ctx, id)
	if err != nil || current.InputUSDPerMillion != 10 || current.TimePricing != nil {
		t.Fatalf("present edit: %#v %v", current, err)
	}
	idx, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after = resolveVersionedPrice(UsageRecord{Timestamp: future}, &current, idx)
	if after.InputUSDPerMillion != 8 || after.TimePricing == nil {
		t.Fatal("present edit changed scheduled full snapshot")
	}
	// Correcting latest means the future version; present values must stay intact.
	correction := correctionTestPayload(model, 12, true)
	if _, err := a.updatePrice(ctx, id, correction); err != nil {
		t.Fatal(err)
	}
	current, err = a.getPrice(ctx, id)
	if err != nil || current.InputUSDPerMillion != 10 || current.TimePricing != nil {
		t.Fatalf("future correction active early: %#v %v", current, err)
	}
	idx, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after = resolveVersionedPrice(UsageRecord{Timestamp: future}, &current, idx)
	if after.InputUSDPerMillion != 12 || after.TimePricing == nil {
		t.Fatal("future correction lost rule")
	}
	if count() != 3 {
		t.Fatalf("unexpected version count %d", count())
	}
	versions, err := a.listModelPriceVersionsForPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if versions[2].TimePricing == nil || !versions[2].EffectiveAt.Equal(future) {
		t.Fatal("history lost snapshot or effective time")
	}
}

func TestPriceTimeBatchRejectsOversizedSelection(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ids := make([]int, priceTimeBatchMaxIDs+1)
	for i := range ids {
		ids[i] = i + 1
	}
	_, err = a.applyPriceTimeBatch(context.Background(), priceTimeBatchPayload{PriceIDs: ids, Rule: testTimeRule()}, false)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("oversized batch error = %v, want 422 validation error", err)
	}
	// Exactly the limit passes the size gate; the unknown ids then fail later
	// with a target error, which must not be the size-limit message.
	_, err = a.applyPriceTimeBatch(context.Background(), priceTimeBatchPayload{PriceIDs: ids[:priceTimeBatchMaxIDs], Rule: testTimeRule()}, false)
	if err == nil || strings.Contains(err.Error(), "单次最多") {
		t.Fatalf("batch at the limit hit the size gate: %v", err)
	}
}

func TestPriceTimeRuleRequiresChannelIdentity(t *testing.T) {
	base := ModelPrice{
		PriceScope:   modelPriceScopeChannel,
		ChannelBrand: testStringPtr("openai_compatibility"),
		ChannelKey:   testStringPtr("deepseek"),
		Model:        "deepseek-v4-flash",
		BillingUnit:  modelBillingUnitToken,
	}
	if !supportsPriceTimeRule(base) {
		t.Fatal("complete channel identity rejected")
	}
	for _, field := range []string{"brand", "key"} {
		for _, value := range []*string{nil, testStringPtr(""), testStringPtr(" \t ")} {
			price := base
			if field == "brand" {
				price.ChannelBrand = value
			} else {
				price.ChannelKey = value
			}
			if supportsPriceTimeRule(price) {
				t.Fatalf("incomplete channel %s accepted for time pricing: %#v", field, price)
			}
		}
	}
}

func TestPriceForRequestTimeToleratesUnpreparedRule(t *testing.T) {
	var rule PriceTimeRule
	if err := json.Unmarshal([]byte(`{"timezone":"Asia/Shanghai","peak_windows":[{"weekdays":[1,2,3,4,5],"start":"09:00","end":"12:00"}],"offpeak_mode":"multiplier","offpeak_multiplier":0.5,"long_context_multiplier":0.5}`), &rule); err != nil {
		t.Fatal(err)
	}
	if rule.location != nil {
		t.Fatal("test precondition: json.Unmarshal must not prepare private fields")
	}
	price := &ModelPrice{InputUSDPerMillion: 10, TimePricing: &rule}
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	peak := time.Date(2026, 9, 7, 10, 0, 0, 0, shanghai) // Monday 10:00
	if got := priceForRequestTime(price, peak); got.InputUSDPerMillion != 10 {
		t.Fatalf("peak price = %v, want 10", got.InputUSDPerMillion)
	}
	offpeak := time.Date(2026, 9, 7, 13, 0, 0, 0, shanghai)
	if got := priceForRequestTime(price, offpeak); got.InputUSDPerMillion != 5 {
		t.Fatalf("off-peak price = %v, want 5", got.InputUSDPerMillion)
	}
	if rule.location != nil {
		t.Fatal("shared rule pointer was mutated during pricing")
	}
	invalid := &ModelPrice{InputUSDPerMillion: 10, TimePricing: &PriceTimeRule{Timezone: "Not/AZone", OffpeakMode: "multiplier", OffpeakMultiplier: 0.5}}
	if got := priceForRequestTime(invalid, offpeak); got.InputUSDPerMillion != 10 {
		t.Fatalf("invalid rule must fail closed to peak price, got %v", got.InputUSDPerMillion)
	}
}

func TestDeepSeekTemplatePutRecreatesMissingSeed(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	admin := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	if _, err := a.db.Exec(`DELETE FROM model_price_time_templates WHERE name='deepseek'`); err != nil {
		t.Fatal(err)
	}
	path := "/api/model-prices/deepseek-template"
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, path, nil, admin, http.StatusNotFound)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, path, testTimeRule(), admin, http.StatusOK)
	var stored PriceTimeRule
	requestJSONForPricingTest(t, handler, http.MethodGet, path, nil, admin, &stored)
	if stored.LongContextMultiplier != testTimeRule().LongContextMultiplier {
		t.Fatalf("template not persisted after seed loss: %#v", stored)
	}
}

func TestPriceTimeBatchImmediateUsesTimeAfterConnectionWait(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := seedCorrectionTestChannelPrice(t, a, "deepseek-batch-wait", 8)
	p, err := a.getPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := a.appendModelPriceVersion(ctx, tx, p, time.Now().Add(-time.Hour), true); err != nil {
		t.Fatal(err)
	}

	type batchResult struct {
		value any
		err   error
	}
	done := make(chan batchResult, 1)
	waits := a.db.Stats().WaitCount
	go func() {
		value, err := a.applyPriceTimeBatch(ctx, priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule()}, true)
		done <- batchResult{value, err}
	}()
	waitForPriceTimeBatchConnection(t, ctx, a.db, waits)

	// Commit another price while the batch is queued for the only connection.
	// The immediate batch must use both its amount and a later effective time.
	changedAt := time.Now().Truncate(time.Microsecond)
	p.InputUSDPerMillion = 12
	if err := a.appendModelPriceVersion(ctx, tx, p, changedAt, false); err != nil {
		t.Fatal(err)
	}
	if err := materializePriceSnapshot(ctx, tx, p); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var result batchResult
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatalf("batch did not finish after releasing the connection: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	response := result.value.(map[string]any)
	effective, ok := parseDBTime(response["effective_at"].(string))
	if !ok || effective.Before(changedAt) {
		t.Errorf("batch effective_at = %v, want at or after concurrent save %v", effective, changedAt)
	}
	items := response["items"].([]priceTimeBatchItem)
	if response["applied"] != true || len(items) != 1 || items[0].Before.InputUSDPerMillion != 12 || items[0].After.InputUSDPerMillion != 12 {
		t.Errorf("batch did not use the latest committed amount: %#v", response)
	}
	current, err := a.getPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.InputUSDPerMillion != 12 || current.TimePricing == nil {
		t.Fatalf("successful immediate batch is not current: input=%v rule=%#v", current.InputUSDPerMillion, current.TimePricing)
	}
}

func TestPriceTimeBatchRejectsScheduleExpiredDuringConnectionWait(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(strconv.FormatBool(apply), func(t *testing.T) {
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
			a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			id := seedCorrectionTestChannelPrice(t, a, "deepseek-expired-batch", 8)
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			future := time.Now().Add(time.Second)
			done := make(chan error, 1)
			waits := a.db.Stats().WaitCount
			go func() {
				_, err := a.applyPriceTimeBatch(ctx, priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule(), EffectiveAt: &future}, apply)
				done <- err
			}()
			waitForPriceTimeBatchConnection(t, ctx, a.db, waits)
			timer := time.NewTimer(time.Until(future.Add(time.Millisecond)))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatalf("batch did not finish after releasing the connection: %v", ctx.Err())
			}
			var appErr *AppError
			if !errors.As(err, &appErr) || appErr.Status != http.StatusUnprocessableEntity {
				t.Errorf("expired schedule error = %v, want 422 validation error", err)
			}
			var count int
			if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_price_versions WHERE price_id=?`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expired schedule wrote %d versions", count)
			}
		})
	}
}

func waitForPriceTimeBatchConnection(t *testing.T, ctx context.Context, db *sql.DB, previousWaits int64) {
	t.Helper()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for db.Stats().WaitCount == previousWaits {
		select {
		case <-poll.C:
		case <-ctx.Done():
			t.Fatalf("batch did not begin waiting for the SQLite connection: %v", ctx.Err())
		}
	}
}
