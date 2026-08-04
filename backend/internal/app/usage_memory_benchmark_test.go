package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const usageSnapshotBenchmarkEnv = "CPA_HELPER_USAGE_BENCHMARK_DB"
const usageSnapshotScenarioHoldEnv = "CPA_HELPER_USAGE_BENCHMARK_HOLD_SECONDS"
const usageSnapshotAllocationProfileEnv = "CPA_HELPER_USAGE_BENCHMARK_ALLOCS_PROFILE"
const usageSnapshotScenarioModeEnv = "CPA_HELPER_USAGE_BENCHMARK_MODE"
const usageSnapshotLatencySamplesEnv = "CPA_HELPER_USAGE_BENCHMARK_LATENCY_SAMPLES"
const usageSnapshotMaxOpenConnsEnv = "CPA_HELPER_USAGE_BENCHMARK_MAX_OPEN_CONNS"

const usageSnapshotLatencyWarmupCount = 3
const usageSnapshotConcurrentOverviewWorkers = 2

type usageSnapshotScenarioMode string

const (
	usageSnapshotScenarioModeStreaming    usageSnapshotScenarioMode = "streaming"
	usageSnapshotScenarioModeMaterialized usageSnapshotScenarioMode = "materialized"
)

type usageBenchmarkResponseWriter struct {
	header http.Header
	status int
}

func (w *usageBenchmarkResponseWriter) Header() http.Header {
	return w.header
}

func (w *usageBenchmarkResponseWriter) Write(value []byte) (int, error) {
	return len(value), nil
}

func (w *usageBenchmarkResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *usageBenchmarkResponseWriter) reset() {
	w.header = make(http.Header)
	w.status = http.StatusOK
}

func BenchmarkUsageOverviewSnapshot(b *testing.B) {
	snapshotPath := strings.TrimSpace(os.Getenv(usageSnapshotBenchmarkEnv))
	if snapshotPath == "" {
		b.Skipf("set %s to run the opt-in SQLite snapshot benchmark", usageSnapshotBenchmarkEnv)
	}

	snapshot := newUsageSnapshotBenchmarkApp(b, snapshotPath)
	defer snapshot.app.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/usage/overview?include_options=false", nil)
	user := &AuthUser{IsAdmin: true}
	writer := &usageBenchmarkResponseWriter{}
	b.ReportMetric(float64(snapshot.records), "records")
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		writer.reset()
		if err := snapshot.app.usageOverview(writer, request, snapshot.filters, user); err != nil {
			b.Fatalf("usageOverview: %v", err)
		}
	}
}

func TestUsageSnapshotMemoryScenario(t *testing.T) {
	snapshotPath := strings.TrimSpace(os.Getenv(usageSnapshotBenchmarkEnv))
	if snapshotPath == "" {
		t.Skipf("set %s to run the opt-in SQLite snapshot scenario", usageSnapshotBenchmarkEnv)
	}
	hold := usageSnapshotScenarioHold(t)
	mode := usageSnapshotScenarioModeFromEnvironment(t)
	snapshot := newUsageSnapshotBenchmarkApp(t, snapshotPath)
	defer snapshot.app.Close()

	user := &AuthUser{IsAdmin: true}
	fullRequest := httptest.NewRequest(http.MethodGet, "/api/usage/overview?include_options=false", nil)
	summaryRequest := httptest.NewRequest(http.MethodGet, "/api/usage/summary", nil)
	writer := &usageBenchmarkResponseWriter{}
	for index := 0; index < 3; index++ {
		runUsageSnapshotOverview(t, snapshot.app, writer, fullRequest, snapshot.filters, user, mode)
	}

	runUsageSnapshotOverview(t, snapshot.app, writer, fullRequest, snapshot.filters, user, mode)
	for index := 0; index < 12; index++ {
		runUsageSnapshotSummary(t, snapshot.app, writer, summaryRequest, snapshot.filters, user, mode)
	}
	runUsageSnapshotOverview(t, snapshot.app, writer, fullRequest, snapshot.filters, user, mode)
	writeUsageSnapshotAllocationProfile(t)
	t.Logf("usage snapshot scenario ready for sampling: mode=%s records=%d hold=%s", mode, snapshot.records, hold)
	time.Sleep(hold)
}

