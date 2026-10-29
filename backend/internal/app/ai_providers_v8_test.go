package app_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func aiProviderV8Fixture(t *testing.T, raw string) map[string]any {
	t.Helper()
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	return config
}

func aiProviderSnapshotMaps(t *testing.T, handler http.Handler, cookies []*http.Cookie) []map[string]any {
	t.Helper()
	body := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var response struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Providers
}

func aiProviderSnapshotPayload(provider map[string]any) map[string]any {
	payload := map[string]any{}
	for _, field := range []string{"brand", "index", "identity_hash", "source_hash", "api_key_hash", "name", "base_url", "models", "headers", "api_key_entries", "excluded_models", "disabled"} {
		if value, exists := provider[field]; exists {
			payload[field] = value
		}
	}
	return payload
}

func aiProviderOrderMaps(providers []map[string]any, indexes ...int) map[string]any {
	order := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		provider := providers[index]
		order = append(order, map[string]any{
			"index": provider["index"], "identity_hash": provider["identity_hash"],
			"source_hash": provider["source_hash"], "api_key_hash": provider["api_key_hash"],
			"name": provider["name"], "base_url": provider["base_url"],
		})
	}
	return map[string]any{"order": order}
}

func aiProviderV8GroupsForTest(fake *fakeAIProviderManagement, family string) []any {
	return fake.v8Config["api-keys"].(map[string]any)[family].([]any)
}

func TestAIProviderV8InheritancePreservesRawConfiguration(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"unrelated":{"n":9007199254740993123},"api-keys":{"codex":[{"name":"external","base-url":"https://same.example","disable-cooling":true,"request-retry":4,"request-scoped-errors":[{"status":429,"match":["  exact\n text  "],"action":"continue","future":{"n":9007199254740993123}}],"unknown-group":{"n":9007199254740993123},"keys":[{"api-key":"codex-preserved-secret","request-retry":null,"disable-codex-cloaking":null,"unknown-key":{"false":false,"zero":0,"null":null,"empty":[]}}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	provider := providers[0]
	if provider["request_retry"] != nil || provider["disable_cooling"] != nil || provider["request_scoped_errors"] != nil {
		t.Fatalf("inherited policy exposed as local override: %#v", provider)
	}
	effective := provider["runtime_policy_effective"].(map[string]any)
	if effective["request_retry"] != float64(4) || effective["disable_cooling"] != true {
		t.Fatalf("effective policy = %#v", effective)
	}
	if provider["source_hash"] == "" || provider["name"] != nil {
		t.Fatalf("raw group identity leaked into native name: %#v", provider)
	}
	for _, secret := range []string{"codex-preserved-secret", "9007199254740993123"} {
		encoded, _ := json.Marshal(provider)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("snapshot exposed private raw value %q", secret)
		}
	}
	payload := aiProviderSnapshotPayload(provider)
	payload["priority"] = 7
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	group := aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)
	key := group["keys"].([]any)[0].(map[string]any)
	if value, exists := key["request-retry"]; !exists || value != nil {
		t.Fatalf("untouched explicit null lost: %#v", key)
	}
	if _, exists := key["disable-cooling"]; exists {
		t.Fatalf("inherited cooling materialized into key: %#v", key)
	}
	if _, exists := key["request-scoped-errors"]; exists {
		t.Fatalf("inherited rules materialized into key: %#v", key)
	}
	if key["api-key"] != "codex-preserved-secret" || key["priority"] != json.Number("7") {
		t.Fatalf("updated key = %#v", key)
	}
	unknown := group["unknown-group"].(map[string]any)
	if unknown["n"] != json.Number("9007199254740993123") {
		t.Fatalf("large opaque integer changed: %#v", unknown)
	}
	fake.mu.Unlock()

	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[0])
	payload["disable_cooling"] = false
	payload["request_retry"] = 0
	payload["request_scoped_errors"] = []any{}
	payload["disable_codex_cloaking"] = false
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	key = aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
	if key["disable-cooling"] != false || key["request-retry"] != json.Number("0") || len(key["request-scoped-errors"].([]any)) != 0 || key["disable-codex-cloaking"] != false {
		t.Fatalf("false/0/[] overrides lost: %#v", key)
	}
	fake.mu.Unlock()

	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[0])
	payload["request_retry"] = nil
	payload["disable_cooling"] = nil
	payload["request_scoped_errors"] = nil
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key = aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
	for _, field := range []string{"request-retry", "disable-cooling", "request-scoped-errors"} {
		if _, exists := key[field]; exists {
			t.Fatalf("clear retained local override %s: %#v", field, key)
		}
	}
}

