package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixtures and doubles
// ---------------------------------------------------------------------------

type aiRadarRoundTripper func(*http.Request) (*http.Response, error)

func (fn aiRadarRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type aiRadarTestUpstream struct {
	mu              sync.Mutex
	liveRequests    int
	historyRequests int
	liveStatus      int
	liveContentType string
	liveBody        string
	historyStatus   int
	historyBody     string
}

type aiRadarSequencedUpstream struct {
	mu           sync.Mutex
	requests     int
	firstStarted chan struct{}
	releaseFirst chan struct{}
	firstStatus  int
	firstBody    string
	secondStatus int
	secondBody   string
}

func (u *aiRadarSequencedUpstream) client(time.Duration) (*http.Client, error) {
	return &http.Client{Transport: aiRadarRoundTripper(func(request *http.Request) (*http.Response, error) {
		u.mu.Lock()
		u.requests++
		requestNumber := u.requests
		u.mu.Unlock()
		if requestNumber == 1 {
			close(u.firstStarted)
			select {
			case <-u.releaseFirst:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
			return aiRadarTestResponse(request, u.firstStatus, "application/json", u.firstBody), nil
		}
		return aiRadarTestResponse(request, u.secondStatus, "application/json", u.secondBody), nil
	})}, nil
}

func (u *aiRadarTestUpstream) client(time.Duration) (*http.Client, error) {
	return &http.Client{Transport: aiRadarRoundTripper(u.roundTrip)}, nil
}

func (u *aiRadarTestUpstream) roundTrip(request *http.Request) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	target := request.URL.String()
	switch {
	case strings.HasPrefix(target, aiRadarLiveEndpoint):
		u.liveRequests++
		contentType := u.liveContentType
		if contentType == "" {
			contentType = "application/json"
		}
		return aiRadarTestResponse(request, u.liveStatus, contentType, u.liveBody), nil
	case strings.HasPrefix(target, aiRadarHistoryEndpoint):
		u.historyRequests++
		return aiRadarTestResponse(request, u.historyStatus, "application/json", u.historyBody), nil
	}
	return nil, fmt.Errorf("unexpected AI radar request host %q", request.URL.Host)
}

func (u *aiRadarTestUpstream) liveRequestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.liveRequests
}

func (u *aiRadarTestUpstream) historyRequestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.historyRequests
}

