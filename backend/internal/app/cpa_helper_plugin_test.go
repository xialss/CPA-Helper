package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

func TestCPAHelperPluginKeysSummary(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	apiKeys := []string{}
	policy := map[string]any{
		"contract_version": "v1", "plugin_version": "0.1.4", "policy_revision": 1,
		"groups": []any{}, "keys": []any{},
	}
	health := map[string]any{"status": "ok", "active": map[string]int{}}
	policyStatus, healthStatus := http.StatusOK, http.StatusOK
	var policyErrorPayload []byte
	policyReads, healthReads := 0, 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, `{"error":{"code":"unauthorized","message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/config":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "GET /v0/management/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": apiKeys})
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&apiKeys); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/policy":
			policyReads++
			if policyStatus != http.StatusOK {
				if policyErrorPayload != nil {
					w.WriteHeader(policyStatus)
					_, _ = w.Write(policyErrorPayload)
					return
				}
				http.Error(w, `{"error":{"code":"policy_unavailable","message":"policy unavailable"}}`, policyStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(policy)
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			healthReads++
			if healthStatus != http.StatusOK {
				http.Error(w, `{"error":{"code":"health_unavailable","message":"health unavailable"}}`, healthStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(health)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()
	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	handler := app.Routes()
	adminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cpa.URL, "management_key": "management-key", "collector_enabled": false}, adminCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil, nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil, memberCookies, http.StatusForbidden)
	mu.Lock()
	if policyReads != 0 || healthReads != 0 {
		t.Errorf("unauthorized or unready user reached plugin: policy=%d health=%d", policyReads, healthReads)
	}
	mu.Unlock()
	memberCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil)
	var empty struct {
		Keys []json.RawMessage `json:"keys"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil, memberCookies, &empty)
	if empty.Keys == nil || len(empty.Keys) != 0 {
		t.Fatalf("empty account keys = %s, want []", empty.Keys)
	}
	var adminKey, memberKey, unconfiguredKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "admin-key"}, adminCookies, &adminKey)
	t.Run("upstream error bodies and codes never reach the API response", func(t *testing.T) {
		if adminKey.APIKey == nil {
			t.Fatal("admin key lacks raw value for redaction fixture")
		}
		secretCode := "remote-code-" + *adminKey.APIKey
		secretMessage := "management-key=" + "management-key" + " api-key=" + *adminKey.APIKey
		jsonBody, err := json.Marshal(map[string]any{"error": map[string]string{"code": secretCode, "message": secretMessage}})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name string
			body []byte
		}{
			{name: "json", body: jsonBody},
			{name: "plain text", body: []byte(secretCode + " " + secretMessage)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				mu.Lock()
				policyStatus, policyErrorPayload = http.StatusInternalServerError, tc.body
				mu.Unlock()
				defer func() {
					mu.Lock()
					policyStatus, policyErrorPayload = http.StatusOK, nil
					mu.Unlock()
				}()
				request := httptest.NewRequest(http.MethodGet, "/api/cpa-helper-plugin/policy", nil)
				for _, cookie := range adminCookies {
					request.AddCookie(cookie)
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				var response struct {
					Detail struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"detail"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if recorder.Code != http.StatusServiceUnavailable || response.Detail.Code != "plugin_unavailable" {
					t.Fatalf("upstream error = %d %#v, want 503 plugin_unavailable", recorder.Code, response.Detail)
				}
				for _, secret := range []string{"management-key", *adminKey.APIKey, secretCode, secretMessage} {
					if strings.Contains(recorder.Body.String(), secret) {
						t.Fatalf("upstream response leaked secret %q: %s", secret, recorder.Body.String())
					}
				}
			})
		}
	})
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "member-key"}, memberCookies, &memberKey)
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "unconfigured-key"}, memberCookies, &unconfiguredKey)
	scopeForKey := func(key backendApp.UserApiKeySummary) string {
		t.Helper()
		if key.APIKey == nil || *key.APIKey == "" {
			t.Fatal("created key lacks raw key for caller-scope fixture")
		}
		sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(*key.APIKey)))
		return hex.EncodeToString(sum[:])
	}
	adminScope, memberScope, unconfiguredScope := scopeForKey(adminKey), scopeForKey(memberKey), scopeForKey(unconfiguredKey)
	foreignScope := strings.Repeat("a", 64)
	mu.Lock()
	policy["groups"] = []any{
		map[string]any{"id": "first", "name": "First rule"},
		map[string]any{"id": "second", "name": "第二规则"},
		map[string]any{"id": "admin-only", "name": "Private admin rule"},
	}
	policy["keys"] = []any{
		map[string]any{"id": memberScope, "enabled": false, "group_ids": []string{"second", "first"}, "max_concurrency": 5},
		map[string]any{"id": adminScope, "group_ids": []string{"admin-only"}, "max_concurrency": 9},
		map[string]any{"id": foreignScope, "group_ids": []string{"admin-only"}, "max_concurrency": 6},
	}
	health["active"] = map[string]int{memberScope: 3, adminScope: 8, foreignScope: 2}
	mu.Unlock()
	type keySummary struct {
		APIKeyHash         string   `json:"api_key_hash"`
		Enabled            bool     `json:"enabled"`
		GroupNames         []string `json:"group_names"`
		CurrentConcurrency int      `json:"current_concurrency"`
		MaxConcurrency     int      `json:"max_concurrency"`
		HasCustomRule      bool     `json:"has_custom_rule"`
	}
	t.Run("custom rule has a required boolean wire contract", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			rule any
			omit bool
			want bool
		}{
			{name: "omitted", omit: true},
			{name: "null", rule: nil},
			{name: "empty object", rule: map[string]any{}},
			{name: "empty lists", rule: map[string]any{"models": []string{}, "denied_models": nil, "credential_ids": []string{}, "denied_credential_ids": []string{}, "credential_providers": []any{}, "denied_credential_providers": []any{}}},
			{name: "nonempty independent rule", rule: map[string]any{"denied_models": []string{"private-model"}}, want: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mu.Lock()
				entry := policy["keys"].([]any)[0].(map[string]any)
				if tc.omit {
					delete(entry, "rule")
				} else {
					entry["rule"] = tc.rule
				}
				mu.Unlock()
				var wire struct {
					Keys []map[string]json.RawMessage `json:"keys"`
				}
				requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil, memberCookies, &wire)
				found := false
				for _, key := range wire.Keys {
					var hash string
					if err := json.Unmarshal(key["api_key_hash"], &hash); err != nil {
						t.Fatal(err)
					}
					if hash != memberKey.APIKeyHash {
						continue
					}
					found = true
					raw, exists := key["has_custom_rule"]
					if !exists || (string(raw) != "true" && string(raw) != "false") {
						t.Fatalf("has_custom_rule must be present and boolean: %s", raw)
					}
					if string(raw) != map[bool]string{true: "true", false: "false"}[tc.want] {
						t.Fatalf("has_custom_rule = %s, want %t", raw, tc.want)
					}
				}
				if !found {
					t.Fatal("member key missing from summary")
				}
			})
		}
		mu.Lock()
		delete(policy["keys"].([]any)[0].(map[string]any), "rule")
		mu.Unlock()
	})
	readSummary := func(t *testing.T, cookies []*http.Cookie) map[string]keySummary {
		t.Helper()
		mu.Lock()
		policyReads, healthReads = 0, 0
		mu.Unlock()
		var response struct {
			Keys []keySummary `json:"keys"`
		}
		requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil, cookies, &response)
		mu.Lock()
		if policyReads != 1 || healthReads != 1 {
			t.Errorf("summary read count: policy=%d health=%d, want one each", policyReads, healthReads)
		}
		mu.Unlock()
		byHash := make(map[string]keySummary, len(response.Keys))
		for _, key := range response.Keys {
			byHash[key.APIKeyHash] = key
		}
		return byHash
	}
	t.Run("member scope mapping and absent scope zero", func(t *testing.T) {
		got := readSummary(t, memberCookies)
		want := map[string]keySummary{
			memberKey.APIKeyHash:       {APIKeyHash: memberKey.APIKeyHash, Enabled: false, GroupNames: []string{"第二规则", "First rule"}, CurrentConcurrency: 3, MaxConcurrency: 5, HasCustomRule: false},
			unconfiguredKey.APIKeyHash: {APIKeyHash: unconfiguredKey.APIKeyHash, Enabled: true, GroupNames: []string{}, CurrentConcurrency: 0, MaxConcurrency: 0, HasCustomRule: false},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("member keys summary = %#v, want %#v", got, want)
		}
	})
	t.Run("super admin account remains owner scoped", func(t *testing.T) {
		got := readSummary(t, adminCookies)
		want := map[string]keySummary{adminKey.APIKeyHash: {APIKeyHash: adminKey.APIKeyHash, Enabled: true, GroupNames: []string{"Private admin rule"}, CurrentConcurrency: 8, MaxConcurrency: 9, HasCustomRule: false}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("admin keys summary = %#v, want %#v", got, want)
		}
	})
	t.Run("live counts include unconfigured key", func(t *testing.T) {
		mu.Lock()
		health["active"] = map[string]int{memberScope: 1, unconfiguredScope: 4}
		mu.Unlock()
		got := readSummary(t, memberCookies)
		if got[memberKey.APIKeyHash].CurrentConcurrency != 1 || got[unconfiguredKey.APIKeyHash].CurrentConcurrency != 4 || got[unconfiguredKey.APIKeyHash].MaxConcurrency != 0 {
			t.Fatalf("updated current concurrency = %#v", got)
		}
	})
	t.Run("empty active map means zero", func(t *testing.T) {
		mu.Lock()
		health["active"] = map[string]int{}
		mu.Unlock()
		for _, key := range readSummary(t, memberCookies) {
			if key.CurrentConcurrency != 0 {
				t.Fatalf("empty active snapshot reported concurrency: %#v", key)
			}
		}
	})
	t.Run("aggregate endpoint keeps admin and account scopes separate", func(t *testing.T) {
		mu.Lock()
		health["active"] = map[string]int{memberScope: 3, adminScope: 8, foreignScope: 2}
		mu.Unlock()
		var adminTotal struct {
			Scope              string `json:"scope"`
			CurrentConcurrency int    `json:"current_concurrency"`
		}
		requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/concurrency-summary?scope=admin", nil, adminCookies, &adminTotal)
		if adminTotal.Scope != "admin" || adminTotal.CurrentConcurrency != 13 {
			t.Fatalf("admin concurrency summary = %#v, want 13", adminTotal)
		}
		var memberTotal struct {
			Scope              string `json:"scope"`
			CurrentConcurrency int    `json:"current_concurrency"`
		}
		requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/concurrency-summary?scope=account", nil, memberCookies, &memberTotal)
		if memberTotal.Scope != "account" || memberTotal.CurrentConcurrency != 3 {
			t.Fatalf("member concurrency summary = %#v, want 3", memberTotal)
		}
		requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/concurrency-summary?scope=admin", nil, memberCookies, http.StatusForbidden)
	})
	t.Run("aggregate endpoint reports plugin unavailable", func(t *testing.T) {
		mu.Lock()
		healthStatus = http.StatusNotFound
		mu.Unlock()
		defer func() {
			mu.Lock()
			healthStatus = http.StatusOK
			mu.Unlock()
		}()
		req := httptest.NewRequest(http.MethodGet, "/api/cpa-helper-plugin/concurrency-summary?scope=admin", nil)
		for _, cookie := range adminCookies {
			req.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("unavailable plugin status = %d, want 503: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Detail struct {
				Code string `json:"code"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Detail.Code != "plugin_not_loaded" {
			t.Fatalf("unavailable plugin code = %q, want plugin_not_loaded", response.Detail.Code)
		}
	})
	for _, tc := range []struct {
		name          string
		active        any
		omitActive    bool
		policyStatus  int
		healthStatus  int
		wantStatus    int
		wantCode      string
		wantHealthGet int
	}{
		{name: "missing active", omitActive: true, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "null active", wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "array active", active: []int{}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "string active", active: "unavailable", wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "null count", active: map[string]any{memberScope: nil}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "string count", active: map[string]any{memberScope: "3"}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "fractional count", active: map[string]any{memberScope: 1.5}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "negative count", active: map[string]any{memberScope: -1}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "invalid foreign count", active: map[string]any{foreignScope: -1}, wantCode: "plugin_incompatible", wantHealthGet: 1},
		{name: "policy unavailable", policyStatus: http.StatusServiceUnavailable, wantCode: "plugin_unavailable"},
		{name: "health unavailable", healthStatus: http.StatusServiceUnavailable, wantCode: "plugin_unavailable", wantHealthGet: 1},
		{name: "plugin not loaded", policyStatus: http.StatusNotFound, wantCode: "plugin_not_loaded"},
		{name: "management authentication", healthStatus: http.StatusUnauthorized, wantStatus: http.StatusBadGateway, wantCode: "plugin_auth_failed", wantHealthGet: 1},
		{name: "management forbidden", healthStatus: http.StatusForbidden, wantStatus: http.StatusBadGateway, wantCode: "plugin_auth_failed", wantHealthGet: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			policyReads, healthReads = 0, 0
			policyStatus, healthStatus = http.StatusOK, http.StatusOK
			if tc.policyStatus != 0 {
				policyStatus = tc.policyStatus
			}
			if tc.healthStatus != 0 {
				healthStatus = tc.healthStatus
			}
			health["active"] = tc.active
			if tc.omitActive {
				delete(health, "active")
			}
			mu.Unlock()
			req := httptest.NewRequest(http.MethodGet, "/api/cpa-helper-plugin/keys-summary", nil)
			for _, cookie := range memberCookies {
				req.AddCookie(cookie)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			wantStatus := tc.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusServiceUnavailable
			}
			var response struct {
				Detail struct {
					Code string `json:"code"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if recorder.Code != wantStatus || response.Detail.Code != tc.wantCode {
				t.Fatalf("summary failure = %d %s, want %d %s", recorder.Code, recorder.Body.String(), wantStatus, tc.wantCode)
			}
			mu.Lock()
			if policyReads != 1 || healthReads != tc.wantHealthGet {
				t.Errorf("failure read count: policy=%d health=%d, want policy=1 health=%d", policyReads, healthReads, tc.wantHealthGet)
			}
			mu.Unlock()
			if strings.Contains(recorder.Body.String(), "\"keys\"") || strings.Contains(recorder.Body.String(), *memberKey.APIKey) {
				t.Fatalf("failed telemetry returned keys or raw key: %s", recorder.Body.String())
			}
		})
	}
}

