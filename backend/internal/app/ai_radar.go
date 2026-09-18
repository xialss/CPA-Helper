package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// AI radar consumes the public codexradar.com intelligence-efficiency feed.
//
// The upstream API sends no Access-Control-Allow-Origin header, so a browser
// cannot call it directly; every request is proxied through this backend. The
// feature is deliberately independent of model monitoring: the model-monitoring
// contract owns exactly four status-page adapters and forbids adding sources or
// a persisted source table, and its payload shape is availability-oriented
// rather than metric-oriented.
const (
	aiRadarLiveEndpoint    = "https://api.codexradar.com/api/v1/intelligence-efficiency"
	aiRadarHistoryEndpoint = "https://codex-reset-radar.pages.dev/data/intelligence-efficiency.json"

	aiRadarSourceLabel = "codexradar.com"
	aiRadarSourceURL   = "https://codexradar.com/"

	// aiRadarLiveSchema is the only accepted live contract version. An unknown
	// schema degrades the feature to "unavailable" instead of guessing at a
	// changed payload.
	aiRadarLiveSchema = 3

	aiRadarHTTPTimeout     = 20 * time.Second
	aiRadarBackfillTimeout = 90 * time.Second
	aiRadarMaxRedirects    = 5
	aiRadarMaxBodyBytes    = 8 << 20

	// aiRadarCacheTTL bounds how often a page view can reach the third-party
	// site: every viewer inside the window shares one upstream request.
	aiRadarCacheTTL = 60 * time.Second
	// aiRadarFailureTTL negative-caches a failed attempt so an outage cannot
	// turn every page view into a fresh timeout.
	aiRadarFailureTTL = 30 * time.Second

	// aiRadarHistoryInterval matches the source's own four-hour checkpoint
	// cadence. Combined with the upstream-revision dedupe it bounds growth to
	// roughly 150 rows/day for the GPT subset.
	aiRadarHistoryInterval = 4 * time.Hour

	// aiRadarModelPrefix restricts this phase to GPT-family models.
	aiRadarModelPrefix = "gpt-"

	// aiRadarLowSampleRuns is the sample floor below which a point is flagged
	// low confidence. Upstream data is uncleaned and contains degenerate
	// values (IQ 0 and IQ 150) produced entirely by tiny samples, so the UI
	// must be able to label them instead of presenting them as measured
	// capability.
	aiRadarLowSampleRuns = 20

	aiRadarOriginLive     = "live"
	aiRadarOriginBackfill = "backfill"
)

// aiRadarCostExponent is ln(2.5)/ln(1.35), the exponent the source documents as
// "a 2.5x price increase is worth a 1.35x duration increase". It reproduces the
// upstream combined_cost_index exactly; that value is NOT normalized upstream.
const aiRadarCostExponent = 3.0532379541169

// aiRadarEffortOrder fixes the reasoning-tier order used for chart series so a
// model's line always runs low to ultra.
var aiRadarEffortOrder = map[string]int{
	"low": 0, "medium": 1, "high": 2, "xhigh": 3, "max": 4, "ultra": 5,
}

var aiRadarWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

type AIRadarPoint struct {
	Model              string   `json:"model"`
	Effort             string   `json:"effort"`
	IQ                 *float64 `json:"iq"`
	Passed             *float64 `json:"passed"`
	Total              *float64 `json:"total"`
	AveragePriceUSD    *float64 `json:"average_price_usd"`
	AverageMinutes     *float64 `json:"average_minutes"`
	CombinedCostIndex  *float64 `json:"combined_cost_index"`
	AverageAgentSteps  *float64 `json:"average_agent_steps"`
	AverageTotalTokens *float64 `json:"average_total_tokens"`
	CacheHitRate       *float64 `json:"cache_hit_rate"`
	Runs24h            *int     `json:"runs_24h"`
	Runs48h            *int     `json:"runs_48h"`
	RunsTotal          *int     `json:"runs_total"`
	LowConfidence      bool     `json:"low_confidence"`
}

type AIRadarResponse struct {
	Available       bool           `json:"available"`
	Stale           bool           `json:"stale"`
	SourceLabel     string         `json:"source_label"`
	SourceURL       string         `json:"source_url"`
	BenchmarkID     string         `json:"benchmark_id"`
	ScoringMode     string         `json:"scoring_mode"`
	ScoreLabel      string         `json:"score_label"`
	SourceUpdatedAt *string        `json:"source_updated_at"`
	FetchedAt       *string        `json:"fetched_at"`
	Runs24hTotal    *int           `json:"runs_24h_total"`
	LowSampleRuns   int            `json:"low_sample_runs"`
	Points          []AIRadarPoint `json:"points"`
	Message         *string        `json:"message"`
}