func TestAIProviderV8SameBaseURLConfigurationsStayIndependent(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://same.example","request-retry":1,"keys":[{"api-key":"same-url-first-secret"}]},{"name":"two","base-url":"https://same.example","request-retry":2,"keys":[{"api-key":"same-url-second-secret"}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	payload := aiProviderSnapshotPayload(providers[1])
	payload["original_base_url"] = payload["base_url"]
	payload["base_url"] = "https://second.example"
	payload["alpha_search"] = true
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/1", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	groups := aiProviderV8GroupsForTest(fake, "codex")
	if len(groups) != 2 || groups[0].(map[string]any)["base-url"] != "https://same.example" || groups[1].(map[string]any)["base-url"] != "https://second.example" {
		t.Fatalf("same URL configurations merged or edited together: %#v", groups)
	}
	fake.mu.Unlock()
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/order", aiProviderOrderMaps(providers, 1, 0), cookies, http.StatusOK)
	fake.mu.Lock()
	groups = aiProviderV8GroupsForTest(fake, "codex")
	if groups[0].(map[string]any)["name"] != "two" || groups[1].(map[string]any)["name"] != "one" {
		t.Fatalf("configuration order = %#v", groups)
	}
	fake.mu.Unlock()
	requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/codex", map[string]any{"api_key": "same-url-third-secret", "base_url": "https://same.example", "models": []any{}, "headers": []any{}}, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(aiProviderV8GroupsForTest(fake, "codex")) != 3 {
		t.Fatal("creating a same URL configuration merged groups")
	}
}

func TestAIProviderV8ExternalMultiKeyGroupSafety(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"external","base-url":"https://external.example","request-retry":3,"future":"group-metadata","keys":[{"api-key":"external-key-one"},{"api-key":"external-key-two"}]},{"name":"separate","base-url":"https://external.example","keys":[{"api-key":"separate-key"}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	payload := aiProviderSnapshotPayload(providers[0])
	payload["request_retry"] = 0
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	group := aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)
	keys := group["keys"].([]any)
	if group["request-retry"] != json.Number("3") || keys[0].(map[string]any)["request-retry"] != json.Number("0") || keys[1].(map[string]any)["request-retry"] != nil {
		t.Fatalf("per-key update changed shared or sibling settings: %#v", group)
	}
	puts := fake.providerPutCount
	fake.mu.Unlock()
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[0])
	payload["base_url"] = "https://unsafe.example"
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/order", aiProviderOrderMaps(providers, 0, 2, 1), cookies, http.StatusConflict)
	fake.mu.Lock()
	if fake.providerPutCount != puts {
		t.Fatal("unsafe operation wrote upstream before rejecting")
	}
	fake.mu.Unlock()
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/order", aiProviderOrderMaps(providers, 2, 1, 0), cookies, http.StatusOK)
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	provider := providers[1]
	query := url.Values{"identity_hash": {provider["identity_hash"].(string)}, "source_hash": {provider["source_hash"].(string)}}
	requestRawJSON(t, handler, http.MethodDelete, "/api/ai-providers/codex/1?"+query.Encode(), nil, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	group = aiProviderV8GroupsForTest(fake, "codex")[1].(map[string]any)
	keys = group["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["api-key"] != "external-key-one" || group["future"] != "group-metadata" {
		t.Fatalf("delete did not preserve sibling/group: %#v", group)
	}
}

