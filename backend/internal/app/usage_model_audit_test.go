package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func newUsageAuditTestApp(t *testing.T, target string) (*App, http.Handler, []*http.Cookie, AppConfig) {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	handler := a.Routes()
	cookies := modelMonitorRequest(t, handler, "POST", "/api/auth/setup", map[string]any{"username": "audit-admin", "password": "audit-password", "nickname": "Audit"}, nil, 200, nil)
	cfg, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = target, "audit-secret"
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return a, handler, cookies, cfg
}

func saveAuditRecord(t *testing.T, a *App, origin string, id string) UsageRecord {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"request_id": id, "model": "requested", "response_model": "reported", "alias": "friendly", "input_tokens": 1})
	record, _, err := a.saveUsageMessage(context.Background(), raw, modelPriceBillingIndex{}, origin)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestUsageModelAuditPermissionsCacheAndSource(t *testing.T) {
	var calls atomic.Int32
	logText := auditTestLog(`{"model":"requested"}`, `{"model":"reported"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer audit-secret" {
			t.Error("missing server credentials")
		}
		fmt.Fprint(w, logText)
	}))
	defer server.Close()
	a, handler, cookies, cfg := newUsageAuditTestApp(t, server.URL)
	record := saveAuditRecord(t, a, usageCollectorOrigin(cfg.Collector), "audit-id")
	base := fmt.Sprintf("/api/usage/records/%d", record.ID)
	var envelope usageAuditEnvelope
	modelMonitorRequest(t, handler, "GET", base+"/model-audit", nil, cookies, 200, &envelope)
	if envelope.Audit != nil || calls.Load() != 0 {
		t.Fatal("GET cache performed remote IO")
	}
	modelMonitorRequest(t, handler, "POST", base+"/model-audit", map[string]any{}, cookies, 200, &envelope)
	if envelope.Audit == nil || envelope.Audit.LogResponseModel == nil || *envelope.Audit.LogResponseModel != "reported" || envelope.Audit.CheckedBy == 0 {
		t.Fatalf("audit = %+v", envelope)
	}
	modelMonitorRequest(t, handler, "POST", base+"/model-audit", map[string]any{}, cookies, 200, &envelope)
	if calls.Load() != 1 {
		t.Fatal("successful cache not reused")
	}
	var detail map[string]any
	modelMonitorRequest(t, handler, "GET", base, nil, cookies, 200, &detail)
	if detail["response_model"] != "reported" || detail["request_alias"] != "friendly" {
		t.Fatalf("ordinary model fields = %+v", detail)
	}
	if _, exists := detail["audit"]; exists {
		t.Fatal("audit leaked into ordinary DTO")
	}
	var preview struct {
		Text       string
		Truncated  bool
		TotalBytes int64 `json:"total_bytes"`
	}
	modelMonitorRequest(t, handler, "GET", base+"/cpa-log", nil, cookies, 200, &preview)
	if preview.Text != logText || preview.Truncated {
		t.Fatal("preview mismatch")
	}
	request := httptest.NewRequest("GET", base+"/cpa-log?download=1", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 || w.Body.String() != logText || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	before := calls.Load()
	if _, err := a.db.Exec(`UPDATE users SET is_super_admin = 0`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct{ method, path string }{{"GET", "model-audit"}, {"POST", "model-audit"}, {"GET", "cpa-log"}} {
		modelMonitorRequest(t, handler, action.method, base+"/"+action.path, map[string]any{}, cookies, 403, nil)
	}
	if _, err := a.db.Exec(`UPDATE users SET is_admin = 0`); err != nil {
		t.Fatal(err)
	}
	modelMonitorRequest(t, handler, "GET", base+"/cpa-log", nil, cookies, 403, nil)
	if calls.Load() != before {
		t.Fatal("denied user caused remote IO")
	}
	if _, err := a.db.Exec(`UPDATE users SET is_admin = 1, is_super_admin = 1`); err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL = "http://localhost:1234"
	if err := a.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	modelMonitorRequest(t, handler, "GET", base+"/model-audit", nil, cookies, 409, nil)
	modelMonitorRequest(t, handler, "GET", base+"/cpa-log", nil, cookies, 409, nil)
	if calls.Load() != before {
		t.Fatal("source mismatch caused remote IO")
	}
}

func TestUsageModelAuditLegacyAcknowledgementAndRetention(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, auditTestLog(`{"model":"requested"}`, `{"model":"reported"}`))
	}))
	defer server.Close()
	a, handler, cookies, cfg := newUsageAuditTestApp(t, server.URL)
	record := saveAuditRecord(t, a, "", "legacy-id")
	base := fmt.Sprintf("/api/usage/records/%d", record.ID)
	var envelope usageAuditEnvelope
	modelMonitorRequest(t, handler, "GET", base+"/model-audit", nil, cookies, 200, &envelope)
	if envelope.SourceStatus != "unknown" || envelope.AcknowledgementToken == nil {
		t.Fatal("missing unknown-origin acknowledgement")
	}
	modelMonitorRequest(t, handler, "POST", base+"/model-audit", map[string]any{}, cookies, 409, nil)
	modelMonitorRequest(t, handler, "GET", base+"/cpa-log", nil, cookies, 409, nil)
	modelMonitorRequest(t, handler, "POST", base+"/model-audit", map[string]any{"acknowledge_source": usageAuditAcknowledgement(cfg, record.ID+1)}, cookies, 409, nil)
	if calls.Load() != 0 {
		t.Fatal("unacknowledged origin caused IO")
	}
	modelMonitorRequest(t, handler, "POST", base+"/model-audit", map[string]any{"acknowledge_source": *envelope.AcknowledgementToken}, cookies, 200, &envelope)
	if envelope.Audit.SourceStatus != "unknown" {
		t.Fatal("acknowledgement incorrectly proved origin")
	}
	if _, err := a.db.Exec(`UPDATE usage_records SET timestamp = ? WHERE id = ?`, dbTime(time.Now().Add(-8*24*time.Hour)), record.ID); err != nil {
		t.Fatal(err)
	}
	modelMonitorRequest(t, handler, "GET", base+"/model-audit", nil, cookies, 404, nil)
	if _, err := a.pruneExpiredUsageRawJSON(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM usage_model_audits`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired audit retained: %d %v", count, err)
	}
}