type AIRadarHistoryPoint struct {
	ObservedAt        string   `json:"observed_at"`
	IQ                *float64 `json:"iq"`
	AveragePriceUSD   *float64 `json:"average_price_usd"`
	AverageMinutes    *float64 `json:"average_minutes"`
	CombinedCostIndex *float64 `json:"combined_cost_index"`
	AverageAgentSteps *float64 `json:"average_agent_steps"`
}

type AIRadarHistorySeries struct {
	Model  string                `json:"model"`
	Effort string                `json:"effort"`
	Points []AIRadarHistoryPoint `json:"points"`
}

type AIRadarHistoryResponse struct {
	Available     bool                   `json:"available"`
	SourceLabel   string                 `json:"source_label"`
	SourceURL     string                 `json:"source_url"`
	IntervalHours float64                `json:"interval_hours"`
	Series        []AIRadarHistorySeries `json:"series"`
	Message       *string                `json:"message"`
}

// aiRadarSnapshot is the cached outcome of the most recent upstream attempt.
type aiRadarSnapshot struct {
	response   *AIRadarResponse
	fetchedAt  time.Time
	attemptAt  time.Time
	lastError  string
	generation uint64
}

type aiRadarFetchResult struct {
	response   AIRadarResponse
	observedAt time.Time
}

type aiRadarRawPoint struct {
	Model              string   `json:"model"`
	Effort             string   `json:"effort"`
	Passed             *float64 `json:"passed"`
	Total              *float64 `json:"total"`
	IQ                 *float64 `json:"iq"`
	AveragePriceUSD    *float64 `json:"average_price_usd"`
	AverageMinutes     *float64 `json:"average_minutes"`
	CombinedCostIndex  *float64 `json:"combined_cost_index"`
	AverageAgentSteps  *float64 `json:"average_agent_steps"`
	AverageTotalTokens *float64 `json:"average_total_tokens"`
	CacheHitRate       *float64 `json:"cache_hit_rate"`
	Runs24h            *int     `json:"runs_24h"`
	Runs48h            *int     `json:"runs_48h"`
	RunsTotal          *int     `json:"runs_total"`
}

type aiRadarRawPayload struct {
	Schema          int             `json:"schema"`
	SourceUpdatedAt string          `json:"source_updated_at"`
	BenchmarkID     string          `json:"benchmark_id"`
	ScoringMode     string          `json:"scoring_mode"`
	ScoreLabel      string          `json:"score_label"`
	Runs24hTotal    *int            `json:"runs_24h_total"`
	Points          json.RawMessage `json:"points"`
}

// The history snapshot is schema 2 and renames valid_tasks to the live total.
type aiRadarSnapshotPoint struct {
	Model              string   `json:"model"`
	Effort             string   `json:"effort"`
	Passed             *float64 `json:"passed"`
	ValidTasks         *float64 `json:"valid_tasks"`
	IQ                 *float64 `json:"iq"`
	AveragePriceUSD    *float64 `json:"average_price_usd"`
	AverageMinutes     *float64 `json:"average_minutes"`
	AverageAgentSteps  *float64 `json:"average_agent_steps"`
	AverageTotalTokens *float64 `json:"average_total_tokens"`
	CacheHitRate       *float64 `json:"cache_hit_rate"`
}

type aiRadarSnapshotFrame struct {
	At     string          `json:"at"`
	Points json.RawMessage `json:"points"`
}

type aiRadarSnapshotPayload struct {
	History []aiRadarSnapshotFrame `json:"history"`
}

// aiRadarSamplingRunner owns the fixed-cadence history sampler. It deliberately
// waits for the first interval instead of doing network I/O during startup; the
// live endpoint can still seed history immediately when a user opens the page.
type aiRadarSamplingRunner struct {
	app      *App
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newAIRadarSamplingRunner(app *App) *aiRadarSamplingRunner {
	return &aiRadarSamplingRunner{app: app, interval: aiRadarHistoryInterval}
}

func (runner *aiRadarSamplingRunner) Start(parent context.Context) {
	if runner == nil {
		return
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.done != nil {
		select {
		case <-runner.done:
		default:
			return
		}
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	runner.cancel = cancel
	runner.done = make(chan struct{})
	go runner.loop(ctx, runner.done, runner.interval)
}

func (runner *aiRadarSamplingRunner) loop(ctx context.Context, done chan<- struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := runner.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ai radar scheduled sampling failed: %v", err)
			}
		}
	}
}

