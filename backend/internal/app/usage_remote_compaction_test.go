package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func compactionTestTrace(at time.Time, n int) string {
	stamp := fmt.Sprintf("%012x", at.UnixMilli())
	return fmt.Sprintf("%s-%s-7000-8000-%012d", stamp[:8], stamp[8:], n)
}

func compactionTestLog(at time.Time, body, downstreamHeaders string) string {
	return "=== REQUEST INFO ===\nVersion: 8.0.47\nURL: /v1/responses\nMethod: POST\nDownstream Transport: http\nUpstream Transport: http\nTimestamp: " + at.Format(time.RFC3339Nano) + "\n\n\n=== HEADERS ===\nAuthorization: Be****nt\n\n\n=== REQUEST BODY ===\n" + body + "\n\n\n=== API REQUEST 1 ===\nAuth: synthetic\nBody:\n{\"model\":\"synthetic\"}\n\n=== API RESPONSE 1 ===\nBody:\ndata: {\"type\":\"response.compaction.compacting\"}\n\n=== RESPONSE ===\nStatus: 200\n" + downstreamHeaders + "\nsynthetic response\n"
}

func TestRemoteCompactionNativeLogParser(t *testing.T) {
	started := time.Now().UTC().Truncate(time.Millisecond)
	logged := started.Add(300 * time.Microsecond)
	queued := logged.Add(time.Millisecond)
	trace := compactionTestTrace(started, 1)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"trigger", `{"input":[{"type":"compaction_trigger"}]}`, true},
		{"history-only", `{"input":[{"type":"compaction","encrypted_content":"synthetic"}]}`, false},
		{"text-and-nested", `{"input":[{"type":"message","content":[{"type":"compaction_trigger"}],"text":"compaction_trigger"}],"metadata":{"input":[{"type":"compaction_trigger"}]}}`, false},
		{"string-input", `{"input":"compaction_trigger"}`, false},
		{"wrong-type", `{"input":[{"type":true},{"Type":"compaction_trigger"}]}`, false},
		{"large-prompt-before-trigger", `{"input":[{"role":"user","content":"` + strings.Repeat("synthetic ", usageLogPreviewBytes/4) + `"},{"type":"compaction_trigger"}]}`, true},
		{"multiline", "{\n\"input\": [\n{\"type\":\"compaction_trigger\"}\n]}\n", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseRemoteCompactionLog(strings.NewReader(compactionTestLog(logged, test.body, "")), trace, queued)
			if err != nil || got != test.want {
				t.Fatalf("classification=%t, error=%v, want %t", got, err, test.want)
			}
		})
	}
	fullTrace := "X-CPA-TRACE-ID: 20261030000000-synthetic-auth-" + trace + "\n"
	if positive, err := parseRemoteCompactionLog(strings.NewReader(compactionTestLog(logged, `{"input":[{"type":"compaction_trigger"}]}`, fullTrace)), trace, queued); err != nil || !positive {
		t.Fatal("matching native full trace was rejected", positive, err)
	}
	base := compactionTestLog(logged, `{"input":[{"type":"compaction_trigger"}]}`, "")
	for name, log := range map[string]string{
		"truncated-body":         strings.Split(base, "=== REQUEST BODY ===")[0] + "=== REQUEST BODY ===\n{\"input\":[{\"type\":\"compaction_trigger\"}]",
		"truncated-finalization": strings.Split(base, "=== RESPONSE ===")[0],
		"foreign-native-trace":   compactionTestLog(logged, `{"input":[{"type":"compaction_trigger"}]}`, "X-CPA-TRACE-ID: 20261030000000-auth-"+compactionTestTrace(started, 2)+"\n"),
		"wrong-path":             strings.Replace(base, "URL: /v1/responses", "URL: /v1/chat/completions", 1),
		"wrong-method":           strings.Replace(base, "Method: POST", "Method: GET", 1),
		"websocket":              strings.Replace(base, "Downstream Transport: http", "Downstream Transport: websocket", 1),
		"upgrade":                strings.Replace(base, "Authorization:", "Upgrade: websocket\nAuthorization:", 1),
		"log-before-trace":       compactionTestLog(started.Add(-time.Millisecond), `{"input":[{"type":"compaction_trigger"}]}`, ""),
		"log-after-queue":        compactionTestLog(queued.Add(time.Millisecond), `{"input":[{"type":"compaction_trigger"}]}`, ""),
		"second-json-body":       compactionTestLog(logged, `{"input":[{"type":"compaction_trigger"}]} {"other":true}`, ""),
		"duplicate-input":        compactionTestLog(logged, `{"input":[{"type":"compaction_trigger"}],"input":[]}`, ""),
		"duplicate-item-type":    compactionTestLog(logged, `{"input":[{"type":"compaction_trigger","type":"message"}]}`, ""),
	} {
		t.Run(name, func(t *testing.T) {
			if positive, err := parseRemoteCompactionLog(strings.NewReader(log), trace, queued); err == nil || positive {
				t.Fatal("incomplete or mismatched evidence accepted", positive, err)
			}
		})
	}
}