func aiRadarTestResponse(request *http.Request, status int, contentType, body string) *http.Response {
	if status == 0 {
		status = http.StatusOK
	}
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func aiRadarFloatPtr(value float64) *float64 { return &value }
func aiRadarIntPtr(value int) *int           { return &value }

func aiRadarRawPointFixture(model, effort string) aiRadarRawPoint {
	return aiRadarRawPoint{
		Model:              model,
		Effort:             effort,
		Passed:             aiRadarFloatPtr(89),
		Total:              aiRadarFloatPtr(136),
		IQ:                 aiRadarFloatPtr(98.16),
		AveragePriceUSD:    aiRadarFloatPtr(1.96),
		AverageMinutes:     aiRadarFloatPtr(8.64),
		AverageAgentSteps:  aiRadarFloatPtr(25.4),
		AverageTotalTokens: aiRadarFloatPtr(967050),
		CacheHitRate:       aiRadarFloatPtr(0.94),
		Runs24h:            aiRadarIntPtr(3),
		Runs48h:            aiRadarIntPtr(5),
		RunsTotal:          aiRadarIntPtr(137),
	}
}

// aiRadarLivePayloadJSON builds a schema-3 payload that mixes GPT models with
// models this phase must drop.
func aiRadarLivePayloadJSON(t *testing.T, sourceUpdatedAt string) string {
	t.Helper()
	point := func(model, effort string, iq, passed, validTasks float64, runsTotal int) map[string]any {
		return map[string]any{
			"model": model, "effort": effort,
			"passed": passed, "total": validTasks, "iq": iq,
			"average_price_usd": 1.96, "average_minutes": 8.64,
			"combined_cost_index": 125.606,
			"average_agent_steps": 25.4, "average_total_tokens": 967050.0,
			"cache_hit_rate": 0.9446,
			"runs_24h":       1, "runs_48h": 2, "runs_total": runsTotal,
		}
	}
	payload := map[string]any{
		"schema":            aiRadarLiveSchema,
		"source_updated_at": sourceUpdatedAt,
		"benchmark_id":      "deep-swe",
		"scoring_mode":      "binary-majority",
		"score_label":       "Pass rate",
		"runs_24h_total":    128,
		"points": []map[string]any{
			point("gpt-6-astra", "high", 108.33, 98, 136, 137),
			point("gpt-6-astra", "low", 98.16, 89, 136, 137),
			point("gpt-5.6-luna", "high", 96.88, 88, 136, 4), // low confidence
			point("gpt-5.5", "high", 96.88, 88, 136, 137),
			point("gpt-5.6-sol", "ultra", 107.14, 97, 136, 137),
			point("claude-opus-5", "high", 99.0, 90, 136, 137),
			point("gemini-3.8-flash", "high", 88.0, 80, 136, 137),
			point("glm-5.3", "high", 90.0, 82, 136, 137),
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// aiRadarSnapshotPayloadJSON builds a schema-2 history snapshot that includes a
// sparse frame, an empty frame, an unfilterable frame and a non-GPT model.
func aiRadarSnapshotPayloadJSON(t *testing.T) string {
	t.Helper()
	point := func(model, effort string, passed, validTasks, iq, price, minutes float64) map[string]any {
		return map[string]any{
			"model": model, "effort": effort,
			"passed": passed, "valid_tasks": validTasks, "iq": iq,
			"average_price_usd": price, "average_minutes": minutes,
			"average_agent_steps": 37.1, "average_total_tokens": 1615915.0,
			"cache_hit_rate": 0.956,
		}
	}
	payload := map[string]any{
		"schema": 2,
		"history": []map[string]any{
			{"at": "2026-07-23T04:55:27+08:00", "points": []map[string]any{
				point("gpt-5.6-terra", "medium", 38, 102, 55.8824, 0.737628, 10.0722),
				point("gpt-5.5", "high", 50, 100, 75.0, 1.0, 10.0),
				point("claude-opus-5", "high", 50, 100, 75.0, 1.0, 10.0),
			}},
			{"at": "2026-07-23T08:55:27+08:00", "points": []map[string]any{}},
			{"at": "not-a-timestamp", "points": []map[string]any{
				point("gpt-5.5", "high", 1, 1, 150.0, 4.51, 20.95),
			}},
			{"at": "2026-07-24T04:55:27+08:00", "points": []map[string]any{
				point("gpt-5.6-terra", "medium", 40, 102, 58.8235, 0.7, 10.1),
			}},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func newAIRadarTestApp(t *testing.T) (*App, http.Handler, []*http.Cookie) {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	// Close before t.TempDir cleanup runs, otherwise Windows refuses to remove
	// the still-open SQLite file.
	t.Cleanup(func() { _ = app.db.Close() })
	handler := app.Routes()
	admin := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup",
		map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	return app, handler, admin
}

func newAIRadarMemberCookies(t *testing.T, handler http.Handler, admin []*http.Cookie) []*http.Cookie {
	t.Helper()
	modelMonitorRequest(t, handler, http.MethodPost, "/api/users",
		map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false},
		admin, http.StatusOK, nil)
	member := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/login",
		map[string]any{"username": "member", "password": "member-password"}, nil, http.StatusOK, nil)
	return modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/change-credentials",
		map[string]any{"current_password": "member-password", "password": "member-new-password"},
		member, http.StatusOK, nil)
}

func aiRadarHistoryRowCount(t *testing.T, app *App) int {
	t.Helper()
	var count int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM ai_radar_points`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// ---------------------------------------------------------------------------
// Metric semantics
// ---------------------------------------------------------------------------

func TestAIRadarCombinedCostIndexMatchesDocumentedFormula(t *testing.T) {
	// Real upstream point: gpt-6-astra/low reports price 1.962449 USD,
	// duration 8.64 minutes and combined_cost_index 125.606.
	got := aiRadarCombinedCostIndex(1.962449, 8.64)
	if math.Abs(got-125.606) > 0.05 {
		t.Fatalf("combined cost index = %f, want ~125.606", got)
	}

	// The exponent must stay ln(2.5)/ln(1.35): the source documents the
	// weighting as "2.5x the price is worth 1.35x the duration".
	if math.Abs(aiRadarCostExponent-math.Log(2.5)/math.Log(1.35)) > 1e-9 {
		t.Fatalf("cost exponent = %f, want ln(2.5)/ln(1.35)", aiRadarCostExponent)
	}
	baseline := aiRadarCombinedCostIndex(2, 10)
	if math.Abs(baseline-200) > 1e-9 {
		t.Fatalf("baseline = %f, want 200", baseline)
	}
	if priceScaled := aiRadarCombinedCostIndex(5, 10); math.Abs(priceScaled-baseline*2.5) > 1e-6 {
		t.Fatalf("2.5x price = %f, want %f", priceScaled, baseline*2.5)
	}
	if timeScaled := aiRadarCombinedCostIndex(2, 13.5); math.Abs(timeScaled-baseline*2.5) > 1e-6 {
		t.Fatalf("1.35x duration = %f, want %f", timeScaled, baseline*2.5)
	}
	if aiRadarCombinedCostIndex(2, 0) != 0 {
		t.Fatal("a zero duration must not produce a cost")
	}
}

func TestAIRadarNormalizePointsKeepsOnlyGPTAndOrdersEfforts(t *testing.T) {
	points := aiRadarNormalizePoints([]aiRadarRawPoint{
		aiRadarRawPointFixture("gpt-6-astra", "ultra"),
		aiRadarRawPointFixture("claude-opus-5", "high"),
		aiRadarRawPointFixture("gpt-6-astra", "low"),
		aiRadarRawPointFixture("gemini-3.8-flash", "high"),
		aiRadarRawPointFixture("gpt-6-astra", "medium"),
		aiRadarRawPointFixture("glm-5.3", "high"),
		aiRadarRawPointFixture("kimi-k2.8-preview", "max"),
		aiRadarRawPointFixture("gpt-6-astra", "bogus"),
		aiRadarRawPointFixture("gpt-5.5", "high"),
		aiRadarRawPointFixture(" GPT-5.5 ", "high"),
	})

	if len(points) != 3 {
		t.Fatalf("normalized points = %d, want 3: %#v", len(points), points)
	}
	efforts := []string{points[0].Effort, points[1].Effort, points[2].Effort}
	if got := strings.Join(efforts, ","); got != "low,medium,ultra" {
		t.Fatalf("effort order = %s, want low,medium,ultra", got)
	}
	for _, point := range points {
		if point.Model != "gpt-6-astra" {
			t.Fatalf("non-GPT model survived the filter: %#v", point)
		}
	}
}

func TestAIRadarNormalizePointsKeepsMissingValuesNull(t *testing.T) {
	raw := aiRadarRawPointFixture("gpt-5.6-luna", "high")
	raw.AveragePriceUSD = nil
	raw.AverageAgentSteps = nil
	raw.CombinedCostIndex = nil
	raw.RunsTotal = nil

	points := aiRadarNormalizePoints([]aiRadarRawPoint{raw})
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if point.AveragePriceUSD != nil || point.AverageAgentSteps != nil || point.CombinedCostIndex != nil {
		t.Fatalf("missing values were not preserved as nil: %#v", point)
	}
	// A missing price cannot produce a derived combined cost.
	encoded, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"average_price_usd":null`,
		`"average_agent_steps":null`,
		`"combined_cost_index":null`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("response %s does not serialize %s", encoded, want)
		}
	}
	if !strings.Contains(string(encoded), `"iq":98.16`) {
		t.Fatalf("a present value was dropped: %s", encoded)
	}
}

func TestAIRadarNormalizePointsDerivesCombinedCostWhenUpstreamOmitsIt(t *testing.T) {
	raw := aiRadarRawPointFixture("gpt-6-astra", "low")
	raw.AveragePriceUSD = aiRadarFloatPtr(1.962449)
	raw.AverageMinutes = aiRadarFloatPtr(8.64)
	raw.CombinedCostIndex = nil

	missingDuration := aiRadarRawPointFixture("gpt-6-astra", "medium")
	missingDuration.AverageMinutes = nil
	missingDuration.CombinedCostIndex = nil

	points := aiRadarNormalizePoints([]aiRadarRawPoint{raw, missingDuration})
	if len(points) != 2 || points[0].CombinedCostIndex == nil {
		t.Fatalf("combined cost was not derived: %#v", points)
	}
	if math.Abs(*points[0].CombinedCostIndex-125.606) > 0.05 {
		t.Fatalf("derived combined cost = %f, want ~125.606", *points[0].CombinedCostIndex)
	}
	if points[1].AverageMinutes != nil || points[1].CombinedCostIndex != nil {
		t.Fatalf("missing duration must remain null and must not derive a combined cost: %#v", points[1])
	}
}

func TestAIRadarSnapshotPointValueKeepsMissingMeasurementsNull(t *testing.T) {
	raw := aiRadarSnapshotPoint{
		Model: "gpt-5.6-luna", Effort: "high",
		Passed:          aiRadarFloatPtr(89),
		AveragePriceUSD: aiRadarFloatPtr(1), AverageMinutes: aiRadarFloatPtr(10),
		AverageAgentSteps: aiRadarFloatPtr(25), AverageTotalTokens: aiRadarFloatPtr(1000),
		CacheHitRate: aiRadarFloatPtr(0.9),
	}

	point, ok := aiRadarSnapshotPointValue(raw)
	if !ok {
		t.Fatal("snapshot point with missing IQ and valid_tasks was dropped")
	}
	if point.IQ != nil || point.Total != nil {
		t.Fatalf("missing snapshot measurements were not preserved as nil: %#v", point)
	}
}

func TestAIRadarNormalizePointsDropsNonFiniteValues(t *testing.T) {
	nan := aiRadarRawPointFixture("gpt-5.6-luna", "high")
	nan.IQ = aiRadarFloatPtr(math.NaN())

	inf := aiRadarRawPointFixture("gpt-5.6-sol", "high")
	inf.AverageMinutes = aiRadarFloatPtr(math.Inf(1))

	negativeInf := aiRadarRawPointFixture("gpt-5.6-terra", "high")
	negativeInf.AveragePriceUSD = aiRadarFloatPtr(math.Inf(-1))

	healthy := aiRadarRawPointFixture("gpt-6-astra", "high")

	points := aiRadarNormalizePoints([]aiRadarRawPoint{nan, inf, negativeInf, healthy})
	if len(points) != 1 || points[0].Model != "gpt-6-astra" {
		t.Fatalf("non-finite points were not dropped: %#v", points)
	}
	// The surviving response must remain JSON-encodable, which non-finite
	// floats would have broken.
	if _, err := json.Marshal(points); err != nil {
		t.Fatalf("normalized points are not JSON-encodable: %v", err)
	}
}

func TestAIRadarNormalizePointsDropsOutOfDomainValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*aiRadarRawPoint)
	}{
		{name: "negative IQ", mutate: func(point *aiRadarRawPoint) { point.IQ = aiRadarFloatPtr(-0.01) }},
		{name: "IQ above scaled pass-rate ceiling", mutate: func(point *aiRadarRawPoint) { point.IQ = aiRadarFloatPtr(150.01) }},
		{name: "negative passed count", mutate: func(point *aiRadarRawPoint) { point.Passed = aiRadarFloatPtr(-1) }},
		{name: "passed exceeds total", mutate: func(point *aiRadarRawPoint) { point.Passed = aiRadarFloatPtr(137) }},
		{name: "negative total", mutate: func(point *aiRadarRawPoint) { point.Total = aiRadarFloatPtr(-1) }},
		{name: "negative price", mutate: func(point *aiRadarRawPoint) { point.AveragePriceUSD = aiRadarFloatPtr(-1) }},
		{name: "negative minutes", mutate: func(point *aiRadarRawPoint) { point.AverageMinutes = aiRadarFloatPtr(-1) }},
		{name: "negative combined cost", mutate: func(point *aiRadarRawPoint) { point.CombinedCostIndex = aiRadarFloatPtr(-1) }},
		{name: "negative agent steps", mutate: func(point *aiRadarRawPoint) { point.AverageAgentSteps = aiRadarFloatPtr(-1) }},
		{name: "negative tokens", mutate: func(point *aiRadarRawPoint) { point.AverageTotalTokens = aiRadarFloatPtr(-1) }},
		{name: "cache hit rate below zero", mutate: func(point *aiRadarRawPoint) { point.CacheHitRate = aiRadarFloatPtr(-0.01) }},
		{name: "cache hit rate above one", mutate: func(point *aiRadarRawPoint) { point.CacheHitRate = aiRadarFloatPtr(1.01) }},
		{name: "negative runs 24h", mutate: func(point *aiRadarRawPoint) { point.Runs24h = aiRadarIntPtr(-1) }},
		{name: "negative runs 48h", mutate: func(point *aiRadarRawPoint) { point.Runs48h = aiRadarIntPtr(-1) }},
		{name: "negative runs total", mutate: func(point *aiRadarRawPoint) { point.RunsTotal = aiRadarIntPtr(-1) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := aiRadarRawPointFixture("gpt-5.6-luna", "high")
			test.mutate(&invalid)
			healthy := aiRadarRawPointFixture("gpt-6-astra", "high")
			points := aiRadarNormalizePoints([]aiRadarRawPoint{invalid, healthy})
			if len(points) != 1 || points[0].Model != healthy.Model {
				t.Fatalf("out-of-domain point survived normalization: %#v", points)
			}
		})
	}

	snapshotTests := []struct {
		name   string
		mutate func(*aiRadarSnapshotPoint)
	}{
		{name: "IQ above scaled pass-rate ceiling", mutate: func(point *aiRadarSnapshotPoint) { point.IQ = aiRadarFloatPtr(151) }},
		{name: "negative passed count", mutate: func(point *aiRadarSnapshotPoint) { point.Passed = aiRadarFloatPtr(-1) }},
		{name: "passed exceeds total", mutate: func(point *aiRadarSnapshotPoint) { point.Passed = aiRadarFloatPtr(137) }},
		{name: "negative total", mutate: func(point *aiRadarSnapshotPoint) { point.ValidTasks = aiRadarFloatPtr(-1) }},
		{name: "negative price", mutate: func(point *aiRadarSnapshotPoint) { point.AveragePriceUSD = aiRadarFloatPtr(-1) }},
		{name: "negative minutes", mutate: func(point *aiRadarSnapshotPoint) { point.AverageMinutes = aiRadarFloatPtr(-1) }},
		{name: "negative agent steps", mutate: func(point *aiRadarSnapshotPoint) { point.AverageAgentSteps = aiRadarFloatPtr(-1) }},
		{name: "negative tokens", mutate: func(point *aiRadarSnapshotPoint) { point.AverageTotalTokens = aiRadarFloatPtr(-1) }},
		{name: "cache hit rate below zero", mutate: func(point *aiRadarSnapshotPoint) { point.CacheHitRate = aiRadarFloatPtr(-0.01) }},
		{name: "cache hit rate above one", mutate: func(point *aiRadarSnapshotPoint) { point.CacheHitRate = aiRadarFloatPtr(1.01) }},
	}
	for _, test := range snapshotTests {
		t.Run("snapshot "+test.name, func(t *testing.T) {
			raw := aiRadarSnapshotPoint{
				Model: "gpt-5.6-luna", Effort: "high", IQ: aiRadarFloatPtr(98.16),
				Passed: aiRadarFloatPtr(89), ValidTasks: aiRadarFloatPtr(136),
				AveragePriceUSD: aiRadarFloatPtr(1), AverageMinutes: aiRadarFloatPtr(10),
				AverageAgentSteps: aiRadarFloatPtr(25), AverageTotalTokens: aiRadarFloatPtr(1000),
				CacheHitRate: aiRadarFloatPtr(0.9),
			}
			test.mutate(&raw)
			if _, ok := aiRadarSnapshotPointValue(raw); ok {
				t.Fatal("out-of-domain snapshot point was accepted for history import")
			}
		})
	}
}