func (runner *aiRadarSamplingRunner) Stop() {
	if runner == nil {
		return
	}
	runner.mu.Lock()
	cancel := runner.cancel
	done := runner.done
	if cancel == nil || done == nil {
		runner.mu.Unlock()
		return
	}
	cancel()
	runner.mu.Unlock()
	<-done
}

func (runner *aiRadarSamplingRunner) RunOnce(ctx context.Context) error {
	if runner == nil || runner.app == nil {
		return errors.New("AI radar sampling runner is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := runner.app.aiRadarClient(aiRadarHTTPTimeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()

	result, err := fetchAIRadarLive(ctx, client)
	if err != nil {
		return err
	}
	return runner.app.recordAIRadarHistory(ctx, result.response.Points, result.observedAt)
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func newAIRadarHTTPClient(timeout time.Duration) (*http.Client, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport has unexpected type")
	}
	// The radar has no dedicated proxy setting, so it keeps Go's default
	// environment-proxy behavior. It deliberately does not borrow the
	// model-monitor proxy columns, which the model-monitoring contract keeps
	// private to that feature.
	transport := defaultTransport.Clone()
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= aiRadarMaxRedirects {
				return errors.New("AI 雷达重定向次数过多")
			}
			if req.URL.Scheme != "https" {
				return errors.New("AI 雷达重定向必须使用 HTTPS")
			}
			return nil
		},
	}, nil
}

func (a *App) aiRadarClient(timeout time.Duration) (*http.Client, error) {
	if a.aiRadarHTTPClient != nil {
		return a.aiRadarHTTPClient(timeout)
	}
	return newAIRadarHTTPClient(timeout)
}

