package app

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModelPriceCatalogPreservesProviderKeyMask(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	const rawKey = "sk-catalog-display-fixture-original-987654"
	const upstreamMask = "upstream-visible-prefix...visible-suffix"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/codex-api-key": []map[string]any{
			{"api-key": rawKey, "auth-index": "Not-The-API-Key.json", "models": []map[string]any{{"name": "display-model"}}},
			{"api-key-masked": upstreamMask, "api-key-hash": hashAPIKey("masked-upstream-key"), "auth-index": "Masked-Index.json", "models": []map[string]any{{"name": "display-model"}}},
			{"auth-index": "No-Key-Metadata.json", "models": []map[string]any{{"name": "display-model"}}},
		},
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "Multi Key Vendor", "api-key-entries": []map[string]any{{"api-key": "first-compatible-key"}, {"api-key": "second-compatible-key"}}, "models": []map[string]any{{"name": "display-model"}}},
		},
		"/v0/management/api-key-usage": map[string]any{},
		"/v0/management/auth-files": map[string]any{"files": []map[string]any{
			{"name": "codex-oauth.json", "type": "codex", "status": "active"},
		}},
		"/v0/management/auth-files/models": map[string]any{"models": []map[string]any{{"id": "oauth-display-model"}}},
	})
	defer cpa.Close()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin", "password": "test-password", "nickname": "Admin",
	}, nil, nil)
	cfg, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = cpa.URL, "test-management-key"
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var providers aiProvidersResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &providers)
	var native aiProviderItem
	maskByIdentity := map[modelPriceChannelGroupIdentity]*string{}
	for _, provider := range providers.Providers {
		identity := modelPriceChannelAliasKey("apikey", string(provider.Brand), provider.ChannelKey)
		maskByIdentity[identity] = provider.APIKeyMasked
		if provider.ChannelKey == "Not-The-API-Key.json" {
			native = provider
		}
		requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", modelPriceChannelAlias{
			AuthType: "apikey", ChannelBrand: string(provider.Brand), ChannelKey: provider.ChannelKey,
			ChannelIdentityHash: provider.IdentityHash, Label: "Display name " + provider.ChannelKey,
		}, cookies, nil)
	}
	assertCatalog := func(wantPriced bool) {
		t.Helper()
		var catalog ModelPriceCatalogResponse
		requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/catalog", nil, cookies, &catalog)
		if len(catalog.Models) != 5 {
			t.Fatalf("catalog rows = %d, want 5", len(catalog.Models))
		}
		for _, row := range catalog.Models {
			if row.ChannelBrand == "openai_compatibility" || row.ChannelAuthType == "oauth" {
				if row.ChannelAPIKeyMasked != nil {
					t.Fatal("a channel/pool must not present an arbitrary credential as its key")
				}
				continue
			}
			identity := modelPriceChannelAliasKey(row.ChannelAuthType, row.ChannelBrand, row.ChannelKey)
			if !reflect.DeepEqual(row.ChannelAPIKeyMasked, maskByIdentity[identity]) {
				t.Fatalf("catalog mask differs from its provider: key=%q mask=%v", row.ChannelKey, row.ChannelAPIKeyMasked)
			}
			if row.ChannelLabel != "Display name "+row.ChannelKey {
				t.Fatalf("mask metadata displaced the alias: %q", row.ChannelLabel)
			}
			if row.ChannelKey == "Masked-Index.json" && aiProviderOptionalString(row.ChannelAPIKeyMasked) != upstreamMask {
				t.Fatal("upstream-masked key was masked again")
			}
			if row.ChannelKey == "Not-The-API-Key.json" && (row.Price != nil) != wantPriced {
				t.Fatal("expected mask metadata on both priced and unpriced catalog rows")
			}
		}
		encoded, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{rawKey, "masked-upstream-key", "first-compatible-key", "second-compatible-key", "test-management-key"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("catalog leaked a fixture credential")
			}
		}
		if !strings.Contains(string(encoded), `"channel_api_key_masked":null`) {
			t.Fatal("missing mask must be explicit JSON null")
		}
	}
	assertCatalog(false)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices", map[string]any{
		"price_scope": "channel", "channel_auth_type": "apikey", "channel_brand": "codex",
		"channel_key": native.ChannelKey, "channel_identity_hash": native.IdentityHash,
		"provider": "openai", "model": "display-model", "input_usd_per_million": 2, "output_usd_per_million": 4,
		"cache_read_usd_per_million": 0, "cache_creation_usd_per_million": 0,
	}, cookies, nil)
	assertCatalog(true)
}

func TestUsageSourceChannelsFollowSelectorSnapshotLifecycle(t *testing.T) {
	key, index := "snapshot-original-key", "Snapshot-Index.json"
	hash := hashAPIKey(key)
	provider := aiProviderItem{Brand: aiProviderBrandCodex, APIKeyHash: &hash, AuthIndex: &index}
	channels := usageSourceChannels([]aiProviderItem{provider})
	identity := modelPriceChannelAliasKey("apikey", "codex", index)
	cache := modelPriceSelectorSnapshotCache{}
	cache.retainConfig("config-a")
	generation, ok := cache.currentGeneration("config-a")
	if !ok || !cache.storeWithLabels("config-a", generation, time.Now(), modelPriceChannelSelectorIndex{}, nil, channels) {
		t.Fatal("could not store source identity snapshot")
	}
	channels[identity] = usageSourceChannel{Count: 9, APIKeyHash: "mutated-input"}
	_, _, first, available := cache.snapshotForConfigWithLabels("config-a")
	if !available || first[identity].Count != 1 || first[identity].APIKeyHash != hash {
		t.Fatal("cache retained a mutable source identity map")
	}
	first[identity] = usageSourceChannel{Count: 9, APIKeyHash: "mutated-snapshot"}
	_, _, second, available := cache.snapshotWithLabels()
	if !available || second[identity].Count != 1 || second[identity].APIKeyHash != hash {
		t.Fatal("snapshot mutated cached source identity metadata")
	}
	if _, _, other, available := cache.snapshotForConfigWithLabels("config-b"); available || other != nil {
		t.Fatal("source identities crossed management configurations")
	}
	cache.expiresAt = time.Now().Add(-time.Second)
	if _, _, expired, available := cache.snapshotWithLabels(); available || expired != nil {
		t.Fatal("source identities outlived selector snapshot TTL")
	}
	cache.invalidate("config-a")
	if cache.storeWithLabels("config-a", generation, time.Now(), nil, nil, channels) {
		t.Fatal("old generation restored source identities after invalidation")
	}
	load, refreshingGeneration, _, done := cache.beginRefresh("config-a", time.Now())
	if !load {
		t.Fatal("could not start a fresh selector refresh")
	}
	cache.retainConfig("config-b")
	cache.finishRefreshWithLabels("config-a", refreshingGeneration, done, time.Now(), nil, nil, channels, nil)
	if _, _, stale, available := cache.snapshotWithLabels(); available || stale != nil {
		t.Fatal("in-flight refresh restored source identities from the old configuration")
	}
}
