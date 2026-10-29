package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func ultrafastTestFloat(value float64) *float64 { return &value }

func newUltrafastTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func assertUltrafastTestMultipliers(t *testing.T, price ModelPrice, fast, ultra float64) {
	t.Helper()
	if price.PriorityMultiplier == nil || *price.PriorityMultiplier != fast || price.UltrafastMultiplier == nil || *price.UltrafastMultiplier != ultra {
		t.Fatalf("multipliers = %v / %v, want %g / %g", price.PriorityMultiplier, price.UltrafastMultiplier, fast, ultra)
	}
}

func TestUltrafastCostUsesOnlyMatchingTierAndExactModels(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra"} {
		for _, tier := range []string{"ultrafast", "priority", "default", "", "unknown"} {
			for _, multiplier := range []*float64{nil, ultrafastTestFloat(1), ultrafastTestFloat(3), ultrafastTestFloat(0), ultrafastTestFloat(-1), ultrafastTestFloat(math.Inf(1)), ultrafastTestFloat(math.MaxFloat64)} {
				t.Run(fmt.Sprintf("%s/%s/%v", model, tier, multiplier), func(t *testing.T) {
					price := ModelPrice{Provider: "openai", Model: model, BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 2, OutputUSDPerMillion: 4, PriorityMultiplier: ultrafastTestFloat(2), UltrafastMultiplier: multiplier}
					record := UsageRecord{Provider: &price.Provider, Model: &model, ServiceTier: &tier, InputTokens: 1_000_000, OutputTokens: 1_000_000}
					got := calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
					invalid := tier == "ultrafast" && multiplier != nil && !priorityMultiplierProducesRoundableCost(price, *multiplier)
					want := 6.0
					if tier == "priority" {
						want = 12
					} else if tier == "ultrafast" && multiplier != nil && !invalid {
						want *= *multiplier
					}
					if invalid {
						if !got.Unpriced || got.TotalUSD != 0 {
							t.Fatalf("unsafe ultrafast = %#v", got)
						}
						return
					}
					if got.Unpriced || got.TotalUSD != want {
						t.Fatalf("tier cost = %#v, want %g", got, want)
					}
					wantMultiplier := tier == "priority" || (tier == "ultrafast" && multiplier != nil)
					if (got.TierMultiplier != nil) != wantMultiplier {
						t.Fatalf("tier multiplier = %v, applied=%t", got.TierMultiplier, wantMultiplier)
					}
				})
			}
		}
	}
	for _, test := range []struct {
		provider, model, brand string
		want                   bool
	}{
		{"openai", "gpt-6.1-sol", "", true}, {"codex", " models/gpt-6-astra ", "", true},
		{"Vendor A", "gpt-6.1-sol", "openai_compatibility", true}, {"Vendor A", "gpt-6-astra", "codex", true},
		{"anthropic", "gpt-6-astra", "claude", false}, {"openai", "gpt-6.1-sol-mini", "", false}, {"openai", "gpt-6-sol", "", false},
	} {
		price := ModelPrice{Provider: test.provider, Model: test.model, InputUSDPerMillion: 2, UltrafastMultiplier: ultrafastTestFloat(3)}
		if test.brand != "" {
			price.PriceScope, price.ChannelBrand = modelPriceScopeChannel, &test.brand
		}
		if got := modelPriceSupportsUltrafast(price); got != test.want {
			t.Errorf("eligibility %#v = %t, want %t", test, got, test.want)
		}
		if !test.want {
			tier := serviceTierUltrafast
			got := calculateRecordCostForMatch(UsageRecord{Provider: &price.Provider, Model: &test.model, ServiceTier: &tier, InputTokens: 1_000_000}, &price, priceMatchStatusMatched, nil, true)
			if got.Unpriced || got.TotalUSD != 2 || got.TierMultiplier != nil {
				t.Fatalf("unsupported tier changed cost: %#v", got)
			}
		}
	}
}

