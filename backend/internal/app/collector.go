package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CollectorRunner struct {
	app                     *App
	mu                      sync.Mutex
	stop                    chan struct{}
	done                    chan struct{}
	selectorDone            chan struct{}
	cancel                  context.CancelFunc
	selectorRefreshInterval time.Duration
	lastRemoteSyncAt        time.Time
}

type collectorPatch struct {
	Running          *bool
	LastPollAt       *time.Time
	LastSuccessAt    *time.Time
	LastError        *string
	RemoteEnabledSet bool
	RemoteEnabled    *bool
	RecordsDelta     int
}

type respError string

func (e respError) Error() string {
	return string(e)
}

var errCollectorSelectorConfigMismatch = errors.New("采集器 provider 选择器快照与当前管理配置不一致")

func NewCollectorRunner(app *App) *CollectorRunner {
	return &CollectorRunner{
		app:                     app,
		selectorRefreshInterval: modelPriceSelectorSnapshotRefreshInterval,
	}
}

func (r *CollectorRunner) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		select {
		case <-r.done:
		default:
			return
		}
	}
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	r.selectorDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.loop(ctx)
	go r.selectorRefreshLoop(ctx, r.selectorDone)
}

func (r *CollectorRunner) Stop() {
	r.mu.Lock()
	stop := r.stop
	done := r.done
	selectorDone := r.selectorDone
	cancel := r.cancel
	if stop == nil || done == nil {
		r.mu.Unlock()
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	if cancel != nil {
		cancel()
	}
	r.mu.Unlock()
	<-done
	if selectorDone != nil {
		<-selectorDone
	}
}

func (r *CollectorRunner) loop(ctx context.Context) {
	defer func() {
		_ = r.updateState(context.Background(), collectorPatch{Running: boolPtr(false)})
		r.mu.Lock()
		if r.done != nil {
			close(r.done)
		}
		r.mu.Unlock()
	}()

	for {
		select {
		case <-r.stop:
			return
		default:
		}

		cfg, err := r.app.loadConfig(ctx)
		if err != nil {
			r.setCollectorError(err, 10*time.Second)
			continue
		}
		collector := cfg.Collector
		if strings.TrimSpace(collector.ManagementKey) != "" {
			r.syncRemoteUsageEnabled(ctx, cfg)
		}
		if !collector.Enabled {
			_ = r.updateState(ctx, collectorPatch{Running: boolPtr(false)})
			if r.sleepOrStop(durationSeconds(minFloat(collector.PollIntervalSeconds, 5))) {
				return
			}
			continue
		}

		now := time.Now()
		_ = r.updateState(ctx, collectorPatch{Running: boolPtr(true), LastPollAt: &now})
		pricing, messages, err := r.loadCollectorBatch(ctx, collector)
		if err != nil {
			r.setCollectorError(err, durationSeconds(collector.RetryIntervalSeconds))
			continue
		}
		inserted := 0
		for _, message := range messages {
			_, created, err := r.app.saveUsageMessage(ctx, []byte(message), pricing)
			if err != nil {
				r.setCollectorError(err, durationSeconds(collector.RetryIntervalSeconds))
				continue
			}
			if created {
				inserted++
			}
		}
		successAt := time.Now()
		emptyError := ""
		_ = r.updateState(ctx, collectorPatch{
			Running:       boolPtr(true),
			LastSuccessAt: &successAt,
			LastError:     &emptyError,
			RecordsDelta:  inserted,
		})
		if r.sleepOrStop(durationSeconds(collector.PollIntervalSeconds)) {
			return
		}
	}
}

func (r *CollectorRunner) selectorRefreshLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	interval := r.selectorRefreshInterval
	if interval <= 0 {
		interval = modelPriceSelectorSnapshotRefreshInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		cfg, err := r.app.loadConfig(ctx)
		if err == nil {
			// The cache retains refresh failures so billing can fail closed after expiry.
			_ = r.app.refreshModelPriceSelectorsIfStale(ctx, cfg)
		}
		timer.Reset(interval)
	}
}

func (r *CollectorRunner) loadCollectorBatch(ctx context.Context, collector CollectorConfig) (modelPriceBillingIndex, []string, error) {
	cfg := AppConfig{Collector: collector}
	configKey := modelPriceSelectorConfigKey(cfg)
	pricing, err := r.app.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		return modelPriceBillingIndex{}, nil, err
	}
	pricing, selectorsCurrent := r.app.attachCachedBillingPriceSelectorsForConfig(pricing, cfg)
	var refreshErr error
	if !selectorsCurrent {
		refreshErr = r.app.refreshModelPriceSelectorsIfStale(ctx, cfg)
		if refreshErr != nil && (ctx.Err() != nil || pricing.MatchContext.SelectorsRequired) {
			return modelPriceBillingIndex{}, nil, refreshErr
		}
		pricing, selectorsCurrent = r.app.attachCachedBillingPriceSelectorsForConfig(pricing, cfg)
	}
	if !selectorsCurrent {
		if refreshErr != nil {
			if pricing.MatchContext.SelectorsRequired {
				return modelPriceBillingIndex{}, nil, refreshErr
			}
		} else if configKey != "" || pricing.MatchContext.SelectorsRequired {
			return modelPriceBillingIndex{}, nil, errCollectorSelectorConfigMismatch
		}
	}
	messages, err := consumeRespQueue(ctx, collector)
	if err != nil {
		return modelPriceBillingIndex{}, nil, err
	}
	return pricing, messages, nil
}

