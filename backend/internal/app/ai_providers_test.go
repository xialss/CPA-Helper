package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

func TestAIProvidersSnapshotReusesOneManagementConfig(t *testing.T) {
	providerPaths := map[string]bool{
		"/v0/management/gemini-api-key":       true,
		"/v0/management/codex-api-key":        true,
		"/v0/management/claude-api-key":       true,
		"/v0/management/openai-compatibility": true,
		"/v0/management/vertex-api-key":       true,
	}
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	var releaseProviderOnce sync.Once
	releaseProviderRequest := func() {
		releaseProviderOnce.Do(func() { close(releaseProvider) })
	}
	var blockProvider sync.Once
	var oldUsageCalls atomic.Int32
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if providerPaths[r.URL.Path] && r.Method == http.MethodGet {
			blockProvider.Do(func() {
				close(providerStarted)
				<-releaseProvider
			})
			if r.URL.Path == "/v0/management/gemini-api-key" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"auth-index": "old-auth",
					"models":     []map[string]any{{"name": "old-model"}},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet {
			oldUsageCalls.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer oldServer.Close()

	var newUsageCalls atomic.Int32
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if providerPaths[r.URL.Path] && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet {
			newUsageCalls.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer newServer.Close()
	defer releaseProviderRequest()

	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := backendApp.NewWithOptions(context.Background(), backendApp.NewOptions{
		Migrate:         true,
		StartBackground: false,
	})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()
	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     oldServer.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	type responseResult struct {
		status int
		body   []byte
	}
	resultCh := make(chan responseResult, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/api/ai-providers", nil)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resultCh <- responseResult{status: recorder.Code, body: append([]byte(nil), recorder.Body.Bytes()...)}
	}()
	select {
	case <-providerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("provider snapshot did not start")
	}

	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     newServer.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)
	releaseProviderRequest()
	var result responseResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("AI provider request did not finish")
	}
	if result.status != http.StatusOK {
		t.Fatalf("GET /api/ai-providers returned %d: %s", result.status, string(result.body))
	}
	if oldUsageCalls.Load() != 1 || newUsageCalls.Load() != 0 {
		t.Fatalf("usage requests old/new = %d/%d, want 1/0", oldUsageCalls.Load(), newUsageCalls.Load())
	}
}

type aiProvidersTestResponse struct {
	Providers []struct {
		Brand                 string   `json:"brand"`
		Index                 int      `json:"index"`
		IdentityHash          string   `json:"identity_hash"`
		APIKeyHash            string   `json:"api_key_hash"`
		APIKeyMasked          string   `json:"api_key_masked"`
		Disabled              *bool    `json:"disabled"`
		ExcludedModels        []string `json:"excluded_models"`
		RecentSuccess         int      `json:"recent_success"`
		RecentFailure         int      `json:"recent_failure"`
		RecentStatus          string   `json:"recent_status"`
		RecentStatusAvailable bool     `json:"recent_status_available"`
		RecentRequests        []struct {
			Time    string `json:"time"`
			Success int    `json:"success"`
			Failed  int    `json:"failed"`
		} `json:"recent_requests"`
	} `json:"providers"`
	Summary struct {
		Total         int `json:"total"`
		RecentSuccess int `json:"recent_success"`
		RecentFailure int `json:"recent_failure"`
	} `json:"summary"`
}

type aiProviderActionTestResponse struct {
	OK         bool `json:"ok"`
	StatusCode int  `json:"status_code"`
	Models     []struct {
		Name string `json:"name"`
	} `json:"models"`
	Reply string `json:"reply"`
}

type fakeAIProviderManagement struct {
	t             *testing.T
	mu            sync.Mutex
	config        map[string]any
	usage         any
	apiCallBodies []map[string]any
}

func newFakeAIProviderManagement(t *testing.T) (*fakeAIProviderManagement, *httptest.Server) {
	t.Helper()
	fake := &fakeAIProviderManagement{
		t: t,
		config: map[string]any{
			"gemini-api-key":       []map[string]any{},
			"codex-api-key":        []map[string]any{},
			"claude-api-key":       []map[string]any{},
			"openai-compatibility": []map[string]any{},
			"vertex-api-key":       []map[string]any{},
		},
		usage: []map[string]any{},
	}
	server := httptest.NewServer(http.HandlerFunc(fake.handle))
	return fake, server
}

func (f *fakeAIProviderManagement) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-management-key" {
		f.t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
	}
	w.Header().Set("Content-Type", "application/json")
	pathToKey := map[string]string{
		"/v0/management/gemini-api-key":       "gemini-api-key",
		"/v0/management/codex-api-key":        "codex-api-key",
		"/v0/management/claude-api-key":       "claude-api-key",
		"/v0/management/openai-compatibility": "openai-compatibility",
		"/v0/management/vertex-api-key":       "vertex-api-key",
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(f.config)
		return
	}
	if r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(f.usage)
		return
	}
	if r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			f.t.Fatalf("decode api-call payload: %v", err)
		}
		f.apiCallBodies = append(f.apiCallBodies, payload)
		targetURL, _ := payload["url"].(string)
		if strings.Contains(targetURL, "aiplatform.googleapis.com") && strings.Contains(targetURL, "/models") && strings.EqualFold(payload["method"].(string), http.MethodGet) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body":        `{"publisherModels":[{"name":"publishers/google/models/gemini-2.5-pro"}]}`,
			})
			return
		}
		if strings.Contains(targetURL, "/models") && strings.EqualFold(payload["method"].(string), http.MethodGet) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body":        `{"data":[{"id":"gpt-test"}]}`,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code": 200,
			"body":        `{"choices":[{"message":{"content":"ok"}}]}`,
		})
		return
	}
	if key, ok := pathToKey[r.URL.Path]; ok {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.config[key])
			return
		case http.MethodPut:
			var next []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				f.t.Fatalf("decode provider PUT body: %v", err)
			}
			f.config[key] = next
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}
	http.NotFound(w, r)
}

func TestAIProvidersRequireConfiguredManagementSettings(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)

	body, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/ai-providers", bytes.NewReader(body))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("GET /api/ai-providers returned %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "系统设置") || !strings.Contains(recorder.Body.String(), "管理密钥") {
		t.Fatalf("missing settings CTA guidance: %s", recorder.Body.String())
	}
}

func TestAIProvidersAreAdminOnly(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{}

	handler, adminCookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "member",
		"password": "member-password",
		"nickname": "Member",
		"is_admin": false,
	}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "member",
		"password": "member-password",
	}, nil, nil)

	requestJSONExpectStatus(t, handler, http.MethodGet, "/api/ai-providers", nil, memberCookies, http.StatusForbidden)
}