func aiRadarGet(ctx context.Context, client *http.Client, targetURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("AI 雷达请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("AI 雷达上游返回 HTTP %d", response.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, "application/json") {
		return nil, fmt.Errorf("AI 雷达上游返回了不支持的内容类型 %q", response.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, aiRadarMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 AI 雷达上游响应失败: %w", err)
	}
	if len(body) > aiRadarMaxBodyBytes {
		return nil, errors.New("AI 雷达上游响应超过 8 MiB 限制")
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// Normalization
// ---------------------------------------------------------------------------

// aiRadarCombinedCostIndex reproduces the upstream efficiency index. The result
// is left unnormalized: the source normalizes "the highest combined cost in
// this chart to 100" at draw time, so normalizing here would destroy
// comparability between different point sets.
func aiRadarCombinedCostIndex(priceUSD, minutes float64) float64 {
	if minutes <= 0 {
		return 0
	}
	return priceUSD * math.Pow(minutes/10, aiRadarCostExponent) * 100
}

// aiRadarFinite rejects non-finite floats. encoding/json cannot encode NaN or
// Inf, so a value that passes a naive range check can still corrupt the whole
// response.
func aiRadarFinite(value *float64) (*float64, bool) {
	if value == nil {
		return nil, true
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil, false
	}
	return value, true
}

func aiRadarFiniteRange(value *float64, minimum, maximum float64) (*float64, bool) {
	finite, ok := aiRadarFinite(value)
	if !ok || finite == nil {
		return finite, ok
	}
	if *finite < minimum || *finite > maximum {
		return nil, false
	}
	return finite, true
}

func aiRadarNonNegativeInt(value *int) bool {
	return value == nil || *value >= 0
}

func aiRadarLowConfidence(total *float64, runsTotal *int) bool {
	if total != nil && *total < aiRadarLowSampleRuns {
		return true
	}
	if runsTotal != nil && *runsTotal < aiRadarLowSampleRuns {
		return true
	}
	return false
}

func aiRadarIsGPTModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, aiRadarModelPrefix) && model != "gpt-5.5"
}

// aiRadarNormalizePoints filters to the GPT subset, drops points carrying
// non-finite numbers, orders the result deterministically, and derives the
// combined cost index when upstream omits it.
func aiRadarNormalizePoints(raw []aiRadarRawPoint) []AIRadarPoint {
	points := make([]AIRadarPoint, 0, len(raw))
	for _, item := range raw {
		if !aiRadarIsGPTModel(item.Model) {
			continue
		}
		effort := strings.ToLower(strings.TrimSpace(item.Effort))
		if _, ok := aiRadarEffortOrder[effort]; !ok {
			continue
		}

		iq, okIQ := aiRadarFiniteRange(item.IQ, 0, 150)
		passed, okPassed := aiRadarFiniteRange(item.Passed, 0, math.MaxFloat64)
		total, okTotal := aiRadarFiniteRange(item.Total, 0, math.MaxFloat64)
		price, okPrice := aiRadarFiniteRange(item.AveragePriceUSD, 0, math.MaxFloat64)
		minutes, okMinutes := aiRadarFiniteRange(item.AverageMinutes, 0, math.MaxFloat64)
		combined, okCombined := aiRadarFiniteRange(item.CombinedCostIndex, 0, math.MaxFloat64)
		steps, okSteps := aiRadarFiniteRange(item.AverageAgentSteps, 0, math.MaxFloat64)
		tokens, okTokens := aiRadarFiniteRange(item.AverageTotalTokens, 0, math.MaxFloat64)
		cache, okCache := aiRadarFiniteRange(item.CacheHitRate, 0, 1)
		if !okIQ || !okPassed || !okTotal || !okPrice || !okMinutes || !okCombined || !okSteps || !okTokens || !okCache ||
			!aiRadarNonNegativeInt(item.Runs24h) || !aiRadarNonNegativeInt(item.Runs48h) || !aiRadarNonNegativeInt(item.RunsTotal) {
			continue
		}
		if passed != nil && total != nil && *passed > *total {
			continue
		}

		if combined == nil && price != nil && minutes != nil && *minutes > 0 {
			derived := aiRadarCombinedCostIndex(*price, *minutes)
			if !math.IsNaN(derived) && !math.IsInf(derived, 0) {
				combined = &derived
			}
		}

		points = append(points, AIRadarPoint{
			Model:              strings.TrimSpace(item.Model),
			Effort:             effort,
			IQ:                 iq,
			Passed:             passed,
			Total:              total,
			AveragePriceUSD:    price,
			AverageMinutes:     minutes,
			CombinedCostIndex:  combined,
			AverageAgentSteps:  steps,
			AverageTotalTokens: tokens,
			CacheHitRate:       cache,
			Runs24h:            item.Runs24h,
			Runs48h:            item.Runs48h,
			RunsTotal:          item.RunsTotal,
			LowConfidence:      aiRadarLowConfidence(total, item.RunsTotal),
		})
	}

	sort.SliceStable(points, func(left, right int) bool {
		if points[left].Model != points[right].Model {
			return points[left].Model < points[right].Model
		}
		return aiRadarEffortOrder[points[left].Effort] < aiRadarEffortOrder[points[right].Effort]
	})
	return points
}

// aiRadarDecodeRawPoints decodes each point independently. encoding/json
// rejects an entire []T when one number overflows float64 (for example 1e400);
// isolating elements lets us discard only that malformed observation.
func aiRadarDecodeRawPoints(raw json.RawMessage, required bool) ([]aiRadarRawPoint, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		if required {
			return nil, errors.New("AI 雷达上游缺少 points 数组")
		}
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("AI 雷达上游 points 不是数组: %w", err)
	}
	points := make([]aiRadarRawPoint, 0, len(items))
	for _, item := range items {
		var point aiRadarRawPoint
		if err := json.Unmarshal(item, &point); err != nil {
			continue
		}
		points = append(points, point)
	}
	return points, nil
}

func aiRadarDecodeSnapshotPoints(raw json.RawMessage) ([]aiRadarSnapshotPoint, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("AI 雷达历史快照 points 不是数组: %w", err)
	}
	points := make([]aiRadarSnapshotPoint, 0, len(items))
	for _, item := range items {
		var point aiRadarSnapshotPoint
		if err := json.Unmarshal(item, &point); err != nil {
			continue
		}
		points = append(points, point)
	}
	return points, nil
}

