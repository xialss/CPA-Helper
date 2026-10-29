package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Captured fixed v1 requirements, independent from the Helper producer.
const ttlRequirementsFixture = `{"metadata_version":"v1","configuration_validation":"pinned_file_requires_reload","reserved_headers":["X-CPA-Helper-Claude-TTL","X-CPA-Helper-TTL-Request"],"payload":{"override":[{"models":[{"name":"*","protocol":"claude","headers":{"X-CPA-Helper-Claude-TTL":"1h"}}],"params":{"tools.#(cache_control.type==\"ephemeral\")#.cache_control.ttl":"1h","system.#(cache_control.type==\"ephemeral\")#.cache_control.ttl":"1h","messages.#(content)#.content.#(cache_control.type==\"ephemeral\")#.cache_control.ttl":"1h"}},{"models":[{"name":"*","protocol":"claude","headers":{"X-CPA-Helper-Claude-TTL":"1h"},"match":[{"cache_control.type":"ephemeral"}]}],"params":{"cache_control.ttl":"1h"}}],"filter":[{"models":[{"name":"*","protocol":"claude","headers":{"X-CPA-Helper-Claude-TTL":"5m"}}],"params":["tools.#(cache_control.type==\"ephemeral\")#.cache_control.ttl","system.#(cache_control.type==\"ephemeral\")#.cache_control.ttl","messages.#(content)#.content.#(cache_control.type==\"ephemeral\")#.cache_control.ttl"]},{"models":[{"name":"*","protocol":"claude","headers":{"X-CPA-Helper-Claude-TTL":"5m"},"match":[{"cache_control.type":"ephemeral"}]}],"params":["cache_control.ttl"]}]}}`

type pluginTTLHTTPFixture struct {
	mu           sync.Mutex
	caps         map[string]json.RawMessage
	health       map[string]json.RawMessage
	requirements string
	healthStatus int
	policy       map[string]json.RawMessage
	requests     []string
	ignoreTTL    bool
	writeCount   int
}

