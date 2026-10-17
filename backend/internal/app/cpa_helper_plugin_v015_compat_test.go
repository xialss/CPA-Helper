package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPluginV015ManagementCapability(t *testing.T) {
	v8, err := pluginUsesV8Management(map[string]json.RawMessage{
		"plugin_version": mustRawString("0.1.5"), "cpa_version": mustRawString("v8.0.12"),
	})
	if err != nil || !v8 {
		t.Fatalf("v0.1.5 capability = %v, %v; want v8 bridge", v8, err)
	}
	if _, err := pluginUsesV8Management(map[string]json.RawMessage{
		"plugin_version": mustRawString("0.1.5"), "cpa_version": mustRawString("v8.0.3"),
	}); err == nil {
		t.Fatal("v0.1.5 accepted an incompatible CPA version")
	}
	v8, err = pluginUsesV8Management(map[string]json.RawMessage{"plugin_version": mustRawString("0.1.4")})
	if err != nil || v8 {
		t.Fatalf("v0.1.4 capability = %v, %v; want legacy bridge", v8, err)
	}
}

func TestPluginV015DownstreamKeysAndConfiguredDirectory(t *testing.T) {
	config := map[string]any{
		"access": map[string]any{"api-keys": []any{"downstream-secret"}},
		"api-keys": map[string]any{
			"codex": []any{map[string]any{
				"base-url": "https://codex.invalid", "prefix": "/team/", "headers": map[string]any{" X-Test ": " value "},
				"keys": []any{map[string]any{"api-key": "upstream-secret", "auth_index": "shared-index"}},
			}},
			"openai-compatibility": []any{map[string]any{
				"name": "keyless", "base-url": "https://keyless.invalid", "keys": nil, "auth_index": "keyless-index",
			}},
		},
	}
	keys, err := pluginV8DownstreamKeys(config)
	if err != nil || !reflect.DeepEqual(keys, []string{"downstream-secret"}) {
		t.Fatalf("v8 downstream keys = %#v, %v", keys, err)
	}
	target := map[string]map[string]any{}
	if err := pluginAddV8ConfiguredCredentials(target, config); err != nil {
		t.Fatal(err)
	}
	if len(target) != 2 {
		t.Fatalf("configured v8 credentials = %#v, want 2", target)
	}
	encoded, _ := json.Marshal(target)
	if strings.Contains(string(encoded), "upstream-secret") || strings.Contains(string(encoded), "downstream-secret") {
		t.Fatalf("directory leaked credential: %s", encoded)
	}
	for _, entry := range target {
		if entry["source"] != "ai-providers" || entry["auth_index"] == nil {
			t.Fatalf("configured entry missing v8 metadata: %#v", entry)
		}
	}
	if _, err := pluginV8DownstreamKeys(map[string]any{"access": map[string]any{"api-keys": "bad"}}); err == nil {
		t.Fatal("malformed v8 downstream list was accepted")
	}
	runtimeTarget := map[string]map[string]any{}
	if err := pluginAddV8ConfiguredCredentials(runtimeTarget, config); err != nil {
		t.Fatal(err)
	}
	mismatchedConfig := map[string]any{
		"api-keys": map[string]any{
			"codex": []any{map[string]any{
				"base-url": "https://codex.invalid", "prefix": "/team/", "headers": map[string]any{" X-Test ": " value "},
				"keys": []any{map[string]any{"api-key": "upstream-secret", "auth_index": "different-index"}},
			}},
		},
	}
	if err := pluginAddV8ConfiguredCredentials(runtimeTarget, mismatchedConfig); err == nil {
		t.Fatal("configured/runtime auth_index mismatch was silently overwritten")
	}
	badAuth := map[string]any{"api-keys": map[string]any{"codex": []any{map[string]any{"keys": []any{map[string]any{"api-key": "key", "auth_index": 12}}}}}}
	if err := pluginAddV8ConfiguredCredentials(map[string]map[string]any{}, badAuth); err == nil {
		t.Fatal("non-string auth_index was silently skipped")
	}
	badKey := map[string]any{"api-keys": map[string]any{"codex": []any{map[string]any{"keys": []any{map[string]any{"api-key": 12, "auth_index": "idx"}}}}}}
	if err := pluginAddV8ConfiguredCredentials(map[string]map[string]any{}, badKey); err == nil {
		t.Fatal("non-string api-key was silently accepted")
	}
	missingOpenAIKeys := map[string]any{"api-keys": map[string]any{"openai-compatibility": []any{map[string]any{"name": "missing"}}}}
	if err := pluginAddV8ConfiguredCredentials(map[string]map[string]any{}, missingOpenAIKeys); err == nil {
		t.Fatal("missing openai keys list was silently treated as keyless")
	}
	weighted := map[string]any{"api-keys": map[string]any{"codex": []any{map[string]any{"keys": []any{map[string]any{"api-key": "key", "auth_index": "idx", "weight": 0}}}}}}
	weightedTarget := map[string]map[string]any{}
	if err := pluginAddV8ConfiguredCredentials(weightedTarget, weighted); err != nil || len(weightedTarget) != 1 {
		t.Fatalf("weighted credential = %#v, err=%v", weightedTarget, err)
	}
	for _, item := range weightedTarget {
		if item["status"] != "disabled" {
			t.Fatalf("zero-weight credential status = %#v, want disabled", item)
		}
	}
}

