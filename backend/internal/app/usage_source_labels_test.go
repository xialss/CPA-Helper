package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsageSourceLabelsUnpricedAliasLifecycleRoutes(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	const nativeKey = "sk-native-label-fixture-original-123456"
	const compatibilityKey = "sk-compatibility-label-fixture-654321"
	cpa := newModelPriceCatalogManagementServer(t, map[string]any{
		"/v0/management/gemini-api-key": []map[string]any{
			{"api-key": nativeKey, "auth-index": "Auth-Not-The-Key.json", "models": []map[string]any{{"name": "unpriced-model"}}},
		},
		"/v0/management/openai-compatibility": []map[string]any{
			{"name": "Fixture Vendor", "api-key-entries": []map[string]any{{"api-key": compatibilityKey}, {"api-key": "sk-other-compatible-fixture"}}, "models": []map[string]any{{"name": "unpriced-model"}}},
		},
		"/v0/management/api-key-usage": map[string]any{},
	})
	defer cpa.Close()
	var managementCalls atomic.Int32
	managementHandler := cpa.Config.Handler
	cpa.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managementCalls.Add(1)
		managementHandler.ServeHTTP(w, r)
	})
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
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
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = cpa.URL, "test-management-key"
	if err := a.saveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var providers aiProvidersResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/ai-providers", nil, adminCookies, &providers)
	providerByBrand := map[aiProviderBrand]aiProviderItem{}
	for _, provider := range providers.Providers {
		providerByBrand[provider.Brand] = provider
	}
	seed := func(username, provider, authIndex, source, requestID string) int64 {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"auth_type": "api_key", "auth_index": authIndex, "source": source,
			"provider": provider, "model": "unpriced-model", "request_id": requestID,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.db.Exec(`INSERT INTO usage_records
			(created_at, timestamp, usage_username, provider, model, source, request_id, auth, auth_index,
			 input_tokens, output_tokens, total_tokens, dedupe_key, raw_json)
			VALUES (?, ?, ?, ?, 'unpriced-model', ?, ?, 'API-KEY', ?, 10, 2, 12, ?, ?)`,
			dbTime(time.Now()), dbTime(time.Now().Add(-time.Minute)), username, provider, source, requestID, authIndex, requestID, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	nativeID := seed("admin", "gemini", "Auth-Not-The-Key.json", nativeKey, "native-label-request")
	compatibleID := seed("member", "openai-compatible-fixture vendor", "compatibility-entry-index", compatibilityKey, "compatible-label-request")
	type recordResponse struct {
		ID               int64          `json:"id"`
		Source           string         `json:"source"`
		SourceLabel      *string        `json:"source_label"`
		AuthIndex        *string        `json:"auth_index"`
		EstimatedCostUSD float64        `json:"estimated_cost_usd"`
		Unpriced         bool           `json:"unpriced"`
		CostBreakdown    map[string]any `json:"cost_breakdown"`
		RawJSON          map[string]any `json:"raw_json"`
	}
	type recordsResponse struct {
		Items []recordResponse `json:"items"`
		Total int              `json:"total"`
	}
	rangeQuery := "&" + url.Values{
		"start": {time.Now().Add(-time.Hour).Format(time.RFC3339)},
		"end":   {time.Now().Add(time.Hour).Format(time.RFC3339)},
	}.Encode()
	readRecords := func(path string, cookies []*http.Cookie) recordsResponse {
		t.Helper()
		var response recordsResponse
		beforeCalls := managementCalls.Load()
		requestJSONForPricingTest(t, handler, http.MethodGet, path+rangeQuery, nil, cookies, &response)
		if managementCalls.Load() != beforeCalls {
			t.Fatal("usage list performed a synchronous management request")
		}
		return response
	}
	readDetail := func(id int64, scope string, cookies []*http.Cookie) recordResponse {
		t.Helper()
		var response recordResponse
		beforeCalls := managementCalls.Load()
		requestJSONForPricingTest(t, handler, http.MethodGet, fmt.Sprintf("/api/usage/records/%d?scope=%s", id, scope), nil, cookies, &response)
		if managementCalls.Load() != beforeCalls {
			t.Fatal("usage detail performed a synchronous management request")
		}
		return response
	}
	nativeSource := nativeKey
	filteredPath := "/api/usage/records?scope=admin&source_key=" + *usageSourceKey(&nativeSource) + "&request_id=native-label-request"
	before := readRecords(filteredPath, adminCookies)
	if before.Total != 1 || len(before.Items) != 1 || before.Items[0].ID != nativeID || before.Items[0].SourceLabel != nil || !before.Items[0].Unpriced {
		t.Fatalf("initial unpriced source response = %#v", before)
	}
	writeAlias := func(brand aiProviderBrand, label string) {
		t.Helper()
		provider := providerByBrand[brand]
		requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", modelPriceChannelAlias{
			AuthType: "apikey", ChannelBrand: string(brand), ChannelKey: provider.ChannelKey,
			ChannelIdentityHash: provider.IdentityHash, Label: label,
		}, adminCookies, nil)
	}
	for _, label := range []string{"Primary channel", "Renamed channel", ""} {
		writeAlias(aiProviderBrandGemini, label)
		after := readRecords(filteredPath, adminCookies)
		if after.Total != before.Total || len(after.Items) != 1 || after.Items[0].ID != nativeID {
			t.Fatalf("rename changed source/request-ID filtering: %#v", after)
		}
		for _, item := range []recordResponse{after.Items[0], readDetail(nativeID, "admin", adminCookies)} {
			if aiProviderOptionalString(item.SourceLabel) != label || item.Source != maskSecret(&nativeSource) || !item.Unpriced || item.EstimatedCostUSD != 0 {
				t.Fatalf("label lifecycle response = %#v, want label %q and unchanged unpriced source", item, label)
			}
			if !reflect.DeepEqual(item.CostBreakdown, before.Items[0].CostBreakdown) {
				t.Fatal("source alias changed the cost breakdown")
			}
			if item.RawJSON != nil && item.RawJSON["source"] != maskSecret(&nativeSource) {
				t.Fatal("source alias changed raw-detail redaction")
			}
		}
	}
	writeAlias(aiProviderBrandGemini, "Shared display name")
	for _, label := range []string{"Shared display name", "Renamed compatibility", ""} {
		writeAlias(aiProviderBrandOpenAICompatibility, label)
		want := label
		if want == "" {
			want = "Fixture Vendor"
		}
		item := readDetail(compatibleID, "admin", adminCookies)
		if aiProviderOptionalString(item.SourceLabel) != want || item.Source == compatibilityKey {
			t.Fatalf("compatible alias/fallback response = %#v, want %q", item, want)
		}
		all := readRecords("/api/usage/records?scope=admin", adminCookies)
		if all.Total != 2 || len(all.Items) != 2 {
			t.Fatal("duplicate display names merged channels")
		}
	}
	writeAlias(aiProviderBrandOpenAICompatibility, "Private administrator alias")
	for _, path := range []string{"/api/usage/records?scope=account", "/api/usage/records?scope=admin"} {
		member := readRecords(path, memberCookies)
		if member.Total != 1 || len(member.Items) != 1 || member.Items[0].ID != compatibleID || member.Items[0].SourceLabel != nil {
			t.Fatalf("member list exposed administrator metadata or another user: %#v", member)
		}
	}
	if item := readDetail(compatibleID, "admin", memberCookies); item.SourceLabel != nil || aiProviderOptionalString(item.AuthIndex) == "compatibility-entry-index" {
		t.Fatal("member detail exposed administrator metadata")
	}
	if item := readDetail(nativeID, "account", adminCookies); item.SourceLabel != nil {
		t.Fatal("administrator account scope exposed channel metadata")
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, fmt.Sprintf("/api/usage/records/%d?scope=admin", nativeID), nil, memberCookies, http.StatusNotFound)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, "/api/usage/records?scope=admin", nil, nil, http.StatusUnauthorized)
	var options struct {
		Sources []struct{ Key, Label string } `json:"sources"`
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/usage/options?scope=admin"+rangeQuery, nil, adminCookies, &options)
	foundSource := false
	for _, source := range options.Sources {
		if source.Key == *usageSourceKey(&nativeSource) {
			foundSource = source.Label == maskSecret(&nativeSource)
		}
	}
	if !foundSource {
		t.Fatal("rename changed source option identity or masking")
	}
	var storedSource, storedRaw string
	if err := a.db.QueryRow(`SELECT source, raw_json FROM usage_records WHERE id = ?`, nativeID).Scan(&storedSource, &storedRaw); err != nil {
		t.Fatal(err)
	}
	if storedSource != nativeKey || !strings.Contains(storedRaw, nativeKey) {
		t.Fatal("display projection rewrote stored usage")
	}
	if _, err := a.db.Exec(`DROP TABLE model_price_channel_aliases`); err != nil {
		t.Fatal(err)
	}
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, "/api/usage/records?scope=admin"+rangeQuery, nil, adminCookies, http.StatusInternalServerError)
	requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, fmt.Sprintf("/api/usage/records/%d?scope=admin", nativeID), nil, adminCookies, http.StatusInternalServerError)
	readRecords("/api/usage/records?scope=account", memberCookies)
}

