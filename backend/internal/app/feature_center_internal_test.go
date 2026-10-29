package app

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func newFeatureCenterTestApp(t *testing.T) (*App, http.Handler, []*http.Cookie) {
	t.Helper()
	app, handler, admin := newAIRadarTestApp(t)
	t.Cleanup(app.Close)
	return app, handler, admin
}

func featureCenterTestSettings(t *testing.T, app *App) FeatureCenterSettings {
	t.Helper()
	settings, err := app.featureCenterSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func featureCenterTestUpdate(t *testing.T, app *App, image, radar bool) FeatureCenterSettings {
	t.Helper()
	previous := featureCenterTestSettings(t, app)
	next, err := app.updateFeatureCenter(context.Background(), previous.Revision, image, radar)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestFeatureCenterSettingsPermissionsRevisionAndPersistence(t *testing.T) {
	app, handler, admin := newFeatureCenterTestApp(t)
	member := newAIRadarMemberCookies(t, handler, admin)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/features", nil, nil, http.StatusUnauthorized, nil)
	var visible map[string]any
	modelMonitorRequest(t, handler, http.MethodGet, "/api/features", nil, member, http.StatusOK, &visible)
	if len(visible) != 3 || visible["revision"] != float64(1) || visible["image_studio_enabled"] != true || visible["ai_radar_enabled"] != true {
		t.Fatalf("unsafe or incorrect public projection: %#v", visible)
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/settings/features", nil, member, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings/features", map[string]any{
		"revision": 1, "image_studio_enabled": false, "ai_radar_enabled": false,
	}, member, http.StatusForbidden, nil)
	if _, err := app.db.Exec(`UPDATE users SET is_admin = 1 WHERE username = 'member'`); err != nil {
		t.Fatal(err)
	}
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings/features", map[string]any{
		"revision": 1, "image_studio_enabled": false, "ai_radar_enabled": false,
	}, member, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings/features", map[string]any{
		"revision": 1, "image_studio_enabled": false,
	}, admin, http.StatusUnprocessableEntity, nil)
	var changed FeatureCenterSettings
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings/features", map[string]any{
		"revision": 1, "image_studio_enabled": false, "ai_radar_enabled": true,
	}, admin, http.StatusOK, &changed)
	if changed.Revision != 2 || changed.ImageStudioEnabled || !changed.AIRadarEnabled {
		t.Fatalf("image-only change = %#v", changed)
	}
	modelMonitorRequest(t, handler, http.MethodPut, "/api/settings/features", map[string]any{
		"revision": 1, "image_studio_enabled": true, "ai_radar_enabled": false,
	}, admin, http.StatusConflict, nil)
	if current := featureCenterTestSettings(t, app); current != changed {
		t.Fatalf("conflicting write changed settings: %#v", current)
	}
	for _, flags := range [][2]bool{{true, false}, {false, false}, {true, true}, {false, false}} {
		changed = featureCenterTestUpdate(t, app, flags[0], flags[1])
		stored, err := readFeatureCenterSettings(context.Background(), app.db)
		if err != nil || stored != changed {
			t.Fatalf("stored = %#v, runtime = %#v, error = %v", stored, changed, err)
		}
	}
	// A generic settings save operates on another table and cannot restore flags.
	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	app.Close()
	reopened, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if current := featureCenterTestSettings(t, reopened); current != changed {
		t.Fatalf("restart/upgrade restored defaults: %#v, want %#v", current, changed)
	}
	if reopened.features.radar != nil || reopened.aiRadarSampling.done != nil {
		t.Fatal("disabled restart created a radar epoch or sampling task")
	}
}

