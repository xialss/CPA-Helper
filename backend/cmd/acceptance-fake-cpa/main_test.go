package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV8ConfigReturnsRootConfiguration(t *testing.T) {
	fixture := newFakeCPA()
	request := httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	request.Header.Set("Authorization", "Bearer acceptance-management-key")
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config status = %d", response.Code)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"api-keys", "access", "plugins"} {
		var section map[string]json.RawMessage
		if err := json.Unmarshal(config[field], &section); err != nil || section == nil {
			t.Fatalf("native v8 config is missing the root %q object: %v", field, err)
		}
	}
}

func TestV8ProviderFamilyWritePreservesRawAndLegacyViews(t *testing.T) {
	fixture := newFakeCPA()
	call := func(method, path, body string, status int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer acceptance-management-key")
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	initial := fixture.v8Config()["api-keys"].(map[string]any)
	for _, kind := range providerKinds {
		for _, group := range objectList(initial[kind.Provider]) {
			if group["keys"] == nil {
				t.Fatalf("%s keys must be a non-null array", kind.Provider)
			}
			if kind.Provider != "openai-compatibility" {
				for _, key := range objectList(group["keys"]) {
					if _, exists := key["base-url"]; exists {
						t.Fatal("native base-url duplicated into key")
					}
				}
			}
		}
	}
	call(http.MethodPut, "/v8/management/config/api-keys/codex", `[{"name":"external","base-url":"https://synthetic.example/v1","request-retry":4,"disable-cooling":true,"keys":[{"api-key":"synthetic-one","request-retry":null,"request-scoped-errors":[],"future":{"n":9007199254740993123}},{"api-key":"synthetic-two","request-retry":0,"disable-cooling":false}]}]`, http.StatusOK)
	var groups []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(call(http.MethodGet, "/v8/management/config/api-keys/codex", "", http.StatusOK)))
	decoder.UseNumber()
	if err := decoder.Decode(&groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]["keys"].([]any)) != 2 {
		t.Fatalf("group shape changed: %#v", groups)
	}
	keys := groups[0]["keys"].([]any)
	first := keys[0].(map[string]any)
	if value, exists := first["request-retry"]; !exists || value != nil {
		t.Fatal("explicit null override was lost")
	}
	if first["future"].(map[string]any)["n"] != json.Number("9007199254740993123") || len(first["request-scoped-errors"].([]any)) != 0 {
		t.Fatalf("opaque value or empty override changed: %#v", first)
	}
	var legacy []map[string]any
	decoder = json.NewDecoder(bytes.NewReader(call(http.MethodGet, "/v0/management/codex-api-key", "", http.StatusOK)))
	decoder.UseNumber()
	if err := decoder.Decode(&legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 2 || legacy[0]["request-retry"] != json.Number("4") || legacy[1]["request-retry"] != json.Number("0") || legacy[1]["disable-cooling"] != false {
		t.Fatalf("legacy inherited view = %#v", legacy)
	}
	call(http.MethodGet, "/v8/management/credentials", "", http.StatusOK)
	call(http.MethodPut, "/v0/management/codex-api-key", `[{"api-key":"synthetic-legacy","base-url":"https://legacy.example","models":[]}]`, http.StatusOK)
	if body := call(http.MethodGet, "/v8/management/config/api-keys/codex", "", http.StatusOK); !bytes.Contains(body, []byte("synthetic-legacy")) || bytes.Contains(body, []byte("synthetic-one")) {
		t.Fatalf("legacy write did not refresh v8 view: %s", body)
	}
	call(http.MethodPut, "/v8/management/config/api-keys/codex", `[]`, http.StatusOK)
	call(http.MethodPut, "/v8/management/config/api-keys/codex", `[{"keys":[{"api-key":"x","base-url":"https://invalid.example"}]}]`, http.StatusBadRequest)
	call(http.MethodPut, "/v8/management/config/api-keys/codex", `null`, http.StatusBadRequest)
	call(http.MethodPut, "/v8/management/config/api-keys/unknown", `[]`, http.StatusNotFound)
}

func TestV8OpenAIExclusionsAreRejectedWithoutChangingState(t *testing.T) {
	fixture := newFakeCPA()
	call := func(t *testing.T, method, path, body string, status int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer acceptance-management-key")
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, status, response.Body.String())
		}
		return response.Body.Bytes()
	}
	const familyPath = "/v8/management/config/api-keys/openai-compatibility"
	const legacyPath = "/v0/management/openai-compatibility"
	beforeFamily := call(t, http.MethodGet, familyPath, "", http.StatusOK)
	beforeLegacy := call(t, http.MethodGet, legacyPath, "", http.StatusOK)
	for _, test := range []struct {
		name string
		list string
	}{
		{name: "empty", list: `[]`},
		{name: "nonempty", list: `["*"]`},
		{name: "null", list: `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `[{"name":"must-not-replace-current","base-url":"https://synthetic.example/v1","keys":[{"api-key":"synthetic-key"}],"excluded-models":` + test.list + `}]`
			problem := call(t, http.MethodPut, familyPath, body, http.StatusBadRequest)
			if !bytes.Contains(problem, []byte(`"invalid_config"`)) {
				t.Fatalf("missing strict-config error: %s", problem)
			}
			if after := call(t, http.MethodGet, familyPath, "", http.StatusOK); !bytes.Equal(beforeFamily, after) {
				t.Fatal("rejected write changed the raw OpenAI family")
			}
			if after := call(t, http.MethodGet, legacyPath, "", http.StatusOK); !bytes.Equal(beforeLegacy, after) {
				t.Fatal("rejected write changed the legacy OpenAI view")
			}
		})
	}
	call(t, http.MethodPut, familyPath, `[{"name":"accepted","base-url":"https://synthetic.example/v1","keys":[{"api-key":"synthetic-key"}]}]`, http.StatusOK)
	var groups []map[string]any
	if err := json.Unmarshal(call(t, http.MethodGet, familyPath, "", http.StatusOK), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0]["name"] != "accepted" || len(groups[0]["keys"].([]any)) != 1 {
		t.Fatalf("valid OpenAI write was not retained: %#v", groups)
	}
	if _, exists := groups[0]["excluded-models"]; exists {
		t.Fatal("valid OpenAI write introduced excluded-models")
	}
	const nativePath = "/v8/management/config/api-keys/codex"
	call(t, http.MethodPut, nativePath, `[{"name":"native","base-url":"https://synthetic.example/v1","excluded-models":["*","gpt-*"],"keys":[{"api-key":"synthetic-native-key"}]}]`, http.StatusOK)
	if body := call(t, http.MethodGet, nativePath, "", http.StatusOK); !bytes.Contains(body, []byte(`"excluded-models":["*","gpt-*"]`)) {
		t.Fatalf("native exclusions were not retained: %s", body)
	}
}

func TestValidateLoopbackListenAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		wantErr bool
	}{
		{address: "127.0.0.1:18319"},
		{address: "localhost:18319"},
		{address: "[::1]:18319"},
		{address: ":18319", wantErr: true},
		{address: "0.0.0.0:18319", wantErr: true},
		{address: "192.168.1.10:18319", wantErr: true},
		{address: "127.0.0.1", wantErr: true},
	} {
		t.Run(test.address, func(t *testing.T) {
			err := validateLoopbackListenAddress(test.address)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateLoopbackListenAddress(%q) error = %v, wantErr %t", test.address, err, test.wantErr)
			}
		})
	}
}
