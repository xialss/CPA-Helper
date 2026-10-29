package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type FeatureCenterSettings struct {
	Revision           int64 `json:"revision"`
	ImageStudioEnabled bool  `json:"image_studio_enabled"`
	AIRadarEnabled     bool  `json:"ai_radar_enabled"`
}

type featureCenterUpdateRequest struct {
	Revision           *int64 `json:"revision"`
	ImageStudioEnabled *bool  `json:"image_studio_enabled"`
	AIRadarEnabled     *bool  `json:"ai_radar_enabled"`
}

type featureCenterRuntime struct {
	updateMu       sync.Mutex // serialize transitions, including their stop/start phase
	mu             sync.Mutex // gate new operations, commits, settings and subscribers
	settings       FeatureCenterSettings
	root           context.Context
	cancel         context.CancelFunc
	closed         bool
	background     bool
	eventsContext  context.Context
	stopEvents     context.CancelFunc
	radar          *aiRadarEpoch
	nextSubscriber uint64
	subscribers    map[uint64]chan FeatureCenterSettings
}

type aiRadarEpoch struct {
	ctx        context.Context
	cancel     context.CancelFunc
	operations sync.WaitGroup
}

type aiRadarOperation struct {
	runtime *featureCenterRuntime
	epoch   *aiRadarEpoch
}

type aiRadarOperationContextKey struct{}

func featureDisabledError() *AppError {
	return appError("feature_disabled", http.StatusForbidden, "该功能已由管理员关闭")
}

func readFeatureCenterSettings(ctx context.Context, db *sql.DB) (FeatureCenterSettings, error) {
	var result FeatureCenterSettings
	var imageEnabled, radarEnabled int
	err := db.QueryRowContext(ctx, `SELECT revision, image_studio_enabled, ai_radar_enabled FROM feature_center_settings WHERE id = 1`).Scan(
		&result.Revision, &imageEnabled, &radarEnabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("%w: feature_center_settings record is missing; run `cpa-helper migrate`", ErrDatabaseNeedsMigration)
	}
	if err != nil {
		return result, fmt.Errorf("read feature center settings: %w", err)
	}
	if result.Revision < 1 || (imageEnabled != 0 && imageEnabled != 1) || (radarEnabled != 0 && radarEnabled != 1) {
		return result, errors.New("feature_center_settings contains invalid state")
	}
	result.ImageStudioEnabled = imageEnabled == 1
	result.AIRadarEnabled = radarEnabled == 1
	return result, nil
}

func (a *App) initializeFeatureCenter(ctx context.Context, background bool) error {
	settings, err := readFeatureCenterSettings(ctx, a.db)
	if err != nil {
		return err
	}
	root, cancel := context.WithCancel(context.WithoutCancel(ctx))
	eventsContext, stopEvents := context.WithCancel(root)
	runtime := &featureCenterRuntime{
		settings: settings, root: root, cancel: cancel, background: background,
		eventsContext: eventsContext, stopEvents: stopEvents,
		subscribers: make(map[uint64]chan FeatureCenterSettings),
	}
	if settings.AIRadarEnabled {
		runtime.radar = newAIRadarEpoch(root)
	}
	a.features = runtime
	a.aiRadarSampling = newAIRadarSamplingRunner(a)
	return nil
}

func newAIRadarEpoch(parent context.Context) *aiRadarEpoch {
	ctx, cancel := context.WithCancel(parent)
	return &aiRadarEpoch{ctx: ctx, cancel: cancel}
}

func (a *App) startFeatureCenterBackground() {
	runtime := a.features
	runtime.updateMu.Lock()
	defer runtime.updateMu.Unlock()
	runtime.mu.Lock()
	epoch := runtime.radar
	enabled := !runtime.closed && runtime.background && runtime.settings.AIRadarEnabled
	runtime.mu.Unlock()
	if enabled && epoch != nil {
		a.aiRadarSampling.Start(epoch.ctx)
	}
}

// StopFeatureCenterEvents starts HTTP drain without closing the database or
// canceling ordinary feature requests. The HTTP server still waits for them.
func (a *App) StopFeatureCenterEvents() {
	if a.features != nil {
		a.features.stopEvents()
	}
}

func (a *App) closeFeatureCenter() {
	runtime := a.features
	if runtime == nil {
		return
	}
	runtime.updateMu.Lock()
	defer runtime.updateMu.Unlock()
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return
	}
	runtime.closed = true
	runtime.cancel()
	epoch := runtime.radar
	runtime.radar = nil
	if epoch != nil {
		epoch.cancel()
	}
	for id, subscriber := range runtime.subscribers {
		delete(runtime.subscribers, id)
		close(subscriber)
	}
	runtime.mu.Unlock()
	a.aiRadarSampling.Stop()
	if epoch != nil {
		epoch.operations.Wait()
	}
}