func TestUsageSnapshotOverviewLatencyScenario(t *testing.T) {
	snapshotPath := strings.TrimSpace(os.Getenv(usageSnapshotBenchmarkEnv))
	if snapshotPath == "" {
		t.Skipf("set %s to run the opt-in SQLite snapshot latency scenario", usageSnapshotBenchmarkEnv)
	}
	sampleCount := usageSnapshotLatencySampleCount(t)
	maxOpenConns := usageSnapshotMaxOpenConns(t)
	snapshot := newUsageSnapshotBenchmarkApp(t, snapshotPath)
	defer snapshot.app.Close()

	// This only changes the copied snapshot application's test connection pool.
	// Runtime SQLite configuration remains SetMaxOpenConns(1) in openRuntimeDB.
	snapshot.app.db.SetMaxOpenConns(maxOpenConns)
	selectedMaxOpenConns := snapshot.app.db.Stats().MaxOpenConnections
	allHistoryFilters, allHistoryRecords := usageSnapshotAllHistoryBenchmarkFilters(t, snapshot.app)
	user := &AuthUser{IsAdmin: true}
	scenarios := []struct {
		name    string
		filters UsageFilters
		records int
	}{
		{name: "latest-30d", filters: snapshot.filters, records: snapshot.records},
		{name: "all-history", filters: allHistoryFilters, records: allHistoryRecords},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/usage/overview?include_options=false", nil)
			writer := &usageBenchmarkResponseWriter{}
			samples := measureUsageSnapshotOverviewLatency(t, snapshot.app, writer, request, scenario.filters, user, sampleCount)
			stats, ok := usageSnapshotLatencyStatistics(samples)
			if !ok {
				t.Fatal("usage overview latency measurement returned no samples")
			}
			t.Logf("usage overview latency: range=%s records=%d count=%d min=%s median=%s p95=%s max=%s db_max_open_conns=%d", scenario.name, scenario.records, stats.Count, stats.Min, stats.Median, stats.P95, stats.Max, selectedMaxOpenConns)
		})
	}

	t.Run("all-history-concurrent-2", func(t *testing.T) {
		samples, waitCount, waitDuration := measureUsageSnapshotConcurrentOverviewLatency(t, snapshot.app, allHistoryFilters, user, sampleCount)
		stats, ok := usageSnapshotLatencyStatistics(samples)
		if !ok {
			t.Fatal("concurrent usage overview latency measurement returned no samples")
		}
		t.Logf("usage overview concurrent latency: range=all-history records=%d count=%d requests_per_round=%d min=%s median=%s p95=%s max=%s db_max_open_conns=%d db_wait_count=%d db_wait_duration=%s errors=0", allHistoryRecords, stats.Count, usageSnapshotConcurrentOverviewWorkers, stats.Min, stats.Median, stats.P95, stats.Max, selectedMaxOpenConns, waitCount, waitDuration)
	})
}

func TestUsageSnapshotPriceMatchCacheCardinality(t *testing.T) {
	snapshotPath := strings.TrimSpace(os.Getenv(usageSnapshotBenchmarkEnv))
	if snapshotPath == "" {
		t.Skipf("set %s to run the opt-in SQLite snapshot diagnostic", usageSnapshotBenchmarkEnv)
	}
	snapshot := newUsageSnapshotBenchmarkApp(t, snapshotPath)
	defer snapshot.app.Close()

	pricing, err := snapshot.app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("load benchmark pricing: %v", err)
	}
	users, err := snapshot.app.userLookup(context.Background(), usageAccessScope{IsAdmin: true})
	if err != nil {
		t.Fatalf("load benchmark users: %v", err)
	}
	collector := newUsageAnalyticsCollector(snapshot.filters, pricing.Prices, pricing.MatchContext, users, usageAnalyticsCollectorOptions{
		Summary:       true,
		Trends:        true,
		Distributions: true,
		Rankings: map[string]string{
			"api_key_description": usageRankingSortTokens,
			"model":               usageRankingSortTokens,
			"user":                usageRankingSortTokens,
		},
	})
	if err := snapshot.app.visitFilteredUsageAnalyticsRecords(context.Background(), snapshot.filters, "timestamp ASC", collector.Add); err != nil {
		t.Fatalf("visit benchmark analytics records: %v", err)
	}
	t.Logf("usage snapshot price match cache: records=%d entries=%d", snapshot.records, len(collector.priceMatches))
}

