package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// CPA v8.0.47 flattens OpenAI groups and strictly decodes OpenAICompatibility:
// internal/config/config_v8.go:406,938 and config_types.go:963 in that tag.
// This fixture checks the same supported group fields, not the whole CPA validator.
func validateOpenAIV8Family(family string, body json.RawMessage) error {
	if family != "openai-compatibility" {
		return nil
	}
	var groups []struct {
		Name                  string            `json:"name"`
		Priority              *int              `json:"priority"`
		Disabled              bool              `json:"disabled"`
		Prefix                string            `json:"prefix"`
		BaseURL               string            `json:"base-url"`
		Headers               map[string]string `json:"headers"`
		Models                []json.RawMessage `json:"models"`
		SupportPromptCacheKey bool              `json:"support-prompt-cache-key"`
		DisableCooling        *bool             `json:"disable-cooling"`
		RequestRetry          *int              `json:"request-retry"`
		RequestScopedErrors   []json.RawMessage `json:"request-scoped-errors"`
		Keys                  []struct {
			APIKey   string `json:"api-key"`
			ProxyURL string `json:"proxy-url"`
			Weight   *int   `json:"weight"`
		} `json:"keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&groups)
}

func TestAIProviderV8OpenAIEditorSaveContract(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8ValidateFamily = validateOpenAIV8Family
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"openai-compatibility":[{"name":"compatible","base-url":"https://compatible.example/v1","headers":{"X-Fixture":"keep"},"models":[{"name":"model-one"}],"keys":[{"api-key":"fixture-key-one","proxy-url":"http://proxy.invalid","weight":2},{"api-key":"fixture-key-two"}]},{"name":"sibling","base-url":"https://sibling.invalid/v1","keys":[]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	siblingBefore, _ := json.Marshal(aiProviderV8GroupsForTest(fake, "openai-compatibility")[1])
	changes := []struct {
		name  string
		field string
		value any
	}{
		{name: "unchanged"},
		{name: "priority", field: "priority", value: 7},
		{name: "name", field: "name", value: "renamed"},
		{name: "disabled", field: "disabled", value: true},
		{name: "behavior", field: "support_prompt_cache_key", value: true},
		{name: "runtime policy", field: "request_retry", value: 0},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
			payload := aiProviderSnapshotPayload(provider)
			// Older real editor requests supplied this even with the picker hidden.
			payload["excluded_models"] = []any{}
			if change.field != "" {
				payload[change.field] = change.value
			}
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", payload, cookies, http.StatusOK)
			updated := aiProviderSnapshotMaps(t, handler, cookies)[0]
			if change.field != "" && !bytes.Equal(mustMarshalJSON(t, updated[change.field]), mustMarshalJSON(t, change.value)) {
				t.Fatalf("saved %s = %#v, want %#v", change.field, updated[change.field], change.value)
			}
		})
	}
	requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/openai_compatibility", map[string]any{
		"name": "new-no-auth", "base_url": "https://new.invalid/v1", "api_key_entries": []any{},
		"models": []any{}, "headers": []any{}, "excluded_models": []any{},
	}, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	groups := aiProviderV8GroupsForTest(fake, "openai-compatibility")
	if len(groups) != 3 {
		t.Fatalf("groups after create = %d", len(groups))
	}
	for _, value := range groups {
		if _, exists := value.(map[string]any)["excluded-models"]; exists {
			t.Fatal("OpenAI write introduced unsupported excluded-models")
		}
	}
	if !bytes.Equal(siblingBefore, mustMarshalJSON(t, groups[1])) {
		t.Fatal("untouched sibling changed")
	}
	keys := groups[0].(map[string]any)["keys"].([]any)
	if len(keys) != 2 || keys[0].(map[string]any)["api-key"] != "fixture-key-one" || keys[1].(map[string]any)["api-key"] != "fixture-key-two" || keys[0].(map[string]any)["weight"] != json.Number("2") || keys[0].(map[string]any)["proxy-url"] != "http://proxy.invalid" {
		t.Fatalf("preserved keys changed: %#v", keys)
	}
}

func TestAIProviderV8OpenAIUnsupportedExclusionIntent(t *testing.T) {
	for _, test := range []struct {
		name    string
		history string
		rules   []any
		omitted bool
		status  int
		strict  bool
	}{
		{name: "new rule", rules: []any{"model-*"}, status: http.StatusUnprocessableEntity},
		{name: "historical clear", history: `,"excluded-models":["model-*"]`, rules: []any{}, status: http.StatusUnprocessableEntity},
		{name: "historical change", history: `,"excluded-models":["model-*"]`, rules: []any{"other"}, status: http.StatusUnprocessableEntity},
		{name: "historical omitted", history: `,"excluded-models":["model-*"]`, omitted: true, status: http.StatusOK},
		{name: "historical unchanged", history: `,"excluded-models":[" model-* "]`, rules: []any{"model-*"}, status: http.StatusOK},
		{name: "strict rejects historical field", history: `,"excluded-models":["model-*"]`, omitted: true, strict: true, status: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			opaque := `,"future":{"number":9007199254740993123}`
			if test.strict {
				opaque = ""
			}
			fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"openai-compatibility":[{"name":"compatible","base-url":"https://compatible.invalid/v1","keys":[{"api-key":"fixture-secret"}]`+opaque+test.history+`}]}}`)
			if test.strict {
				fake.v8ValidateFamily = validateOpenAIV8Family
			}
			original := mustMarshalJSON(t, aiProviderV8GroupsForTest(fake, "openai-compatibility"))
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
			if test.omitted {
				delete(payload, "excluded_models")
			} else {
				payload["excluded_models"] = test.rules
			}
			body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", payload, cookies, test.status)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if !bytes.Equal(original, mustMarshalJSON(t, aiProviderV8GroupsForTest(fake, "openai-compatibility"))) {
				t.Fatal("unrelated edit rewrote historical raw configuration")
			}
			if test.status != http.StatusOK && !test.strict {
				if fake.providerPutCount != 0 || !strings.Contains(string(body), "不支持修改排除模型") {
					t.Fatalf("unsupported intent was not rejected before PUT: count=%d, response=%s", fake.providerPutCount, body)
				}
			}
			if test.strict && (fake.providerPutCount != 1 || !strings.Contains(string(body), "HTTP 400")) {
				t.Fatalf("invalid history was silently removed or hidden: count=%d, response=%s", fake.providerPutCount, body)
			}
		})
	}
	// Creation must reject explicit unsupported rules before it can persist them.
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/openai_compatibility", map[string]any{
		"name": "new", "base_url": "https://new.invalid/v1", "api_key_entries": []any{}, "excluded_models": []any{"model-*"},
	}, cookies, http.StatusUnprocessableEntity)
	if fake.providerPutCount != 0 {
		t.Fatal("new unsupported exclusion reached upstream")
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
