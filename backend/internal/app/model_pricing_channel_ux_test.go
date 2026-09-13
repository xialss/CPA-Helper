package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	backendMigrations "cpa-helper/backend/migrations"
)

func TestLiteLLMOptionsUseSourceIDsAndCurrentCatalogMatching(t *testing.T) {
	entry := func(provider string) map[string]any {
		return map[string]any{"litellm_provider": provider, "input_cost_per_token": 0.000001}
	}
	raw := map[string]any{
		" gpt-ux ": entry("openai"), "other": entry("openai"),
		"gemini-2.5-pro": entry("gemini"), "disabled": entry("openai"),
		"sample_spec": entry("openai"), "missing-provider": map[string]any{"input_cost_per_token": 1},
		"invalid": map[string]any{"litellm_provider": "openai", "input_cost_per_token": "NaN"},
	}
	options := liteLLMOptions(raw, []ModelPriceCatalogItem{
		{Name: "gpt-ux", SuggestedProvider: "openai", ChannelStatus: modelPriceChannelStatusReady},
		{Name: "models/gemini-2.5-pro", SuggestedProvider: "gemini", ChannelStatus: modelPriceChannelStatusReady},
		{Name: "disabled", SuggestedProvider: "openai", ChannelStatus: modelPriceChannelStatusReady, ChannelDisabled: true},
	})
	if len(options) != 4 {
		t.Fatalf("options = %#v, want 4 valid entries", options)
	}
	for _, option := range options {
		wantMatch := option.Model == " gpt-ux " || option.Model == "gemini-2.5-pro"
		if option.MatchedCurrent != wantMatch {
			t.Fatalf("option = %#v, want matched %v", option, wantMatch)
		}
		if option.Model == " gpt-ux " && option.PriceModel != "gpt-ux" {
			t.Fatalf("raw source ID and normalized model lost: %#v", option)
		}
	}
}

func TestLiteLLMSelectedSyncPreservesUnselectedManualAndConflictPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	if _, err := a.db.Exec(`INSERT INTO model_prices
		(id, provider, model, input_usd_per_million, source, updated_at)
		VALUES (920001, 'openai', 'selected', 1, 'litellm', '2026-01-01'),
		(920002, 'openai', 'unselected', 2, 'litellm', '2026-01-01'),
		(920003, 'openai', 'manual', 3, 'manual', '2026-01-01'),
		(920004, 'openai', 'conflict', 4, 'litellm', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO model_price_library_conflicts
		(original_id, selected_price_id, conflict_reason, provider, model,
		 input_usd_per_million, output_usd_per_million, cache_read_usd_per_million,
		 cache_creation_usd_per_million, source, auto_synced, updated_at)
		VALUES (920005, 920004, 'case_insensitive_library_identity', 'OpenAI', 'Conflict', 5, 0, 0, 0, 'manual', 0, '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	for _, model := range []string{" selected ", "unselected", "manual", "conflict", "new"} {
		raw[model] = map[string]any{"litellm_provider": "openai", "input_cost_per_token": 0.000009}
	}
	raw["SELECTED"] = map[string]any{"litellm_provider": "OpenAI", "input_cost_per_token": 0.000019}
	before, err := a.listPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range [][]string{nil, {}, {""}, {"unknown"}, {" selected ", "unknown"}, {"selected"}, {" selected ", "SELECTED"}} {
		if _, err := a.syncLiteLLMPricesSelected(ctx, "fixture", raw, selection, 42); err == nil {
			t.Fatalf("selection %#v must fail", selection)
		}
		after, err := a.listPrices(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("invalid selection mutated prices: %v", err)
		}
	}
	result, err := a.syncLiteLLMPricesSelected(ctx, "fixture", raw, []string{" selected ", " selected ", "manual", "conflict", "new"}, 42)
	if err != nil {
		t.Fatal(err)
	}
	if result["selected_entries"] != 4 || result["created"] != 1 || result["updated"] != 1 || result["skipped_manual"] != 2 {
		t.Fatalf("unexpected result %#v", result)
	}
	selected, err := a.getPrice(ctx, 920001)
	if err != nil || selected.InputUSDPerMillion != 9 || selected.SourceModel == nil || *selected.SourceModel != " selected " {
		t.Fatalf("selected row lost stable ID/source ID: %#v %v", selected, err)
	}
	for _, prior := range before {
		if prior.ID == 920001 {
			continue
		}
		after, err := a.getPrice(ctx, prior.ID)
		if err != nil || !reflect.DeepEqual(prior, after) {
			t.Fatalf("unselected/protected row %d changed: %v", prior.ID, err)
		}
	}
	var archives int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM model_price_library_conflicts`).Scan(&archives); err != nil || archives != 1 {
		t.Fatalf("conflict archive changed: %v", err)
	}
	if _, err := a.syncLiteLLMPricesSelected(ctx, "fixture", raw, []string{" selected "}, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := a.getPrice(ctx, 920001); err != nil {
		t.Fatal("repeat sync changed stable ID", err)
	}
}

