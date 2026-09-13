package app

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestHistoricalRequestBeforeFirstPriceIsChargedWhenCollectedLater(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	model := "text-job"
	id := seedCorrectionTestChannelPrice(t, a, model, 0)
	requestUSD := 0.04
	payload := correctionTestPayload(model, 0, false)
	payload.BillingUnit, payload.RequestUSD = modelBillingUnitRequest, &requestUSD
	if _, err := a.updatePrice(ctx, id, payload); err != nil {
		t.Fatal(err)
	}
	versions, err := a.listModelPriceVersionsForPrice(ctx, id)
	if err != nil || len(versions) != 1 || versions[0].Baseline {
		t.Fatalf("first saved request price versions = %#v, err=%v", versions, err)
	}
	userID := seedQuotaTestUser(t, a, "member")
	apiKey, lifetime := "sk-pre-version-request", 5.0
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"api_key": apiKey, "provider": "codex", "model": model, "auth_type": "oauth",
		"timestamp": dbTime(versions[0].EffectiveAt.Add(-time.Microsecond)), "failed": false, "request_id": "pre-version-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, created, err := a.saveUsageMessage(ctx, raw, pricing)
	if err != nil || !created {
		t.Fatalf("save delayed usage created=%v err=%v", created, err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != requestUSD || unpriced {
		t.Errorf("delayed request cost/unpriced = %v/%v, want %v/false", cost, unpriced, requestUSD)
	}
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != lifetime-requestUSD || user.QuotaUnpricedRecords != 0 {
		t.Errorf("quota = %#v, want request charge and no unpriced records", user)
	}
	var amount float64
	var unpriced bool
	if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != requestUSD || unpriced {
		t.Errorf("persisted charge = %v/%v, want %v/false", amount, unpriced, requestUSD)
	}
}

func TestFirstChannelPriceRepricesHistoryWithoutRewritingQuotaCharges(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	userID := seedQuotaTestUser(t, a, "member")
	apiKey, lifetime := "sk-first-channel-price", 5.0
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := "first-channel-price"
	message := map[string]any{
		"api_key": apiKey, "provider": "codex", "model": model, "auth_type": "oauth",
		"timestamp": dbTime(time.Now().Add(-time.Hour)), "input_tokens": 1000000, "request_id": "before-first-price",
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	record, created, err := a.saveUsageMessage(ctx, raw, pricing)
	if err != nil || !created {
		t.Fatalf("save initial usage created=%v err=%v", created, err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
		t.Fatalf("usage before pricing = %v/%v, want 0/true", cost, unpriced)
	}

	id := seedCorrectionTestChannelPrice(t, a, model, 2)
	for _, input := range []float64{2, 3} {
		if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, input, false)); err != nil {
			t.Fatal(err)
		}
		pricing, err = a.billingPriceIndexWithoutSelectors(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cost, unpriced := recordCostWithBilling(record, pricing); cost != 2 || unpriced {
			t.Errorf("historical cost after saving %v = %v/%v, want first price 2/false", input, cost, unpriced)
		}
		if err := a.applyQuotaCharge(ctx, record, pricing); err != nil {
			t.Fatal(err)
		}
		var amount float64
		var unpriced bool
		if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&amount, &unpriced); err != nil {
			t.Fatal(err)
		}
		if amount != 0 || !unpriced {
			t.Errorf("settled unpriced charge changed after saving %v: %v/%v", input, amount, unpriced)
		}
	}
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != lifetime || user.QuotaUnpricedRecords != 1 {
		t.Errorf("settled quota changed after repricing: %#v", user)
	}

	// A delayed request has no settled charge yet and uses the first price,
	// even when collection happens after the second price was saved.
	message["request_id"] = "delayed-before-first-price"
	raw, err = json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	delayed, created, err := a.saveUsageMessage(ctx, raw, pricing)
	if err != nil || !created {
		t.Fatalf("save delayed usage created=%v err=%v", created, err)
	}
	var amount float64
	var unpriced bool
	if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, delayed.ID).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 2 || unpriced {
		t.Errorf("delayed charge = %v/%v, want first price 2/false", amount, unpriced)
	}
	user, err = a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != lifetime-2 || user.QuotaUnpricedRecords != 1 {
		t.Errorf("quota after delayed collection = %#v, want only the new charge deducted", user)
	}

	if err := a.deletePrice(ctx, id); err != nil {
		t.Fatal(err)
	}
	recreatedID := seedCorrectionTestChannelPrice(t, a, model, 4)
	if _, err := a.updatePrice(ctx, recreatedID, correctionTestPayload(model, 4, false)); err != nil {
		t.Fatal(err)
	}
	pricing, err = a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
		t.Errorf("recreated price crossed the lifecycle boundary: %v/%v, want 0/true", cost, unpriced)
	}
}