func (a *App) featureCenterSnapshot() (FeatureCenterSettings, error) {
	if a.features == nil {
		return FeatureCenterSettings{}, errors.New("feature center is not initialized")
	}
	a.features.mu.Lock()
	defer a.features.mu.Unlock()
	if a.features.closed {
		return FeatureCenterSettings{}, appError("unavailable", http.StatusServiceUnavailable, "功能状态服务已停止")
	}
	return a.features.settings, nil
}

// A lease registers before the gate opens and drains after disable. Nested
// history/backfill calls share the outer lease instead of registering new work.
func (a *App) beginAIRadarOperation(ctx context.Context) (context.Context, func(), error) {
	runtime := a.features
	if runtime == nil {
		return nil, nil, errors.New("feature center is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	epoch := runtime.radar
	if runtime.closed || !runtime.settings.AIRadarEnabled || epoch == nil {
		return nil, nil, featureDisabledError()
	}
	if current, ok := ctx.Value(aiRadarOperationContextKey{}).(*aiRadarOperation); ok && current.runtime == runtime {
		if current.epoch != epoch {
			return nil, nil, featureDisabledError()
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return ctx, func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	operationContext, cancel := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(epoch.ctx, cancel)
	operationContext = context.WithValue(operationContext, aiRadarOperationContextKey{}, &aiRadarOperation{runtime: runtime, epoch: epoch})
	epoch.operations.Add(1)
	var once sync.Once
	return operationContext, func() {
		once.Do(func() {
			stopCancellation()
			cancel()
			epoch.operations.Done()
		})
	}, nil
}

// All writes acquire this gate before obtaining SQLite's single connection.
// A settings transition therefore cannot race a checked-but-not-yet-committed
// radar result, and never waits for a worker while holding a database transaction.
func (a *App) withAIRadarCommit(ctx context.Context, commit func() error) error {
	runtime := a.features
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	operation, ok := ctx.Value(aiRadarOperationContextKey{}).(*aiRadarOperation)
	if !ok || operation.runtime != runtime || operation.epoch != runtime.radar || runtime.closed || !runtime.settings.AIRadarEnabled {
		return featureDisabledError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return commit()
}

func (a *App) checkAIRadarOperation(ctx context.Context) error {
	return a.withAIRadarCommit(ctx, func() error { return nil })
}

func (a *App) updateFeatureCenter(ctx context.Context, expected int64, imageEnabled, radarEnabled bool) (FeatureCenterSettings, error) {
	runtime := a.features
	runtime.updateMu.Lock()
	defer runtime.updateMu.Unlock()
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return FeatureCenterSettings{}, appError("unavailable", http.StatusServiceUnavailable, "功能状态服务已停止")
	}
	current := runtime.settings
	if current.Revision != expected {
		runtime.mu.Unlock()
		return current, conflictError("功能配置已被其他管理员修改，请刷新后重试")
	}
	if current.ImageStudioEnabled == imageEnabled && current.AIRadarEnabled == radarEnabled {
		runtime.mu.Unlock()
		return current, nil
	}
	next := FeatureCenterSettings{Revision: current.Revision + 1, ImageStudioEnabled: imageEnabled, AIRadarEnabled: radarEnabled}
	result, err := a.db.ExecContext(ctx, `UPDATE feature_center_settings SET image_studio_enabled = ?, ai_radar_enabled = ?, revision = ?, updated_at = ? WHERE id = 1 AND revision = ?`,
		imageEnabled, radarEnabled, next.Revision, dbTime(time.Now()), expected)
	if err != nil {
		runtime.mu.Unlock()
		return current, fmt.Errorf("save feature center settings: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		runtime.mu.Unlock()
		return current, err
	}
	if count != 1 {
		runtime.mu.Unlock()
		return current, conflictError("功能配置状态不一致，请重新加载")
	}
	runtime.settings = next
	var stopped *aiRadarEpoch
	if current.AIRadarEnabled != radarEnabled {
		stopped = runtime.radar
		if stopped != nil {
			stopped.cancel()
		}
		runtime.radar = nil
		if radarEnabled {
			runtime.radar = newAIRadarEpoch(runtime.root)
		}
		a.aiRadarMu.Lock()
		a.aiRadarSnapshot = aiRadarSnapshot{}
		a.aiRadarMu.Unlock()
	}
	// A slow network request must not delay cancellation in browser sessions.
	runtime.publishLocked(next)
	active := runtime.radar
	background := runtime.background
	runtime.mu.Unlock()
	if stopped != nil {
		a.aiRadarSampling.Stop()
		stopped.operations.Wait()
	}
	if current.AIRadarEnabled != radarEnabled && radarEnabled && background {
		a.aiRadarSampling.Start(active.ctx)
	}
	return next, nil
}

func (runtime *featureCenterRuntime) publishLocked(settings FeatureCenterSettings) {
	for _, subscriber := range runtime.subscribers {
		select {
		case subscriber <- settings:
		default:
			// Snapshots are complete, so a slow viewer needs only the newest one.
			select {
			case <-subscriber:
			default:
			}
			subscriber <- settings
		}
	}
}

func (a *App) subscribeFeatureCenter() (<-chan FeatureCenterSettings, func(), error) {
	runtime := a.features
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.eventsContext.Err() != nil {
		return nil, nil, appError("unavailable", http.StatusServiceUnavailable, "功能状态服务已停止")
	}
	runtime.nextSubscriber++
	id := runtime.nextSubscriber
	subscriber := make(chan FeatureCenterSettings, 1)
	subscriber <- runtime.settings
	runtime.subscribers[id] = subscriber
	return subscriber, func() {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if _, exists := runtime.subscribers[id]; exists {
			delete(runtime.subscribers, id)
			close(subscriber)
		}
	}, nil
}

func (a *App) handleFeatures(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.readyUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	settings, err := a.featureCenterSnapshot()
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settings)
	return nil
}

func (a *App) handleFeatureCenterSettings(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.superAdminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet, http.MethodPut); err != nil {
		return err
	}
	var settings FeatureCenterSettings
	var err error
	if r.Method == http.MethodPut {
		var payload featureCenterUpdateRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		if payload.Revision == nil || *payload.Revision < 1 || payload.ImageStudioEnabled == nil || payload.AIRadarEnabled == nil {
			return validationError("功能配置需要完整的开关状态和有效版本")
		}
		settings, err = a.updateFeatureCenter(r.Context(), *payload.Revision, *payload.ImageStudioEnabled, *payload.AIRadarEnabled)
	} else {
		settings, err = a.featureCenterSnapshot()
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settings)
	return nil
}

func (a *App) handleFeatureCenterEvents(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.readyUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	if originText := r.Header.Get("Origin"); originText != "" {
		origin, err := url.Parse(originText)
		scheme := "http"
		if sessionCookieSecure(r) {
			scheme = "https"
		}
		if err != nil || origin.Scheme != scheme || !strings.EqualFold(origin.Host, r.Host) || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
			return forbiddenError("功能状态事件仅允许同源连接")
		}
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return forbiddenError("功能状态事件仅允许同源连接")
	}
	controller := http.NewResponseController(w)
	// Bound slow-consumer writes; an idle event connection is kept alive below.
	if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("feature center event stream cannot set write deadline: %w", err)
	}
	events, unsubscribe, err := a.subscribeFeatureCenter()
	if err != nil {
		return err
	}
	defer unsubscribe()
	eventsContext := a.features.eventsContext
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	writeEvent := func(settings *FeatureCenterSettings) error {
		if err := eventsContext.Err(); err != nil {
			return err
		}
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if settings == nil {
			_, err = fmt.Fprint(w, ": heartbeat\n\n")
		} else {
			var body []byte
			body, err = json.Marshal(settings)
			if err == nil {
				_, err = fmt.Fprintf(w, "event: snapshot\nid: %d\ndata: %s\n\n", settings.Revision, body)
			}
		}
		if err != nil {
			return err
		}
		return controller.Flush()
	}
	for {
		var settings *FeatureCenterSettings
		select {
		case <-eventsContext.Done():
			return nil
		case <-r.Context().Done():
			return nil
		case next, ok := <-events:
			if !ok {
				return nil
			}
			settings = &next
		case <-heartbeat.C:
		}
		// EventSource cookies are fixed for this connection. Revalidate on every
		// event/heartbeat so session revocation, credential changes and expiry close it.
		if _, err := a.readyUser(r.Context(), r); err != nil {
			var authError *AppError
			if r.Context().Err() == nil && (!errors.As(err, &authError) || authError.Status >= http.StatusInternalServerError) {
				log.Printf("feature center event session check failed: %v", err)
			}
			return nil
		}
		if err := writeEvent(settings); err != nil {
			if r.Context().Err() == nil && eventsContext.Err() == nil {
				log.Printf("feature center event stream ended: %v", err)
			}
			return nil // headers were sent; a JSON error would corrupt the stream
		}
	}
}