func TestAIProviderV8RejectsStaleSourceAndIgnoredWrite(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://one.example","keys":[{"api-key":"source-secret"}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	payload := aiProviderSnapshotPayload(providers[0])
	payload["alpha_search"] = true
	fake.mu.Lock()
	aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)["request-retry"] = json.Number("3")
	fake.mu.Unlock()
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	fake.mu.Lock()
	if fake.providerPutCount != 0 {
		t.Fatal("stale source wrote upstream")
	}
	fake.v8IgnoreFields = []string{"alpha-search"}
	fake.mu.Unlock()
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[0])
	payload["alpha_search"] = true
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	if !bytes.Contains(body, []byte("provider_config_committed_refresh_required")) || bytes.Contains(body, []byte("source-secret")) {
		t.Fatalf("ignored write response = %s", body)
	}
}

func TestAIProviderV8ErrorsDoNotFallBackToLegacy(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`},
		{"server error", http.StatusInternalServerError, `{}`},
		{"invalid JSON", http.StatusOK, `{`},
		{"null", http.StatusOK, `null`},
		{"invalid family", http.StatusOK, `{"api-keys":{"codex":{}}}`},
		{"invalid keys", http.StatusOK, `{"api-keys":{"codex":[{"keys":null}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var legacy atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v8/management/config" {
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				if strings.HasPrefix(r.URL.Path, "/v0/management/") && r.URL.Path != "/v0/management/usage-statistics-enabled" {
					legacy.Add(1)
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			legacy.Store(0)
			requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusUnprocessableEntity)
			if legacy.Load() != 0 {
				t.Fatalf("v8 failure triggered %d legacy reads", legacy.Load())
			}
		})
	}
}

func TestAIProviderV8OpenAIGroupKeepsKeysAndRawValues(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"openai-compatibility":[{"name":"compatible","base-url":"https://compatible.example","request-retry":null,"request-scoped-errors":[],"headers":null,"future":{"n":9007199254740993123},"keys":[{"api-key":"openai-key-one","future":{"n":9007199254740993123}},{"api-key":"openai-key-two","proxy-url":null}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	if len(providers) != 1 || len(providers[0]["api_key_entries"].([]any)) != 2 {
		t.Fatalf("OpenAI multi-key group projected as multiple configs: %#v", providers)
	}
	payload := aiProviderSnapshotPayload(providers[0])
	payload["support_prompt_cache_key"] = false
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	group := aiProviderV8GroupsForTest(fake, "openai-compatibility")[0].(map[string]any)
	if group["support-prompt-cache-key"] != false || group["headers"] != nil || group["future"].(map[string]any)["n"] != json.Number("9007199254740993123") {
		t.Fatalf("OpenAI group raw fields changed: %#v", group)
	}
	if value, exists := group["request-retry"]; !exists || value != nil {
		t.Fatal("OpenAI explicit null was materialized or removed")
	}
	keys := group["keys"].([]any)
	if len(keys) != 2 || keys[0].(map[string]any)["api-key"] != "openai-key-one" || keys[1].(map[string]any)["api-key"] != "openai-key-two" {
		t.Fatalf("OpenAI keys changed: %#v", keys)
	}
}

func TestAIProviderV0AdvancedPolicyAndClaudePresence(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["claude-api-key"] = []map[string]any{{"api-key": "claude-presence-secret", "fingerprint-profile": "oauth-cli", "cloak": map[string]any{"mode": "future-mode", "cache-user-id": true, "future": "keep"}}}
	fake.config["vertex-api-key"] = []map[string]any{{"api-key": "vertex-policy-secret", "base-url": "https://vertex.example"}}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	claude := providers[0]
	if claude["fingerprint_profile"] != "claude-code-cli" {
		t.Fatalf("legacy fingerprint not normalized: %#v", claude)
	}
	payload := aiProviderSnapshotPayload(claude)
	payload["cloak"] = map[string]any{"cache_user_id": nil}
	payload["request_retry"] = -1
	payload["disable_cooling"] = false
	payload["request_scoped_errors"] = []map[string]any{{"status": 429, "match": []string{"  exact\n text  "}, "match_regex": []string{`quota\s+limit`}, "action": "continue-and-cooldown"}}
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	current := fake.config["claude-api-key"].([]map[string]any)[0]
	cloak := current["cloak"].(map[string]any)
	if cloak["mode"] != "future-mode" || cloak["future"] != "keep" {
		t.Fatalf("untouched unknown cloak mode lost: %#v", cloak)
	}
	if _, exists := cloak["cache-user-id"]; exists {
		t.Fatalf("explicit nested clear ignored: %#v", cloak)
	}
	rule := current["request-scoped-errors"].([]any)[0].(map[string]any)
	if rule["match"].([]any)[0] != "  exact\n text  " || rule["match-regexr"].([]any)[0] != `quota\s+limit` {
		t.Fatalf("rule conditions changed: %#v", rule)
	}
	fake.mu.Unlock()
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[0])
	payload["cloak"] = nil
	payload["fingerprint_profile"] = nil
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	current = fake.config["claude-api-key"].([]map[string]any)[0]
	if _, exists := current["cloak"]; exists {
		t.Fatal("whole cloak clear ignored")
	}
	if _, exists := current["fingerprint-profile"]; exists {
		t.Fatal("fingerprint clear ignored")
	}
	fake.mu.Unlock()
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	payload = aiProviderSnapshotPayload(providers[1])
	payload["disable_cooling"] = false
	payload["request_retry"] = 0
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/vertex/0", payload, cookies, http.StatusOK)
	payload["request_scoped_errors"] = []any{}
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/vertex/0", payload, cookies, http.StatusUnprocessableEntity)
}

func TestAIProviderV0RejectsNewInvalidErrorRule(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["codex-api-key"] = []map[string]any{{"api-key": "rule-secret", "base-url": "https://rule.example"}}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
	for _, rule := range []map[string]any{
		{"status": 0, "match": []string{"x"}, "action": "stop"},
		{"status": 429, "match_regex": []string{"(?=unsupported)"}, "action": "stop"},
		{"status": 429, "match": []string{""}, "action": "stop"},
		{"status": 429, "match": []string{"x"}, "action": "unknown"},
	} {
		payload := aiProviderSnapshotPayload(provider)
		payload["request_scoped_errors"] = []map[string]any{rule}
		requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusUnprocessableEntity)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.providerPutCount != 0 {
		t.Fatalf("invalid rule wrote %d times", fake.providerPutCount)
	}
}

func TestAIProviderV8SourceRequiredForSavedAction(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://action.example","keys":[{"api-key":"action-saved-secret"}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
	payload := aiProviderSnapshotPayload(provider)
	requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", map[string]any{"brand": "codex", "provider": payload}, cookies, http.StatusOK)
	delete(payload, "source_hash")
	requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", map[string]any{"brand": "codex", "provider": payload}, cookies, http.StatusConflict)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 1 || !strings.Contains(fmt.Sprint(fake.apiCallBodies[0]["header"]), "action-saved-secret") {
		t.Fatalf("saved action resolved wrong credentials: %#v", fake.apiCallBodies)
	}
}

func TestAIProviderV0ReportsRetainedCloakOnClear(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["claude-api-key"] = []map[string]any{{"api-key": "retained-cloak-secret", "cloak": map[string]any{"mode": "always", "future": "keep"}}}
	fake.preserveFields = []string{"cloak"}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["cloak"] = nil
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusConflict)
	if !bytes.Contains(body, []byte("provider_config_committed_refresh_required")) || bytes.Contains(body, []byte("retained-cloak-secret")) {
		t.Fatalf("retained cloak reported success or leaked key: %s", body)
	}
}

func TestAIProviderV8IfMatchProtectsReadWriteRace(t *testing.T) {
	fake, unusedServer := newFakeAIProviderManagement(t)
	defer unusedServer.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://race.example","keys":[{"api-key":"race-secret","auth_index":"runtime-only"}]}]}}`)
	var conditionalWrite atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v8/management/config" {
			w.Header().Set("ETag", `"before-external-write"`)
		}
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v8/management/config/api-keys/") {
			conditionalWrite.Store(r.Header.Get("If-Match") == `"before-external-write"`)
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		fake.handle(w, r)
	}))
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["alpha_search"] = true
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	if !conditionalWrite.Load() || bytes.Contains(body, []byte("committed_refresh_required")) {
		t.Fatalf("read/write race was not rejected before commit: %s", body)
	}
}