func TestModelPriceVersionsRoutePreservesNonFiniteLongContextAudit(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	model := "gpt-non-finite-history"
	id := seedCorrectionTestChannelPrice(t, a, model, 1)
	at := time.Now().Add(-time.Hour)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (
		price_id, provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, long_context_threshold_tokens, long_context_input_usd_per_million,
		long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
		long_context_cache_creation_usd_per_million, effective_at, baseline
	) VALUES (?, 'codex', ?, 'oauth', 'codex', 'oauth_pool', 1, 0, 0, 0, 'token', 1000, ?, 2, 0, 0, ?, 1)`,
		id, model, math.Inf(1), dbTime(at)); err != nil {
		t.Fatal(err)
	}
	payload := correctionTestPayload(model, 1, false)
	payload.LongContext = longContextPayloadFromPrice(&ModelPriceLongContext{ThresholdInputTokens: 1000, InputUSDPerMillion: 2, OutputUSDPerMillion: 3})
	if _, err := a.updatePrice(ctx, id, payload); err != nil {
		t.Fatal(err)
	}
	handler := a.Routes()
	admin := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin", "password": "test-password", "nickname": "Admin",
	}, nil, nil)
	var versions []ModelPriceVersion
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/"+strconv.Itoa(id)+"/versions", nil, admin, &versions)
	if len(versions) != 2 || !versions[0].Baseline || versions[0].LongContext != nil || versions[1].LongContext == nil {
		t.Fatalf("expected readable invalid baseline and valid current version: %#v", versions)
	}
	audit := versions[0].PreservedLongContext
	if audit == nil || audit.InputUSDPerMillion != nil || audit.NonFiniteFields["input_usd_per_million"] != "+Inf" ||
		audit.ThresholdInputTokens == nil || *audit.ThresholdInputTokens != 1000 || audit.OutputUSDPerMillion == nil || *audit.OutputUSDPerMillion != 2 ||
		audit.CacheReadUSDPerMillion == nil || *audit.CacheReadUSDPerMillion != 0 {
		t.Fatalf("non-finite audit lost its original value or finite fields: %#v", audit)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := channelModelPriceKey(modelPriceChannelAuthTypeOAuth, "codex", "oauth_pool", model)
	old := pricing.Versions[key].all[0].Price
	if !old.longContextInvalid || old.PreservedLongContext == nil || old.PreservedLongContext.InputUSDPerMillion == nil || !math.IsInf(*old.PreservedLongContext.InputUSDPerMillion, 1) {
		t.Fatalf("history read changed the raw invalid snapshot: %#v", old)
	}
	record := UsageRecord{Model: &model, Provider: testStringPtr("codex"), Auth: testStringPtr("oauth"), Timestamp: at.Add(time.Minute), InputTokens: 1001}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
		t.Fatalf("historical invalid tier cost/unpriced = %v/%v, want 0/true", cost, unpriced)
	}
}

func TestLongContextAuditProjectionsPreserveNonFiniteValues(t *testing.T) {
	threshold := int64(1000)
	positiveInfinity, negativeInfinity, notANumber := math.Inf(1), math.Inf(-1), math.NaN()
	raw := &ModelPriceLibraryConflictLongContext{
		ThresholdInputTokens: &threshold, InputUSDPerMillion: &positiveInfinity, OutputUSDPerMillion: &notANumber,
		CacheReadUSDPerMillion: &negativeInfinity, CacheCreationUSDPerMillion: &positiveInfinity,
	}
	price := modelPriceForAPI(ModelPrice{PreservedLongContext: raw})
	version := modelPriceVersionForAPI(ModelPriceVersion{PreservedLongContext: raw})
	conflicts := modelPriceLibraryConflictsForAPI([]ModelPriceLibraryConflict{{ArchivedLongContext: raw}})
	for _, tc := range []struct {
		name  string
		value any
		audit *ModelPriceLibraryConflictLongContext
	}{
		{"price", price, price.PreservedLongContext},
		{"version", version, version.PreservedLongContext},
		{"archived conflict", conflicts, conflicts[0].ArchivedLongContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := json.Marshal(apiJSONValue(tc.value)); err != nil {
				t.Fatalf("audit response is not JSON-safe: %v", err)
			}
			if tc.audit == nil || tc.audit == raw || tc.audit.InputUSDPerMillion != nil || tc.audit.OutputUSDPerMillion != nil ||
				tc.audit.CacheReadUSDPerMillion != nil || tc.audit.CacheCreationUSDPerMillion != nil {
				t.Fatalf("projection must copy and replace only non-finite amounts: %#v", tc.audit)
			}
			want := map[string]string{"input_usd_per_million": "+Inf", "output_usd_per_million": "NaN",
				"cache_read_usd_per_million": "-Inf", "cache_creation_usd_per_million": "+Inf"}
			if len(tc.audit.NonFiniteFields) != len(want) {
				t.Fatalf("non-finite audit fields = %#v, want %#v", tc.audit.NonFiniteFields, want)
			}
			for field, value := range want {
				if tc.audit.NonFiniteFields[field] != value {
					t.Errorf("audit %s = %q, want %q", field, tc.audit.NonFiniteFields[field], value)
				}
			}
			repeated := modelPriceLongContextAuditForAPI(tc.audit)
			if repeated.NonFiniteFields["input_usd_per_million"] != "+Inf" {
				t.Fatal("repeated projection erased the audit metadata")
			}
			repeated.NonFiniteFields["input_usd_per_million"] = "changed"
			if tc.audit.NonFiniteFields["input_usd_per_million"] != "+Inf" {
				t.Fatal("projection shares its mutable audit metadata with the source")
			}
		})
	}
	if !math.IsInf(*raw.InputUSDPerMillion, 1) || !math.IsNaN(*raw.OutputUSDPerMillion) ||
		!math.IsInf(*raw.CacheReadUSDPerMillion, -1) || !math.IsInf(*raw.CacheCreationUSDPerMillion, 1) || len(raw.NonFiniteFields) != 0 {
		t.Fatalf("response projection changed raw audit values: %#v", raw)
	}
	finite := 2.0
	partial := modelPriceLongContextAuditForAPI(&ModelPriceLibraryConflictLongContext{InputUSDPerMillion: &finite})
	if partial.InputUSDPerMillion == nil || *partial.InputUSDPerMillion != finite || partial.OutputUSDPerMillion != nil {
		t.Fatalf("finite amounts or original NULL changed: %#v", partial)
	}
	if _, exists := apiJSONValue(partial).(map[string]any)["non_finite_fields"]; exists {
		t.Fatal("finite/NULL audit tuple must not acquire non-finite metadata")
	}
}

func TestResolveVersionedPriceUsesRequestTimestamp(t *testing.T) {
	brand, key := "openai_compatibility", "k"
	old := ModelPrice{PriceScope: modelPriceScopeChannel, Model: "gpt", ChannelBrand: &brand, ChannelKey: &key, InputUSDPerMillion: 0.09}
	newer := old
	newer.InputUSDPerMillion = 0.11
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	idx := modelPriceVersionIndex{}
	mapKey := channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, key, old.Model)
	idx.add(mapKey, modelPriceVersion{Price: old, EffectiveAt: at.Add(-time.Hour)})
	idx.add(mapKey, modelPriceVersion{Price: newer, EffectiveAt: at})
	before := resolveVersionedPrice(UsageRecord{Timestamp: at.Add(-time.Nanosecond)}, &newer, idx)
	if before == nil || before.InputUSDPerMillion != 0.09 {
		t.Fatalf("before boundary price = %#v", before)
	}
	after := resolveVersionedPrice(UsageRecord{Timestamp: at.Add(time.Nanosecond)}, &newer, idx)
	if after == nil || after.InputUSDPerMillion != 0.11 {
		t.Fatalf("after boundary price = %#v", after)
	}
	delayed := resolveVersionedPrice(UsageRecord{Timestamp: at.Add(-time.Minute)}, &newer, idx)
	if delayed == nil || delayed.InputUSDPerMillion != 0.09 {
		t.Fatalf("delayed ingestion price = %#v", delayed)
	}
}

func TestResolveVersionedPriceDoesNotReusePriorPriceLifecycle(t *testing.T) {
	brand, key := "gemini", "auth.json"
	current := ModelPrice{ID: 22, PriceScope: modelPriceScopeChannel, Model: "gemini-2.5-pro", ChannelBrand: &brand, ChannelKey: &key, InputUSDPerMillion: 0.13}
	old := current
	old.ID = 11
	old.InputUSDPerMillion = 0.09
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	mapKey := channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, key, current.Model)
	idx := modelPriceVersionIndex{}
	idx.add(mapKey, modelPriceVersion{PriceID: int64(old.ID), Price: old, EffectiveAt: at.Add(-time.Hour), Baseline: true})
	resolved := resolveVersionedPrice(UsageRecord{Timestamp: at}, &current, idx)
	if resolved == nil || resolved.ID != current.ID || resolved.InputUSDPerMillion != current.InputUSDPerMillion {
		t.Fatalf("recreated price inherited prior lifecycle: %#v", resolved)
	}
}

func TestLoadModelPriceVersionsFailsClosedForPartialLongContext(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	brand, key, model := "gemini", "partial.json", "gemini-partial"
	result, err := a.db.Exec(`INSERT INTO model_prices (
		provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, source, updated_at) VALUES ('gemini', ?, 'channel', 'apikey', ?, ?, 1, 1, 1, 1, 'token', 'manual', ?)`,
		model, brand, key, dbTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	priceID, _ := result.LastInsertId()
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (
		price_id, provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, long_context_threshold_tokens, long_context_input_usd_per_million, effective_at, baseline)
		VALUES (?, 'gemini', ?, 'apikey', ?, ?, 1, 1, 1, 1, 'token', 1000, 2, ?, 1)`,
		priceID, model, brand, key, dbTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	versions, err := a.loadModelPriceVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := versions[channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, key, model)].all
	if len(history) != 1 {
		t.Fatalf("history = %#v", history)
	}
	if history[0].Price.LongContext == nil || history[0].Price.PreservedLongContext == nil || !history[0].Price.longContextInvalid {
		t.Fatalf("partial long context was not rejected: %#v", history[0].Price)
	}
	for _, correct := range []bool{false, true} {
		tx, err := a.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if correct {
			err = a.correctLatestModelPriceVersion(context.Background(), tx, history[0].Price, time.Now())
		} else {
			err = a.appendModelPriceVersion(context.Background(), tx, history[0].Price, time.Now(), false)
		}
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		loaded, err := a.loadModelPriceVersions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		chain := loaded[channelModelPriceKey(modelPriceChannelAuthTypeAPIKey, brand, key, model)].all
		latest := chain[len(chain)-1].Price
		if !latest.longContextInvalid || latest.PreservedLongContext == nil || latest.PreservedLongContext.InputUSDPerMillion == nil || *latest.PreservedLongContext.InputUSDPerMillion != 2 {
			t.Fatalf("snapshot write lost partial tier (correction=%v): %#v", correct, latest)
		}
	}
}