func fetchAIRadarLive(ctx context.Context, client *http.Client) (aiRadarFetchResult, error) {
	body, err := aiRadarGet(ctx, client, aiRadarLiveEndpoint)
	if err != nil {
		return aiRadarFetchResult{}, err
	}
	var payload aiRadarRawPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return aiRadarFetchResult{}, fmt.Errorf("AI 雷达上游返回了无效 JSON: %w", err)
	}
	if payload.Schema != aiRadarLiveSchema {
		return aiRadarFetchResult{}, fmt.Errorf("AI 雷达上游 schema 不受支持: %d", payload.Schema)
	}
	rawPoints, err := aiRadarDecodeRawPoints(payload.Points, true)
	if err != nil {
		return aiRadarFetchResult{}, err
	}

	fetchedAt := time.Now()
	observedAt, hasObserved := parseAIRadarSourceUpdatedAt(payload.SourceUpdatedAt)
	if !hasObserved {
		return aiRadarFetchResult{}, errors.New("AI 雷达上游缺少有效 source_updated_at")
	}
	runs24hTotal := payload.Runs24hTotal
	if !aiRadarNonNegativeInt(runs24hTotal) {
		runs24hTotal = nil
	}
	result := aiRadarFetchResult{
		response: AIRadarResponse{
			Available:     true,
			SourceLabel:   aiRadarSourceLabel,
			SourceURL:     aiRadarSourceURL,
			BenchmarkID:   payload.BenchmarkID,
			ScoringMode:   payload.ScoringMode,
			ScoreLabel:    payload.ScoreLabel,
			Runs24hTotal:  runs24hTotal,
			LowSampleRuns: aiRadarLowSampleRuns,
			FetchedAt:     aiRadarStringPtr(dbTime(fetchedAt)),
			Points:        aiRadarNormalizePoints(rawPoints),
		},
	}
	result.observedAt = observedAt
	result.response.SourceUpdatedAt = aiRadarStringPtr(dbTime(observedAt))
	return result, nil
}

func parseAIRadarSourceUpdatedAt(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.In(appTimeLocation), true
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

// aiRadarServeCached reports whether the cached snapshot may be served without
// another upstream attempt. A stored failure is honored only for the short
// negative-cache window; a stored success for the full TTL.
func aiRadarServeCached(snapshot aiRadarSnapshot, now time.Time, force bool, ttl, failureTTL time.Duration) bool {
	if force || snapshot.attemptAt.IsZero() {
		return false
	}
	if snapshot.response != nil && now.Sub(snapshot.fetchedAt) < ttl {
		return true
	}
	if snapshot.lastError != "" && now.Sub(snapshot.attemptAt) < failureTTL {
		return true
	}
	return false
}

func aiRadarCachedResponse(snapshot aiRadarSnapshot) AIRadarResponse {
	if snapshot.response != nil {
		out := *snapshot.response
		if snapshot.lastError != "" {
			out.Stale = true
			out.Message = aiRadarStringPtr(snapshot.lastError)
		} else {
			out.Stale = false
			out.Message = nil
		}
		return out
	}
	return aiRadarUnavailableResponse(snapshot.lastError)
}

// aiRadarUnavailableResponse keeps upstream failure out of the HTTP status:
// callers always receive HTTP 200 with a structurally valid payload, matching
// how model monitoring isolates a failed source.
func aiRadarUnavailableResponse(reason string) AIRadarResponse {
	response := AIRadarResponse{
		Available:     false,
		SourceLabel:   aiRadarSourceLabel,
		SourceURL:     aiRadarSourceURL,
		LowSampleRuns: aiRadarLowSampleRuns,
		Points:        []AIRadarPoint{},
	}
	if strings.TrimSpace(reason) != "" {
		response.Message = aiRadarStringPtr(reason)
	}
	return response
}

func aiRadarStringPtr(value string) *string {
	return &value
}

type aiRadarLiveFlight struct {
	done       chan struct{}
	result     aiRadarFetchResult
	err        error
	generation uint64
}

// Flights are kept outside App so this feature can add single-flight behavior
// without changing the shared App layout. Entries are removed by the leader
// after completion, so the map does not retain completed requests.
var aiRadarLiveFlights sync.Map // map[*App]*aiRadarLiveFlight

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (a *App) handleAIRadar(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.readyUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	return a.serveAIRadar(w, r, false)
}

func (a *App) handleAIRadarRefresh(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodPost); err != nil {
		return err
	}
	return a.serveAIRadar(w, r, true)
}

