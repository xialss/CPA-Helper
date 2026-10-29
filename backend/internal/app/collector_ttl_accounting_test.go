package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectorTTLRunnerRetainsDequeuedBatchOnInboxFailure(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw := collectorTTLMessage(t, collectorTTLID(80), time.Now(), func(raw map[string]any) { raw["tokens"].(map[string]any)["cache_creation_tokens"] = 0 })
	var calls atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/usage-queue":
			if calls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode([]string{raw})
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		case "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer cpa.Close()
	cfg, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector = CollectorConfig{Enabled: true, CLIProxyURL: cpa.URL, ManagementKey: "test-management", BatchSize: 10, PollIntervalSeconds: .2, RetryIntervalSeconds: 1}
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER fail_inbox_persist BEFORE INSERT ON usage_collector_inbox BEGIN SELECT RAISE(ABORT, 'inbox unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	runner := NewCollectorRunner(a)
	runner.Start()
	defer runner.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for {
		state, err := a.collectorState(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.LastError != nil && strings.Contains(*state.LastError, "persist dequeued usage batch") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector did not report inbox persistence failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("collector dequeued another batch while original was not durable: %d", calls.Load())
	}
	if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`); count != 0 {
		t.Fatalf("failed persistence counted usage: %d", count)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_inbox_persist`); err != nil {
		t.Fatal(err)
	}
	for collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_records`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("collector did not retry retained original batch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.Stop()
	record := collectorTTLRecord(t, a, raw)
	if record.RequestID == nil || !strings.Contains(*record.RequestID, collectorTTLID(80)) {
		t.Fatal("retry processed a different input")
	}
	if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox`); count != 1 {
		t.Fatalf("retry inserted duplicate inbox receipts: %d", count)
	}
}
