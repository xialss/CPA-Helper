package app_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

type usageRecordsResponse struct {
	Items []struct {
		Timestamp string  `json:"timestamp"`
		ID        int     `json:"id"`
		Source    string  `json:"source"`
		AuthIndex *string `json:"auth_index"`
	} `json:"items"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type usageRecordDetailResponse struct {
	Source    string         `json:"source"`
	AuthIndex *string        `json:"auth_index"`
	RawJSON   map[string]any `json:"raw_json"`
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

func TestUsageResponsesRedactAPIKeySourceWithoutChangingStoredRecord(t *testing.T) {
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

	const source = "test-api-key-source-redaction-secret-value"
	id := seedUsageRecordWithValues(t, dataDir, usageRecordSeed{
		Timestamp:    "2026-05-16T16:37:00+08:00",
		Username:     "admin",
		Source:       source,
		RequestID:    "req-api-key-source",
		Auth:         "apikey",
		DedupeKey:    "api-key-source-test",
		RawJSON:      `{"auth_type":"apikey","source":"` + source + `","api_key":"` + source + `","request_id":"req-api-key-source"}`,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	})

	records := usageRecordsResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records?scope=admin&start=2026-05-16T00:00:00&end=2026-05-17T00:00:00&page=1&page_size=1", nil, cookies, &records)
	if len(records.Items) != 1 {
		t.Fatalf("usage record count = %d, want 1", len(records.Items))
	}
	if records.Items[0].Source == source || !strings.Contains(records.Items[0].Source, "...") {
		t.Fatalf("list source = %q, want masked API key", records.Items[0].Source)
	}

	detail := usageRecordDetailResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records/"+strconv.Itoa(id)+"?scope=admin", nil, cookies, &detail)
	if detail.Source == source || !strings.Contains(detail.Source, "...") {
		t.Fatalf("detail source = %q, want masked API key", detail.Source)
	}
	if got, _ := detail.RawJSON["source"].(string); got == source || !strings.Contains(got, "...") {
		t.Fatalf("raw_json.source = %q, want masked API key", got)
	}
	if got, _ := detail.RawJSON["api_key"].(string); got == source || !strings.Contains(got, "...") {
		t.Fatalf("raw_json.api_key = %q, want masked API key", got)
	}

	storedSource, storedRaw := storedUsageSourceAndRawJSON(t, dataDir, id)
	if storedSource != source {
		t.Fatalf("stored source = %q, want original", storedSource)
	}
	if !strings.Contains(storedRaw, source) {
		t.Fatalf("stored raw_json = %q, want original API key", storedRaw)
	}
}

func TestUsageDetailRedactsSourceWhenRawJSONAuthTypeIsAPIKey(t *testing.T) {
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

	const source = "test-api-key-source-redaction-secret-value"
	id := seedUsageRecordWithValues(t, dataDir, usageRecordSeed{
		Timestamp:    "2026-05-16T16:37:00+08:00",
		Username:     "admin",
		Source:       source,
		RequestID:    "req-raw-auth-type-api-key",
		Auth:         "oauth",
		DedupeKey:    "raw-auth-type-api-key-test",
		RawJSON:      `{"auth_type":"apikey","source":"` + source + `","request_id":"req-raw-auth-type-api-key"}`,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	})

	detail := usageRecordDetailResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records/"+strconv.Itoa(id)+"?scope=admin", nil, cookies, &detail)
	if detail.Source == source || !strings.Contains(detail.Source, "...") {
		t.Fatalf("detail source = %q, want masked API key source for admin", detail.Source)
	}
	if got, _ := detail.RawJSON["source"].(string); got == source || !strings.Contains(got, "...") {
		t.Fatalf("raw_json.source = %q, want masked API key source for admin", got)
	}
}

func TestUsageRecordDetailRedactsAccountSourceForNonAdminOnly(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	adminCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPost, "/api/users", map[string]any{
		"username": "member",
		"password": "member-password",
		"nickname": "Member",
		"is_admin": false,
	}, adminCookies, nil)
	memberCookies := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "member",
		"password": "member-password",
	}, nil, nil)

	const source = "codex-member@example.com"
	const authIndex = "319a3ed7ef9c3080"
	id := seedUsageRecordWithValues(t, dataDir, usageRecordSeed{
		Timestamp:    "2026-05-16T16:37:00+08:00",
		Username:     "member",
		Source:       source,
		RequestID:    "req-account-source",
		Auth:         "oauth",
		DedupeKey:    "account-source-test",
		RawJSON:      `{"auth_type":"oauth","auth_index":"` + authIndex + `","source":"` + source + `","request_id":"req-account-source"}`,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	})

	adminDetail := usageRecordDetailResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records/"+strconv.Itoa(id)+"?scope=admin", nil, adminCookies, &adminDetail)
	if adminDetail.Source != source {
		t.Fatalf("admin detail source = %q, want original account source", adminDetail.Source)
	}
	if got, _ := adminDetail.RawJSON["source"].(string); got != source {
		t.Fatalf("admin raw_json.source = %q, want original account source", got)
	}
	if adminDetail.AuthIndex == nil || *adminDetail.AuthIndex != authIndex {
		t.Fatalf("admin detail auth_index = %v, want original auth index", adminDetail.AuthIndex)
	}
	if got, _ := adminDetail.RawJSON["auth_index"].(string); got != authIndex {
		t.Fatalf("admin raw_json.auth_index = %q, want original auth index", got)
	}

	memberDetail := usageRecordDetailResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records/"+strconv.Itoa(id)+"?scope=account", nil, memberCookies, &memberDetail)
	if memberDetail.Source == source || !strings.Contains(memberDetail.Source, "...") {
		t.Fatalf("member detail source = %q, want masked account source", memberDetail.Source)
	}
	if got, _ := memberDetail.RawJSON["source"].(string); got == source || !strings.Contains(got, "...") {
		t.Fatalf("member raw_json.source = %q, want masked account source", got)
	}
	if memberDetail.AuthIndex == nil || *memberDetail.AuthIndex == authIndex || !strings.Contains(*memberDetail.AuthIndex, "...") {
		t.Fatalf("member detail auth_index = %v, want masked auth index", memberDetail.AuthIndex)
	}
	if got, _ := memberDetail.RawJSON["auth_index"].(string); got == authIndex || !strings.Contains(got, "...") {
		t.Fatalf("member raw_json.auth_index = %q, want masked auth index", got)
	}

	memberRecords := usageRecordsResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/usage/records?scope=account&start=2026-05-16T00:00:00&end=2026-05-17T00:00:00&page=1&page_size=1", nil, memberCookies, &memberRecords)
	if len(memberRecords.Items) != 1 {
		t.Fatalf("member usage record count = %d, want 1", len(memberRecords.Items))
	}
	if memberRecords.Items[0].AuthIndex == nil || *memberRecords.Items[0].AuthIndex == authIndex || !strings.Contains(*memberRecords.Items[0].AuthIndex, "...") {
		t.Fatalf("member list auth_index = %v, want masked auth index", memberRecords.Items[0].AuthIndex)
	}

	storedSource, storedRaw := storedUsageSourceAndRawJSON(t, dataDir, id)
	if storedSource != source {
		t.Fatalf("stored source = %q, want original", storedSource)
	}
	if !strings.Contains(storedRaw, source) {
		t.Fatalf("stored raw_json = %q, want original account source", storedRaw)
	}
}

type usageRecordSeed struct {
	Timestamp    string
	Username     string
	Source       string
	RequestID    string
	Auth         string
	DedupeKey    string
	RawJSON      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func seedUsageRecord(t *testing.T, dataDir string, timestamp string) {
	t.Helper()
	seedUsageRecordWithValues(t, dataDir, usageRecordSeed{
		Timestamp:    timestamp,
		Username:     "admin",
		Source:       "code10001",
		RequestID:    "req-time-test",
		Auth:         "bearer",
		DedupeKey:    "time-test-1",
		RawJSON:      `{"request_id":"req-time-test"}`,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	})
}

func seedUsageRecordWithValues(t *testing.T, dataDir string, seed usageRecordSeed) int {
	t.Helper()

	db := openUsageTestDB(t, dataDir)
	defer db.Close()

	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider, model,
			endpoint, source, request_id, auth, latency_ms, failed, input_tokens,
			output_tokens, cached_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (
			?, ?, ?, 'VSCode', 'openai', 'gpt-5.5', '/v1/chat/completions',
			?, ?, ?, 1000, 0, ?, ?, 0, 0, ?, ?, ?
		)
	`, seed.Timestamp, seed.Timestamp, seed.Username, seed.Source, seed.RequestID, seed.Auth, seed.InputTokens, seed.OutputTokens, seed.TotalTokens, seed.DedupeKey, seed.RawJSON)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func storedUsageSourceAndRawJSON(t *testing.T, dataDir string, id int) (string, string) {
	t.Helper()

	db := openUsageTestDB(t, dataDir)
	defer db.Close()

	var source, rawJSON string
	if err := db.QueryRow(`SELECT source, raw_json FROM usage_records WHERE id = ?`, id).Scan(&source, &rawJSON); err != nil {
		t.Fatal(err)
	}
	return source, rawJSON
}

func openUsageTestDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(dataDir, "db", "cpa_helper.sqlite3")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