func TestPluginV015CredentialOverridesAndRuntimeAuthority(t *testing.T) {
	config := map[string]any{"api-keys": map[string]any{"codex": []any{map[string]any{
		"base-url": "https://codex.invalid", "proxy-url": "https://proxy.invalid", "prefix": "team", "disabled": true,
		"keys": []any{map[string]any{"api-key": "key", "auth_index": "idx", "proxy-url": "", "prefix": ""}},
	}}}}
	const expectedRef = "sha256:58368c8a8861b4fefa4edd3a4c8181655b47bc98701adf439f5d02c7e6a05dba"
	target := map[string]map[string]any{}
	if err := pluginAddV8ConfiguredCredentials(target, config); err != nil {
		t.Fatal(err)
	}
	if len(target) != 1 || target[expectedRef] == nil {
		t.Fatalf("explicit empty overrides produced wrong scheduler reference: %#v", target)
	}
	target[expectedRef]["status"] = "disabled"
	target[expectedRef]["source"] = "auth-files"
	if err := pluginAddV8ConfiguredCredentials(target, config); err != nil {
		t.Fatal(err)
	}
	if target[expectedRef]["status"] != "disabled" || target[expectedRef]["source"] != "auth-files" {
		t.Fatalf("configuration overwrote runtime metadata: %#v", target[expectedRef])
	}
	unknown := map[string]any{"api-keys": map[string]any{"unsupported": []any{map[string]any{"keys": []any{map[string]any{"auth_index": "idx"}}}}}}
	if err := pluginAddV8ConfiguredCredentials(map[string]map[string]any{}, unknown); err == nil {
		t.Fatal("unsupported provider silently skipped")
	}
	keyless := map[string]any{"api-keys": map[string]any{"openai-compatibility": []any{map[string]any{"keys": nil, "auth_index": "idx", "weight": json.Number("0")}}}}
	target = map[string]map[string]any{}
	if err := pluginAddV8ConfiguredCredentials(target, keyless); err != nil || len(target) != 1 {
		t.Fatalf("keyless directory = %#v, %v", target, err)
	}
	for _, entry := range target {
		if entry["status"] != "disabled" {
			t.Fatal("zero-weight keyless credential was active")
		}
	}
}

func TestPluginV015ResponseModelFieldContract(t *testing.T) {
	var validateBody, putBody []byte
	current := map[string]any{"enabled": false, "stream_enabled": true, "max_buffer_bytes": 2 * 1024 * 1024, "max_total_buffer_bytes": 8 * 1024 * 1024, "action": "reject", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": nil, "accepted_models": map[string]any{"old": []string{"old-response"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/capabilities"):
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_version": "0.1.5", "cpa_version": "v8.0.12", "contract_version": "v1", "response_model_mismatch": current})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/response-model/validate"):
			validateBody, _ = ioReadAll(r)
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "stream_enabled": true, "max_buffer_bytes": 2 * 1024 * 1024, "max_total_buffer_bytes": 8 * 1024 * 1024, "action": "reject", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": nil, "accepted_models": map[string]any{}})
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch":
			putBody, _ = ioReadAll(r)
			current = map[string]any{"enabled": true, "stream_enabled": true, "max_buffer_bytes": 2 * 1024 * 1024, "max_total_buffer_bytes": 8 * 1024 * 1024, "action": "reject", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": nil, "accepted_models": map[string]any{}}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	requested := map[string]json.RawMessage{
		"enabled": mustRawBool(true), "stream_enabled": mustRawBool(true),
		"responses_only_stream": mustRawBool(true), "max_buffer_bytes": mustRawNumber(2 * 1024 * 1024),
		"max_total_buffer_bytes": mustRawNumber(8 * 1024 * 1024), "action": mustRawString("reject"), "unknown_action": mustRawString("pass"),
		"case_sensitive": mustRawBool(false), "ignore_thinking_suffix": mustRawBool(true), "ignored_models": mustRaw(nil),
		"accepted_models": mustRaw(map[string][]string{"old": {"old-response"}}),
	}
	app := &App{}
	cfg := AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "management"}}
	loaded, err := app.pluginResponseModelConfig(context.Background(), cfg)
	if err != nil || string(loaded["ignored_models"]) != `[]` {
		t.Fatalf("loaded ignored_models = %s, err=%v; want []", loaded["ignored_models"], err)
	}
	result, err := app.updatePluginResponseModelConfig(context.Background(), cfg, requested)
	if err != nil {
		t.Fatal(err)
	}
	if result["accepted_models"] == nil || string(result["accepted_models"]) != `{}` {
		t.Fatalf("result accepted_models = %s; want empty mapping", result["accepted_models"])
	}
	if string(result["ignored_models"]) != `[]` || !bytes.Contains(putBody, []byte(`"ignored_models":[]`)) {
		t.Fatalf("null ignored_models did not round-trip as []: result=%s put=%s", result["ignored_models"], putBody)
	}
	if bytes.Contains(validateBody, []byte("responses_only_stream")) || bytes.Contains(putBody, []byte("responses_only_stream")) {
		t.Fatalf("v0.1.5 request included removed field: validate=%s put=%s", validateBody, putBody)
	}
	if !bytes.Contains(putBody, []byte(`"accepted_models":{}`)) {
		t.Fatalf("PUT did not send complete object with cleared mappings: %s", putBody)
	}
}

func ioReadAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}