func TestHistoricalVersionInvalidFastMultiplierFailsClosedForUsageAndQuota(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ctx := context.Background()
	model := "gpt-versioned-invalid-fast"
	priceID := seedCorrectionTestChannelPrice(t, a, model, 1)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (
		price_id, provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, priority_multiplier, effective_at, baseline
	) VALUES (?, 'codex', ?, 'oauth', 'codex', 'oauth_pool', 1, 0, 0, 0, 'token', ?, ?, 1)`,
		priceID, model, math.Inf(1), dbTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	userID := seedQuotaTestUser(t, a, "member")
	apiKey := "sk-versioned-invalid-fast"
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	lifetime := 5.0
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := channelModelPriceKey(modelPriceChannelAuthTypeOAuth, "codex", modelPriceOAuthPoolChannelKey, model)
	history := pricing.Versions[key].all
	if len(history) != 1 || history[0].Price.PriorityMultiplier == nil || !math.IsInf(*history[0].Price.PriorityMultiplier, 1) {
		t.Fatalf("loaded version multiplier = %#v, want retained +Inf", history)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"codex","model":"` + model + `","auth_type":"oauth","service_tier":"priority","input_tokens":1000000,"request_id":"versioned-invalid-fast"}`
	record, created, err := a.saveUsageMessage(ctx, []byte(raw), pricing)
	if err != nil || !created {
		t.Fatalf("save usage created=%v err=%v", created, err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
		t.Fatalf("versioned Fast cost = %v/%v, want 0/true", cost, unpriced)
	}
	if breakdown := versionedCostBreakdown(record, pricing, true); !breakdown.Unpriced || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
		t.Fatalf("versioned Fast breakdown = %#v, want unpriced", breakdown)
	}
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != lifetime || user.QuotaUnpricedRecords != 1 {
		t.Fatalf("quota after invalid historical Fast = %#v, want unchanged balance and one unpriced record", user)
	}
	var amount float64
	var unpriced bool
	if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 0 || !unpriced {
		t.Fatalf("historical Fast quota charge = %v/%v, want 0/true", amount, unpriced)
	}
}