func TestUsageSourceLabelIdentityEvidence(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	text := func(value string) *string { return &value }
	native := func(brand aiProviderBrand, index, key string) aiProviderItem {
		return aiProviderItem{Brand: brand, AuthIndex: text(index), APIKeyHash: text(hashAPIKey(key))}
	}
	compatible := func(name string) aiProviderItem {
		return aiProviderItem{Brand: aiProviderBrandOpenAICompatibility, Name: text(name)}
	}
	withModels := func(provider aiProviderItem, models ...string) aiProviderItem {
		for _, model := range models {
			provider.Models = append(provider.Models, aiProviderModel{Name: model})
		}
		return provider
	}
	alias := func(brand, key, label string) modelPriceChannelAlias {
		return modelPriceChannelAlias{AuthType: "apikey", ChannelBrand: brand, ChannelKey: key, Label: label}
	}
	baseRecord := UsageRecord{
		Provider: text("gemini"), Model: text("not-priced-or-configured"), Auth: text("API-KEY"),
		AuthIndex: text("Auth.json"), Source: text("fixture-key-one"), RawJSON: `{"auth_type":"api_key"}`,
	}
	noIndex := baseRecord
	noIndex.AuthIndex = nil
	google := baseRecord
	google.Provider = text("google")
	noModel := baseRecord
	noModel.Model = nil
	compat := baseRecord
	compat.Provider, compat.AuthIndex = text("openai-compatible-fixture vendor"), nil
	nativeNamedCompat := noIndex
	nativeNamedCompat.Source = text("fixture-compatible-key")
	noAuth := baseRecord
	noAuth.RawJSON = `{}`
	authConflict := baseRecord
	authConflict.RawJSON = `{"auth_type":"oauth"}`
	indexConflict := baseRecord
	indexConflict.RawJSON = `{"auth_type":"apikey","auth_index":"different-index"}`
	wrongSource := baseRecord
	wrongSource.Source = text("different-complete-key")
	masked := noIndex
	masked.Source = text("fixt...one")
	missingProvider := baseRecord
	missingProvider.Provider = nil
	keyhash := "keyhash:" + hashAPIKey("fixture-key-one")
	nativeAlias := alias("gemini", "Auth.json", "Primary")
	hashAlias := alias("gemini", keyhash, "Missing-index name")
	sameNameCompatible := withModels(compatible("gemini"), *baseRecord.Model)
	sameNameCompatible.APIKeyEntries = []aiProviderKeyEntry{{APIKeyHash: text(hashAPIKey("different-complete-key"))}}
	cases := []struct {
		name      string
		record    UsageRecord
		providers []aiProviderItem
		aliases   []modelPriceChannelAlias
		want      string
	}{
		{"native without a price or configured model", baseRecord, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"native selector remains case sensitive", baseRecord, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), native(aiProviderBrandGemini, "auth.json", "fixture-key-two")}, []modelPriceChannelAlias{nativeAlias, alias("gemini", "auth.json", "Lowercase")}, "Primary"},
		{"no retained raw authentication evidence", noAuth, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"conflicting authentication evidence", authConflict, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"conflicting auth indexes", indexConflict, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"conflicting key and auth index", wrongSource, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"duplicate native selector", baseRecord, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), native(aiProviderBrandGemini, "Auth.json", "fixture-key-two")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"compatible name collides with native brand", baseRecord, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), compatible("gemini")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"compatible name collides without auth index", noIndex, []aiProviderItem{native(aiProviderBrandGemini, "", "fixture-key-one"), compatible("gemini")}, []modelPriceChannelAlias{hashAlias}, ""},
		{"google includes Gemini and Vertex", google, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), native(aiProviderBrandVertex, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"google has one exact native selector", google, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), native(aiProviderBrandVertex, "Vertex.json", "fixture-key-two")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"model uniquely selects Gemini over Vertex", google, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model), withModels(native(aiProviderBrandVertex, "Auth.json", "fixture-key-two"), "other-model")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"model uniquely selects native over compatible", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model), withModels(compatible("gemini"), "other-model")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"model uniquely selects compatible over native", nativeNamedCompat, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), withModels(compatible("gemini"), *baseRecord.Model)}, []modelPriceChannelAlias{alias("openai_compatibility", "gemini", "Compatibility")}, "Compatibility"},
		{"model change rejects native credentials before compatible alias", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{nativeAlias, alias("openai_compatibility", "gemini", "Compatibility")}, ""},
		{"model change rejects native credentials before compatible upstream name", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"model change rejects keyhash credentials before compatible alias", noIndex, []aiProviderItem{withModels(native(aiProviderBrandGemini, "", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{hashAlias, alias("openai_compatibility", "gemini", "Compatibility")}, ""},
		{"model change rejects duplicate native credentials before compatible alias", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-two"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{nativeAlias, alias("openai_compatibility", "gemini", "Compatibility")}, ""},
		{"model change rejects reversed duplicate native credentials before compatible alias", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-two"), "other-model"), withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{nativeAlias, alias("openai_compatibility", "gemini", "Compatibility")}, ""},
		{"model match keeps compatible alias for a different complete key", wrongSource, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{nativeAlias, alias("openai_compatibility", "gemini", "Compatibility")}, "Compatibility"},
		{"model match keeps compatible alias without masked key evidence", masked, []aiProviderItem{withModels(native(aiProviderBrandGemini, "", "fixture-key-one"), "other-model"), sameNameCompatible}, []modelPriceChannelAlias{hashAlias, alias("openai_compatibility", "gemini", "Compatibility")}, "Compatibility"},
		{"same model native and compatible conflict", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model), withModels(compatible("gemini"), *baseRecord.Model)}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"same model conflict without auth index", nativeNamedCompat, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model), withModels(compatible("gemini"), *baseRecord.Model)}, []modelPriceChannelAlias{alias("openai_compatibility", "gemini", "Compatibility")}, ""},
		{"model match rejects mismatched source hash", wrongSource, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model)}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"unconfigured model keeps exact identity fallback", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"missing model keeps exact identity fallback", noModel, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), "other-model")}, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"duplicate native identity with different models rejects first hash", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model), withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-two"), "other-model")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"duplicate native identity with different models rejects reversed hashes", baseRecord, []aiProviderItem{withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-two"), "other-model"), withModels(native(aiProviderBrandGemini, "Auth.json", "fixture-key-one"), *baseRecord.Model)}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"duplicate compatible identity with different models", compat, []aiProviderItem{withModels(compatible("Fixture Vendor"), *baseRecord.Model), withModels(compatible("fixture vendor"), "other-model")}, []modelPriceChannelAlias{alias("openai_compatibility", "fixture vendor", "Compatibility")}, ""},
		{"compatible upstream name", compat, []aiProviderItem{compatible("Fixture Vendor")}, nil, "Fixture Vendor"},
		{"compatible prefix and canonical alias", compat, []aiProviderItem{compatible("Fixture Vendor")}, []modelPriceChannelAlias{alias("openai_compatibility", "FIXTURE VENDOR", "Compatibility")}, "Compatibility"},
		{"compatible exact and prefix candidates conflict", compat, []aiProviderItem{compatible("Fixture Vendor"), compatible("openai-compatible-fixture vendor")}, nil, ""},
		{"duplicate compatible names conflict", compat, []aiProviderItem{compatible("Fixture Vendor"), compatible("fixture vendor")}, nil, ""},
		{"missing record auth index is not inferred", noIndex, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
		{"keyhash exact original source", noIndex, []aiProviderItem{native(aiProviderBrandGemini, "", "fixture-key-one")}, []modelPriceChannelAlias{hashAlias}, "Missing-index name"},
		{"keyhash rejects masked source", masked, []aiProviderItem{native(aiProviderBrandGemini, "", "fixture-key-one")}, []modelPriceChannelAlias{hashAlias}, ""},
		{"keyhash rejects duplicate credential", noIndex, []aiProviderItem{native(aiProviderBrandGemini, "", "fixture-key-one"), native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{hashAlias}, ""},
		{"retained exact historical identity", baseRecord, nil, []modelPriceChannelAlias{nativeAlias}, "Primary"},
		{"retained compatible name", compat, nil, []modelPriceChannelAlias{alias("openai_compatibility", "fixture vendor", "Historical vendor")}, "Historical vendor"},
		{"historical keyhash still requires current evidence", noIndex, nil, []modelPriceChannelAlias{hashAlias}, ""},
		{"missing provider", missingProvider, []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "fixture-key-one")}, []modelPriceChannelAlias{nativeAlias}, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := a.db.Exec(`DELETE FROM model_price_channel_aliases`); err != nil {
				t.Fatal(err)
			}
			for _, item := range test.aliases {
				if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases
					(auth_type, channel_brand, channel_key, label, created_at, updated_at) VALUES (?, ?, ?, ?, '', '')`,
					item.AuthType, item.ChannelBrand, canonicalModelPriceChannelKey(item.ChannelBrand, item.ChannelKey), item.Label); err != nil {
					t.Fatal(err)
				}
			}
			match := modelPriceMatchContext{SelectorsAvailable: true, Selectors: modelPriceChannelSelectors(test.providers), SourceChannels: usageSourceChannels(test.providers), ChannelLabels: modelPriceChannelLabels(test.providers)}
			labels, err := a.usageSourceLabels(context.Background(), match)
			if err != nil {
				t.Fatal(err)
			}
			if got := aiProviderOptionalString(labels.labelFor(test.record)); got != test.want {
				t.Fatalf("label = %q, want %q", got, test.want)
			}
			match.SelectorsAvailable = false
			labels, err = a.usageSourceLabels(context.Background(), match)
			if err != nil || labels.labelFor(test.record) != nil {
				t.Fatalf("unavailable snapshot supplied a label: %v", err)
			}
		})
	}
}