func TestModelPriceChannelAliasLifecycleAndValidation(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	responses := map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{"api-key": "first-secret", "auth-index": "Auth.json", "models": []map[string]any{{"name": "models/gemini-2.5-pro"}}},
			{"api-key": "second-secret", "auth-index": "auth.json", "models": []map[string]any{{"name": "models/gemini-2.5-pro"}}},
			{"api-key": "third-secret", "models": []map[string]any{}},
		},
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "Compat Vendor", "models": []map[string]any{{"name": "compat-model"}}},
		},
		"/v0/management/auth-files":    map[string]any{"files": []any{}},
		"/v0/management/api-key-usage": map[string]any{},
	}
	cpa := newModelPriceCatalogManagementServer(t, responses)
	defer cpa.Close()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = cpa.URL, "test-management-key"
	if err := a.saveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	providers, err := a.aiProviderConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := modelPriceChannelAlias{AuthType: "apikey", ChannelBrand: "gemini", ChannelKey: "Auth.json", Label: "Primary upstream", ChannelIdentityHash: providers[0].IdentityHash}
	var compatibility aiProviderItem
	for _, provider := range providers {
		if provider.Brand == aiProviderBrandOpenAICompatibility {
			compatibility = provider
			break
		}
	}
	if compatibility.IdentityHash == "" || modelPriceChannelAliasSelector(compatibility) != "compat vendor" {
		t.Fatalf("OpenAI-compatible provider lost its stable local-name selector: %#v", compatibility)
	}
	compatibilityPayload := modelPriceChannelAlias{
		AuthType:            "apikey",
		ChannelBrand:        string(aiProviderBrandOpenAICompatibility),
		ChannelKey:          modelPriceChannelAliasSelector(compatibility),
		ChannelIdentityHash: compatibility.IdentityHash,
		Label:               "Friendly compatibility channel",
	}
	beforeSelectors, beforeLabels, beforeSourceChannels, available := a.priceSelectors.snapshotWithLabels()
	if !available {
		t.Fatal("expected selector snapshot")
	}
	if _, err := a.upsertModelPriceChannelAlias(ctx, payload); err != nil {
		t.Fatal(err)
	}
	_, err = a.upsertModelPriceChannelAlias(ctx, compatibilityPayload)
	var aliasError *AppError
	if !errors.As(err, &aliasError) || aliasError.Code != "validation_error" || aliasError.Status != http.StatusUnprocessableEntity || !strings.Contains(aliasError.Message, "Provider 名称") {
		t.Fatalf("OpenAI-compatible alias must direct users to edit the Provider name: %v", err)
	}
	var compatibilityAliasCount int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_price_channel_aliases WHERE channel_brand = ?`, compatibilityPayload.ChannelBrand).Scan(&compatibilityAliasCount); err != nil || compatibilityAliasCount != 0 {
		t.Fatalf("rejected compatible alias was persisted: count=%d error=%v", compatibilityAliasCount, err)
	}
	// Legacy aliases stay stored, but cannot override the Provider name.
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label) VALUES (?, ?, ?, ?)`, compatibilityPayload.AuthType, compatibilityPayload.ChannelBrand, compatibilityPayload.ChannelKey, compatibilityPayload.Label); err != nil {
		t.Fatal(err)
	}
	afterSelectors, afterLabels, afterSourceChannels, available := a.priceSelectors.snapshotWithLabels()
	if !available || !reflect.DeepEqual(beforeSelectors, afterSelectors) || !reflect.DeepEqual(beforeLabels, afterLabels) || !reflect.DeepEqual(beforeSourceChannels, afterSourceChannels) {
		t.Fatal("display-only alias changed/invalidated billing selectors or cached labels")
	}
	var snapshot aiProvidersResponse
	snapshot, err = a.aiProvidersSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers[0].ChannelAlias != payload.Label || snapshot.Providers[0].ChannelKey != payload.ChannelKey || snapshot.Providers[1].ChannelAlias != "" {
		t.Fatalf("aliases mixed case-sensitive channels: %#v", snapshot.Providers)
	}
	var compatibilitySnapshot *aiProviderItem
	for index := range snapshot.Providers {
		if snapshot.Providers[index].Brand == aiProviderBrandOpenAICompatibility {
			compatibilitySnapshot = &snapshot.Providers[index]
			break
		}
	}
	if compatibilitySnapshot == nil || compatibilitySnapshot.ChannelAlias != "" || compatibilitySnapshot.ChannelKey != compatibilityPayload.ChannelKey || compatibilitySnapshot.IdentityHash != compatibility.IdentityHash || aiProviderOptionalString(compatibilitySnapshot.Name) != "Compat Vendor" {
		t.Fatalf("legacy OpenAI-compatible alias displaced its Provider name or identity: %#v", compatibilitySnapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"local_alias":"Primary upstream"`) || strings.Contains(string(encoded), "first-secret") {
		t.Fatal("provider response lost alias or exposed key")
	}
	catalog, err := a.modelPriceCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compatibleCatalogRows := 0
	for _, row := range catalog.Models {
		if row.ChannelKey == "Auth.json" && (row.ChannelLabel != payload.Label || row.ChannelLabelFallback || row.ChannelAlias != payload.Label) {
			t.Fatalf("catalog omitted alias: %#v", row)
		}
		if row.ChannelBrand == string(aiProviderBrandOpenAICompatibility) && row.ChannelKey == compatibilityPayload.ChannelKey {
			compatibleCatalogRows++
			if row.ChannelLabel != "Compat Vendor" || row.ChannelLabelFallback || row.ChannelAlias != "" {
				t.Fatalf("catalog legacy alias displaced the OpenAI-compatible Provider name: %#v", row)
			}
		}
	}
	if compatibleCatalogRows != 1 {
		t.Fatalf("compatible catalog rows = %d, want 1", compatibleCatalogRows)
	}
	masked := providers[0]
	newMask := "changed...mask"
	masked.APIKeyMasked = &newMask
	maskedProviders := []aiProviderItem{masked}
	if err := a.applyModelPriceChannelAliases(ctx, maskedProviders); err != nil || maskedProviders[0].ChannelAlias != payload.Label {
		t.Fatalf("mask change lost alias: %v", err)
	}
	noAuth := providers[0]
	noAuth.AuthIndex = nil
	if got := modelPriceChannelAliasSelector(noAuth); got != "keyhash:"+aiProviderOptionalString(noAuth.APIKeyHash) {
		t.Fatalf("credential without runtime auth index needs a stable local alias selector, got %q", got)
	}
	if key, _, _ := modelPriceChannelSelector(noAuth); key != "" {
		t.Fatal("local alias selector must not become a billing selector")
	}
	noAuth.APIKeyHash = nil
	if key := modelPriceChannelAliasSelector(noAuth); key != "" {
		t.Fatal("an index-only fallback identity must not become a persisted alias selector")
	}
	noAuthPayload := modelPriceChannelAlias{
		AuthType: "apikey", ChannelBrand: "gemini", ChannelKey: snapshot.Providers[2].ChannelKey,
		ChannelIdentityHash: snapshot.Providers[2].IdentityHash, Label: "Unnamed credential",
	}
	if _, err := a.upsertModelPriceChannelAlias(ctx, noAuthPayload); err != nil {
		t.Fatalf("native provider without runtime auth index could not be named: %v", err)
	}
	named, err := a.aiProvidersSnapshot(ctx)
	if err != nil || named.Providers[2].ChannelAlias != noAuthPayload.Label {
		t.Fatalf("native provider without runtime auth index lost its label: %v", err)
	}
	noAuthPayload.Label = ""
	if _, err := a.upsertModelPriceChannelAlias(ctx, noAuthPayload); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*modelPriceChannelAlias){
		func(p *modelPriceChannelAlias) { p.AuthType = "invalid" },
		func(p *modelPriceChannelAlias) { p.AuthType = "oauth" },
		func(p *modelPriceChannelAlias) { p.ChannelBrand = "unsupported" },
		func(p *modelPriceChannelAlias) { p.ChannelIdentityHash = "stale" },
		func(p *modelPriceChannelAlias) { p.ChannelKey = "missing" },
		func(p *modelPriceChannelAlias) { p.Label = strings.Repeat("x", 201) },
		func(p *modelPriceChannelAlias) { p.Label = "line\nbreak" },
		func(p *modelPriceChannelAlias) { p.Label = "nul\x00byte" },
	} {
		invalid := payload
		mutate(&invalid)
		if _, err := a.upsertModelPriceChannelAlias(ctx, invalid); err == nil {
			t.Fatalf("accepted invalid alias %#v", invalid)
		}
	}
	// Only native aliases remain available for historical display.
	aliases, err := a.listModelPriceChannelAliases(ctx)
	if err != nil || len(aliases) != 1 || !modelPriceAliasExists(aliases, payload) {
		t.Fatalf("persisted aliases = %#v %v", aliases, err)
	}
	handler := a.Routes()
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", payload, nil, http.StatusUnauthorized)
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices/sync/litellm", map[string]any{"models": []string{}}, cookies, http.StatusUnprocessableEntity)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPost, "/api/model-prices/sync/litellm", map[string]any{}, cookies, http.StatusUnprocessableEntity)
	legacyLabel := compatibilityPayload.Label
	compatibilityPayload.Label = "Friendly compatibility channel via API"
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", compatibilityPayload, cookies, http.StatusUnprocessableEntity)
	var storedCompatibilityLabel string
	if err := a.db.QueryRowContext(ctx, `SELECT label FROM model_price_channel_aliases WHERE channel_brand = ? AND channel_key = ?`, compatibilityPayload.ChannelBrand, compatibilityPayload.ChannelKey).Scan(&storedCompatibilityLabel); err != nil || storedCompatibilityLabel != legacyLabel {
		t.Fatalf("legacy compatible alias was changed or removed: label=%q error=%v", storedCompatibilityLabel, err)
	}
	payload.Label = "Primary upstream via API"
	var savedAlias modelPriceChannelAlias
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", payload, cookies, &savedAlias)
	if savedAlias.Label != payload.Label || savedAlias.ChannelKey != payload.ChannelKey || savedAlias.ChannelIdentityHash != payload.ChannelIdentityHash {
		t.Fatalf("native alias API result = %#v", savedAlias)
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/channel-aliases", nil, cookies, &aliases)
	if len(aliases) != 1 || !modelPriceAliasExists(aliases, payload) {
		t.Fatalf("alias API exposed legacy compatible names: %#v", aliases)
	}
	payload.Label = ""
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", payload, cookies, nil)
	compatibilityPayload.Label = ""
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", compatibilityPayload, cookies, nil)
	aliases, err = a.listModelPriceChannelAliases(ctx)
	if err != nil || len(aliases) != 0 {
		t.Fatalf("reset did not delete alias: %#v %v", aliases, err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_price_channel_aliases`).Scan(&compatibilityAliasCount); err != nil || compatibilityAliasCount != 0 {
		t.Fatalf("explicit reset did not delete legacy compatible alias: count=%d error=%v", compatibilityAliasCount, err)
	}
	if _, err := a.db.Exec(`DROP TABLE model_price_channel_aliases`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.aiProvidersSnapshot(ctx); err == nil {
		t.Fatal("missing alias DB table was silently ignored")
	}
	if err := requireSchemaShape(ctx, a.db); !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "model_price_channel_aliases") {
		t.Fatalf("readiness accepted missing alias table: %v", err)
	}
}