func TestHistoricalVersionPartialLongContextFailsClosedForUsageAndQuota(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ctx := context.Background()
	model := "gpt-versioned-partial-long-context"
	priceID := seedCorrectionTestChannelPrice(t, a, model, 1)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (
		price_id, provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, long_context_threshold_tokens, long_context_input_usd_per_million, effective_at, baseline
	) VALUES (?, 'codex', ?, 'oauth', 'codex', 'oauth_pool', 1, 0, 0, 0, 'token', 1000, 2, ?, 1)`,
		priceID, model, dbTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	userID := seedQuotaTestUser(t, a, "member")
	apiKey := "sk-versioned-partial-long-context"
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	lifetime := 5.0
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := channelModelPriceKey(modelPriceChannelAuthTypeOAuth, "codex", modelPriceOAuthPoolChannelKey, model)
	history := pricing.Versions[key].all
	if len(history) != 1 || history[0].Price.LongContext == nil || !history[0].Price.longContextInvalid {
		t.Fatalf("loaded partial historical tier = %#v, want retained invalid candidate", history)
	}

	raw := `{"api_key":"` + apiKey + `","provider":"codex","model":"` + model + `","auth_type":"oauth","input_tokens":1001,"request_id":"versioned-partial-long-context"}`
	record, created, err := a.saveUsageMessage(ctx, []byte(raw), pricing)
	if err != nil || !created {
		t.Fatalf("save usage created=%v err=%v", created, err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); cost != 0 || !unpriced {
		t.Fatalf("versioned partial long-context cost = %v/%v, want 0/true", cost, unpriced)
	}
	if breakdown := versionedCostBreakdown(record, pricing, true); !breakdown.Unpriced || breakdown.LongContextApplied || breakdown.TotalUSD != 0 || len(breakdown.Items) != 0 {
		t.Fatalf("versioned partial long-context breakdown = %#v, want unpriced", breakdown)
	}
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != lifetime || user.QuotaUnpricedRecords != 1 {
		t.Fatalf("quota after partial historical tier = %#v, want unchanged balance and one unpriced record", user)
	}
	var amount float64
	var unpriced bool
	if err := a.db.QueryRow(`SELECT amount_usd, unpriced FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&amount, &unpriced); err != nil {
		t.Fatal(err)
	}
	if amount != 0 || !unpriced {
		t.Fatalf("historical partial tier quota charge = %v/%v, want 0/true", amount, unpriced)
	}
}

