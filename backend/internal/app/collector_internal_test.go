package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type collectorProtocolTestServer struct {
	listener net.Listener
	mu       sync.Mutex
	events   []string
}

func newCollectorProtocolTestServer(t *testing.T) *collectorProtocolTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	server := &collectorProtocolTestServer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handle(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return server
}

func (server *collectorProtocolTestServer) url(scheme string) string {
	return scheme + "://" + server.listener.Addr().String()
}

func (server *collectorProtocolTestServer) record(event string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.events = append(server.events, event)
}

func (server *collectorProtocolTestServer) snapshotEvents() []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]string(nil), server.events...)
}

func (server *collectorProtocolTestServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	prefix, err := reader.Peek(1)
	if err != nil {
		return
	}
	if prefix[0] != '*' {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		server.record("http:" + request.URL.Path)
		body := "[]"
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		return
	}
	for {
		payload, err := readResp(reader)
		if err != nil {
			return
		}
		parts, ok := payload.([]any)
		if !ok || len(parts) == 0 {
			return
		}
		commandBytes, ok := parts[0].([]byte)
		if !ok {
			return
		}
		command := strings.ToUpper(string(commandBytes))
		server.record("resp:" + command)
		switch command {
		case "AUTH":
			_, _ = conn.Write([]byte("+OK\r\n"))
		case "LPOP":
			_, _ = conn.Write([]byte("*-1\r\n"))
			return
		default:
			_, _ = conn.Write([]byte("-unsupported command\r\n"))
			return
		}
	}
}

func TestConsumeRespQueueUsesHTTPManagementUsageQueue(t *testing.T) {
	requested := false
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.URL.Path != "/v0/management/usage-queue" {
			t.Fatalf("path = %q, want /v0/management/usage-queue", r.URL.Path)
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Fatalf("count query = %q, want 2", got)
		}
		if got := r.Header.Get("X-Management-Key"); got != "test-management-key" {
			t.Fatalf("management header = %q, want test-management-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"request_id": "req-http", "total_tokens": 12},
			`{"request_id":"req-string"}`,
			nil,
		})
	}))
	defer cpa.Close()

	items, err := consumeRespQueue(context.Background(), CollectorConfig{
		CLIProxyURL:   cpa.URL,
		ManagementKey: "test-management-key",
		QueueName:     "usage",
		BatchSize:     2,
	})
	if err != nil {
		t.Fatalf("consumeRespQueue returned error: %v", err)
	}
	if !requested {
		t.Fatal("HTTP usage queue endpoint was not requested")
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2: %#v", len(items), items)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(items[0]), &first); err != nil {
		t.Fatalf("first item is not JSON object: %q", items[0])
	}
	if first["request_id"] != "req-http" {
		t.Fatalf("first request_id = %#v, want req-http", first["request_id"])
	}
	if items[1] != `{"request_id":"req-string"}` {
		t.Fatalf("second item = %q, want encoded string payload", items[1])
	}
}

func TestUsesRespQueueProtocolOnlyForExplicitRawProtocols(t *testing.T) {
	tests := map[string]bool{
		"https://api.example.com":     false,
		"http://127.0.0.1:8317":       false,
		"api.example.com:8317":        false,
		"tcp://127.0.0.1:8317":        true,
		"redis://127.0.0.1:8317":      true,
		"resp://127.0.0.1:8317":       true,
		"wss://api.example.com/ws":    false,
		"https://api.example.com:443": false,
	}
	for rawURL, want := range tests {
		if got := usesRespQueueProtocol(rawURL); got != want {
			t.Fatalf("usesRespQueueProtocol(%q) = %v, want %v", rawURL, got, want)
		}
	}
}

