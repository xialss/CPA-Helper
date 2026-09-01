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
		"/v0/management/xai-api-key":          true,
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

func TestAIProvidersReorderSnapshotUsesWriteTargetConfig(t *testing.T) {
	providerPathToKey := map[string]string{
		"/v0/management/gemini-api-key":       "gemini-api-key",
		"/v0/management/codex-api-key":        "codex-api-key",
		"/v0/management/claude-api-key":       "claude-api-key",
		"/v0/management/openai-compatibility": "openai-compatibility",
		"/v0/management/vertex-api-key":       "vertex-api-key",
		"/v0/management/xai-api-key":          "xai-api-key",
	}
	var oldConfigMu sync.Mutex
	oldConfig := map[string]any{
		"gemini-api-key": []map[string]any{
			{"api-key": "old-first-secret", "name": "old-first", "base-url": "https://old-first.example"},
			{"api-key": "old-second-secret", "name": "old-second", "base-url": "https://old-second.example"},
		},
		"codex-api-key":        []map[string]any{},
		"claude-api-key":       []map[string]any{},
		"openai-compatibility": []map[string]any{},
		"vertex-api-key":       []map[string]any{},
		"xai-api-key":          []map[string]any{},
	}
	putReceived := make(chan struct{})
	releasePut := make(chan struct{})
	var putReceivedOnce sync.Once
	var releasePutOnce sync.Once
	releasePendingPut := func() {
		releasePutOnce.Do(func() { close(releasePut) })
	}
	defer releasePendingPut()

	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
			oldConfigMu.Lock()
			defer oldConfigMu.Unlock()
			_ = json.NewEncoder(w).Encode(oldConfig)
			return
		}
		if r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		key, ok := providerPathToKey[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			oldConfigMu.Lock()
			defer oldConfigMu.Unlock()
			_ = json.NewEncoder(w).Encode(oldConfig[key])
			return
		case http.MethodPut:
			if key != "gemini-api-key" {
				http.Error(w, "unexpected provider write", http.StatusMethodNotAllowed)
				return
			}
			var next []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				t.Fatalf("decode reorder PUT: %v", err)
			}
			oldConfigMu.Lock()
			oldConfig[key] = next
			oldConfigMu.Unlock()
			putReceivedOnce.Do(func() { close(putReceived) })
			<-releasePut
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}))
	defer oldServer.Close()

	var newManagementRequests atomic.Int32
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		newManagementRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gemini-api-key":       []map[string]any{{"api-key": "new-secret", "name": "new-target", "base-url": "https://new.example"}},
				"codex-api-key":        []map[string]any{},
				"claude-api-key":       []map[string]any{},
				"openai-compatibility": []map[string]any{},
				"vertex-api-key":       []map[string]any{},
				"xai-api-key":          []map[string]any{},
			})
			return
		}
		if r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if key, ok := providerPathToKey[r.URL.Path]; ok && r.Method == http.MethodGet {
			if key == "gemini-api-key" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"api-key": "new-secret", "name": "new-target", "base-url": "https://new.example"}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer newServer.Close()

	handler, cookies, closeApp := setupAIProviderTestApp(t, oldServer.URL)
	defer closeApp()
	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		refs = append(refs, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"api_key_hash":  provider["api_key_hash"],
			"name":          provider["name"],
			"base_url":      provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("initial reorder references = %#v, want two Gemini providers", refs)
	}

	type responseResult struct {
		status int
		body   []byte
	}
	resultCh := make(chan responseResult, 1)
	go func() {
		body, err := json.Marshal(map[string]any{"order": []map[string]any{refs[1], refs[0]}})
		if err != nil {
			resultCh <- responseResult{status: 0, body: []byte(err.Error())}
			return
		}
		request := httptest.NewRequest(http.MethodPut, "/api/ai-providers/gemini/order", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		resultCh <- responseResult{status: recorder.Code, body: append([]byte(nil), recorder.Body.Bytes()...)}
	}()
	select {
	case <-putReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("reorder PUT did not reach the old CLIProxyAPI target")
	}

	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     newServer.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)
	releasePendingPut()
	var result responseResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("reorder request did not finish")
	}
	if result.status != http.StatusOK {
		t.Fatalf("reorder returned %d: %s", result.status, string(result.body))
	}
	var response struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(result.body, &response); err != nil {
		t.Fatalf("decode reorder response: %v", err)
	}
	orderedGemini := make([]map[string]any, 0, 2)
	for _, provider := range response.Providers {
		if provider["brand"] == "gemini" {
			orderedGemini = append(orderedGemini, provider)
		}
	}
	if len(orderedGemini) != 2 || orderedGemini[0]["name"] != "old-second" || orderedGemini[1]["name"] != "old-first" {
		t.Fatalf("reorder response Gemini providers = %#v, want old-second/old-first", orderedGemini)
	}
	if newManagementRequests.Load() != 0 {
		t.Fatalf("new CLIProxyAPI received %d request(s), want none", newManagementRequests.Load())
	}
	oldConfigMu.Lock()
	defer oldConfigMu.Unlock()
	remoteList := oldConfig["gemini-api-key"].([]map[string]any)
	if len(remoteList) != 2 || remoteList[0]["name"] != "old-second" || remoteList[1]["name"] != "old-first" {
		t.Fatalf("old CLIProxyAPI remote order = %#v, want old-second/old-first", remoteList)
	}
}