func TestModelPriceVersionsRoutePreservesPartialLongContextAndSanitizesFast(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	model := "gpt-version-history-audit"
	priceID := seedCorrectionTestChannelPrice(t, a, model, 1)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (
		price_id, provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, priority_multiplier, long_context_threshold_tokens, long_context_input_usd_per_million, effective_at, baseline
	) VALUES (?, 'codex', ?, 'oauth', 'codex', 'oauth_pool', 1, 0, 0, 0, 'token', ?, 1000, 2, ?, 1)`,
		priceID, model, math.Inf(1), dbTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	handler := a.Routes()
	adminCookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	var versions []ModelPriceVersion
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/"+strconv.Itoa(priceID)+"/versions", nil, adminCookies, &versions)
	if len(versions) != 1 {
		t.Fatalf("versions = %#v, want one", versions)
	}
	version := versions[0]
	if version.PriorityMultiplier != nil {
		t.Fatalf("API historical Fast multiplier = %v, want JSON-safe null", *version.PriorityMultiplier)
	}
	if version.LongContext != nil {
		t.Fatalf("API historical long context = %#v, want null for partial tier", version.LongContext)
	}
	preserved := version.PreservedLongContext
	if preserved == nil || preserved.ThresholdInputTokens == nil || *preserved.ThresholdInputTokens != 1000 || preserved.InputUSDPerMillion == nil || *preserved.InputUSDPerMillion != 2 || preserved.OutputUSDPerMillion != nil || preserved.CacheReadUSDPerMillion != nil || preserved.CacheCreationUSDPerMillion != nil {
		t.Fatalf("API preserved historical long context = %#v, want exact partial snapshot", preserved)
	}
}

func testChannelPriceVersion(t *testing.T, input float64) ModelPrice {
	t.Helper()
	brand, key := "gemini", "auth.json"
	return ModelPrice{
		Provider: "gemini", Model: "gemini-2.5-pro", PriceScope: modelPriceScopeChannel,
		ChannelAuthType: testStringPtr(modelPriceChannelAuthTypeAPIKey), ChannelBrand: &brand, ChannelKey: &key,
		InputUSDPerMillion: input, BillingUnit: modelBillingUnitToken,
	}
}

func testStringPtr(value string) *string { return &value }

func insertTestChannelPriceVersion(t *testing.T, a *App, price ModelPrice, effectiveAt time.Time) int64 {
	t.Helper()
	result, err := a.db.Exec(`INSERT INTO model_price_versions (
		provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, effective_at, baseline) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, 0)`,
		price.Provider, price.Model, nullableStringArg(price.ChannelAuthType), nullableStringArg(price.ChannelBrand), nullableStringArg(price.ChannelKey),
		price.InputUSDPerMillion, price.BillingUnit, dbTime(effectiveAt))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAppendModelPriceVersionAlwaysPreservesPreviousVersion(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	initialAt := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	initial := testChannelPriceVersion(t, 0.09)
	id := insertTestChannelPriceVersion(t, a, initial, initialAt)
	corrected := initial
	corrected.InputUSDPerMillion = 0.11
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.appendModelPriceVersion(ctx, tx, corrected, initialAt.Add(time.Hour), false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM model_price_versions WHERE channel_key = ?`, "auth.json").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("correction created %d versions, want 2", count)
	}
	rows, err := a.db.Query(`SELECT id, input_usd_per_million FROM model_price_versions WHERE channel_key = ? ORDER BY effective_at`, "auth.json")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var inputs []float64
	for rows.Next() {
		var versionID int64
		var input float64
		if err := rows.Scan(&versionID, &input); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, versionID)
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != id || inputs[0] != 0.09 || ids[1] == id || inputs[1] != 0.11 {
		t.Fatalf("correction versions = ids %v inputs %v, want preserved old and appended new", ids, inputs)
	}
}

type priceVersionRow struct {
	id          int64
	input       float64
	effectiveAt string
	baseline    int
}