func (a *App) serveAIRadar(w http.ResponseWriter, r *http.Request, force bool) error {
	now := time.Now()
	a.aiRadarMu.Lock()
	snapshot := a.aiRadarSnapshot
	if aiRadarServeCached(snapshot, now, force, aiRadarCacheTTL, aiRadarFailureTTL) {
		a.aiRadarMu.Unlock()
		writeJSON(w, http.StatusOK, aiRadarCachedResponse(snapshot))
		return nil
	}
	var flight *aiRadarLiveFlight
	leader := true
	if !force {
		if existing, ok := aiRadarLiveFlights.Load(a); ok {
			flight = existing.(*aiRadarLiveFlight)
			leader = false
		}
	}
	if leader {
		flight = &aiRadarLiveFlight{done: make(chan struct{})}
		if !force {
			aiRadarLiveFlights.Store(a, flight)
		}
		a.aiRadarGeneration++
		flight.generation = a.aiRadarGeneration
	}
	generation := flight.generation
	a.aiRadarMu.Unlock()

	if !leader {
		select {
		case <-flight.done:
		case <-r.Context().Done():
			return r.Context().Err()
		}
		a.aiRadarMu.Lock()
		current := a.aiRadarSnapshot
		a.aiRadarMu.Unlock()
		writeJSON(w, http.StatusOK, aiRadarCachedResponse(current))
		return nil
	}

	client, err := a.aiRadarClient(aiRadarHTTPTimeout)
	if err != nil {
		flight.err = err
		if !force {
			aiRadarLiveFlights.Delete(a)
		}
		close(flight.done)
		return err
	}
	defer client.CloseIdleConnections()

	fetchContext := r.Context()
	var cancel context.CancelFunc
	if !force {
		fetchContext, cancel = context.WithTimeout(context.Background(), aiRadarHTTPTimeout)
		defer cancel()
	}
	result, fetchErr := fetchAIRadarLive(fetchContext, client)
	// A live revision must not become visible before its history sample. Both
	// cache hits and flight followers may immediately reload history on seeing
	// that revision. Keep sampling best effort, and independent of the first
	// viewer disconnecting when this is a shared ordinary fetch.
	if fetchErr == nil {
		if err := a.recordAIRadarHistory(fetchContext, result.response.Points, result.observedAt); err != nil {
			log.Printf("ai radar history sampling failed: %v", err)
		}
	}
	completedAt := time.Now()
	flight.result = result
	flight.err = fetchErr

	a.aiRadarMu.Lock()
	// Network calls run without the mutex. Publish only if no newer attempt has
	// already committed, and preserve the latest shared response on failure.
	// This prevents a slow old success or failure from replacing a newer result.
	if generation > a.aiRadarSnapshot.generation {
		if fetchErr != nil {
			current := a.aiRadarSnapshot
			current.attemptAt = completedAt
			current.lastError = fetchErr.Error()
			current.generation = generation
			a.aiRadarSnapshot = current
		} else {
			response := result.response
			a.aiRadarSnapshot = aiRadarSnapshot{
				response:   &response,
				fetchedAt:  completedAt,
				attemptAt:  completedAt,
				generation: generation,
			}
		}
	}
	current := a.aiRadarSnapshot
	a.aiRadarMu.Unlock()
	if !force {
		aiRadarLiveFlights.Delete(a)
	}
	close(flight.done)

	writeJSON(w, http.StatusOK, aiRadarCachedResponse(current))
	return nil
}