func TestAIRadarFetchDropsNegativeAggregateCount(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")), &payload); err != nil {
		t.Fatal(err)
	}
	payload["runs_24h_total"] = -1
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &aiRadarTestUpstream{liveBody: string(body)}
	client, err := upstream.client(aiRadarHTTPTimeout)
	if err != nil {
		t.Fatal(err)
	}

	result, err := fetchAIRadarLive(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if result.response.Runs24hTotal != nil {
		t.Fatalf("negative aggregate count reached the response: %d", *result.response.Runs24hTotal)
	}
}

func TestAIRadarFetchRejectsMissingPointsAndSourceRevision(t *testing.T) {
	base := map[string]any{}
	if err := json.Unmarshal([]byte(aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")), &base); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"points", "source_updated_at"} {
		t.Run(field, func(t *testing.T) {
			payload := make(map[string]any, len(base))
			for key, value := range base {
				payload[key] = value
			}
			delete(payload, field)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			upstream := &aiRadarTestUpstream{liveBody: string(body)}
			client, err := upstream.client(aiRadarHTTPTimeout)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fetchAIRadarLive(context.Background(), client); err == nil {
				t.Fatalf("missing %s was accepted", field)
			}
		})
	}
}

func TestAIRadarFetchDropsOnlyOverflowingPoint(t *testing.T) {
	body := `{"schema":3,"source_updated_at":"2026-09-17T02:10:47Z","points":[` +
		`{"model":"gpt-6-astra","effort":"high","iq":1e400},` +
		`{"model":"gpt-6-astra","effort":"low","iq":98.16,"passed":89,"total":136,"average_price_usd":1.96,"average_minutes":8.64}]}`
	upstream := &aiRadarTestUpstream{liveBody: body}
	client, err := upstream.client(aiRadarHTTPTimeout)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetchAIRadarLive(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.response.Points) != 1 || result.response.Points[0].Effort != "low" {
		t.Fatalf("overflowing point was not isolated: %#v", result.response.Points)
	}
}

func TestAIRadarSnapshotDecodeDropsOnlyOverflowingPoint(t *testing.T) {
	raw := json.RawMessage(`[{"model":"gpt-6-astra","effort":"high","iq":1e400},{"model":"gpt-6-astra","effort":"low","iq":98.16}]`)
	points, err := aiRadarDecodeSnapshotPoints(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Effort != "low" {
		t.Fatalf("overflowing history point was not isolated: %#v", points)
	}
}

func TestAIRadarLowConfidenceUsesTheSampleFloor(t *testing.T) {
	if !aiRadarLowConfidence(aiRadarFloatPtr(1), aiRadarIntPtr(1)) {
		t.Fatal("a one-sample point must be flagged low confidence")
	}
	if aiRadarLowConfidence(aiRadarFloatPtr(136), aiRadarIntPtr(137)) {
		t.Fatal("a well-sampled point must not be flagged low confidence")
	}
	if aiRadarLowConfidence(nil, nil) {
		t.Fatal("unknown sample counts must not be reported as low confidence")
	}
}

// ---------------------------------------------------------------------------
// Cache behavior
// ---------------------------------------------------------------------------

func TestAIRadarServeCachedHonorsTTLAndFailureWindow(t *testing.T) {
	now := time.Now()
	response := AIRadarResponse{Available: true, Points: []AIRadarPoint{}}

	if aiRadarServeCached(aiRadarSnapshot{}, now, false, aiRadarCacheTTL, aiRadarFailureTTL) {
		t.Fatal("an empty snapshot must not be served from cache")
	}

	fresh := aiRadarSnapshot{response: &response, fetchedAt: now.Add(-30 * time.Second), attemptAt: now.Add(-30 * time.Second)}
	if !aiRadarServeCached(fresh, now, false, aiRadarCacheTTL, aiRadarFailureTTL) {
		t.Fatal("a fresh success must be served from cache")
	}
	if aiRadarServeCached(fresh, now, true, aiRadarCacheTTL, aiRadarFailureTTL) {
		t.Fatal("force must bypass the cache")
	}

	expired := aiRadarSnapshot{response: &response, fetchedAt: now.Add(-5 * time.Minute), attemptAt: now.Add(-5 * time.Minute)}
	if aiRadarServeCached(expired, now, false, aiRadarCacheTTL, aiRadarFailureTTL) {
		t.Fatal("an expired success must trigger a refetch")
	}

	failed := aiRadarSnapshot{response: &response, fetchedAt: now.Add(-10 * time.Minute), attemptAt: now.Add(-5 * time.Second), lastError: "boom"}
	if !aiRadarServeCached(failed, now, false, aiRadarCacheTTL, aiRadarFailureTTL) {
		t.Fatal("a recent failure must be negative-cached")
	}
	stale := aiRadarCachedResponse(failed)
	if !stale.Stale || stale.Message == nil || !stale.Available || len(stale.Points) != 0 {
		t.Fatalf("stale response = %#v, want available stale data with a message", stale)
	}

	empty := aiRadarCachedResponse(aiRadarSnapshot{attemptAt: now, lastError: "boom"})
	if empty.Available || empty.Points == nil || len(empty.Points) != 0 || empty.Stale {
		t.Fatalf("unavailable response = %#v, want available=false with an empty array", empty)
	}
}

func TestAIRadarConcurrentFetchesCannotOverwriteNewerSnapshot(t *testing.T) {
	tests := []struct {
		name        string
		firstStatus int
		firstBody   func(*testing.T) string
	}{
		{
			name:        "older failure",
			firstStatus: http.StatusBadGateway,
			firstBody:   func(*testing.T) string { return `{}` },
		},
		{
			name:        "older success",
			firstStatus: http.StatusOK,
			firstBody: func(t *testing.T) string {
				return aiRadarLivePayloadJSON(t, "2026-09-17T02:00:00Z")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, handler, admin := newAIRadarTestApp(t)
			upstream := &aiRadarSequencedUpstream{
				firstStarted: make(chan struct{}),
				releaseFirst: make(chan struct{}),
				firstStatus:  test.firstStatus,
				firstBody:    test.firstBody(t),
				secondStatus: http.StatusOK,
				secondBody:   aiRadarLivePayloadJSON(t, "2026-09-17T03:00:00Z"),
			}
			app.aiRadarHTTPClient = upstream.client

			releaseOnce := sync.Once{}
			t.Cleanup(func() { releaseOnce.Do(func() { close(upstream.releaseFirst) }) })
			perform := func(method, path string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(method, path, nil)
				for _, cookie := range admin {
					request.AddCookie(cookie)
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				return recorder
			}

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- perform(http.MethodGet, "/api/ai-radar") }()
			select {
			case <-upstream.firstStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("older request did not reach the controlled upstream")
			}

			newer := perform(http.MethodPost, "/api/ai-radar/refresh")
			releaseOnce.Do(func() { close(upstream.releaseFirst) })
			var older *httptest.ResponseRecorder
			select {
			case older = <-firstDone:
			case <-time.After(2 * time.Second):
				t.Fatal("older request did not finish after release")
			}
			if newer.Code != http.StatusOK || older.Code != http.StatusOK {
				t.Fatalf("response statuses = newer %d, older %d; want 200/200", newer.Code, older.Code)
			}

			app.aiRadarMu.Lock()
			snapshot := app.aiRadarSnapshot
			app.aiRadarMu.Unlock()
			if snapshot.response == nil || snapshot.response.SourceUpdatedAt == nil {
				t.Fatalf("newer healthy snapshot was lost: %#v", snapshot)
			}
			updatedAt, ok := parseDBTime(*snapshot.response.SourceUpdatedAt)
			if !ok || !updatedAt.Equal(time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC)) {
				t.Fatalf("published source revision = %v/%t, want the newer 03:00 revision", updatedAt, ok)
			}
			if snapshot.lastError != "" || snapshot.generation != 2 {
				t.Fatalf("older completion changed the newer snapshot: %#v", snapshot)
			}
		})
	}
}

func TestAIRadarOrdinaryCacheMissesShareOneFetch(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	requests := 0
	app.aiRadarHTTPClient = func(time.Duration) (*http.Client, error) {
		return &http.Client{Transport: aiRadarRoundTripper(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			requests++
			mu.Unlock()
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
			return aiRadarTestResponse(request, http.StatusOK, "application/json", aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")), nil
		})}, nil
	}
	perform := func(done chan<- int) {
		request := httptest.NewRequest(http.MethodGet, "/api/ai-radar", nil)
		for _, cookie := range admin {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		done <- recorder.Code
	}
	results := make(chan int, 2)
	go perform(results)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("shared fetch did not start")
	}
	go perform(results)
	time.Sleep(25 * time.Millisecond)
	mu.Lock()
	count := requests
	mu.Unlock()
	if count != 1 {
		t.Fatalf("ordinary misses started %d upstream requests, want 1", count)
	}
	close(release)
	for range 2 {
		select {
		case code := <-results:
			if code != http.StatusOK {
				t.Fatalf("response status = %d, want 200", code)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shared requests did not finish")
		}
	}
}

// ---------------------------------------------------------------------------
// History sampling
// ---------------------------------------------------------------------------

func TestAIRadarHistorySamplingRequiresNewRevisionAndElapsedInterval(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.db.Close() })
	ctx := context.Background()
	base := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	points := []AIRadarPoint{{Model: "gpt-6-astra", Effort: "high", IQ: aiRadarFloatPtr(100)}}

	if err := app.recordAIRadarHistory(ctx, points, base); err != nil {
		t.Fatal(err)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 1 {
		t.Fatalf("rows after first observation = %d, want 1", count)
	}

	// Same upstream revision: no duplicate row.
	if err := app.recordAIRadarHistory(ctx, points, base); err != nil {
		t.Fatal(err)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 1 {
		t.Fatalf("rows after repeating the same revision = %d, want 1", count)
	}

	// New revision but inside the observation interval: still nothing.
	if err := app.recordAIRadarHistory(ctx, points, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 1 {
		t.Fatalf("rows after a too-early revision = %d, want 1", count)
	}

	// New revision after the interval: recorded.
	if err := app.recordAIRadarHistory(ctx, points, base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 2 {
		t.Fatalf("rows after an eligible revision = %d, want 2", count)
	}

	// An absent upstream timestamp cannot anchor a series.
	if err := app.recordAIRadarHistory(ctx, points, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 2 {
		t.Fatalf("rows after a missing timestamp = %d, want 2", count)
	}
}

func TestAIRadarSamplingRunnerRecordsWithoutAUserRequest(t *testing.T) {
	app, _, _ := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")}
	app.aiRadarHTTPClient = upstream.client

	runner := newAIRadarSamplingRunner(app)
	runner.interval = 100 * time.Millisecond
	runner.Start(context.Background())
	defer runner.Stop()

	// Startup is deliberately quiet: the first external request belongs to the
	// first interval tick rather than application initialization.
	time.Sleep(25 * time.Millisecond)
	if count := upstream.liveRequestCount(); count != 0 {
		t.Fatalf("sampling runner made %d immediate request(s), want none before the first tick", count)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if aiRadarHistoryRowCount(t, app) > 0 {
			runner.Stop()
			requests := upstream.liveRequestCount()
			if requests == 0 {
				t.Fatal("scheduled sampling wrote history without using the configured upstream client")
			}
			time.Sleep(2 * runner.interval)
			if got := upstream.liveRequestCount(); got != requests {
				t.Fatalf("sampling runner made requests after Stop: before=%d after=%d", requests, got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduled sampling did not record history")
}

func TestAIRadarSamplingRunnerReportsHistoryWriteFailure(t *testing.T) {
	app, _, _ := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")}
	app.aiRadarHTTPClient = upstream.client
	if _, err := app.db.ExecContext(context.Background(), `DROP TABLE ai_radar_points`); err != nil {
		t.Fatal(err)
	}

	runner := newAIRadarSamplingRunner(app)
	err := runner.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ai_radar_points") {
		t.Fatalf("scheduled history write error = %v, want the database failure", err)
	}
	if count := upstream.liveRequestCount(); count != 1 {
		t.Fatalf("scheduled sampling upstream requests = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func TestAIRadarHTTPClientRejectsUnsafeAndExcessiveRedirects(t *testing.T) {
	client, err := newAIRadarHTTPClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if client.CheckRedirect == nil {
		t.Fatal("AI radar client has no redirect policy")
	}

	insecure, err := http.NewRequest(http.MethodGet, "http://example.test/insecure", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(insecure, []*http.Request{insecure}); err == nil || !strings.Contains(err.Error(), "必须使用 HTTPS") {
		t.Fatalf("insecure redirect error = %v, want HTTPS-only rejection", err)
	}

	secure, err := http.NewRequest(http.MethodGet, "https://example.test/loop", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, aiRadarMaxRedirects)
	for index := range via {
		via[index] = secure
	}
	if err := client.CheckRedirect(secure, via); err == nil || !strings.Contains(err.Error(), "次数过多") {
		t.Fatalf("redirect limit error = %v, want maximum-redirect rejection", err)
	}
}

func TestAIRadarRouteServesGPTOnlyAndSharesUpstreamCache(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47+00:00")}
	app.aiRadarHTTPClient = upstream.client

	var first AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &first)

	if !first.Available || first.Stale {
		t.Fatalf("live response = %#v, want available and not stale", first)
	}
	if len(first.Points) != 4 {
		t.Fatalf("points = %d, want 4 GPT points: %#v", len(first.Points), first.Points)
	}
	for _, point := range first.Points {
		if !strings.HasPrefix(point.Model, "gpt-") || point.Model == "gpt-5.5" {
			t.Fatalf("excluded model reached the response: %#v", point)
		}
	}
	if first.BenchmarkID != "deep-swe" || first.ScoringMode != "binary-majority" || first.ScoreLabel != "Pass rate" {
		t.Fatalf("benchmark metadata was not carried through: %#v", first)
	}
	if first.SourceLabel != aiRadarSourceLabel || first.SourceURL != aiRadarSourceURL {
		t.Fatalf("attribution is missing: %#v", first)
	}
	if first.SourceUpdatedAt == nil || first.FetchedAt == nil {
		t.Fatalf("timestamps are missing: %#v", first)
	}
	// The low-sample GPT point must be flagged, the others must not be.
	flagged := 0
	for _, point := range first.Points {
		if point.LowConfidence {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("low-confidence points = %d, want 1", flagged)
	}

	var second AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &second)
	if count := upstream.liveRequestCount(); count != 1 {
		t.Fatalf("upstream requests = %d, want 1 shared request inside the TTL", count)
	}
	if len(second.Points) != len(first.Points) {
		t.Fatalf("cached response changed: %#v", second)
	}
}

func TestAIRadarRouteDegradesWithoutServerError(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveStatus: http.StatusBadGateway, liveBody: "{}"}
	app.aiRadarHTTPClient = upstream.client

	var unavailable AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &unavailable)
	if unavailable.Available || unavailable.Points == nil || len(unavailable.Points) != 0 {
		t.Fatalf("unavailable response = %#v, want available=false with points []", unavailable)
	}
	if unavailable.Message == nil {
		t.Fatal("an unavailable response must explain itself")
	}

	// The failure is negative-cached, so an immediate retry must not hammer
	// the upstream again.
	var second AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &second)
	if count := upstream.liveRequestCount(); count != 1 {
		t.Fatalf("upstream requests = %d, want 1 negative-cached failure", count)
	}
}

func TestAIRadarForceRefreshServesStaleDataAfterUpstreamFailure(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47+00:00")}
	app.aiRadarHTTPClient = upstream.client

	var warm AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &warm)
	if !warm.Available {
		t.Fatalf("warm-up response = %#v", warm)
	}

	upstream.mu.Lock()
	upstream.liveStatus = http.StatusInternalServerError
	upstream.mu.Unlock()

	var forced AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodPost, "/api/ai-radar/refresh", nil, admin, http.StatusOK, &forced)
	if !forced.Available || !forced.Stale {
		t.Fatalf("forced refresh = %#v, want previously good data marked stale", forced)
	}
	if len(forced.Points) != len(warm.Points) {
		t.Fatalf("stale response lost data: %#v", forced)
	}
}

func TestAIRadarRejectsUnsupportedSchema(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: `{"schema":4,"points":[]}`}
	app.aiRadarHTTPClient = upstream.client

	var response AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &response)
	if response.Available || response.Message == nil {
		t.Fatalf("unsupported schema = %#v, want an explicit unavailable state", response)
	}
}

func TestAIRadarRejectsUnsupportedContentType(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveContentType: "text/html", liveBody: "<html></html>"}
	app.aiRadarHTTPClient = upstream.client

	var response AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &response)
	if response.Available {
		t.Fatalf("html body = %#v, want unavailable", response)
	}
}

func TestAIRadarRejectsResponseOverEightMiB(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: strings.Repeat("x", aiRadarMaxBodyBytes+1)}
	app.aiRadarHTTPClient = upstream.client

	var response AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &response)
	if response.Available || response.Message == nil || !strings.Contains(*response.Message, "8 MiB") {
		t.Fatalf("oversized response = %#v, want an explicit unavailable state", response)
	}
}