func TestFeatureCenterMigrationUpgradesPreviousHeadAndPreservesSavedFlags(t *testing.T) {
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
	ctx := context.Background()
	if err := goose.UpToContext(ctx, db, ".", 202610290002); err != nil {
		t.Fatal(err)
	}
	if testTableExists(t, db, "feature_center_settings") {
		t.Fatal("previous schema already has feature flags")
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	settings, err := readFeatureCenterSettings(ctx, db)
	if err != nil || settings.Revision != 1 || !settings.ImageStudioEnabled || !settings.AIRadarEnabled {
		t.Fatalf("upgrade defaults = %#v, error = %v", settings, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE feature_center_settings SET image_studio_enabled = 0, ai_radar_enabled = 0, revision = 2 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	settings, err = readFeatureCenterSettings(ctx, db)
	if err != nil || settings.Revision != 2 || settings.ImageStudioEnabled || settings.AIRadarEnabled {
		t.Fatalf("subsequent upgrade reset flags = %#v, error = %v", settings, err)
	}
}

func TestFeatureCenterReadOnlyStartupRejectsMissingRecordOrTable(t *testing.T) {
	for _, missing := range []string{"record", "table"} {
		t.Run(missing, func(t *testing.T) {
			app, _, _ := newFeatureCenterTestApp(t)
			statement := `DELETE FROM feature_center_settings`
			if missing == "table" {
				statement = `DROP TABLE feature_center_settings`
			}
			if _, err := app.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err := requireSchemaShape(context.Background(), app.db); !errors.Is(err, ErrDatabaseNeedsMigration) {
				t.Fatalf("schema shape error = %v", err)
			}
			if _, err := NewWithOptions(context.Background(), NewOptions{RequireReady: true}); !errors.Is(err, ErrDatabaseNeedsMigration) {
				t.Fatalf("read-only startup error = %v", err)
			}
			if missing == "record" {
				var count int
				if err := app.db.QueryRow(`SELECT COUNT(*) FROM feature_center_settings`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("startup recreated missing record: %d, %v", count, err)
				}
			}
		})
	}
}

func TestFeatureCenterFailedSaveKeepsRuntimeAndDoesNotPublish(t *testing.T) {
	app, _, _ := newFeatureCenterTestApp(t)
	events, unsubscribe, err := app.subscribeFeatureCenter()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	initial := <-events
	epoch := app.features.radar
	if _, err := app.db.Exec(`CREATE TRIGGER reject_feature_update BEFORE UPDATE ON feature_center_settings BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.updateFeatureCenter(context.Background(), initial.Revision, false, false); err == nil {
		t.Fatal("failed storage write was reported as successful")
	}
	if current := featureCenterTestSettings(t, app); current != initial || app.features.radar != epoch || epoch.ctx.Err() != nil {
		t.Fatalf("failed write changed runtime state: %#v", current)
	}
	select {
	case event := <-events:
		t.Fatalf("failed write published %#v", event)
	default:
	}
}

func TestFeatureCenterConcurrentRevisionAndIdempotentSave(t *testing.T) {
	app, _, _ := newFeatureCenterTestApp(t)
	initial := featureCenterTestSettings(t, app)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, flags := range [][2]bool{{false, true}, {true, false}} {
		go func(image, radar bool) {
			<-start
			_, err := app.updateFeatureCenter(context.Background(), initial.Revision, image, radar)
			results <- err
		}(flags[0], flags[1])
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		var appErr *AppError
		if err == nil {
			success++
		} else if errors.As(err, &appErr) && appErr.Code == "conflict" {
			conflict++
		} else {
			t.Fatalf("concurrent error = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	current := featureCenterTestSettings(t, app)
	if current.Revision != initial.Revision+1 || current.ImageStudioEnabled == current.AIRadarEnabled {
		t.Fatalf("concurrent flags were merged or lost: %#v", current)
	}
	epoch, done := app.features.radar, app.aiRadarSampling.done
	unchanged, err := app.updateFeatureCenter(context.Background(), current.Revision, current.ImageStudioEnabled, current.AIRadarEnabled)
	if err != nil || unchanged != current || app.features.radar != epoch || app.aiRadarSampling.done != done {
		t.Fatalf("same-state save restarted work: %#v, %v", unchanged, err)
	}
}

func TestFeatureCenterBackgroundReenableStartsOneQuietRunner(t *testing.T) {
	app, _, _ := newFeatureCenterTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, time.Now().Format(time.RFC3339))}
	app.aiRadarHTTPClient = upstream.client
	app.features.background = true
	app.startFeatureCenterBackground()
	firstDone, firstEpoch := app.aiRadarSampling.done, app.features.radar
	if firstDone == nil || app.aiRadarSampling.interval != 4*time.Hour {
		t.Fatal("background runner did not use the existing four-hour cadence")
	}
	featureCenterTestUpdate(t, app, false, true)
	app.startFeatureCenterBackground()
	if app.aiRadarSampling.done != firstDone || app.features.radar != firstEpoch {
		t.Fatal("unrelated feature change or repeated start restarted radar")
	}
	featureCenterTestUpdate(t, app, false, false)
	select {
	case <-firstDone:
	default:
		t.Fatal("disable returned before its sampling runner stopped")
	}
	featureCenterTestUpdate(t, app, false, true)
	secondDone, secondEpoch := app.aiRadarSampling.done, app.features.radar
	if secondDone == firstDone || secondEpoch == firstEpoch {
		t.Fatal("re-enable did not create a new sampling epoch")
	}
	featureCenterTestUpdate(t, app, false, true)
	app.startFeatureCenterBackground()
	if app.aiRadarSampling.done != secondDone || app.features.radar != secondEpoch {
		t.Fatal("same-state save created another sampling runner")
	}
	// Observe the restarted goroutine before its four-hour first tick. Reopening
	// availability must not perform an immediate sample or replay missed ticks.
	time.Sleep(25 * time.Millisecond)
	if upstream.liveRequestCount() != 0 || upstream.historyRequestCount() != 0 {
		t.Fatal("startup or re-enable immediately contacted the upstream")
	}
	featureCenterTestUpdate(t, app, false, false)
	select {
	case <-secondDone:
	default:
		t.Fatal("second disable returned before the restarted runner stopped")
	}
}

func TestFeatureCenterDisabledRadarBlocksCacheAPIBackfillAndSampling(t *testing.T) {
	app, handler, admin := newFeatureCenterTestApp(t)
	upstream := &aiRadarTestUpstream{liveBody: aiRadarLivePayloadJSON(t, time.Now().Format(time.RFC3339)), historyBody: aiRadarSnapshotPayloadJSON(t)}
	app.aiRadarHTTPClient = upstream.client
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, nil)
	rows := aiRadarHistoryRowCount(t, app)
	if err := app.markAIRadarBackfillCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	featureCenterTestUpdate(t, app, true, false)
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/ai-radar"},
		{http.MethodGet, "/api/ai-radar/history?window=all"},
		{http.MethodPost, "/api/ai-radar/refresh"},
	} {
		var response struct {
			Detail struct {
				Code string `json:"code"`
			} `json:"detail"`
		}
		modelMonitorRequest(t, handler, endpoint.method, endpoint.path, nil, admin, http.StatusForbidden, &response)
		if response.Detail.Code != "feature_disabled" {
			t.Fatalf("%s did not report feature_disabled", endpoint.path)
		}
	}
	for _, err := range []error{
		app.aiRadarSampling.RunOnce(context.Background()),
		app.ensureAIRadarBackfill(context.Background()),
		app.recordAIRadarHistory(context.Background(), nil, time.Now()),
		app.markAIRadarBackfillCompleted(context.Background()),
		app.importAIRadarSnapshot(context.Background(), nil),
	} {
		var appErr *AppError
		if !errors.As(err, &appErr) || appErr.Code != "feature_disabled" {
			t.Fatalf("disabled internal entry error = %v", err)
		}
	}
	if upstream.liveRequestCount() != 1 || upstream.historyRequestCount() != 0 || aiRadarHistoryRowCount(t, app) != rows {
		t.Fatal("disabled feature contacted upstream or wrote history")
	}
	if completed, err := app.aiRadarBackfillCompleted(context.Background()); err != nil || !completed {
		t.Fatalf("disable discarded the backfill marker: %t, %v", completed, err)
	}
	featureCenterTestUpdate(t, app, true, true)
	if upstream.liveRequestCount() != 1 || app.aiRadarSampling.done != nil {
		t.Fatal("re-enabling with StartBackground=false started work")
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar", nil, admin, http.StatusOK, nil)
	if upstream.liveRequestCount() != 2 {
		t.Fatal("re-enable reused a snapshot from the disabled epoch")
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/ai-radar/history?window=all", nil, admin, http.StatusOK, nil)
	if upstream.historyRequestCount() != 0 || aiRadarHistoryRowCount(t, app) != rows {
		t.Fatal("re-enable discarded history or replayed completed backfill")
	}
}

func TestFeatureCenterDisablePublishesBeforeDrainAndRejectsLateRadarResults(t *testing.T) {
	for _, mode := range []string{"shared", "force", "history", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			app, handler, admin := newFeatureCenterTestApp(t)
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			live := aiRadarLivePayloadJSON(t, time.Now().Format(time.RFC3339))
			history := aiRadarSnapshotPayloadJSON(t)
			app.aiRadarHTTPClient = func(time.Duration) (*http.Client, error) {
				return &http.Client{Transport: aiRadarRoundTripper(func(request *http.Request) (*http.Response, error) {
					close(started)
					<-request.Context().Done()
					close(canceled)
					<-release // deliberately return success after cancellation
					body := live
					if mode == "history" {
						body = history
					}
					return aiRadarTestResponse(request, http.StatusOK, "application/json", body), nil
				})}, nil
			}
			operationDone := make(chan error, 1)
			go func() {
				if mode == "scheduled" {
					operationDone <- app.aiRadarSampling.RunOnce(context.Background())
					return
				}
				method, target := http.MethodGet, "/api/ai-radar"
				if mode == "force" {
					method, target = http.MethodPost, "/api/ai-radar/refresh"
				}
				if mode == "history" {
					target = "/api/ai-radar/history?window=all"
				}
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(method, target, nil)
				for _, cookie := range admin {
					request.AddCookie(cookie)
				}
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "feature_disabled") {
					operationDone <- errors.New("canceled route returned cached/late success: " + recorder.Body.String())
					return
				}
				operationDone <- nil
			}()
			featureCenterWait(t, started, "upstream request")
			var followerResult chan error
			if mode == "shared" {
				// Ordinary fetches detach their fetch context from the viewer. The
				// original request's Done is observed only at the follower wait.
				waiting := &featureCenterObservedContext{Context: context.Background(), entered: make(chan struct{})}
				followerResult = make(chan error, 1)
				go func() {
					request := httptest.NewRequest(http.MethodGet, "/api/ai-radar", nil).WithContext(waiting)
					followerResult <- app.serveAIRadar(httptest.NewRecorder(), request, false)
				}()
				featureCenterWait(t, waiting.entered, "shared-flight follower")
			}
			events, unsubscribe, err := app.subscribeFeatureCenter()
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribe()
			initial := <-events
			disabled := make(chan error, 1)
			go func() {
				_, err := app.updateFeatureCenter(context.Background(), initial.Revision, true, false)
				disabled <- err
			}()
			select {
			case event := <-events:
				if event.AIRadarEnabled || event.Revision != initial.Revision+1 {
					t.Fatalf("wrong disable event: %#v", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disable was not published before the blocked upstream drained")
			}
			featureCenterWait(t, canceled, "upstream cancellation")
			if followerResult != nil {
				select {
				case err := <-followerResult:
					var appErr *AppError
					if !errors.As(err, &appErr) || appErr.Code != "feature_disabled" {
						t.Fatalf("shared follower returned a cached/late result: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("shared follower waited for the blocked leader after disable")
				}
			}
			select {
			case err := <-disabled:
				t.Fatalf("disable returned before in-flight work drained: %v", err)
			default:
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-operationDone:
				if mode == "scheduled" {
					var appErr *AppError
					if !errors.As(err, &appErr) || appErr.Code != "feature_disabled" {
						t.Fatalf("late scheduled result: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled radar operation did not drain")
			}
			if err := <-disabled; err != nil {
				t.Fatal(err)
			}
			if aiRadarHistoryRowCount(t, app) != 0 || app.aiRadarSnapshot.response != nil {
				t.Fatal("late result populated history or cache after disable")
			}
			if _, exists := aiRadarLiveFlights.Load(app); exists {
				t.Fatal("canceled shared flight was retained")
			}
		})
	}
}

type featureCenterObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *featureCenterObservedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func featureCenterWait(t *testing.T, signal <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + action)
	}
}

func featureCenterReadEvent(t *testing.T, reader *bufio.Reader) FeatureCenterSettings {
	t.Helper()
	var data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
		if line == "" && data != "" {
			var settings FeatureCenterSettings
			if err := json.Unmarshal([]byte(data), &settings); err != nil {
				t.Fatal(err)
			}
			return settings
		}
	}
}

func TestFeatureCenterEventsInitialSnapshotUpdateAndDisconnectCleanup(t *testing.T) {
	app, handler, admin := newFeatureCenterTestApp(t)
	streamDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.URL.Path == "/api/features/events" {
			close(streamDone)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/features/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", server.URL)
	for _, cookie := range admin {
		request.AddCookie(cookie)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("event response = %d %#v", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	if initial := featureCenterReadEvent(t, reader); initial != featureCenterTestSettings(t, app) {
		t.Fatalf("initial event = %#v", initial)
	}
	changed := featureCenterTestUpdate(t, app, false, false)
	if received := featureCenterReadEvent(t, reader); received != changed {
		t.Fatalf("update event = %#v, want %#v", received, changed)
	}
	response.Body.Close()
	cancel()
	featureCenterWait(t, streamDone, "event stream disconnect")
	app.features.mu.Lock()
	count := len(app.features.subscribers)
	app.features.mu.Unlock()
	if count != 0 {
		t.Fatalf("disconnected subscriber retained: %d", count)
	}
}

func TestFeatureCenterEventsRejectCrossOriginAndCloseRevokedSession(t *testing.T) {
	app, handler, admin := newFeatureCenterTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/api/features/events", nil)
	request.Header.Set("Origin", "https://unrelated.example")
	for _, cookie := range admin {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin response = %d", recorder.Code)
	}
	streamDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(streamDone)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/features/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range admin {
		request.AddCookie(cookie)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	featureCenterReadEvent(t, reader)
	if _, err := app.db.Exec(`UPDATE users SET session_version = session_version + 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	featureCenterTestUpdate(t, app, false, true)
	featureCenterWait(t, streamDone, "revoked session stream")
	remaining, err := io.ReadAll(reader)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("revoked session received data: %q, %v", remaining, err)
	}
}

func TestFeatureCenterSlowSubscriberReceivesLatestSnapshotAndShutdownClosesIt(t *testing.T) {
	app, _, _ := newFeatureCenterTestApp(t)
	events, unsubscribe, err := app.subscribeFeatureCenter()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	featureCenterTestUpdate(t, app, false, true)
	featureCenterTestUpdate(t, app, false, false)
	latest := featureCenterTestUpdate(t, app, true, false)
	if event := <-events; event != latest {
		t.Fatalf("slow subscriber received old snapshot %#v, want %#v", event, latest)
	}
	app.closeFeatureCenter()
	if _, open := <-events; open {
		t.Fatal("shutdown retained an event subscriber")
	}
}

func TestFeatureCenterEventsCloseExpiredSessionAndShutdown(t *testing.T) {
	for _, mode := range []string{"expiry", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			app, handler, admin := newFeatureCenterTestApp(t)
			var expiresAt int64
			if mode == "expiry" {
				cfg, err := app.loadConfig(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				// Keep the real signed session shape, shortening only its expiry.
				cookie := *admin[0]
				parts := strings.SplitN(cookie.Value, ".", 2)
				body, err := base64.RawURLEncoding.DecodeString(parts[0])
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatal(err)
				}
				expiresAt = time.Now().Unix() + 1
				payload["exp"] = expiresAt
				body, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				encoded := base64.RawURLEncoding.EncodeToString(body)
				mac := hmac.New(sha256.New, []byte(cfg.SessionSecret))
				mac.Write([]byte(encoded))
				cookie.Value = encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
				admin = []*http.Cookie{&cookie}
			}
			streamDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r)
				close(streamDone)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/features/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, cookie := range admin {
				request.AddCookie(cookie)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			featureCenterReadEvent(t, reader)
			if mode == "expiry" {
				// The token contract accepts its expiry second; wake after it has passed.
				timer := time.NewTimer(time.Until(time.Unix(expiresAt+1, 0)))
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				featureCenterTestUpdate(t, app, false, true)
			} else {
				app.closeFeatureCenter()
			}
			featureCenterWait(t, streamDone, mode+" event stream cleanup")
			remaining, err := io.ReadAll(reader)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("closed stream retained output: %q, %v", remaining, err)
			}
			app.features.mu.Lock()
			count := len(app.features.subscribers)
			app.features.mu.Unlock()
			if count != 0 {
				t.Fatalf("%s retained %d event subscribers", mode, count)
			}
		})
	}
}