func (r *CollectorRunner) setCollectorError(err error, delay time.Duration) {
	message := fmt.Sprintf("%T: %s", err, err.Error())
	if len(message) > 2000 {
		message = message[:2000]
	}
	_ = r.updateState(context.Background(), collectorPatch{
		Running:   boolPtr(true),
		LastError: &message,
	})
	_ = r.sleepOrStop(delay)
}

func (r *CollectorRunner) sleepOrStop(delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-r.stop:
		return true
	case <-timer.C:
		return false
	}
}

func (r *CollectorRunner) syncRemoteUsageEnabled(ctx context.Context, cfg AppConfig) {
	r.mu.Lock()
	if !r.lastRemoteSyncAt.IsZero() && time.Since(r.lastRemoteSyncAt) < time.Minute {
		r.mu.Unlock()
		return
	}
	r.lastRemoteSyncAt = time.Now()
	r.mu.Unlock()

	remoteEnabled, message := r.remoteUsageEnabled(ctx, cfg)
	patch := collectorPatch{RemoteEnabledSet: true, RemoteEnabled: remoteEnabled}
	if message != nil {
		patch.LastError = message
	} else if !cfg.Collector.Enabled {
		emptyError := ""
		patch.LastError = &emptyError
	}
	_ = r.updateState(ctx, patch)
}

func (r *CollectorRunner) remoteUsageEnabled(ctx context.Context, cfg AppConfig) (*bool, *string) {
	headers := managementHeaders(cfg.Collector.ManagementKey)
	target := makeURL(cfg.Collector.CLIProxyURL, "/v0/management/usage-statistics-enabled", nil)
	response, payload, err := doJSON(ctx, httpClient(8*time.Second), http.MethodGet, target, headers, nil)
	if err != nil {
		message := "远程 usage 开关查询失败：" + err.Error()
		return nil, &message
	}
	current := parseRemoteUsageEnabled(response, payload)
	if current != nil && *current {
		return current, nil
	}
	response, _, err = doJSON(ctx, httpClient(8*time.Second), http.MethodPut, target, headers, map[string]bool{"value": true})
	if err != nil {
		message := "远程 usage 开关开启失败：" + err.Error()
		return current, &message
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("远程 usage 开关开启失败：HTTP %d", response.StatusCode)
		return current, &message
	}
	enabled := true
	return &enabled, nil
}

func parseRemoteUsageEnabled(response *http.Response, payload []byte) *bool {
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	var raw map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		return nil
	}
	if value, ok := raw["usage-statistics-enabled"].(bool); ok {
		return &value
	}
	return nil
}

func (r *CollectorRunner) updateState(ctx context.Context, patch collectorPatch) error {
	state, err := r.app.collectorState(ctx)
	if err != nil {
		return err
	}
	if patch.Running != nil {
		state.Running = *patch.Running
	}
	if patch.LastPollAt != nil {
		state.LastPollAt = patch.LastPollAt
	}
	if patch.LastSuccessAt != nil {
		state.LastSuccessAt = patch.LastSuccessAt
	}
	if patch.LastError != nil {
		state.LastError = patch.LastError
	}
	if patch.RemoteEnabledSet {
		state.RemoteEnabled = patch.RemoteEnabled
	}
	state.RecordsCollected += patch.RecordsDelta

	var lastPoll, lastSuccess, lastError, remoteEnabled any
	if state.LastPollAt != nil {
		lastPoll = dbTime(*state.LastPollAt)
	}
	if state.LastSuccessAt != nil {
		lastSuccess = dbTime(*state.LastSuccessAt)
	}
	if state.LastError != nil && strings.TrimSpace(*state.LastError) != "" {
		lastError = *state.LastError
	}
	if state.RemoteEnabled != nil {
		remoteEnabled = *state.RemoteEnabled
	}
	_, err = r.app.db.ExecContext(ctx, `
		UPDATE collector_state
		SET running = ?, last_poll_at = ?, last_success_at = ?, last_error = ?,
		    remote_enabled = ?, records_collected = ?, updated_at = ?
		WHERE id = 1
	`, state.Running, lastPoll, lastSuccess, lastError, remoteEnabled, state.RecordsCollected, dbTime(time.Now()))
	return err
}

func consumeRespQueue(ctx context.Context, cfg CollectorConfig) ([]string, error) {
	if !usesRespQueueProtocol(cfg.CLIProxyURL) {
		return consumeHTTPUsageQueue(ctx, cfg)
	}
	return consumeRespQueueOverTCP(ctx, cfg)
}

func usesRespQueueProtocol(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "redis", "resp", "tcp":
		return true
	default:
		return false
	}
}