func TestAIRadarHistoryWriteFailureDoesNotBreakLiveResponse(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47Z")}
	app.aiRadarHTTPClient = upstream.client
	if _, err := app.db.ExecContext(context.Background(), `DROP TABLE ai_radar_points`); err != nil {
		t.Fatal(err)
	}

	var response AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, &response)
	if !response.Available || response.Stale || len(response.Points) == 0 {
		t.Fatalf("history write failure degraded the live response: %#v", response)
	}
}

func TestAIRadarRefreshIsAdminOnly(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, "2026-09-17T02:10:47+00:00")}
	app.aiRadarHTTPClient = upstream.client

	member := newAIRadarMemberCookies(t, handler, admin)

	modelMonitorRequest(t, handler, http.MethodPost, "/api/ai-radar/refresh", nil, member, http.StatusForbidden, nil)
	if count := upstream.liveRequestCount(); count != 0 {
		t.Fatalf("a member refresh reached the upstream %d time(s)", count)
	}

	// Reading stays open to any signed-in user.
	var memberView AIRadarResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, member, http.StatusOK, &memberView)
	if !memberView.Available {
		t.Fatalf("member read = %#v, want available", memberView)
	}

	// Unauthenticated access is still refused.
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, nil, http.StatusUnauthorized, nil)
}

