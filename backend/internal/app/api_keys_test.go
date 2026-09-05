package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

type apiKeyCreateResponse struct {
	APIKey     string `json:"api_key"`
	APIKeyHash string `json:"api_key_hash"`
}

func TestListAPIKeysReturnsEmptyArrayForFreshAccount(t *testing.T) {
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

	var keys []apiKeyCreateResponse
	requestJSON(t, handler, http.MethodGet, "/api/api-keys", nil, cookies, &keys)
	if keys == nil {
		t.Fatal("fresh API key list decoded as nil; want empty JSON array")
	}
	if len(keys) != 0 {
		t.Fatalf("fresh API key list length = %d, want 0", len(keys))
	}
}

func TestAccountModelRequestGuideUsesConfiguredURL(t *testing.T) {
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
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     "http://127.0.0.1:8317",
		"model_request_url": "http://models.example.local/proxy",
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	var guide struct {
		ModelRequestURL    string `json:"model_request_url"`
		OpenAIBaseURL      string `json:"openai_base_url"`
		ChatCompletionsURL string `json:"chat_completions_url"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/account/model-request", nil, cookies, &guide)
	if guide.ModelRequestURL != "http://models.example.local/proxy" {
		t.Fatalf("model_request_url = %q", guide.ModelRequestURL)
	}
	if guide.OpenAIBaseURL != "http://models.example.local/proxy/v1" {
		t.Fatalf("openai_base_url = %q", guide.OpenAIBaseURL)
	}
	if guide.ChatCompletionsURL != "http://models.example.local/proxy/v1/chat/completions" {
		t.Fatalf("chat_completions_url = %q", guide.ChatCompletionsURL)
	}

	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"model_request_url": "http://models.example.local/v1",
	}, cookies, nil)
	requestJSON(t, handler, http.MethodGet, "/api/account/model-request", nil, cookies, &guide)
	if guide.OpenAIBaseURL != "http://models.example.local/v1" {
		t.Fatalf("openai_base_url with existing /v1 = %q", guide.OpenAIBaseURL)
	}
	if guide.ChatCompletionsURL != "http://models.example.local/v1/chat/completions" {
		t.Fatalf("chat_completions_url with existing /v1 = %q", guide.ChatCompletionsURL)
	}
}

func TestAccountModelRequestTestUsesCurrentUserAPIKey(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	var expectedAuth string
	remoteKeys := []string{}
	chatCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch:
			var payload struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteKeys = append(remoteKeys, payload.New)
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
			chatCalls++
			if got := r.Header.Get("Authorization"); got != expectedAuth {
				t.Fatalf("Authorization = %q, want %q", got, expectedAuth)
			}
			var payload struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				Stream bool `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Model != "gpt-test" {
				t.Fatalf("model = %q, want gpt-test", payload.Model)
			}
			if payload.Stream {
				t.Fatal("stream = true, want false")
			}
			if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "ping" {
				t.Fatalf("messages = %#v, want one user ping message", payload.Messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": "pong"}},
				},
				"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
			})
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
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"model_request_url": cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, &created)
	expectedAuth = "Bearer " + created.APIKey

	var result struct {
		Endpoint   string         `json:"endpoint"`
		Model      string         `json:"model"`
		Reply      string         `json:"reply"`
		StatusCode int            `json:"status_code"`
		DurationMS int64          `json:"duration_ms"`
		Usage      map[string]any `json:"usage"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/account/model-request/test", map[string]any{
		"api_key_hash": created.APIKeyHash,
		"model":        "gpt-test",
		"message":      "ping",
	}, cookies, &result)

	if result.Endpoint != "chat_completions" || result.Model != "gpt-test" || result.Reply != "pong" || result.StatusCode != http.StatusOK {
		t.Fatalf("test response = %#v, want model/reply/status", result)
	}
	if result.DurationMS < 0 {
		t.Fatalf("duration_ms = %d, want non-negative", result.DurationMS)
	}
	if got := result.Usage["total_tokens"]; got != float64(3) {
		t.Fatalf("usage total_tokens = %#v, want 3", got)
	}
	if chatCalls != 1 {
		t.Fatalf("chat calls = %d, want 1", chatCalls)
	}
}

func TestAccountModelRequestTestSupportsResponsesEndpoint(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	var expectedAuth string
	remoteKeys := []string{}
	responsesCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch:
			var payload struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteKeys = append(remoteKeys, payload.New)
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case r.URL.Path == "/v1/responses" && r.Method == http.MethodPost:
			responsesCalls++
			if got := r.Header.Get("Authorization"); got != expectedAuth {
				t.Fatalf("Authorization = %q, want %q", got, expectedAuth)
			}
			var payload struct {
				Model  string `json:"model"`
				Input  string `json:"input"`
				Stream bool   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Model != "gpt-response-test" {
				t.Fatalf("model = %q, want gpt-response-test", payload.Model)
			}
			if payload.Input != "ping responses" {
				t.Fatalf("input = %q, want ping responses", payload.Input)
			}
			if payload.Stream {
				t.Fatal("stream = true, want false")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output_text": "responses pong",
				"usage":       map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3},
			})
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
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"model_request_url": cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, &created)
	expectedAuth = "Bearer " + created.APIKey

	var result struct {
		Endpoint   string         `json:"endpoint"`
		Model      string         `json:"model"`
		Reply      string         `json:"reply"`
		StatusCode int            `json:"status_code"`
		Usage      map[string]any `json:"usage"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/account/model-request/test", map[string]any{
		"api_key_hash": created.APIKeyHash,
		"endpoint":     "responses",
		"model":        "gpt-response-test",
		"message":      "ping responses",
	}, cookies, &result)

	if result.Endpoint != "responses" || result.Model != "gpt-response-test" || result.Reply != "responses pong" || result.StatusCode != http.StatusOK {
		t.Fatalf("test response = %#v, want responses endpoint/model/reply/status", result)
	}
	if got := result.Usage["total_tokens"]; got != float64(3) {
		t.Fatalf("usage total_tokens = %#v, want 3", got)
	}
	if responsesCalls != 1 {
		t.Fatalf("responses calls = %d, want 1", responsesCalls)
	}
}