type usageSnapshotBenchmark struct {
	app     *App
	filters UsageFilters
	records int
}

type usageSnapshotLatencyStats struct {
	Count  int
	Min    time.Duration
	Median time.Duration
	P95    time.Duration
	Max    time.Duration
}

type usageSnapshotConcurrentOverviewResult struct {
	worker int
	err    error
}

func newUsageSnapshotBenchmarkApp(tb testing.TB, snapshotPath string) usageSnapshotBenchmark {
	tb.Helper()

	absoluteSnapshot, err := filepath.Abs(snapshotPath)
	if err != nil {
		tb.Fatalf("resolve snapshot path: %v", err)
	}
	info, err := os.Stat(absoluteSnapshot)
	if err != nil {
		tb.Fatalf("inspect snapshot: %v", err)
	}
	if !info.Mode().IsRegular() {
		tb.Fatalf("snapshot must be a regular SQLite file: %s", absoluteSnapshot)
	}

	dataDir := tb.TempDir()
	databasePath := filepath.Join(dataDir, "db", "cpa_helper.sqlite3")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		tb.Fatalf("create benchmark data directory: %v", err)
	}
	copyUsageSnapshot(tb, absoluteSnapshot, databasePath)
	tb.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, RequireReady: true})
	if err != nil {
		tb.Fatalf("open copied snapshot: %v", err)
	}
	filters, records := usageSnapshotBenchmarkFilters(tb, app)
	return usageSnapshotBenchmark{app: app, filters: filters, records: records}
}

func copyUsageSnapshot(tb testing.TB, sourcePath, targetPath string) {
	tb.Helper()

	source, err := os.Open(sourcePath)
	if err != nil {
		tb.Fatalf("open snapshot: %v", err)
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			tb.Fatalf("close snapshot: %v", closeErr)
		}
	}()

	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		tb.Fatalf("create benchmark snapshot copy: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		tb.Fatalf("copy snapshot: %v", err)
	}
	if err := target.Close(); err != nil {
		tb.Fatalf("close benchmark snapshot copy: %v", err)
	}
}

func usageSnapshotBenchmarkFilters(tb testing.TB, app *App) (UsageFilters, int) {
	tb.Helper()

	_, latestTime := usageSnapshotTimestampBounds(tb, app)
	filters := usageSnapshotLatest30DayFilters(latestTime)

	var records int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_records WHERE timestamp >= ? AND timestamp < ?`, dbTime(*filters.Start), dbTime(*filters.End)).Scan(&records); err != nil {
		tb.Fatalf("count snapshot benchmark records: %v", err)
	}
	if records == 0 {
		tb.Fatal("snapshot has no usage records in the latest 30-day window")
	}
	return filters, records
}

func usageSnapshotAllHistoryBenchmarkFilters(tb testing.TB, app *App) (UsageFilters, int) {
	tb.Helper()

	earliestTime, latestTime := usageSnapshotTimestampBounds(tb, app)
	filters := usageSnapshotAllHistoryFilters(earliestTime, latestTime)
	var records int
	if err := app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_records WHERE timestamp >= ? AND timestamp < ?`, dbTime(*filters.Start), dbTime(*filters.End)).Scan(&records); err != nil {
		tb.Fatalf("count all-history snapshot records: %v", err)
	}
	if records == 0 {
		tb.Fatal("snapshot has no usage records in the all-history window")
	}
	return filters, records
}

