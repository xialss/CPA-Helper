package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestLibraryTemplateAffixMatching(t *testing.T) {
	library := func(id int, provider, model string) ModelPrice {
		return ModelPrice{ID: id, Provider: provider, Model: model, PriceScope: modelPriceScopeLibrary}
	}
	base := library(1, "gemini", "gemini-4-argon")
	tests := []struct {
		name     string
		provider string
		model    string
		prices   []ModelPrice
		wantID   int
	}{
		{name: "reported suffix", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{base}, wantID: 1},
		{name: "prefix and suffix", provider: "gemini", model: "vendor-gemini-4-argon-token", prices: []ModelPrice{base}, wantID: 1},
		{name: "case and whitespace", provider: "GEMINI", model: " GEMINI-4-ARGON-token ", prices: []ModelPrice{base}, wantID: 1},
		{name: "google path", provider: "gemini", model: "publishers/google/models/gemini-4-argon-token", prices: []ModelPrice{base}, wantID: 1},
		{name: "canonical library path", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{library(2, "gemini", "models/gemini-4-argon")}, wantID: 2},
		{name: "native library prefix", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{library(2, "gemini", "gemini/gemini-4-argon")}, wantID: 2},
		{name: "exact before shorter template", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{base, library(2, "gemini", "gemini-4-argon-token")}, wantID: 2},
		{name: "longest template", provider: "gemini", model: "vendor-gemini-4-argon-token-v2", prices: []ModelPrice{base, library(2, "gemini", "gemini-4-argon-token")}, wantID: 2},
		{name: "provider before longer fallback", provider: "codex", model: "gpt-example-special-token", prices: []ModelPrice{library(1, "codex", "gpt-example"), library(2, "openai", "gpt-example-special")}, wantID: 1},
		{name: "fallback exact before primary affix", provider: "codex", model: "gpt-example-token", prices: []ModelPrice{library(1, "codex", "gpt-example"), library(2, "openai", "gpt-example-token")}, wantID: 2},
		{name: "codex alias affix", provider: "codex", model: "gpt-example-token", prices: []ModelPrice{library(2, "openai", "gpt-example")}, wantID: 2},
		{name: "claude alias affix", provider: "claude", model: "claude-example-token", prices: []ModelPrice{library(2, "anthropic", "claude-example")}, wantID: 2},
		{name: "gemini explicit google fallback", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{library(2, "google", "gemini-4-argon")}, wantID: 2},
		{name: "gemini forbids vertex fallback", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{library(2, "vertex_ai", "gemini-4-argon")}},
		{name: "numeric continuation", provider: "gemini", model: "gemini-4-argon2", prices: []ModelPrice{base}},
		{name: "left continuation", provider: "gemini", model: "mygemini-4-argon-token", prices: []ModelPrice{base}},
		{name: "no reverse selection", provider: "gemini", model: "gemini-4-argon", prices: []ModelPrice{library(2, "gemini", "gemini-4-argon-thinking")}},
		{name: "later bounded occurrence", provider: "gemini", model: "mygemini-4-argon/gemini-4-argon-token", prices: []ModelPrice{base}, wantID: 1},
		{name: "equal canonical templates ambiguous", provider: "gemini", model: "gemini-4-argon-token", prices: []ModelPrice{base, library(2, "gemini", "models/gemini-4-argon")}},
		{name: "longer clears shorter ambiguity", provider: "gemini", model: "gemini-4-argon-token-v2", prices: []ModelPrice{base, library(2, "gemini", "models/gemini-4-argon"), library(3, "gemini", "gemini-4-argon-token")}, wantID: 3},
		{name: "unique global template", provider: "vendor", model: "vendor-gemini-4-argon-token", prices: []ModelPrice{base}, wantID: 1},
		{name: "global ambiguity", provider: "vendor", model: "vendor-gemini-4-argon-token", prices: []ModelPrice{base, library(2, "google", "gemini-4-argon")}},
		{name: "exact ambiguity never enters affix", provider: "vendor", model: "gemini-4-argon-token", prices: []ModelPrice{library(1, "first", "gemini-4-argon-token"), library(2, "second", "gemini-4-argon-token"), library(3, "vendor", "gemini-4-argon")}},
		{name: "global exact before local affix", provider: "vendor", model: "gemini-4-argon-token", prices: []ModelPrice{library(1, "first", "gemini-4-argon-token"), library(2, "vendor", "gemini-4-argon")}, wantID: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := append([]ModelPrice(nil), test.prices...)
			got := findCatalogPrice(libraryPricesByKey(test.prices), test.prices, test.provider, nil, test.model)
			if test.wantID == 0 {
				if got != nil {
					t.Fatalf("template = %#v, want no unique template", got)
				}
			} else if got == nil || got.ID != test.wantID {
				t.Fatalf("template = %#v, want ID %d", got, test.wantID)
			}
			if !reflect.DeepEqual(test.prices, before) {
				t.Fatal("template selection mutated prices")
			}
		})
	}
	for _, separator := range []string{"-", "_", ".", "/", ":"} {
		t.Run("delimiter "+separator, func(t *testing.T) {
			model := "vendor" + separator + base.Model + separator + "token"
			got := findCatalogPrice(libraryPricesByKey([]ModelPrice{base}), []ModelPrice{base}, "gemini", nil, model)
			if got == nil || got.ID != base.ID {
				t.Fatalf("template for %q = %#v, want base template", model, got)
			}
		})
	}
}