func TestAIRadarHistoryBackfillsOnceAndToleratesSparseFrames(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	upstream := &aiRadarTestUpstream{
		historyStatus: http.StatusInternalServerError,
		historyBody:   `{}`,
	}
	app.aiRadarHTTPClient = upstream.client

	var failed AIRadarHistoryResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=all", nil, admin, http.StatusOK, &failed)
	if failed.Available || failed.Message == nil {
		t.Fatalf("failed backfill = %#v, want empty history with an error message", failed)
	}
	completed, err := app.aiRadarBackfillCompleted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed {
		t.Fatal("failed backfill wrote the completion marker")
	}

	upstream.mu.Lock()
	upstream.historyStatus = http.StatusOK
	upstream.historyBody = aiRadarSnapshotPayloadJSON(t)
	upstream.mu.Unlock()

	var first AIRadarHistoryResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=all", nil, admin, http.StatusOK, &first)
	if !first.Available || len(first.Series) != 1 {
		t.Fatalf("backfilled history = %#v, want one GPT series", first)
	}
	series := first.Series[0]
	if series.Model != "gpt-5.6-terra" || series.Effort != "medium" || len(series.Points) != 2 {
		t.Fatalf("series = %#v, want gpt-5.6-terra/medium with 2 points", series)
	}
	if count := aiRadarHistoryRowCount(t, app); count != 2 {
		t.Fatalf("backfill stored %d rows, want only the two allowed model points", count)
	}
	// The unfilterable frame's point must not appear under a GPT model.
	for _, point := range series.Points {
		if point.IQ == nil || point.CombinedCostIndex == nil {
			t.Fatalf("backfilled point lost derived values: %#v", point)
		}
	}
	// valid_tasks is stored as total, and the cost index is derived.
	var total float64
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT total FROM ai_radar_points WHERE model = 'gpt-5.6-terra' AND effort = 'medium' ORDER BY observed_at ASC LIMIT 1`,
	).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 102 {
		t.Fatalf("stored total = %f, want 102 mapped from valid_tasks", total)
	}
	if math.Abs(*series.Points[0].CombinedCostIndex-75.40) > 0.05 {
		t.Fatalf("derived history cost = %f, want ~75.40", *series.Points[0].CombinedCostIndex)
	}
	if first.IntervalHours != aiRadarHistoryInterval.Hours() {
		t.Fatalf("interval hours = %f, want %f", first.IntervalHours, aiRadarHistoryInterval.Hours())
	}
	completed, err = app.aiRadarBackfillCompleted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("recovered backfill did not write the completion marker")
	}

	// The completion marker prevents a second 6 MiB download.
	var second AIRadarHistoryResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=all", nil, admin, http.StatusOK, &second)
	if count := upstream.historyRequestCount(); count != 2 {
		t.Fatalf("history downloads = %d, want one failed attempt plus one recovered retry", count)
	}
	if len(second.Series) != len(first.Series) {
		t.Fatalf("second history response = %#v", second)
	}
}

func TestAIRadarHistoryExcludesPreviouslyStoredGPT55(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	ctx := context.Background()
	if err := app.markAIRadarBackfillCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	// Seed legacy rows directly: ingestion now filters these, but an existing
	// database may already contain them and must not expose them on reads.
	models := []string{"gpt-5.5", " GPT-5.5 ", "claude-opus-5", "gpt-5.6-sol", "gpt-5.5-preview"}
	for _, model := range models {
		if _, err := app.db.ExecContext(ctx, `
			INSERT INTO ai_radar_points (model, effort, observed_at, iq, origin, created_at)
			VALUES (?, 'high', ?, 100, 'live', ?)`, model, dbTime(time.Now()), dbTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	for _, window := range []string{"all", "24h"} {
		t.Run(window, func(t *testing.T) {
			var response AIRadarHistoryResponse
			modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window="+window, nil, admin, http.StatusOK, &response)
			if !response.Available || len(response.Series) != 2 {
				t.Fatalf("history = %#v, want two retained GPT series", response)
			}
			if response.Series[0].Model != "gpt-5.5-preview" || response.Series[1].Model != "gpt-5.6-sol" {
				t.Fatalf("history models = %#v, exclusion must match GPT-5.5 exactly", response.Series)
			}
		})
	}
	if count := aiRadarHistoryRowCount(t, app); count != len(models) {
		t.Fatalf("history read changed stored data: rows = %d, want %d", count, len(models))
	}
}

func TestAIRadarHistoryWindowFiltersByAge(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	// The fixture frames are dated July, so a 24h window must exclude them
	// while "all" must include them.
	upstream := &aiRadarTestUpstream{historyBody: aiRadarSnapshotPayloadJSON(t)}
	app.aiRadarHTTPClient = upstream.client

	var recent AIRadarHistoryResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=24h", nil, admin, http.StatusOK, &recent)
	if recent.Available || len(recent.Series) != 0 {
		t.Fatalf("24h window = %#v, want no series from July fixtures", recent)
	}
	if recent.Series == nil {
		t.Fatal("an empty history must still serialize series as []")
	}

	var all AIRadarHistoryResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=all", nil, admin, http.StatusOK, &all)
	if !all.Available || len(all.Series) != 1 {
		t.Fatalf("all window = %#v, want the backfilled series", all)
	}
}

func TestAIRadarHistoryRejectsUnknownWindow(t *testing.T) {
	app, handler, admin := newAIRadarTestApp(t)
	// The valid windows still reach the one-time backfill, so the upstream is
	// stubbed here too: no test may touch the real snapshot host.
	upstream := &aiRadarTestUpstream{historyBody: aiRadarSnapshotPayloadJSON(t)}
	app.aiRadarHTTPClient = upstream.client

	for _, window := range []string{"", "24h", "7d", "30d", "all"} {
		modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window="+window, nil, admin, http.StatusOK, nil)
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=1y", nil, admin, http.StatusUnprocessableEntity, nil)

	// The stub must be the only host that saw traffic; the real snapshot host is
	// never contacted from a test.
	if upstream.historyRequestCount() == 0 {
		t.Fatal("the accepted windows did not reach the stubbed upstream")
	}
}