func usageSnapshotTimestampBounds(tb testing.TB, app *App) (time.Time, time.Time) {
	tb.Helper()

	var earliest, latest string
	if err := app.db.QueryRowContext(context.Background(), `SELECT CAST(MIN(timestamp) AS TEXT), CAST(MAX(timestamp) AS TEXT) FROM usage_records`).Scan(&earliest, &latest); err != nil {
		tb.Fatalf("read snapshot timestamp bounds: %v", err)
	}
	earliestTime, ok := parseDBTime(earliest)
	if !ok {
		tb.Fatalf("parse snapshot minimum timestamp %q", earliest)
	}
	latestTime, ok := parseDBTime(latest)
	if !ok {
		tb.Fatalf("parse snapshot maximum timestamp %q", latest)
	}
	return earliestTime, latestTime
}

func usageSnapshotLatest30DayFilters(latest time.Time) UsageFilters {
	end := latest.Add(time.Second)
	start := end.AddDate(0, 0, -30)
	return UsageFilters{Start: &start, End: &end}
}

func usageSnapshotAllHistoryFilters(earliest, latest time.Time) UsageFilters {
	end := latest.Add(time.Second)
	start := earliest
	return UsageFilters{Start: &start, End: &end}
}

func usageSnapshotScenarioHold(t *testing.T) time.Duration {
	t.Helper()
	seconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv(usageSnapshotScenarioHoldEnv)))
	if err != nil || seconds <= 0 {
		t.Fatalf("%s must be a positive integer number of seconds", usageSnapshotScenarioHoldEnv)
	}
	return time.Duration(seconds) * time.Second
}

func usageSnapshotLatencySampleCount(t *testing.T) int {
	t.Helper()
	samples, err := strconv.Atoi(strings.TrimSpace(os.Getenv(usageSnapshotLatencySamplesEnv)))
	if err != nil || samples <= 0 {
		t.Fatalf("%s must be a positive integer sample count", usageSnapshotLatencySamplesEnv)
	}
	return samples
}

func usageSnapshotMaxOpenConns(t *testing.T) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(usageSnapshotMaxOpenConnsEnv))
	if value == "" {
		return 1
	}
	connections, err := strconv.Atoi(value)
	if err != nil || connections <= 0 {
		t.Fatalf("%s must be a positive integer when set", usageSnapshotMaxOpenConnsEnv)
	}
	return connections
}

func usageSnapshotScenarioModeFromEnvironment(t *testing.T) usageSnapshotScenarioMode {
	t.Helper()
	mode := usageSnapshotScenarioMode(strings.TrimSpace(os.Getenv(usageSnapshotScenarioModeEnv)))
	switch mode {
	case "", usageSnapshotScenarioModeStreaming:
		return usageSnapshotScenarioModeStreaming
	case usageSnapshotScenarioModeMaterialized:
		return usageSnapshotScenarioModeMaterialized
	default:
		t.Fatalf("%s must be %q or %q", usageSnapshotScenarioModeEnv, usageSnapshotScenarioModeStreaming, usageSnapshotScenarioModeMaterialized)
		return ""
	}
}

func writeUsageSnapshotAllocationProfile(t *testing.T) {
	t.Helper()
	profilePath := strings.TrimSpace(os.Getenv(usageSnapshotAllocationProfileEnv))
	if profilePath == "" {
		return
	}
	profile := pprof.Lookup("allocs")
	if profile == nil {
		t.Fatal("allocation profile is unavailable")
	}
	file, err := os.OpenFile(profilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create allocation profile: %v", err)
	}
	if err := profile.WriteTo(file, 0); err != nil {
		_ = file.Close()
		t.Fatalf("write allocation profile: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close allocation profile: %v", err)
	}
}

func runUsageSnapshotOverview(t *testing.T, app *App, writer *usageBenchmarkResponseWriter, request *http.Request, filters UsageFilters, user *AuthUser, mode usageSnapshotScenarioMode) {
	t.Helper()
	writer.reset()
	if mode == usageSnapshotScenarioModeMaterialized {
		runUsageSnapshotMaterializedOverview(t, app, writer, filters, user)
		return
	}
	if err := app.usageOverview(writer, request, filters, user); err != nil {
		t.Fatalf("usageOverview: %v", err)
	}
}