func readTestPriceVersions(t *testing.T, a *App, channelKey string) []priceVersionRow {
	t.Helper()
	rows, err := a.db.Query(`SELECT id, input_usd_per_million, CAST(effective_at AS TEXT), baseline FROM model_price_versions WHERE channel_key = ? ORDER BY effective_at, id`, channelKey)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []priceVersionRow
	for rows.Next() {
		var row priceVersionRow
		if err := rows.Scan(&row.id, &row.input, &row.effectiveAt, &row.baseline); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func seedCorrectionTestChannelPrice(t *testing.T, a *App, model string, input float64) int {
	t.Helper()
	result, err := a.db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, billing_unit, source, updated_at
		) VALUES ('codex', ?, 'channel', 'oauth', 'codex', 'oauth_pool', ?, 0, 0, 0, 'token', 'manual', ?)
	`, model, input, dbTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return int(id)
}

func correctionTestPayload(model string, input float64, correct bool) modelPricePayload {
	auth, brand, key := modelPriceChannelAuthTypeOAuth, "codex", "oauth_pool"
	return modelPricePayload{
		Provider: "codex", Model: model, PriceScope: modelPriceScopeChannel,
		ChannelAuthType: &auth, ChannelBrand: &brand, ChannelKey: &key,
		BillingUnit: modelBillingUnitToken, InputUSDPerMillion: input,
		CorrectLatestVersion: correct,
	}
}

func TestUpdatePriorityMultiplierCorrectLatestVersion(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "gpt-fast-correction-test"
	id := seedCorrectionTestChannelPrice(t, a, model, 0.5)
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 0.5, false)); err != nil {
		t.Fatal(err)
	}
	if got := readTestPriceVersions(t, a, "oauth_pool"); len(got) != 1 {
		t.Fatalf("seed versions = %#v", got)
	}
	oldUpdatedAt := time.Date(2000, 1, 2, 3, 4, 5, 123456000, time.UTC)
	resetUpdatedAt := func() {
		t.Helper()
		if _, err := a.db.ExecContext(ctx, `UPDATE model_prices SET updated_at=? WHERE id=?`, dbTime(oldUpdatedAt), id); err != nil {
			t.Fatal(err)
		}
	}
	assertUpdatedAt := func(stage string, response ModelPrice) {
		t.Helper()
		stored, err := getPriceWithQuerier(ctx, a.db, id)
		if err != nil {
			t.Fatal(err)
		}
		if !stored.UpdatedAt.After(oldUpdatedAt) {
			t.Errorf("%s did not update the stored timestamp: %v", stage, stored.UpdatedAt)
		}
		if !response.UpdatedAt.Equal(stored.UpdatedAt) {
			t.Errorf("%s returned updated_at %v, want stored %v", stage, response.UpdatedAt, stored.UpdatedAt)
		}
	}

	// Default path appends.
	resetUpdatedAt()
	two := 2.0
	appendedPrice, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: &two})
	if err != nil {
		t.Fatal(err)
	}
	assertUpdatedAt("append", appendedPrice)
	appended := readTestPriceVersions(t, a, "oauth_pool")
	if len(appended) != 2 {
		t.Fatalf("append path versions = %#v", appended)
	}

	// Correction overwrites the newest version in place and keeps its effective_at.
	resetUpdatedAt()
	three := 3.0
	updated, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: &three, CorrectLatestVersion: true})
	if err != nil {
		t.Fatal(err)
	}
	assertUpdatedAt("correction", updated)
	corrected := readTestPriceVersions(t, a, "oauth_pool")
	if len(corrected) != 2 || corrected[1].id != appended[1].id || corrected[1].effectiveAt != appended[1].effectiveAt {
		t.Fatalf("correction changed version identity: %#v -> %#v", appended, corrected)
	}
	var storedMultiplier float64
	if err := a.db.QueryRow(`SELECT priority_multiplier FROM model_price_versions WHERE id = ?`, corrected[1].id).Scan(&storedMultiplier); err != nil {
		t.Fatal(err)
	}
	if storedMultiplier != 3 || updated.PriorityMultiplier == nil || *updated.PriorityMultiplier != 3 {
		t.Fatalf("latest version multiplier = %v, returned = %v, want 3", storedMultiplier, updated.PriorityMultiplier)
	}

	// Correcting a scheduled version must not alter the live price.
	future := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	scheduled := updated
	scheduled.InputUSDPerMillion = 9
	if err := a.appendModelPriceVersion(ctx, tx, scheduled, future, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	resetUpdatedAt()
	four := 4.0
	live, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: &four, CorrectLatestVersion: true})
	if err != nil {
		t.Fatal(err)
	}
	assertUpdatedAt("scheduled correction", live)
	if live.InputUSDPerMillion != 0.5 || live.PriorityMultiplier == nil || *live.PriorityMultiplier != 3 {
		t.Fatalf("live price after correcting scheduled version = %#v, want input 0.5 and multiplier 3", live)
	}
	rows := readTestPriceVersions(t, a, "oauth_pool")
	if len(rows) != 3 || rows[2].input != 9 {
		t.Fatalf("scheduled version lost after correction: %#v", rows)
	}
	if err := a.db.QueryRow(`SELECT priority_multiplier FROM model_price_versions WHERE id = ?`, rows[2].id).Scan(&storedMultiplier); err != nil {
		t.Fatal(err)
	}
	if storedMultiplier != 4 {
		t.Fatalf("scheduled version multiplier = %v, want 4", storedMultiplier)
	}
}

func TestUpdatePriceCorrectLatestVersionOverwritesInPlace(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "gpt-correction-test"
	id := seedCorrectionTestChannelPrice(t, a, model, 0.09)
	baselineAt := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, effective_at, baseline) VALUES ('codex', ?, 'oauth', 'codex', 'oauth_pool', 0.09, 0, 0, 0, 'token', ?, 1)`,
		model, dbTime(baselineAt)); err != nil {
		t.Fatal(err)
	}
	before := readTestPriceVersions(t, a, "oauth_pool")
	if len(before) != 1 {
		t.Fatalf("seed versions = %#v", before)
	}

	// Default path appends a new version.
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 0.5, false)); err != nil {
		t.Fatal(err)
	}
	appended := readTestPriceVersions(t, a, "oauth_pool")
	if len(appended) != 2 || appended[0].id != before[0].id || appended[0].input != 0.09 || appended[1].input != 0.5 || appended[1].baseline != 0 {
		t.Fatalf("append path versions = %#v", appended)
	}

	// Correction path overwrites the newest version and keeps effective_at/baseline.
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 0.11, true)); err != nil {
		t.Fatal(err)
	}
	corrected := readTestPriceVersions(t, a, "oauth_pool")
	if len(corrected) != 2 {
		t.Fatalf("correction changed version count: %#v", corrected)
	}
	if corrected[0] != appended[0] {
		t.Fatalf("correction touched older version: %#v -> %#v", appended[0], corrected[0])
	}
	if corrected[1].id != appended[1].id || corrected[1].effectiveAt != appended[1].effectiveAt || corrected[1].baseline != appended[1].baseline || corrected[1].input != 0.11 {
		t.Fatalf("latest version after correction = %#v, want same id/effective_at/baseline with input 0.11", corrected[1])
	}
	stored, err := a.getPrice(ctx, id)
	if err != nil || stored.InputUSDPerMillion != 0.11 {
		t.Fatalf("stored price = %#v %v", stored, err)
	}

	// Correcting a baseline version is allowed as well.
	if _, err := a.db.Exec(`DELETE FROM model_price_versions WHERE id = ?`, corrected[1].id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 0.2, true)); err != nil {
		t.Fatal(err)
	}
	baselineCorrected := readTestPriceVersions(t, a, "oauth_pool")
	if len(baselineCorrected) != 1 || baselineCorrected[0].id != before[0].id || baselineCorrected[0].baseline != 1 || baselineCorrected[0].input != 0.2 || baselineCorrected[0].effectiveAt != before[0].effectiveAt {
		t.Fatalf("baseline correction = %#v", baselineCorrected)
	}

	versions, err := a.listModelPriceVersionsForPrice(ctx, id)
	if err != nil || len(versions) != 1 || !versions[0].Baseline || versions[0].InputUSDPerMillion != 0.2 || versions[0].ID != before[0].id {
		t.Fatalf("listed versions = %#v %v", versions, err)
	}
}

