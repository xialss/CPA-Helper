package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

// Loopback CLIProxyAPI management test double for acceptance runs (see
// .trellis/spec/guides/acceptance-seed-data.md). Provider lists, v8 credential
// views and Plugin v0.1.8 share writable, in-memory acceptance state. No real
// keys or external provider calls are used; state resets when the process exits.

func initialProviderConfig() map[string][]map[string]any {
	config := map[string][]map[string]any{
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
			codexEntry("tiny-key", "codex-short", "https://short-key.example.com/v1"),
		},
		"openai-compatibility": []map[string]any{
			compatEntry("deepseek", "https://api.deepseek.com/v1", "sk-ds-test-official-2001cafe"),
			compatEntry("deepseek-silicon", "https://api.siliconflow.cn/v1", "sk-ds-test-silicon-2002beef"),
			compatEntry("deepseek-volc", "https://ark.volces.example.com/v3", "sk-ds-test-volc-2003feed"),
			compatEntry("local-keyless", "http://127.0.0.1:19001/v1", ""),
		},
		"vertex-api-key": []map[string]any{},
		"xai-api-key":    []map[string]any{},
	}
	config["codex-api-key"][0]["models"] = append(models("gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"), map[string]any{"name": "private-upstream-model", "alias": "codex-route-alias"})
	config["codex-api-key"][1]["models"] = models("gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra", "codex-backup-only")
	config["codex-api-key"][3]["models"] = models("short-key-model")
	config["codex-api-key"][3]["prefix"] = "short"
	config["openai-compatibility"][0]["api-key-entries"] = []map[string]any{
		{"api-key": "sk-ds-test-official-2001cafe", "auth-index": "deepseek-key-one"},
		{"api-key": "sk-ds-test-second-2004beef", "auth-index": "deepseek-key-two"},
	}
	config["openai-compatibility"][3]["models"] = models("local-no-key-model")
	return config
}

var (
	defaultFakeCPA = newFakeCPA()
	pathToKey      = map[string]string{
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
	keys := []map[string]any{}
	if key != "" {
		keys = append(keys, map[string]any{"api-key": key, "auth-index": name + "-key"})
	}
	return map[string]any{
		"name":            name,
		"base-url":        baseURL,
		"auth-index":      name + "-keyless",
		"api-key-entries": keys,
		"models":          models("deepseek-v4-pro", "deepseek-v4-flash", "deepseek-reasoner"),
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	defaultFakeCPA.ServeHTTP(w, r)
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