func newPluginTTLHTTPFixture(t *testing.T) (*pluginTTLHTTPFixture, AppConfig) {
	t.Helper()
	f := &pluginTTLHTTPFixture{
		caps: map[string]json.RawMessage{
			"plugin_version": mustRawString("0.1.10"), "cpa_version": mustRawString("v8.0.45"), "contract_version": mustRawString("v1"),
			"abi_version": mustRawNumber(1), "schema_version": mustRawNumber(7),
			"modules": mustRawStringArray([]string{"model_rules", "claude_cache_ttl"}), "policy_fields": mustRawStringArray([]string{"keys[].claude_cache_ttl"}),
			"claude_cache_ttl": mustRaw(map[string]any{"last_error": "D:/private/host-config.yaml"}),
		},
		health: map[string]json.RawMessage{
			"status": mustRawString("ok"), "policy_revision": mustRawNumber(7), "active": mustRaw(map[string]int{}),
			"claude_cache_ttl": mustRaw(map[string]any{"status": "configured", "ttl_source": "enforced_request", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload", "last_error": "D:/private/host-config.yaml"}),
		},
		requirements: ttlRequirementsFixture,
		policy: map[string]json.RawMessage{
			"contract_version": mustRawString("v1"), "plugin_version": mustRawString("0.1.10"), "policy_revision": mustRawNumber(7),
			"groups": mustRaw([]any{}), "keys": mustRaw([]any{}),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer ttl-management-secret" {
			t.Error("TTL management request lost configured authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch r.Method + " " + r.URL.Path {
		case "GET " + cpaHelperPluginManagementPrefix + "/capabilities":
			payload = f.caps
		case "GET " + cpaHelperPluginManagementPrefix + "/claude-ttl/requirements":
			_, _ = io.WriteString(w, f.requirements)
			return
		case "GET " + cpaHelperPluginManagementPrefix + "/health":
			f.health["policy_revision"] = f.policy["policy_revision"]
			if f.healthStatus != 0 {
				w.WriteHeader(f.healthStatus)
			}
			payload = f.health
		case "GET " + cpaHelperPluginManagementPrefix + "/policy":
			w.Header().Set("ETag", fmt.Sprintf("\"%d\"", rawUint(f.policy["policy_revision"])))
			payload = f.policy
		case "PUT " + cpaHelperPluginManagementPrefix + "/policy":
			f.writeCount++
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", rawUint(f.policy["policy_revision"])) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			var next map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.ignoreTTL {
				prior := pluginPolicyTTLs(f.policy)
				keys, _ := rawObjectArray(next, "keys")
				for _, key := range keys {
					delete(key, "claude_cache_ttl")
					if ttl := prior[rawString(key["id"])]; ttl != "" {
						key["claude_cache_ttl"] = mustRawString(ttl)
					}
				}
				next["keys"] = mustRaw(keys)
			}
			f.policy = next
			payload = map[string]any{"policy_revision": rawUint(next["policy_revision"])}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return f, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "ttl-management-secret"}}
}

func TestPluginClaudeTTLFeatureProbe(t *testing.T) {
	cases := []struct {
		name      string
		edit      func(*pluginTTLHTTPFixture)
		supported bool
		status    string
		fails     bool
	}{
		{"configured", nil, true, "configured", false},
		{"disabled still supports retained metadata", func(f *pluginTTLHTTPFixture) {
			f.health["claude_cache_ttl"] = mustRaw(map[string]any{"status": "disabled", "ttl_source": "enforced_request", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload"})
		}, true, "disabled", false},
		{"health 503 still supports retained metadata", func(f *pluginTTLHTTPFixture) { f.healthStatus = 503 }, true, "unavailable", false},
		{"old published 019 has no TTL", func(f *pluginTTLHTTPFixture) { f.caps["plugin_version"] = mustRawString("0.1.9") }, false, "unsupported", false},
		{"no feature", func(f *pluginTTLHTTPFixture) { delete(f.caps, "modules"); delete(f.caps, "policy_fields") }, false, "unsupported", false},
		{"missing policy field", func(f *pluginTTLHTTPFixture) { delete(f.caps, "policy_fields") }, false, "", true},
		{"null modules", func(f *pluginTTLHTTPFixture) { f.caps["modules"] = mustRaw(nil) }, false, "", true},
		{"wrong CPA target", func(f *pluginTTLHTTPFixture) { f.caps["cpa_version"] = mustRawString("v8.0.32") }, false, "", true},
		{"incomplete modern capabilities", func(f *pluginTTLHTTPFixture) { f.caps["plugin_version"] = mustRawString("0.1.11") }, false, "", true},
		{"unknown metadata protocol", func(f *pluginTTLHTTPFixture) {
			f.requirements = strings.Replace(f.requirements, `"metadata_version":"v1"`, `"metadata_version":"v2"`, 1)
		}, false, "", true},
		{"wrong root matching rule", func(f *pluginTTLHTTPFixture) {
			f.requirements = strings.Replace(f.requirements, `"cache_control.type":"ephemeral"`, `"cache_control.type":"other"`, 1)
		}, false, "", true},
		{"unknown health source", func(f *pluginTTLHTTPFixture) {
			f.health["claude_cache_ttl"] = mustRaw(map[string]any{"status": "configured", "ttl_source": "guessed_policy", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload"})
		}, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, cfg := newPluginTTLHTTPFixture(t)
			if tc.edit != nil {
				tc.edit(f)
			}
			feature, err := (&App{}).pluginClaudeTTLFeature(context.Background(), cfg)
			if (err != nil) != tc.fails {
				t.Fatalf("feature=%#v error=%v, want fails=%v", feature, err, tc.fails)
			}
			if !tc.fails && (feature.Supported != tc.supported || feature.Status != tc.status || (feature.Supported && feature.MetadataVersion != "v1")) {
				t.Fatalf("feature=%#v", feature)
			}
			if tc.status == "unsupported" {
				for _, request := range f.requests {
					if strings.Contains(request, "/claude-ttl/") || strings.Contains(request, "/health") {
						t.Fatal("unsupported feature called a removed/new API")
					}
				}
			}
		})
	}
}

func TestPluginClaudeTTLPolicyValidationAndMerge(t *testing.T) {
	for _, raw := range []string{`null`, `1`, `true`, `[]`, `{}`, `"5min"`, `" 1h"`} {
		key := map[string]json.RawMessage{"claude_cache_ttl": json.RawMessage(raw)}
		if normalizePluginPolicyKey(key) == nil {
			t.Fatalf("invalid TTL %s accepted", raw)
		}
	}
	current := map[string]json.RawMessage{"groups": mustRaw([]any{}), "keys": mustRaw([]any{map[string]any{"id": "scope", "claude_cache_ttl": "1h", "future": 42}})}
	for _, clear := range []bool{false, true} {
		next := map[string]json.RawMessage{"id": mustRawString("scope"), "label": mustRawString("changed")}
		if clear {
			next["claude_cache_ttl"] = mustRawString("")
		}
		merged, err := replacePluginKey(current, next)
		if err != nil || normalizePluginPolicy(merged) != nil {
			t.Fatal(err)
		}
		keys, _ := rawObjectArray(merged, "keys")
		if rawUint(keys[0]["future"]) != 42 || (rawString(keys[0]["claude_cache_ttl"]) == "1h") == clear {
			t.Fatalf("clear=%v lost fields or merge semantics: %s", clear, merged["keys"])
		}
		if clear {
			if _, exists := keys[0]["claude_cache_ttl"]; exists {
				t.Fatal("explicit clear retained a live field")
			}
		}
	}
	current["groups"] = mustRaw([]any{map[string]any{"id": "group", "claude_cache_ttl": "1h"}})
	if normalizePluginPolicy(current) == nil {
		t.Fatal("Group accepted a Key-only TTL")
	}
}

func TestPluginClaudeTTLKeyRouteAndSafeStatus(t *testing.T) {
	f, cfg := newPluginTTLHTTPFixture(t)
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.db.Close() })
	handler := a.Routes()
	admin := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, 200, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cfg.Collector.CLIProxyURL, "management_key": cfg.Collector.ManagementKey, "collector_enabled": false}, admin, 200, nil)
	var ownerID int
	if err := a.db.QueryRow("SELECT id FROM users WHERE username = 'admin'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := a.upsertUserAPIKey(context.Background(), ownerID, hashAPIKey("ttl-owner-key"), "ttl-owner-key", "TTL key"); err != nil {
		t.Fatal(err)
	}
	path := "/api/cpa-helper-plugin/policy/key/" + hashAPIKey("ttl-owner-key")
	var policy struct {
		Revision uint64                     `json:"policy_revision"`
		Key      map[string]json.RawMessage `json:"key"`
	}
	modelMonitorRequest(t, handler, http.MethodGet, path, nil, admin, 200, &policy)
	if _, exists := policy.Key["claude_cache_ttl"]; exists {
		t.Fatal("old Key default was replaced with 5m")
	}
	policy.Key["claude_cache_ttl"], policy.Key["future"] = mustRawString("1h"), mustRawNumber(42)
	put := func(want int) {
		t.Helper()
		modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, admin, want, nil)
		policy.Key = nil
		modelMonitorRequest(t, handler, http.MethodGet, path, nil, admin, 200, &policy)
	}
	put(200)
	if rawString(policy.Key["claude_cache_ttl"]) != "1h" || rawUint(policy.Key["future"]) != 42 {
		t.Fatal("TTL round trip lost intended fields")
	}
	f.mu.Lock()
	f.health["claude_cache_ttl"] = mustRaw(map[string]any{"status": "unavailable", "ttl_source": "enforced_request", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload"})
	f.mu.Unlock()
	policy.Key["label"] = mustRawString("unrelated change")
	put(200) // unchanged TTL must not require a healthy config probe
	policy.Key["claude_cache_ttl"] = mustRawString("5m")
	put(503)
	policy.Key["claude_cache_ttl"] = mustRawString("")
	put(200) // clear remains possible while the feature is unavailable
	if _, exists := policy.Key["claude_cache_ttl"]; exists {
		t.Fatal("clear returned success while TTL persisted")
	}
	f.mu.Lock()
	f.ignoreTTL = true
	f.health["claude_cache_ttl"] = mustRaw(map[string]any{"status": "configured", "ttl_source": "enforced_request", "metadata_retention_hours": 24, "configuration_validation": "pinned_file_requires_reload"})
	f.mu.Unlock()
	policy.Key["claude_cache_ttl"] = mustRawString("1h")
	put(503) // same revision accepted but the intended field was not applied
	f.mu.Lock()
	f.ignoreTTL = false
	f.mu.Unlock()
	before := f.writeCount
	policy.Key["claude_cache_ttl"] = json.RawMessage(`null`)
	modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, admin, 422, nil)
	modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision - 1, "key": policy.Key}, admin, 409, nil)
	if f.writeCount != before {
		t.Fatal("invalid/stale update reached Plugin PUT")
	}
	var status map[string]json.RawMessage
	modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/status", nil, admin, 200, &status)
	if strings.Contains(string(mustRaw(status)), "private/host-config") || len(status["claude_cache_ttl"]) == 0 || string(status["response_model_verification_supported"]) != "false" {
		t.Fatalf("unsafe/missing status projection: %s", mustRaw(status))
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, admin, 503, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", map[string]any{"enabled": true}, admin, 503, nil)
	for _, request := range f.requests {
		if strings.Contains(request, "response-model/validate") || strings.Contains(request, "config/plugins/configs/cpa-helper-plugin/response_model_mismatch") {
			t.Fatal("removed verifier was called")
		}
	}
	member := newAIRadarMemberCookies(t, handler, admin)
	beforeRequests := len(f.requests)
	modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, member, 404, nil)
	if len(f.requests) != beforeRequests {
		t.Fatal("another user's TTL update reached CPA")
	}
}