func measureUsageSnapshotOverviewLatency(t *testing.T, app *App, writer *usageBenchmarkResponseWriter, request *http.Request, filters UsageFilters, user *AuthUser, sampleCount int) []time.Duration {
	t.Helper()
	for index := 0; index < usageSnapshotLatencyWarmupCount; index++ {
		writer.reset()
		if err := app.usageOverview(writer, request, filters, user); err != nil {
			t.Fatalf("warm usageOverview: %v", err)
		}
	}

	samples := make([]time.Duration, 0, sampleCount)
	for index := 0; index < sampleCount; index++ {
		writer.reset()
		started := time.Now()
		if err := app.usageOverview(writer, request, filters, user); err != nil {
			t.Fatalf("measure usageOverview: %v", err)
		}
		samples = append(samples, time.Since(started))
	}
	return samples
}

func measureUsageSnapshotConcurrentOverviewLatency(t *testing.T, app *App, filters UsageFilters, user *AuthUser, sampleCount int) ([]time.Duration, int64, time.Duration) {
	t.Helper()
	for index := 0; index < usageSnapshotLatencyWarmupCount; index++ {
		_, results := runUsageSnapshotConcurrentOverviewRound(app, filters, user)
		assertUsageSnapshotConcurrentOverviewSuccess(t, "warmup", index+1, results)
	}

	before := app.db.Stats()
	samples := make([]time.Duration, 0, sampleCount)
	for index := 0; index < sampleCount; index++ {
		wallClock, results := runUsageSnapshotConcurrentOverviewRound(app, filters, user)
		assertUsageSnapshotConcurrentOverviewSuccess(t, "measurement", index+1, results)
		t.Logf("usage overview concurrent round: round=%d wall_clock=%s errors=0", index+1, wallClock)
		samples = append(samples, wallClock)
	}
	after := app.db.Stats()
	return samples, after.WaitCount - before.WaitCount, after.WaitDuration - before.WaitDuration
}

func runUsageSnapshotConcurrentOverviewRound(app *App, filters UsageFilters, user *AuthUser) (time.Duration, []usageSnapshotConcurrentOverviewResult) {
	ready := make(chan struct{}, usageSnapshotConcurrentOverviewWorkers)
	start := make(chan struct{})
	results := make(chan usageSnapshotConcurrentOverviewResult, usageSnapshotConcurrentOverviewWorkers)
	var workers sync.WaitGroup
	for index := 0; index < usageSnapshotConcurrentOverviewWorkers; index++ {
		worker := index
		workers.Add(1)
		go func() {
			defer workers.Done()
			writer := &usageBenchmarkResponseWriter{}
			request := httptest.NewRequest(http.MethodGet, "/api/usage/overview?include_options=false", nil)
			userCopy := *user
			ready <- struct{}{}
			<-start
			writer.reset()
			results <- usageSnapshotConcurrentOverviewResult{
				worker: worker,
				err:    app.usageOverview(writer, request, filters, &userCopy),
			}
		}()
	}
	for index := 0; index < usageSnapshotConcurrentOverviewWorkers; index++ {
		<-ready
	}
	started := time.Now()
	close(start)
	workers.Wait()
	wallClock := time.Since(started)
	close(results)

	failed := make([]usageSnapshotConcurrentOverviewResult, 0)
	for result := range results {
		if result.err != nil {
			failed = append(failed, result)
		}
	}
	return wallClock, failed
}

func assertUsageSnapshotConcurrentOverviewSuccess(t *testing.T, phase string, round int, results []usageSnapshotConcurrentOverviewResult) {
	t.Helper()
	if len(results) == 0 {
		return
	}
	for _, result := range results {
		t.Errorf("concurrent usageOverview %s round=%d worker=%d: %v", phase, round, result.worker, result.err)
	}
	t.FailNow()
}

func usageSnapshotLatencyStatistics(samples []time.Duration) (usageSnapshotLatencyStats, bool) {
	if len(samples) == 0 {
		return usageSnapshotLatencyStats{}, false
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left] < ordered[right]
	})
	return usageSnapshotLatencyStats{
		Count:  len(ordered),
		Min:    ordered[0],
		Median: usageSnapshotMedian(ordered),
		P95:    usageSnapshotSortedPercentile(ordered, 95),
		Max:    ordered[len(ordered)-1],
	}, true
}