func TestModelPriceChannelAliasCanClearAfterProviderRemoval(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ctx := context.Background()
	for _, test := range []struct{ brand, key string }{
		{"gemini", "removed-auth.json"},
		{"openai_compatibility", "Removed Vendor"},
	} {
		t.Run(test.brand, func(t *testing.T) {
			key := canonicalModelPriceChannelKey(test.brand, test.key)
			if _, err := a.db.ExecContext(ctx, `INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label) VALUES ('apikey', ?, ?, ?)`, test.brand, key, "Former upstream"); err != nil {
				t.Fatal(err)
			}
			// Clearing remains local even without upstream settings or an identity hash.
			if _, err := a.upsertModelPriceChannelAlias(ctx, modelPriceChannelAlias{
				AuthType:     "apikey",
				ChannelBrand: test.brand,
				ChannelKey:   test.key,
				Label:        "",
			}); err != nil {
				t.Fatalf("clear alias after provider removal: %v", err)
			}
			var count int
			if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_price_channel_aliases WHERE channel_brand=? AND channel_key=?`, test.brand, key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("removed-provider alias rows = %d, want 0", count)
			}
		})
	}
}

func modelPriceAliasExists(aliases []modelPriceChannelAlias, want modelPriceChannelAlias) bool {
	for _, alias := range aliases {
		if alias.AuthType == want.AuthType && alias.ChannelBrand == want.ChannelBrand && alias.ChannelKey == want.ChannelKey && alias.Label == want.Label {
			return true
		}
	}
	return false
}

func TestModelPriceChannelAliasMigrationUpgradesPreviousHead(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	prepareMigrationTestDatabase(t, dataDir, 202609040001)
	ctx := context.Background()
	if _, err := CheckStartup(ctx); !errors.Is(err, ErrDatabaseNeedsMigration) {
		t.Fatalf("previous schema must need migration: %v", err)
	}
	report, err := Migrate(ctx)
	if err != nil || report.CurrentVersion != backendMigrations.LatestVersion {
		t.Fatalf("upgrade failed: %#v %v", report, err)
	}
	a, err := NewWithOptions(ctx, NewOptions{RequireReady: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label) VALUES ('apikey', 'gemini', 'Auth.json', 'First'), ('apikey', 'gemini', 'auth.json', 'Second')`); err != nil {
		t.Fatalf("native selector case was not preserved: %v", err)
	}
	if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label) VALUES ('apikey', 'gemini', 'Auth.json', 'Duplicate')`); err == nil {
		t.Fatal("alias uniqueness was not enforced")
	}
	if _, err := Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	aliases, err := a.listModelPriceChannelAliases(ctx)
	if err != nil || len(aliases) != 2 {
		t.Fatalf("repeat migration changed aliases: %#v %v", aliases, err)
	}
}

func BenchmarkLiteLLMSelectedSync(b *testing.B) {
	for _, selectedCount := range []int{20, 3561} {
		b.Run(fmt.Sprintf("entries_3561_selected_%d", selectedCount), func(b *testing.B) {
			b.Setenv("CPA_HELPER_DATA_DIR", b.TempDir())
			a, err := New()
			if err != nil {
				b.Fatal(err)
			}
			defer a.Close()
			raw := make(map[string]any, 3561)
			selected := make([]string, 0, selectedCount)
			for i := 0; i < 3561; i++ {
				name := fmt.Sprintf("benchmark-model-%04d", i)
				raw[name] = map[string]any{"litellm_provider": "openai", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "max_input_tokens": 128000, "metadata": strings.Repeat("x", 420)}
				if i < selectedCount {
					selected = append(selected, name)
				}
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(encoded)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var decoded map[string]any
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					b.Fatal(err)
				}
				if _, err := a.syncLiteLLMPricesSelected(context.Background(), "synthetic-offline-fixture", decoded, selected, len(encoded)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