func (a *App) handleAIRadarHistory(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.readyUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	since, err := parseAIRadarWindow(r.URL.Query().Get("window"))
	if err != nil {
		return err
	}

	backfillErr := a.ensureAIRadarBackfill(r.Context())

	series, err := a.loadAIRadarHistory(r.Context(), since)
	if err != nil {
		return err
	}
	response := AIRadarHistoryResponse{
		Available:     len(series) > 0,
		SourceLabel:   aiRadarSourceLabel,
		SourceURL:     aiRadarSourceURL,
		IntervalHours: aiRadarHistoryInterval.Hours(),
		Series:        series,
	}
	if backfillErr != nil {
		response.Message = aiRadarStringPtr(backfillErr.Error())
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

// parseAIRadarWindow maps the requested window to a lower bound. An absent
// window defaults to 30 days; "all" removes the bound.
func parseAIRadarWindow(value string) (*time.Time, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		text = "30d"
	}
	if text == "all" {
		return nil, nil
	}
	duration, ok := aiRadarWindows[text]
	if !ok {
		return nil, validationError("AI 雷达时间范围无效: " + value)
	}
	bound := time.Now().Add(-duration)
	return &bound, nil
}

// ---------------------------------------------------------------------------
// History storage
// ---------------------------------------------------------------------------

// aiRadarShouldRecord reports whether a new observation belongs in the series.
//
// Both conditions must hold. The first (this upstream revision is new to the
// series) prevents duplicate rows while upstream has not moved. The second (the
// observation interval has elapsed) bounds growth when upstream refreshes far
// more often than the history chart needs.
func aiRadarShouldRecord(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, model, effort string, observedAt time.Time, interval time.Duration) (bool, error) {
	observed := dbTime(observedAt)

	var duplicate int
	if err := queryer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_radar_points WHERE model = ? AND effort = ? AND observed_at = ?`,
		model, effort, observed,
	).Scan(&duplicate); err != nil {
		return false, err
	}
	if duplicate > 0 {
		return false, nil
	}

	var latest sql.NullString
	if err := queryer.QueryRowContext(ctx,
		`SELECT MAX(observed_at) FROM ai_radar_points WHERE model = ? AND effort = ?`,
		model, effort,
	).Scan(&latest); err != nil {
		return false, err
	}
	if latest.Valid && strings.TrimSpace(latest.String) != "" {
		if last, ok := parseDBTime(latest.String); ok && observedAt.Sub(last) < interval {
			return false, nil
		}
	}
	return true, nil
}

func (a *App) recordAIRadarHistory(ctx context.Context, points []AIRadarPoint, observedAt time.Time) error {
	if len(points) == 0 || observedAt.IsZero() {
		return nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	createdAt := dbTime(time.Now())
	for _, point := range points {
		record, err := aiRadarShouldRecord(ctx, tx, point.Model, point.Effort, observedAt, aiRadarHistoryInterval)
		if err != nil {
			return err
		}
		if !record {
			continue
		}
		if err := insertAIRadarPoint(ctx, tx, point, observedAt, aiRadarOriginLive, createdAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertAIRadarPoint(ctx context.Context, tx *sql.Tx, point AIRadarPoint, observedAt time.Time, origin, createdAt string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO ai_radar_points (
			model, effort, observed_at, iq, passed, total,
			average_price_usd, average_minutes, combined_cost_index,
			average_agent_steps, average_total_tokens, cache_hit_rate,
			runs_24h, runs_48h, runs_total, origin, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		point.Model, point.Effort, dbTime(observedAt), point.IQ, point.Passed, point.Total,
		point.AveragePriceUSD, point.AverageMinutes, point.CombinedCostIndex,
		point.AverageAgentSteps, point.AverageTotalTokens, point.CacheHitRate,
		point.Runs24h, point.Runs48h, point.RunsTotal, origin, createdAt,
	)
	return err
}

func (a *App) aiRadarBackfillCompleted(ctx context.Context) (bool, error) {
	var completed sql.NullString
	err := a.db.QueryRowContext(ctx,
		`SELECT backfill_completed_at FROM ai_radar_state WHERE id = 1`,
	).Scan(&completed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return completed.Valid && strings.TrimSpace(completed.String) != "", nil
}

func (a *App) markAIRadarBackfillCompleted(ctx context.Context) error {
	now := dbTime(time.Now())
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO ai_radar_state (id, backfill_completed_at, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET backfill_completed_at = excluded.backfill_completed_at, updated_at = excluded.updated_at`,
		now, now,
	)
	return err
}

// ensureAIRadarBackfill downloads the published snapshot once. A second
// concurrent request skips instead of downloading the 6 MiB payload again, and
// a failed attempt leaves no marker so a later request can retry.
func (a *App) ensureAIRadarBackfill(ctx context.Context) error {
	completed, err := a.aiRadarBackfillCompleted(ctx)
	if err != nil || completed {
		return err
	}
	if !a.aiRadarBackfillMu.TryLock() {
		return nil
	}
	defer a.aiRadarBackfillMu.Unlock()

	completed, err = a.aiRadarBackfillCompleted(ctx)
	if err != nil || completed {
		return err
	}

	client, err := a.aiRadarClient(aiRadarBackfillTimeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()

	backfillCtx, cancel := context.WithTimeout(ctx, aiRadarBackfillTimeout)
	defer cancel()

	body, err := aiRadarGet(backfillCtx, client, aiRadarHistoryEndpoint)
	if err != nil {
		return err
	}
	var payload aiRadarSnapshotPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("AI 雷达历史快照不是有效 JSON: %w", err)
	}
	if err := a.importAIRadarSnapshot(backfillCtx, payload.History); err != nil {
		return err
	}
	return a.markAIRadarBackfillCompleted(backfillCtx)
}

// importAIRadarSnapshot is tolerant of sparse frames: published snapshots
// carry between 1 and 70 points each, so a frame with few or no usable points
// is expected rather than an error.
func (a *App) importAIRadarSnapshot(ctx context.Context, frames []aiRadarSnapshotFrame) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	createdAt := dbTime(time.Now())
	imported := 0
	for _, frame := range frames {
		observedAt, ok := parseDBTime(frame.At)
		if !ok {
			continue
		}
		rawPoints, err := aiRadarDecodeSnapshotPoints(frame.Points)
		if err != nil {
			continue
		}
		for _, raw := range rawPoints {
			point, ok := aiRadarSnapshotPointValue(raw)
			if !ok {
				continue
			}
			if err := insertAIRadarPoint(ctx, tx, point, observedAt, aiRadarOriginBackfill, createdAt); err != nil {
				return err
			}
			imported++
		}
	}
	if imported == 0 {
		return errors.New("AI 雷达历史快照没有可导入的 GPT 观测点")
	}
	return tx.Commit()
}