func collectorManagementHTTPURL(rawURL string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if value == "" {
		return "", errors.New("CLIProxyAPI 地址不能为空")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("CLIProxyAPI 地址无效")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Hostname() == "" {
			return "", errors.New("CLIProxyAPI 地址缺少主机名")
		}
		return value, nil
	case "redis", "resp", "tcp":
		host := parsed.Hostname()
		if host == "" {
			host = "127.0.0.1"
		}
		port := parsed.Port()
		if port == "" {
			port = "8317"
		}
		return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
	default:
		return "", errors.New("CLIProxyAPI 地址必须使用 http、https、tcp、redis 或 resp 协议")
	}
}

func consumeHTTPUsageQueue(ctx context.Context, cfg CollectorConfig) ([]string, error) {
	query := url.Values{}
	query.Set("count", strconv.Itoa(cfg.BatchSize))
	response, payload, err := doJSON(
		ctx,
		httpClient(8*time.Second),
		http.MethodGet,
		makeURL(cfg.CLIProxyURL, "/v0/management/usage-queue", query),
		managementHeaders(cfg.ManagementKey),
		nil,
	)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("usage queue HTTP %d", response.StatusCode)
	}
	return decodeHTTPQueuePayload(payload)
}

func decodeHTTPQueuePayload(payload []byte) ([]string, error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(payload, &rawItems); err != nil {
		return nil, err
	}
	items := make([]string, 0, len(rawItems))
	for _, rawItem := range rawItems {
		trimmed := strings.TrimSpace(string(rawItem))
		if trimmed == "" || trimmed == "null" {
			continue
		}
		var text string
		if err := json.Unmarshal(rawItem, &text); err == nil {
			if normalized := strings.TrimSpace(text); normalized != "" {
				items = append(items, normalized)
			}
			continue
		}
		items = append(items, string(rawItem))
	}
	return items, nil
}

func consumeRespQueueOverTCP(ctx context.Context, cfg CollectorConfig) ([]string, error) {
	parsed, err := url.Parse(cfg.CLIProxyURL)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		port = "8317"
	}
	dialer := net.Dialer{Timeout: 8 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	reader := bufio.NewReader(conn)
	if strings.TrimSpace(cfg.ManagementKey) != "" {
		if _, err := sendRespCommand(conn, reader, "AUTH", cfg.ManagementKey); err != nil {
			return nil, err
		}
	}
	result, err := sendRespCommand(conn, reader, "LPOP", cfg.QueueName, strconv.Itoa(cfg.BatchSize))
	if err != nil {
		var respErr respError
		if !errors.As(err, &respErr) {
			return nil, err
		}
		items := make([]string, 0, cfg.BatchSize)
		for i := 0; i < cfg.BatchSize; i++ {
			item, singleErr := sendRespCommand(conn, reader, "LPOP", cfg.QueueName)
			if singleErr != nil {
				return items, singleErr
			}
			decoded := decodeQueueItem(item)
			if decoded == nil {
				break
			}
			items = append(items, *decoded)
		}
		return items, nil
	}
	return decodeQueueResult(result), nil
}

func sendRespCommand(conn net.Conn, reader *bufio.Reader, parts ...string) (any, error) {
	var builder strings.Builder
	builder.WriteString("*")
	builder.WriteString(strconv.Itoa(len(parts)))
	builder.WriteString("\r\n")
	for _, part := range parts {
		builder.WriteString("$")
		builder.WriteString(strconv.Itoa(len([]byte(part))))
		builder.WriteString("\r\n")
		builder.WriteString(part)
		builder.WriteString("\r\n")
	}
	if _, err := io.WriteString(conn, builder.String()); err != nil {
		return nil, err
	}
	return readResp(reader)
}

func readResp(reader *bufio.Reader) (any, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		return readRespLine(reader)
	case '-':
		line, _ := readRespLine(reader)
		return nil, respError(line)
	case ':':
		line, err := readRespLine(reader)
		if err != nil {
			return nil, err
		}
		return strconv.Atoi(line)
	case '$':
		line, err := readRespLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length == -1 {
			return nil, nil
		}
		payload := make([]byte, length+2)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
		return payload[:length], nil
	case '*':
		line, err := readRespLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length == -1 {
			return nil, nil
		}
		items := make([]any, 0, length)
		for i := 0; i < length; i++ {
			item, err := readResp(reader)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("未知 RESP 响应前缀：%q", prefix)
	}
}

func readRespLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func decodeQueueResult(value any) []string {
	if value == nil {
		return []string{}
	}
	if items, ok := value.([]any); ok {
		result := make([]string, 0, len(items))
		for _, item := range items {
			if decoded := decodeQueueItem(item); decoded != nil {
				result = append(result, *decoded)
			}
		}
		return result
	}
	if decoded := decodeQueueItem(value); decoded != nil {
		return []string{*decoded}
	}
	return []string{}
}

func decodeQueueItem(value any) *string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		text := string(typed)
		return &text
	case string:
		return &typed
	default:
		payload, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		text := string(payload)
		return &text
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func durationSeconds(value float64) time.Duration {
	if value <= 0 {
		value = 1
	}
	return time.Duration(value * float64(time.Second))
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