func TestRemoteCompactionNativeLogBeforeUpstreamFailure(t *testing.T) {
	started := time.Now().UTC().Truncate(time.Millisecond)
	logged, queued := started.Add(time.Microsecond), started.Add(time.Millisecond)
	trace := compactionTestTrace(started, 1)
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{"trigger", `{"input":[{"type":"compaction_trigger"}]}`, true},
		{"history-only", `{"input":[{"type":"compaction"}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Exact native writeAPIErrorResponses/writeResponseSection layout;
			// no API REQUEST exists when validation failed before upstream IO.
			prefix := strings.Split(compactionTestLog(logged, test.body, ""), "=== API REQUEST 1 ===")[0]
			errorSection := "=== API ERROR RESPONSE ===\nHTTP Status: 400\nsynthetic validation failure mentioning compaction_trigger\n\n\n"
			complete := prefix + errorSection + "=== RESPONSE ===\nStatus: 400\nContent-Type: application/json\n\n{\"error\":\"synthetic validation failure\"}\n"
			positive, err := parseRemoteCompactionLog(strings.NewReader(complete), trace, queued)
			if err != nil || positive != test.want {
				t.Fatal("complete failed request lost its original intent", positive, err)
			}
			if positive, err := parseRemoteCompactionLog(strings.NewReader(prefix+errorSection), trace, queued); err == nil || positive {
				t.Fatal("unfinished error log accepted", positive, err)
			}
		})
	}
}

func TestRemoteCompactionNativeBatchPreservesAccountingAndDetails(t *testing.T) {
	a, owner, pricing := newCollectorTTLApp(t)
	ctx := context.Background()
	started := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	logged, queued, received := started.Add(time.Microsecond), started.Add(time.Millisecond), time.Now()
	var batch []string
	for n := 1; n <= 2; n++ {
		batch = append(batch, collectorTTLMessage(t, collectorTTLID(700+n), queued, func(raw map[string]any) {
			raw["request_id"] = compactionTestTrace(started, n)
			raw["endpoint"] = "POST /v1/responses"
			raw["is_compaction"] = true // Historical DAG status does not identify this request.
		}))
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-log-management" || !strings.HasPrefix(r.URL.Path, "/v0/management/request-log-by-id/") {
			t.Error("native authenticated log request expected")
			w.WriteHeader(500)
			return
		}
		if collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`) != 2 {
			t.Error("log request ran before original billing committed")
		}
		body := `{"input":[{"type":"compaction","encrypted_content":"synthetic"}]}`
		if strings.HasSuffix(r.URL.Path, compactionTestTrace(started, 1)) {
			body = `{"input":[{"type":"compaction"},{"type":"compaction_trigger"}]}`
		}
		_, _ = w.Write([]byte(compactionTestLog(logged, body, "")))
	}))
	defer server.Close()
	cfg := AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "synthetic-log-management"}}
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, batch, received); err != nil {
		t.Fatal(err)
	}
	if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, time.Now()); err != nil || count != 2 {
		t.Fatal("original accounting failed", count, err)
	}
	before := collectorTTLRecord(t, a, batch[0])
	reader := newRemoteCompactionReader(ctx, a)
	defer reader.stop()
	if err := reader.scheduleBatch(ctx, cfg.Collector, batch, received); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !usageRemoteCompaction(collectorTTLRecord(t, a, batch[0])) {
		if time.Now().After(deadline) {
			t.Fatal("native positive marker did not persist")
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := collectorTTLRecord(t, a, batch[0])
	if usageRemoteCompaction(collectorTTLRecord(t, a, batch[1])) {
		t.Fatal("history-only request acquired a marker")
	}
	before.RawJSON = after.RawJSON
	if !reflect.DeepEqual(before, after) {
		t.Fatal("native label changed billing or identity fields")
	}
	if strings.Contains(after.RawJSON, "encrypted_content") || strings.Contains(after.RawJSON, "synthetic response") {
		t.Fatal("log body retained in usage storage")
	}
	assertCollectorTTLCharge(t, a, after.ID, 2, false)
	duplicateAt := received.Add(time.Second)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, batch, duplicateAt); err != nil {
		t.Fatal(err)
	}
	if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, duplicateAt); err != nil {
		t.Fatal(err)
	}
	if err := reader.scheduleBatch(ctx, cfg.Collector, batch, duplicateAt); err != nil {
		t.Fatal(err)
	}
	reader.stop()
	if calls.Load() != 2 || collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`) != 2 {
		t.Fatal("duplicate caused log reread or quota replay", calls.Load())
	}
	index := "collector-ttl-auth"
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{Brand: aiProviderBrandClaude, AuthIndex: &index, Models: []aiProviderModel{{Name: "claude-test"}}}})
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/usage/records/%d", after.ID), nil)
	w := httptest.NewRecorder()
	if err := a.usageRecordDetail(w, request, after.ID, &AuthUser{ID: owner, Username: "collector-ttl-owner"}); err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["remote_compaction"] != true || detail["model"] != "claude-test" || detail["estimated_cost_usd"] != float64(2) {
		t.Fatal("native detail label or billing incorrect")
	}
	if calls.Load() != 2 {
		t.Fatal("details fetched logs")
	}
	if err := a.usageRecordDetail(httptest.NewRecorder(), request, after.ID, &AuthUser{ID: owner + 1, Username: "another-user"}); err == nil {
		t.Fatal("label bypassed record ownership")
	}
}

func TestRemoteCompactionNativeLogDelayedAndCancelled(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	started := time.Now().UTC().Truncate(time.Millisecond)
	trace := compactionTestTrace(started, 10)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/request-log" {
			_, _ = w.Write([]byte(`{"request-log":true}`))
			return
		}
		switch attempts.Add(1) {
		case 1:
			w.WriteHeader(404)
			return
		case 2:
			_, _ = w.Write([]byte("=== REQUEST INFO ===\n"))
			return
		}
		_, _ = w.Write([]byte(compactionTestLog(started.Add(time.Microsecond), `{"input":[]}`, "")))
	}))
	defer server.Close()
	candidate := remoteCompactionCandidate{RequestID: trace, QueuedAt: started.Add(time.Millisecond), Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "synthetic"}}
	if err := a.readRemoteCompaction(context.Background(), candidate); err != nil || attempts.Load() != 3 {
		t.Fatal("newly finalized log was not read", attempts.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := newRemoteCompactionReader(ctx, a)
	reader.jobs <- candidate
	cancel()
	reader.stop()
	if _, err := parseRemoteCompactionLog(compactionContextReader{ctx, strings.NewReader("not-read")}, trace, candidate.QueuedAt); err == nil {
		t.Fatal("cancelled parse continued")
	}
}

func TestRemoteCompactionNativeLogRetryExhaustion(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	started := time.Now().UTC().Truncate(time.Millisecond)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/request-log" {
			_, _ = w.Write([]byte(`{"request-log":true}`))
			return
		}
		attempts.Add(1)
		w.WriteHeader(404)
	}))
	defer server.Close()
	err := a.readRemoteCompaction(context.Background(), remoteCompactionCandidate{RequestID: compactionTestTrace(started, 1), QueuedAt: started.Add(time.Millisecond), Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "synthetic"}})
	if err == nil || !strings.Contains(err.Error(), "after four attempts") || attempts.Load() != 4 {
		t.Fatal("missing log retry did not terminate explicitly", attempts.Load(), err)
	}
}

func TestRemoteCompactionNativeReaderStopCancelsActiveIO(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	reader := newRemoteCompactionReader(context.Background(), a)
	reader.jobs <- remoteCompactionCandidate{RequestID: compactionTestTrace(time.Now(), 1), Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "synthetic"}}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		reader.stop()
		t.Fatal("worker never began its log request")
	}
	done := make(chan struct{})
	go func() { reader.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader shutdown did not cancel and drain active IO")
	}
}

func TestRemoteCompactionNativeProvenanceIsNotQueueData(t *testing.T) {
	trace := compactionTestTrace(time.Now(), 1)
	marker, err := json.Marshal(remoteCompactionMarker{1, remoteCompactionLogSource, trace, true})
	if err != nil {
		t.Fatal(err)
	}
	object := map[string]json.RawMessage{"request_id": json.RawMessage(fmt.Sprintf("%q", trace)), remoteCompactionMetadataField: marker}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(normalized.RawJSON, remoteCompactionMetadataField) || usageRemoteCompaction(UsageRecord{RequestID: &trace, RawJSON: normalized.RawJSON}) {
		t.Fatal("queue data supplied server-owned provenance")
	}
	object[remoteCompactionMetadataField] = json.RawMessage(fmt.Sprintf(`{"version":1,"source":"cpa-helper-plugin","request_id":%q,"remote_compaction":true}`, trace))
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if usageRemoteCompaction(UsageRecord{RequestID: &trace, RawJSON: string(raw)}) {
		t.Fatal("retired Plugin provenance was accepted")
	}
}

func TestRemoteCompactionNativeFailuresAndSpoolCleanup(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	t.Setenv("TMP", t.TempDir())
	t.Setenv("TEMP", os.Getenv("TMP"))
	t.Setenv("TMPDIR", os.Getenv("TMP"))
	started := time.Now().UTC().Truncate(time.Millisecond)
	for _, mode := range []string{"disabled", "denied", "unavailable", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "disabled" {
					if r.URL.Path == "/v0/management/request-log" {
						_, _ = w.Write([]byte(`{"request-log":false}`))
					} else {
						w.WriteHeader(404)
					}
					return
				}
				switch mode {
				case "denied":
					w.WriteHeader(401)
				case "unavailable":
					w.WriteHeader(503)
				case "malformed":
					_, _ = w.Write([]byte("incomplete synthetic log"))
				}
			}))
			defer server.Close()
			candidate := remoteCompactionCandidate{RequestID: compactionTestTrace(started, 1), QueuedAt: started.Add(time.Millisecond), Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "synthetic"}}
			if positive, err := a.inspectRemoteCompactionLog(context.Background(), candidate); err == nil || positive {
				t.Fatal("unavailable log classified", positive, err)
			}
			files, err := filepath.Glob(filepath.Join(os.TempDir(), "cpa-helper-log-*.log"))
			if err != nil || len(files) != 0 {
				t.Fatal("temporary log retained", files, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.readRemoteCompaction(ctx, remoteCompactionCandidate{}); err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancelled read succeeded")
	}
}

func TestRemoteCompactionNativeReaderBoundedBacklog(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	ctx := context.Background()
	started, received := time.Now().Add(-time.Second).Truncate(time.Millisecond), time.Now()
	message := collectorTTLMessage(t, collectorTTLID(799), started.Add(time.Millisecond), func(raw map[string]any) {
		raw["endpoint"] = "POST /v1/responses"
		raw["request_id"] = compactionTestTrace(started, 99)
	})
	collector := CollectorConfig{CLIProxyURL: "http://127.0.0.1:12345", ManagementKey: "synthetic"}
	if err := a.persistCollectorUsageBatch(ctx, collector, []string{message}, received); err != nil {
		t.Fatal(err)
	}
	if _, err := a.processCollectorUsageInbox(ctx, AppConfig{Collector: collector}, pricing, time.Now()); err != nil {
		t.Fatal(err)
	}
	// An already-full queue cannot hold up the collector or spawn more readers.
	reader := &remoteCompactionReader{app: a, ctx: ctx, jobs: make(chan remoteCompactionCandidate, 1)}
	reader.jobs <- remoteCompactionCandidate{}
	if err := reader.scheduleBatch(ctx, collector, []string{message}, received); err == nil || !strings.Contains(err.Error(), "backlog is full") {
		t.Fatal("backlog exhaustion was hidden", err)
	}
}