func usageSnapshotMedian(ordered []time.Duration) time.Duration {
	middle := len(ordered) / 2
	if len(ordered)%2 != 0 {
		return ordered[middle]
	}
	return ordered[middle-1] + (ordered[middle]-ordered[middle-1])/2
}

func usageSnapshotPercentile(samples []time.Duration, percentile int) (time.Duration, bool) {
	if len(samples) == 0 || percentile <= 0 || percentile > 100 {
		return 0, false
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left] < ordered[right]
	})
	return usageSnapshotSortedPercentile(ordered, percentile), true
}

func usageSnapshotSortedPercentile(ordered []time.Duration, percentile int) time.Duration {
	rank := (len(ordered)*percentile + 99) / 100
	return ordered[rank-1]
}

func runUsageSnapshotSummary(t *testing.T, app *App, writer *usageBenchmarkResponseWriter, request *http.Request, filters UsageFilters, user *AuthUser, mode usageSnapshotScenarioMode) {
	t.Helper()
	writer.reset()
	if mode == usageSnapshotScenarioModeMaterialized {
		runUsageSnapshotMaterializedSummary(t, app, writer, filters, user)
		return
	}
	if err := app.usageSummary(writer, request, filters, user); err != nil {
		t.Fatalf("usageSummary: %v", err)
	}
}

func runUsageSnapshotMaterializedOverview(t *testing.T, app *App, writer *usageBenchmarkResponseWriter, filters UsageFilters, user *AuthUser) {
	t.Helper()
	scope := accessScope(user, filters.Scope)
	scoped, err := app.scopedFilters(context.Background(), normalizedUsageFilters(filters), scope)
	if err != nil {
		t.Fatalf("scope benchmark overview filters: %v", err)
	}
	records, err := app.filteredUsageAnalyticsRecords(context.Background(), scoped, "timestamp ASC")
	if err != nil {
		t.Fatalf("load benchmark overview records: %v", err)
	}
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("load benchmark overview pricing: %v", err)
	}
	users, err := app.userLookup(context.Background(), scope)
	if err != nil {
		t.Fatalf("load benchmark overview users: %v", err)
	}
	apiKeyRanking := rankingFromRecordsBySort(records, pricing.Prices, "api_key_description", users, usageRankingSortTokens, pricing.MatchContext)
	userRanking := map[string]any{"group_by": "user", "items": []any{}}
	if scope.IsAdmin {
		userRanking = rankingFromRecordsBySort(records, pricing.Prices, "user", users, usageRankingSortTokens, pricing.MatchContext)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"summary":                     usageSummaryFromRecords(scoped, records, pricing.Prices, pricing.MatchContext),
		"trends":                      trendPointsFromRecords(scoped, records, pricing.Prices, pricing.MatchContext),
		"user_ranking":                userRanking,
		"api_key_description_ranking": apiKeyRanking,
		"api_key_ranking":             apiKeyRanking,
		"model_ranking":               rankingFromRecordsBySort(records, pricing.Prices, "model", users, usageRankingSortTokens, pricing.MatchContext),
		"distributions":               distributionsFromRecords(records, pricing.Prices, pricing.MatchContext),
	})
}

func runUsageSnapshotMaterializedSummary(t *testing.T, app *App, writer *usageBenchmarkResponseWriter, filters UsageFilters, user *AuthUser) {
	t.Helper()
	scope := accessScope(user, filters.Scope)
	scoped, err := app.scopedFilters(context.Background(), normalizedUsageFilters(filters), scope)
	if err != nil {
		t.Fatalf("scope benchmark summary filters: %v", err)
	}
	records, err := app.filteredUsageAnalyticsRecords(context.Background(), scoped, "")
	if err != nil {
		t.Fatalf("load benchmark summary records: %v", err)
	}
	pricing, err := app.billingPriceIndex(context.Background())
	if err != nil {
		t.Fatalf("load benchmark summary pricing: %v", err)
	}
	writeJSON(writer, http.StatusOK, usageSummaryFromRecords(scoped, records, pricing.Prices, pricing.MatchContext))
}

