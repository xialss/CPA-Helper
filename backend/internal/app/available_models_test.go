package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

func TestAvailableModelsPluginRouting(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	type catalogReply struct {
		status  int
		headers http.Header
		body    string
	}
	var mu sync.Mutex
	remoteKeys := []string{}
	capabilities := map[string]any{}
	capabilityStatus := http.StatusOK
	capabilityReads := 0
	queries := []string{}
	replies := map[string]catalogReply{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			queries = append(queries, key)
			reply, exists := replies[key]
			if !exists || r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			for name, values := range reply.headers {
				w.Header()[name] = values
			}
			if reply.status != 0 {
				w.WriteHeader(reply.status)
			}
			_, _ = w.Write([]byte(reply.body))
			return
		}
		if r.Header.Get("Authorization") != "Bearer management-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/config":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "GET /v0/management/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&remoteKeys); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities":
			capabilityReads++
			w.WriteHeader(capabilityStatus)
			_ = json.NewEncoder(w).Encode(capabilities)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()
	app, err := backendApp.New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Routes()
	adminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cpa.URL, "management_key": "management-key", "collector_enabled": false}, adminCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/account/models", nil, nil, http.StatusUnauthorized)
	var empty backendApp.AvailableModelsResponse
	requestJSON(t, handler, http.MethodGet, "/api/account/models", nil, adminCookies, &empty)
	if empty.HasAPIKeys || empty.ModelListFilterStatus != "not_checked" || empty.Models == nil || empty.Errors == nil {
		t.Fatalf("empty account = %#v", empty)
	}
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member"}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	memberCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil)
	var alpha, beta, foreign backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "Alpha"}, memberCookies, &alpha)
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "Beta"}, memberCookies, &beta)
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "Admin"}, adminCookies, &foreign)
	if alpha.APIKey == nil || beta.APIKey == nil || foreign.APIKey == nil {
		t.Fatal("created keys lack complete secrets")
	}
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		capabilities = map[string]any{"contract_version": "v1", "plugin_version": "0.1.4", "modules": []string{"model_list_filter"}, "model_list_filter_enabled": true}
		capabilityStatus, capabilityReads = http.StatusOK, 0
		queries = []string{}
		// These are the authoritative catalogs AFTER the plugin composes
		// linked and individual model rules, applies deny precedence and enabled.
		replies = map[string]catalogReply{
			*alpha.APIKey:   {body: `{"data":[{"id":"alpha-only"},{"id":"shared"},{"id":"shared"}]}`},
			*beta.APIKey:    {body: `{"data":[{"id":"beta-only"},{"id":"shared"}]}`},
			*foreign.APIKey: {body: `{"data":[{"id":"private-admin-model"}]}`},
		}
	}
	read := func(t *testing.T) backendApp.AvailableModelsResponse {
		t.Helper()
		var response backendApp.AvailableModelsResponse
		requestJSON(t, handler, http.MethodGet, "/api/account/models", nil, memberCookies, &response)
		mu.Lock()
		defer mu.Unlock()
		if capabilityReads != 1 || len(queries) != 2 {
			t.Fatalf("capability reads=%d model queries=%d, want 1 and 2", capabilityReads, len(queries))
		}
		for _, key := range queries {
			if key != *alpha.APIKey && key != *beta.APIKey {
				t.Fatal("queried a key not owned by current account")
			}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{*alpha.APIKey, *beta.APIKey, *foreign.APIKey, "management-key"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("model response contains raw credentials")
			}
		}
		return response
	}
	t.Run("filtered catalogs retain exact source keys", func(t *testing.T) {
		reset()
		got := read(t)
		if got.APIKeyCount != 2 || got.QueryableAPIKeyCount != 2 || got.ModelListFilterStatus != "enabled" || len(got.Errors) != 0 {
			t.Fatalf("response = %#v", got)
		}
		want := map[string][]string{"alpha-only": {alpha.APIKeyHash}, "beta-only": {beta.APIKeyHash}, "shared": {alpha.APIKeyHash, beta.APIKeyHash}}
		actual := map[string][]string{}
		for _, model := range got.Models {
			for _, source := range model.Sources {
				actual[model.ID] = append(actual[model.ID], source.APIKeyHash)
			}
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("model sources = %#v, want %#v", actual, want)
		}
	})
	t.Run("enabled key with no allowed models remains successful", func(t *testing.T) {
		reset()
		mu.Lock()
		replies[*alpha.APIKey] = catalogReply{body: `{"data":[]}`}
		replies[*beta.APIKey] = catalogReply{body: `{"data":[]}`}
		mu.Unlock()
		got := read(t)
		if len(got.Models) != 0 || len(got.Errors) != 0 || got.Models == nil {
			t.Fatalf("empty filtered response = %#v", got)
		}
	})
	t.Run("empty success and failed key produce partial response", func(t *testing.T) {
		reset()
		mu.Lock()
		replies[*alpha.APIKey] = catalogReply{body: `{"data":[]}`}
		replies[*beta.APIKey] = catalogReply{status: http.StatusServiceUnavailable}
		mu.Unlock()
		got := read(t)
		if len(got.Models) != 0 || len(got.Errors) != 1 || got.Errors[0].APIKeyHash != beta.APIKeyHash {
			t.Fatalf("partial empty response = %#v", got)
		}
	})
	for _, tc := range []struct {
		name    string
		reply   catalogReply
		message string
	}{
		{"http failure", catalogReply{status: http.StatusForbidden}, "HTTP 403"},
		{"policy unavailable in HTTP 200", catalogReply{headers: http.Header{"X-Cpa-Helper-Error": {"policy_unavailable"}}, body: `{"error":{"code":"policy_unavailable"}}`}, "模型列表策略不可用"},
		{"invalid catalog in HTTP 200", catalogReply{headers: http.Header{"X-Cpa-Helper-Error": {"invalid_model_catalog"}}, body: `{"data":[{"id":"must-not-appear"}]}`}, "模型目录无效"},
		{"identity unavailable", catalogReply{headers: http.Header{"X-Cpa-Helper-Model-List": {"unfiltered-identity-unavailable"}}, body: `{"data":[{"id":"must-not-appear"}]}`}, "无法识别当前 API 密钥"},
		{"malformed catalog item", catalogReply{body: `{"data":[{"id":"must-not-appear"},{}]}`}, "无效模型标识"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			mu.Lock()
			replies[*beta.APIKey] = tc.reply
			mu.Unlock()
			got := read(t)
			if len(got.Models) != 2 || len(got.Errors) != 1 || got.Errors[0].APIKeyHash != beta.APIKeyHash || !strings.Contains(got.Errors[0].Message, tc.message) {
				t.Fatalf("partial failure response = %#v", got)
			}
			for _, model := range got.Models {
				if len(model.Sources) != 1 || model.Sources[0].APIKeyHash != alpha.APIKeyHash {
					t.Fatalf("failed key contributed source: %#v", model)
				}
			}
		})
	}
	for _, state := range []string{"disabled", "unsupported", "not_loaded"} {
		t.Run(state+" preserves CPA catalog with explicit state", func(t *testing.T) {
			reset()
			mu.Lock()
			switch state {
			case "disabled":
				capabilities["model_list_filter_enabled"] = false
			case "unsupported":
				delete(capabilities, "model_list_filter_enabled")
			case "not_loaded":
				capabilityStatus = http.StatusNotFound
			}
			mu.Unlock()
			got := read(t)
			if got.ModelListFilterStatus != state || len(got.Models) != 3 || len(got.Errors) != 0 {
				t.Fatalf("unfiltered response = %#v", got)
			}
		})
	}
	t.Run("all keys failed report explicit failure", func(t *testing.T) {
		reset()
		mu.Lock()
		replies[*alpha.APIKey] = catalogReply{status: http.StatusUnauthorized}
		replies[*beta.APIKey] = catalogReply{status: http.StatusUnauthorized}
		mu.Unlock()
		requestJSONExpectStatus(t, handler, http.MethodGet, "/api/account/models", nil, memberCookies, http.StatusUnprocessableEntity)
	})
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusUnauthorized} {
		t.Run(http.StatusText(status)+" during plugin discovery is not absence", func(t *testing.T) {
			reset()
			mu.Lock()
			capabilityStatus = status
			mu.Unlock()
			wantStatus, wantCode := status, "plugin_unavailable"
			if status == http.StatusUnauthorized {
				wantStatus, wantCode = http.StatusBadGateway, "plugin_auth_failed"
			}
			request := httptest.NewRequest(http.MethodGet, "/api/account/models", nil)
			for _, cookie := range memberCookies {
				request.AddCookie(cookie)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != wantStatus {
				t.Fatalf("GET /api/account/models returned %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			var response struct {
				Detail struct {
					Code string `json:"code"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode plugin discovery error: %v", err)
			}
			if response.Detail.Code != wantCode {
				t.Fatalf("plugin discovery error code = %q, want %q", response.Detail.Code, wantCode)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(queries) != 0 {
				t.Fatal("queried raw catalog after plugin discovery failed")
			}
		})
	}
}