func TestUsageModelAuditHTTPFailuresAndPreview(t *testing.T) {
	var status atomic.Int32
	status.Store(404)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/request-log" {
			fmt.Fprint(w, `{"request-log":false}`)
			return
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			fmt.Fprint(w, strings.Repeat("x", usageLogPreviewBytes+100))
		} else {
			fmt.Fprint(w, "SECRET-UPSTREAM-BODY")
		}
	}))
	defer server.Close()
	a, handler, cookies, cfg := newUsageAuditTestApp(t, server.URL)
	record := saveAuditRecord(t, a, usageCollectorOrigin(cfg.Collector), "failure-id")
	base := fmt.Sprintf("/api/usage/records/%d/cpa-log", record.ID)
	modelMonitorRequest(t, handler, "GET", base, nil, cookies, 409, nil)
	status.Store(401)
	var failure map[string]any
	modelMonitorRequest(t, handler, "GET", base, nil, cookies, 502, &failure)
	encoded, _ := json.Marshal(failure)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("upstream error body leaked")
	}
	status.Store(200)
	var preview struct {
		Text       string
		Truncated  bool
		TotalBytes int64 `json:"total_bytes"`
	}
	modelMonitorRequest(t, handler, "GET", base, nil, cookies, 200, &preview)
	if !preview.Truncated || len(preview.Text) != usageLogPreviewBytes || preview.TotalBytes != usageLogPreviewBytes+100 {
		t.Fatalf("preview not explicitly bounded: %d %v", preview.TotalBytes, preview.Truncated)
	}
}

func TestUsageResponseModelMigrationBackfill(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openRuntimeDB(paths, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", 202609170001); err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{`{"response_model":" reported ","alias":"alias"}`, `{"nested":{"response_model":"wrong","alias":"wrong"}}`, `broken`} {
		if _, err := db.Exec(`INSERT INTO usage_records(created_at,timestamp,dedupe_key,raw_json) VALUES(?,?,?,?)`, dbTime(time.Now()), dbTime(time.Now()), fmt.Sprint(i), raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpContext(context.Background(), db, "."); err != nil {
		t.Fatal(err)
	}
	var model, alias, origin *string
	if err := db.QueryRow(`SELECT response_model,request_alias,collector_origin FROM usage_records WHERE dedupe_key='0'`).Scan(&model, &alias, &origin); err != nil {
		t.Fatal(err)
	}
	if model == nil || *model != "reported" || alias == nil || *alias != "alias" || origin != nil {
		t.Fatal("backfill mismatch")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE dedupe_key IN ('1','2') AND response_model IS NULL AND request_alias IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("nested/invalid payload backfill: %d %v", count, err)
	}
}

func TestUsageAuditRejectsInFlightRoleAndSourceChanges(t *testing.T) {
	for _, change := range []string{"role", "source"} {
		t.Run(change, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				fmt.Fprint(w, auditTestLog(`{"model":"requested"}`, `{"model":"reported"}`))
			}))
			defer server.Close()
			a, handler, cookies, cfg := newUsageAuditTestApp(t, server.URL)
			record := saveAuditRecord(t, a, usageCollectorOrigin(cfg.Collector), "in-flight")
			r := httptest.NewRequest("POST", fmt.Sprintf("/api/usage/records/%d/model-audit", record.ID), strings.NewReader(`{}`))
			for _, cookie := range cookies {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); handler.ServeHTTP(w, r) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("download did not start")
			}
			want := 403
			if change == "role" {
				if _, err := a.db.Exec(`UPDATE users SET is_super_admin=0`); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg.Collector.CLIProxyURL = "http://127.0.0.1:1234"
				if err := a.saveConfig(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				want = 409
			}
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("audit did not finish")
			}
			if w.Code != want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			var count int
			if err := a.db.QueryRow(`SELECT COUNT(*) FROM usage_model_audits`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalidated result cached: %d %v", count, err)
			}
		})
	}
}