func TestUsageSnapshotPercentile(t *testing.T) {
	samples := []time.Duration{50 * time.Millisecond, 10 * time.Millisecond, 40 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	original := append([]time.Duration(nil), samples...)

	median, ok := usageSnapshotPercentile(samples, 50)
	if !ok || median != 30*time.Millisecond {
		t.Fatalf("median percentile = %s, %t; want 30ms, true", median, ok)
	}
	p95, ok := usageSnapshotPercentile(samples, 95)
	if !ok || p95 != 50*time.Millisecond {
		t.Fatalf("p95 percentile = %s, %t; want 50ms, true", p95, ok)
	}
	for index := range samples {
		if samples[index] != original[index] {
			t.Fatalf("percentile mutated input at index %d: got %s want %s", index, samples[index], original[index])
		}
	}
	if _, ok := usageSnapshotPercentile(nil, 95); ok {
		t.Fatal("empty samples unexpectedly produced a percentile")
	}
}

func TestUsageSnapshotLatencyStatistics(t *testing.T) {
	stats, ok := usageSnapshotLatencyStatistics([]time.Duration{80 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 60 * time.Millisecond})
	if !ok {
		t.Fatal("latency statistics unexpectedly rejected samples")
	}
	if stats.Count != 4 || stats.Min != 20*time.Millisecond || stats.Median != 50*time.Millisecond || stats.P95 != 80*time.Millisecond || stats.Max != 80*time.Millisecond {
		t.Fatalf("latency statistics = %+v; want count=4 min=20ms median=50ms p95=80ms max=80ms", stats)
	}
	if _, ok := usageSnapshotLatencyStatistics(nil); ok {
		t.Fatal("empty samples unexpectedly produced latency statistics")
	}
}

func TestUsageSnapshotMaxOpenConns(t *testing.T) {
	t.Run("defaults to one", func(t *testing.T) {
		t.Setenv(usageSnapshotMaxOpenConnsEnv, "")
		if got := usageSnapshotMaxOpenConns(t); got != 1 {
			t.Fatalf("default snapshot max open connections = %d, want 1", got)
		}
	})
	t.Run("accepts explicit measurement value", func(t *testing.T) {
		t.Setenv(usageSnapshotMaxOpenConnsEnv, "2")
		if got := usageSnapshotMaxOpenConns(t); got != 2 {
			t.Fatalf("configured snapshot max open connections = %d, want 2", got)
		}
	})
}

func TestUsageSnapshotRangeFilters(t *testing.T) {
	earliest := time.Date(2025, time.January, 5, 8, 9, 10, 0, appTimeLocation)
	latest := time.Date(2025, time.February, 10, 11, 12, 13, 456000000, appTimeLocation)
	wantEnd := latest.Add(time.Second)

	latest30Days := usageSnapshotLatest30DayFilters(latest)
	if latest30Days.Start == nil || latest30Days.End == nil {
		t.Fatal("latest 30-day filters must include explicit bounds")
	}
	if !latest30Days.Start.Equal(wantEnd.AddDate(0, 0, -30)) || !latest30Days.End.Equal(wantEnd) {
		t.Fatalf("latest 30-day filters = [%v, %v); want [%v, %v)", latest30Days.Start, latest30Days.End, wantEnd.AddDate(0, 0, -30), wantEnd)
	}

	allHistory := usageSnapshotAllHistoryFilters(earliest, latest)
	if allHistory.Start == nil || allHistory.End == nil {
		t.Fatal("all-history filters must include explicit bounds")
	}
	if !allHistory.Start.Equal(earliest) || !allHistory.End.Equal(wantEnd) {
		t.Fatalf("all-history filters = [%v, %v); want [%v, %v)", allHistory.Start, allHistory.End, earliest, wantEnd)
	}
}

var _ http.ResponseWriter = (*usageBenchmarkResponseWriter)(nil)