func TestAvailableModelLibraryAffixesKeepOwnerScope(t *testing.T) {
	provider, model := "codex", "gpt-example-token"
	prices := []ModelPrice{{ID: 1, Provider: "openai", Model: "gpt-example", PriceScope: modelPriceScopeLibrary}}
	lookup := libraryPricesByKey(prices)
	if got := findMatchingLibraryPrice(lookup, &provider, &model); got == nil || got.ID != 1 {
		t.Fatalf("alias metadata template = %#v, want 1", got)
	}
	provider = "unrelated"
	if got := findMatchingLibraryPrice(lookup, &provider, &model); got != nil {
		t.Fatalf("unrelated owner metadata template = %#v, want none", got)
	}
	if got := findMatchingLibraryPrice(lookup, nil, &model); got != nil {
		t.Fatalf("missing owner metadata template = %#v, want none", got)
	}
}

func TestLiteLLMOptionsMatchAffixesWithoutChangingSourceIDs(t *testing.T) {
	raw := map[string]any{
		"gemini/gemini-4-argon": map[string]any{"litellm_provider": "gemini", "input_cost_per_token": 0.000001},
		"gemini/unused":         map[string]any{"litellm_provider": "gemini", "input_cost_per_token": 0.000002},
	}
	options := liteLLMOptions(raw, []ModelPriceCatalogItem{{Name: "gemini-4-argon-token", SuggestedProvider: "gemini", ChannelStatus: modelPriceChannelStatusReady}})
	if len(options) != 2 {
		t.Fatalf("options = %#v, want both unchanged source entries", options)
	}
	for _, option := range options {
		if option.MatchedCurrent != (option.Model == "gemini/gemini-4-argon") {
			t.Fatalf("option = %#v, want only original base source ID matched", option)
		}
	}
}

func TestModelPriceCatalogAffixTemplatesDoNotCreateChannelPrices(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/openai-compatibility": []map[string]any{{"name": "Vendor A", "models": []map[string]any{{"name": "gemini-4-argon-token"}}}},
	})
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
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_prices (provider, model, input_usd_per_million, source, updated_at) VALUES ('gemini', 'gemini-4-argon', 7, 'manual', '2026-10-30T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	before, err := a.listPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := a.modelPriceCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 || catalog.PricedModels != 0 || catalog.UnpricedModels != 1 {
		t.Fatalf("catalog = %#v, want one unpriced channel", catalog)
	}
	item := catalog.Models[0]
	if item.Name != "gemini-4-argon-token" || item.Price != nil || item.TemplatePrice == nil || item.TemplatePrice.Model != "gemini-4-argon" || item.TemplatePrice.InputUSDPerMillion != 7 {
		t.Fatalf("channel = %#v, want unchanged actual name and base-only template", item)
	}
	provider, model := "Vendor A", item.Name
	breakdown := calculateRecordCostBreakdown(UsageRecord{Provider: &provider, Model: &model, InputTokens: 1_000_000, TotalTokens: 1_000_000}, channelPricesByKey(before))
	if !breakdown.Unpriced || breakdown.TotalUSD != 0 {
		t.Fatalf("unsaved channel cost = %#v, want unpriced", breakdown)
	}
	after, err := a.listPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("catalog template matching wrote a price")
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_prices (provider, model, price_scope, channel_brand, channel_key, input_usd_per_million, source, updated_at) VALUES ('Vendor A', 'gemini-4-argon', 'channel', 'openai_compatibility', 'vendor a', 9, 'manual', '2026-10-30T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	withBaseChannel, err := a.listPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	breakdown = calculateRecordCostBreakdown(UsageRecord{Provider: &provider, Model: &model, InputTokens: 1_000_000, TotalTokens: 1_000_000}, channelPricesByKey(withBaseChannel))
	if !breakdown.Unpriced {
		t.Fatalf("base channel matched affixed usage: %#v", breakdown)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_prices (provider, model, price_scope, channel_brand, channel_key, input_usd_per_million, source, updated_at) VALUES ('Vendor A', 'gemini-4-argon-token', 'channel', 'openai_compatibility', 'vendor a', 11, 'manual', '2026-10-30T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	catalog, err = a.modelPriceCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item = catalog.Models[0]
	if item.Price == nil || item.Price.Model != model || item.Price.InputUSDPerMillion != 11 || item.TemplatePrice == nil || item.TemplatePrice.InputUSDPerMillion != 7 {
		t.Fatalf("saved channel = %#v, want unchanged saved rate plus separate template", item)
	}
}

func TestAvailableModelAffixPriceMetadata(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	const apiKey = "sk-isolated-affix-models"
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "gemini-4-argon-token", "owned_by": "gemini"},
			{"id": "vendor-gemini-4-argon-token", "owned_by": "unrelated"},
			{"id": "prefix-gemini-4-argon-token"},
		}})
	}))
	defer cpa.Close()
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = cpa.URL, ""
	if err := a.saveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	now := dbTime(time.Now())
	result, err := a.db.ExecContext(ctx, `INSERT INTO users (username, nickname, created_at, updated_at) VALUES ('member', 'Member', ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.upsertUserAPIKey(ctx, int(userID), hashAPIKey(apiKey), apiKey, "Isolated model metadata"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO model_prices (provider, model, input_usd_per_million, source, updated_at) VALUES ('gemini', 'gemini-4-argon', 7, 'manual', ?)`, now); err != nil {
		t.Fatal(err)
	}
	response, err := a.availableModelsForUser(ctx, int(userID))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 3 || len(response.Errors) != 0 {
		t.Fatalf("models = %#v, want all three authoritative IDs", response)
	}
	for _, item := range response.Models {
		if item.ID == "gemini-4-argon-token" {
			if item.Price == nil || item.Price.Model != "gemini-4-argon" || item.Price.InputUSDPerMillion != 7 {
				t.Fatalf("metadata = %#v, want base model reference price", item)
			}
		} else if item.Price != nil {
			t.Fatalf("metadata with missing/unrelated owner = %#v, want no global provider fallback", item)
		}
	}
}