func TestUltrafastLongContextAndRequestAmounts(t *testing.T) {
	model, tier := "gpt-6-astra", serviceTierUltrafast
	price := ModelPrice{Provider: "openai", Model: model, BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 2, PriorityMultiplier: ultrafastTestFloat(2), UltrafastMultiplier: ultrafastTestFloat(3), LongContext: &ModelPriceLongContext{ThresholdInputTokens: 100, InputUSDPerMillion: 5, OutputUSDPerMillion: 10}}
	got := calculateRecordCostForMatch(UsageRecord{Provider: &price.Provider, Model: &model, ServiceTier: &tier, InputTokens: 1_000_000, OutputTokens: 1_000_000}, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || !got.LongContextApplied || got.TotalUSD != 45 {
		t.Fatalf("long context before ultrafast = %#v", got)
	}
	price.BillingUnit, price.RequestUSD, price.LongContext = modelBillingUnitRequest, ultrafastTestFloat(0.01), nil
	got = calculateRecordCostForMatch(UsageRecord{Provider: &price.Provider, Model: &model, ServiceTier: &tier}, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0.03 {
		t.Fatalf("request ultrafast = %#v", got)
	}
	got = calculateRecordCostForMatch(UsageRecord{Provider: &price.Provider, Model: &model, ServiceTier: &tier, Failed: true}, &price, priceMatchStatusMatched, nil, true)
	if got.Unpriced || got.TotalUSD != 0 {
		t.Fatalf("failed request = %#v", got)
	}
}

func TestUltrafastEligibilityProjectionUsesResolvedPrice(t *testing.T) {
	for _, test := range []struct {
		name, provider, model, brand string
		eligible                     bool
	}{
		{"named compatible", "Vendor A", "gpt-6.1-sol", "openai_compatibility", true},
		{"named codex", "OAuth Pool", "gpt-6-astra", "codex", true},
		{"other model", "openai", "gpt-6-sol", "openai_compatibility", false},
		{"target on ineligible channel", "openai", "gpt-6-astra", "claude", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			price := ModelPrice{Provider: test.provider, Model: test.model, PriceScope: modelPriceScopeChannel, ChannelBrand: &test.brand, InputUSDPerMillion: 2}
			tier := serviceTierUltrafast
			record := UsageRecord{Provider: &test.provider, Model: &test.model, ServiceTier: &tier, InputTokens: 1_000_000}
			breakdown := calculateRecordCostForMatch(record, &price, priceMatchStatusMatched, nil, true)
			if breakdown.UltrafastEligible != test.eligible || breakdown.TierMultiplier != nil || breakdown.Unpriced || breakdown.TotalUSD != 2 {
				t.Fatalf("resolved eligibility/cost = %#v, want %t/base 2", breakdown, test.eligible)
			}
			item := listItemFromRecordWithBreakdown(record, nil, usageRedactionOptions{}, breakdown)
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				CostBreakdown map[string]any `json:"cost_breakdown"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if projected, exists := wire.CostBreakdown["ultrafast_eligible"]; !exists || projected != test.eligible {
				t.Fatalf("usage DTO eligibility = %#v", wire.CostBreakdown)
			}
		})
	}
}

func TestUltrafastAPIIdentityChangesResetOnlyInheritedMultiplier(t *testing.T) {
	for _, test := range []struct {
		name, provider, model string
		includeValue          bool
		value                 any
		want                  *float64
		reject                bool
	}{
		{"same identity omitted", "openai", "gpt-6-astra", false, nil, ultrafastTestFloat(3), false},
		{"same identity null", "openai", "gpt-6-astra", true, nil, ultrafastTestFloat(3), false},
		{"target to other model", "openai", "gpt-6-sol", false, nil, nil, false},
		{"target to eligible model", "openai", "gpt-6.1-sol", false, nil, nil, false},
		{"provider identity changes", "anthropic", "gpt-6-astra", false, nil, nil, false},
		{"explicit new eligible value", "openai", "gpt-6.1-sol", true, 1.00001, ultrafastTestFloat(1.00001), false},
		{"explicit ineligible value", "openai", "gpt-6-sol", true, 3, nil, true},
		{"explicit invalid value", "openai", "gpt-6-astra", true, 0, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, ctx := newUltrafastTestApp(t), context.Background()
			price, err := a.createPrice(ctx, modelPricePayload{Provider: "openai", Model: "gpt-6-astra", InputUSDPerMillion: 2, UltrafastMultiplier: ultrafastTestFloat(3)})
			if err != nil {
				t.Fatal(err)
			}
			price, err = a.updatePriorityMultiplier(ctx, price.ID, priorityMultiplierPayload{PriorityMultiplier: ultrafastTestFloat(4)})
			if err != nil {
				t.Fatal(err)
			}
			handler := a.Routes()
			cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
			payload := map[string]any{"provider": test.provider, "model": test.model, "billing_unit": "token", "input_usd_per_million": 3}
			if test.includeValue {
				payload["ultrafast_multiplier"] = test.value
			}
			path := fmt.Sprintf("/api/model-prices/%d", price.ID)
			if test.reject {
				requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, path, payload, cookies, http.StatusUnprocessableEntity)
				stored, err := a.getPrice(ctx, price.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(price, stored) {
					t.Fatalf("rejected identity edit changed price: %#v -> %#v", price, stored)
				}
				return
			}
			var updated ModelPrice
			requestJSONForPricingTest(t, handler, http.MethodPut, path, payload, cookies, &updated)
			if !reflect.DeepEqual(updated.UltrafastMultiplier, test.want) {
				t.Fatalf("edited identity ultrafast = %v, want %v", updated.UltrafastMultiplier, test.want)
			}
			sameIdentity := test.provider == price.Provider && test.model == price.Model
			if sameIdentity && (updated.PriorityMultiplier == nil || *updated.PriorityMultiplier != 4) {
				t.Fatalf("same identity lost Fast: %#v", updated)
			}
			if !sameIdentity && updated.PriorityMultiplier != nil {
				t.Fatalf("new identity inherited Fast: %#v", updated)
			}
		})
	}
}

func TestUltrafastAPISingleFieldsAtomicValidationAndConcurrentBaseUpdate(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	price, err := a.createPrice(ctx, modelPricePayload{Provider: "openai", Model: "gpt-6.1-sol", BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if price.UltrafastMultiplier != nil {
		t.Fatal("new prices must not guess an ultrafast default")
	}
	handler := a.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	path := fmt.Sprintf("/api/model-prices/%d/priority-multiplier", price.ID)
	for _, payload := range []map[string]any{{"priority_multiplier": 2, "ultrafast_multiplier": 3}, {"priority_multiplier": 4}, {"ultrafast_multiplier": 5}} {
		requestJSONForPricingTest(t, handler, http.MethodPut, path, payload, cookies, &price)
	}
	assertUltrafastTestMultipliers(t, price, 4, 5)
	for _, payload := range []priorityMultiplierPayload{{}, {PriorityMultiplier: ultrafastTestFloat(6), UltrafastMultiplier: ultrafastTestFloat(0)}, {UltrafastMultiplier: ultrafastTestFloat(-1)}, {UltrafastMultiplier: ultrafastTestFloat(math.Inf(1))}, {UltrafastMultiplier: ultrafastTestFloat(math.MaxFloat64)}} {
		if _, err := a.updatePriorityMultiplier(ctx, price.ID, payload); err == nil {
			t.Fatalf("unsafe payload accepted: %#v", payload)
		}
		stored, err := a.getPrice(ctx, price.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertUltrafastTestMultipliers(t, stored, 4, 5)
	}
	base := modelPricePayload{Provider: "openai", Model: price.Model, BillingUnit: modelBillingUnitToken, InputUSDPerMillion: 3}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, err := a.updatePrice(ctx, price.ID, base); errs <- err }()
	go func() {
		defer wg.Done()
		_, err := a.updatePriorityMultiplier(ctx, price.ID, priorityMultiplierPayload{UltrafastMultiplier: ultrafastTestFloat(7)})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	price, err = a.getPrice(ctx, price.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertUltrafastTestMultipliers(t, price, 4, 7)
	if price.InputUSDPerMillion != 3 {
		t.Fatalf("concurrent base price = %g", price.InputUSDPerMillion)
	}
	base.InputUSDPerMillion = 1e300
	if _, err := a.updatePrice(ctx, price.ID, base); err == nil {
		t.Fatal("base-price update must validate both stored multipliers")
	}
	other, err := a.createPrice(ctx, modelPricePayload{Provider: "openai", Model: "gpt-6-sol", InputUSDPerMillion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.updatePriorityMultiplier(ctx, other.ID, priorityMultiplierPayload{UltrafastMultiplier: ultrafastTestFloat(2)}); err == nil {
		t.Fatal("non-target model accepted ultrafast")
	}
	var wire map[string]any
	requestJSONForPricingTest(t, handler, http.MethodPut, path, map[string]any{"ultrafast_multiplier": 7}, cookies, &wire)
	if wire["ultrafast_multiplier"] != float64(7) {
		t.Fatalf("price JSON lost ultrafast: %#v", wire)
	}
}

func TestUltrafastVersionsPreserveOmittedTierAndScheduledTime(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	model := "gpt-6-astra"
	id := seedCorrectionTestChannelPrice(t, a, model, 2)
	current, err := a.updatePriorityMultiplier(ctx, id, priorityMultiplierPayload{PriorityMultiplier: ultrafastTestFloat(2), UltrafastMultiplier: ultrafastTestFloat(3)})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	scheduled := current
	scheduled.InputUSDPerMillion, scheduled.PriorityMultiplier, scheduled.UltrafastMultiplier = 9, ultrafastTestFloat(4), ultrafastTestFloat(5)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.appendModelPriceVersion(ctx, tx, scheduled, future, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []priorityMultiplierPayload{{UltrafastMultiplier: ultrafastTestFloat(6), CorrectLatestVersion: true}, {PriorityMultiplier: ultrafastTestFloat(7), CorrectLatestVersion: true}} {
		current, err = a.updatePriorityMultiplier(ctx, id, payload)
		if err != nil {
			t.Fatal(err)
		}
		assertUltrafastTestMultipliers(t, current, 2, 3)
		if current.InputUSDPerMillion != 2 {
			t.Fatalf("future correction materialized early: %#v", current)
		}
	}
	base := correctionTestPayload(model, 10, true)
	if _, err := a.updatePrice(ctx, id, base); err != nil {
		t.Fatal(err)
	}
	versions, err := a.listModelPriceVersionsForPrice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || !versions[1].EffectiveAt.Equal(future) || versions[1].InputUSDPerMillion != 10 {
		t.Fatalf("version identity changed: %#v", versions)
	}
	if versions[1].PriorityMultiplier == nil || *versions[1].PriorityMultiplier != 7 || versions[1].UltrafastMultiplier == nil || *versions[1].UltrafastMultiplier != 6 {
		t.Fatalf("scheduled omitted tier lost: %#v", versions[1])
	}
	index, err := a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selected := effectivePriceFromVersions(current, index, future.Add(time.Minute))
	assertUltrafastTestMultipliers(t, selected, 7, 6)
	if selected.InputUSDPerMillion != 10 {
		t.Fatalf("scheduled input = %g", selected.InputUSDPerMillion)
	}
}

func TestUltrafastSyncAndConflictCopiesPreserveValues(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra"} {
		if _, err := a.db.Exec(`INSERT INTO model_prices (provider, model, input_usd_per_million, priority_multiplier, ultrafast_multiplier, source, updated_at) VALUES ('openai', ?, 2, 3, 4, 'litellm', CURRENT_TIMESTAMP)`, model); err != nil {
			t.Fatal(err)
		}
	}
	raw := map[string]any{}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra"} {
		raw[model] = map[string]any{"litellm_provider": "openai", "input_cost_per_token": 0.000003}
	}
	if _, err := a.syncLiteLLMPricesSelected(ctx, "fixture", raw, []string{"gpt-6.1-sol"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncLiteLLMPrices(ctx, "fixture", raw); err != nil {
		t.Fatal(err)
	}
	prices, err := a.listPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range prices {
		assertUltrafastTestMultipliers(t, price, 3, 4)
	}
	active := prices[0]
	if _, err := a.db.Exec(`INSERT INTO model_price_library_conflicts (original_id, selected_price_id, conflict_reason, provider, model, input_usd_per_million, priority_multiplier, ultrafast_multiplier, updated_at) VALUES (900001, ?, 'case_insensitive_library_identity', 'OpenAI', ?, 7, 8, 9, CURRENT_TIMESTAMP)`, active.ID, active.Model); err != nil {
		t.Fatal(err)
	}
	conflicts, err := a.listModelPriceLibraryConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v / %v", conflicts, err)
	}
	assertUltrafastTestMultipliers(t, conflicts[0].Price, 8, 9)
	replaced, err := a.replaceActiveModelPriceLibraryConflict(ctx, 900001)
	if err != nil {
		t.Fatal(err)
	}
	assertUltrafastTestMultipliers(t, replaced, 8, 9)
	conflicts, err = a.listModelPriceLibraryConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("archived conflict = %#v / %v", conflicts, err)
	}
	assertUltrafastTestMultipliers(t, conflicts[0].Price, 3, 4)
	promoted, err := a.promoteModelPriceLibraryConflict(ctx, 900001, modelPriceLibraryConflictPromotePayload{Provider: "codex", Model: active.Model})
	if err != nil {
		t.Fatal(err)
	}
	assertUltrafastTestMultipliers(t, promoted, 3, 4)
	// API copies sanitize non-finite values without hiding them from billing/storage.
	promoted.UltrafastMultiplier = ultrafastTestFloat(math.Inf(1))
	if _, err := json.Marshal(modelPriceForAPI(promoted)); err != nil {
		t.Fatal(err)
	}
	if promoted.UltrafastMultiplier == nil || !math.IsInf(*promoted.UltrafastMultiplier, 1) {
		t.Fatal("API projection mutated persisted/billing value")
	}
}

func TestUltrafastMigrationAndLegacyProjection(t *testing.T) {
	ctx := context.Background()
	dbPath := prepareMigrationTestDatabase(t, t.TempDir(), 202610280001)
	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`INSERT INTO model_prices (provider, model, priority_multiplier, cache_creation_1h_usd_per_million, updated_at) VALUES ('openai', 'gpt-6-astra', 2, 17, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	legacy, err := listPricesWithQueryer(ctx, db)
	if err != nil || len(legacy) != 1 || legacy[0].CacheCreation1hUSDPerMillion == nil || *legacy[0].CacheCreation1hUSDPerMillion != 17 || legacy[0].UltrafastMultiplier != nil {
		t.Fatalf("legacy projection = %#v / %v", legacy, err)
	}
	clone := legacy[0]
	brand, key := "codex", "oauth_pool"
	clone.PriceScope, clone.ChannelBrand, clone.ChannelKey = modelPriceScopeChannel, &brand, &key
	clone.LongContext = &ModelPriceLongContext{ThresholdInputTokens: 100, InputUSDPerMillion: 2, CacheCreation1hUSDPerMillion: ultrafastTestFloat(19)}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).appendModelPriceVersion(ctx, tx, clone, time.Now(), true); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	history, err := loadModelPriceVersionsWithQueryer(ctx, db)
	if err != nil || len(history) != 1 {
		t.Fatalf("legacy version projection = %#v / %v", history, err)
	}
	for _, chain := range history {
		version := chain.all[0].Price
		if version.CacheCreation1hUSDPerMillion == nil || *version.CacheCreation1hUSDPerMillion != 17 || version.LongContext == nil || version.LongContext.CacheCreation1hUSDPerMillion == nil || *version.LongContext.CacheCreation1hUSDPerMillion != 19 {
			t.Fatalf("missing ultrafast column discarded existing 1h snapshot: %#v", version)
		}
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	clone.UltrafastMultiplier = ultrafastTestFloat(3)
	if err := (&App{}).appendModelPriceVersion(ctx, tx, clone, time.Now(), false); err == nil {
		tx.Rollback()
		t.Fatal("pre-migration writes cannot silently drop an explicit ultrafast value")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatal(err)
	}
	up, err := listPricesWithQueryer(ctx, db)
	if err != nil || !reflect.DeepEqual(legacy, up) {
		t.Fatalf("migration changed price: %#v / %v", up, err)
	}
	if err := requireSchemaShape(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_price_library_conflicts (original_id, selected_price_id, conflict_reason, provider, model, updated_at) VALUES (100, 1, 'case_insensitive_library_identity', 'openai', 'gpt-6-astra', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
		if _, err := db.Exec("UPDATE " + table + " SET ultrafast_multiplier = 3"); err != nil {
			t.Fatal(err)
		}
		if err := goose.DownToContext(ctx, db, ".", 202610280001); err == nil || !strings.Contains(err.Error(), "contains ultrafast multiplier configuration") {
			t.Fatalf("unsafe downgrade %s = %v", table, err)
		}
		for _, other := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
			if !testColumnExists(t, db, other, "ultrafast_multiplier") {
				t.Fatalf("failed downgrade partially changed %s", other)
			}
		}
		if _, err := db.Exec("UPDATE " + table + " SET ultrafast_multiplier = NULL"); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.DownToContext(ctx, db, ".", 202610280001); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
		if testColumnExists(t, db, table, "ultrafast_multiplier") {
			t.Fatalf("downgrade kept %s new column", table)
		}
	}
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatal(err)
	}
	if err := requireSchemaShape(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestUltrafastHistoricalDetailsAnalyticsKeeperAndSettledQuota(t *testing.T) {
	a, ctx := newUltrafastTestApp(t), context.Background()
	userID := seedQuotaTestUser(t, a, "ultrafast-member")
	apiKey, quota := "sk-ultrafast-integration", 100.0
	seedQuotaTestAPIKey(t, a, userID, apiKey)
	if _, err := a.updateUserQuota(ctx, userID, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, appTimeLocation)
	end := start.AddDate(0, 0, 1)
	models := []string{"gpt-6.1-sol", "gpt-6-astra"}
	for _, model := range models {
		seedQuotaTestPrice(t, a, "Vendor A", model, 2)
	}
	if _, err := a.db.Exec(`UPDATE model_prices SET priority_multiplier = 2, ultrafast_multiplier = 3`); err != nil {
		t.Fatal(err)
	}
	pricing := quotaTestBillingPriceIndex(t, a)
	for _, price := range pricing.Prices {
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.appendModelPriceVersion(ctx, tx, price, start.Add(-24*time.Hour), true); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	pricing.Versions, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records := []UsageRecord{}
	for _, model := range models {
		for _, test := range []struct {
			tier string
			cost float64
		}{{"default", 2}, {"priority", 4}, {"ultrafast", 6}} {
			raw, err := json.Marshal(map[string]any{"api_key": apiKey, "provider": "Vendor A", "model": model, "service_tier": test.tier, "request_id": model + test.tier, "timestamp": start.Add(10 * time.Hour).Format(time.RFC3339), "tokens": map[string]any{"input_tokens": 1_000_000}})
			if err != nil {
				t.Fatal(err)
			}
			record, created, err := a.saveUsageMessage(ctx, raw, pricing)
			if err != nil || !created {
				t.Fatalf("save %s/%s = %t/%v", model, test.tier, created, err)
			}
			records = append(records, record)
			stored, err := a.getUsageRecord(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			detail := versionedCostBreakdown(stored, pricing, true)
			if detail.Unpriced || detail.TotalUSD != test.cost {
				t.Fatalf("detail %s/%s = %#v", model, test.tier, detail)
			}
			var charged float64
			if err := a.db.QueryRow(`SELECT amount_usd FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&charged); err != nil {
				t.Fatal(err)
			}
			if charged != test.cost {
				t.Fatalf("first quota cost = %g, want %g", charged, test.cost)
			}
		}
	}
	// This shared assertion covers persisted hourly facts, summary, rankings and keeper.
	assertUltrafastHistoricalCosts(t, a, pricing, start, end, map[string]float64{models[0]: 12, models[1]: 12})
	for _, price := range pricing.Prices {
		if _, err := a.updatePriorityMultiplier(ctx, price.ID, priorityMultiplierPayload{UltrafastMultiplier: ultrafastTestFloat(4), CorrectLatestVersion: true}); err != nil {
			t.Fatal(err)
		}
	}
	pricing.Versions, err = a.loadModelPriceVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertUltrafastHistoricalCosts(t, a, pricing, start, end, map[string]float64{models[0]: 14, models[1]: 14})
	for _, record := range records {
		if err := a.applyQuotaCharge(ctx, record, pricing); err != nil {
			t.Fatal(err)
		}
	}
	var charged float64
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*), SUM(amount_usd) FROM user_quota_charges`).Scan(&count, &charged); err != nil {
		t.Fatal(err)
	}
	user, err := a.getUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 || charged != 24 || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 76 {
		t.Fatalf("repricing rewrote settled quota: count=%d amount=%g user=%#v", count, charged, user)
	}
}

func assertUltrafastHistoricalCosts(t *testing.T, a *App, pricing modelPriceBillingIndex, start, end time.Time, costs map[string]float64) {
	t.Helper()
	ctx := context.Background()
	want := 0.0
	for _, cost := range costs {
		want += cost
	}
	filters := UsageFilters{Start: &start, End: &end}
	if !usageAnalyticsHourlyReadPlanFor(filters).useHourly {
		t.Fatal("test must exercise hourly reads")
	}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true, Rankings: map[string]string{"model": "cost"}})
	if err := a.collectUsageAnalytics(ctx, filters, pricing, collector, "timestamp ASC"); err != nil {
		t.Fatal(err)
	}
	summary := collector.summaryResponse()
	if summary["estimated_cost_usd"] != want || summary["total_records"] != 6 || summary["unpriced_records"] != 0 {
		t.Fatalf("summary = %#v, want %g / 6 / 0", summary, want)
	}
	ranking := collector.rankingResponse("model")["items"].([]map[string]any)
	if len(ranking) != len(costs) {
		t.Fatalf("ranking = %#v", ranking)
	}
	for _, item := range ranking {
		_, model, _ := strings.Cut(item["key"].(string), "::")
		if expected, exists := costs[model]; !exists || item["estimated_cost_usd"] != expected {
			t.Fatalf("ranking %q = %#v", model, item)
		}
	}
	var hourly float64
	if err := a.db.QueryRow(`SELECT COALESCE(SUM(estimated_cost_usd), 0) FROM usage_analytics_hourly WHERE hour_start >= ? AND hour_start < ?`, dbTime(start), dbTime(end)).Scan(&hourly); err != nil {
		t.Fatal(err)
	}
	if hourly != want {
		t.Fatalf("persisted hourly = %g, want %g", hourly, want)
	}
	records, err := a.keeperUsageRecordsInRange(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	usage := &keeperQuotaWindowUsage{WindowStart: start, WindowEnd: end}
	for _, record := range records {
		addRecordToKeeperQuotaWindowUsageVersioned(usage, record, pricing.Prices, pricing.Versions, pricing.MatchContext)
	}
	if usage.Records != 6 || usage.EstimatedCostUSD != want || usage.UnpricedRecords != 0 {
		t.Fatalf("keeper = %#v, want %g / 6 / 0", usage, want)
	}
}