func TestCollectorManagementHTTPURLDerivesRespProtocols(t *testing.T) {
	tests := map[string]string{
		"http://127.0.0.1:8317":         "http://127.0.0.1:8317",
		"https://api.example.com":       "https://api.example.com",
		"tcp://127.0.0.1:9000":          "http://127.0.0.1:9000",
		"redis://example.com:6380/0":    "http://example.com:6380",
		"resp://[2001:db8::1]:8317/key": "http://[2001:db8::1]:8317",
		"resp://example.com":            "http://example.com:8317",
	}
	for rawURL, want := range tests {
		got, err := collectorManagementHTTPURL(rawURL)
		if err != nil {
			t.Fatalf("collectorManagementHTTPURL(%q) failed: %v", rawURL, err)
		}
		if got != want {
			t.Fatalf("collectorManagementHTTPURL(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

func TestLoadCollectorBatchUsesDerivedHTTPManagementURLForRespQueue(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	server := newCollectorProtocolTestServer(t)
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = server.url("resp")
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	pricing, messages, err := app.collector.loadCollectorBatch(ctx, cfg.Collector)
	if err != nil {
		t.Fatalf("loadCollectorBatch failed: %v", err)
	}
	if !pricing.MatchContext.SelectorsAvailable {
		t.Fatal("selector snapshot was not attached to RESP collector batch")
	}
	if pricing.MatchContext.SelectorsRequired {
		t.Fatal("selector snapshot attachment made selectors required without channel prices")
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %#v, want empty queue", messages)
	}
	want := []string{
		"http:/v0/management/gemini-api-key",
		"http:/v0/management/codex-api-key",
		"http:/v0/management/claude-api-key",
		"http:/v0/management/openai-compatibility",
		"http:/v0/management/vertex-api-key",
		"resp:AUTH",
		"resp:LPOP",
	}
	if got := server.snapshotEvents(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("protocol event order = %#v, want %#v", got, want)
	}
}

func TestLoadCollectorBatchContinuesWithoutChannelPricesWhenSelectorRefreshFails(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	queueRequested := false
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/usage-queue" {
			mu.Lock()
			queueRequested = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		http.Error(w, "provider snapshot unavailable", http.StatusBadGateway)
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if err := app.refreshModelPriceSelectorsIfStale(ctx, cfg); err == nil {
		t.Fatal("initial selector refresh unexpectedly succeeded")
	}

	pricing, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector)
	if err != nil {
		t.Fatalf("loadCollectorBatch failed without channel prices: %v", err)
	}
	if pricing.MatchContext.SelectorsRequired || pricing.MatchContext.SelectorsAvailable {
		t.Fatalf("selector context = %#v, want optional and unavailable", pricing.MatchContext)
	}
	mu.Lock()
	defer mu.Unlock()
	if !queueRequested {
		t.Fatal("usage queue was not requested after optional selector refresh failed")
	}
}

func TestLoadCollectorBatchContinuesWithoutSelectorConfigWhenSelectorsAreOptional(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	queueRequested := false
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Management-Key"); got != "" {
			t.Fatalf("management header = %q, want empty", got)
		}
		queueRequested = true
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = ""
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	pricing, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector)
	if err != nil {
		t.Fatalf("loadCollectorBatch failed without selector config: %v", err)
	}
	if pricing.MatchContext.SelectorsRequired || pricing.MatchContext.SelectorsAvailable {
		t.Fatalf("selector context = %#v, want optional and unavailable", pricing.MatchContext)
	}
	if !queueRequested {
		t.Fatal("usage queue was not requested without selector config")
	}
}

func TestCollectorSelectorRefreshLoopRunsWhenCollectionDisabled(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	providerRequests := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/gemini-api-key", "/v0/management/codex-api-key", "/v0/management/claude-api-key", "/v0/management/openai-compatibility", "/v0/management/vertex-api-key":
			mu.Lock()
			providerRequests++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	cfg.Collector.Enabled = false
	cfg.Collector.PollIntervalSeconds = 3600
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	app.collector.selectorRefreshInterval = 10 * time.Millisecond
	refreshCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go app.collector.selectorRefreshLoop(refreshCtx, done)
	defer func() {
		cancel()
		<-done
	}()

	waitForProviderRequests := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := providerRequests
			mu.Unlock()
			if got >= want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		got := providerRequests
		mu.Unlock()
		t.Fatalf("provider requests = %d, want at least %d", got, want)
	}

	waitForProviderRequests(5)
	app.priceSelectors.mu.Lock()
	app.priceSelectors.expiresAt = time.Now().Add(-time.Second)
	app.priceSelectors.refreshAfter = time.Time{}
	app.priceSelectors.mu.Unlock()
	waitForProviderRequests(10)
	if _, available := app.priceSelectors.snapshotForConfig(modelPriceSelectorConfigKey(cfg)); !available {
		t.Fatal("selector refresh loop did not restore an expired snapshot")
	}
}