func TestUsageAuditDownloadEscapingRedirectsAndCancellation(t *testing.T) {
	var leaked atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer redirect.Close()
	var escaped string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped = r.URL.EscapedPath()
		http.Redirect(w, r, redirect.URL, http.StatusFound)
	}))
	defer server.Close()
	a, _, _, cfg := newUsageAuditTestApp(t, server.URL)
	_, _, err := a.fetchUsageLog(context.Background(), cfg, "id/path?with=query")
	if err == nil || leaked.Load() != 0 {
		t.Fatal("redirect followed")
	}
	if escaped != "/v0/management/request-log-by-id/id%2Fpath%3Fwith=query" {
		t.Fatalf("request id not single escaped segment: %s", escaped)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, _, err = a.fetchUsageLog(ctx, cfg, "timeout")
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != "audit_timeout" {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, _, err = a.fetchUsageLog(ctx, cfg, "cancel")
	if !errors.As(err, &appErr) || appErr.Code != "audit_cancelled" {
		t.Fatalf("cancel: %v", err)
	}
	a.usageAuditMu.Lock()
	a.usageAuditActive = 4
	a.usageAuditMu.Unlock()
	_, _, err = a.fetchUsageLog(context.Background(), cfg, "busy")
	if !errors.As(err, &appErr) || appErr.Code != "audit_busy" {
		t.Fatalf("busy: %v", err)
	}
}

func TestUsageResponseFieldsExactAndOriginStable(t *testing.T) {
	a, _, _, cfg := newUsageAuditTestApp(t, "http://localhost:8317")
	origin := usageCollectorOrigin(cfg.Collector)
	for i, raw := range []string{`{"request_id":"first","model":"billing","response_model":"real","alias":"label"}`, `{"request_id":"second","model":"billing","nested":{"response_model":"echo","alias":"echo"}}`} {
		record, _, err := a.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{}, origin)
		if err != nil {
			t.Fatal(err)
		}
		read, err := a.getUsageRecord(context.Background(), record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (read.ResponseModel == nil || *read.ResponseModel != "real" || read.RequestAlias == nil || *read.RequestAlias != "label") {
			t.Fatal("exact queue fields lost")
		}
		if i == 1 && (read.ResponseModel != nil || read.RequestAlias != nil) {
			t.Fatal("nested echo treated as response evidence")
		}
		cfg.Collector.CLIProxyURL = "http://localhost:9999"
		if err := a.saveConfig(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if _, created, err := a.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{}, usageCollectorOrigin(cfg.Collector)); err != nil || created {
			t.Fatal("duplicate changed")
		}
		var saved string
		if err := a.db.QueryRow(`SELECT collector_origin FROM usage_records WHERE id=?`, record.ID).Scan(&saved); err != nil || saved != origin {
			t.Fatal("duplicate overwrote source")
		}
	}
	if usageCollectorOrigin(CollectorConfig{CLIProxyURL: "http://LOCALHOST:80/base/"}) != usageCollectorOrigin(CollectorConfig{CLIProxyURL: "http://localhost/base"}) {
		t.Fatal("equivalent management origins differ")
	}
	if usageCollectorOrigin(CollectorConfig{CLIProxyURL: "resp://localhost:8317"}) != origin {
		t.Fatal("RESP/HTTP origin mismatch")
	}
}

func TestUsageAuditConcurrentCallsShareDownload(t *testing.T) {
	var downloads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if downloads.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, auditTestLog(`{"model":"requested"}`, `{"model":"reported"}`))
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })
	a, handler, cookies, cfg := newUsageAuditTestApp(t, server.URL)
	record := saveAuditRecord(t, a, usageCollectorOrigin(cfg.Collector), "concurrent")
	begin, submitted, done := make(chan struct{}), make(chan struct{}, 8), make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-begin
			r := httptest.NewRequest("POST", fmt.Sprintf("/api/usage/records/%d/model-audit", record.ID), strings.NewReader(`{}`))
			for _, cookie := range cookies {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			submitted <- struct{}{}
			handler.ServeHTTP(w, r)
			done <- w
		}()
	}
	close(begin)
	for i := 0; i < 8; i++ {
		<-submitted
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no download started")
	}
	once.Do(func() { close(release) })
	for i := 0; i < 8; i++ {
		select {
		case w := <-done:
			if w.Code != 200 {
				t.Fatalf("concurrent audit %d: %s", w.Code, w.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent audit stuck")
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloaded %d times", downloads.Load())
	}
}

func TestUsageAuditSchemaReadiness(t *testing.T) {
	a, _, _, _ := newUsageAuditTestApp(t, "http://localhost:8317")
	if _, err := a.db.Exec(`ALTER TABLE usage_model_audits DROP COLUMN collector_origin`); err != nil {
		t.Fatal(err)
	}
	err := requireSchemaShape(context.Background(), a.db)
	if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "usage_model_audits.collector_origin") {
		t.Fatalf("missing audit schema not detected: %v", err)
	}
}