func TestCPAHelperPluginPolicyAndDirectoryContracts(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	apiKeys := []string{"downstream-directory-key"}
	pluginVersion := "0.1.4"
	defaultLegacyConfig := map[string]any{
		"gemini-api-key":       []any{map[string]any{"name": "Gemini", "api-key": "secret-config-key", "base-url": "https://gemini.example", "disabled": false}},
		"openai-compatibility": []any{map[string]any{"name": "compat", "base-url": "https://compat.example", "api-key-entries": []any{map[string]any{"api-key": "secret-compat-key"}}}},
	}
	legacyConfig := defaultLegacyConfig
	defaultV8Credentials := []any{map[string]any{"id": "oauth-file", "auth_index": "oauth-index", "provider": "claude", "name": "OAuth", "source": "file", "secret": "must-not-leak"}}
	v8Credentials := defaultV8Credentials
	policyWrites, upstreamRequests := 0, 0
	modelQueries := []string{}
	modelCatalogs := map[string][]any{
		"downstream-directory-key": {map[string]any{"id": "gpt-test"}, map[string]any{"id": "claude-test"}},
	}
	failedModelKey := ""
	holdResponseConfig := false
	responseConfigWrites := 0
	var validationResponseOverride map[string]any
	policy := map[string]any{
		"contract_version": "v1", "plugin_version": "0.1.4", "policy_revision": 1,
		"generated_at": "2026-09-30T00:00:00Z", "groups": []any{
			map[string]any{"id": "public", "name": "Public", "note": "keep", "rule": map[string]any{"models": []string{"gpt-test"}}},
		}, "keys": []any{},
	}
	responseConfig := map[string]any{
		"enabled": false, "stream_enabled": true, "responses_only_stream": true,
		"max_buffer_bytes": 16777216, "max_total_buffer_bytes": 67108864,
		"action": "reject", "unknown_action": "pass", "case_sensitive": false,
		"ignore_thinking_suffix": true, "ignored_models": nil,
		"accepted_models": map[string]any{"request-model": []string{"response-model"}},
	}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		upstreamRequests++
		if r.URL.Path == "/v1/models" {
			authorized := false
			for _, key := range apiKeys {
				if r.Header.Get("Authorization") == "Bearer "+key {
					authorized = true
					break
				}
			}
			if !authorized {
				http.Error(w, `{"error":{"code":"unauthorized","message":"bad key"}}`, http.StatusUnauthorized)
				return
			}
		} else if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, `{"error":{"code":"unauthorized","message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/config":
			_ = json.NewEncoder(w).Encode(legacyConfig)
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]any{"id": "oauth-file", "provider": "claude", "name": "OAuth", "source": "file", "secret": "must-not-leak"}}})
		case "GET /v8/management/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"access": map[string]any{"api-keys": apiKeys}, "api-keys": map[string]any{}})
		case "GET /v8/management/credentials":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": v8Credentials})
		case "GET /v0/management/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": apiKeys})
		case "PATCH /v0/management/api-keys":
			http.NotFound(w, r)
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&apiKeys); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "GET /v1/models":
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			modelQueries = append(modelQueries, key)
			if key == failedModelKey {
				http.Error(w, `{"error":"models unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			items := modelCatalogs[key]
			if items == nil {
				items = []any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_id": "cpa-helper-plugin", "plugin_version": pluginVersion, "cpa_version": "v8.0.12", "contract_version": "v1", "response_model_mismatch": responseConfig})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "policy_revision": policy["policy_revision"], "contract_version": "v1", "plugin_version": pluginVersion, "active": map[string]int{strings.Repeat("a", 64): 3, strings.Repeat("b", 64): 9}})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/policy":
			w.Header().Set("ETag", `"1"`)
			_ = json.NewEncoder(w).Encode(policy)
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/policy":
			policyWrites++
			if r.Header.Get("If-Match") != `"1"` || r.Header.Get("Idempotency-Key") == "" {
				http.Error(w, `{"error":{"code":"precondition_required","message":"revision required"}}`, http.StatusPreconditionRequired)
				return
			}
			var next map[string]any
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			next["policy_revision"] = float64(2)
			policy = next
			_ = json.NewEncoder(w).Encode(map[string]any{"policy_revision": 2})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/directory":
			_ = json.NewEncoder(w).Encode(map[string]any{"credentials": []any{map[string]any{"ref": "sha256:" + strings.Repeat("a", 64), "provider": "openai", "source": "ai-providers", "label": "OpenAI", "status": "active", "secret": "must-not-leak"}}})
		case "POST /v0/management/plugins/cpa-helper-plugin/v1/response-model/validate":
			var next map[string]any
			_ = json.NewDecoder(r.Body).Decode(&next)
			if next["action"] == "invalid" {
				http.Error(w, `{"error":{"code":"invalid_response_model_config","message":"invalid action"}}`, http.StatusBadRequest)
				return
			}
			if validationResponseOverride != nil {
				_ = json.NewEncoder(w).Encode(validationResponseOverride)
				return
			}
			_ = json.NewEncoder(w).Encode(next)
		case "PATCH /v0/management/plugins/cpa-helper-plugin/config":
			responseConfigWrites++
			var patch struct {
				Response map[string]any `json:"response_model_mismatch"`
			}
			_ = json.NewDecoder(r.Body).Decode(&patch)
			if !holdResponseConfig {
				responseConfig = patch.Response
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "PUT /v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch":
			responseConfigWrites++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cpa.URL, "model_request_url": cpa.URL, "management_key": "management-key", "collector_enabled": false}, cookies, nil)
	var adminKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "admin-key"}, cookies, &adminKey)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, cookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	memberCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy/key/"+adminKey.APIKeyHash, nil, memberCookies, http.StatusNotFound)
	t.Run("foreign key PUT is rejected before upstream write", func(t *testing.T) {
		var adminPolicy map[string]any
		requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy/key/"+adminKey.APIKeyHash, nil, cookies, &adminPolicy)
		foreignKey := adminPolicy["key"].(map[string]any)
		foreignKey["enabled"] = false
		mu.Lock()
		writesBefore := policyWrites
		mu.Unlock()
		requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy/key/"+adminKey.APIKeyHash, map[string]any{
			"policy_revision": adminPolicy["policy_revision"], "key": foreignKey,
		}, memberCookies, http.StatusNotFound)
		mu.Lock()
		defer mu.Unlock()
		if policyWrites != writesBefore {
			t.Fatalf("cross-user PUT reached plugin: writes=%d, before=%d", policyWrites, writesBefore)
		}
	})
	var memberKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "member-key"}, memberCookies, &memberKey)
	var memberPolicy map[string]any
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy/key/"+memberKey.APIKeyHash, nil, memberCookies, &memberPolicy)
	staleKey := memberPolicy["key"].(map[string]any)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy/key/"+memberKey.APIKeyHash, map[string]any{"policy_revision": 999, "key": staleKey}, memberCookies, http.StatusConflict)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, memberCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, memberCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", map[string]any{}, memberCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/status", nil, nil, http.StatusUnauthorized)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "manager", "password": "manager-password", "nickname": "Manager", "is_admin": true}, cookies, nil)
	managerCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "manager", "password": "manager-password"}, nil, nil)
	managerCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "manager-password", "password": "manager-new-password"}, managerCookies, nil)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, managerCookies, http.StatusForbidden)
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, managerCookies, http.StatusForbidden)
	for _, role := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{"member", memberCookies}, {"manager", managerCookies}, {"super admin", cookies},
	} {
		t.Run("status redacts caller scopes for "+role.name, func(t *testing.T) {
			var result struct {
				Capabilities map[string]any `json:"capabilities"`
				Health       map[string]any `json:"health"`
			}
			requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/status", nil, role.cookies, &result)
			if _, exposed := result.Health["active"]; exposed {
				t.Fatalf("status exposed caller-scope activity: %#v", result.Health)
			}
			if result.Health["status"] != "ok" || result.Health["policy_revision"] != float64(1) {
				t.Fatalf("status lost safe diagnostic fields: %#v", result.Health)
			}
			if _, exposed := result.Capabilities["response_model_mismatch"]; exposed != (role.name == "super admin") {
				t.Fatalf("response-model configuration permissions changed: %#v", result.Capabilities)
			}
		})
	}

	var gotPolicy map[string]any
	for _, field := range []string{"groups", "keys"} {
		for _, malformed := range []struct {
			name  string
			value any
			omit  bool
		}{
			{name: "missing", omit: true}, {name: "null", value: nil}, {name: "wrong type", value: "invalid"},
		} {
			t.Run("upstream policy "+field+" "+malformed.name+" fails closed", func(t *testing.T) {
				mu.Lock()
				original, existed := policy[field]
				writesBefore := policyWrites
				if malformed.omit {
					delete(policy, field)
				} else {
					policy[field] = malformed.value
				}
				mu.Unlock()
				defer func() {
					mu.Lock()
					if existed {
						policy[field] = original
					} else {
						delete(policy, field)
					}
					mu.Unlock()
				}()
				request := httptest.NewRequest(http.MethodGet, "/api/cpa-helper-plugin/policy", nil)
				for _, cookie := range cookies {
					request.AddCookie(cookie)
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				var response struct {
					Detail struct {
						Code string `json:"code"`
					} `json:"detail"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if recorder.Code != http.StatusServiceUnavailable || response.Detail.Code != "plugin_incompatible" {
					t.Fatalf("incomplete policy = %d %s, want 503 plugin_incompatible", recorder.Code, recorder.Body.String())
				}
				mu.Lock()
				defer mu.Unlock()
				if policyWrites != writesBefore {
					t.Fatalf("invalid policy snapshot caused a write: %d -> %d", writesBefore, policyWrites)
				}
			})
		}
	}
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, cookies, &gotPolicy)
	if gotPolicy["policy_revision"] != float64(1) || len(gotPolicy["groups"].([]any)) != 1 {
		t.Fatalf("policy response = %#v", gotPolicy)
	}
	policyKeys, ok := gotPolicy["keys"].([]any)
	if !ok || len(policyKeys) < 2 {
		t.Fatalf("policy response did not expose existing Helper API keys: %#v", gotPolicy["keys"])
	}
	for _, raw := range policyKeys {
		key, ok := raw.(map[string]any)
		if !ok || key["group_ids"] == nil || key["rule"] == nil {
			t.Fatalf("policy key was not normalized: %#v", raw)
		}
	}
	for _, malformed := range []struct {
		name   string
		policy map[string]any
	}{
		{name: "missing keys", policy: map[string]any{"groups": gotPolicy["groups"]}},
		{name: "null keys", policy: map[string]any{"groups": gotPolicy["groups"], "keys": nil}},
		{name: "wrong groups type", policy: map[string]any{"groups": "invalid", "keys": gotPolicy["keys"]}},
	} {
		t.Run("client policy "+malformed.name+" is rejected before PUT", func(t *testing.T) {
			requestBody := map[string]any{"policy_revision": gotPolicy["policy_revision"]}
			for key, value := range malformed.policy {
				requestBody[key] = value
			}
			mu.Lock()
			writesBefore := policyWrites
			mu.Unlock()
			requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", requestBody, cookies, http.StatusUnprocessableEntity)
			mu.Lock()
			defer mu.Unlock()
			if policyWrites != writesBefore {
				t.Fatalf("invalid client snapshot reached plugin PUT: %d -> %d", writesBefore, policyWrites)
			}
		})
	}
	var directory struct {
		Models              []string         `json:"models"`
		Credentials         []map[string]any `json:"credentials"`
		CredentialProviders []map[string]any `json:"credential_providers"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, &directory)
	if len(directory.Credentials) < 3 || directory.Credentials[0]["secret"] != nil {
		t.Fatalf("directory response leaked or lost credentials: %#v", directory)
	}
	if len(directory.Models) != 2 || len(directory.CredentialProviders) < 2 {
		t.Fatalf("directory did not aggregate CPA models/providers: %#v", directory)
	}
	for _, entry := range directory.Credentials {
		if strings.Contains(entry["label"].(string), "secret-") {
			t.Fatalf("directory leaked configured credential: %#v", entry)
		}
		if entry["source"] == "api-keys" {
			t.Fatalf("directory exposed downstream key as scheduler credential: %#v", entry)
		}
	}
	for _, entry := range directory.CredentialProviders {
		if entry["source"] != "auth-files" && entry["source"] != "ai-providers" {
			t.Fatalf("directory exposed an unsupported credential category: %#v", entry)
		}
	}
	t.Run("v8 sanitizer omits indexes without hiding valid credentials", func(t *testing.T) {
		mu.Lock()
		pluginVersion = "0.1.5"
		v8Credentials = []any{
			map[string]any{"id": "oauth-file", "provider": "claude", "name": "Sanitized duplicate"},
			map[string]any{"id": "sanitized-null", "auth_index": nil, "provider": "claude", "name": "Sanitized null"},
			defaultV8Credentials[0],
		}
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			pluginVersion, v8Credentials = "0.1.4", defaultV8Credentials
			mu.Unlock()
		})
		requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, &directory)
		if len(directory.Credentials) != 2 {
			t.Fatalf("sanitized v8 directory = %#v, want plugin credential and valid OAuth file", directory)
		}
		foundOAuth := false
		for _, entry := range directory.Credentials {
			if strings.Contains(entry["label"].(string), "Sanitized") {
				t.Fatalf("sanitized credential appeared in directory: %#v", entry)
			}
			if entry["auth_index"] == "oauth-index" && entry["source"] == "auth-files" {
				foundOAuth = true
			}
		}
		if !foundOAuth {
			t.Fatalf("valid OAuth credential disappeared: %#v", directory)
		}
	})
	t.Run("v8 malformed auth index remains an explicit error", func(t *testing.T) {
		mu.Lock()
		pluginVersion = "0.1.5"
		v8Credentials = []any{defaultV8Credentials[0], map[string]any{"id": "malformed-file", "auth_index": 12, "provider": "claude"}}
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			pluginVersion, v8Credentials = "0.1.4", defaultV8Credentials
			mu.Unlock()
		})
		requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, http.StatusUnprocessableEntity)
	})
	for _, field := range []string{"gemini-api-key", "interactions-api-key", "claude-api-key", "codex-api-key", "xai-api-key", "meta-api-key", "openai-compatibility", "vertex-api-key"} {
		for _, tc := range []struct {
			name  string
			value any
		}{
			{"non-array", "malformed"},
			{"non-object entry", []any{map[string]any{}, "malformed"}},
		} {
			t.Run("legacy "+field+" "+tc.name+" fails", func(t *testing.T) {
				mu.Lock()
				legacyConfig = map[string]any{field: tc.value}
				mu.Unlock()
				t.Cleanup(func() {
					mu.Lock()
					legacyConfig = defaultLegacyConfig
					mu.Unlock()
				})
				requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, http.StatusUnprocessableEntity)
			})
		}
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"non-array", "malformed"},
		{"non-object entry", []any{map[string]any{"api-key": "secret-compat-key"}, "malformed"}},
	} {
		t.Run("legacy OpenAI api-key-entries "+tc.name+" fails", func(t *testing.T) {
			mu.Lock()
			legacyConfig = map[string]any{"openai-compatibility": []any{map[string]any{"name": "compat", "base-url": "https://compat.example", "api-key-entries": tc.value}}}
			mu.Unlock()
			t.Cleanup(func() {
				mu.Lock()
				legacyConfig = defaultLegacyConfig
				mu.Unlock()
			})
			requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, http.StatusUnprocessableEntity)
		})
	}
	for _, tc := range []struct {
		name   string
		config map[string]any
	}{
		{"missing", map[string]any{}},
		{"null", map[string]any{"gemini-api-key": nil, "openai-compatibility": nil, "vertex-api-key": nil}},
		{"empty", map[string]any{"gemini-api-key": []any{}, "openai-compatibility": []any{}, "vertex-api-key": []any{}}},
	} {
		t.Run("legacy "+tc.name+" optional fields remain valid", func(t *testing.T) {
			mu.Lock()
			legacyConfig = tc.config
			mu.Unlock()
			t.Cleanup(func() {
				mu.Lock()
				legacyConfig = defaultLegacyConfig
				mu.Unlock()
			})
			requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, &directory)
			if len(directory.Credentials) != 2 || len(directory.Models) != 2 {
				t.Fatalf("optional legacy fields hid valid directory entries: %#v", directory)
			}
		})
	}
	for _, state := range []string{"missing", "null", "empty"} {
		t.Run("legacy keyless OpenAI "+state+" keys remain valid", func(t *testing.T) {
			entry := map[string]any{"name": "keyless", "base-url": "https://keyless.example"}
			if state == "null" {
				entry["api-key-entries"] = nil
			} else if state == "empty" {
				entry["api-key-entries"] = []any{}
			}
			mu.Lock()
			legacyConfig = map[string]any{"openai-compatibility": []any{entry}}
			mu.Unlock()
			t.Cleanup(func() {
				mu.Lock()
				legacyConfig = defaultLegacyConfig
				mu.Unlock()
			})
			requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, &directory)
			if len(directory.Credentials) != 3 {
				t.Fatalf("keyless OpenAI credential disappeared: %#v", directory)
			}
		})
	}
	var secondMemberKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "second-member-key"}, memberCookies, &secondMemberKey)
	if adminKey.APIKey == nil || memberKey.APIKey == nil || secondMemberKey.APIKey == nil {
		t.Fatal("created keys lack raw values for model-catalog fixtures")
	}
	mu.Lock()
	apiKeys = []string{"downstream-directory-key", *adminKey.APIKey, *memberKey.APIKey, *secondMemberKey.APIKey}
	modelCatalogs["downstream-directory-key"] = []any{}
	modelCatalogs[*adminKey.APIKey] = []any{map[string]any{"id": "admin-only"}, map[string]any{"id": "shared"}}
	modelCatalogs[*memberKey.APIKey] = []any{map[string]any{"id": "member-only"}, map[string]any{"id": "shared"}}
	modelCatalogs[*secondMemberKey.APIKey] = []any{map[string]any{"id": "second-member-only"}, map[string]any{"id": "shared"}}
	mu.Unlock()
	for _, version := range []string{"0.1.4", "0.1.5"} {
		t.Run("directory scope "+version, func(t *testing.T) {
			mu.Lock()
			pluginVersion = version
			mu.Unlock()
			for _, tc := range []struct {
				name        string
				cookies     []*http.Cookie
				hash        string
				wantModels  []string
				wantQueries []string
			}{
				{"global union after empty first key", cookies, "", []string{"admin-only", "member-only", "second-member-only", "shared"}, []string{"downstream-directory-key", *adminKey.APIKey, *memberKey.APIKey, *secondMemberKey.APIKey}},
				{"account union", memberCookies, "", []string{"member-only", "second-member-only", "shared"}, []string{*memberKey.APIKey, *secondMemberKey.APIKey}},
				{"selected member key", memberCookies, memberKey.APIKeyHash, []string{"member-only", "shared"}, []string{*memberKey.APIKey}},
				{"selected key for super admin", cookies, memberKey.APIKeyHash, []string{"member-only", "shared"}, []string{*memberKey.APIKey}},
				{"account with no keys", managerCookies, "", []string{}, []string{}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					mu.Lock()
					modelQueries = []string{}
					mu.Unlock()
					path := "/api/cpa-helper-plugin/directory"
					if tc.hash != "" {
						path += "?api_key_hash=" + tc.hash
					}
					var result struct {
						Models              []string         `json:"models"`
						Credentials         []map[string]any `json:"credentials"`
						CredentialProviders []map[string]any `json:"credential_providers"`
					}
					requestJSON(t, handler, http.MethodGet, path, nil, tc.cookies, &result)
					if !reflect.DeepEqual(result.Models, tc.wantModels) {
						t.Fatalf("models = %#v, want %#v", result.Models, tc.wantModels)
					}
					mu.Lock()
					queries := append([]string{}, modelQueries...)
					mu.Unlock()
					sort.Strings(queries)
					wantQueries := append([]string{}, tc.wantQueries...)
					sort.Strings(wantQueries)
					if !reflect.DeepEqual(queries, wantQueries) {
						t.Fatalf("queried keys = %#v, want %#v", queries, wantQueries)
					}
					if len(result.Credentials) == 0 || len(result.CredentialProviders) == 0 {
						t.Fatal("directory lost upstream routing options")
					}
					for _, entry := range result.Credentials {
						if entry["source"] != "auth-files" && entry["source"] != "ai-providers" {
							t.Fatalf("unsupported scheduler credential: %#v", entry)
						}
					}
					for _, entry := range result.CredentialProviders {
						if entry["source"] != "auth-files" && entry["source"] != "ai-providers" {
							t.Fatalf("unsupported scheduler category: %#v", entry)
						}
					}
				})
			}
			t.Run("foreign key rejected before CPA access", func(t *testing.T) {
				mu.Lock()
				requestsBefore := upstreamRequests
				mu.Unlock()
				requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+adminKey.APIKeyHash, nil, memberCookies, http.StatusNotFound)
				mu.Lock()
				defer mu.Unlock()
				if upstreamRequests != requestsBefore {
					t.Fatalf("cross-user directory reached CPA: requests=%d, before=%d", upstreamRequests, requestsBefore)
				}
			})
			t.Run("failed catalog query remains an error", func(t *testing.T) {
				mu.Lock()
				failedModelKey = *memberKey.APIKey
				mu.Unlock()
				t.Cleanup(func() {
					mu.Lock()
					failedModelKey = ""
					mu.Unlock()
				})
				requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, cookies, http.StatusUnprocessableEntity)
				requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+memberKey.APIKeyHash, nil, memberCookies, http.StatusUnprocessableEntity)
			})
		})
	}
	mu.Lock()
	pluginVersion = "0.1.4"
	mu.Unlock()
	var gotConfig map[string]any
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, cookies, &gotConfig)
	if gotConfig["action"] != "reject" || gotConfig["enabled"] != false {
		t.Fatalf("response config = %#v", gotConfig)
	}
	if !reflect.DeepEqual(gotConfig["ignored_models"], []any{}) {
		t.Fatalf("null ignored_models was not normalized: %#v", gotConfig)
	}
	if !reflect.DeepEqual(gotConfig["accepted_models"], map[string]any{"request-model": []any{"response-model"}}) {
		t.Fatalf("accepted_models mapping changed: %#v", gotConfig["accepted_models"])
	}
	baseResponseConfig := responseConfig
	for _, tc := range []struct {
		name      string
		value     any
		omitField bool
		wantEmpty bool
	}{
		{name: "missing", omitField: true, wantEmpty: true},
		{name: "null", value: nil, wantEmpty: true},
		{name: "string", value: "invalid"},
		{name: "array", value: []any{}},
		{name: "model list string", value: map[string]any{"request": "response"}},
		{name: "model list number", value: map[string]any{"request": 42}},
		{name: "model list object", value: map[string]any{"request": map[string]any{}}},
		{name: "model list null", value: map[string]any{"request": nil}},
		{name: "empty model list", value: map[string]any{"request": []any{}}},
	} {
		t.Run("response accepted_models "+tc.name, func(t *testing.T) {
			current := make(map[string]any, len(baseResponseConfig))
			for field, value := range baseResponseConfig {
				current[field] = value
			}
			if tc.omitField {
				delete(current, "accepted_models")
			} else {
				current["accepted_models"] = tc.value
			}
			mu.Lock()
			responseConfig = current
			mu.Unlock()
			defer func() {
				mu.Lock()
				responseConfig = baseResponseConfig
				mu.Unlock()
			}()
			if tc.wantEmpty {
				var config map[string]any
				requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, cookies, &config)
				if !reflect.DeepEqual(config["accepted_models"], map[string]any{}) {
					t.Fatalf("empty accepted_models = %#v, want {}", config["accepted_models"])
				}
				return
			}
			request := httptest.NewRequest(http.MethodGet, "/api/cpa-helper-plugin/response-model", nil)
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			var response struct {
				Detail struct {
					Code string `json:"code"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusServiceUnavailable || response.Detail.Code != "plugin_incompatible" {
				t.Fatalf("malformed accepted_models = %d %s, want 503 plugin_incompatible", recorder.Code, recorder.Body.String())
			}
		})
	}
	updatedConfig := map[string]any{"enabled": true, "stream_enabled": true, "responses_only_stream": true, "max_buffer_bytes": 16777216, "max_total_buffer_bytes": 67108864, "action": "audit", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": nil, "accepted_models": map[string]any{}}
	t.Run("malformed validator response does not write host config", func(t *testing.T) {
		mu.Lock()
		validationResponseOverride = map[string]any{
			"ignored_models": []string{}, "accepted_models": map[string]any{"request-model": "invalid"},
		}
		writesBefore := responseConfigWrites
		mu.Unlock()
		defer func() {
			mu.Lock()
			validationResponseOverride = nil
			mu.Unlock()
		}()
		payload, err := json.Marshal(updatedConfig)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/cpa-helper-plugin/response-model", strings.NewReader(string(payload)))
		request.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var response struct {
			Detail struct {
				Code string `json:"code"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusServiceUnavailable || response.Detail.Code != "plugin_incompatible" {
			t.Fatalf("malformed validator response = %d %s, want 503 plugin_incompatible", recorder.Code, recorder.Body.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if responseConfigWrites != writesBefore {
			t.Fatalf("malformed validator response reached host PATCH/PUT: %d -> %d", writesBefore, responseConfigWrites)
		}
	})
	requestJSON(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", updatedConfig, cookies, &gotConfig)
	if gotConfig["action"] != "audit" || gotConfig["enabled"] != true || !reflect.DeepEqual(gotConfig["ignored_models"], []any{}) {
		t.Fatalf("updated response config = %#v", gotConfig)
	}
	invalidConfig := map[string]any{"enabled": true, "stream_enabled": true, "responses_only_stream": true, "max_buffer_bytes": 16777216, "max_total_buffer_bytes": 67108864, "action": "invalid", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": []string{}, "accepted_models": map[string]any{}}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", invalidConfig, cookies, http.StatusBadRequest)
	mu.Lock()
	holdResponseConfig = true
	mu.Unlock()
	staleConfig := map[string]any{"enabled": false, "stream_enabled": true, "responses_only_stream": true, "max_buffer_bytes": 16777216, "max_total_buffer_bytes": 67108864, "action": "reject", "unknown_action": "pass", "case_sensitive": false, "ignore_thinking_suffix": true, "ignored_models": []string{}, "accepted_models": map[string]any{}}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", staleConfig, cookies, http.StatusServiceUnavailable)
	mu.Lock()
	holdResponseConfig = false
	mu.Unlock()
	for _, field := range []string{"groups", "keys"} {
		for _, entry := range []struct {
			name  string
			value any
		}{
			{"null", nil}, {"number", 42}, {"array", []any{}}, {"string", "invalid"},
		} {
			t.Run(field+" rejects "+entry.name+" entry", func(t *testing.T) {
				payload, err := json.Marshal(map[string]any{"policy_revision": 1, field: []any{entry.value}})
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPut, "/api/cpa-helper-plugin/policy", strings.NewReader(string(payload)))
				request.Header.Set("Content-Type", "application/json")
				for _, cookie := range cookies {
					request.AddCookie(cookie)
				}
				mu.Lock()
				writesBefore := policyWrites
				mu.Unlock()
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				var response struct {
					Detail struct {
						Code string `json:"code"`
					} `json:"detail"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if recorder.Code != http.StatusUnprocessableEntity || response.Detail.Code != "validation_error" {
					t.Fatalf("invalid entry response = %d %s, want 422 validation_error", recorder.Code, recorder.Body.String())
				}
				mu.Lock()
				defer mu.Unlock()
				if policyWrites != writesBefore {
					t.Fatalf("invalid entry reached plugin PUT: writes=%d, before=%d", policyWrites, writesBefore)
				}
			})
		}
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", map[string]any{"groups": []any{}, "keys": []any{}}, cookies, http.StatusConflict)
	requestJSON(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", map[string]any{
		"contract_version": "v1", "plugin_version": "0.1.4", "policy_revision": 1,
		"generated_at": "2026-09-30T00:00:00Z", "groups": gotPolicy["groups"], "keys": gotPolicy["keys"],
	}, cookies, &gotPolicy)
	if gotPolicy["policy_revision"] != float64(2) {
		t.Fatalf("updated policy revision = %#v, want 2", gotPolicy["policy_revision"])
	}
}
