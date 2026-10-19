package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

func TestAPIKeyExpirationLifecycle(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	remoteKeys := []string{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v0/management/") && r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case "PATCH /v0/management/api-keys":
			var payload struct {
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			remoteKeys = append(remoteKeys, payload.New)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "PUT /v0/management/api-keys":
			if err := json.NewDecoder(r.Body).Decode(&remoteKeys); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "GET /v1/models":
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if r.Header.Get("Authorization") != "Bearer "+key || !containsString(remoteKeys, key) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
		case "POST /v1/chat/completions":
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !containsString(remoteKeys, key) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
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
	admin := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "expiry-admin", "password": "expiry-admin-password", "nickname": "Admin"}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cpa.URL, "model_request_url": cpa.URL, "management_key": "management-key", "collector_enabled": false}, admin, nil)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "expiry-member", "password": "member-password", "nickname": "Member", "is_admin": false}, admin, nil)
	member := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "expiry-member", "password": "member-password"}, nil, nil)
	member = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, member, nil)

	var key backendApp.UserApiKeySummary
	requestedExpiration := time.Now().Add(5 * time.Second).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	expiresAt := requestedExpiration.UTC().Format(time.RFC3339Nano)
	wantExpiration := requestedExpiration.Truncate(time.Microsecond).In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "temporary", "expires_at": expiresAt}, admin, &key)
	if key.ExpiresAt == nil || *key.ExpiresAt != wantExpiration {
		t.Fatalf("created expiration = %v, want %s with UTC+8 offset and microsecond precision", key.ExpiresAt, wantExpiration)
	}
	if key.ExpirationEnforcedAt != nil {
		t.Fatal("future key is already marked enforced")
	}
	var permanentKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "permanent by default"}, admin, &permanentKey)
	if permanentKey.ExpiresAt != nil || permanentKey.ExpirationEnforcedAt != nil || permanentKey.ExpirationSyncError != nil {
		t.Fatalf("key created without expiration = %#v, want a permanent key", permanentKey)
	}
	if permanentKey.APIKey == nil {
		t.Fatal("permanent key response is missing its plaintext in owner scope")
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/api-keys/"+key.APIKeyHash, map[string]any{"description": "invalid", "expires_at": "yesterday"}, admin, http.StatusUnprocessableEntity)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/api-keys/"+key.APIKeyHash, map[string]any{"description": "past", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}, admin, http.StatusUnprocessableEntity)
	requestJSON(t, handler, http.MethodPut, "/api/api-keys/"+key.APIKeyHash, map[string]any{"description": "renamed"}, admin, &key)
	if key.ExpiresAt == nil || *key.ExpiresAt != wantExpiration {
		t.Fatalf("omitted expiration changed: %v, want %s", key.ExpiresAt, wantExpiration)
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/api-keys/"+key.APIKeyHash, map[string]any{"description": "foreign edit", "expires_at": nil}, member, http.StatusNotFound)

	deadline := time.Now().Add(8 * time.Second)
	for {
		var summaries []backendApp.UserApiKeySummary
		requestJSON(t, handler, http.MethodGet, "/api/api-keys", nil, admin, &summaries)
		for _, summary := range summaries {
			if summary.APIKeyHash == key.APIKeyHash && summary.ExpirationEnforcedAt != nil {
				key = summary
				break
			}
		}
		if key.ExpirationEnforcedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background expiration did not revoke the key")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if key.ExpirationSyncError != nil {
		t.Fatalf("expiration sync error: %s", *key.ExpirationSyncError)
	}
	if key.APIKey == nil {
		t.Fatal("key response is missing its plaintext in owner scope")
	}

	expiredRequest, err := http.NewRequest(http.MethodGet, cpa.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	expiredRequest.Header.Set("Authorization", "Bearer "+*key.APIKey)
	response, err := http.DefaultClient.Do(expiredRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("direct CPA model request status = %d, want 401", response.StatusCode)
	}
	permanentRequest, err := http.NewRequest(http.MethodGet, cpa.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	permanentRequest.Header.Set("Authorization", "Bearer "+*permanentKey.APIKey)
	response, err = http.DefaultClient.Do(permanentRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("permanent key CPA model request status = %d, want 200", response.StatusCode)
	}
	var listedKeys []backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodGet, "/api/api-keys", nil, admin, &listedKeys)
	for _, listed := range listedKeys {
		if listed.APIKeyHash == permanentKey.APIKeyHash {
			if listed.ExpiresAt != nil || listed.ExpirationEnforcedAt != nil || listed.ExpirationSyncError != nil {
				t.Fatalf("listed permanent key state = %#v", listed)
			}
			permanentKey = listed
			break
		}
	}
	if permanentKey.APIKeyHash == "" {
		t.Fatal("key created without expiration is missing from the owner list")
	}
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/account/model-request/test", map[string]any{"api_key_hash": key.APIKeyHash, "model": "test-model", "message": "ping", "endpoint": "chat_completions"}, admin, http.StatusConflict)
	var models struct {
		Queryable int `json:"queryable_api_key_count"`
		Errors    []struct {
			Hash    string `json:"api_key_hash"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/account/models", nil, admin, &models)
	if models.Queryable != 1 || len(models.Errors) != 1 || models.Errors[0].Hash != key.APIKeyHash {
		t.Fatalf("expired model response = %#v", models)
	}

	requestJSON(t, handler, http.MethodPut, "/api/api-keys/"+key.APIKeyHash, map[string]any{"description": "permanent", "expires_at": nil}, admin, &key)
	if key.ExpiresAt != nil || key.ExpirationEnforcedAt != nil {
		t.Fatalf("cleared expiration state = %#v", key)
	}
	mu.Lock()
	if !containsString(remoteKeys, *key.APIKey) {
		t.Fatal("clearing an enforced expiration did not restore the key in CPA")
	}
	mu.Unlock()
}

func mustParseExpiration(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