func TestPluginNewManagementReleasePairs(t *testing.T) {
	for _, version := range []string{"0.1.9", "0.1.10"} {
		for _, target := range []string{"v8.0.45", "v8.0.32", ""} {
			f, cfg := newPluginTTLHTTPFixture(t)
			f.caps["plugin_version"], f.caps["cpa_version"] = mustRawString(version), mustRawString(target)
			caps, err := (&App{}).cpaHelperPluginCapabilities(context.Background(), cfg)
			if target == "v8.0.45" {
				if err != nil {
					t.Fatal(err)
				}
				v8, err := pluginUsesV8Management(caps)
				if err != nil || !v8 || !pluginSupportsModelCatalog(caps) {
					t.Fatalf("known combination rejected: %v", err)
				}
			} else {
				var problem *AppError
				if !errors.As(err, &problem) || problem.Code != "plugin_incompatible" {
					t.Fatalf("mismatched combination accepted: %v", err)
				}
			}
		}
	}
}

func TestPluginManagementPolicyRejectsUnknownOrMismatchedRelease(t *testing.T) {
	f, cfg := newPluginTTLHTTPFixture(t)
	a, handler, admin := newCatalogRouteApp(t, cfg)
	var ownerID int
	if err := a.db.QueryRow("SELECT id FROM users WHERE username = 'catalog-admin'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	const key = "policy-compatibility-key"
	if err := a.upsertUserAPIKey(context.Background(), ownerID, hashAPIKey(key), key, "Compatibility key"); err != nil {
		t.Fatal(err)
	}
	path := "/api/cpa-helper-plugin/policy/key/" + hashAPIKey(key)
	var policy map[string]json.RawMessage
	modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, admin, 200, &policy)
	keyRequest := map[string]any{"policy_revision": rawUint(policy["policy_revision"]), "key": map[string]any{"id": pluginCallerScope(key), "enabled": false}}
	member := newAIRadarMemberCookies(t, handler, admin)
	for _, tc := range []struct {
		name, version, target string
		abi, schema           int
	}{
		{"incomplete modern capabilities", "0.1.11", "v8.0.45", 1, 7},
		{"new target mismatch", "0.1.10", "v8.0.32", 1, 7},
		{"old target mismatch", "0.1.8", "v8.0.45", 1, 7},
		{"unknown abi", "0.1.10", "v8.0.45", 2, 7},
		{"unknown schema", "0.1.9", "v8.0.45", 1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.caps["plugin_version"], f.caps["cpa_version"] = mustRawString(tc.version), mustRawString(tc.target)
			f.caps["abi_version"], f.caps["schema_version"] = mustRawNumber(tc.abi), mustRawNumber(tc.schema)
			before := len(f.requests)
			f.mu.Unlock()
			for _, endpoint := range []struct {
				method, path string
				body         any
			}{
				{http.MethodGet, "/api/cpa-helper-plugin/policy", nil},
				{http.MethodPut, "/api/cpa-helper-plugin/policy", policy},
				{http.MethodGet, path, nil},
				{http.MethodPut, path, keyRequest},
			} {
				var problem struct {
					Detail struct {
						Code string `json:"code"`
					} `json:"detail"`
				}
				modelMonitorRequest(t, handler, endpoint.method, endpoint.path, endpoint.body, admin, 503, &problem)
				if problem.Detail.Code != "plugin_incompatible" {
					t.Fatalf("unknown policy contract returned %q", problem.Detail.Code)
				}
			}
			f.mu.Lock()
			for _, request := range f.requests[before:] {
				if request != "GET "+cpaHelperPluginManagementPrefix+"/capabilities" {
					t.Errorf("unsupported version reached policy IO: %s", request)
				}
			}
			before = len(f.requests)
			f.mu.Unlock()
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, member, 404, nil)
			modelMonitorRequest(t, handler, http.MethodPut, path, keyRequest, member, 404, nil)
			f.mu.Lock()
			if len(f.requests) != before {
				t.Error("foreign Key check disclosed the external compatibility state")
			}
			f.mu.Unlock()
		})
	}
}
