package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// This fixture exercises the native channel pencil's real local alias API.
// The host IDs stay fixed; changing an OpenAI-compatible Provider name is a
// different operation and must not stand in for the reported native case.
func TestPluginNativeAliasPreservesPolicyReferences(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	const primaryKey = "native-primary-secret-key-1234"
	const secondaryKey = "native-secondary-secret-key-5678"
	const primaryID = "codex:apikey:7bfa63c8989f"
	const secondaryID = "codex:apikey:ed63d44889c3"
	primaryRef, secondaryRef := pluginCredentialRef(primaryID), pluginCredentialRef(secondaryID)
	var mu sync.Mutex
	downstream := []string{}
	requests := []string{}
	policy := map[string]any{
		"contract_version": "v1", "plugin_version": "0.1.7", "policy_revision": 1,
		"groups": []any{}, "keys": []any{},
	}
	config := map[string]any{
		"api-keys": map[string]any{"codex": []any{
			map[string]any{"base-url": "https://native-a.invalid", "keys": []any{map[string]any{"api-key": primaryKey, "auth_index": "Native-A"}}},
			map[string]any{"base-url": "https://native-b.invalid", "keys": []any{map[string]any{"api-key": secondaryKey, "auth_index": "Native-B"}}},
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities":
			payload = map[string]any{"contract_version": "v1", "plugin_version": "0.1.7", "cpa_version": "v8.0.29", "modules": []string{"model_list_filter"}, "model_list_filter_enabled": true}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/directory":
			payload = map[string]any{"credentials": []any{}}
		case "GET /v8/management/config":
			config["access"] = map[string]any{"api-keys": downstream}
			payload = config
		case "GET /v8/management/credentials":
			payload = map[string]any{"files": []any{
				map[string]any{"id": primaryID, "auth_index": "Native-A", "source": "memory", "provider": "codex", "name": "Old runtime label", "status": "active"},
				map[string]any{"id": secondaryID, "auth_index": "Native-B", "source": "memory", "provider": "codex", "name": "Other runtime label", "status": "active"},
			}}
		case "GET /v0/management/codex-api-key":
			payload = []any{
				map[string]any{"api-key": primaryKey, "base-url": "https://native-a.invalid", "auth-index": "Native-A"},
				map[string]any{"api-key": secondaryKey, "base-url": "https://native-b.invalid", "auth-index": "Native-B"},
			}
		case "GET /v0/management/gemini-api-key", "GET /v0/management/claude-api-key", "GET /v0/management/openai-compatibility", "GET /v0/management/vertex-api-key", "GET /v0/management/xai-api-key":
			payload = []any{}
		case "GET /v0/management/api-keys":
			payload = map[string]any{"api-keys": downstream}
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&downstream); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload = map[string]any{"ok": true}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/policy":
			w.Header().Set("ETag", fmt.Sprintf("\"%v\"", policy["policy_revision"]))
			payload = policy
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/policy":
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%v\"", policy["policy_revision"]) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload = map[string]any{"policy_revision": policy["policy_revision"]}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			payload = map[string]any{"policy_revision": policy["policy_revision"], "active": map[string]int{}}
		case "GET /v1/models":
			// The routing fixture has one owner per public ID. Both group and
			// independent deny references must survive every policy round trip.
			visible := []any{}
			encoded, _ := json.Marshal(policy)
			if strings.Contains(string(encoded), primaryRef) && strings.Contains(string(encoded), secondaryRef) {
				visible = append(visible, map[string]any{"id": "route-model"})
			}
			payload = map[string]any{"data": visible}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	handler := a.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "native-admin", "password": "native-password", "nickname": "Native"}, nil, nil)
	cfg, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = server.URL, "native-management-secret"
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var key UserApiKeySummary
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "native-route-key"}, cookies, &key)
	readPolicy := func() map[string]json.RawMessage {
		var value map[string]json.RawMessage
		requestJSONForPricingTest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, cookies, &value)
		return value
	}
	current := readPolicy()
	current["groups"] = mustRaw([]any{map[string]any{"id": "native-group", "name": "Native group", "rule": map[string]any{"credential_ids": []string{primaryRef}, "denied_credential_ids": []string{secondaryRef}}}})
	current["keys"] = mustRaw([]any{map[string]any{"id": pluginCallerScope(*key.APIKey), "enabled": true, "group_ids": []string{"native-group"}, "rule": map[string]any{"credential_ids": []string{primaryRef}, "denied_credential_ids": []string{secondaryRef}}}})
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", current, cookies, &current)
	type directory struct {
		Models      []string         `json:"models"`
		Credentials []map[string]any `json:"credentials"`
	}
	readDirectory := func() directory {
		var value directory
		requestJSONForPricingTest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+key.APIKeyHash, nil, cookies, &value)
		return value
	}
	before := readDirectory()
	identities := func(value directory) []map[string]any {
		result := make([]map[string]any, 0, len(value.Credentials))
		for _, item := range value.Credentials {
			result = append(result, map[string]any{"ref": item["ref"], "provider": item["provider"], "source": item["source"], "auth_index": item["auth_index"]})
		}
		return result
	}
	if len(before.Credentials) != 2 {
		t.Fatalf("native fixture host IDs did not merge with configuration: %#v", identities(before))
	}
	beforeGroups, beforeKeys := current["groups"], current["keys"]
	for _, name := range []string{"Primary channel", "Renamed channel", "Same channel", ""} {
		mu.Lock()
		start := len(requests)
		mu.Unlock()
		requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/channel-aliases", modelPriceChannelAlias{
			AuthType: "apikey", ChannelBrand: "codex", ChannelKey: "Native-A", ChannelIdentityHash: hashAPIKey(primaryKey), Label: name,
		}, cookies, nil)
		mu.Lock()
		aliasRequests := append([]string(nil), requests[start:]...)
		mu.Unlock()
		for _, request := range aliasRequests {
			if !strings.HasPrefix(request, "GET ") {
				t.Fatalf("native alias wrote host state or policy: %s", request)
			}
		}
		after := readDirectory()
		if !reflect.DeepEqual(identities(before), identities(after)) || !reflect.DeepEqual(before.Models, after.Models) || !reflect.DeepEqual(after.Models, []string{"route-model"}) {
			t.Fatalf("native rename changed identities or routability: before=%#v after=%#v models=%#v", identities(before), identities(after), after.Models)
		}
		latest := readPolicy()
		if !jsonValueEqual(beforeGroups, latest["groups"]) || !jsonValueEqual(beforeKeys, latest["keys"]) {
			t.Fatal("native alias changed group/key allow or deny policy references")
		}
		requestJSONForPricingTest(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", latest, cookies, &latest)
		if !jsonValueEqual(beforeGroups, latest["groups"]) || !jsonValueEqual(beforeKeys, latest["keys"]) {
			t.Fatal("saving refreshed native rule changed references")
		}
		for _, item := range after.Credentials {
			if item["ref"] == primaryRef && name != "" && !strings.HasPrefix(pluginAnyText(item["label"]), name+" · ") {
				t.Errorf("native ref and policy stayed stable, but alias %q is missing from directory label %q", name, item["label"])
			}
		}
		encoded, _ := json.Marshal(after)
		for _, secret := range []string{primaryKey, secondaryKey, "native-management-secret", *key.APIKey, primaryID, secondaryID} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("native directory exposed a raw host identity or credential")
			}
		}
	}
}
