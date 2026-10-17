package app_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

func TestCPAHelperPluginKeyLookupDoesNotDiscloseOtherAccounts(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	var mu sync.Mutex
	apiKeys := []string{}
	upstreamRequests := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		upstreamRequests++
		w.Header().Set("Content-Type", "application/json")
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
	var adminKey backendApp.UserApiKeySummary
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "admin-key"}, adminCookies, &adminKey)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, nil)
	memberCookies = requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, nil)
	endpoints := []struct {
		name   string
		method string
		prefix string
	}{
		{"read policy", http.MethodGet, "/api/cpa-helper-plugin/policy/key/"},
		{"update policy", http.MethodPut, "/api/cpa-helper-plugin/policy/key/"},
		{"read directory", http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="},
	}
	requestError := func(t *testing.T, method, path string) (int, map[string]any) {
		t.Helper()
		payload := ""
		if method == http.MethodPut {
			payload = `{"policy_revision":1,"key":{"id":"sha256:` + strings.Repeat("a", 64) + `","enabled":false}}`
		}
		request := httptest.NewRequest(method, path, strings.NewReader(payload))
		if payload != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for _, cookie := range memberCookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s response: %v", method, err)
		}
		t.Logf("%s status=%d body=%s", method, recorder.Code, recorder.Body.String())
		return recorder.Code, body
	}
	mu.Lock()
	requestsBefore := upstreamRequests
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if upstreamRequests != requestsBefore {
			t.Errorf("rejected key lookup reached CPA: requests=%d, before=%d", upstreamRequests, requestsBefore)
		}
	})
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			missingStatus, missingBody := requestError(t, endpoint.method, endpoint.prefix+strings.Repeat("0", 64))
			foreignStatus, foreignBody := requestError(t, endpoint.method, endpoint.prefix+adminKey.APIKeyHash)
			if missingStatus != http.StatusNotFound || foreignStatus != missingStatus {
				t.Errorf("missing/foreign status = %d/%d, want 404/404", missingStatus, foreignStatus)
			}
			want := map[string]any{"detail": map[string]any{"code": "not_found", "message": "API KEY 不存在"}}
			if !reflect.DeepEqual(missingBody, want) {
				t.Errorf("missing key error = %#v, want %#v", missingBody, want)
			}
			if !reflect.DeepEqual(foreignBody, missingBody) {
				t.Errorf("foreign key error = %#v, differs from missing key error %#v", foreignBody, missingBody)
			}
		})
	}
	t.Run("database errors remain internal failures", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(dataDir, "db", "cpa_helper.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`DROP TABLE user_api_keys`); err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range endpoints {
			t.Run(endpoint.name, func(t *testing.T) {
				status, body := requestError(t, endpoint.method, endpoint.prefix+adminKey.APIKeyHash)
				want := map[string]any{"detail": map[string]any{"code": "app_error", "message": "服务器内部错误"}}
				if status != http.StatusInternalServerError || !reflect.DeepEqual(body, want) {
					t.Errorf("database failure response = %d %#v, want 500 %#v", status, body, want)
				}
			})
		}
	})
}
