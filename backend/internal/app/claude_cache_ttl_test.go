package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClaudeCacheCreationIgnoresRetiredTTLAndBuckets(t *testing.T) {
	provider, model, brand := "claude", "claude-cache-write", "claude"
	price := ModelPrice{
		Provider: provider, Model: model, PriceScope: modelPriceScopeChannel, ChannelBrand: &brand,
		InputUSDPerMillion: 2, OutputUSDPerMillion: 4, CacheReadUSDPerMillion: 0.5,
		CacheCreationUSDPerMillion: 3, CacheCreation1hUSDPerMillion: claudeTTLFloatPtr(80),
		BillingUnit: modelBillingUnitToken,
	}
	for _, ttl := range []string{"5m", "1h", "mixed", "unknown", ""} {
		for _, oneHour := range []*float64{nil, claudeTTLFloatPtr(0), claudeTTLFloatPtr(80), claudeTTLFloatPtr(math.Inf(1))} {
			current := price
			current.CacheCreation1hUSDPerMillion = oneHour
			record := UsageRecord{
				Provider: &provider, Model: &model, InputTokens: 1000000, OutputTokens: 1000000,
				CacheReadTokens: 1000000, CacheCreationTokens: 1000000, CacheCreationTTL: &ttl,
				// Even incomplete or contradictory historical buckets are inert.
				CacheCreation5mTokens: claudeTTLIntPtr(-1), CacheCreation1hTokens: claudeTTLIntPtr(600000),
				CacheCreationMetadata: json.RawMessage(`{"status":"enforced","ttl_source":"key_policy"}`),
			}
			got := calculateRecordCostForMatch(record, &current, priceMatchStatusMatched, nil, true)
			if got.Unpriced || got.TotalUSD != 9.5 || got.NormalInputTokens != 1000000 || got.ContextInputTokens != 3000000 || len(got.Items) != 4 {
				t.Fatalf("TTL %q, retired rate %v: cost = %#v, want four ordinary token items totaling 9.5", ttl, oneHour, got)
			}
			item, ok := got.Items[2].(usageTokenCostBreakdownItem)
			if !ok || item.Kind != usageCostKindCacheCreation || item.Tokens != 1000000 || item.USDPerMillion != 3 || item.SubtotalUSD != 3 {
				t.Fatalf("TTL %q: cache-write item = %#v", ttl, got.Items[2])
			}
		}
	}
	price.CacheCreationUSDPerMillion = 0
	ttl := "1h"
	got := calculateRecordCostForMatch(UsageRecord{Provider: &provider, Model: &model, CacheCreationTokens: 1000000, CacheCreationTTL: &ttl}, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0 || len(got.Items) != 1 {
		t.Fatalf("zero Claude cache-write rate = %#v, want a priced zero item", got)
	}
}

func TestRetiredCacheTTLDoesNotChangeGenericTokenSemantics(t *testing.T) {
	provider, model, ttl := "openai", "gpt-cache-write", "1h"
	price := ModelPrice{
		Provider: provider, Model: model, PriceScope: modelPriceScopeLibrary, BillingUnit: modelBillingUnitToken,
		InputUSDPerMillion: 1, CacheCreationUSDPerMillion: 2, CacheCreation1hUSDPerMillion: claudeTTLFloatPtr(88),
	}
	record := UsageRecord{Provider: &provider, Model: &model, InputTokens: 10, CacheCreationTokens: 4, CacheCreationTTL: &ttl}
	got := calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0.000014 || got.NormalInputTokens != 6 {
		t.Fatalf("generic cache-write cost = %#v, want ordinary included-input semantics", got)
	}
	price.CacheCreationUSDPerMillion = 0
	got = calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0.00001 || len(got.Items) != 1 {
		t.Fatalf("generic unset cache-write cost = %#v, want original input fallback", got)
	}
}

func TestRetiredCachePricesDoNotBlockActiveValidationOrTierMultipliers(t *testing.T) {
	for _, retired := range []float64{-1, math.Inf(1), math.MaxFloat64} {
		payload := modelPricePayload{
			Provider: "openai", Model: "gpt-6.1-sol", BillingUnit: modelBillingUnitToken,
			InputUSDPerMillion: 2, CacheCreationUSDPerMillion: 3, CacheCreation1hUSDPerMillion: &retired,
			LongContext: &modelPriceLongContextPayload{
				ThresholdInputTokens: claudeTTLInt64Ptr(100), InputUSDPerMillion: claudeTTLFloatPtr(4),
				OutputUSDPerMillion: claudeTTLFloatPtr(0), CacheReadUSDPerMillion: claudeTTLFloatPtr(0),
				CacheCreationUSDPerMillion: claudeTTLFloatPtr(6), CacheCreation1hUSDPerMillion: &retired,
			},
		}
		validated, err := validatePricePayload(payload)
		if err != nil {
			t.Fatalf("retired rate %v rejected active payload: %v", retired, err)
		}
		price := modelPriceFromPayload(validated, claudeTTLFloatPtr(2))
		price.UltrafastMultiplier = claudeTTLFloatPtr(3)
		if err := validatePriorityMultiplierForPrice(price); err != nil {
			t.Fatalf("retired rate %v rejected tier multiplier: %v", retired, err)
		}
		provider, model := "openai", price.Model
		for _, tier := range []struct {
			name string
			want float64
		}{{serviceTierPriority, 14}, {serviceTierUltrafast, 21}} {
			record := UsageRecord{Provider: &provider, Model: &model, InputTokens: 1500000, CacheCreationTokens: 500000, ServiceTier: &tier.name}
			got := calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
			if got.Unpriced || !got.LongContextApplied || got.TotalUSD != tier.want {
				t.Fatalf("retired rate %v / %s: cost = %#v, want %g", retired, tier.name, got, tier.want)
			}
		}
		payload.InputUSDPerMillion = -1
		if _, err := validatePricePayload(payload); err == nil {
			t.Fatal("negative active input price was accepted")
		}
	}
}

func TestRetiredCacheTTLFieldsAreAbsentFromAllPriceProjections(t *testing.T) {
	retired := math.Inf(1)
	audit := &ModelPriceLibraryConflictLongContext{
		InputUSDPerMillion: claudeTTLFloatPtr(math.Inf(1)), CacheCreation1hUSDPerMillion: &retired,
		NonFiniteFields: map[string]string{"input_usd_per_million": "+Inf", "cache_creation_1h_usd_per_million": "+Inf"},
	}
	for _, provider := range []string{"anthropic", "openai"} {
		price := ModelPrice{
			Provider: provider, Model: "model", BillingUnit: modelBillingUnitToken, CacheCreation1hUSDPerMillion: &retired,
			LongContext:          &ModelPriceLongContext{ThresholdInputTokens: 100, InputUSDPerMillion: 1, CacheCreation1hUSDPerMillion: &retired},
			PreservedLongContext: audit, TimePricing: &PriceTimeRule{OffpeakRates: &PriceOffpeakRates{CacheCreation1h: &retired}},
		}
		version := ModelPriceVersion{CacheCreation1hUSDPerMillion: &retired, LongContext: price.LongContext, PreservedLongContext: audit, TimePricing: price.TimePricing}
		catalog := ModelPriceCatalogResponse{Models: []ModelPriceCatalogItem{{Price: &price, TemplatePrice: &price}}}
		for _, projection := range []any{
			modelPriceForAPI(price), availableModelPriceFromPrice(&price), modelPriceCatalogForAPI(catalog),
			modelPriceLibraryConflictsForAPI([]ModelPriceLibraryConflict{{Price: price, ArchivedLongContext: audit}}),
			modelPriceVersionsForAPI([]ModelPriceVersion{version}),
		} {
			encoded, err := json.Marshal(projection)
			if err != nil {
				t.Fatalf("marshal %T: %v", projection, err)
			}
			if strings.Contains(string(encoded), "cache_creation_1h") || strings.Contains(string(encoded), "cache_creation_5m") {
				t.Fatalf("%T exposed retired cache price: %s", projection, encoded)
			}
		}
	}
	if audit.NonFiniteFields["cache_creation_1h_usd_per_million"] != "+Inf" || audit.NonFiniteFields["input_usd_per_million"] != "+Inf" || audit.CacheCreation1hUSDPerMillion != &retired {
		t.Fatal("API projection mutated historical audit values")
	}
}

func TestRetiredCacheTimePricingJSONIgnoresOldFieldAndValidatesActiveRates(t *testing.T) {
	const legacy = `{"timezone":"UTC","peak_windows":[{"weekdays":[1],"start":"09:00","end":"17:00"}],"offpeak_mode":"explicit","offpeak_multiplier":0.5,"offpeak_rates":{"input_usd_per_million":2,"output_usd_per_million":4,"cache_read_usd_per_million":0.5,"cache_creation_usd_per_million":3,"cache_creation_1h_usd_per_million":"retired invalid value"},"long_context_multiplier":0.25}`
	rule, err := parsePriceTimeRule(sql.NullString{String: legacy, Valid: true})
	if err != nil || rule == nil || rule.OffpeakRates.CacheCreation != 3 {
		t.Fatalf("read old time pricing JSON: %#v / %v", rule, err)
	}
	encoded, err := json.Marshal(rule)
	if err != nil || strings.Contains(string(encoded), "cache_creation_1h") {
		t.Fatalf("time price projection = %s / %v", encoded, err)
	}
	for _, invalid := range []string{
		strings.Replace(legacy, `"cache_creation_usd_per_million":3`, `"cache_creation_usd_per_million":-1`, 1),
		strings.Replace(legacy, `"cache_read_usd_per_million":0.5,`, "", 1),
	} {
		if _, err := parsePriceTimeRule(sql.NullString{String: invalid, Valid: true}); err == nil {
			t.Fatal("invalid active time price was accepted")
		}
	}
	for _, body := range []string{
		`{"provider":"openai","model":"gpt-6.1-sol","cache_creation_1h_usd_per_million":null}`,
		`{"provider":"openai","model":"gpt-6.1-sol","long_context":{"cache_creation_1h_usd_per_million":8}}`,
		`{"provider":"claude","model":"claude-test","time_pricing":` + legacy + `}`,
	} {
		var payload modelPricePayload
		request := httptest.NewRequest("PUT", "/api/model-prices/1", strings.NewReader(body))
		if err := decodeModelPricingPayload(request, &payload); err == nil {
			t.Fatalf("new price request accepted retired cache rate: %s", body)
		}
	}
	var batch priceTimeBatchPayload
	if err := json.Unmarshal([]byte(`{"price_ids":[1],"rule":`+legacy+`}`), &batch); err == nil {
		t.Fatal("batch API accepted a retired off-peak cache rate")
	}
	request := httptest.NewRequest("PUT", "/api/model-prices/deepseek-template", strings.NewReader(legacy))
	if err := decodePriceTimeRulePayload(request, &PriceTimeRule{}); err == nil {
		t.Fatal("template API accepted a retired off-peak cache rate")
	}
}

func TestRetiredCachePriceHTTPRejectsPreciselyWithoutMutations(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	handler := a.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	library, err := a.createPrice(ctx, modelPricePayload{Provider: "openai", Model: "gpt-retired-http", InputUSDPerMillion: 2})
	if err != nil {
		t.Fatal(err)
	}
	model := "deepseek-retired-http"
	id := seedCorrectionTestChannelPrice(t, a, model, 2)
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 3, false)); err != nil {
		t.Fatal(err)
	}
	snapshot := func() any {
		t.Helper()
		prices, err := listPricesWithQueryer(ctx, a.db)
		if err != nil {
			t.Fatal(err)
		}
		versions, err := a.listModelPriceVersionsForPrice(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var template string
		if err := a.db.QueryRowContext(ctx, `SELECT rule FROM model_price_time_templates WHERE name='deepseek'`).Scan(&template); err != nil {
			t.Fatal(err)
		}
		state, err := loadUsageAnalyticsState(ctx, a.db)
		if err != nil {
			t.Fatal(err)
		}
		return struct {
			Prices   []ModelPrice
			Versions []ModelPriceVersion
			Template string
			State    usageAnalyticsState
		}{prices, versions, template, state}
	}
	before := snapshot()
	const retiredRule = `{"timezone":"UTC","peak_windows":[{"weekdays":[1],"start":"09:00","end":"17:00"}],"offpeak_mode":"explicit","offpeak_rates":{"input_usd_per_million":2,"output_usd_per_million":4,"cache_read_usd_per_million":0.5,"cache_creation_usd_per_million":3,"cache_creation_1h_usd_per_million":8},"long_context_multiplier":0.25}`
	libraryBody := func() map[string]any {
		return map[string]any{"provider": "openai", "model": library.Model, "input_usd_per_million": 99, "cache_creation_1h_usd_per_million": 8}
	}
	channelBody := func(correct bool) map[string]any {
		return map[string]any{
			"provider": "codex", "model": model, "price_scope": "channel", "channel_auth_type": "oauth",
			"channel_brand": "codex", "channel_key": "oauth_pool", "billing_unit": "token",
			"input_usd_per_million": 99, "correct_latest_version": correct, "time_pricing": json.RawMessage(retiredRule),
		}
	}
	longContext := map[string]any{
		"threshold_input_tokens": 1000, "input_usd_per_million": 4, "output_usd_per_million": 0,
		"cache_read_usd_per_million": 0, "cache_creation_usd_per_million": 6, "cache_creation_1h_usd_per_million": 8,
	}
	createLongContext := map[string]any{"provider": "openai", "model": "new-retired-http", "input_usd_per_million": 99, "long_context": longContext}
	editLongContext := map[string]any{"provider": "openai", "model": library.Model, "input_usd_per_million": 99, "long_context": longContext}
	for _, test := range []struct {
		name, method, path string
		body               any
	}{
		{"create base", http.MethodPost, "/api/model-prices", map[string]any{"provider": "openai", "model": "new-retired-base-http", "input_usd_per_million": 99, "cache_creation_1h_usd_per_million": 8}},
		{"create long context", http.MethodPost, "/api/model-prices", createLongContext},
		{"update base", http.MethodPut, "/api/model-prices/" + strconv.Itoa(library.ID), libraryBody()},
		{"update long context", http.MethodPut, "/api/model-prices/" + strconv.Itoa(library.ID), editLongContext},
		{"append channel version", http.MethodPut, "/api/model-prices/" + strconv.Itoa(id), channelBody(false)},
		{"correct channel version", http.MethodPut, "/api/model-prices/" + strconv.Itoa(id), channelBody(true)},
		{"batch preview", http.MethodPost, "/api/model-prices/deepseek-template/preview", map[string]any{"price_ids": []int{id}, "rule": json.RawMessage(retiredRule)}},
		{"batch apply", http.MethodPost, "/api/model-prices/deepseek-template/apply", map[string]any{"price_ids": []int{id}, "rule": json.RawMessage(retiredRule)}},
		{"template edit", http.MethodPut, "/api/model-prices/deepseek-template", json.RawMessage(retiredRule)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			var response struct {
				Detail struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v / %s", err, recorder.Body.String())
			}
			if recorder.Code != http.StatusUnprocessableEntity || response.Detail.Code != "validation_error" || response.Detail.Message != "TTL 缓存价格已移除，请使用统一缓存写入价格" {
				t.Fatalf("%s %s returned %d: %s", test.method, test.path, recorder.Code, recorder.Body.String())
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected request changed prices, versions, template, or pricing cache: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestRetiredCachePriceStorageSurvivesEditsAndVersionReads(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	seedQuotaTestNativePrice(t, a, "claude", "retired-cache-auth", "claude-retired-cache", 1)
	if _, err := a.db.ExecContext(ctx, `UPDATE model_prices SET cache_creation_usd_per_million=2, cache_creation_1h_usd_per_million=?, long_context_cache_creation_1h_usd_per_million=?`, math.Inf(1), math.Inf(1)); err != nil {
		t.Fatal(err)
	}
	prices, err := listPricesWithQueryer(ctx, a.db)
	if err != nil || len(prices) != 1 {
		t.Fatalf("load existing price: %#v / %v", prices, err)
	}
	price := prices[0]
	if price.LongContext != nil || price.longContextInvalid || price.PreservedLongContext != nil {
		t.Fatalf("retired-only long-context column configured an active tier: %#v", price)
	}
	payload := modelPricePayload{
		Provider: price.Provider, Model: price.Model, PriceScope: price.PriceScope, ChannelAuthType: price.ChannelAuthType,
		ChannelBrand: price.ChannelBrand, ChannelKey: price.ChannelKey, BillingUnit: modelBillingUnitToken,
		InputUSDPerMillion: 3, CacheCreationUSDPerMillion: 4,
	}
	updated, err := a.updatePrice(ctx, price.ID, payload)
	if err != nil {
		t.Fatalf("ordinary edit with hidden old rates: %v", err)
	}
	var base, long float64
	if err := a.db.QueryRowContext(ctx, `SELECT cache_creation_1h_usd_per_million,long_context_cache_creation_1h_usd_per_million FROM model_prices WHERE id=?`, price.ID).Scan(&base, &long); err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(base, 1) || !math.IsInf(long, 1) || updated.CacheCreationUSDPerMillion != 4 {
		t.Fatalf("active edit lost old storage or active rate: %g / %g / %#v", base, long, updated)
	}
	versions, err := a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history := versions[channelModelPriceKey(modelPriceChannelAuthType(updated), *updated.ChannelBrand, *updated.ChannelKey, updated.Model)]
	if history == nil || len(history.all) == 0 {
		t.Fatal("normal edit did not retain the channel version history")
	}
	for _, version := range history.all {
		if version.Price.LongContext != nil || version.Price.longContextInvalid {
			t.Fatalf("retired-only version column configured an active tier: %#v", version.Price)
		}
	}
	listed, err := a.listModelPriceVersionsForPrice(ctx, price.ID)
	if err != nil || len(listed) == 0 {
		t.Fatalf("list old/new versions: %#v / %v", listed, err)
	}
	encoded, err := json.Marshal(modelPriceVersionsForAPI(listed))
	if err != nil || strings.Contains(string(encoded), "cache_creation_1h") {
		t.Fatalf("version API = %s / %v", encoded, err)
	}
	record := UsageRecord{Provider: &updated.Provider, Model: &updated.Model, Timestamp: time.Now(), CacheCreationTokens: 1000000, CacheCreationTTL: claudeTTLStringPtr("1h")}
	got := calculateRecordCostForMatch(record, &updated, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 4 {
		t.Fatalf("edited legacy price cost = %#v, want ordinary cache-write price 4", got)
	}
}

func TestRetiredCacheLongContextStorageSurvivesFastAndTimeSnapshots(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	for _, test := range []struct {
		name       string
		columns    string
		configured bool
		invalid    bool
	}{
		{name: "no-active-tier"},
		{name: "complete-tier", columns: ",long_context_threshold_tokens=100,long_context_input_usd_per_million=4,long_context_output_usd_per_million=0,long_context_cache_read_usd_per_million=0,long_context_cache_creation_usd_per_million=6", configured: true},
		{name: "partial-tier", columns: ",long_context_threshold_tokens=100,long_context_input_usd_per_million=4", configured: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := "deepseek-retired-snapshot-" + test.name
			id := seedCorrectionTestChannelPrice(t, a, model, 2)
			if _, err := a.db.ExecContext(ctx, `UPDATE model_prices SET cache_creation_usd_per_million=3,cache_creation_1h_usd_per_million=?,long_context_cache_creation_1h_usd_per_million=?`+test.columns+` WHERE id=?`, math.Inf(1), math.Inf(1), id); err != nil {
				t.Fatal(err)
			}
			check := func() ModelPrice {
				t.Helper()
				price, err := getPriceWithQuerier(ctx, a.db, id)
				if err != nil {
					t.Fatal(err)
				}
				if (price.LongContext != nil) != test.configured || price.longContextInvalid != test.invalid || (price.PreservedLongContext != nil) != test.invalid {
					t.Fatalf("retired storage changed active tier: %#v", price)
				}
				if price.CacheCreation1hUSDPerMillion == nil || !math.IsInf(*price.CacheCreation1hUSDPerMillion, 1) || price.retiredLongContextCacheCreation1h == nil || !math.IsInf(*price.retiredLongContextCacheCreation1h, 1) {
					t.Fatalf("snapshot lost historical cache rates: %#v", price)
				}
				var base, long float64
				if err := a.db.QueryRowContext(ctx, `SELECT cache_creation_1h_usd_per_million,long_context_cache_creation_1h_usd_per_million FROM model_prices WHERE id=?`, id).Scan(&base, &long); err != nil || !math.IsInf(base, 1) || !math.IsInf(long, 1) {
					t.Fatalf("persisted historical rates = %g / %g, err=%v", base, long, err)
				}
				return price
			}
			price := check()
			if test.invalid {
				payload := correctionTestPayload(model, 3, false)
				preserve := true
				payload.PreserveInvalidLongContext = &preserve
				if _, err := a.updatePrice(ctx, id, payload); err != nil {
					t.Fatal(err)
				}
			} else if _, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: claudeTTLFloatPtr(2)}); err != nil {
				t.Fatal(err)
			}
			check()
			if _, err := a.applyPriceTimeBatch(ctx, priceTimeBatchPayload{PriceIDs: []int{id}, Rule: testTimeRule()}, true); err != nil {
				t.Fatal(err)
			}
			versions, err := a.loadModelPriceVersions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			selected := effectivePriceFromVersions(price, versions, time.Now())
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := materializePriceSnapshot(ctx, tx, selected); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			check()
			payload := correctionTestPayload(model, 5, true)
			if test.invalid {
				preserve := true
				payload.PreserveInvalidLongContext = &preserve
			} else {
				payload.LongContext = longContextPayloadFromPrice(selected.LongContext)
			}
			if _, err := a.updatePrice(ctx, id, payload); err != nil {
				t.Fatal(err)
			}
			check()
			rows, err := a.db.QueryContext(ctx, `SELECT cache_creation_1h_usd_per_million,long_context_cache_creation_1h_usd_per_million FROM model_price_versions WHERE price_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var base, long float64
				if err := rows.Scan(&base, &long); err != nil || !math.IsInf(base, 1) || !math.IsInf(long, 1) {
					t.Fatalf("version snapshot lost historical rate: %g / %g, err=%v", base, long, err)
				}
				count++
			}
			if err := rows.Err(); err != nil || count < 2 {
				t.Fatalf("version snapshots: count=%d err=%v", count, err)
			}
		})
	}
}

func TestRetiredCachePriceSyncAndConflictCopiesIgnoreHiddenValues(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_prices(provider,model,input_usd_per_million,cache_creation_1h_usd_per_million,long_context_cache_creation_1h_usd_per_million,source,updated_at) VALUES('openai','gpt-5.5',2,?,?,'litellm',CURRENT_TIMESTAMP)`, math.Inf(1), math.Inf(1)); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{"gpt-5.5": map[string]any{"litellm_provider": "openai", "input_cost_per_token": 0.000003}}
	if _, err := a.syncLiteLLMPricesSelected(ctx, "fixture", raw, []string{"gpt-5.5"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncLiteLLMPrices(ctx, "fixture", raw); err != nil {
		t.Fatal(err)
	}
	prices, err := a.listPrices(ctx)
	if err != nil || len(prices) != 1 {
		t.Fatalf("synced prices: %#v / %v", prices, err)
	}
	price := prices[0]
	if price.PriorityMultiplier == nil || *price.PriorityMultiplier != 2.5 || price.LongContext != nil || price.longContextInvalid || price.CacheCreation1hUSDPerMillion == nil || !math.IsInf(*price.CacheCreation1hUSDPerMillion, 1) {
		t.Fatalf("retired rates affected Fast initialization or existing storage: %#v", price)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_price_library_conflicts(original_id,selected_price_id,conflict_reason,provider,model,input_usd_per_million,cache_creation_1h_usd_per_million,long_context_cache_creation_1h_usd_per_million,priority_multiplier,updated_at) VALUES(900001,?,'case_insensitive_library_identity','OpenAI','gpt-5.5',7,?,?,2.5,CURRENT_TIMESTAMP)`, price.ID, math.Inf(1), math.Inf(1)); err != nil {
		t.Fatal(err)
	}
	conflicts, err := a.listModelPriceLibraryConflicts(ctx)
	if err != nil || len(conflicts) != 1 || conflicts[0].Price.LongContext != nil || conflicts[0].ArchivedLongContext != nil {
		t.Fatalf("retired-only conflict configured a tier: %#v / %v", conflicts, err)
	}
	replaced, err := a.replaceActiveModelPriceLibraryConflict(ctx, 900001)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.InputUSDPerMillion != 7 || replaced.CacheCreation1hUSDPerMillion == nil || !math.IsInf(*replaced.CacheCreation1hUSDPerMillion, 1) {
		t.Fatalf("replacement lost active price or historical values: %#v", replaced)
	}
	promoted, err := a.promoteModelPriceLibraryConflict(ctx, 900001, modelPriceLibraryConflictPromotePayload{Provider: "codex", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if promoted.InputUSDPerMillion != 3 || promoted.LongContext != nil || promoted.CacheCreation1hUSDPerMillion == nil || !math.IsInf(*promoted.CacheCreation1hUSDPerMillion, 1) {
		t.Fatalf("promotion lost active price or historical values: %#v", promoted)
	}
}

func claudeTTLFloatPtr(value float64) *float64 { return &value }
func claudeTTLIntPtr(value int) *int           { return &value }
func claudeTTLInt64Ptr(value int64) *int64     { return &value }
func claudeTTLStringPtr(value string) *string  { return &value }