func TestUpdatePriceCorrectLatestVersionFallsBackToAppendWithoutVersions(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "gpt-correction-legacy"
	id := seedCorrectionTestChannelPrice(t, a, model, 0.09)
	if rows := readTestPriceVersions(t, a, "oauth_pool"); len(rows) != 0 {
		t.Fatalf("unexpected seeded versions %#v", rows)
	}
	updated, err := a.updatePrice(ctx, id, correctionTestPayload(model, 0.11, true))
	if err != nil {
		t.Fatal(err)
	}
	if updated.InputUSDPerMillion != 0.11 {
		t.Fatalf("updated = %#v", updated)
	}
	rows := readTestPriceVersions(t, a, "oauth_pool")
	if len(rows) != 1 || rows[0].input != 0.11 || rows[0].baseline != 0 {
		t.Fatalf("fallback versions = %#v, want one appended non-baseline version", rows)
	}
}

func TestUpdatePriceCorrectionReResolvesHistoryWithoutRewritingQuotaCharges(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "gpt-correction-history"
	id := seedCorrectionTestChannelPrice(t, a, model, 2)
	if _, err := a.db.Exec(`INSERT INTO model_price_versions (provider, model, channel_auth_type, channel_brand, channel_key,
		input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
		billing_unit, effective_at, baseline) VALUES ('codex', ?, 'oauth', 'codex', 'oauth_pool', 2, 0, 0, 0, 'token', ?, 1)`,
		model, dbTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	userID := seedQuotaTestUser(t, a, "member")
	apiKey := "sk-correction-history"
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	lifetime := 5.0
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &lifetime}); err != nil {
		t.Fatal(err)
	}
	pricing, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"api_key":"` + apiKey + `","provider":"codex","model":"` + model + `","auth_type":"oauth","auth_index":"acct.json","input_tokens":1000000,"request_id":"correction-history"}`
	record, created, err := a.saveUsageMessage(ctx, []byte(raw), pricing)
	if err != nil || !created {
		t.Fatalf("usage created=%v err=%v", created, err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); unpriced || cost != 2 {
		t.Fatalf("initial cost = %v unpriced=%v, want 2", cost, unpriced)
	}
	var chargedBefore float64
	if err := a.db.QueryRow(`SELECT amount_usd FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&chargedBefore); err != nil {
		t.Fatal(err)
	}
	if chargedBefore != 2 {
		t.Fatalf("charged before = %v, want 2", chargedBefore)
	}

	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 1, true)); err != nil {
		t.Fatal(err)
	}
	pricing, err = a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cost, unpriced := recordCostWithBilling(record, pricing); unpriced || cost != 1 {
		t.Fatalf("corrected historical cost = %v unpriced=%v, want 1", cost, unpriced)
	}
	var chargedAfter float64
	var chargeCount int
	if err := a.db.QueryRow(`SELECT amount_usd, (SELECT COUNT(*) FROM user_quota_charges) FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&chargedAfter, &chargeCount); err != nil {
		t.Fatal(err)
	}
	if chargedAfter != 2 || chargeCount != 1 {
		t.Fatalf("quota charge after correction = %v (count %d), want untouched 2", chargedAfter, chargeCount)
	}

	// Appending (default) instead must leave the historical request on the old version.
	time.Sleep(5 * time.Millisecond) // keep the new version strictly after the record timestamp
	if _, err := a.updatePrice(ctx, id, correctionTestPayload(model, 3, false)); err != nil {
		t.Fatal(err)
	}
	pricing, err = a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cost, _ := recordCostWithBilling(record, pricing); cost != 1 {
		t.Fatalf("historical cost after append = %v, want 1 (previous version)", cost)
	}
}

// latestCookiesByName keeps only the most recently issued cookie per name so a
// rotated session cookie is not shadowed by its stale predecessor.
func latestCookiesByName(cookies []*http.Cookie) []*http.Cookie {
	byName := map[string]int{}
	var result []*http.Cookie
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		if index, ok := byName[cookie.Name]; ok {
			result[index] = cookie
			continue
		}
		byName[cookie.Name] = len(result)
		result = append(result, cookie)
	}
	return result
}

func TestModelPriceVersionsRouteIsAdminOnlyAndReadOnly(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	model := "gpt-versions-route"
	channelID := seedCorrectionTestChannelPrice(t, a, model, 0.09)
	if _, err := a.updatePrice(ctx, channelID, correctionTestPayload(model, 0.11, false)); err != nil {
		t.Fatal(err)
	}
	libraryResult, err := a.db.Exec(`
		INSERT INTO model_prices (provider, model, price_scope, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, billing_unit, source, updated_at)
		VALUES ('OpenAI', ?, 'library', 1, 2, 0, 0, 'token', 'manual', ?)`, model, dbTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := libraryResult.LastInsertId()

	handler := a.Routes()
	channelPath := "/api/model-prices/" + strconv.Itoa(channelID) + "/versions"
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, channelPath, nil, nil, http.StatusUnauthorized)
	adminCookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, adminCookies, nil)
	memberCookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	memberCookies = latestCookiesByName(requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil))
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, channelPath, nil, memberCookies, http.StatusForbidden)

	var versions []ModelPriceVersion
	requestJSONForPricingTest(t, handler, http.MethodGet, channelPath, nil, adminCookies, &versions)
	if len(versions) != 1 || versions[0].InputUSDPerMillion != 0.11 || versions[0].Baseline {
		t.Fatalf("channel versions = %#v, want one appended non-baseline version", versions)
	}
	var libraryVersions []ModelPriceVersion
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/"+strconv.Itoa(int(libraryID))+"/versions", nil, adminCookies, &libraryVersions)
	if len(libraryVersions) != 0 {
		t.Fatalf("library price must have no versions, got %#v", libraryVersions)
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, "/api/model-prices/999999/versions", nil, adminCookies, http.StatusNotFound)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, channelPath, map[string]any{}, adminCookies, http.StatusMethodNotAllowed)
}

func TestAppendModelPriceVersionAppendsWhenLatestWasReferenced(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	initialAt := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	initial := testChannelPriceVersion(t, 0.09)
	id := insertTestChannelPriceVersion(t, a, initial, initialAt)
	if _, err := a.db.Exec(`INSERT INTO usage_records (created_at, timestamp, model, dedupe_key, raw_json) VALUES (?, ?, ?, ?, ?)`,
		dbTime(initialAt.Add(time.Minute)), dbTime(initialAt.Add(time.Minute)), initial.Model, "price-version-reference", `{}`); err != nil {
		t.Fatal(err)
	}
	corrected := initial
	corrected.InputUSDPerMillion = 0.11
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.appendModelPriceVersion(ctx, tx, corrected, initialAt.Add(time.Hour), false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.Query(`SELECT id, input_usd_per_million FROM model_price_versions WHERE channel_key = ? ORDER BY effective_at`, "auth.json")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var inputs []float64
	for rows.Next() {
		var versionID int64
		var input float64
		if err := rows.Scan(&versionID, &input); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, versionID)
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != id || inputs[0] != 0.09 || ids[1] == id || inputs[1] != 0.11 {
		t.Fatalf("referenced correction versions = ids %v inputs %v, want preserved old and appended new", ids, inputs)
	}
}