func TestLoadCollectorBatchStopsBeforeQueueWhenExpiredSelectorRefreshFails(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	providerRequestsFail := false
	queueRequests := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		failProviders := providerRequestsFail
		if r.URL.Path == "/v0/management/usage-queue" {
			queueRequests++
		}
		mu.Unlock()
		if r.URL.Path == "/v0/management/usage-queue" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if failProviders {
			http.Error(w, "provider snapshot unavailable", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES ('vendor', 'collector-stale-model', 'channel', 'openai_compatibility', 'vendor', 1, 1, 0, 0, 'manual', 0, ?)
	`, dbTime(time.Now())); err != nil {
		t.Fatalf("insert channel price: %v", err)
	}

	if _, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector); err != nil {
		t.Fatalf("initial loadCollectorBatch failed: %v", err)
	}
	app.priceSelectors.mu.Lock()
	app.priceSelectors.expiresAt = time.Now().Add(-time.Second)
	app.priceSelectors.refreshAfter = time.Time{}
	app.priceSelectors.mu.Unlock()
	mu.Lock()
	providerRequestsFail = true
	mu.Unlock()

	if _, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector); err == nil {
		t.Fatal("loadCollectorBatch unexpectedly reused an expired selector snapshot")
	}
	mu.Lock()
	defer mu.Unlock()
	if queueRequests != 1 {
		t.Fatalf("usage queue requests = %d, want only the initial successful batch", queueRequests)
	}
}

func TestLoadCollectorBatchUsesUnexpiredSelectorsAfterProactiveRefreshFails(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	providerRequestsFail := false
	queueRequests := 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		failProviders := providerRequestsFail
		if r.URL.Path == "/v0/management/usage-queue" {
			queueRequests++
		}
		mu.Unlock()
		if r.URL.Path == "/v0/management/usage-queue" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if failProviders {
			http.Error(w, "provider snapshot unavailable", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES ('vendor', 'collector-renewal-model', 'channel', 'openai_compatibility', 'vendor', 1, 1, 0, 0, 'manual', 0, ?)
	`, dbTime(time.Now())); err != nil {
		t.Fatalf("insert channel price: %v", err)
	}

	if _, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector); err != nil {
		t.Fatalf("initial loadCollectorBatch failed: %v", err)
	}
	app.priceSelectors.mu.Lock()
	app.priceSelectors.refreshAfter = time.Time{}
	app.priceSelectors.mu.Unlock()
	mu.Lock()
	providerRequestsFail = true
	mu.Unlock()
	if err := app.refreshModelPriceSelectorsIfStale(ctx, cfg); err == nil {
		t.Fatal("proactive selector refresh unexpectedly succeeded")
	}

	pricing, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector)
	if err != nil {
		t.Fatalf("loadCollectorBatch rejected an unexpired selector snapshot: %v", err)
	}
	if !pricing.MatchContext.SelectorsAvailable {
		t.Fatal("unexpired selector snapshot was not attached after renewal failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if queueRequests != 2 {
		t.Fatalf("usage queue requests = %d, want both batches consumed", queueRequests)
	}
}

