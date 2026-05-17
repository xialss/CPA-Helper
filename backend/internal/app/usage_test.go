package app_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

type usageRecordsResponse struct {
	Items []struct {
		Timestamp string `json:"timestamp"`
	} `json:"items"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type usageSummaryResponse struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func TestUsageRecordsReturnBeijingOffsetTimes(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

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
	seedUsageRecord(t, dataDir, "2026-05-16T16:37:00+08:00")

	const recordsPath = "/api/usage/records?scope=admin&start=2026-05-16T00:00:00&end=2026-05-17T00:00:00"
	records := usageRecordsResponse{}
	requestJSON(t, handler, http.MethodGet, recordsPath, nil, cookies, &records)
	if len(records.Items) != 1 {
		t.Fatalf("usage record count = %d, want 1", len(records.Items))
	}
	if records.Items[0].Timestamp != "2026-05-16T16:37:00+08:00" {
		t.Fatalf("timestamp = %q, want Beijing offset value", records.Items[0].Timestamp)
	}
	if records.Start != "2026-05-16T00:00:00+08:00" || records.End != "2026-05-17T00:00:00+08:00" {
		t.Fatalf("range = %q - %q, want Beijing offset range", records.Start, records.End)
	}

	const summaryPath = "/api/usage/summary?scope=admin&start=2026-05-16T00:00:00&end=2026-05-17T00:00:00"
	summary := usageSummaryResponse{}
	requestJSON(t, handler, http.MethodGet, summaryPath, nil, cookies, &summary)
	if summary.Start != "2026-05-16T00:00:00+08:00" || summary.End != "2026-05-17T00:00:00+08:00" {
		t.Fatalf("summary range = %q - %q, want Beijing offset range", summary.Start, summary.End)
	}
}

func seedUsageRecord(t *testing.T, dataDir string, timestamp string) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(dataDir, "db", "cpa_helper.sqlite3")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider, model,
			endpoint, source, request_id, auth, latency_ms, failed, input_tokens,
			output_tokens, cached_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (
			?, ?, 'admin', 'VSCode', 'openai', 'gpt-5.5', '/v1/chat/completions',
			'code10001', 'req-time-test', 'bearer', 1000, 0, 10, 2, 0, 0, 12,
			'time-test-1', '{"request_id":"req-time-test"}'
		)
	`, timestamp, timestamp)
	if err != nil {
		t.Fatal(err)
	}
}