func TestAIProvidersSnapshotMasksSecretsAndMapsUsage(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":    "gemini-secret-key",
			"base-url":   "https://gemini.example",
			"auth-index": "auth-gemini-0",
			"models":     []map[string]any{{"name": "gemini-2.5-pro", "alias": "gemini-pro"}},
		},
	}
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name": "custom-openai",
			"api-key-entries": []map[string]any{
				{"api-key": "openai-secret-key", "proxy-url": "http://proxy.local"},
			},
		},
	}
	fake.usage = []map[string]any{
		{"provider": "gemini", "api_key": "gemini-secret-key", "success_count": 2, "failure_count": 1},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if response.Summary.Total != 2 {
		t.Fatalf("summary total = %d, want 2", response.Summary.Total)
	}
	if response.Summary.RecentSuccess != 2 || response.Summary.RecentFailure != 1 {
		t.Fatalf("summary usage = %d/%d, want 2/1", response.Summary.RecentSuccess, response.Summary.RecentFailure)
	}
	text := string(responseBody)
	for _, secret := range []string{"gemini-secret-key", "openai-secret-key", "test-management-key"} {
		if strings.Contains(text, secret) {
			t.Fatalf("response leaked secret %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "gemini") || !strings.Contains(text, "custom-openai") {
		t.Fatalf("response missing provider identity: %s", text)
	}
}

func TestAIProvidersSnapshotMapsNestedUsageBuckets(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":  "gemini-secret-key",
			"base-url": "https://gemini.example",
		},
	}
	fake.usage = map[string]any{
		"gemini": map[string]any{
			"https://gemini.example|gemini-secret-key": map[string]any{
				"success": float64(3),
				"failed":  float64(2),
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	if response.Providers[0].RecentSuccess != 3 || response.Providers[0].RecentFailure != 2 {
		t.Fatalf("provider usage = %d/%d, want 3/2", response.Providers[0].RecentSuccess, response.Providers[0].RecentFailure)
	}
	if response.Summary.RecentSuccess != 3 || response.Summary.RecentFailure != 2 {
		t.Fatalf("summary usage = %d/%d, want 3/2", response.Summary.RecentSuccess, response.Summary.RecentFailure)
	}
	if strings.Contains(string(responseBody), "gemini-secret-key") {
		t.Fatalf("response leaked nested usage secret: %s", string(responseBody))
	}
}

func TestAIProvidersSnapshotDoesNotDuplicateSharedKeyUsageAcrossBaseURLs(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":  "shared-secret-key",
			"base-url": "https://a.example",
		},
		{
			"api-key":  "shared-secret-key",
			"base-url": "https://b.example",
		},
	}
	fake.usage = map[string]any{
		"gemini": map[string]any{
			"https://a.example|shared-secret-key": map[string]any{
				"success": float64(2),
			},
			"https://b.example|shared-secret-key": map[string]any{
				"failed": float64(1),
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 2 {
		t.Fatalf("providers length = %d, want 2", len(response.Providers))
	}
	if response.Providers[0].RecentSuccess != 2 || response.Providers[0].RecentFailure != 0 {
		t.Fatalf("first provider usage = %d/%d, want 2/0", response.Providers[0].RecentSuccess, response.Providers[0].RecentFailure)
	}
	if response.Providers[1].RecentSuccess != 0 || response.Providers[1].RecentFailure != 1 {
		t.Fatalf("second provider usage = %d/%d, want 0/1", response.Providers[1].RecentSuccess, response.Providers[1].RecentFailure)
	}
	if response.Summary.RecentSuccess != 2 || response.Summary.RecentFailure != 1 {
		t.Fatalf("summary usage = %d/%d, want 2/1", response.Summary.RecentSuccess, response.Summary.RecentFailure)
	}
}

func TestAIProvidersSnapshotMapsRecentRequestBuckets(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":  "gemini-secret-key",
			"base-url": "https://gemini.example",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider": "gemini",
			"api_key":  "gemini-secret-key",
			"recent_requests": []map[string]any{
				{"time": "2026-07-05T10:00:00+08:00", "success": 2, "failed": 0},
				{"time": "2026-07-05T10:10:00+08:00", "success": 1, "failed": 1},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "failing" {
		t.Fatalf("recent status = %q available %v, want failing available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 3 || provider.RecentFailure != 1 {
		t.Fatalf("provider usage = %d/%d, want 3/1", provider.RecentSuccess, provider.RecentFailure)
	}
	if len(provider.RecentRequests) != 2 || provider.RecentRequests[1].Success != 1 || provider.RecentRequests[1].Failed != 1 {
		t.Fatalf("recent requests = %#v, want two parsed buckets", provider.RecentRequests)
	}
	if strings.Contains(string(responseBody), "gemini-secret-key") {
		t.Fatalf("response leaked usage secret: %s", string(responseBody))
	}
}

func TestAIProvidersSnapshotKeepsHighSuccessRateRecentUsageHealthy(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":  "gemini-secret-key",
			"base-url": "https://gemini.example",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider": "gemini",
			"api_key":  "gemini-secret-key",
			"recent_requests": []map[string]any{
				{"time": "2026-07-05T10:00:00+08:00", "success": 1974, "failed": 3},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "healthy" {
		t.Fatalf("recent status = %q available %v, want healthy available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 1974 || provider.RecentFailure != 3 {
		t.Fatalf("provider usage = %d/%d, want 1974/3", provider.RecentSuccess, provider.RecentFailure)
	}
}

func TestAIProvidersSnapshotDerivesCountsFromRecentRequestBuckets(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider":    "gemini",
			"api_key":     "gemini-secret-key",
			"total_count": 5,
			"recent_requests": []map[string]any{
				{"time": "2026-07-05T10:00:00+08:00", "success": 3, "failed": 0},
				{"time": "2026-07-05T10:10:00+08:00", "success": 0, "failed": 2},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "failing" {
		t.Fatalf("recent status = %q available %v, want failing available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 3 || provider.RecentFailure != 2 {
		t.Fatalf("provider usage = %d/%d, want derived 3/2", provider.RecentSuccess, provider.RecentFailure)
	}
}

func TestAIProvidersSnapshotMergesMixedTimedBucketsDeterministically(t *testing.T) {
	cases := []struct {
		name     string
		incoming []map[string]any
	}{
		{
			name: "untimed first",
			incoming: []map[string]any{
				{"success": 1, "failed": 0},
				{"time": "2026-07-05T10:10:00+08:00", "success": 1, "failed": 0},
			},
		},
		{
			name: "timed first",
			incoming: []map[string]any{
				{"time": "2026-07-05T10:10:00+08:00", "success": 1, "failed": 0},
				{"success": 1, "failed": 0},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			fake.config["gemini-api-key"] = []map[string]any{
				{
					"api-key": "gemini-secret-key",
				},
			}
			fake.usage = []map[string]any{
				{
					"provider": "gemini",
					"api_key":  "gemini-secret-key",
					"recent_requests": []map[string]any{
						{"success": 1, "failed": 0},
					},
				},
				{
					"provider":        "gemini",
					"api_key":         "gemini-secret-key",
					"recent_requests": tc.incoming,
				},
			}

			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
			response := aiProvidersTestResponse{}
			if err := json.Unmarshal(responseBody, &response); err != nil {
				t.Fatalf("decode ai providers response: %v", err)
			}
			if len(response.Providers) != 1 {
				t.Fatalf("providers length = %d, want 1", len(response.Providers))
			}
			provider := response.Providers[0]
			if provider.RecentStatusAvailable || provider.RecentStatus != "unavailable" {
				t.Fatalf("recent status = %q available %v, want unavailable", provider.RecentStatus, provider.RecentStatusAvailable)
			}
			if provider.RecentSuccess != 3 || provider.RecentFailure != 0 {
				t.Fatalf("provider usage = %d/%d, want merged 3/0", provider.RecentSuccess, provider.RecentFailure)
			}
			if len(provider.RecentRequests) != 3 {
				t.Fatalf("recent requests length = %d, want deterministic 3 buckets: %#v", len(provider.RecentRequests), provider.RecentRequests)
			}
		})
	}
}

func TestAIProvidersSnapshotNormalizesSingleUsagePaddedRecentRequestsBeforeTrimming(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	recentRequests := make([]map[string]any, 0, 46)
	for index := 20; index >= 0; index-- {
		recentRequests = append(recentRequests, map[string]any{
			"time":    fmt.Sprintf("2026-07-05T%02d:%02d:00+08:00", 10+index/6, (index%6)*10),
			"success": 1,
			"failed":  0,
		})
	}
	for index := 0; index < 25; index++ {
		recentRequests = append(recentRequests, map[string]any{"success": 0, "failed": 0})
	}
	fake.usage = []map[string]any{
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"recent_requests": recentRequests,
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "healthy" {
		t.Fatalf("recent status = %q available %v, want healthy available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 20 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want newest timed bucket counts 20/0", provider.RecentSuccess, provider.RecentFailure)
	}
	if len(provider.RecentRequests) != 20 {
		t.Fatalf("recent requests length = %d, want newest 20 timed buckets: %#v", len(provider.RecentRequests), provider.RecentRequests)
	}
	for index, bucket := range provider.RecentRequests {
		expectedTime := fmt.Sprintf("2026-07-05T%02d:%02d:00+08:00", 10+(index+1)/6, ((index+1)%6)*10)
		if bucket.Time != expectedTime {
			t.Fatalf("recent request %d time = %q, want %q in sorted newest window: %#v", index, bucket.Time, expectedTime, provider.RecentRequests)
		}
		if bucket.Success != 1 || bucket.Failed != 0 {
			t.Fatalf("recent request %d = %#v, want timed success bucket", index, bucket)
		}
	}
}

func TestAIProvidersSnapshotTrimsClockBucketsAcrossMidnight(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	recentRequests := make([]map[string]any, 0, 25)
	for index := 0; index < 25; index++ {
		start := (20*60 + 40 + index*10) % (24 * 60)
		end := (start + 10) % (24 * 60)
		recentRequests = append(recentRequests, map[string]any{
			"time":    fmt.Sprintf("%02d:%02d-%02d:%02d", start/60, start%60, end/60, end%60),
			"success": 1,
			"failed":  0,
		})
	}
	fake.usage = []map[string]any{
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"recent_requests": recentRequests,
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "healthy" {
		t.Fatalf("recent status = %q available %v, want healthy available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 20 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want newest clock bucket counts 20/0", provider.RecentSuccess, provider.RecentFailure)
	}
	if len(provider.RecentRequests) != 20 {
		t.Fatalf("recent requests length = %d, want newest 20 clock buckets: %#v", len(provider.RecentRequests), provider.RecentRequests)
	}
	for index, bucket := range provider.RecentRequests {
		start := (21*60 + 30 + index*10) % (24 * 60)
		end := (start + 10) % (24 * 60)
		expectedTime := fmt.Sprintf("%02d:%02d-%02d:%02d", start/60, start%60, end/60, end%60)
		if bucket.Time != expectedTime {
			t.Fatalf("recent request %d time = %q, want %q in clock-sorted newest window: %#v", index, bucket.Time, expectedTime, provider.RecentRequests)
		}
		if bucket.Success != 1 || bucket.Failed != 0 {
			t.Fatalf("recent request %d = %#v, want clock success bucket", index, bucket)
		}
	}
}

func TestAIProvidersSnapshotKeepsTimedBucketsWhenMergingUntimedZeroBuckets(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	timedBuckets := make([]map[string]any, 0, 20)
	for index := 0; index < 20; index++ {
		timedBuckets = append(timedBuckets, map[string]any{
			"time":    fmt.Sprintf("2026-07-05T%02d:%02d:00+08:00", 10+index/6, (index%6)*10),
			"success": 1,
			"failed":  0,
		})
	}
	untimedZeroBuckets := make([]map[string]any, 0, 25)
	for index := 0; index < 25; index++ {
		untimedZeroBuckets = append(untimedZeroBuckets, map[string]any{"success": 0, "failed": 0})
	}
	fake.usage = []map[string]any{
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"recent_requests": timedBuckets,
		},
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"recent_requests": untimedZeroBuckets,
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "healthy" {
		t.Fatalf("recent status = %q available %v, want healthy available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 20 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want timed bucket counts 20/0", provider.RecentSuccess, provider.RecentFailure)
	}
	if len(provider.RecentRequests) != 20 {
		t.Fatalf("recent requests length = %d, want all 20 timed buckets: %#v", len(provider.RecentRequests), provider.RecentRequests)
	}
	for index, bucket := range provider.RecentRequests {
		if strings.TrimSpace(bucket.Time) == "" {
			t.Fatalf("recent request %d has blank time after merge: %#v", index, provider.RecentRequests)
		}
		if bucket.Success != 1 || bucket.Failed != 0 {
			t.Fatalf("recent request %d = %#v, want untouched timed bucket", index, bucket)
		}
	}
}

func TestAIProvidersSnapshotRejectsUsageWithMismatchedAPIKeyHash(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":    "gemini-secret-key",
			"auth-index": "shared-auth",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider":      "gemini",
			"api_key":       "other-secret-key",
			"auth_index":    "shared-auth",
			"success_count": 3,
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if provider.RecentSuccess != 0 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want mismatched hash usage ignored", provider.RecentSuccess, provider.RecentFailure)
	}
	if provider.RecentStatus != "unknown" || !provider.RecentStatusAvailable {
		t.Fatalf("recent status = %q available %v, want unknown available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
}

func TestAIProvidersSnapshotMarksPositiveUsageWithoutBucketsUnavailable(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"success_count":   2,
			"recent_requests": []map[string]any{},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if provider.RecentStatusAvailable || provider.RecentStatus != "unavailable" {
		t.Fatalf("recent status = %q available %v, want unavailable", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 2 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want 2/0", provider.RecentSuccess, provider.RecentFailure)
	}
}

func TestAIProvidersSnapshotKeepsZeroUsageEmptyBucketsAvailable(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider":        "gemini",
			"api_key":         "gemini-secret-key",
			"recent_requests": []map[string]any{},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if !provider.RecentStatusAvailable || provider.RecentStatus != "unknown" {
		t.Fatalf("recent status = %q available %v, want unknown available", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 0 || provider.RecentFailure != 0 || len(provider.RecentRequests) != 0 {
		t.Fatalf("provider usage = %d/%d requests %#v, want zero usage with empty buckets", provider.RecentSuccess, provider.RecentFailure, provider.RecentRequests)
	}
}

func TestAIProvidersSnapshotMarksPositiveUsageWithoutTimedBucketsUnavailable(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	fake.usage = []map[string]any{
		{
			"provider": "gemini",
			"api_key":  "gemini-secret-key",
			"recent_requests": []map[string]any{
				{"success": 2, "failed": 0},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if provider.RecentStatusAvailable || provider.RecentStatus != "unavailable" {
		t.Fatalf("recent status = %q available %v, want unavailable", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if provider.RecentSuccess != 2 || provider.RecentFailure != 0 {
		t.Fatalf("provider usage = %d/%d, want 2/0", provider.RecentSuccess, provider.RecentFailure)
	}
}

func TestAIProvidersSnapshotMarksUnsupportedUsageUnavailable(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
		},
	}
	fake.usage = map[string]any{"unexpected": "shape"}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	response := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode ai providers response: %v", err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(response.Providers))
	}
	provider := response.Providers[0]
	if provider.RecentStatusAvailable || provider.RecentStatus != "unavailable" {
		t.Fatalf("recent status = %q available %v, want unavailable", provider.RecentStatus, provider.RecentStatusAvailable)
	}
	if !strings.Contains(string(responseBody), "api-key-usage") {
		t.Fatalf("response missing usage warning: %s", string(responseBody))
	}
}

func TestAIProviderExcludedModelsDisableRuleNormalizesAndWritesBack(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":         "gemini-secret-key",
			"disabled":        true,
			"excluded-models": []string{"gemini-old", "*"},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	provider := snapshot.Providers[0]
	if provider.Disabled == nil || !*provider.Disabled {
		t.Fatalf("disabled = %#v, want true from excluded-models wildcard", provider.Disabled)
	}
	if len(provider.ExcludedModels) != 1 || provider.ExcludedModels[0] != "gemini-old" {
		t.Fatalf("excluded models = %#v, want wildcard filtered", provider.ExcludedModels)
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":           "gemini",
		"identity_hash":   provider.IdentityHash,
		"api_key_hash":    provider.APIKeyHash,
		"api_key":         "",
		"disabled":        false,
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
		"excluded_models": []string{"gemini-old"},
	}, cookies, nil)

	fake.mu.Lock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	excluded, ok := items[0]["excluded-models"].([]any)
	fake.mu.Unlock()
	if !ok || len(excluded) != 1 || excluded[0] != "gemini-old" {
		t.Fatalf("enabled excluded-models = %#v, want wildcard removed", excluded)
	}
	if _, ok := items[0]["disabled"]; ok {
		t.Fatalf("enabled provider kept stale disabled field: %#v", items[0])
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":           "gemini",
		"identity_hash":   provider.IdentityHash,
		"api_key_hash":    provider.APIKeyHash,
		"api_key":         "",
		"disabled":        true,
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
		"excluded_models": []string{"gemini-old"},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items = fake.config["gemini-api-key"].([]map[string]any)
	excluded, ok = items[0]["excluded-models"].([]any)
	if !ok || len(excluded) != 2 || excluded[0] != "gemini-old" || excluded[1] != "*" {
		t.Fatalf("disabled excluded-models = %#v, want wildcard appended", excluded)
	}
	if _, ok := items[0]["disabled"]; ok {
		t.Fatalf("disabled non-OpenAI provider kept stale disabled field: %#v", items[0])
	}
}

func TestAIProviderOpenAICompatibilityPreservesExcludedModelsWildcard(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":            "custom-openai",
			"base-url":        "https://openai.example",
			"excluded-models": []string{"*", "gpt-old"},
			"api-key-entries": []map[string]any{},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	provider := snapshot.Providers[0]
	if provider.Disabled != nil && *provider.Disabled {
		t.Fatalf("disabled = %#v, want OpenAI-compatible disabled independent of wildcard", provider.Disabled)
	}
	if len(provider.ExcludedModels) != 2 || provider.ExcludedModels[0] != "*" || provider.ExcludedModels[1] != "gpt-old" {
		t.Fatalf("excluded models = %#v, want wildcard preserved for OpenAI-compatible", provider.ExcludedModels)
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":           "openai_compatibility",
		"identity_hash":   provider.IdentityHash,
		"name":            "custom-openai",
		"disabled":        false,
		"base_url":        "https://openai.example",
		"api_key_entries": []map[string]any{},
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
		"excluded_models": []string{"*", "gpt-old"},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["openai-compatibility"].([]map[string]any)
	excluded, ok := items[0]["excluded-models"].([]any)
	if !ok || len(excluded) != 2 || excluded[0] != "*" || excluded[1] != "gpt-old" {
		t.Fatalf("saved excluded-models = %#v, want wildcard preserved", items[0]["excluded-models"])
	}
}

func TestAIProviderLegacyDisabledFieldMigratesToExcludedModelsWildcard(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":         "gemini-secret-key",
			"disabled":        true,
			"excluded-models": []string{"gemini-old"},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	provider := snapshot.Providers[0]
	if provider.Disabled == nil || !*provider.Disabled {
		t.Fatalf("disabled = %#v, want true from legacy disabled field", provider.Disabled)
	}
	if len(provider.ExcludedModels) != 1 || provider.ExcludedModels[0] != "gemini-old" {
		t.Fatalf("excluded models = %#v, want existing exclusion preserved", provider.ExcludedModels)
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": provider.IdentityHash,
		"api_key_hash":  provider.APIKeyHash,
		"api_key":       "",
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if _, ok := items[0]["disabled"]; ok {
		t.Fatalf("migrated provider kept stale disabled field: %#v", items[0])
	}
	excluded, ok := items[0]["excluded-models"].([]any)
	if !ok || len(excluded) != 2 || excluded[0] != "gemini-old" || excluded[1] != "*" {
		t.Fatalf("migrated excluded-models = %#v, want legacy disabled converted to wildcard", items[0]["excluded-models"])
	}
}

func TestAIProviderUpdateOmittedExcludedModelsPreservesRemoteExclusions(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":         "gemini-secret-key",
			"excluded-models": []string{"gemini-old", "gemini-legacy"},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	provider := snapshot.Providers[0]

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": provider.IdentityHash,
		"api_key_hash":  provider.APIKeyHash,
		"api_key":       "",
		"disabled":      false,
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	excluded, ok := items[0]["excluded-models"].([]any)
	if !ok || len(excluded) != 2 || excluded[0] != "gemini-old" || excluded[1] != "gemini-legacy" {
		t.Fatalf("excluded-models = %#v, want omitted payload to preserve remote exclusions", items[0]["excluded-models"])
	}
}

func TestAIProviderUpdateBlankAPIKeyPreservesRemoteKeyAndUnknownFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":      "gemini-secret-key",
			"priority":     float64(1),
			"unknown-flag": "keep-me",
			"base-url":     "https://old.example",
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":           "gemini",
		"identity_hash":   snapshot.Providers[0].IdentityHash,
		"api_key_hash":    snapshot.Providers[0].APIKeyHash,
		"api_key":         "",
		"priority":        9,
		"base_url":        "https://new.example",
		"models":          []map[string]any{{"name": "gemini-2.5-pro", "force_mapping": true}},
		"headers":         []map[string]any{{"name": "X-Test", "value": "yes"}},
		"excluded_models": []string{"gemini-old"},
		"disable_cooling": true,
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	got := items[0]
	if got["api-key"] != "gemini-secret-key" {
		t.Fatalf("api-key = %#v, want preserved remote key", got["api-key"])
	}
	if got["unknown-flag"] != "keep-me" {
		t.Fatalf("unknown field = %#v, want preserved", got["unknown-flag"])
	}
	if got["priority"] != float64(9) {
		t.Fatalf("priority = %#v, want 9", got["priority"])
	}
	if got["base-url"] != "https://new.example" {
		t.Fatalf("base-url = %#v, want updated", got["base-url"])
	}
}

func TestAIProviderUpdatePreservesUnknownModelFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key": "gemini-secret-key",
			"models": []map[string]any{
				{
					"name":          "gemini-2.5-pro",
					"alias":         "old-alias",
					"unknown-model": "keep-me",
					"vendor-options": map[string]any{
						"route": "keep",
					},
				},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"api_key_hash":  snapshot.Providers[0].APIKeyHash,
		"api_key":       "",
		"models": []map[string]any{
			{"name": "gemini-2.5-pro", "alias": "new-alias", "force_mapping": true},
		},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	models := fake.config["gemini-api-key"].([]map[string]any)[0]["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models length = %d, want 1", len(models))
	}
	model := models[0].(map[string]any)
	if model["alias"] != "new-alias" || model["force-mapping"] != true {
		t.Fatalf("model known fields = %#v, want updated alias and force-mapping", model)
	}
	if model["unknown-model"] != "keep-me" {
		t.Fatalf("unknown model field = %#v, want preserved", model["unknown-model"])
	}
	options, ok := model["vendor-options"].(map[string]any)
	if !ok || options["route"] != "keep" {
		t.Fatalf("vendor-options = %#v, want preserved nested options", model["vendor-options"])
	}
}

func TestAIProviderUpdatePreservesUnknownClaudeCloakFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["claude-api-key"] = []map[string]any{
		{
			"api-key": "claude-secret-key",
			"cloak": map[string]any{
				"mode": "old",
				"sensitive-words": []any{
					"old-secret",
				},
				"unknown-cloak": "keep-me",
				"vendor-options": map[string]any{
					"route": "keep",
				},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", map[string]any{
		"brand":         "claude",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"api_key_hash":  snapshot.Providers[0].APIKeyHash,
		"api_key":       "",
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
		"cloak": map[string]any{
			"mode":            "new",
			"strict_mode":     true,
			"sensitive_words": []string{"new-secret"},
			"cache_user_id":   true,
		},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	cloak := fake.config["claude-api-key"].([]map[string]any)[0]["cloak"].(map[string]any)
	if cloak["mode"] != "new" || cloak["strict-mode"] != true || cloak["cache-user-id"] != true {
		t.Fatalf("cloak known fields = %#v, want updated mode/strict-mode/cache-user-id", cloak)
	}
	if cloak["unknown-cloak"] != "keep-me" {
		t.Fatalf("unknown cloak field = %#v, want preserved", cloak["unknown-cloak"])
	}
	options, ok := cloak["vendor-options"].(map[string]any)
	if !ok || options["route"] != "keep" {
		t.Fatalf("vendor-options = %#v, want preserved nested options", cloak["vendor-options"])
	}
	words := cloak["sensitive-words"].([]any)
	if len(words) != 1 || words[0] != "new-secret" {
		t.Fatalf("sensitive-words = %#v, want updated words", words)
	}
}

func TestAIProviderUpdateClearsOptionalFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":   "gemini-secret-key",
			"priority":  float64(5),
			"prefix":    "gemini-prefix",
			"base-url":  "https://gemini.example",
			"proxy-url": "http://proxy.local",
		},
	}
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":     "custom-openai",
			"base-url": "https://openai.example",
			"api-key-entries": []map[string]any{
				{"api-key": "openai-secret-key", "proxy-url": "http://entry-proxy.local"},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	var snapshot struct {
		Providers []struct {
			Brand         string `json:"brand"`
			Index         int    `json:"index"`
			IdentityHash  string `json:"identity_hash"`
			APIKeyHash    string `json:"api_key_hash"`
			APIKeyMasked  string `json:"api_key_masked"`
			RecentSuccess int    `json:"recent_success"`
			RecentFailure int    `json:"recent_failure"`
			APIKeyEntries []struct {
				APIKeyHash string `json:"api_key_hash"`
			} `json:"api_key_entries"`
		} `json:"providers"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	var geminiProvider, openAIProvider struct {
		Brand         string `json:"brand"`
		Index         int    `json:"index"`
		IdentityHash  string `json:"identity_hash"`
		APIKeyHash    string `json:"api_key_hash"`
		APIKeyMasked  string `json:"api_key_masked"`
		RecentSuccess int    `json:"recent_success"`
		RecentFailure int    `json:"recent_failure"`
		APIKeyEntries []struct {
			APIKeyHash string `json:"api_key_hash"`
		} `json:"api_key_entries"`
	}
	for _, provider := range snapshot.Providers {
		switch provider.Brand {
		case "gemini":
			geminiProvider = provider
		case "openai_compatibility":
			openAIProvider = provider
		}
	}
	if geminiProvider.IdentityHash == "" || openAIProvider.IdentityHash == "" {
		t.Fatalf("snapshot providers = %#v, want gemini and openai-compatible", snapshot.Providers)
	}
	if len(openAIProvider.APIKeyEntries) != 1 || openAIProvider.APIKeyEntries[0].APIKeyHash == "" {
		t.Fatalf("openai key entries = %#v, want one entry hash", openAIProvider.APIKeyEntries)
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": geminiProvider.IdentityHash,
		"api_key_hash":  geminiProvider.APIKeyHash,
		"api_key":       "",
		"priority":      nil,
		"prefix":        "",
		"base_url":      "",
		"proxy_url":     "",
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
	}, cookies, nil)
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":         "openai_compatibility",
		"identity_hash": openAIProvider.IdentityHash,
		"name":          "custom-openai",
		"base_url":      "https://openai.example",
		"api_key_entries": []map[string]any{
			{"api_key": "", "api_key_hash": openAIProvider.APIKeyEntries[0].APIKeyHash, "proxy_url": ""},
		},
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	gemini := fake.config["gemini-api-key"].([]map[string]any)[0]
	for _, key := range []string{"priority", "prefix", "base-url", "proxy-url"} {
		if _, ok := gemini[key]; ok {
			t.Fatalf("gemini %s = %#v, want cleared", key, gemini[key])
		}
	}
	entries := fake.config["openai-compatibility"].([]map[string]any)[0]["api-key-entries"].([]any)
	entry := entries[0].(map[string]any)
	if entry["api-key"] != "openai-secret-key" {
		t.Fatalf("openai entry api-key = %#v, want preserved remote key", entry["api-key"])
	}
	if _, ok := entry["proxy-url"]; ok {
		t.Fatalf("openai entry proxy-url = %#v, want cleared", entry["proxy-url"])
	}
}

func TestAIProviderUpdateSelectorUsesOriginalBaseURL(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	original := map[string]any{
		"api-key":  "shared-secret-key",
		"base-url": "https://original.example",
		"marker":   "original",
	}
	other := map[string]any{
		"api-key":  "shared-secret-key",
		"base-url": "https://other.example",
		"marker":   "other",
	}
	fake.config["gemini-api-key"] = []map[string]any{original, other}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 2 {
		t.Fatalf("providers length = %d, want 2", len(snapshot.Providers))
	}

	fake.mu.Lock()
	fake.config["gemini-api-key"] = []map[string]any{other, original}
	fake.mu.Unlock()
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":             "gemini",
		"identity_hash":     snapshot.Providers[0].IdentityHash,
		"api_key_hash":      snapshot.Providers[0].APIKeyHash,
		"api_key":           "",
		"original_base_url": "https://original.example",
		"base_url":          "https://updated.example",
		"models":            []map[string]any{},
		"headers":           []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if items[0]["marker"] != "other" || items[0]["base-url"] != "https://other.example" {
		t.Fatalf("first provider = %#v, want untouched other provider", items[0])
	}
	if items[1]["marker"] != "original" || items[1]["base-url"] != "https://updated.example" {
		t.Fatalf("second provider = %#v, want updated original provider", items[1])
	}
}

func TestAIProviderOpenAIEntriesCloneMatchedKeyFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":     "custom-openai",
			"base-url": "https://openai.example",
			"api-key-entries": []map[string]any{
				{"api-key": "deleted-secret-key", "proxy-url": "http://deleted-proxy.local", "unknown-entry": "deleted"},
				{"api-key": "survivor-secret-key", "proxy-url": "http://survivor-proxy.local", "unknown-entry": "survivor"},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	var snapshot struct {
		Providers []struct {
			Brand         string `json:"brand"`
			Index         int    `json:"index"`
			IdentityHash  string `json:"identity_hash"`
			Name          string `json:"name"`
			APIKeyEntries []struct {
				APIKeyHash string `json:"api_key_hash"`
			} `json:"api_key_entries"`
		} `json:"providers"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 || len(snapshot.Providers[0].APIKeyEntries) != 2 {
		t.Fatalf("snapshot = %#v, want one provider with two key entries", snapshot)
	}
	survivorHash := snapshot.Providers[0].APIKeyEntries[1].APIKeyHash
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":         "openai_compatibility",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"name":          "custom-openai",
		"base_url":      "https://openai.example",
		"api_key_entries": []map[string]any{
			{"api_key": "", "api_key_hash": survivorHash, "proxy_url": "http://updated-proxy.local"},
		},
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	entries := fake.config["openai-compatibility"].([]map[string]any)[0]["api-key-entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	entry := entries[0].(map[string]any)
	if entry["api-key"] != "survivor-secret-key" {
		t.Fatalf("api-key = %#v, want survivor-secret-key", entry["api-key"])
	}
	if entry["unknown-entry"] != "survivor" {
		t.Fatalf("unknown-entry = %#v, want survivor", entry["unknown-entry"])
	}
	if entry["proxy-url"] != "http://updated-proxy.local" {
		t.Fatalf("proxy-url = %#v, want updated proxy", entry["proxy-url"])
	}
}

func TestAIProviderOpenAIUpdateRejectsNewBlankKeyEntry(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":     "custom-openai",
			"base-url": "https://openai.example",
			"api-key-entries": []map[string]any{
				{"api-key": "deleted-secret-key", "proxy-url": "http://deleted-proxy.local"},
				{"api-key": "survivor-secret-key", "proxy-url": "http://survivor-proxy.local"},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	var snapshot struct {
		Providers []struct {
			Brand         string `json:"brand"`
			Index         int    `json:"index"`
			IdentityHash  string `json:"identity_hash"`
			Name          string `json:"name"`
			APIKeyEntries []struct {
				APIKeyHash string `json:"api_key_hash"`
			} `json:"api_key_entries"`
		} `json:"providers"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 || len(snapshot.Providers[0].APIKeyEntries) != 2 {
		t.Fatalf("snapshot = %#v, want one provider with two key entries", snapshot)
	}
	survivorHash := snapshot.Providers[0].APIKeyEntries[1].APIKeyHash
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":         "openai_compatibility",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"name":          "custom-openai",
		"base_url":      "https://openai.example",
		"api_key_entries": []map[string]any{
			{"api_key": "", "api_key_hash": survivorHash},
			{"api_key": "", "proxy_url": "http://new-proxy.local"},
		},
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	entries := fake.config["openai-compatibility"].([]map[string]any)[0]["api-key-entries"].([]map[string]any)
	if len(entries) != 2 || entries[0]["api-key"] != "deleted-secret-key" || entries[1]["api-key"] != "survivor-secret-key" {
		t.Fatalf("entries after rejected update = %#v, want original two key entries", entries)
	}
}

func TestAIProviderOpenAIUpdateRejectsStaleKeyEntryHash(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":     "custom-openai",
			"base-url": "https://openai.example",
			"api-key-entries": []map[string]any{
				{"api-key": "old-secret-key", "proxy-url": "http://old-proxy.local"},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	var snapshot struct {
		Providers []struct {
			Brand         string `json:"brand"`
			Index         int    `json:"index"`
			IdentityHash  string `json:"identity_hash"`
			Name          string `json:"name"`
			APIKeyEntries []struct {
				APIKeyHash string `json:"api_key_hash"`
			} `json:"api_key_entries"`
		} `json:"providers"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 || len(snapshot.Providers[0].APIKeyEntries) != 1 {
		t.Fatalf("snapshot = %#v, want one provider with one key entry", snapshot)
	}

	fake.mu.Lock()
	fake.config["openai-compatibility"].([]map[string]any)[0]["api-key-entries"] = []map[string]any{
		{"api-key": "replacement-secret-key", "proxy-url": "http://replacement-proxy.local"},
	}
	fake.mu.Unlock()
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":         "openai_compatibility",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"name":          "custom-openai",
		"base_url":      "https://openai.example",
		"api_key_entries": []map[string]any{
			{"api_key": "", "api_key_hash": snapshot.Providers[0].APIKeyEntries[0].APIKeyHash, "proxy_url": "http://updated-proxy.local"},
		},
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusConflict)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	entries := fake.config["openai-compatibility"].([]map[string]any)[0]["api-key-entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["api-key"] != "replacement-secret-key" || entries[0]["proxy-url"] != "http://replacement-proxy.local" {
		t.Fatalf("entries after rejected stale update = %#v, want untouched replacement entry", entries)
	}
}

func TestAIProviderUpdateReturnsConflictWhenTargetMissing(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{{"api-key": "gemini-secret-key"}}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": "missing",
		"api_key":       "",
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
	}, cookies, http.StatusConflict)
}

func TestAIProviderUpdateRejectsStaleAuthIndexIdentity(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"auth-index": "auth-original",
			"base-url":   "https://gemini.example",
			"proxy-url":  "http://original-proxy.local",
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	fake.mu.Lock()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"auth-index": "auth-replacement",
			"base-url":   "https://gemini.example",
			"proxy-url":  "http://replacement-proxy.local",
		},
	}
	fake.mu.Unlock()
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":             "gemini",
		"identity_hash":     snapshot.Providers[0].IdentityHash,
		"api_key":           "",
		"original_base_url": "https://gemini.example",
		"base_url":          "https://gemini.example",
		"models":            []map[string]any{},
		"headers":           []map[string]any{},
	}, cookies, http.StatusConflict)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if len(items) != 1 || items[0]["auth-index"] != "auth-replacement" || items[0]["proxy-url"] != "http://replacement-proxy.local" {
		t.Fatalf("providers after rejected stale update = %#v, want untouched replacement provider", items)
	}
}

func TestAIProviderUpdatePreservesAuthIndexCredential(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"auth-index": "auth-gemini-0",
			"base-url":   "https://gemini.example",
			"proxy-url":  "http://proxy.local",
			"models":     []map[string]any{{"name": "gemini-2.5-pro"}},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":             "gemini",
		"identity_hash":     snapshot.Providers[0].IdentityHash,
		"api_key":           "",
		"original_base_url": "https://gemini.example",
		"base_url":          "https://gemini.example",
		"models":            []map[string]any{{"name": "gemini-2.5-pro", "alias": "Gemini Pro"}},
		"headers":           []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	item := fake.config["gemini-api-key"].([]map[string]any)[0]
	if item["auth-index"] != "auth-gemini-0" {
		t.Fatalf("auth-index = %#v, want preserved auth-gemini-0", item["auth-index"])
	}
	if _, ok := item["api-key"]; ok {
		t.Fatalf("api-key = %#v, want auth-index-only provider to stay without api-key", item["api-key"])
	}
}

func TestAIProviderSaveRequiresBaseURLForCodexOpenAICompatibleAndVertex(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/ai-providers/codex", map[string]any{
		"brand":   "codex",
		"api_key": "codex-secret-key",
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/ai-providers/openai_compatibility", map[string]any{
		"brand":    "openai_compatibility",
		"name":     "custom-openai",
		"base_url": "",
		"api_key_entries": []map[string]any{
			{"api_key": "openai-secret-key"},
		},
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/ai-providers/vertex", map[string]any{
		"brand":   "vertex",
		"api_key": "vertex-secret-key",
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := len(fake.config["codex-api-key"].([]map[string]any)); got != 0 {
		t.Fatalf("codex providers length = %d, want 0", got)
	}
	if got := len(fake.config["openai-compatibility"].([]map[string]any)); got != 0 {
		t.Fatalf("openai-compatible providers length = %d, want 0", got)
	}
	if got := len(fake.config["vertex-api-key"].([]map[string]any)); got != 0 {
		t.Fatalf("vertex providers length = %d, want 0", got)
	}
}

func TestAIProviderSaveRejectsInvalidBaseURL(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{{"api-key": "gemini-secret-key"}}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/0", map[string]any{
		"brand":         "gemini",
		"identity_hash": snapshot.Providers[0].IdentityHash,
		"api_key_hash":  snapshot.Providers[0].APIKeyHash,
		"api_key":       "",
		"base_url":      "not-a-url",
		"models":        []map[string]any{},
		"headers":       []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	provider := fake.config["gemini-api-key"].([]map[string]any)[0]
	if _, ok := provider["base-url"]; ok {
		t.Fatalf("base-url = %#v, want rejected update to leave provider unchanged", provider["base-url"])
	}
}

func TestAIProviderOpenAIAllowsEmptyKeyEntries(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/openai_compatibility", map[string]any{
		"brand":           "openai_compatibility",
		"name":            "custom-openai",
		"base_url":        "https://openai.example",
		"api_key_entries": []map[string]any{},
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	items := fake.config["openai-compatibility"].([]map[string]any)
	if len(items) != 1 {
		fake.mu.Unlock()
		t.Fatalf("openai-compatible providers length = %d, want 1", len(items))
	}
	entries, ok := items[0]["api-key-entries"].([]any)
	if !ok || len(entries) != 0 {
		fake.mu.Unlock()
		t.Fatalf("api-key-entries = %#v, want empty list", items[0]["api-key-entries"])
	}
	fake.mu.Unlock()

	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":           "openai_compatibility",
		"identity_hash":   snapshot.Providers[0].IdentityHash,
		"name":            "custom-openai",
		"disabled":        true,
		"base_url":        "https://openai.example",
		"api_key_entries": []map[string]any{},
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items = fake.config["openai-compatibility"].([]map[string]any)
	entries, ok = items[0]["api-key-entries"].([]any)
	if !ok || len(entries) != 0 || items[0]["disabled"] != true {
		t.Fatalf("updated openai-compatible provider = %#v, want disabled provider with empty key entries", items[0])
	}
}

func TestAIProviderDiscoveryAndTestUseUnsavedPayloadWithoutEchoingSecret(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	payload := map[string]any{
		"brand": "openai_compatibility",
		"provider": map[string]any{
			"brand":      "openai_compatibility",
			"name":       "draft-openai",
			"base_url":   "https://openai.example",
			"auth_index": "attacker-auth",
			"api_key_entries": []map[string]any{
				{"api_key": "unsaved-openai-secret"},
			},
			"models":  []map[string]any{{"name": "gpt-test"}},
			"headers": []map[string]any{{"name": "X-Draft", "value": "1"}},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK || len(discovery.Models) != 1 || discovery.Models[0].Name != "gpt-test" {
		t.Fatalf("discovery response = %#v, want gpt-test", discovery)
	}
	testPayload := payload
	testPayload["model"] = "gpt-test"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK || connectivity.Reply != "ok" {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	firstHeader := fake.apiCallBodies[0]["header"].(map[string]any)
	if firstHeader["Authorization"] != "Bearer unsaved-openai-secret" || firstHeader["X-Draft"] != "1" {
		t.Fatalf("api-call headers = %#v, want submitted unsaved key and custom header", firstHeader)
	}
	for index, body := range fake.apiCallBodies {
		if body["auth_index"] != nil {
			t.Fatalf("api-call %d auth_index = %#v, want omitted for unsaved submitted key", index, body["auth_index"])
		}
	}
	encoded, err := json.Marshal([]aiProviderActionTestResponse{discovery, connectivity})
	if err != nil {
		t.Fatalf("marshal action responses: %v", err)
	}
	if strings.Contains(string(encoded), "unsaved-openai-secret") {
		t.Fatalf("action response leaked submitted secret: %s", string(encoded))
	}
}

func TestAIProviderOpenAINoAuthActionsOmitAuthorization(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	unsavedPayload := map[string]any{
		"brand": "openai_compatibility",
		"provider": map[string]any{
			"brand":           "openai_compatibility",
			"name":            "draft-no-auth-openai",
			"base_url":        "https://draft-openai.local/v1",
			"auth_index":      "attacker-auth",
			"api_key_entries": []map[string]any{},
			"models":          []map[string]any{{"name": "gpt-test"}},
			"headers":         []map[string]any{},
		},
	}
	unsavedDiscovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", unsavedPayload, cookies, &unsavedDiscovery)
	if !unsavedDiscovery.OK || len(unsavedDiscovery.Models) != 1 || unsavedDiscovery.Models[0].Name != "gpt-test" {
		t.Fatalf("unsaved discovery response = %#v, want gpt-test", unsavedDiscovery)
	}
	unsavedTestPayload := unsavedPayload
	unsavedTestPayload["model"] = "gpt-test"
	unsavedTestPayload["message"] = "ping"
	unsavedConnectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", unsavedTestPayload, cookies, &unsavedConnectivity)
	if !unsavedConnectivity.OK || unsavedConnectivity.Reply != "ok" {
		t.Fatalf("unsaved connectivity response = %#v, want ok", unsavedConnectivity)
	}
	fake.mu.Lock()
	if len(fake.apiCallBodies) != 2 {
		fake.mu.Unlock()
		t.Fatalf("unsaved api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	if fake.apiCallBodies[0]["url"] != "https://draft-openai.local/v1/models" {
		fake.mu.Unlock()
		t.Fatalf("unsaved discovery url = %#v, want draft no-auth models endpoint", fake.apiCallBodies[0]["url"])
	}
	if fake.apiCallBodies[1]["url"] != "https://draft-openai.local/v1/chat/completions" {
		fake.mu.Unlock()
		t.Fatalf("unsaved test url = %#v, want draft no-auth chat completions endpoint", fake.apiCallBodies[1]["url"])
	}
	for index, body := range fake.apiCallBodies {
		if body["auth_index"] != nil {
			fake.mu.Unlock()
			t.Fatalf("unsaved api-call %d auth_index = %#v, want omitted", index, body["auth_index"])
		}
		if header, ok := body["header"].(map[string]any); ok && header["Authorization"] != nil {
			fake.mu.Unlock()
			t.Fatalf("unsaved api-call %d Authorization = %#v, want omitted", index, header["Authorization"])
		}
	}
	fake.apiCallBodies = nil
	fake.mu.Unlock()

	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/openai_compatibility", map[string]any{
		"brand":           "openai_compatibility",
		"name":            "no-auth-openai",
		"base_url":        "https://openai.local/v1",
		"api_key_entries": []map[string]any{},
		"models":          []map[string]any{},
		"headers":         []map[string]any{},
	}, cookies, nil)
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	fake.mu.Lock()
	fake.apiCallBodies = nil
	fake.mu.Unlock()
	payload := map[string]any{
		"brand": "openai_compatibility",
		"provider": map[string]any{
			"brand":           "openai_compatibility",
			"index":           0,
			"identity_hash":   snapshot.Providers[0].IdentityHash,
			"name":            "no-auth-openai",
			"api_key_entries": []map[string]any{},
			"models":          []map[string]any{},
			"headers":         []map[string]any{},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK || len(discovery.Models) != 1 || discovery.Models[0].Name != "gpt-test" {
		t.Fatalf("discovery response = %#v, want gpt-test", discovery)
	}
	testPayload := payload
	testPayload["model"] = "gpt-test"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK || connectivity.Reply != "ok" {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	if fake.apiCallBodies[0]["url"] != "https://openai.local/v1/models" {
		t.Fatalf("discovery url = %#v, want no-auth models endpoint", fake.apiCallBodies[0]["url"])
	}
	if fake.apiCallBodies[1]["url"] != "https://openai.local/v1/chat/completions" {
		t.Fatalf("test url = %#v, want no-auth chat completions endpoint", fake.apiCallBodies[1]["url"])
	}
	for index, body := range fake.apiCallBodies {
		if header, ok := body["header"].(map[string]any); ok && header["Authorization"] != nil {
			t.Fatalf("api-call %d Authorization = %#v, want omitted for no-auth OpenAI-compatible", index, header["Authorization"])
		}
	}
}

func TestAIProviderAPICallNormalizesVersionedBaseURLs(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	cases := []struct {
		name         string
		brand        string
		baseURL      string
		model        string
		discoveryURL string
		testURL      string
	}{
		{
			name:         "gemini v1beta",
			brand:        "gemini",
			baseURL:      "https://generativelanguage.googleapis.com/v1beta",
			model:        "gemini-2.5-pro",
			discoveryURL: "https://generativelanguage.googleapis.com/v1beta/models",
			testURL:      "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent",
		},
		{
			name:         "claude v1",
			brand:        "claude",
			baseURL:      "https://api.anthropic.com/v1",
			model:        "claude-3-5-sonnet",
			discoveryURL: "https://api.anthropic.com/v1/models",
			testURL:      "https://api.anthropic.com/v1/messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.mu.Lock()
			fake.apiCallBodies = nil
			fake.mu.Unlock()

			payload := map[string]any{
				"brand": tc.brand,
				"provider": map[string]any{
					"brand":    tc.brand,
					"api_key":  "unsaved-secret",
					"base_url": tc.baseURL,
					"models":   []map[string]any{{"name": tc.model}},
					"headers":  []map[string]any{},
				},
			}
			discovery := aiProviderActionTestResponse{}
			requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
			if !discovery.OK {
				t.Fatalf("discovery response = %#v, want ok", discovery)
			}
			testPayload := map[string]any{
				"brand":    tc.brand,
				"provider": payload["provider"],
				"model":    tc.model,
				"message":  "ping",
			}
			connectivity := aiProviderActionTestResponse{}
			requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
			if !connectivity.OK {
				t.Fatalf("connectivity response = %#v, want ok", connectivity)
			}

			fake.mu.Lock()
			bodies := append([]map[string]any(nil), fake.apiCallBodies...)
			fake.mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("api-call count = %d, want 2", len(bodies))
			}
			if bodies[0]["url"] != tc.discoveryURL {
				t.Fatalf("discovery url = %#v, want %s", bodies[0]["url"], tc.discoveryURL)
			}
			if bodies[1]["url"] != tc.testURL {
				t.Fatalf("test url = %#v, want %s", bodies[1]["url"], tc.testURL)
			}
		})
	}
}

func TestAIProviderCodexConnectivityUsesResponsesAPI(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	payload := map[string]any{
		"brand": "codex",
		"provider": map[string]any{
			"brand":    "codex",
			"api_key":  "codex-secret-key",
			"base_url": "https://api.openai.com/v1",
			"models":   []map[string]any{{"name": "gpt-5"}},
			"headers":  []map[string]any{},
		},
		"model":   "gpt-5",
		"message": "ping",
	}
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", payload, cookies, &connectivity)
	if !connectivity.OK {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 1 {
		t.Fatalf("api-call count = %d, want 1", len(fake.apiCallBodies))
	}
	body := fake.apiCallBodies[0]
	if body["method"] != http.MethodPost || body["url"] != "https://api.openai.com/v1/responses" {
		t.Fatalf("codex request = %#v, want Responses API endpoint", body)
	}
	header := body["header"].(map[string]any)
	if header["Authorization"] != "Bearer codex-secret-key" {
		t.Fatalf("Authorization = %#v, want codex bearer key", header["Authorization"])
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body["data"].(string)), &data); err != nil {
		t.Fatalf("decode codex request data: %v", err)
	}
	if data["model"] != "gpt-5" || data["input"] != "ping" {
		t.Fatalf("codex request data = %#v, want model and input", data)
	}
	if _, ok := data["messages"]; ok {
		t.Fatalf("codex request data = %#v, want no chat messages payload", data)
	}
}

func TestAIProviderActionRespectsSubmittedEmptyHeaders(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":  "gemini-secret-key",
			"base-url": "https://gemini.example",
			"headers":  map[string]any{"X-Remote": "stale"},
			"models":   []map[string]any{{"name": "gemini-2.5-pro"}},
		},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	payload := map[string]any{
		"brand": "gemini",
		"provider": map[string]any{
			"brand":         "gemini",
			"index":         0,
			"identity_hash": snapshot.Providers[0].IdentityHash,
			"api_key_hash":  snapshot.Providers[0].APIKeyHash,
			"auth_index":    "attacker-auth",
			"models":        []map[string]any{{"name": "gemini-2.5-pro"}},
			"headers":       []map[string]any{},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK {
		t.Fatalf("discovery response = %#v, want ok", discovery)
	}
	testPayload := payload
	testPayload["model"] = "gemini-2.5-pro"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	for index, body := range fake.apiCallBodies {
		header := body["header"].(map[string]any)
		if header["X-Remote"] != nil {
			t.Fatalf("api-call %d X-Remote = %#v, want cleared header omitted", index, header["X-Remote"])
		}
	}
}

func TestAIProviderAPICallIncludesAuthIndexForSavedProvider(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":    "gemini-secret-key",
			"base-url":   "https://gemini.example",
			"auth-index": "auth-gemini-0",
			"proxy-url":  "http://proxy.local",
			"models":     []map[string]any{{"name": "gemini-2.5-pro"}},
		},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	payload := map[string]any{
		"brand": "gemini",
		"provider": map[string]any{
			"brand":         "gemini",
			"index":         0,
			"identity_hash": snapshot.Providers[0].IdentityHash,
			"api_key_hash":  snapshot.Providers[0].APIKeyHash,
			"models":        []map[string]any{{"name": "gemini-2.5-pro"}},
			"headers":       []map[string]any{},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK {
		t.Fatalf("discovery response = %#v, want ok", discovery)
	}
	testPayload := payload
	testPayload["model"] = "gemini-2.5-pro"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	for index, body := range fake.apiCallBodies {
		if body["auth_index"] != "auth-gemini-0" {
			t.Fatalf("api-call %d auth_index = %#v, want auth-gemini-0", index, body["auth_index"])
		}
	}
}

func TestAIProviderAPICallAllowsAuthIndexWithoutPlaintextKey(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"base-url":   "https://gemini.example",
			"auth-index": "auth-gemini-0",
			"proxy-url":  "http://proxy.local",
			"models":     []map[string]any{{"name": "gemini-2.5-pro"}},
		},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	payload := map[string]any{
		"brand": "gemini",
		"provider": map[string]any{
			"brand":         "gemini",
			"index":         0,
			"identity_hash": snapshot.Providers[0].IdentityHash,
			"auth_index":    "attacker-auth",
			"models":        []map[string]any{{"name": "gemini-2.5-pro"}},
			"headers":       []map[string]any{},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK {
		t.Fatalf("discovery response = %#v, want ok", discovery)
	}
	testPayload := payload
	testPayload["model"] = "gemini-2.5-pro"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	for index, body := range fake.apiCallBodies {
		if body["auth_index"] != "auth-gemini-0" {
			t.Fatalf("api-call %d auth_index = %#v, want auth-gemini-0", index, body["auth_index"])
		}
		header, ok := body["header"].(map[string]any)
		if !ok {
			t.Fatalf("api-call %d header = %#v, want header map", index, body["header"])
		}
		if header["x-goog-api-key"] != "$TOKEN$" {
			t.Fatalf("api-call %d x-goog-api-key = %#v, want TOKEN placeholder", index, header["x-goog-api-key"])
		}
	}
}

func TestAIProviderVertexAPICallUsesVertexCompatibleShape(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["vertex-api-key"] = []map[string]any{
		{
			"api-key":  "vertex-secret-key",
			"base-url": "https://aiplatform.googleapis.com",
			"models":   []map[string]any{{"name": "gemini-2.5-pro"}},
		},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshot := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, &snapshot)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}

	payload := map[string]any{
		"brand": "vertex",
		"provider": map[string]any{
			"brand":         "vertex",
			"index":         0,
			"identity_hash": snapshot.Providers[0].IdentityHash,
			"api_key_hash":  snapshot.Providers[0].APIKeyHash,
			"models":        []map[string]any{{"name": "gemini-2.5-pro"}},
			"headers":       []map[string]any{},
		},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", payload, cookies, &discovery)
	if !discovery.OK {
		t.Fatalf("discovery response = %#v, want ok", discovery)
	}
	if len(discovery.Models) != 1 || discovery.Models[0].Name != "gemini-2.5-pro" {
		t.Fatalf("discovery models = %#v, want parsed Vertex publisher model", discovery.Models)
	}
	testPayload := payload
	testPayload["model"] = "publishers/google/models/gemini-2.5-pro"
	testPayload["message"] = "ping"
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", testPayload, cookies, &connectivity)
	if !connectivity.OK {
		t.Fatalf("connectivity response = %#v, want ok", connectivity)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	discoveryBody := fake.apiCallBodies[0]
	if discoveryBody["method"] != http.MethodGet || discoveryBody["url"] != "https://aiplatform.googleapis.com/v1/publishers/google/models" {
		t.Fatalf("vertex discovery request = %#v, want Vertex models endpoint", discoveryBody)
	}
	testBody := fake.apiCallBodies[1]
	if testBody["method"] != http.MethodPost || testBody["url"] != "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-pro:generateContent" {
		t.Fatalf("vertex test request = %#v, want Vertex generateContent endpoint", testBody)
	}
	for index, body := range fake.apiCallBodies {
		header, ok := body["header"].(map[string]any)
		if !ok {
			t.Fatalf("api-call %d header = %#v, want header map", index, body["header"])
		}
		if header["x-goog-api-key"] != "vertex-secret-key" {
			t.Fatalf("api-call %d x-goog-api-key = %#v, want vertex key", index, header["x-goog-api-key"])
		}
		if header["Authorization"] != nil {
			t.Fatalf("api-call %d Authorization = %#v, want omitted for Vertex API key", index, header["Authorization"])
		}
	}
}

func setupAIProviderTestApp(t *testing.T, cpaURL string) (http.Handler, []*http.Cookie, func()) {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpaURL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)
	return handler, cookies, app.Close
}

func requestRawJSON(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	cookies []*http.Cookie,
	expectedStatus int,
) []byte {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != expectedStatus {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, recorder.Code, expectedStatus, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}