func TestLoadCollectorBatchRejectsSelectorSnapshotFromAnotherConfig(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	oldConfigRequests := 0
	oldCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		oldConfigRequests++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer oldCPA.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()
	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = "http://current-config.invalid"
	cfg.Collector.ManagementKey = "current-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	configKey := modelPriceSelectorConfigKey(cfg)
	generation, current := app.priceSelectors.currentGeneration(configKey)
	if !current || !app.priceSelectors.store(configKey, generation, time.Now(), modelPriceChannelSelectorIndex{}) {
		t.Fatal("failed to install current-config selector snapshot")
	}

	oldCollector := cfg.Collector
	oldCollector.CLIProxyURL = oldCPA.URL
	oldCollector.ManagementKey = "old-management-key"
	_, _, err = app.collector.loadCollectorBatch(ctx, oldCollector)
	if !errors.Is(err, errCollectorSelectorConfigMismatch) {
		t.Fatalf("loadCollectorBatch error = %v, want selector config mismatch", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if oldConfigRequests != 0 {
		t.Fatalf("old config received %d requests before batch rejection", oldConfigRequests)
	}
}

func TestLoadCollectorBatchLoadsSelectorsBeforeConsumingQueue(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	var mu sync.Mutex
	requests := []string{}
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("X-Management-Key"); got != "test-management-key" {
			t.Errorf("management header = %q, want test-management-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/gemini-api-key", "/v0/management/codex-api-key", "/v0/management/claude-api-key", "/v0/management/openai-compatibility", "/v0/management/vertex-api-key":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/v0/management/usage-queue":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	if err := app.saveConfig(ctx, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million,
			source, auto_synced, updated_at
		) VALUES ('vendor', 'collector-model', 'channel', 'openai_compatibility', 'vendor', 1, 1, 0, 0, 'manual', 0, ?)
	`, dbTime(time.Now())); err != nil {
		t.Fatalf("insert channel price: %v", err)
	}

	if _, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector); err != nil {
		t.Fatalf("loadCollectorBatch failed: %v", err)
	}
	if _, _, err := app.collector.loadCollectorBatch(ctx, cfg.Collector); err != nil {
		t.Fatalf("second loadCollectorBatch failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/v0/management/gemini-api-key",
		"/v0/management/codex-api-key",
		"/v0/management/claude-api-key",
		"/v0/management/openai-compatibility",
		"/v0/management/vertex-api-key",
		"/v0/management/usage-queue",
		"/v0/management/usage-queue",
	}
	if len(requests) != len(want) {
		t.Fatalf("request order = %#v, want %#v", requests, want)
	}
	for index := range want {
		if requests[index] != want[index] {
			t.Fatalf("request order = %#v, want %#v", requests, want)
		}
	}
}

func TestSyncRemoteUsageEnabledClearsDisabledCollectorStaleErrorOnSuccess(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-statistics-enabled" {
			t.Fatalf("path = %q, want /v0/management/usage-statistics-enabled", r.URL.Path)
		}
		if got := r.Header.Get("X-Management-Key"); got != "test-management-key" {
			t.Fatalf("management header = %q, want test-management-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"usage-statistics-enabled": true})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	cfg.Collector.Enabled = false

	staleError := "remote usage toggle query failed: timeout"
	if err := app.collector.updateState(ctx, collectorPatch{LastError: &staleError}); err != nil {
		t.Fatalf("updateState failed: %v", err)
	}
	app.collector.mu.Lock()
	app.collector.lastRemoteSyncAt = time.Time{}
	app.collector.mu.Unlock()

	app.collector.syncRemoteUsageEnabled(ctx, cfg)

	state, err := app.collectorState(ctx)
	if err != nil {
		t.Fatalf("collectorState failed: %v", err)
	}
	if state.LastError != nil {
		t.Fatalf("last error = %q, want cleared", *state.LastError)
	}
	if state.RemoteEnabled == nil || !*state.RemoteEnabled {
		t.Fatalf("remote enabled = %v, want true", state.RemoteEnabled)
	}
}

func TestSyncRemoteUsageEnabledKeepsEnabledCollectorErrorOnSuccess(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"usage-statistics-enabled": true})
	}))
	defer cpa.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	app.collector.Stop()

	ctx := context.Background()
	cfg, err := app.loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.Collector.CLIProxyURL = cpa.URL
	cfg.Collector.ManagementKey = "test-management-key"
	cfg.Collector.Enabled = true

	collectorError := "usage queue HTTP 500"
	if err := app.collector.updateState(ctx, collectorPatch{LastError: &collectorError}); err != nil {
		t.Fatalf("updateState failed: %v", err)
	}
	app.collector.mu.Lock()
	app.collector.lastRemoteSyncAt = time.Time{}
	app.collector.mu.Unlock()

	app.collector.syncRemoteUsageEnabled(ctx, cfg)

	state, err := app.collectorState(ctx)
	if err != nil {
		t.Fatalf("collectorState failed: %v", err)
	}
	if state.LastError == nil || *state.LastError != collectorError {
		t.Fatalf("last error = %v, want %q", state.LastError, collectorError)
	}
}