func TestAIProviderV8IgnoresRuntimeAuthIndexInPersistence(t *testing.T) {
	fake, unusedServer := newFakeAIProviderManagement(t)
	defer unusedServer.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://auth-index.example","keys":[{"api-key":"index-secret","auth_index":"old-runtime-index"}]}]}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v8/management/config" {
			fake.mu.Lock()
			key := aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
			key["auth_index"] = fmt.Sprintf("runtime-%d", fake.providerPutCount)
			fake.mu.Unlock()
		}
		fake.handle(w, r)
	}))
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["alpha_search"] = true
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if bytes.Contains(fake.providerPutBodies[0], []byte(`"auth_index":`)) || bytes.Contains(fake.providerPutBodies[0], []byte(`"auth-index":`)) {
		t.Fatalf("runtime auth index written to config: %s", fake.providerPutBodies[0])
	}
}

func TestAIProviderV8RuleReorderAndEditPreservesExactExtensions(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://rule-order.example","keys":[{"api-key":"ordered-rules-secret","request-scoped-errors":[{"status":429,"match":[" first "],"action":"stop","future":"one"},{"status":503,"match":[" second "],"action":"continue","future":"two"},{"match":[""],"future":"inert"}]}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
	rules := provider["request_scoped_errors"].([]any)
	rules[1].(map[string]any)["action"] = "stop-and-cooldown"
	payload := aiProviderSnapshotPayload(provider)
	payload["request_scoped_errors"] = []any{rules[1], rules[2], rules[0]}
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
	result := key["request-scoped-errors"].([]any)
	for index, expected := range []string{"two", "inert", "one"} {
		if result[index].(map[string]any)["future"] != expected {
			t.Fatalf("rule extension associated with wrong rule: %#v", result)
		}
		if _, exists := result[index].(map[string]any)["source_hash"]; exists {
			t.Fatal("rule source hash written upstream")
		}
	}
	if result[0].(map[string]any)["match"].([]any)[0] != " second " || result[0].(map[string]any)["action"] != "stop-and-cooldown" {
		t.Fatalf("modified rule conditions changed: %#v", result[0])
	}
}

