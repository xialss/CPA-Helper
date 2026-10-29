package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type pluginTTLHTTPFixture struct {
	mu           sync.Mutex
	caps         map[string]json.RawMessage
	health       map[string]json.RawMessage
	healthStatus int
	policy       map[string]json.RawMessage
	requests     []string
	writeCount   int
}

// The legacy name keeps the shared forward-compatibility fixtures stable.
// Retired feature endpoints are traps, never a prerequisite to management.
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
			"claude_cache_ttl": mustRaw(map[string]any{"last_error": "D:/private/host-config.yaml"}),
		},
		policy: map[string]json.RawMessage{
			"contract_version": mustRawString("v1"), "plugin_version": mustRawString("0.1.10"), "policy_revision": mustRawNumber(7),
			"groups": mustRaw([]any{}), "keys": mustRaw([]any{}),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer management-secret" {
			t.Error("management request lost configured authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch r.Method + " " + r.URL.Path {
		case "GET " + cpaHelperPluginManagementPrefix + "/capabilities":
			payload = f.caps
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
			f.policy = next
			payload = map[string]any{"policy_revision": rawUint(next["policy_revision"])}
		default:
			if r.URL.Path == cpaHelperPluginManagementPrefix+"/claude-ttl/requirements" || r.URL.Path == cpaHelperPluginManagementPrefix+"/claude-ttl/executions" {
				t.Error("retired TTL endpoint was requested")
			}
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return f, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "management-secret"}}
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
			f.caps["plugin_version"], f.caps["cpa_version"] = mustRawString(tc.version), mustRawString(tc.target)
			f.caps["abi_version"], f.caps["schema_version"] = mustRawNumber(tc.abi), mustRawNumber(tc.schema)
			before := len(f.requests)
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
			for _, request := range f.requests[before:] {
				if request != "GET "+cpaHelperPluginManagementPrefix+"/capabilities" {
					t.Errorf("unsupported version reached policy IO: %s", request)
				}
			}
			before = len(f.requests)
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, member, 404, nil)
			modelMonitorRequest(t, handler, http.MethodPut, path, keyRequest, member, 404, nil)
			if len(f.requests) != before {
				t.Error("foreign Key check disclosed the external compatibility state")
			}
		})
	}
}