func TestAccountModelRequestTestSupportsClaudeMessagesEndpoint(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	var expectedAPIKey string
	remoteKeys := []string{}
	claudeCalls := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch:
			var payload struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			remoteKeys = append(remoteKeys, payload.New)
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case r.URL.Path == "/v1/messages" && r.Method == http.MethodPost:
			claudeCalls++
			if got := r.Header.Get("x-api-key"); got != expectedAPIKey {
				t.Fatalf("x-api-key = %q, want %q", got, expectedAPIKey)
			}
			if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
				t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
			}
			var payload struct {
				Model    string `json:"model"`
				MaxToken int    `json:"max_tokens"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Model != "claude-test" {
				t.Fatalf("model = %q, want claude-test", payload.Model)
			}
			if payload.MaxToken != 1024 {
				t.Fatalf("max_tokens = %d, want 1024", payload.MaxToken)
			}
			if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "ping claude" {
				t.Fatalf("messages = %#v, want one user ping claude message", payload.Messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "claude pong"},
				},
				"usage": map[string]any{"input_tokens": 2, "output_tokens": 1},
			})
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
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"model_request_url": cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, &created)
	expectedAPIKey = created.APIKey

	var result struct {
		Endpoint   string         `json:"endpoint"`
		Model      string         `json:"model"`
		Reply      string         `json:"reply"`
		StatusCode int            `json:"status_code"`
		Usage      map[string]any `json:"usage"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/account/model-request/test", map[string]any{
		"api_key_hash": created.APIKeyHash,
		"endpoint":     "claude_messages",
		"model":        "claude-test",
		"message":      "ping claude",
	}, cookies, &result)

	if result.Endpoint != "claude_messages" || result.Model != "claude-test" || result.Reply != "claude pong" || result.StatusCode != http.StatusOK {
		t.Fatalf("test response = %#v, want claude endpoint/model/reply/status", result)
	}
	if got := result.Usage["output_tokens"]; got != float64(1) {
		t.Fatalf("usage output_tokens = %#v, want 1", got)
	}
	if claudeCalls != 1 {
		t.Fatalf("claude calls = %d, want 1", claudeCalls)
	}
}

func TestAccountModelRequestTestRejectsOtherUserAPIKey(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch {
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": []string{"ok"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer cpa.Close()

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	adminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"model_request_url": cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, adminCookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "Admin",
	}, adminCookies, &created)
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
	requestJSON(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{
		"current_password": "member-password",
		"password":         "member-new-password",
	}, memberCookies, nil)

	requestJSONExpectStatus(t, handler, http.MethodPost, "/api/account/model-request/test", map[string]any{
		"api_key_hash": created.APIKeyHash,
		"model":        "gpt-test",
		"message":      "ping",
	}, memberCookies, http.StatusNotFound)
}

func TestCreateAPIKeyWithoutCPAConfigGuidesToSettings(t *testing.T) {
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

	body, err := json.Marshal(map[string]any{"description": "VSCode"})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/api-keys", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /api/api-keys returned %d: %s", recorder.Code, recorder.Body.String())
	}
	message := recorder.Body.String()
	if !strings.Contains(message, "CPA 配置未完成") ||
		!strings.Contains(message, "系统设置") ||
		!strings.Contains(message, "CLIProxyAPI 地址和管理密钥") {
		t.Fatalf("missing actionable CPA config guidance in response: %s", message)
	}
}

func TestCreateAPIKeyUsesPatchAppendWhenRemoteListIsEmpty(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	remoteKeys := []string{}
	getCalls := 0
	putCalls := 0
	patchCalls := 0
	badPatchPayload := ""
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPatch:
			patchCalls++
			var payload struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Old == "" || payload.New == "" || payload.Old != payload.New {
				badPatchPayload = "old/new key must be the same non-empty value"
				http.Error(w, badPatchPayload, http.StatusBadRequest)
				return
			}
			replaced := false
			for index, key := range remoteKeys {
				if key == payload.Old {
					remoteKeys[index] = payload.New
					replaced = true
					break
				}
			}
			if !replaced {
				remoteKeys = append(remoteKeys, payload.New)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case http.MethodGet:
			getCalls++
			http.Error(w, "GET should not be needed to create the first API key", http.StatusInternalServerError)
		case http.MethodPut:
			putCalls++
			http.Error(w, "PUT should not be needed to create the first API key", http.StatusInternalServerError)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()

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
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, &created)

	if created.APIKey == "" || created.APIKeyHash == "" {
		t.Fatalf("created API key response is missing key fields: %#v", created)
	}
	if len(remoteKeys) != 1 || remoteKeys[0] != created.APIKey {
		t.Fatalf("remote keys = %#v, want the created key %#v", remoteKeys, created.APIKey)
	}
	if badPatchPayload != "" {
		t.Fatal(badPatchPayload)
	}
	if patchCalls != 1 || getCalls != 0 || putCalls != 0 {
		t.Fatalf("remote call counts patch/get/put = %d/%d/%d, want 1/0/0", patchCalls, getCalls, putCalls)
	}
}
