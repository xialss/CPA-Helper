package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type pluginManagementRelease struct {
	version   string
	cpaTarget string
	v8        bool
}

var pluginManagementReleases = []pluginManagementRelease{
	{version: "0.1.4", cpaTarget: "v8.0.3"},
	{version: "0.1.5", cpaTarget: "v8.0.12", v8: true},
	{version: "0.1.6", cpaTarget: "v8.0.17", v8: true},
	{version: "0.1.7", cpaTarget: "v8.0.29", v8: true},
	{version: "0.1.8", cpaTarget: "v8.0.32", v8: true},
}

type pluginManagementSnapshot struct {
	requests  []string
	settings  map[string]json.RawMessage
	validated map[string]json.RawMessage
	submitted map[string]json.RawMessage
}

type pluginManagementFixture struct {
	mu                 sync.Mutex
	validationResponse map[string]json.RawMessage
	catalog            *pluginModelCatalogSnapshot
	pluginManagementSnapshot
}

func newPluginManagementFixture(t *testing.T, release pluginManagementRelease) (*pluginManagementFixture, AppConfig) {
	t.Helper()
	response := map[string]json.RawMessage{
		"enabled":                mustRawBool(false),
		"stream_enabled":         mustRawBool(true),
		"responses_only_stream":  mustRawBool(true),
		"max_buffer_bytes":       mustRawNumber(2 * 1024 * 1024),
		"max_total_buffer_bytes": mustRawNumber(8 * 1024 * 1024),
		"action":                 mustRawString("reject"),
		"unknown_action":         mustRawString("pass"),
		"case_sensitive":         mustRawBool(false),
		"ignore_thinking_suffix": mustRawBool(true),
		"ignored_models":         mustRaw(nil),
		"accepted_models":        mustRaw(map[string][]string{"old-model": {"old-response"}}),
	}
	fixture := &pluginManagementFixture{pluginManagementSnapshot: pluginManagementSnapshot{
		settings: map[string]json.RawMessage{
			"enabled":                   mustRawBool(true),
			"state_dir":                 mustRawString("plugins/keep-state"),
			"model_list_filter_enabled": mustRawBool(true),
			"response_model_mismatch":   mustRawObject(response),
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.requests = append(fixture.requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		key := "compat-management-secret"
		if r.URL.Path == "/v1/models" {
			key = "compat-downstream-secret"
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("unexpected authorization for %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload any
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities":
			payload = map[string]any{
				"plugin_version": release.version, "cpa_version": release.cpaTarget, "contract_version": "v1",
				"response_model_mismatch": fixture.settings["response_model_mismatch"],
			}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/directory":
			payload = map[string]any{"credentials": []any{}}
		case "GET /v8/management/config":
			if !release.v8 {
				http.NotFound(w, r)
				return
			}
			payload = map[string]any{
				"access": map[string]any{"api-keys": []string{"compat-downstream-secret"}},
				"api-keys": map[string]any{"codex": []any{map[string]any{
					"base-url": "https://compat.invalid", "keys": []any{map[string]any{
						"api-key": "compat-upstream-secret", "auth_index": "configured-index",
					}},
				}}},
				"plugins": map[string]any{"configs": map[string]any{"cpa-helper-plugin": fixture.settings}},
			}
		case "GET /v0/management/config":
			if release.v8 {
				http.NotFound(w, r)
				return
			}
			payload = map[string]any{"codex-api-key": []any{map[string]any{
				"api-key": "compat-upstream-secret", "base-url": "https://compat.invalid",
			}}}
		case "GET /v8/management/credentials", "GET /v0/management/auth-files":
			if (r.URL.Path == "/v8/management/credentials") != release.v8 {
				http.NotFound(w, r)
				return
			}
			payload = map[string]any{"files": []any{map[string]any{
				"id": "oauth-file", "auth_index": "oauth-index", "provider": "claude", "name": "OAuth",
				"source": "file", "secret": "compat-oauth-secret",
			}}}
		case "GET /v0/management/api-keys":
			if release.v8 {
				http.NotFound(w, r)
				return
			}
			payload = map[string]any{"api-keys": []string{"compat-downstream-secret"}}
		case "GET /v1/models":
			payload = map[string]any{"data": []any{map[string]any{"id": "compat-model"}}}
		case "GET /v8/management/credentials/models":
			if release.version != "0.1.8" {
				http.NotFound(w, r)
				return
			}
			id := r.URL.Query().Get("name")
			configuredID := pluginConfiguredCredentialID("codex:apikey", []string{"compat-upstream-secret", "https://compat.invalid", "", "", ""}, map[string]int{})
			if id != "oauth-file" && id != configuredID {
				t.Error("models endpoint received an auth_index/ref instead of the host auth ID")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload = map[string]any{"models": []any{map[string]any{"id": "compat-model"}}}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/model-catalog":
			if release.version != "0.1.8" {
				http.NotFound(w, r)
				return
			}
			revision, healthStatus := uint64(0), "unavailable"
			if fixture.catalog != nil {
				revision, healthStatus = fixture.catalog.Revision, "ok"
			}
			w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
			payload = map[string]any{"snapshot": fixture.catalog, "health": map[string]any{"catalog_revision": revision, "status": healthStatus}}
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/model-catalog":
			if release.version != "0.1.8" {
				http.NotFound(w, r)
				return
			}
			revision := uint64(0)
			if fixture.catalog != nil {
				revision = fixture.catalog.Revision
			}
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", revision) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			var next pluginModelCatalogSnapshot
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil || next.Revision != revision+1 {
				t.Error("invalid catalog transaction")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.catalog = &next
			payload = map[string]any{"catalog_revision": next.Revision}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			if fixture.catalog == nil {
				t.Error("health read before catalog publication")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			payload = map[string]any{"model_catalog": map[string]any{"catalog_revision": fixture.catalog.Revision, "status": "ok"}}
		case "POST /v0/management/plugins/cpa-helper-plugin/v1/response-model/validate":
			var next map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				t.Errorf("decode validation request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := next["responses_only_stream"]; release.v8 && exists {
				t.Error("v8 validation received the removed responses_only_stream field")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.validated = cloneRawObject(next)
			payload = next
			if fixture.validationResponse != nil {
				payload = fixture.validationResponse
			}
		case "PUT /v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch", "PATCH /v0/management/plugins/cpa-helper-plugin/config":
			if (r.Method == http.MethodPut) != release.v8 {
				http.NotFound(w, r)
				return
			}
			var next map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				t.Errorf("decode host configuration request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !release.v8 {
				var responseConfig map[string]json.RawMessage
				if err := json.Unmarshal(next["response_model_mismatch"], &responseConfig); err != nil {
					t.Errorf("decode legacy response configuration: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				next = responseConfig
			}
			fixture.submitted = cloneRawObject(next)
			fixture.settings["response_model_mismatch"] = mustRawObject(next)
			payload = map[string]any{"ok": true}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode fake CPA response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return fixture, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "compat-management-secret"}}
}

func (fixture *pluginManagementFixture) snapshot() pluginManagementSnapshot {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return pluginManagementSnapshot{
		requests: append([]string(nil), fixture.requests...), settings: cloneRawObject(fixture.settings),
		validated: cloneRawObject(fixture.validated), submitted: cloneRawObject(fixture.submitted),
	}
}

func TestPluginManagementReleaseDirectory(t *testing.T) {
	for _, release := range pluginManagementReleases {
		t.Run(release.version, func(t *testing.T) {
			fixture, cfg := newPluginManagementFixture(t, release)
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
			application, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(application.Close)
			directory, err := application.pluginDirectory(context.Background(), cfg, &AuthUser{IsSuperAdmin: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(directory["models"], []string{"compat-model"}) {
				t.Fatalf("models = %#v", directory["models"])
			}
			credentials, ok := directory["credentials"].([]map[string]any)
			if !ok || len(credentials) != 2 {
				t.Fatalf("credentials = %#v; want configured and runtime credentials", directory["credentials"])
			}
			providers := map[string]bool{}
			for _, credential := range credentials {
				providers[pluginAnyText(credential["provider"])] = true
				if release.v8 && pluginAnyText(credential["auth_index"]) == "" {
					t.Fatalf("v8 credential lacks auth_index: %#v", credential)
				}
			}
			if !providers["codex"] || !providers["claude"] {
				t.Fatalf("directory providers = %#v", providers)
			}
			encoded, err := json.Marshal(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"compat-management-secret", "compat-downstream-secret", "compat-upstream-secret", "compat-oauth-secret"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("directory exposed a raw credential")
				}
			}
			wantRequests := []string{
				"GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities",
				"GET /v0/management/plugins/cpa-helper-plugin/v1/directory",
			}
			if release.v8 {
				wantRequests = append(wantRequests, "GET /v8/management/config", "GET /v8/management/credentials")
			} else {
				wantRequests = append(wantRequests, "GET /v0/management/config", "GET /v0/management/auth-files", "GET /v0/management/api-keys")
			}
			if release.version == "0.1.8" {
				wantRequests = append(wantRequests,
					"GET /v0/management/plugins/cpa-helper-plugin/v1/model-catalog",
					"GET /v8/management/credentials/models", "GET /v8/management/credentials/models",
					"GET /v8/management/config", "GET /v8/management/credentials",
					"PUT /v0/management/plugins/cpa-helper-plugin/v1/model-catalog",
					"GET /v0/management/plugins/cpa-helper-plugin/v1/health",
					"GET /v0/management/plugins/cpa-helper-plugin/v1/model-catalog",
				)
			} else {
				wantRequests = append(wantRequests, "GET /v1/models")
			}
			if got := fixture.snapshot().requests; !reflect.DeepEqual(got, wantRequests) {
				t.Fatalf("requests = %v; want %v", got, wantRequests)
			}
		})
	}
}

func TestPluginManagementReleaseResponseModelSave(t *testing.T) {
	for _, release := range pluginManagementReleases {
		t.Run(release.version, func(t *testing.T) {
			fixture, cfg := newPluginManagementFixture(t, release)
			before := fixture.snapshot()
			application := &App{}
			loaded, err := application.pluginResponseModelConfig(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, hasLegacyField := loaded["responses_only_stream"]
			if hasLegacyField == release.v8 {
				t.Fatalf("loaded legacy field presence = %v; v8 = %v", hasLegacyField, release.v8)
			}
			requested := cloneRawObject(loaded)
			requested["enabled"] = mustRawBool(true)
			requested["accepted_models"] = json.RawMessage(`{}`)
			requested["responses_only_stream"] = mustRawBool(true)
			result, err := application.updatePluginResponseModelConfig(context.Background(), cfg, requested)
			if err != nil {
				t.Fatal(err)
			}
			wantResponse := cloneRawObject(requested)
			if release.v8 {
				delete(wantResponse, "responses_only_stream")
			}
			after := fixture.snapshot()
			if !reflect.DeepEqual(after.validated, wantResponse) || !reflect.DeepEqual(after.submitted, wantResponse) || !reflect.DeepEqual(result, wantResponse) {
				t.Fatalf("response did not round-trip completely: validated=%s submitted=%s result=%s", mapToRawObject(after.validated), mapToRawObject(after.submitted), mapToRawObject(result))
			}
			wantSettings := cloneRawObject(before.settings)
			wantSettings["response_model_mismatch"] = mustRawObject(wantResponse)
			if !reflect.DeepEqual(after.settings, wantSettings) {
				t.Fatalf("host configuration = %s; want %s", mapToRawObject(after.settings), mapToRawObject(wantSettings))
			}
			if string(result["accepted_models"]) != `{}` || string(result["ignored_models"]) != `[]` {
				t.Fatalf("cleared mappings/lists = %s / %s", result["accepted_models"], result["ignored_models"])
			}
			hostWrite := "PATCH /v0/management/plugins/cpa-helper-plugin/config"
			if release.v8 {
				hostWrite = "PUT /v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch"
			}
			wantRequests := []string{
				"GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities",
				"GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities",
				"POST /v0/management/plugins/cpa-helper-plugin/v1/response-model/validate",
				hostWrite,
				"GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities",
			}
			if !reflect.DeepEqual(after.requests, wantRequests) {
				t.Fatalf("requests = %v; want validate, host save, then capabilities confirmation %v", after.requests, wantRequests)
			}
		})
	}
}

func TestPluginManagementCapabilitiesRejectUnsupportedReleases(t *testing.T) {
	for _, release := range []pluginManagementRelease{
		{version: "0.1.5", cpaTarget: "v8.0.3"},
		{version: "0.1.6", cpaTarget: "v8.0.12"},
		{version: "0.1.7", cpaTarget: "v8.0.17"},
		{version: "0.1.7"},
		{version: "0.1.8", cpaTarget: "v8.0.29"},
		{version: "0.1.8"},
		{version: "0.1.9", cpaTarget: "v8.0.32"},
		{version: "0.2.0", cpaTarget: "v8.0.29"},
		{version: "0.1.7-private-secret", cpaTarget: "untrusted-cpa-secret"},
		{version: "0.1.7", cpaTarget: "untrusted-cpa-secret"},
		{cpaTarget: "v8.0.29"},
	} {
		t.Run(release.version+"/"+release.cpaTarget, func(t *testing.T) {
			fixture, cfg := newPluginManagementFixture(t, release)
			application := &App{}
			operations := []struct {
				name string
				run  func() error
			}{
				{"capabilities", func() error {
					_, err := application.cpaHelperPluginCapabilities(context.Background(), cfg)
					return err
				}},
				{"directory", func() error {
					_, err := application.pluginDirectory(context.Background(), cfg, &AuthUser{IsSuperAdmin: true}, "")
					return err
				}},
				{"response read", func() error {
					_, err := application.pluginResponseModelConfig(context.Background(), cfg)
					return err
				}},
				{"response save", func() error {
					_, err := application.updatePluginResponseModelConfig(context.Background(), cfg, map[string]json.RawMessage{})
					return err
				}},
				{"model filter status", func() error {
					_, err := application.availableModelListFilterStatus(context.Background(), cfg)
					return err
				}},
			}
			for _, operation := range operations {
				err := operation.run()
				var appErr *AppError
				if !errors.As(err, &appErr) || appErr.Code != "plugin_incompatible" || appErr.Status != http.StatusServiceUnavailable {
					t.Fatalf("%s error = %v; want 503 plugin_incompatible", operation.name, err)
				}
				if appErr.Message != "CPA Helper Plugin 版本不兼容" && appErr.Message != "CPA Helper Plugin 管理契约不兼容" {
					t.Fatalf("%s exposed an uncontrolled error message: %q", operation.name, appErr.Message)
				}
			}
			requests := fixture.snapshot().requests
			if len(requests) != len(operations) {
				t.Fatalf("unsupported capability triggered additional requests: %v", requests)
			}
			for _, request := range requests {
				if request != "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities" {
					t.Fatalf("unsupported capability reached a management operation: %s", request)
				}
			}
		})
	}
}

func TestPluginManagementCapabilitiesPreserveLegacyReleases(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4"} {
		t.Run(version, func(t *testing.T) {
			_, cfg := newPluginManagementFixture(t, pluginManagementRelease{version: version})
			application := &App{}
			if _, err := application.cpaHelperPluginCapabilities(context.Background(), cfg); err != nil {
				t.Fatalf("legacy capability was rejected: %v", err)
			}
		})
	}
}

func TestPluginResponseModelMalformedLists(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value json.RawMessage
	}{
		{"ignored_object", "ignored_models", json.RawMessage(`{}`)},
		{"ignored_string", "ignored_models", json.RawMessage(`"model"`)},
		{"ignored_number_element", "ignored_models", json.RawMessage(`[1]`)},
		{"ignored_null_element", "ignored_models", json.RawMessage(`[null]`)},
		{"ignored_mixed_null", "ignored_models", json.RawMessage(`["model",null]`)},
		{"alias_null_element", "accepted_models", json.RawMessage(`{"model":[null]}`)},
		{"alias_mixed_null", "accepted_models", json.RawMessage(`{"model":["response",null]}`)},
		{"alias_number_element", "accepted_models", json.RawMessage(`{"model":[1]}`)},
		{"alias_null_array", "accepted_models", json.RawMessage(`{"model":null}`)},
		{"alias_empty_array", "accepted_models", json.RawMessage(`{"model":[]}`)},
	}
	for _, release := range []pluginManagementRelease{pluginManagementReleases[0], pluginManagementReleases[3]} {
		for _, tc := range cases {
			for _, source := range []string{"capabilities", "validation"} {
				t.Run(release.version+"/"+tc.name+"/"+source, func(t *testing.T) {
					fixture, cfg := newPluginManagementFixture(t, release)
					before := fixture.snapshot()
					var candidate map[string]json.RawMessage
					if err := json.Unmarshal(before.settings["response_model_mismatch"], &candidate); err != nil {
						t.Fatal(err)
					}
					requested := cloneRawObject(candidate)
					candidate[tc.field] = tc.value
					if release.v8 {
						delete(candidate, "responses_only_stream")
					}
					fixture.mu.Lock()
					if source == "capabilities" {
						fixture.settings["response_model_mismatch"] = mustRawObject(candidate)
					} else {
						fixture.validationResponse = candidate
					}
					fixture.mu.Unlock()
					application := &App{}
					var err error
					if source == "capabilities" {
						_, err = application.pluginResponseModelConfig(context.Background(), cfg)
					} else {
						_, err = application.updatePluginResponseModelConfig(context.Background(), cfg, requested)
					}
					after := fixture.snapshot()
					var appErr *AppError
					if !errors.As(err, &appErr) || appErr.Code != "plugin_incompatible" || appErr.Status != http.StatusServiceUnavailable {
						t.Fatalf("malformed %s error = %v; host write = %v; requests = %v", tc.field, err, len(after.submitted) > 0, after.requests)
					}
					if len(after.submitted) != 0 {
						t.Fatalf("malformed response reached host configuration: %s", mapToRawObject(after.submitted))
					}
					wantRequests := []string{"GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities"}
					if source == "validation" {
						wantRequests = append(wantRequests, "POST /v0/management/plugins/cpa-helper-plugin/v1/response-model/validate")
						if !reflect.DeepEqual(after.settings, before.settings) {
							t.Fatal("malformed validation response changed host settings")
						}
					}
					if !reflect.DeepEqual(after.requests, wantRequests) {
						t.Fatalf("requests = %v; want %v", after.requests, wantRequests)
					}
				})
			}
		}
	}
}

func TestPluginResponseModelValidLists(t *testing.T) {
	cases := []struct {
		name         string
		ignored      json.RawMessage
		accepted     json.RawMessage
		wantIgnored  string
		wantAccepted string
	}{
		{"missing", nil, nil, `[]`, `{}`},
		{"null", json.RawMessage(`null`), json.RawMessage(`null`), `[]`, `{}`},
		{"empty", json.RawMessage(`[]`), json.RawMessage(`{}`), `[]`, `{}`},
		{
			"strings_unchanged",
			json.RawMessage(`["skip-model"," keep-spaces ","skip-model"]`),
			json.RawMessage(`{"request-model":["response-model"," response-model ","response-model"]}`),
			`["skip-model"," keep-spaces ","skip-model"]`,
			`{"request-model":["response-model"," response-model ","response-model"]}`,
		},
	}
	for _, release := range []pluginManagementRelease{pluginManagementReleases[0], pluginManagementReleases[3]} {
		for _, tc := range cases {
			t.Run(release.version+"/"+tc.name, func(t *testing.T) {
				fixture, cfg := newPluginManagementFixture(t, release)
				var candidate map[string]json.RawMessage
				if err := json.Unmarshal(fixture.snapshot().settings["response_model_mismatch"], &candidate); err != nil {
					t.Fatal(err)
				}
				delete(candidate, "ignored_models")
				delete(candidate, "accepted_models")
				if tc.ignored != nil {
					candidate["ignored_models"] = tc.ignored
				}
				if tc.accepted != nil {
					candidate["accepted_models"] = tc.accepted
				}
				if release.v8 {
					delete(candidate, "responses_only_stream")
				}
				fixture.mu.Lock()
				fixture.settings["response_model_mismatch"] = mustRawObject(candidate)
				fixture.validationResponse = candidate
				fixture.mu.Unlock()
				application := &App{}
				loaded, err := application.pluginResponseModelConfig(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if string(loaded["ignored_models"]) != tc.wantIgnored || string(loaded["accepted_models"]) != tc.wantAccepted {
					t.Fatalf("loaded lists = %s / %s", loaded["ignored_models"], loaded["accepted_models"])
				}
				result, err := application.updatePluginResponseModelConfig(context.Background(), cfg, loaded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result, loaded) {
					t.Fatalf("valid lists changed on save: %s; want %s", mapToRawObject(result), mapToRawObject(loaded))
				}
			})
		}
	}
}