func TestAIProviderV8NativeDisableSentinelAndCloakAbsence(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"claude":[{"name":"one","excluded-models":["*","claude-legacy*"],"keys":[{"api-key":"claude-no-cloak-secret","disabled":true}]}],"openai-compatibility":[{"name":"compatible","base-url":"https://no-auth.example","excluded-models":["*"],"keys":[]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	providers := aiProviderSnapshotMaps(t, handler, cookies)
	if providers[0]["disabled"] != true || providers[1]["disabled"] == true {
		t.Fatalf("native and OpenAI wildcard meanings mixed: %#v", providers)
	}
	payload := aiProviderSnapshotPayload(providers[0])
	payload["disabled"] = false
	payload["rebuild_mid_system_message"] = true
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusOK)
	providers = aiProviderSnapshotMaps(t, handler, cookies)
	if providers[0]["disabled"] != false || providers[0]["cloak"] != nil {
		t.Fatalf("native enable or absent cloak changed: %#v", providers[0])
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	group := aiProviderV8GroupsForTest(fake, "claude")[0].(map[string]any)
	key := group["keys"].([]any)[0].(map[string]any)
	if _, exists := key["disabled"]; exists {
		t.Fatal("legacy disabled field retained")
	}
	if rules := key["excluded-models"].([]any); len(rules) != 1 || rules[0] != "claude-legacy*" {
		t.Fatalf("native wildcard rules changed: %#v", key)
	}
	if _, exists := key["cloak"]; exists {
		t.Fatal("unrelated update created cloak object")
	}
}

func TestAIProviderV8SnapshotFailureReportsCommittedWrite(t *testing.T) {
	fake, unusedServer := newFakeAIProviderManagement(t)
	defer unusedServer.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"one","base-url":"https://refresh.example","keys":[{"api-key":"refresh-secret"}]}]}}`)
	var committed atomic.Bool
	var readsAfterWrite atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if committed.Load() && r.URL.Path == "/v8/management/config" && readsAfterWrite.Add(1) > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fake.handle(w, r)
		if r.Method == http.MethodPut {
			committed.Store(true)
		}
	}))
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["alpha_search"] = true
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	if !bytes.Contains(body, []byte("provider_config_committed_refresh_required")) {
		t.Fatalf("committed write reported as uncommitted failure: %s", body)
	}
}
