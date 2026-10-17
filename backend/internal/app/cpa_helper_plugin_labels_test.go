package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

func TestCPAHelperPluginLabelsUseLocalDescriptions(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	apiKeys := []string{}
	policy := map[string]any{"contract_version": "v1", "plugin_version": "0.1.4", "policy_revision": float64(1), "groups": []any{}, "keys": []any{}, "unknown_policy_field": "keep"}
	pluginAvailable := true
	pluginReads, policyWrites := 0, 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer management-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/config":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "GET /v0/management/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": apiKeys})
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&apiKeys); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/policy":
			pluginReads++
			if !pluginAvailable {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("ETag", fmt.Sprintf(`"%.0f"`, policy["policy_revision"]))
			_ = json.NewEncoder(w).Encode(policy)
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/policy":
			if r.Header.Get("If-Match") != fmt.Sprintf(`"%.0f"`, policy["policy_revision"]) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			var next map[string]any
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			policyWrites++
			policy = next
			_ = json.NewEncoder(w).Encode(map[string]any{"policy_revision": policy["policy_revision"]})
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"policy_revision": policy["policy_revision"]})
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
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member"}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	memberCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil)
	var adminKey, memberKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "Admin description"}, adminCookies, &adminKey)
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "Member description"}, memberCookies, &memberKey)
	callerScope := func(key backendApp.UserApiKeySummary) string {
		t.Helper()
		if key.APIKey == nil {
			t.Fatal("complete key missing from fixture")
		}
		sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(*key.APIKey)))
		return hex.EncodeToString(sum[:])
	}
	adminScope, memberScope, externalScope := callerScope(adminKey), callerScope(memberKey), strings.Repeat("a", 64)
	external := map[string]any{"id": externalScope, "label": "External plugin label", "enabled": false, "group_ids": []any{}, "max_concurrency": float64(3), "rule": map[string]any{"denied_models": []any{"blocked"}}, "unknown_key_field": "keep"}
	mu.Lock()
	policy["groups"] = []any{map[string]any{"id": "public", "name": "Public", "rule": map[string]any{}, "note": "keep note"}}
	policy["keys"] = []any{
		map[string]any{"id": adminScope, "label": "Old admin plugin label", "rule": map[string]any{}},
		map[string]any{"id": memberScope, "label": "Old member plugin label", "group_ids": []any{"public"}, "rule": map[string]any{"models": []any{"allowed"}}, "unknown_key_field": "member keep"},
		external,
	}
	mu.Unlock()
	keyLabels := func(snapshot map[string]any) map[string]string {
		labels := map[string]string{}
		for _, raw := range snapshot["keys"].([]any) {
			key := raw.(map[string]any)
			labels[key["id"].(string)], _ = key["label"].(string)
		}
		return labels
	}
	var global, memberPolicy map[string]any
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, adminCookies, &global)
	wantLabels := map[string]string{adminScope: "Admin description", memberScope: "Member description", externalScope: "External plugin label"}
	if !reflect.DeepEqual(keyLabels(global), wantLabels) {
		t.Fatalf("projected global labels = %#v, want %#v", keyLabels(global), wantLabels)
	}
	metadata := global["helper_key_metadata"].(map[string]any)
	for scope, want := range map[string]struct {
		username, nickname, description string
		key                             *string
	}{
		adminScope:  {"admin", "Admin", "Admin description", adminKey.APIKey},
		memberScope: {"member", "Member", "Member description", memberKey.APIKey},
	} {
		owner := metadata[scope].(map[string]any)
		preview := (*want.key)[:6] + "..." + (*want.key)[len(*want.key)-4:]
		if owner["username"] != want.username || owner["nickname"] != want.nickname || owner["description"] != want.description || owner["key_preview"] != preview {
			t.Fatalf("key owner metadata = %#v", owner)
		}
	}
	if _, exists := metadata[externalScope]; exists {
		t.Fatal("external plugin key was assigned a Helper owner")
	}
	globalRaw, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{*adminKey.APIKey, *memberKey.APIKey, "management-key"} {
		if strings.Contains(string(globalRaw), secret) {
			t.Fatal("global policy metadata leaked a secret")
		}
	}
	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, memberCookies, http.StatusForbidden)
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy/key/"+memberKey.APIKeyHash, nil, memberCookies, &memberPolicy)
	if memberPolicy["key"].(map[string]any)["label"] != "Member description" {
		t.Fatalf("per-key read label = %#v", memberPolicy)
	}
	memberRaw, err := json.Marshal(memberPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := memberPolicy["helper_key_metadata"]; exists || strings.Contains(string(memberRaw), "Admin description") || strings.Contains(string(memberRaw), adminScope) {
		t.Fatal("member policy leaked another user's key or owner metadata")
	}
	mu.Lock()
	if policyWrites != 0 || keyLabels(policy)[memberScope] != "Old member plugin label" {
		t.Error("read-time label projection unexpectedly wrote remote policy")
	}
	pluginAvailable, pluginReads = false, 0
	mu.Unlock()
	requestJSON(t, handler, http.MethodPut, "/api/api-keys/"+memberKey.APIKeyHash, map[string]any{"description": "  Renamed member  "}, memberCookies, nil)
	mu.Lock()
	if pluginReads != 0 || policyWrites != 0 {
		t.Error("description editing contacted the unavailable plugin")
	}
	pluginAvailable = true
	mu.Unlock()
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, adminCookies, &global)
	if keyLabels(global)[memberScope] != "Renamed member" || global["policy_revision"] != float64(1) {
		t.Fatalf("description change is not reflected in route-rule labels: %#v", global)
	}
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy/key/"+memberKey.APIKeyHash, nil, memberCookies, &memberPolicy)
	memberPolicy["key"].(map[string]any)["label"] = "Independent client label"
	memberPolicy["key"].(map[string]any)["max_concurrency"] = 7
	requestJSON(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy/key/"+memberKey.APIKeyHash, memberPolicy, memberCookies, &memberPolicy)
	if memberPolicy["key"].(map[string]any)["label"] != "Renamed member" {
		t.Fatalf("per-key write accepted client naming: %#v", memberPolicy)
	}
	mu.Lock()
	if policyWrites != 1 || keyLabels(policy)[memberScope] != "Renamed member" || keyLabels(policy)[adminScope] != "Admin description" {
		t.Errorf("persisted labels were not canonical: %#v", policy)
	}
	for _, raw := range policy["keys"].([]any) {
		key := raw.(map[string]any)
		if key["id"] == externalScope && !reflect.DeepEqual(key, external) {
			t.Errorf("external key changed: %#v, want %#v", key, external)
		}
		if key["id"] == memberScope && (key["unknown_key_field"] != "member keep" || !reflect.DeepEqual(key["group_ids"], []any{"public"}) || key["max_concurrency"] != float64(7)) {
			t.Errorf("per-key edit lost existing policy fields: %#v", key)
		}
	}
	mu.Unlock()
	requestJSON(t, handler, http.MethodPut, "/api/api-keys/"+memberKey.APIKeyHash, map[string]any{"description": "Latest member name"}, memberCookies, nil)
	requestJSON(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, adminCookies, &global)
	for _, raw := range global["keys"].([]any) {
		key := raw.(map[string]any)
		if key["id"] == adminScope || key["id"] == memberScope {
			key["label"] = "Forged global label"
		}
	}
	requestJSON(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", global, adminCookies, &global)
	mu.Lock()
	if _, exists := policy["helper_key_metadata"]; exists || policy["unknown_policy_field"] != "keep" {
		t.Errorf("global write leaked Helper metadata or dropped unknown policy fields: %#v", policy)
	}
	if _, exists := global["helper_key_metadata"]; !exists {
		t.Error("global save response lost display metadata")
	}
	if keyLabels(policy)[memberScope] != "Latest member name" || keyLabels(policy)[adminScope] != "Admin description" || keyLabels(policy)[externalScope] != "External plugin label" {
		t.Errorf("global write allowed separate local labels: %#v", policy)
	}
	mu.Unlock()
	remaining := []any{}
	for _, raw := range global["keys"].([]any) {
		if raw.(map[string]any)["id"] != memberScope {
			remaining = append(remaining, raw)
		}
	}
	global["keys"] = remaining
	requestJSON(t, handler, http.MethodPut, "/api/cpa-helper-plugin/policy", global, adminCookies, nil)
	mu.Lock()
	defer mu.Unlock()
	if _, exists := keyLabels(policy)[memberScope]; exists {
		t.Fatal("label canonicalization restored a deliberately removed policy entry")
	}
}