type aiProvidersTestResponse struct {
	Providers []struct {
		Brand          string `json:"brand"`
		BrandLabel     string `json:"brand_label"`
		Index          int    `json:"index"`
		IdentityHash   string `json:"identity_hash"`
		APIKeyHash     string `json:"api_key_hash"`
		APIKeyMasked   string `json:"api_key_masked"`
		Priority       *int   `json:"priority"`
		Weight         *int   `json:"weight"`
		Prefix         string `json:"prefix"`
		BaseURL        string `json:"base_url"`
		ProxyURL       string `json:"proxy_url"`
		Websockets     *bool  `json:"websockets"`
		DisableCooling *bool  `json:"disable_cooling"`
		Models         []struct {
			Name             string         `json:"name"`
			Alias            string         `json:"alias"`
			DisplayName      string         `json:"display_name"`
			MaxContextLength *int           `json:"max_context_length"`
			ForceMapping     *bool          `json:"force_mapping"`
			IsCompat         *bool          `json:"is_compat"`
			Thinking         map[string]any `json:"thinking"`
		} `json:"models"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
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
		XAI           int `json:"xai"`
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
	t                 *testing.T
	mu                sync.Mutex
	config            map[string]any
	usage             any
	apiCallBodies     []map[string]any
	providerPutCount  int
	providerPutBodies []json.RawMessage
	providerPutStatus int
	providerGetStatus int
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
			"xai-api-key":          []map[string]any{},
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
		"/v0/management/xai-api-key":          "xai-api-key",
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
		if strings.Contains(targetURL, "/responses") && strings.EqualFold(payload["method"].(string), http.MethodPost) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 200,
				"body":        `{"output_text":"responses ok"}`,
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
			if f.providerGetStatus >= 300 {
				w.WriteHeader(f.providerGetStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(f.config[key])
			return
		case http.MethodPut:
			var rawBody json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&rawBody); err != nil {
				f.t.Fatalf("decode provider PUT body: %v", err)
			}
			var next []map[string]any
			if err := json.Unmarshal(rawBody, &next); err != nil {
				f.t.Fatalf("decode provider PUT body: %v", err)
			}
			f.providerPutCount++
			f.providerPutBodies = append(f.providerPutBodies, append(json.RawMessage(nil), rawBody...))
			if f.providerPutStatus >= 300 {
				w.WriteHeader(f.providerPutStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed"})
				return
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

func TestAIProviderOpenAICompatibilityThinkingSurvivesUnrelatedUpdate(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["openai-compatibility"] = []map[string]any{
		{
			"name":            "custom-openai",
			"base-url":        "https://openai.example",
			"api-key-entries": []map[string]any{},
			"models": []map[string]any{
				{
					"name":     "gpt-thinking",
					"alias":    "before",
					"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048},
				},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshotBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	responseModels := aiProviderResponseModelsByName(t, snapshotBody)
	thinking, ok := responseModels["gpt-thinking"]["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("snapshot OpenAI-compatible thinking = %#v", responseModels["gpt-thinking"]["thinking"])
	}

	snapshot := aiProvidersTestResponse{}
	if err := json.Unmarshal(snapshotBody, &snapshot); err != nil {
		t.Fatalf("decode OpenAI-compatible snapshot: %v", err)
	}
	if len(snapshot.Providers) != 1 || len(snapshot.Providers[0].Models) != 1 {
		t.Fatalf("snapshot providers = %#v, want one provider with one model", snapshot.Providers)
	}
	provider := snapshot.Providers[0]
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/openai_compatibility/0", map[string]any{
		"brand":           "openai_compatibility",
		"identity_hash":   provider.IdentityHash,
		"name":            "custom-openai",
		"prefix":          "saved-prefix",
		"base_url":        "https://openai.example",
		"api_key_entries": []map[string]any{},
		"models": []map[string]any{
			{
				"name":     provider.Models[0].Name,
				"alias":    provider.Models[0].Alias,
				"thinking": provider.Models[0].Thinking,
			},
		},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["openai-compatibility"].([]map[string]any)
	if items[0]["prefix"] != "saved-prefix" {
		t.Fatalf("saved OpenAI-compatible prefix = %#v, want saved-prefix", items[0]["prefix"])
	}
	models, ok := items[0]["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("saved OpenAI-compatible models = %#v, want one model", items[0]["models"])
	}
	savedModel, ok := models[0].(map[string]any)
	if !ok {
		t.Fatalf("saved OpenAI-compatible model = %#v, want object", models[0])
	}
	savedThinking, ok := savedModel["thinking"].(map[string]any)
	if !ok || savedThinking["type"] != "enabled" || savedThinking["budget_tokens"] != float64(2048) {
		t.Fatalf("saved OpenAI-compatible thinking = %#v", savedModel["thinking"])
	}
}

func TestAIProviderXAIFieldsRoundTripAndDisableSentinel(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["xai-api-key"] = []map[string]any{
		{
			"api-key":         "xai-secret-key",
			"priority":        3,
			"weight":          5,
			"prefix":          "team-xai",
			"base-url":        "https://api.x.ai/v1",
			"websockets":      true,
			"proxy-url":       "http://proxy.example",
			"headers":         map[string]any{"X-XAI-Test": "before"},
			"excluded-models": []string{"grok-legacy", "*"},
			"disable-cooling": true,
			"alpha-search":    true,
			"alpha_search":    true,
			"future-option":   map[string]any{"route": "keep"},
			"models": []map[string]any{
				{
					"name":                "grok-4.5",
					"alias":               "grok-latest",
					"display-name":        "Grok Latest",
					"max-context-length":  131072,
					"force-mapping":       true,
					"is-compat":           false,
					"thinking":            map[string]any{"type": "enabled", "budget_tokens": 2048},
					"alpha-search":        true,
					"alpha_search":        true,
					"future-model-option": map[string]any{"mode": "keep"},
				},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	responseBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	if strings.Contains(string(responseBody), "xai-secret-key") {
		t.Fatalf("snapshot leaked xAI API key: %s", string(responseBody))
	}
	snapshot := aiProvidersTestResponse{}
	if err := json.Unmarshal(responseBody, &snapshot); err != nil {
		t.Fatalf("decode xAI snapshot: %v", err)
	}
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	provider := snapshot.Providers[0]
	if provider.Brand != "xai" || provider.BrandLabel != "xAI" {
		t.Fatalf("xAI brand = %q/%q, want xai/xAI", provider.Brand, provider.BrandLabel)
	}
	if snapshot.Summary.Total != 1 || snapshot.Summary.XAI != 1 {
		t.Fatalf("xAI summary = total %d xai %d, want 1/1", snapshot.Summary.Total, snapshot.Summary.XAI)
	}
	if provider.APIKeyHash == "" || provider.APIKeyMasked == "" {
		t.Fatalf("xAI safe key selectors missing: hash=%q masked=%q", provider.APIKeyHash, provider.APIKeyMasked)
	}
	if provider.Weight == nil || *provider.Weight != 5 {
		t.Fatalf("xAI weight = %#v, want 5", provider.Weight)
	}
	if provider.Priority == nil || *provider.Priority != 3 || provider.Prefix != "team-xai" {
		t.Fatalf("xAI priority/prefix = %#v/%q", provider.Priority, provider.Prefix)
	}
	if provider.BaseURL != "https://api.x.ai/v1" || provider.ProxyURL != "http://proxy.example" || provider.Websockets == nil || !*provider.Websockets {
		t.Fatalf("xAI base/proxy/websockets = %q/%q/%#v", provider.BaseURL, provider.ProxyURL, provider.Websockets)
	}
	if provider.DisableCooling == nil || !*provider.DisableCooling {
		t.Fatalf("xAI disable_cooling = %#v, want true", provider.DisableCooling)
	}
	if provider.Disabled == nil || !*provider.Disabled {
		t.Fatalf("xAI disabled = %#v, want true from wildcard", provider.Disabled)
	}
	if len(provider.ExcludedModels) != 1 || provider.ExcludedModels[0] != "grok-legacy" {
		t.Fatalf("xAI excluded models = %#v, want wildcard filtered", provider.ExcludedModels)
	}
	if len(provider.Headers) != 1 || provider.Headers[0].Name != "X-XAI-Test" || provider.Headers[0].Value != "before" {
		t.Fatalf("xAI headers = %#v", provider.Headers)
	}
	if len(provider.Models) != 1 {
		t.Fatalf("xAI models length = %d, want 1", len(provider.Models))
	}
	model := provider.Models[0]
	if model.Name != "grok-4.5" || model.Alias != "grok-latest" || model.DisplayName != "Grok Latest" {
		t.Fatalf("xAI model identity = %#v", model)
	}
	if model.MaxContextLength == nil || *model.MaxContextLength != 131072 || model.ForceMapping == nil || !*model.ForceMapping || model.IsCompat == nil || *model.IsCompat {
		t.Fatalf("xAI model options = %#v", model)
	}
	if model.Thinking["type"] != "enabled" || model.Thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("xAI thinking = %#v", model.Thinking)
	}

	updatePayload := map[string]any{
		"brand":           "xai",
		"identity_hash":   provider.IdentityHash,
		"api_key_hash":    provider.APIKeyHash,
		"api_key":         "",
		"priority":        0,
		"weight":          0,
		"prefix":          "team-xai-next",
		"base_url":        "https://api.x.ai/v1",
		"websockets":      false,
		"proxy_url":       "http://proxy-next.example",
		"headers":         []map[string]any{{"name": "X-XAI-Test", "value": "after"}},
		"excluded_models": []string{"grok-legacy", "grok-preview"},
		"disabled":        false,
		"disable_cooling": false,
		"models": []map[string]any{
			{
				"name":               "grok-4.5",
				"alias":              "grok-stable",
				"display_name":       "Grok Stable",
				"max_context_length": 0,
				"force_mapping":      false,
				"is_compat":          true,
				"thinking":           map[string]any{"type": "adaptive"},
			},
		},
	}
	updateBody := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", updatePayload, cookies, http.StatusOK)
	if strings.Contains(string(updateBody), "xai-secret-key") {
		t.Fatalf("update response leaked xAI API key: %s", string(updateBody))
	}

	fake.mu.Lock()
	items := fake.config["xai-api-key"].([]map[string]any)
	got := items[0]
	if got["api-key"] != "xai-secret-key" || got["weight"] != float64(0) || got["websockets"] != false {
		fake.mu.Unlock()
		t.Fatalf("saved xAI credential fields = %#v", got)
	}
	if got["priority"] != float64(0) || got["prefix"] != "team-xai-next" || got["base-url"] != "https://api.x.ai/v1" || got["proxy-url"] != "http://proxy-next.example" || got["disable-cooling"] != false {
		fake.mu.Unlock()
		t.Fatalf("saved xAI routing fields = %#v", got)
	}
	headers, ok := got["headers"].(map[string]any)
	if !ok || headers["X-XAI-Test"] != "after" {
		fake.mu.Unlock()
		t.Fatalf("saved xAI headers = %#v", got["headers"])
	}
	if _, ok := got["disabled"]; ok {
		fake.mu.Unlock()
		t.Fatalf("saved xAI provider kept provider-level disabled: %#v", got)
	}
	for _, key := range []string{"alpha-search", "alpha_search"} {
		if _, ok := got[key]; ok {
			fake.mu.Unlock()
			t.Fatalf("saved xAI provider kept Codex-only %s: %#v", key, got)
		}
	}
	excluded, ok := got["excluded-models"].([]any)
	if !ok || len(excluded) != 2 || excluded[0] != "grok-legacy" || excluded[1] != "grok-preview" {
		fake.mu.Unlock()
		t.Fatalf("saved xAI excluded-models = %#v", got["excluded-models"])
	}
	future, ok := got["future-option"].(map[string]any)
	if !ok || future["route"] != "keep" {
		fake.mu.Unlock()
		t.Fatalf("saved xAI future option = %#v", got["future-option"])
	}
	models, ok := got["models"].([]any)
	if !ok || len(models) != 1 {
		fake.mu.Unlock()
		t.Fatalf("saved xAI models = %#v", got["models"])
	}
	savedModel, ok := models[0].(map[string]any)
	if !ok {
		fake.mu.Unlock()
		t.Fatalf("saved xAI model = %#v, want object", models[0])
	}
	if savedModel["display-name"] != "Grok Stable" || savedModel["max-context-length"] != float64(0) || savedModel["force-mapping"] != false || savedModel["is-compat"] != true {
		fake.mu.Unlock()
		t.Fatalf("saved xAI model fields = %#v", savedModel)
	}
	for _, key := range []string{"alpha-search", "alpha_search"} {
		if _, ok := savedModel[key]; ok {
			fake.mu.Unlock()
			t.Fatalf("saved xAI model kept Codex-only %s: %#v", key, savedModel)
		}
	}
	futureModel, ok := savedModel["future-model-option"].(map[string]any)
	if !ok || futureModel["mode"] != "keep" {
		fake.mu.Unlock()
		t.Fatalf("saved xAI model lost unknown field: %#v", savedModel)
	}
	thinking, ok := savedModel["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		fake.mu.Unlock()
		t.Fatalf("saved xAI thinking = %#v", savedModel["thinking"])
	}
	fake.mu.Unlock()

	delete(updatePayload, "weight")
	updatePayload["disabled"] = true
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", updatePayload, cookies, nil)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	got = fake.config["xai-api-key"].([]map[string]any)[0]
	if got["weight"] != float64(0) {
		t.Fatalf("omitted xAI weight = %#v, want preserved explicit zero", got["weight"])
	}
	excluded, ok = got["excluded-models"].([]any)
	if !ok || len(excluded) != 3 || excluded[0] != "grok-legacy" || excluded[1] != "grok-preview" || excluded[2] != "*" {
		t.Fatalf("disabled xAI excluded-models = %#v, want wildcard appended", got["excluded-models"])
	}
}

func TestAIProviderXAIIsCompatUpdatePresence(t *testing.T) {
	tests := []struct {
		name                 string
		remoteKey            string
		remoteValue          any
		submit               bool
		submittedValue       any
		wantHyphenatedSet    bool
		wantHyphenatedValue  any
		wantUnderscoredSet   bool
		wantUnderscoredValue any
		wantResponseSet      bool
		wantResponseValue    any
	}{
		{
			name: "omitted preserves absence",
		},
		{
			name:              "omitted preserves hyphenated null",
			remoteKey:         "is-compat",
			remoteValue:       nil,
			wantHyphenatedSet: true,
			wantResponseSet:   true,
		},
		{
			name:                 "omitted preserves underscored false",
			remoteKey:            "is_compat",
			remoteValue:          false,
			wantUnderscoredSet:   true,
			wantUnderscoredValue: false,
			wantResponseSet:      true,
			wantResponseValue:    false,
		},
		{
			name:              "explicit null replaces current value",
			remoteKey:         "is_compat",
			remoteValue:       true,
			submit:            true,
			submittedValue:    nil,
			wantHyphenatedSet: true,
			wantResponseSet:   true,
		},
		{
			name:                "explicit false writes false",
			remoteKey:           "is-compat",
			remoteValue:         nil,
			submit:              true,
			submittedValue:      false,
			wantHyphenatedSet:   true,
			wantHyphenatedValue: false,
			wantResponseSet:     true,
			wantResponseValue:   false,
		},
		{
			name:                "explicit true writes true",
			submit:              true,
			submittedValue:      true,
			wantHyphenatedSet:   true,
			wantHyphenatedValue: true,
			wantResponseSet:     true,
			wantResponseValue:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			remoteModel := map[string]any{
				"name":                "grok-4.5",
				"alias":               "before",
				"future-model-option": "keep",
			}
			if tc.remoteKey != "" {
				remoteModel[tc.remoteKey] = tc.remoteValue
			}
			fake.config["xai-api-key"] = []map[string]any{
				{
					"api-key":  "xai-update-presence-key",
					"base-url": "https://api.x.ai/v1",
					"models":   []map[string]any{remoteModel},
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
			modelPayload := map[string]any{
				"name":  "grok-4.5",
				"alias": "after",
			}
			if tc.submit {
				modelPayload["is_compat"] = tc.submittedValue
			}
			responseBody := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", map[string]any{
				"brand":         "xai",
				"identity_hash": provider.IdentityHash,
				"api_key_hash":  provider.APIKeyHash,
				"api_key":       "",
				"base_url":      "https://api.x.ai/v1",
				"models":        []map[string]any{modelPayload},
				"headers":       []map[string]any{},
			}, cookies, http.StatusOK)

			fake.mu.Lock()
			savedModels, ok := fake.config["xai-api-key"].([]map[string]any)[0]["models"].([]any)
			if !ok || len(savedModels) != 1 {
				fake.mu.Unlock()
				t.Fatalf("saved xAI models = %#v, want one model", fake.config["xai-api-key"])
			}
			savedModel, ok := savedModels[0].(map[string]any)
			fake.mu.Unlock()
			if !ok {
				t.Fatalf("saved xAI model = %#v, want object", savedModels[0])
			}
			if savedModel["alias"] != "after" || savedModel["future-model-option"] != "keep" {
				t.Fatalf("saved unrelated model fields = %#v, want updated alias and preserved unknown field", savedModel)
			}
			assertAIProviderJSONField(t, savedModel, "is-compat", tc.wantHyphenatedSet, tc.wantHyphenatedValue)
			assertAIProviderJSONField(t, savedModel, "is_compat", tc.wantUnderscoredSet, tc.wantUnderscoredValue)

			responseModel := firstAIProviderResponseModel(t, responseBody)
			assertAIProviderJSONField(t, responseModel, "is_compat", tc.wantResponseSet, tc.wantResponseValue)
		})
	}
}

func TestAIProviderXAIIsCompatCreatePresence(t *testing.T) {
	tests := []struct {
		name           string
		submit         bool
		submittedValue any
		wantSet        bool
		wantValue      any
	}{
		{name: "omitted stays absent"},
		{name: "explicit null stays null", submit: true, wantSet: true},
		{name: "explicit false stays false", submit: true, submittedValue: false, wantSet: true, wantValue: false},
		{name: "explicit true stays true", submit: true, submittedValue: true, wantSet: true, wantValue: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()

			modelPayload := map[string]any{"name": "grok-4.5"}
			if tc.submit {
				modelPayload["is_compat"] = tc.submittedValue
			}
			responseBody := requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/xai", map[string]any{
				"brand":    "xai",
				"api_key":  "xai-create-presence-key",
				"base_url": "https://api.x.ai/v1",
				"models":   []map[string]any{modelPayload},
				"headers":  []map[string]any{},
			}, cookies, http.StatusOK)

			fake.mu.Lock()
			savedModels, ok := fake.config["xai-api-key"].([]map[string]any)[0]["models"].([]any)
			if !ok || len(savedModels) != 1 {
				fake.mu.Unlock()
				t.Fatalf("saved xAI models = %#v, want one model", fake.config["xai-api-key"])
			}
			savedModel, ok := savedModels[0].(map[string]any)
			fake.mu.Unlock()
			if !ok {
				t.Fatalf("saved xAI model = %#v, want object", savedModels[0])
			}
			assertAIProviderJSONField(t, savedModel, "is-compat", tc.wantSet, tc.wantValue)
			assertAIProviderJSONField(t, savedModel, "is_compat", false, nil)

			responseModel := firstAIProviderResponseModel(t, responseBody)
			assertAIProviderJSONField(t, responseModel, "is_compat", tc.wantSet, tc.wantValue)
		})
	}
}

func TestAIProviderXAINullableModelFieldsSurviveUnrelatedUpdate(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["xai-api-key"] = []map[string]any{
		{
			"api-key":  "xai-nullable-model-key",
			"base-url": "https://api.x.ai/v1",
			"models": []map[string]any{
				{
					"name":               "grok-nullable",
					"alias":              "before-nullable",
					"display-name":       nil,
					"max-context-length": nil,
					"thinking":           nil,
				},
				{
					"name":     "grok-empty-thinking",
					"alias":    "before-empty",
					"thinking": map[string]any{},
				},
			},
		},
	}

	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	snapshotBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	snapshot := aiProvidersTestResponse{}
	if err := json.Unmarshal(snapshotBody, &snapshot); err != nil {
		t.Fatalf("decode xAI snapshot: %v", err)
	}
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(snapshot.Providers))
	}
	responseModels := aiProviderResponseModelsByName(t, snapshotBody)
	nullableModel := responseModels["grok-nullable"]
	assertAIProviderJSONField(t, nullableModel, "display_name", true, nil)
	assertAIProviderJSONField(t, nullableModel, "max_context_length", true, nil)
	assertAIProviderJSONField(t, nullableModel, "thinking", true, nil)
	emptyThinkingModel := responseModels["grok-empty-thinking"]
	emptyThinking, ok := emptyThinkingModel["thinking"].(map[string]any)
	if !ok || len(emptyThinking) != 0 {
		t.Fatalf("empty thinking response = %#v, want empty object", emptyThinkingModel["thinking"])
	}

	provider := snapshot.Providers[0]
	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", map[string]any{
		"brand":         "xai",
		"identity_hash": provider.IdentityHash,
		"api_key_hash":  provider.APIKeyHash,
		"api_key":       "",
		"base_url":      "https://api.x.ai/v1",
		"models": []map[string]any{
			{"name": "grok-nullable", "alias": "after-nullable"},
			{"name": "grok-empty-thinking", "alias": "after-empty"},
		},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	savedModels, ok := fake.config["xai-api-key"].([]map[string]any)[0]["models"].([]any)
	fake.mu.Unlock()
	if !ok || len(savedModels) != 2 {
		t.Fatalf("saved xAI models = %#v, want two models", fake.config["xai-api-key"])
	}
	savedByName := map[string]map[string]any{}
	for _, saved := range savedModels {
		model, ok := saved.(map[string]any)
		if !ok {
			t.Fatalf("saved xAI model = %#v, want object", saved)
		}
		name, _ := model["name"].(string)
		savedByName[name] = model
	}
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "display-name", true, nil)
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "max-context-length", true, nil)
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "thinking", true, nil)
	if thinking, ok := savedByName["grok-empty-thinking"]["thinking"].(map[string]any); !ok || len(thinking) != 0 {
		t.Fatalf("saved empty thinking = %#v, want empty object", savedByName["grok-empty-thinking"]["thinking"])
	}

	requestJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", map[string]any{
		"brand":         "xai",
		"identity_hash": provider.IdentityHash,
		"api_key_hash":  provider.APIKeyHash,
		"api_key":       "",
		"base_url":      "https://api.x.ai/v1",
		"models": []map[string]any{
			{
				"name":               "grok-nullable",
				"alias":              "after-nullable-explicit",
				"display_name":       nil,
				"max_context_length": nil,
				"thinking":           nil,
			},
			{
				"name":     "grok-empty-thinking",
				"alias":    "after-empty-explicit",
				"thinking": map[string]any{},
			},
		},
		"headers": []map[string]any{},
	}, cookies, nil)

	fake.mu.Lock()
	savedModels, ok = fake.config["xai-api-key"].([]map[string]any)[0]["models"].([]any)
	fake.mu.Unlock()
	if !ok || len(savedModels) != 2 {
		t.Fatalf("saved xAI models after explicit update = %#v, want two models", fake.config["xai-api-key"])
	}
	savedByName = map[string]map[string]any{}
	for _, saved := range savedModels {
		model, ok := saved.(map[string]any)
		if !ok {
			t.Fatalf("saved xAI model after explicit update = %#v, want object", saved)
		}
		name, _ := model["name"].(string)
		savedByName[name] = model
	}
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "display-name", true, nil)
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "max-context-length", true, nil)
	assertAIProviderJSONField(t, savedByName["grok-nullable"], "thinking", true, nil)
	if thinking, ok := savedByName["grok-empty-thinking"]["thinking"].(map[string]any); !ok || len(thinking) != 0 {
		t.Fatalf("saved explicit empty thinking = %#v, want empty object", savedByName["grok-empty-thinking"]["thinking"])
	}
}

func TestAIProviderXAIWebsocketsPresenceSurvivesUnrelatedUpdate(t *testing.T) {
	tests := []struct {
		name        string
		remoteSet   bool
		remoteValue any
		wantSet     bool
		wantValue   any
	}{
		{name: "omitted remains omitted"},
		{name: "null remains null", remoteSet: true, wantSet: true},
		{name: "explicit false remains false", remoteSet: true, remoteValue: false, wantSet: true, wantValue: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			provider := map[string]any{
				"api-key":  "xai-websockets-key",
				"base-url": "https://api.x.ai/v1",
				"models":   []map[string]any{{"name": "grok-websockets"}},
			}
			if tc.remoteSet {
				provider["websockets"] = tc.remoteValue
			}
			fake.config["xai-api-key"] = []map[string]any{provider}

			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			snapshotBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
			responseProviders := aiProviderResponseProviders(t, snapshotBody)
			assertAIProviderJSONField(t, responseProviders[0], "websockets", tc.wantSet, tc.wantValue)
			snapshot := aiProvidersTestResponse{}
			if err := json.Unmarshal(snapshotBody, &snapshot); err != nil {
				t.Fatalf("decode xAI snapshot: %v", err)
			}
			updatePayload := map[string]any{
				"brand":         "xai",
				"identity_hash": snapshot.Providers[0].IdentityHash,
				"api_key_hash":  snapshot.Providers[0].APIKeyHash,
				"api_key":       "",
				"base_url":      "https://api.x.ai/v1",
				"priority":      3,
				"models":        []map[string]any{{"name": "grok-websockets"}},
				"headers":       []map[string]any{},
			}
			if tc.remoteSet {
				updatePayload["websockets"] = tc.remoteValue
			}
			requestJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", updatePayload, cookies, nil)

			fake.mu.Lock()
			saved := fake.config["xai-api-key"].([]map[string]any)[0]
			fake.mu.Unlock()
			assertAIProviderJSONField(t, saved, "websockets", tc.wantSet, tc.wantValue)
		})
	}
}

func TestAIProviderXAIWeightPresenceSurvivesUnrelatedUpdate(t *testing.T) {
	tests := []struct {
		name        string
		remoteSet   bool
		remoteValue any
		wantSet     bool
		wantValue   any
	}{
		{name: "omitted remains omitted"},
		{name: "null remains null", remoteSet: true, wantSet: true},
		{name: "explicit zero remains zero", remoteSet: true, remoteValue: 0, wantSet: true, wantValue: float64(0)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			provider := map[string]any{
				"api-key":  "xai-weight-key",
				"base-url": "https://api.x.ai/v1",
				"models":   []map[string]any{{"name": "grok-weight"}},
			}
			if tc.remoteSet {
				provider["weight"] = tc.remoteValue
			}
			fake.config["xai-api-key"] = []map[string]any{provider}

			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			snapshotBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
			responseProviders := aiProviderResponseProviders(t, snapshotBody)
			assertAIProviderJSONField(t, responseProviders[0], "weight", tc.wantSet, tc.wantValue)
			snapshot := aiProvidersTestResponse{}
			if err := json.Unmarshal(snapshotBody, &snapshot); err != nil {
				t.Fatalf("decode xAI snapshot: %v", err)
			}
			updatePayload := map[string]any{
				"brand":         "xai",
				"identity_hash": snapshot.Providers[0].IdentityHash,
				"api_key_hash":  snapshot.Providers[0].APIKeyHash,
				"api_key":       "",
				"base_url":      "https://api.x.ai/v1",
				"priority":      3,
				"models":        []map[string]any{{"name": "grok-weight"}},
				"headers":       []map[string]any{},
			}
			if tc.remoteSet {
				updatePayload["weight"] = tc.remoteValue
			}
			requestJSON(t, handler, http.MethodPut, "/api/ai-providers/xai/0", updatePayload, cookies, nil)

			fake.mu.Lock()
			saved := fake.config["xai-api-key"].([]map[string]any)[0]
			fake.mu.Unlock()
			assertAIProviderJSONField(t, saved, "weight", tc.wantSet, tc.wantValue)
		})
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

func TestAIProviderSaveRequiresBaseURLForCodexOpenAICompatibleVertexAndXAI(t *testing.T) {
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
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/ai-providers/xai", map[string]any{
		"brand":   "xai",
		"api_key": "xai-secret-key",
		"models":  []map[string]any{},
		"headers": []map[string]any{},
	}, cookies, http.StatusUnprocessableEntity)
	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/ai-providers/xai", map[string]any{
		"brand":    "xai",
		"api_key":  "xai-secret-key",
		"base_url": "https://api.x.ai/v1",
		"weight":   1000001,
		"models":   []map[string]any{},
		"headers":  []map[string]any{},
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
	if got := len(fake.config["xai-api-key"].([]map[string]any)); got != 0 {
		t.Fatalf("xAI providers length = %d, want 0", got)
	}
}

func TestAIProviderXAICreateAndDelete(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	createBody := requestRawJSON(t, handler, http.MethodPost, "/api/ai-providers/xai", map[string]any{
		"brand":           "xai",
		"api_key":         "created-xai-secret",
		"base_url":        "https://api.x.ai/v1",
		"weight":          0,
		"models":          []map[string]any{{"name": "grok-4.5"}},
		"headers":         []map[string]any{},
		"excluded_models": []string{},
	}, cookies, http.StatusOK)
	if strings.Contains(string(createBody), "created-xai-secret") {
		t.Fatalf("create response leaked xAI API key: %s", string(createBody))
	}
	created := aiProvidersTestResponse{}
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatalf("decode created xAI snapshot: %v", err)
	}
	if len(created.Providers) != 1 || created.Providers[0].Brand != "xai" {
		t.Fatalf("created xAI providers = %#v", created.Providers)
	}
	if created.Providers[0].Weight == nil || *created.Providers[0].Weight != 0 {
		t.Fatalf("created xAI weight = %#v, want explicit zero", created.Providers[0].Weight)
	}

	deletePath := fmt.Sprintf("/api/ai-providers/xai/0?identity_hash=%s", created.Providers[0].IdentityHash)
	deleted := aiProvidersTestResponse{}
	requestJSON(t, handler, http.MethodDelete, deletePath, nil, cookies, &deleted)
	if len(deleted.Providers) != 0 || deleted.Summary.Total != 0 || deleted.Summary.XAI != 0 {
		t.Fatalf("deleted xAI snapshot = %#v", deleted)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := len(fake.config["xai-api-key"].([]map[string]any)); got != 0 {
		t.Fatalf("xAI providers length after delete = %d, want 0", got)
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

func TestAIProviderXAIActionsUseModelsAndResponses(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	provider := map[string]any{
		"brand":      "xai",
		"api_key":    "unsaved-xai-secret",
		"auth_index": "attacker-auth-index",
		"base_url":   "https://api.x.ai/v1",
		"models":     []map[string]any{{"name": "grok-4.5"}},
		"headers":    []map[string]any{{"name": "X-XAI-Test", "value": "yes"}},
	}
	discovery := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/discover-models", map[string]any{
		"brand":    "xai",
		"provider": provider,
	}, cookies, &discovery)
	if !discovery.OK || len(discovery.Models) != 1 || discovery.Models[0].Name != "gpt-test" {
		t.Fatalf("xAI discovery response = %#v, want gpt-test", discovery)
	}
	connectivity := aiProviderActionTestResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/ai-providers/test", map[string]any{
		"brand":    "xai",
		"provider": provider,
		"model":    "grok-4.5",
		"message":  "ping xai",
	}, cookies, &connectivity)
	if !connectivity.OK || connectivity.Reply != "responses ok" {
		t.Fatalf("xAI connectivity response = %#v, want Responses reply", connectivity)
	}
	encoded, err := json.Marshal([]aiProviderActionTestResponse{discovery, connectivity})
	if err != nil {
		t.Fatalf("marshal xAI action responses: %v", err)
	}
	if strings.Contains(string(encoded), "unsaved-xai-secret") {
		t.Fatalf("xAI action response leaked submitted key: %s", string(encoded))
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apiCallBodies) != 2 {
		t.Fatalf("xAI api-call count = %d, want 2", len(fake.apiCallBodies))
	}
	discoveryBody := fake.apiCallBodies[0]
	if discoveryBody["method"] != http.MethodGet || discoveryBody["url"] != "https://api.x.ai/v1/models" {
		t.Fatalf("xAI discovery request = %#v, want /v1/models", discoveryBody)
	}
	testBody := fake.apiCallBodies[1]
	if testBody["method"] != http.MethodPost || testBody["url"] != "https://api.x.ai/v1/responses" {
		t.Fatalf("xAI connectivity request = %#v, want /v1/responses", testBody)
	}
	for index, body := range fake.apiCallBodies {
		if body["auth_index"] != nil {
			t.Fatalf("xAI api-call %d auth_index = %#v, want omitted for unsaved key", index, body["auth_index"])
		}
		header, ok := body["header"].(map[string]any)
		if !ok || header["Authorization"] != "Bearer unsaved-xai-secret" || header["X-XAI-Test"] != "yes" {
			t.Fatalf("xAI api-call %d headers = %#v", index, body["header"])
		}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(testBody["data"].(string)), &data); err != nil {
		t.Fatalf("decode xAI Responses payload: %v", err)
	}
	if data["model"] != "grok-4.5" || data["input"] != "ping xai" {
		t.Fatalf("xAI Responses payload = %#v", data)
	}
	if _, ok := data["messages"]; ok {
		t.Fatalf("xAI Responses payload = %#v, want no chat messages", data)
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

func TestAIProvidersReorderPersistsRemoteOrderAndPreservesRawFields(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{
			"api-key":          "gemini-first-secret",
			"name":             "first",
			"base-url":         "https://first.example",
			"models":           []map[string]any{{"name": "first-model"}},
			"unknown":          map[string]any{"keep": true},
			"unknown-large-id": json.Number("9007199254740993"),
			"unknown-nested":   map[string]any{"timestamp": json.Number("9007199254740993")},
			"priority":         7,
			"headers":          map[string]any{"X-First": "one"},
		},
		{
			"api-key":  "gemini-second-secret",
			"name":     "second",
			"base-url": "https://second.example",
			"models":   []map[string]any{{"name": "second-model"}},
			"unknown":  "preserve-second",
			"priority": 3,
		},
	}
	fake.config["codex-api-key"] = []map[string]any{{
		"api-key":  "codex-secret",
		"base-url": "https://codex.example",
		"unknown":  "untouched-codex",
	}}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	gemini := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] == "gemini" {
			gemini = append(gemini, provider)
		}
	}
	if len(gemini) != 2 {
		t.Fatalf("initial Gemini providers = %#v, want 2", gemini)
	}
	order := make([]map[string]any, 0, len(gemini))
	for index := len(gemini) - 1; index >= 0; index-- {
		provider := gemini[index]
		reference := map[string]any{"index": provider["index"], "identity_hash": provider["identity_hash"]}
		for _, key := range []string{"api_key_hash", "name", "base_url"} {
			if value, ok := provider[key]; ok && value != nil {
				reference[key] = value
			}
		}
		order = append(order, reference)
	}
	responseBody := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": order,
	}, cookies, http.StatusOK)

	fake.mu.Lock()
	remoteList, ok := fake.config["gemini-api-key"].([]map[string]any)
	if !ok || len(remoteList) != 2 {
		fake.mu.Unlock()
		t.Fatalf("remote Gemini list after reorder = %#v, want two objects", fake.config["gemini-api-key"])
	}
	if got := remoteList[0]["api-key"]; got != "gemini-second-secret" {
		t.Fatalf("remote first API key = %#v, want second secret", got)
	}
	if got := remoteList[0]["unknown"].(string); got != "preserve-second" {
		t.Fatalf("remote first unknown field = %#v, want preserved", got)
	}
	if got := remoteList[1]["api-key"]; got != "gemini-first-secret" {
		t.Fatalf("remote second API key = %#v, want first secret", got)
	}
	if got := remoteList[1]["unknown"].(map[string]any)["keep"]; got != true {
		t.Fatalf("remote second nested unknown field = %#v, want true", got)
	}
	if len(fake.providerPutBodies) != 1 {
		t.Fatalf("provider PUT bodies = %d, want 1", len(fake.providerPutBodies))
	}
	providerPutBody := append(json.RawMessage(nil), fake.providerPutBodies[0]...)
	codexList := fake.config["codex-api-key"].([]map[string]any)
	if got := codexList[0]["unknown"]; got != "untouched-codex" {
		t.Fatalf("other brand unknown field = %#v, want untouched", got)
	}
	fake.mu.Unlock()

	var persisted []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(providerPutBody))
	decoder.UseNumber()
	if err := decoder.Decode(&persisted); err != nil {
		t.Fatalf("decode reordered provider PUT body: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("reordered provider PUT entries = %#v, want two entries", persisted)
	}
	if got, ok := persisted[1]["unknown-large-id"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("reordered unknown large ID = %#v, want 9007199254740993", persisted[1]["unknown-large-id"])
	}
	nested, ok := persisted[1]["unknown-nested"].(map[string]any)
	if !ok {
		t.Fatalf("reordered nested unknown field = %#v, want object", persisted[1]["unknown-nested"])
	}
	if got, ok := nested["timestamp"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("reordered nested unknown large ID = %#v, want 9007199254740993", nested["timestamp"])
	}

	var response struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode reorder response: %v", err)
	}
	orderedGemini := make([]map[string]any, 0, 2)
	for _, provider := range response.Providers {
		if provider["brand"] == "gemini" {
			orderedGemini = append(orderedGemini, provider)
		}
	}
	if len(orderedGemini) != 2 || orderedGemini[0]["name"] != "second" || orderedGemini[1]["name"] != "first" {
		t.Fatalf("reorder response Gemini order = %#v, want second/first", orderedGemini)
	}
	for index, provider := range orderedGemini {
		if provider["index"] != float64(index) {
			t.Fatalf("reorder response provider %d index = %#v, want %d", index, provider["index"], index)
		}
	}
	for _, secret := range []string{"gemini-first-secret", "gemini-second-secret", "test-management-key"} {
		if strings.Contains(string(responseBody), secret) {
			t.Fatalf("reorder response leaked secret %q: %s", secret, responseBody)
		}
	}
}

func TestAIProvidersReorderNoOpSkipsRemotePut(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "gemini-first-secret", "base-url": "https://first.example"},
		{"api-key": "gemini-second-secret", "base-url": "https://second.example"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	order := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		reference := map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"base_url":      provider["base_url"],
		}
		if value, ok := provider["api_key_hash"]; ok && value != nil {
			reference["api_key_hash"] = value
		}
		order = append(order, reference)
	}
	if len(order) != 2 {
		t.Fatalf("initial canonical order = %#v, want 2 Gemini providers", order)
	}

	fake.mu.Lock()
	before := fake.providerPutCount
	fake.mu.Unlock()
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{"order": order}, cookies, http.StatusOK)
	fake.mu.Lock()
	after := fake.providerPutCount
	fake.mu.Unlock()
	if after != before {
		t.Fatalf("no-op reorder provider PUT count = %d, want unchanged at %d", after, before)
	}
}

func TestAIProvidersReorderPropagatesRemotePutFailure(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "gemini-first-secret", "name": "first", "base-url": "https://first.example"},
		{"api-key": "gemini-second-secret", "name": "second", "base-url": "https://second.example"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	order := make([]map[string]any, 0, 2)
	for index := len(initial.Providers) - 1; index >= 0; index-- {
		provider := initial.Providers[index]
		if provider["brand"] != "gemini" {
			continue
		}
		order = append(order, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"api_key_hash":  provider["api_key_hash"],
			"name":          provider["name"],
			"base_url":      provider["base_url"],
		})
	}
	if len(order) != 2 {
		t.Fatalf("initial reorder references = %#v, want two Gemini providers", order)
	}

	fake.mu.Lock()
	fake.providerPutStatus = http.StatusBadGateway
	before := fake.config["gemini-api-key"].([]map[string]any)
	fake.mu.Unlock()
	responseBody := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": order,
	}, cookies, http.StatusUnprocessableEntity)
	if !strings.Contains(string(responseBody), "HTTP 502") {
		t.Fatalf("reorder failure response = %s, want upstream status", responseBody)
	}
	for _, secret := range []string{"gemini-first-secret", "gemini-second-secret", "test-management-key"} {
		if strings.Contains(string(responseBody), secret) {
			t.Fatalf("reorder failure response leaked secret %q: %s", secret, responseBody)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	after := fake.config["gemini-api-key"].([]map[string]any)
	if len(after) != len(before) || after[0]["name"] != "first" || after[1]["name"] != "second" {
		t.Fatalf("remote list after failed reorder = %#v, want unchanged first/second", after)
	}
	if fake.providerPutCount == 0 {
		t.Fatalf("provider PUT count = %d, want attempted upstream write", fake.providerPutCount)
	}
}

func TestAIProvidersReorderReportsCommittedWriteWhenSnapshotRefreshFails(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "gemini-first-secret", "name": "first", "base-url": "https://first.example"},
		{"api-key": "gemini-second-secret", "name": "second", "base-url": "https://second.example"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()

	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		refs = append(refs, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"api_key_hash":  provider["api_key_hash"],
			"name":          provider["name"],
			"base_url":      provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("initial reorder references = %#v, want two Gemini providers", refs)
	}

	fake.mu.Lock()
	fake.providerGetStatus = http.StatusBadGateway
	fake.mu.Unlock()
	responseBody := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{refs[1], refs[0]},
	}, cookies, http.StatusConflict)
	var failure struct {
		Detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(responseBody, &failure); err != nil {
		t.Fatalf("decode committed reorder response: %v", err)
	}
	if failure.Detail.Code != "provider_order_committed_refresh_required" {
		t.Fatalf("committed reorder error code = %q, want provider_order_committed_refresh_required", failure.Detail.Code)
	}
	if !strings.Contains(failure.Detail.Message, "已写入远端") || !strings.Contains(failure.Detail.Message, "刷新") {
		t.Fatalf("committed reorder message = %q, want committed refresh guidance", failure.Detail.Message)
	}
	for _, secret := range []string{"gemini-first-secret", "gemini-second-secret", "test-management-key"} {
		if strings.Contains(string(responseBody), secret) {
			t.Fatalf("committed reorder response leaked secret %q: %s", secret, responseBody)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if len(items) != 2 || items[0]["name"] != "second" || items[1]["name"] != "first" {
		t.Fatalf("remote order after committed refresh failure = %#v, want second/first", items)
	}
	if fake.providerPutCount != 1 {
		t.Fatalf("provider PUT count = %d, want one committed write", fake.providerPutCount)
	}
}

func TestAIProvidersReorderRejectsStaleOrAmbiguousOrder(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "gemini-first-secret", "base-url": "https://first.example"},
		{"api-key": "gemini-second-secret", "base-url": "https://second.example"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		refs = append(refs, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"api_key_hash":  provider["api_key_hash"],
			"base_url":      provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("initial order references = %#v, want 2", refs)
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{
			{"identity_hash": refs[0]["identity_hash"]},
			refs[1],
		},
	}, cookies, http.StatusUnprocessableEntity)

	// A concurrent reorder changes the selector at each original index. The
	// stale request must fail without writing either provider.
	fake.mu.Lock()
	current := fake.config["gemini-api-key"].([]map[string]any)
	fake.config["gemini-api-key"] = []map[string]any{current[1], current[0]}
	fake.mu.Unlock()
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{"order": refs}, cookies, http.StatusConflict)
	fake.mu.Lock()
	current = fake.config["gemini-api-key"].([]map[string]any)
	if current[0]["api-key"] != "gemini-second-secret" || current[1]["api-key"] != "gemini-first-secret" {
		t.Fatalf("stale reorder changed remote list = %#v", current)
	}
	fake.mu.Unlock()

	// Duplicate references are malformed even when their selectors otherwise
	// match, and must not reach the upstream PUT.
	fake.mu.Lock()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "gemini-first-secret", "base-url": "https://first.example"},
		{"api-key": "gemini-second-secret", "base-url": "https://second.example"},
	}
	fake.mu.Unlock()
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{
			{"identity_hash": refs[0]["identity_hash"], "api_key_hash": refs[0]["api_key_hash"], "base_url": refs[0]["base_url"]},
			refs[1],
		},
	}, cookies, http.StatusUnprocessableEntity)
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{refs[0], refs[0]},
	}, cookies, http.StatusUnprocessableEntity)

	// Optional secondary selectors must not let duplicate remote records evade
	// ambiguity detection by submitting only the shared identity hash.
	fake.mu.Lock()
	fake.config["gemini-api-key"] = []map[string]any{
		{"api-key": "shared-secret", "base-url": "https://same.example"},
		{"api-key": "shared-secret", "base-url": "https://same.example"},
	}
	fake.mu.Unlock()
	duplicateBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var duplicateSnapshot struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(duplicateBody, &duplicateSnapshot); err != nil {
		t.Fatalf("decode duplicate snapshot: %v", err)
	}
	duplicateRefs := make([]map[string]any, 0, 2)
	for _, provider := range duplicateSnapshot.Providers {
		if provider["brand"] == "gemini" {
			duplicateRefs = append(duplicateRefs, map[string]any{
				"index":         provider["index"],
				"identity_hash": provider["identity_hash"],
			})
		}
	}
	if len(duplicateRefs) != 2 {
		t.Fatalf("duplicate order references = %#v, want 2", duplicateRefs)
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{duplicateRefs[1], duplicateRefs[0]},
	}, cookies, http.StatusConflict)
}

func TestAIProvidersReorderAllowsUniqueFallbackIdentity(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"name": "first", "base-url": "https://first.example", "marker": "first"},
		{"name": "second", "base-url": "https://second.example", "marker": "second"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		refs = append(refs, map[string]any{
			"index":    provider["index"],
			"name":     provider["name"],
			"base_url": provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("fallback order references = %#v, want 2", refs)
	}
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{refs[1], refs[0]},
	}, cookies, http.StatusOK)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if len(items) != 2 || items[0]["marker"] != "second" || items[1]["marker"] != "first" {
		t.Fatalf("fallback remote order = %#v, want second/first", items)
	}
}

func TestAIProvidersReorderRejectsAmbiguousFallbackIdentity(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"base-url": "https://same.example", "marker": "first"},
		{"base-url": "https://same.example", "marker": "second"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		refs = append(refs, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"base_url":      provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("ambiguous fallback order references = %#v, want 2", refs)
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{refs[1], refs[0]},
	}, cookies, http.StatusConflict)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if len(items) != 2 || items[0]["marker"] != "first" || items[1]["marker"] != "second" {
		t.Fatalf("ambiguous fallback remote order = %#v, want first/second", items)
	}
}

func TestAIProvidersReorderRequiresUniqueSubmittedFallbackSelector(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["gemini-api-key"] = []map[string]any{
		{"name": "first", "base-url": "https://shared.example", "marker": "first"},
		{"name": "second", "base-url": "https://shared.example", "marker": "second"},
	}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	initialBody := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, http.StatusOK)
	var initial struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(initialBody, &initial); err != nil {
		t.Fatalf("decode initial provider snapshot: %v", err)
	}
	refs := make([]map[string]any, 0, 2)
	for _, provider := range initial.Providers {
		if provider["brand"] != "gemini" {
			continue
		}
		// The fallback identity is index-derived. Supplying only the shared
		// base URL must not let that index make an ambiguous selector appear
		// unique.
		refs = append(refs, map[string]any{
			"index":         provider["index"],
			"identity_hash": provider["identity_hash"],
			"base_url":      provider["base_url"],
		})
	}
	if len(refs) != 2 {
		t.Fatalf("fallback selector references = %#v, want two", refs)
	}
	requestJSONExpectStatus(t, handler, http.MethodPut, "/api/ai-providers/gemini/order", map[string]any{
		"order": []map[string]any{refs[1], refs[0]},
	}, cookies, http.StatusConflict)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	items := fake.config["gemini-api-key"].([]map[string]any)
	if len(items) != 2 || items[0]["marker"] != "first" || items[1]["marker"] != "second" {
		t.Fatalf("non-unique fallback selector changed remote order = %#v", items)
	}
}

func firstAIProviderResponseModel(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var response struct {
		Providers []struct {
			Models []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode AI provider response: %v", err)
	}
	if len(response.Providers) != 1 || len(response.Providers[0].Models) != 1 {
		t.Fatalf("AI provider response models = %#v, want one provider with one model", response.Providers)
	}
	return response.Providers[0].Models[0]
}

func aiProviderResponseProviders(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var response struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode AI provider response: %v", err)
	}
	if len(response.Providers) == 0 {
		t.Fatalf("AI provider response providers = %#v, want at least one provider", response.Providers)
	}
	return response.Providers
}

func aiProviderResponseModelsByName(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	providers := aiProviderResponseProviders(t, body)
	models, ok := providers[0]["models"].([]any)
	if !ok {
		t.Fatalf("AI provider response models = %#v, want array", providers[0]["models"])
	}
	result := make(map[string]map[string]any, len(models))
	for _, value := range models {
		model, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("AI provider response model = %#v, want object", value)
		}
		name, ok := model["name"].(string)
		if !ok {
			t.Fatalf("AI provider response model name = %#v, want string", model["name"])
		}
		result[name] = model
	}
	return result
}

func assertAIProviderJSONField(t *testing.T, object map[string]any, key string, wantSet bool, wantValue any) {
	t.Helper()
	got, set := object[key]
	if set != wantSet {
		t.Fatalf("%s presence = %v, want %v in %#v", key, set, wantSet, object)
	}
	if wantSet && got != wantValue {
		t.Fatalf("%s = %#v, want %#v in %#v", key, got, wantValue, object)
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