func aiRadarSnapshotPointValue(raw aiRadarSnapshotPoint) (AIRadarPoint, bool) {
	if !aiRadarIsGPTModel(raw.Model) {
		return AIRadarPoint{}, false
	}
	effort := strings.ToLower(strings.TrimSpace(raw.Effort))
	if _, ok := aiRadarEffortOrder[effort]; !ok {
		return AIRadarPoint{}, false
	}

	iq, okIQ := aiRadarFiniteRange(raw.IQ, 0, 150)
	passed, okPassed := aiRadarFiniteRange(raw.Passed, 0, math.MaxFloat64)
	total, okTotal := aiRadarFiniteRange(raw.ValidTasks, 0, math.MaxFloat64)
	price, okPrice := aiRadarFiniteRange(raw.AveragePriceUSD, 0, math.MaxFloat64)
	minutes, okMinutes := aiRadarFiniteRange(raw.AverageMinutes, 0, math.MaxFloat64)
	steps, okSteps := aiRadarFiniteRange(raw.AverageAgentSteps, 0, math.MaxFloat64)
	tokens, okTokens := aiRadarFiniteRange(raw.AverageTotalTokens, 0, math.MaxFloat64)
	cache, okCache := aiRadarFiniteRange(raw.CacheHitRate, 0, 1)
	if !okIQ || !okPassed || !okTotal || !okPrice || !okMinutes || !okSteps || !okTokens || !okCache {
		return AIRadarPoint{}, false
	}
	if passed != nil && total != nil && *passed > *total {
		return AIRadarPoint{}, false
	}

	// Snapshot frames predate combined_cost_index, so it is derived with the
	// documented formula. A missing duration cannot produce a finite cost.
	var combined *float64
	if price != nil && minutes != nil && *minutes > 0 {
		derived := aiRadarCombinedCostIndex(*price, *minutes)
		if !math.IsNaN(derived) && !math.IsInf(derived, 0) {
			combined = &derived
		}
	}

	return AIRadarPoint{
		Model:              strings.TrimSpace(raw.Model),
		Effort:             effort,
		IQ:                 iq,
		Passed:             passed,
		Total:              total,
		AveragePriceUSD:    price,
		AverageMinutes:     minutes,
		CombinedCostIndex:  combined,
		AverageAgentSteps:  steps,
		AverageTotalTokens: tokens,
		CacheHitRate:       cache,
		LowConfidence:      aiRadarLowConfidence(total, nil),
	}, true
}

func (a *App) loadAIRadarHistory(ctx context.Context, since *time.Time) ([]AIRadarHistorySeries, error) {
	query := `
		SELECT model, effort, observed_at, iq, average_price_usd, average_minutes, combined_cost_index,
			average_agent_steps
		FROM ai_radar_points`
	args := []any{}
	if since != nil {
		query += ` WHERE observed_at >= ?`
		args = append(args, dbTime(*since))
	}
	query += ` ORDER BY model ASC, effort ASC, observed_at ASC`

	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	series := []AIRadarHistorySeries{}
	index := map[string]int{}
	for rows.Next() {
		var model, effort, observedAt string
		var iq, price, minutes, combined, agentSteps sql.NullFloat64
		if err := rows.Scan(&model, &effort, &observedAt, &iq, &price, &minutes, &combined, &agentSteps); err != nil {
			return nil, err
		}
		if !aiRadarIsGPTModel(model) {
			continue
		}
		key := model + "\x00" + effort
		position, ok := index[key]
		if !ok {
			series = append(series, AIRadarHistorySeries{Model: model, Effort: effort, Points: []AIRadarHistoryPoint{}})
			position = len(series) - 1
			index[key] = position
		}
		series[position].Points = append(series[position].Points, AIRadarHistoryPoint{
			ObservedAt:        observedAt,
			IQ:                aiRadarNullFloat(iq),
			AveragePriceUSD:   aiRadarNullFloat(price),
			AverageMinutes:    aiRadarNullFloat(minutes),
			CombinedCostIndex: aiRadarNullFloat(combined),
			AverageAgentSteps: aiRadarNullFloat(agentSteps),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return series, nil
}

func aiRadarNullFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
