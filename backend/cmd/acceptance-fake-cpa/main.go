package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Loopback CLIProxyAPI management test double for acceptance runs (see
// .trellis/spec/guides/acceptance-seed-data.md). Serves three brands with three
// channels each so CPA-Helper can list providers and validate channel prices.
// Contains no real keys; any Bearer token is accepted.

var (
	mu     sync.Mutex
	config = map[string]any{
		"gemini-api-key": []map[string]any{},
		"claude-api-key": []map[string]any{
			claudeEntry("sk-ant-test-primary-0001cafe", "claude-primary", "https://api.anthropic.com"),
			claudeEntry("sk-ant-test-backup-0002beef", "claude-backup", "https://api.anthropic.com"),
			claudeEntry("sk-ant-test-relay-0003feed", "claude-relay", "https://anthropic-relay.example.com"),
		},
		"codex-api-key": []map[string]any{
			codexEntry("sk-oai-test-primary-1001cafe", "codex-primary", "https://api.openai.com"),
			codexEntry("sk-oai-test-backup-1002beef", "codex-backup", "https://api.openai.com"),
			codexEntry("sk-oai-test-relay-1003feed", "codex-relay", "https://openai-relay.example.com"),
		},
		"openai-compatibility": []map[string]any{
			compatEntry("deepseek", "https://api.deepseek.com/v1", "sk-ds-test-official-2001cafe"),
			compatEntry("deepseek-silicon", "https://api.siliconflow.cn/v1", "sk-ds-test-silicon-2002beef"),
			compatEntry("deepseek-volc", "https://ark.volces.example.com/v3", "sk-ds-test-volc-2003feed"),
		},
		"vertex-api-key": []map[string]any{},
		"xai-api-key":    []map[string]any{},
	}
	pathToKey = map[string]string{
		"/v0/management/gemini-api-key":       "gemini-api-key",
		"/v0/management/codex-api-key":        "codex-api-key",
		"/v0/management/claude-api-key":       "claude-api-key",
		"/v0/management/openai-compatibility": "openai-compatibility",
		"/v0/management/vertex-api-key":       "vertex-api-key",
		"/v0/management/xai-api-key":          "xai-api-key",
	}
)

func models(names ...string) []map[string]any {
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		items = append(items, map[string]any{"name": name})
	}
	return items
}

func claudeEntry(key, authIndex, baseURL string) map[string]any {
	return map[string]any{
		"api-key":    key,
		"auth-index": authIndex,
		"base-url":   baseURL,
		"models":     models("claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"),
	}
}

func codexEntry(key, authIndex, baseURL string) map[string]any {
	return map[string]any{
		"api-key":    key,
		"auth-index": authIndex,
		"base-url":   baseURL,
		"models":     models("gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"),
	}
}

func compatEntry(name, baseURL, key string) map[string]any {
	return map[string]any{
		"name":            name,
		"base-url":        baseURL,
		"api-key-entries": []map[string]any{{"api-key": key}},
		"models":          models("deepseek-v4-pro", "deepseek-v4-flash", "deepseek-reasoner"),
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, `{"error":"missing management key"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	mu.Lock()
	defer mu.Unlock()
	switch {
	case r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(config)
	case r.URL.Path == "/v0/management/api-key-usage" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	case r.URL.Path == "/v0/management/usage-statistics-enabled" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false})
	default:
		key, ok := pathToKey[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(config[key])
		case http.MethodPut:
			var next []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
				return
			}
			config[key] = next
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
	log.Printf("%s %s", r.Method, r.URL.Path)
}

func main() {
	addr := "127.0.0.1:18319"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	if err := validateLoopbackListenAddress(addr); err != nil {
		log.Fatal(err)
	}
	log.Printf("fake CPA management listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(handle)))
}

func validateLoopbackListenAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("fake CPA address must be a loopback host and port, got %q", addr)
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("fake CPA address must bind to loopback only, got %q", addr)
	}
	return nil
}
