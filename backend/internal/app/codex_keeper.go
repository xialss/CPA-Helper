package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keeperUsageURL         = "https://chatgpt.com/backend-api/wham/usage"
	keeperLogFilePrefix    = "codex-keeper-"
	keeperLogComponent     = "codex_keeper"
	keeperLogRetainedFiles = 3
	keeperMaxInMemoryLogs  = 300
)

type KeeperRunner struct {
	app            *App
	mu             sync.Mutex
	daemonStop     chan struct{}
	daemonDone     chan struct{}
	running        bool
	runningModes   map[string]struct{}
	inFlightAuths  map[string]string
	state          string
	detail         string
	mode           *string
	lastStartedAt  *time.Time
	lastFinishedAt *time.Time
	stats          keeperStats
	logs           []string
}

type keeperStats struct {
	Total            int `json:"total"`
	Healthy          int `json:"healthy"`
	StatusDisabled   int `json:"status_disabled"`
	StatusEnabled    int `json:"status_enabled"`
	PriorityDegraded int `json:"priority_degraded"`
	PriorityRestored int `json:"priority_restored"`
	Skipped          int `json:"skipped"`
	NetworkError     int `json:"network_error"`
}

type keeperStatusResponse struct {
	Running        bool        `json:"running"`
	RunningModes   []string    `json:"running_modes"`
	DaemonRunning  bool        `json:"daemon_running"`
	State          string      `json:"state"`
	Detail         string      `json:"detail"`
	Mode           *string     `json:"mode"`
	LastStartedAt  *string     `json:"last_started_at"`
	LastFinishedAt *string     `json:"last_finished_at"`
	Stats          keeperStats `json:"stats"`
	Logs           []string    `json:"logs"`
}

type keeperPriorityRule struct {
	AccountType string `json:"account_type"`
	Priority    int    `json:"priority"`
}

type keeperSettingsUpdateRequest struct {
	ScheduleCron                      *string              `json:"schedule_cron"`
	QuotaThreshold                    *int                 `json:"quota_threshold"`
	UsageTimeoutSeconds               *int                 `json:"usage_timeout_seconds"`
	CPATimeoutSeconds                 *int                 `json:"cpa_timeout_seconds"`
	MaxRetries                        *int                 `json:"max_retries"`
	WorkerThreads                     *int                 `json:"worker_threads"`
	ConditionalRefreshIntervalSeconds *int                 `json:"conditional_refresh_interval_seconds"`
	AccountRefreshCacheMinutes        *int                 `json:"account_refresh_cache_minutes"`
	DryRun                            *bool                `json:"dry_run"`
	AutoStartDaemon                   *bool                `json:"auto_start_daemon"`
	PriorityRules                     []keeperPriorityRule `json:"priority_rules"`
}

type keeperCronPreviewRequest struct {
	ScheduleCron string `json:"schedule_cron"`
}

type keeperBulkDeleteRequest struct {
	AuthNames []string `json:"auth_names"`
}

type keeperRefreshAccountsRequest struct {
	AuthNames []string `json:"auth_names"`
}

type keeperPriorityUpdateRequest struct {
	Priority int `json:"priority"`
}

type keeperAccount struct {
	Name                 string     `json:"name"`
	Email                *string    `json:"email"`
	AccountType          *string    `json:"account_type"`
	Disabled             bool       `json:"disabled"`
	Priority             *int       `json:"priority"`
	PrimaryUsedPercent   *int       `json:"primary_used_percent"`
	SecondaryUsedPercent *int       `json:"secondary_used_percent"`
	PrimaryResetAt       *time.Time `json:"primary_reset_at"`
	SecondaryResetAt     *time.Time `json:"secondary_reset_at"`
	QuotaThreshold       *int       `json:"quota_threshold"`
	LastStatusCode       *int       `json:"last_status_code"`
	LastError            *string    `json:"last_error"`
	LatestAction         *string    `json:"latest_action"`
	LastCheckedAt        *time.Time `json:"last_checked_at"`
	LastHealthyAt        *time.Time `json:"last_healthy_at"`
}

type keeperAccountResponse struct {
	Name                 string  `json:"name"`
	Email                *string `json:"email"`
	AccountType          *string `json:"account_type"`
	Disabled             bool    `json:"disabled"`
	Priority             *int    `json:"priority"`
	PrimaryUsedPercent   *int    `json:"primary_used_percent"`
	SecondaryUsedPercent *int    `json:"secondary_used_percent"`
	PrimaryResetAt       *string `json:"primary_reset_at"`
	SecondaryResetAt     *string `json:"secondary_reset_at"`
	QuotaThreshold       *int    `json:"quota_threshold"`
	LastStatusCode       *int    `json:"last_status_code"`
	LastError            *string `json:"last_error"`
	LatestAction         *string `json:"latest_action"`
	LastCheckedAt        *string `json:"last_checked_at"`
	LastHealthyAt        *string `json:"last_healthy_at"`
}

type keeperAuthState struct {
	keeperAccount
	RestorePriority *int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type keeperUsageInfo struct {
	PlanType             string
	PrimaryUsedPercent   int
	SecondaryUsedPercent *int
	PrimaryResetAt       *time.Time
	SecondaryResetAt     *time.Time
}

type keeperHTTPResult struct {
	StatusCode *int
	JSONData   map[string]any
	Brief      string
	Error      string
}

type keeperAccountResult struct {
	Name                 string
	Result               string
	Email                *string
	AccountType          *string
	Priority             *int
	RestorePriority      *int
	ClearRestorePriority bool
	Disabled             *bool
	PrimaryUsedPercent   *int
	SecondaryUsedPercent *int
	PrimaryResetAt       *time.Time
	SecondaryResetAt     *time.Time
	QuotaThreshold       *int
	LastStatusCode       *int
	LastError            *string
	LatestAction         *string
	CheckedAt            time.Time
}

func NewKeeperRunner(app *App) *KeeperRunner {
	return &KeeperRunner{
		app:    app,
		state:  "idle",
		detail: "尚未运行",
		logs:   []string{},
	}
}

func (r *KeeperRunner) LoadPersistedState(ctx context.Context) {
	logs, logErr := r.app.loadKeeperLogLines(keeperMaxInMemoryLogs)
	if logErr != nil {
		log.Printf("restore codex keeper logs failed: %v", logErr)
	}
	run, err := r.app.latestKeeperRun(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = logs
	if err != nil || run == nil {
		return
	}
	r.state = run.State
	r.detail = run.Detail
	r.mode = run.Mode
	r.lastStartedAt = run.StartedAt
	r.lastFinishedAt = run.FinishedAt
	r.stats = run.Stats
}

func (r *KeeperRunner) StartAutoIfConfigured() {
	cfg, err := r.app.loadConfig(context.Background())
	if err != nil {
		r.log("读取 Codex Keeper 自动启动配置失败：" + err.Error())
		return
	}
	if cfg.CodexKeeper.AutoStartDaemon && strings.TrimSpace(cfg.Collector.ManagementKey) != "" {
		if err := r.StartDaemon(); err != nil {
			r.log("启动 Codex Keeper 自动巡检失败：" + err.Error())
		}
	}
}

func (r *KeeperRunner) StartOnce() error {
	if !r.markRunning("once") {
		return conflictError("Codex Keeper 正在运行")
	}
	go r.run("once")
	return nil
}

func (r *KeeperRunner) StartAccounts(authNames []string) error {
	names, err := normalizeKeeperAuthNames(authNames)
	if err != nil {
		return err
	}
	if !r.markRunning("accounts") {
		return conflictError("Codex Keeper 正在运行")
	}
	go r.runAccounts("accounts", names)
	return nil
}

func (r *KeeperRunner) StartDaemon() error {
	cfg, err := r.app.loadConfig(context.Background())
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		return validationError("管理密钥未设置，无法运行 Codex Keeper")
	}
	if _, _, err := nextRunTimes(cfg.CodexKeeper.ScheduleCron, 1, time.Now()); err != nil {
		return err
	}

	r.mu.Lock()
	if r.daemonRunningLocked() {
		r.mu.Unlock()
		return nil
	}
	r.daemonStop = make(chan struct{})
	r.daemonDone = make(chan struct{})
	stop := r.daemonStop
	done := r.daemonDone
	r.mu.Unlock()

	go r.daemonLoop(stop, done)
	r.log("Codex Keeper 已开始按计划自动巡检")
	return nil
}

func (r *KeeperRunner) Stop() {
	r.mu.Lock()
	stop := r.daemonStop
	done := r.daemonDone
	if stop == nil || done == nil {
		r.mu.Unlock()
		return
	}
	select {
	case <-done:
		r.daemonStop = nil
		r.daemonDone = nil
		r.mu.Unlock()
		return
	default:
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	r.mu.Unlock()
	<-done
	r.mu.Lock()
	if r.daemonStop == stop {
		r.daemonStop = nil
	}
	if r.daemonDone == done {
		r.daemonDone = nil
	}
	r.mu.Unlock()
	r.log("Codex Keeper 已停止自动巡检")
}

func (r *KeeperRunner) ClearLogs() {
	r.mu.Lock()
	r.logs = []string{}
	r.mu.Unlock()
	if err := r.app.clearKeeperLogFiles(); err != nil {
		log.Printf("clear codex keeper log files failed: %v", err)
	}
}

func (r *KeeperRunner) Status() keeperStatusResponse {
	r.mu.Lock()
	logs := append([]string{}, r.logs...)
	runningModes := r.runningModeListLocked()
	response := keeperStatusResponse{
		Running:        len(runningModes) > 0,
		RunningModes:   runningModes,
		DaemonRunning:  r.daemonRunningLocked(),
		State:          r.state,
		Detail:         r.detail,
		Mode:           cloneStringPtr(r.mode),
		LastStartedAt:  apiDateTimePtr(r.lastStartedAt),
		LastFinishedAt: apiDateTimePtr(r.lastFinishedAt),
		Stats:          r.stats,
		Logs:           logs,
	}
	r.mu.Unlock()
	if r.app != nil {
		response.Stats = keeperStats{}
		if run, err := r.app.latestKeeperRunByMode(context.Background(), "daemon"); err == nil && run != nil {
			response.Stats = run.Stats
		}
	}
	return response
}

func (r *KeeperRunner) daemonRunningLocked() bool {
	if r.daemonDone == nil {
		return false
	}
	select {
	case <-r.daemonDone:
		return false
	default:
		return true
	}
}

func (r *KeeperRunner) daemonLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		cfg, err := r.app.loadConfig(context.Background())
		if err != nil {
			r.log("读取 Codex Keeper 配置失败：" + err.Error())
			if waitForStop(stop, time.Minute) {
				return
			}
			continue
		}
		times, _, err := nextRunTimes(cfg.CodexKeeper.ScheduleCron, 1, time.Now().In(appTimeLocation))
		if err != nil {
			r.log("Codex Keeper 定时表达式无效：" + err.Error())
			if waitForStop(stop, time.Minute) {
				return
			}
			continue
		}
		delay := positiveDuration(time.Until(times[0]))
		r.log("下一轮计划：" + times[0].In(appTimeLocation).Format("2006-01-02 15:04:05"))
		cronTimer := time.NewTimer(delay)
		conditionalInterval := keeperConditionalRefreshInterval(cfg)
		var conditionalTicker *time.Ticker
		var conditionalC <-chan time.Time
		if conditionalInterval > 0 {
			conditionalTicker = time.NewTicker(conditionalInterval)
			conditionalC = conditionalTicker.C
		}

		restartCycle := false
		for !restartCycle {
			select {
			case <-stop:
				cronTimer.Stop()
				if conditionalTicker != nil {
					conditionalTicker.Stop()
				}
				return
			case <-cronTimer.C:
				if conditionalTicker != nil {
					conditionalTicker.Stop()
				}
				if r.markRunning("daemon") {
					go r.run("daemon")
				}
				restartCycle = true
			case <-conditionalC:
				nextCfg, err := r.app.loadConfig(context.Background())
				if err != nil {
					r.log("读取 Codex Keeper 条件刷新配置失败：" + err.Error())
					continue
				}
				nextInterval := keeperConditionalRefreshInterval(nextCfg)
				if nextInterval != conditionalInterval {
					cronTimer.Stop()
					if conditionalTicker != nil {
						conditionalTicker.Stop()
					}
					restartCycle = true
					continue
				}
				names, err := r.app.conditionalKeeperRefreshCandidates(context.Background(), nextCfg)
				if err != nil {
					r.log("Codex Keeper 条件刷新候选查询失败：" + err.Error())
					continue
				}
				if len(names) == 0 {
					continue
				}
				if r.markRunning("conditional") {
					go r.runAccounts("conditional", names)
				}
			}
		}
	}
}

func positiveDuration(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	return delay
}

func keeperConditionalRefreshInterval(cfg AppConfig) time.Duration {
	seconds := cfg.CodexKeeper.ConditionalRefreshIntervalSeconds
	if !validKeeperConditionalRefreshInterval(seconds) || seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (r *KeeperRunner) ensureRunningModesLocked() {
	if r.runningModes == nil {
		r.runningModes = map[string]struct{}{}
	}
}

func (r *KeeperRunner) runningModeListLocked() []string {
	r.ensureRunningModesLocked()
	modes := make([]string, 0, len(r.runningModes))
	for mode := range r.runningModes {
		modes = append(modes, mode)
	}
	sort.Slice(modes, func(i, j int) bool {
		leftOrder := keeperModeOrder(modes[i])
		rightOrder := keeperModeOrder(modes[j])
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return modes[i] < modes[j]
	})
	return modes
}

func (r *KeeperRunner) ensureInFlightAuthsLocked() {
	if r.inFlightAuths == nil {
		r.inFlightAuths = map[string]string{}
	}
}

func (r *KeeperRunner) tryLockAuthName(mode string, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInFlightAuthsLocked()
	if _, exists := r.inFlightAuths[name]; exists {
		return false
	}
	r.inFlightAuths[name] = mode
	return true
}

func (r *KeeperRunner) unlockAuthName(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlightAuths == nil {
		return
	}
	delete(r.inFlightAuths, name)
}

func keeperModeOrder(mode string) int {
	switch mode {
	case "daemon":
		return 0
	case "once":
		return 1
	case "conditional":
		return 2
	case "accounts":
		return 3
	default:
		return 99
	}
}

func keeperModesConflict(existingMode string, nextMode string) bool {
	if existingMode == nextMode {
		return true
	}
	return (existingMode == "once" && nextMode == "daemon") ||
		(existingMode == "daemon" && nextMode == "once")
}

func keeperStatusModePtr(modes []string) *string {
	if len(modes) == 0 {
		return nil
	}
	mode := modes[0]
	return &mode
}

func keeperRunningDetail(modes []string) string {
	if len(modes) > 1 {
		return "正在运行多个 Codex Keeper 任务"
	}
	if len(modes) == 0 {
		return "尚未运行"
	}
	switch modes[0] {
	case "accounts":
		return "正在刷新 Codex 账号"
	case "conditional":
		return "正在按条件刷新 Codex 账号"
	default:
		return "正在巡检 Codex 账号"
	}
}

func (r *KeeperRunner) markRunning(mode string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRunningModesLocked()
	for runningMode := range r.runningModes {
		if keeperModesConflict(runningMode, mode) {
			return false
		}
	}
	r.runningModes[mode] = struct{}{}
	now := time.Now().In(appTimeLocation)
	r.running = true
	r.state = "running"
	runningModes := r.runningModeListLocked()
	r.detail = keeperRunningDetail(runningModes)
	r.mode = keeperStatusModePtr(runningModes)
	r.lastStartedAt = &now
	r.lastFinishedAt = nil
	r.stats = keeperStats{}
	return true
}

func (r *KeeperRunner) run(mode string) {
	r.runAccounts(mode, nil)
}

func (r *KeeperRunner) runAccounts(mode string, authNames []string) {
	options := keeperRunOptionsForMode(mode, authNames)
	options.TryLockAuthName = r.tryLockAuthName
	options.UnlockAuthName = r.unlockAuthName
	stats, detail, err := r.app.executeKeeperRunWithOptions(context.Background(), options, r.log)
	finishedAt := time.Now().In(appTimeLocation)
	logMessage := detail
	if err != nil {
		logMessage = "巡检失败：" + err.Error()
	}
	r.mu.Lock()
	r.ensureRunningModesLocked()
	delete(r.runningModes, mode)
	runningModes := r.runningModeListLocked()
	r.running = len(runningModes) > 0
	r.lastFinishedAt = &finishedAt
	r.stats = stats
	if r.running {
		r.state = "running"
		r.detail = keeperRunningDetail(runningModes)
		r.mode = keeperStatusModePtr(runningModes)
	} else if err != nil {
		r.state = "failed"
		r.detail = err.Error()
	} else {
		completedMode := mode
		r.mode = &completedMode
		r.state = "completed"
		r.detail = detail
	}
	r.mu.Unlock()
	if strings.TrimSpace(logMessage) != "" {
		r.log(logMessage)
	}
}

func (r *KeeperRunner) log(message string) {
	timestamp := time.Now().In(appTimeLocation)
	line := formatKeeperLogLine(timestamp, message)
	r.mu.Lock()
	r.logs = appendKeeperLog(r.logs, line)
	r.mu.Unlock()
	if err := r.app.appendKeeperLogFile(timestamp, line); err != nil {
		log.Printf("write codex keeper log failed: %v", err)
	}
}

func appendKeeperLog(logs []string, line string) []string {
	logs = append(logs, line)
	if len(logs) > keeperMaxInMemoryLogs {
		logs = logs[len(logs)-keeperMaxInMemoryLogs:]
	}
	return logs
}

func formatKeeperLogLine(timestamp time.Time, message string) string {
	var output strings.Builder
	handler := slog.NewTextHandler(&output, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.TimeKey {
				return slog.String(slog.TimeKey, timestamp.In(appTimeLocation).Format("2006-01-02T15:04:05.000Z07:00"))
			}
			return attr
		},
	})
	record := slog.NewRecord(timestamp.In(appTimeLocation), slog.LevelInfo, message, 0)
	record.AddAttrs(slog.String("component", keeperLogComponent))
	_ = handler.Handle(context.Background(), record)
	return strings.TrimSuffix(output.String(), "\n")
}

type keeperLogFile struct {
	path string
	date time.Time
}

func (a *App) keeperLogDir() string {
	return filepath.Join(a.dataDir, "logs")
}

func (a *App) appendKeeperLogFile(timestamp time.Time, line string) error {
	dir := a.keeperLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, keeperLogFilePrefix+timestamp.In(appTimeLocation).Format("2006-01-02")+".log")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(line + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return a.pruneKeeperLogFiles()
}

func (a *App) loadKeeperLogLines(limit int) ([]string, error) {
	files, err := a.keeperLogFiles()
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].date.Before(files[j].date)
	})
	if len(files) > keeperLogRetainedFiles {
		files = files[len(files)-keeperLogRetainedFiles:]
	}
	lines := []string{}
	for _, file := range files {
		handle, err := os.Open(file.path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(handle)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			lines = appendKeeperLog(lines, line)
		}
		scanErr := scanner.Err()
		closeErr := handle.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if limit > 0 && len(lines) > limit {
		return lines[len(lines)-limit:], nil
	}
	return lines, nil
}

func (a *App) pruneKeeperLogFiles() error {
	files, err := a.keeperLogFiles()
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].date.After(files[j].date)
	})
	for index, file := range files {
		if index < keeperLogRetainedFiles {
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (a *App) clearKeeperLogFiles() error {
	files, err := a.keeperLogFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (a *App) keeperLogFiles() ([]keeperLogFile, error) {
	dir := a.keeperLogDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	files := []keeperLogFile{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, keeperLogFilePrefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		dateText := strings.TrimSuffix(strings.TrimPrefix(name, keeperLogFilePrefix), ".log")
		date, err := time.ParseInLocation("2006-01-02", dateText, appTimeLocation)
		if err != nil {
			continue
		}
		files = append(files, keeperLogFile{
			path: filepath.Join(dir, name),
			date: date,
		})
	}
	return files, nil
}

func (a *App) handleCodexKeeper(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	parts := splitPath(r.URL.Path, "/api/codex-keeper/")
	if len(parts) == 0 {
		return notFoundError("Not Found")
	}
	switch {
	case len(parts) == 1 && parts[0] == "settings":
		if r.Method == http.MethodGet {
			cfg, err := a.loadConfig(r.Context())
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, keeperSettingsResponse(cfg))
			return nil
		}
		if r.Method == http.MethodPut {
			return a.updateKeeperSettings(w, r)
		}
		return methodNotAllowed()
	case len(parts) == 2 && parts[0] == "schedule" && parts[1] == "preview":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		var payload keeperCronPreviewRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		times, normalized, err := nextRunTimes(payload.ScheduleCron, 5, time.Now())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedule_cron": normalized, "next_run_times": apiDateTimes(times)})
		return nil
	case len(parts) == 1 && parts[0] == "status":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, a.keeper.Status())
		return nil
	case len(parts) == 1 && parts[0] == "accounts":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		accounts, err := a.listKeeperAccounts(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": keeperAccountResponses(accounts)})
		return nil
	case len(parts) == 1 && parts[0] == "run-once":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		if err := a.keeper.StartOnce(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
		return nil
	case len(parts) == 1 && parts[0] == "start":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		if err := a.keeper.StartDaemon(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
		return nil
	case len(parts) == 1 && parts[0] == "stop":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		a.keeper.Stop()
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
		return nil
	case len(parts) == 2 && parts[0] == "logs" && parts[1] == "clear":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		a.keeper.ClearLogs()
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return nil
	case len(parts) == 2 && parts[0] == "accounts" && parts[1] == "bulk-delete":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		return a.bulkDeleteKeeperAccounts(w, r)
	case len(parts) == 2 && parts[0] == "accounts" && parts[1] == "refresh":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		var payload keeperRefreshAccountsRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		if err := a.keeper.StartAccounts(payload.AuthNames); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
		return nil
	case len(parts) == 3 && parts[0] == "accounts" && (parts[2] == "enable" || parts[2] == "disable"):
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		authName, err := url.PathUnescape(parts[1])
		if err != nil {
			return validationError("账号名称无效")
		}
		disabled := parts[2] == "disable"
		if err := a.setKeeperAccountDisabled(r.Context(), authName, disabled); err != nil {
			return err
		}
		if disabled {
			writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
		}
		return nil
	case len(parts) == 2 && parts[0] == "accounts" && r.Method == http.MethodDelete:
		authName, err := url.PathUnescape(parts[1])
		if err != nil {
			return validationError("账号名称无效")
		}
		if err := a.deleteKeeperAccount(r.Context(), authName); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return nil
	case len(parts) == 3 && parts[0] == "accounts" && parts[2] == "priority":
		if err := requireMethod(r, http.MethodPatch); err != nil {
			return err
		}
		authName, err := url.PathUnescape(parts[1])
		if err != nil {
			return validationError("账号名称无效")
		}
		var payload keeperPriorityUpdateRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		if err := a.updateKeeperAccountPriority(r.Context(), authName, payload.Priority); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return nil
	default:
		return notFoundError("Not Found")
	}
}

func keeperSettingsResponse(cfg AppConfig) map[string]any {
	times, normalized, err := nextRunTimes(cfg.CodexKeeper.ScheduleCron, 5, time.Now())
	if err != nil {
		normalized = cfg.CodexKeeper.ScheduleCron
		times = []time.Time{}
	}
	return map[string]any{
		"cliaproxy_url":                        cfg.Collector.CLIProxyURL,
		"management_key_set":                   strings.TrimSpace(cfg.Collector.ManagementKey) != "",
		"schedule_cron":                        normalized,
		"next_run_times":                       apiDateTimes(times),
		"quota_threshold":                      cfg.CodexKeeper.QuotaThreshold,
		"usage_timeout_seconds":                cfg.CodexKeeper.UsageTimeoutSeconds,
		"cpa_timeout_seconds":                  cfg.CodexKeeper.CPATimeoutSeconds,
		"max_retries":                          cfg.CodexKeeper.MaxRetries,
		"worker_threads":                       cfg.CodexKeeper.WorkerThreads,
		"conditional_refresh_interval_seconds": cfg.CodexKeeper.ConditionalRefreshIntervalSeconds,
		"account_refresh_cache_minutes":        cfg.CodexKeeper.AccountRefreshCacheMinutes,
		"dry_run":                              cfg.CodexKeeper.DryRun,
		"auto_start_daemon":                    cfg.CodexKeeper.AutoStartDaemon,
		"priority_rules":                       sortedPriorityRules(cfg.CodexKeeperPriorityRule),
	}
}

func keeperAccountResponses(accounts []keeperAccount) []keeperAccountResponse {
	responses := make([]keeperAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		responses = append(responses, keeperAccountResponse{
			Name:                 account.Name,
			Email:                account.Email,
			AccountType:          account.AccountType,
			Disabled:             account.Disabled,
			Priority:             keeperDisplayPriority(account.Priority),
			PrimaryUsedPercent:   account.PrimaryUsedPercent,
			SecondaryUsedPercent: account.SecondaryUsedPercent,
			PrimaryResetAt:       apiDateTimePtr(account.PrimaryResetAt),
			SecondaryResetAt:     apiDateTimePtr(account.SecondaryResetAt),
			QuotaThreshold:       account.QuotaThreshold,
			LastStatusCode:       account.LastStatusCode,
			LastError:            account.LastError,
			LatestAction:         account.LatestAction,
			LastCheckedAt:        apiDateTimePtr(account.LastCheckedAt),
			LastHealthyAt:        apiDateTimePtr(account.LastHealthyAt),
		})
	}
	return responses
}

func (a *App) updateKeeperSettings(w http.ResponseWriter, r *http.Request) error {
	var payload keeperSettingsUpdateRequest
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	cfg, err := a.loadConfig(r.Context())
	if err != nil {
		return err
	}
	if payload.ScheduleCron != nil {
		_, normalized, err := nextRunTimes(*payload.ScheduleCron, 5, time.Now())
		if err != nil {
			return err
		}
		cfg.CodexKeeper.ScheduleCron = normalized
	}
	if payload.QuotaThreshold != nil {
		if *payload.QuotaThreshold < 0 || *payload.QuotaThreshold > 100 {
			return validationError("quota_threshold 超出范围")
		}
		cfg.CodexKeeper.QuotaThreshold = *payload.QuotaThreshold
	}
	if payload.UsageTimeoutSeconds != nil {
		if *payload.UsageTimeoutSeconds < 1 {
			return validationError("usage_timeout_seconds 不能小于 1")
		}
		cfg.CodexKeeper.UsageTimeoutSeconds = *payload.UsageTimeoutSeconds
	}
	if payload.CPATimeoutSeconds != nil {
		if *payload.CPATimeoutSeconds < 1 {
			return validationError("cpa_timeout_seconds 不能小于 1")
		}
		cfg.CodexKeeper.CPATimeoutSeconds = *payload.CPATimeoutSeconds
	}
	if payload.MaxRetries != nil {
		if *payload.MaxRetries < 0 || *payload.MaxRetries > 5 {
			return validationError("max_retries 超出范围")
		}
		cfg.CodexKeeper.MaxRetries = *payload.MaxRetries
	}
	if payload.WorkerThreads != nil {
		if *payload.WorkerThreads < 1 || *payload.WorkerThreads > 64 {
			return validationError("worker_threads 超出范围")
		}
		cfg.CodexKeeper.WorkerThreads = *payload.WorkerThreads
	}
	if payload.ConditionalRefreshIntervalSeconds != nil {
		if !validKeeperConditionalRefreshInterval(*payload.ConditionalRefreshIntervalSeconds) {
			return validationError("conditional_refresh_interval_seconds 超出范围")
		}
		cfg.CodexKeeper.ConditionalRefreshIntervalSeconds = *payload.ConditionalRefreshIntervalSeconds
	}
	if payload.AccountRefreshCacheMinutes != nil {
		if *payload.AccountRefreshCacheMinutes < 1 {
			return validationError("account_refresh_cache_minutes 不能小于 1")
		}
		cfg.CodexKeeper.AccountRefreshCacheMinutes = *payload.AccountRefreshCacheMinutes
	}
	if payload.DryRun != nil {
		cfg.CodexKeeper.DryRun = *payload.DryRun
	}
	if payload.AutoStartDaemon != nil {
		cfg.CodexKeeper.AutoStartDaemon = *payload.AutoStartDaemon
	}
	if payload.PriorityRules != nil {
		rules := map[string]int{}
		for _, item := range payload.PriorityRules {
			key := strings.ToLower(strings.TrimSpace(item.AccountType))
			if key == "" {
				return validationError("账号类型不能为空")
			}
			if item.Priority < 0 || item.Priority > 20 {
				return validationError("priority 超出范围")
			}
			rules[key] = item.Priority
		}
		cfg.CodexKeeperPriorityRule = normalizePriorityRules(rules)
	}
	if err := a.saveConfig(r.Context(), cfg); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, keeperSettingsResponse(cfg))
	return nil
}

func (a *App) executeKeeperRun(ctx context.Context, mode string, logFn func(string)) (keeperStats, string, error) {
	return a.executeKeeperRunForAccounts(ctx, mode, nil, logFn)
}

type keeperRunOptions struct {
	Mode            string
	AuthNames       []string
	ManualRefresh   bool
	UseRefreshCache bool
	PersistRun      bool
	TryLockAuthName func(string, string) bool
	UnlockAuthName  func(string)
}

func (a *App) executeKeeperRunForAccounts(ctx context.Context, mode string, authNames []string, logFn func(string)) (keeperStats, string, error) {
	return a.executeKeeperRunWithOptions(ctx, keeperRunOptionsForMode(mode, authNames), logFn)
}

func keeperRunOptionsForMode(mode string, authNames []string) keeperRunOptions {
	return keeperRunOptions{
		Mode:            mode,
		AuthNames:       authNames,
		ManualRefresh:   mode == "accounts",
		UseRefreshCache: mode == "daemon" || mode == "conditional",
		PersistRun:      keeperModePersistsRun(mode),
	}
}

func keeperModePersistsRun(mode string) bool {
	return mode != "accounts" && mode != "conditional"
}

func (a *App) executeKeeperRunWithOptions(ctx context.Context, options keeperRunOptions, logFn func(string)) (keeperStats, string, error) {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return keeperStats{}, "", err
	}
	if strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		return keeperStats{}, "", validationError("管理密钥未设置，无法运行 Codex Keeper")
	}
	runID := 0
	if options.PersistRun {
		runID, err = a.createKeeperRun(ctx, options.Mode)
		if err != nil {
			return keeperStats{}, "", err
		}
	}
	targetNames, err := normalizeOptionalKeeperAuthNames(options.AuthNames)
	if err != nil {
		if runID > 0 {
			_ = a.finishKeeperRun(ctx, runID, "failed", err.Error(), keeperStats{})
		}
		return keeperStats{}, "", err
	}
	targetSet := map[string]bool{}
	for _, name := range targetNames {
		targetSet[name] = true
	}
	if options.Mode == "conditional" {
		logFn(fmt.Sprintf("开始按条件刷新 %d 个 Codex 账号", len(targetSet)))
	} else if len(targetSet) > 0 {
		logFn(fmt.Sprintf("开始刷新 %d 个 Codex 账号", len(targetSet)))
	} else {
		logFn("开始 Codex 账号巡检")
	}
	stats := keeperStats{}
	detail := "巡检完成"
	authFiles, err := a.listKeeperRemoteAuthFiles(ctx, cfg)
	if err != nil {
		if runID > 0 {
			_ = a.finishKeeperRun(ctx, runID, "failed", err.Error(), stats)
		}
		return stats, "", err
	}
	filtered := make([]map[string]any, 0, len(authFiles))
	remoteCodexNames := map[string]bool{}
	for _, item := range authFiles {
		if keeperString(item["type"]) != "codex" {
			continue
		}
		name := keeperString(item["name"])
		if name != "" {
			remoteCodexNames[name] = true
		}
		if len(targetSet) == 0 || targetSet[name] {
			filtered = append(filtered, item)
		}
	}
	if len(targetSet) == 0 {
		pruned, err := a.pruneKeeperMissingAuthStates(ctx, remoteCodexNames)
		if err != nil {
			if runID > 0 {
				_ = a.finishKeeperRun(ctx, runID, "failed", err.Error(), stats)
			}
			return stats, "", err
		}
		if pruned > 0 {
			logFn(fmt.Sprintf("清理本地已不存在的 Codex 账号 %d 个", pruned))
		}
	}
	stats.Total = len(filtered)
	if options.UseRefreshCache {
		var skipped int
		filtered, skipped, err = a.filterKeeperCachedAuthItems(ctx, filtered, cfg)
		if err != nil {
			if runID > 0 {
				_ = a.finishKeeperRun(ctx, runID, "failed", err.Error(), stats)
			}
			return stats, "", err
		}
		stats.Skipped += skipped
	}
	if len(filtered) == 0 {
		if stats.Total > 0 && options.UseRefreshCache {
			detail = "缓存时间内没有需要自动刷新的 Codex auth file"
		} else if len(targetSet) > 0 {
			detail = "未发现指定 Codex auth file"
		} else {
			detail = "未发现 Codex auth file"
		}
		if runID > 0 {
			_ = a.finishKeeperRun(ctx, runID, "completed", detail, stats)
		}
		return stats, detail, nil
	}
	for _, item := range filtered {
		name := keeperString(item["name"])
		locked := false
		if options.TryLockAuthName != nil && name != "" {
			if !options.TryLockAuthName(options.Mode, name) {
				stats.Skipped++
				logFn(name + ": 正在其他 Keeper 任务处理中，跳过")
				continue
			}
			locked = true
		}
		unlock := func() {
			if locked && options.UnlockAuthName != nil {
				options.UnlockAuthName(name)
				locked = false
			}
		}
		if locked && options.UseRefreshCache {
			cutoff := time.Now().In(appTimeLocation).Add(-keeperRefreshCacheDuration(cfg))
			cached, err := a.keeperAuthCheckedSince(ctx, name, cutoff)
			if err != nil {
				unlock()
				if runID > 0 {
					_ = a.finishKeeperRun(ctx, runID, "failed", err.Error(), stats)
				}
				return stats, "", err
			}
			if cached {
				unlock()
				stats.Skipped++
				logFn(name + ": 缓存时间内已刷新，跳过")
				continue
			}
		}
		result := a.processKeeperAuth(ctx, cfg, item, logFn, options.ManualRefresh)
		unlock()
		a.mergeKeeperStats(&stats, result)
		if runID > 0 {
			if err := a.recordKeeperRunAccount(ctx, runID, result); err != nil {
				logFn("写入巡检账号历史失败：" + err.Error())
			}
		}
	}
	if options.Mode == "conditional" {
		detail = fmt.Sprintf("条件刷新完成：健康 %d，坏凭证禁用 %d，优先级降级 %d，优先级恢复 %d，网络错误 %d，缓存跳过 %d", stats.Healthy, stats.StatusDisabled, stats.PriorityDegraded, stats.PriorityRestored, stats.NetworkError, stats.Skipped)
	} else if len(targetSet) > 0 {
		detail = fmt.Sprintf("账号刷新完成：健康 %d，凭证异常 %d，网络错误 %d", stats.Healthy, stats.StatusDisabled, stats.NetworkError)
	} else {
		detail = fmt.Sprintf("巡检完成：健康 %d，坏凭证禁用 %d，优先级降级 %d，网络错误 %d，缓存跳过 %d", stats.Healthy, stats.StatusDisabled, stats.PriorityDegraded, stats.NetworkError, stats.Skipped)
	}
	if runID > 0 {
		_ = a.finishKeeperRun(ctx, runID, "completed", detail, stats)
	}
	return stats, detail, nil
}

func keeperRefreshCacheDuration(cfg AppConfig) time.Duration {
	minutes := cfg.CodexKeeper.AccountRefreshCacheMinutes
	if minutes < 1 {
		minutes = 10
	}
	return time.Duration(minutes) * time.Minute
}

func (a *App) conditionalKeeperRefreshCandidates(ctx context.Context, cfg AppConfig) ([]string, error) {
	cacheWindow := keeperRefreshCacheDuration(cfg)
	since := time.Now().In(appTimeLocation).Add(-cacheWindow)
	aliases, err := a.keeperAuthNameAliases(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{}
	seen := map[string]bool{}
	usageIdentifiers := []string{}
	seenUsageIdentifiers := map[string]bool{}
	addName := func(name string) {
		normalized := strings.TrimSpace(name)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		names = append(names, normalized)
	}
	addUsageIdentifier := func(identifier string, allowOpaque bool) bool {
		normalized := strings.TrimSpace(identifier)
		aliasKey := keeperAuthAliasKey(normalized)
		if aliasKey == "" || seenUsageIdentifiers[aliasKey] {
			return len(keeperAuthNamesForUsageIdentifier(normalized, aliases)) > 0
		}
		if !allowOpaque && !keeperLooksLikeAuthIdentifier(normalized, aliases) {
			return false
		}
		seenUsageIdentifiers[aliasKey] = true
		usageIdentifiers = append(usageIdentifiers, normalized)
		resolved := false
		for _, name := range keeperAuthNamesForUsageIdentifier(normalized, aliases) {
			addName(name)
			resolved = true
		}
		return resolved
	}

	rows, err := a.db.QueryContext(ctx, `
		SELECT source, raw_json
		FROM usage_records
		WHERE timestamp >= ?
		ORDER BY timestamp DESC
	`, dbTime(since))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var source sql.NullString
		var rawJSON string
		if err := rows.Scan(&source, &rawJSON); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sourceResolved := false
		if source.Valid {
			sourceResolved = addUsageIdentifier(source.String, false)
		}
		if identifier := rawJSONStringField(rawJSON, "source"); identifier != nil {
			sourceResolved = addUsageIdentifier(*identifier, false) || sourceResolved
		}
		if sourceResolved {
			continue
		}
		for _, field := range []string{"auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId"} {
			if identifier := rawJSONStringField(rawJSON, field); identifier != nil {
				addUsageIdentifier(*identifier, true)
			}
		}
		for _, field := range []string{"email", "account_email", "accountEmail", "user_email", "userEmail"} {
			if identifier := rawJSONStringField(rawJSON, field); identifier != nil {
				addUsageIdentifier(*identifier, false)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range a.keeperAuthNamesFromRemoteUsageIdentifiers(ctx, cfg, usageIdentifiers, aliases) {
		addName(name)
	}

	rows, err = a.db.QueryContext(ctx, `
		SELECT auth_name
		FROM codex_keeper_auth_states
		WHERE (primary_reset_at IS NOT NULL AND primary_reset_at <= ?)
		   OR (secondary_reset_at IS NOT NULL AND secondary_reset_at <= ?)
		ORDER BY auth_name
	`, dbTime(time.Now().In(appTimeLocation)), dbTime(time.Now().In(appTimeLocation)))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		addName(name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	filtered, _, err := a.filterKeeperCachedAuthNames(ctx, names, cfg)
	return filtered, err
}

func (a *App) keeperAuthNameAliases(ctx context.Context) (map[string][]string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT auth_name, email
		FROM codex_keeper_auth_states
		ORDER BY auth_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aliases := map[string][]string{}
	for rows.Next() {
		var name string
		var email sql.NullString
		if err := rows.Scan(&name, &email); err != nil {
			return nil, err
		}
		addKeeperAuthAlias(aliases, name, name)
		if email.Valid {
			addKeeperAuthAlias(aliases, email.String, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aliases, nil
}

func (a *App) keeperAuthNamesFromRemoteUsageIdentifiers(ctx context.Context, cfg AppConfig, identifiers []string, aliases map[string][]string) []string {
	if !keeperHasUnresolvedUsageIdentifiers(identifiers, aliases) || strings.TrimSpace(cfg.Collector.CLIProxyURL) == "" {
		return nil
	}
	authFiles, err := a.listKeeperRemoteAuthFiles(ctx, cfg)
	if err != nil {
		return nil
	}
	codexFiles := make([]map[string]any, 0, len(authFiles))
	for _, item := range authFiles {
		if keeperString(item["type"]) != "codex" {
			continue
		}
		codexFiles = append(codexFiles, item)
		addKeeperAuthObjectAliases(aliases, item)
	}
	if !keeperHasUnresolvedUsageIdentifiers(identifiers, aliases) {
		return keeperAuthNamesForUsageIdentifiers(identifiers, aliases)
	}
	for _, item := range codexFiles {
		name := keeperString(item["name"])
		if name == "" {
			continue
		}
		detail, err := a.getKeeperRemoteAuthFile(ctx, cfg, name)
		if err != nil || detail == nil {
			continue
		}
		merged := mergeKeeperObjects(item, detail)
		addKeeperAuthObjectAliases(aliases, merged)
		if !keeperHasUnresolvedUsageIdentifiers(identifiers, aliases) {
			break
		}
	}
	return keeperAuthNamesForUsageIdentifiers(identifiers, aliases)
}

func keeperHasUnresolvedUsageIdentifiers(identifiers []string, aliases map[string][]string) bool {
	for _, identifier := range identifiers {
		if len(keeperAuthNamesForUsageIdentifier(identifier, aliases)) == 0 {
			return true
		}
	}
	return false
}

func keeperAuthNamesForUsageIdentifiers(identifiers []string, aliases map[string][]string) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, identifier := range identifiers {
		for _, name := range keeperAuthNamesForUsageIdentifier(identifier, aliases) {
			normalized := strings.TrimSpace(name)
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			names = append(names, normalized)
		}
	}
	return names
}

func keeperAuthNamesForUsageIdentifier(identifier string, aliases map[string][]string) []string {
	normalized := strings.TrimSpace(identifier)
	if normalized == "" {
		return nil
	}
	if names := aliases[keeperAuthAliasKey(normalized)]; len(names) > 0 {
		return names
	}
	if strings.HasSuffix(strings.ToLower(normalized), ".json") {
		return []string{normalized}
	}
	return nil
}

func keeperLooksLikeAuthIdentifier(identifier string, aliases map[string][]string) bool {
	normalized := strings.TrimSpace(identifier)
	if normalized == "" {
		return false
	}
	if len(aliases[keeperAuthAliasKey(normalized)]) > 0 {
		return true
	}
	lower := strings.ToLower(normalized)
	return strings.HasSuffix(lower, ".json") || strings.Contains(normalized, "@")
}

func addKeeperAuthObjectAliases(aliases map[string][]string, object map[string]any) {
	name := keeperString(object["name"])
	if name == "" {
		return
	}
	addKeeperAuthAlias(aliases, name, name)
	for _, key := range []string{"auth_name", "authName", "auth_index", "authIndex", "index", "source", "email", "account_email", "accountEmail", "user_email", "userEmail", "account_id", "accountId"} {
		if value := keeperAliasString(object[key]); value != "" {
			addKeeperAuthAlias(aliases, value, name)
		}
	}
}

func addKeeperAuthAlias(aliases map[string][]string, alias string, name string) {
	normalizedAlias := keeperAuthAliasKey(alias)
	normalizedName := strings.TrimSpace(name)
	if normalizedAlias == "" || normalizedName == "" {
		return
	}
	for _, existing := range aliases[normalizedAlias] {
		if existing == normalizedName {
			return
		}
	}
	aliases[normalizedAlias] = append(aliases[normalizedAlias], normalizedName)
}

func keeperAliasString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(typed, 'f', -1, 64))
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func keeperAuthAliasKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (a *App) filterKeeperCachedAuthItems(ctx context.Context, items []map[string]any, cfg AppConfig) ([]map[string]any, int, error) {
	if len(items) == 0 {
		return items, 0, nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if name := keeperString(item["name"]); name != "" {
			names = append(names, name)
		}
	}
	allowedNames, skipped, err := a.filterKeeperCachedAuthNames(ctx, names, cfg)
	if err != nil {
		return nil, 0, err
	}
	allowed := map[string]bool{}
	for _, name := range allowedNames {
		allowed[name] = true
	}
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		name := keeperString(item["name"])
		if name == "" || allowed[name] {
			filtered = append(filtered, item)
		}
	}
	return filtered, skipped, nil
}

func (a *App) filterKeeperCachedAuthNames(ctx context.Context, names []string, cfg AppConfig) ([]string, int, error) {
	normalized, err := normalizeOptionalKeeperAuthNames(names)
	if err != nil {
		return nil, 0, err
	}
	if len(normalized) == 0 {
		return normalized, 0, nil
	}
	cutoff := time.Now().In(appTimeLocation).Add(-keeperRefreshCacheDuration(cfg))
	filtered := make([]string, 0, len(normalized))
	skipped := 0
	for _, name := range normalized {
		cached, err := a.keeperAuthCheckedSince(ctx, name, cutoff)
		if err != nil {
			return nil, 0, err
		}
		if cached {
			skipped++
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered, skipped, nil
}

func (a *App) keeperAuthCheckedSince(ctx context.Context, name string, cutoff time.Time) (bool, error) {
	var lastChecked sql.NullString
	err := a.db.QueryRowContext(ctx, `
		SELECT CAST(last_checked_at AS TEXT)
		FROM codex_keeper_auth_states
		WHERE auth_name = ?
	`, name).Scan(&lastChecked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !lastChecked.Valid {
		return false, nil
	}
	checkedAt, ok := parseDBTime(lastChecked.String)
	if !ok {
		return false, nil
	}
	return checkedAt.After(cutoff) || checkedAt.Equal(cutoff), nil
}

func (a *App) processKeeperAuth(ctx context.Context, cfg AppConfig, authInfo map[string]any, logFn func(string), manualRefresh bool) keeperAccountResult {
	now := time.Now().In(appTimeLocation)
	name := keeperString(authInfo["name"])
	if name == "" {
		name = "unknown"
	}
	result := keeperAccountResult{Name: name, Result: "skipped", CheckedAt: now}
	detail, err := a.getKeeperRemoteAuthFile(ctx, cfg, name)
	if err != nil {
		message := "读取 auth file 详情失败：" + err.Error()
		result.LastError = &message
		result.LatestAction = &message
		_ = a.upsertKeeperState(ctx, result)
		logFn(name + ": " + message)
		return result
	}
	if detail == nil {
		message := "读取 auth file 详情失败"
		result.LastError = &message
		result.LatestAction = &message
		_ = a.upsertKeeperState(ctx, result)
		return result
	}
	merged := mergeKeeperObjects(authInfo, detail)
	result.Email = keeperStringPtr(merged["email"], merged["account_email"], merged["user_email"])
	result.Priority = keeperIntPtr(merged["priority"])
	disabled := keeperBool(merged["disabled"])
	result.Disabled = &disabled
	result.AccountType = accountTypeFromKeeperDetail(merged, nil)
	if disabled && !manualRefresh {
		result.Result = "disabled"
		a.preserveKeeperBadCredentialDiagnosis(ctx, &result)
		_ = a.upsertKeeperState(ctx, result)
		return result
	}
	if keeperString(merged["access_token"]) == "" {
		message := "缺少 access token"
		action := "刷新发现凭证不可用：" + message
		if !cfg.CodexKeeper.DryRun {
			if !disabled {
				if err := a.setKeeperRemoteDisabled(ctx, cfg, name, true); err != nil {
					message = "禁用坏凭证失败：" + err.Error()
					result.LastError = &message
					result.Result = "network_error"
					_ = a.upsertKeeperState(ctx, result)
					return result
				}
			}
			_ = a.setKeeperRemotePriority(ctx, cfg, name, nil)
			disabled = true
			result.Disabled = &disabled
			result.Priority = nil
			action = "禁用凭证：" + message
		} else {
			action = "模拟禁用：" + message
		}
		result.Result = "status_disabled"
		result.LastError = &message
		result.LatestAction = &action
		_ = a.upsertKeeperState(ctx, result)
		logFn(name + ": " + action)
		return result
	}

	usageResult := a.checkKeeperUsage(ctx, cfg, merged)
	if usageResult.StatusCode == nil {
		message := "网络检测失败：" + usageResult.Error
		result.Result = "network_error"
		result.LastError = &message
		result.LatestAction = &message
		_ = a.upsertKeeperState(ctx, result)
		logFn(name + ": " + message)
		return result
	}
	result.LastStatusCode = usageResult.StatusCode
	if isBadKeeperCredential(usageResult) {
		message := fmt.Sprintf("凭证不可用：HTTP %d", *usageResult.StatusCode)
		if usageResult.Brief != "" {
			message += "，" + usageResult.Brief
		}
		action := "刷新发现凭证不可用：" + message
		if !cfg.CodexKeeper.DryRun {
			if !disabled {
				if err := a.setKeeperRemoteDisabled(ctx, cfg, name, true); err != nil {
					message = "禁用坏凭证失败：" + err.Error()
					result.Result = "network_error"
					result.LastError = &message
					_ = a.upsertKeeperState(ctx, result)
					return result
				}
			}
			_ = a.setKeeperRemotePriority(ctx, cfg, name, nil)
			disabled = true
			result.Disabled = &disabled
			result.Priority = nil
			action = "禁用凭证：" + message
		} else {
			action = "模拟禁用：" + message
		}
		result.Result = "status_disabled"
		result.LastError = &message
		result.LatestAction = &action
		_ = a.upsertKeeperState(ctx, result)
		logFn(name + ": " + action)
		return result
	}
	if *usageResult.StatusCode < 200 || *usageResult.StatusCode >= 300 {
		message := fmt.Sprintf("usage 检测失败：HTTP %d", *usageResult.StatusCode)
		if usageResult.Brief != "" {
			message += "，" + usageResult.Brief
		}
		result.Result = "network_error"
		result.LastError = &message
		result.LatestAction = &message
		_ = a.upsertKeeperState(ctx, result)
		return result
	}
	usage := parseKeeperUsageInfo(usageResult.JSONData)
	result.AccountType = accountTypeFromKeeperDetail(merged, &usage)
	result.PrimaryUsedPercent = &usage.PrimaryUsedPercent
	result.SecondaryUsedPercent = usage.SecondaryUsedPercent
	result.PrimaryResetAt = usage.PrimaryResetAt
	result.SecondaryResetAt = usage.SecondaryResetAt
	result.QuotaThreshold = &cfg.CodexKeeper.QuotaThreshold
	result.Result = "healthy"

	if manualRefresh {
		accountType := "unknown"
		if result.AccountType != nil && strings.TrimSpace(*result.AccountType) != "" {
			accountType = *result.AccountType
		}
		action := fmt.Sprintf("刷新完成，类型 %s", accountType)
		result.LatestAction = &action
		result.LastError = nil
		_ = a.upsertKeeperState(ctx, result)
		logFn(name + ": " + action)
		return result
	}

	var restorePriority *int
	if state, err := a.getKeeperState(ctx, name); err == nil {
		restorePriority = state.RestorePriority
	}
	action := a.applyKeeperPriorityPolicy(ctx, cfg, name, result.AccountType, result.Priority, restorePriority, usage)
	if action != nil {
		result.LatestAction = &action.Message
		if action.Result == "priority_degraded" {
			result.Result = "priority_degraded"
			result.Priority = action.Priority
			result.RestorePriority = action.RestorePriority
		}
		if action.Result == "priority_restored" {
			result.Result = "priority_restored"
			result.Priority = action.Priority
			result.ClearRestorePriority = true
		}
		logFn(name + ": " + action.Message)
	} else {
		accountType := "unknown"
		if result.AccountType != nil && strings.TrimSpace(*result.AccountType) != "" {
			accountType = *result.AccountType
		}
		logFn(fmt.Sprintf("%s: 巡检正常，类型 %s", name, accountType))
	}
	if result.Priority == nil || *result.Priority != -1 {
		result.ClearRestorePriority = true
	}
	result.LastError = nil
	_ = a.upsertKeeperState(ctx, result)
	return result
}

type keeperPriorityPolicyAction struct {
	Message         string
	Result          string
	Priority        *int
	RestorePriority *int
}

func (a *App) applyKeeperPriorityPolicy(ctx context.Context, cfg AppConfig, name string, accountType *string, priority *int, restorePriority *int, usage keeperUsageInfo) *keeperPriorityPolicyAction {
	quotaReached := usage.PrimaryUsedPercent >= cfg.CodexKeeper.QuotaThreshold ||
		(usage.SecondaryUsedPercent != nil && *usage.SecondaryUsedPercent >= cfg.CodexKeeper.QuotaThreshold)
	currentPriority := keeperEffectivePriority(priority)
	next := keeperPriorityForType(accountType, cfg.CodexKeeperPriorityRule)
	if quotaReached {
		if currentPriority <= -1 {
			return nil
		}
		restoreTo := restorePriority
		if restoreTo == nil {
			restoreTo = next
		}
		if currentPriority > 20 {
			restoreTo = &currentPriority
		}
		if restoreTo == nil {
			restoreTo = &currentPriority
		}
		message := fmt.Sprintf("降为低优先级：额度使用率达到阈值 %d%%", cfg.CodexKeeper.QuotaThreshold)
		if cfg.CodexKeeper.DryRun {
			message = "模拟" + message
			low := -1
			return &keeperPriorityPolicyAction{Message: message, Result: "priority_degraded", Priority: &low, RestorePriority: restoreTo}
		}
		low := -1
		if err := a.setKeeperRemotePriority(ctx, cfg, name, &low); err != nil {
			message = "写入低优先级失败：" + err.Error()
			return &keeperPriorityPolicyAction{Message: message}
		}
		return &keeperPriorityPolicyAction{Message: message, Result: "priority_degraded", Priority: &low, RestorePriority: restoreTo}
	}
	if currentPriority == -1 {
		restoreTo := restorePriority
		if restoreTo == nil {
			restoreTo = next
		}
		if restoreTo == nil {
			zero := 0
			restoreTo = &zero
		}
		message := fmt.Sprintf("恢复优先级：priority %d", *restoreTo)
		if cfg.CodexKeeper.DryRun {
			message = "模拟" + message
			return &keeperPriorityPolicyAction{Message: message, Result: "priority_restored", Priority: restoreTo}
		}
		if err := a.setKeeperRemotePriority(ctx, cfg, name, restoreTo); err != nil {
			message = "恢复优先级失败：" + err.Error()
			return &keeperPriorityPolicyAction{Message: message}
		}
		return &keeperPriorityPolicyAction{Message: message, Result: "priority_restored", Priority: restoreTo}
	}
	if currentPriority < -1 || currentPriority > 20 {
		return nil
	}
	if next == nil {
		return nil
	}
	if currentPriority != *next {
		message := fmt.Sprintf("应用类型优先级：%s -> priority %d", valueOr(accountType, "unknown"), *next)
		if cfg.CodexKeeper.DryRun {
			message = "模拟" + message
			return &keeperPriorityPolicyAction{Message: message, Result: "priority_restored", Priority: next}
		}
		if err := a.setKeeperRemotePriority(ctx, cfg, name, next); err != nil {
			message = "写入类型优先级失败：" + err.Error()
			return &keeperPriorityPolicyAction{Message: message}
		}
		return &keeperPriorityPolicyAction{Message: message, Result: "priority_restored", Priority: next}
	}
	return nil
}

func keeperEffectivePriority(priority *int) int {
	if priority == nil {
		return 0
	}
	return *priority
}

func keeperDisplayPriority(priority *int) *int {
	if priority != nil {
		return priority
	}
	zero := 0
	return &zero
}

func (a *App) mergeKeeperStats(stats *keeperStats, result keeperAccountResult) {
	switch result.Result {
	case "healthy":
		stats.Healthy++
	case "status_disabled":
		stats.StatusDisabled++
	case "status_enabled":
		stats.StatusEnabled++
	case "priority_degraded":
		stats.PriorityDegraded++
	case "priority_restored":
		stats.PriorityRestored++
	case "network_error":
		stats.NetworkError++
	default:
		stats.Skipped++
	}
}

func (a *App) listKeeperRemoteAuthFiles(ctx context.Context, cfg AppConfig) ([]map[string]any, error) {
	_, payload, err := a.keeperRequest(ctx, cfg, http.MethodGet, "/v0/management/auth-files", nil, nil, time.Duration(cfg.CodexKeeper.CPATimeoutSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, validationError("读取 auth files 失败：响应不是有效 JSON")
	}
	return extractKeeperObjects(raw, []string{"files", "items", "data", "value"}), nil
}

func (a *App) getKeeperRemoteAuthFile(ctx context.Context, cfg AppConfig, name string) (map[string]any, error) {
	query := url.Values{"name": []string{name}}
	response, payload, err := a.keeperRequest(ctx, cfg, http.MethodGet, "/v0/management/auth-files/download", query, nil, time.Duration(cfg.CodexKeeper.CPATimeoutSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, validationError("读取 auth file 详情失败：响应不是有效 JSON")
	}
	return raw, nil
}

func (a *App) setKeeperRemoteDisabled(ctx context.Context, cfg AppConfig, name string, disabled bool) error {
	_, _, err := a.keeperRequest(ctx, cfg, http.MethodPatch, "/v0/management/auth-files/status", nil, map[string]any{"name": name, "disabled": disabled}, time.Duration(cfg.CodexKeeper.CPATimeoutSeconds)*time.Second)
	return err
}

func (a *App) setKeeperRemotePriority(ctx context.Context, cfg AppConfig, name string, priority *int) error {
	_, _, err := a.keeperRequest(ctx, cfg, http.MethodPatch, "/v0/management/auth-files/fields", nil, map[string]any{"name": name, "priority": priority}, time.Duration(cfg.CodexKeeper.CPATimeoutSeconds)*time.Second)
	return err
}

func (a *App) deleteKeeperRemoteAuthFile(ctx context.Context, cfg AppConfig, name string) error {
	query := url.Values{"name": []string{name}}
	_, _, err := a.keeperRequest(ctx, cfg, http.MethodDelete, "/v0/management/auth-files", query, nil, time.Duration(cfg.CodexKeeper.CPATimeoutSeconds)*time.Second)
	return err
}

func (a *App) keeperRequest(ctx context.Context, cfg AppConfig, method, path string, query url.Values, body any, timeout time.Duration) (*http.Response, []byte, error) {
	response, payload, err := doJSON(ctx, httpClient(timeout), method, makeURL(cfg.Collector.CLIProxyURL, path, query), managementHeaders(cfg.Collector.ManagementKey), body)
	if err != nil {
		return nil, nil, validationError("CLIProxyAPI 管理请求失败：" + err.Error())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, payload, validationError(fmt.Sprintf("CLIProxyAPI 管理请求失败：HTTP %d", response.StatusCode))
	}
	return response, payload, nil
}

func (a *App) checkKeeperUsage(ctx context.Context, cfg AppConfig, detail map[string]any) keeperHTTPResult {
	authIndex := keeperAuthIndex(detail)
	header := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0",
	}
	if accountID := keeperString(detail["account_id"]); accountID != "" {
		header["Chatgpt-Account-Id"] = accountID
	}
	body := map[string]any{
		"auth_index": authIndex,
		"method":     "GET",
		"url":        keeperUsageURL,
		"header":     header,
		"data":       "",
	}
	response, payload, err := a.keeperRequest(ctx, cfg, http.MethodPost, "/v0/management/api-call", nil, body, time.Duration(cfg.CodexKeeper.UsageTimeoutSeconds)*time.Second)
	if err != nil {
		return keeperHTTPResult{Error: err.Error()}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return keeperHTTPResult{Error: fmt.Sprintf("api-call 管理请求失败：HTTP %d", response.StatusCode), Brief: briefPayload(payload)}
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return keeperHTTPResult{Error: "api-call 响应不是有效 JSON"}
	}
	statusCode := keeperIntPtr(raw["status_code"], raw["statusCode"])
	if statusCode == nil {
		return keeperHTTPResult{Error: "api-call 响应缺少 status_code"}
	}
	bodyJSON := keeperBodyJSON(raw["body"])
	return keeperHTTPResult{
		StatusCode: statusCode,
		JSONData:   bodyJSON,
		Brief:      briefAny(raw["body"]),
	}
}

func (a *App) listKeeperAccounts(ctx context.Context) ([]keeperAccount, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT auth_name, email, account_type, disabled, priority, primary_used_percent,
		       secondary_used_percent, CAST(primary_reset_at AS TEXT), CAST(secondary_reset_at AS TEXT), quota_threshold,
		       last_status_code, last_error, latest_action, CAST(last_checked_at AS TEXT), CAST(last_healthy_at AS TEXT),
		       restore_priority, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM codex_keeper_auth_states
		ORDER BY COALESCE(email, ''), auth_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []keeperAccount{}
	for rows.Next() {
		state, err := scanKeeperState(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, state.keeperAccount)
	}
	return accounts, rows.Err()
}

func (a *App) pruneKeeperMissingAuthStates(ctx context.Context, remoteNames map[string]bool) (int, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT auth_name FROM codex_keeper_auth_states`)
	if err != nil {
		return 0, err
	}
	stale := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !remoteNames[name] {
			stale = append(stale, name)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM codex_keeper_auth_states WHERE auth_name = ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	pruned := 0
	for _, name := range stale {
		result, err := stmt.ExecContext(ctx, name)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		pruned += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pruned, nil
}

func (a *App) getKeeperState(ctx context.Context, name string) (*keeperAuthState, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT auth_name, email, account_type, disabled, priority, primary_used_percent,
		       secondary_used_percent, CAST(primary_reset_at AS TEXT), CAST(secondary_reset_at AS TEXT), quota_threshold,
		       last_status_code, last_error, latest_action, CAST(last_checked_at AS TEXT), CAST(last_healthy_at AS TEXT),
		       restore_priority, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM codex_keeper_auth_states WHERE auth_name = ?
	`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, notFoundError("账号状态不存在")
	}
	state, err := scanKeeperState(rows)
	if err != nil {
		return nil, err
	}
	return &state, rows.Err()
}

func scanKeeperState(scanner interface{ Scan(dest ...any) error }) (keeperAuthState, error) {
	var state keeperAuthState
	var email, accountType, primaryReset, secondaryReset, lastError, latestAction, lastChecked, lastHealthy, createdAt, updatedAt sql.NullString
	var priority, primaryUsed, secondaryUsed, quotaThreshold, lastStatus, restorePriority sql.NullInt64
	err := scanner.Scan(
		&state.Name, &email, &accountType, &state.Disabled, &priority, &primaryUsed,
		&secondaryUsed, &primaryReset, &secondaryReset, &quotaThreshold, &lastStatus,
		&lastError, &latestAction, &lastChecked, &lastHealthy, &restorePriority,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return keeperAuthState{}, err
	}
	state.Email = nullableString(email)
	state.AccountType = nullableString(accountType)
	state.Priority = nullableInt(priority)
	state.PrimaryUsedPercent = nullableInt(primaryUsed)
	state.SecondaryUsedPercent = nullableInt(secondaryUsed)
	state.PrimaryResetAt = timePtr(primaryReset)
	state.SecondaryResetAt = timePtr(secondaryReset)
	state.QuotaThreshold = nullableInt(quotaThreshold)
	state.LastStatusCode = nullableInt(lastStatus)
	state.LastError = nullableString(lastError)
	state.LatestAction = nullableString(latestAction)
	state.LastCheckedAt = timePtr(lastChecked)
	state.LastHealthyAt = timePtr(lastHealthy)
	state.RestorePriority = nullableInt(restorePriority)
	if parsed, ok := parseDBTime(createdAt.String); ok {
		state.CreatedAt = parsed
	}
	if parsed, ok := parseDBTime(updatedAt.String); ok {
		state.UpdatedAt = parsed
	}
	return state, nil
}

func (a *App) upsertKeeperState(ctx context.Context, result keeperAccountResult) error {
	now := dbTime(time.Now())
	checkedAt := dbTime(result.CheckedAt)
	var lastHealthy any
	if result.Result == "healthy" || result.Result == "priority_degraded" || result.Result == "priority_restored" {
		lastHealthy = checkedAt
	}
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO codex_keeper_auth_states (
			auth_name, email, account_type, disabled, priority, restore_priority, latest_action, last_error,
			last_status_code, primary_used_percent, secondary_used_percent, quota_threshold,
			primary_reset_at, secondary_reset_at, last_checked_at, last_healthy_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(auth_name) DO UPDATE SET
			email = excluded.email,
			account_type = excluded.account_type,
			disabled = excluded.disabled,
			priority = excluded.priority,
			restore_priority = CASE
				WHEN ? THEN NULL
				WHEN excluded.restore_priority IS NOT NULL THEN excluded.restore_priority
				ELSE codex_keeper_auth_states.restore_priority
			END,
			latest_action = excluded.latest_action,
			last_error = excluded.last_error,
			last_status_code = excluded.last_status_code,
			primary_used_percent = excluded.primary_used_percent,
			secondary_used_percent = excluded.secondary_used_percent,
			quota_threshold = excluded.quota_threshold,
			primary_reset_at = excluded.primary_reset_at,
			secondary_reset_at = excluded.secondary_reset_at,
			last_checked_at = excluded.last_checked_at,
			last_healthy_at = COALESCE(excluded.last_healthy_at, codex_keeper_auth_states.last_healthy_at),
			updated_at = excluded.updated_at
	`, result.Name, result.Email, result.AccountType, boolValue(result.Disabled), result.Priority, result.RestorePriority, result.LatestAction, result.LastError, result.LastStatusCode, result.PrimaryUsedPercent, result.SecondaryUsedPercent, result.QuotaThreshold, dbTimePtr(result.PrimaryResetAt), dbTimePtr(result.SecondaryResetAt), checkedAt, lastHealthy, now, now, result.ClearRestorePriority)
	return err
}

func (a *App) setKeeperAccountDisabled(ctx context.Context, authName string, disabled bool) error {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return err
	}
	state, err := a.getKeeperState(ctx, authName)
	if err != nil {
		return err
	}
	if err := a.setKeeperRemoteDisabled(ctx, cfg, authName, disabled); err != nil {
		return err
	}
	now := dbTime(time.Now())
	var checkedAt any = now
	var lastHealthy any
	if !disabled {
		lastHealthy = now
	}
	_, err = a.db.ExecContext(ctx, `
		UPDATE codex_keeper_auth_states
		SET disabled = ?, restore_priority = NULL, latest_action = NULL, last_error = NULL,
		    last_status_code = NULL, primary_used_percent = CASE WHEN ? THEN NULL ELSE primary_used_percent END,
		    secondary_used_percent = CASE WHEN ? THEN NULL ELSE secondary_used_percent END,
		    primary_reset_at = CASE WHEN ? THEN NULL ELSE primary_reset_at END,
		    secondary_reset_at = CASE WHEN ? THEN NULL ELSE secondary_reset_at END,
		    quota_threshold = CASE WHEN ? THEN NULL ELSE quota_threshold END,
		    last_checked_at = ?, last_healthy_at = COALESCE(?, last_healthy_at), updated_at = ?
		WHERE auth_name = ?
	`, disabled, disabled, disabled, disabled, disabled, disabled, checkedAt, lastHealthy, now, state.Name)
	return err
}

func (a *App) deleteKeeperAccount(ctx context.Context, authName string) error {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return err
	}
	state, err := a.getKeeperState(ctx, authName)
	if err != nil {
		return err
	}
	if !state.Disabled {
		return validationError("只能删除已禁用账号")
	}
	if err := a.deleteKeeperRemoteAuthFile(ctx, cfg, authName); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `DELETE FROM codex_keeper_auth_states WHERE auth_name = ?`, authName)
	return err
}

func (a *App) bulkDeleteKeeperAccounts(w http.ResponseWriter, r *http.Request) error {
	var payload keeperBulkDeleteRequest
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	names, err := normalizeKeeperAuthNames(payload.AuthNames)
	if err != nil {
		return err
	}
	deleted := []string{}
	failures := []map[string]string{}
	for _, name := range names {
		if err := a.deleteKeeperAccount(r.Context(), name); err != nil {
			failures = append(failures, map[string]string{"name": name, "message": err.Error()})
			continue
		}
		deleted = append(deleted, name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "deleted": deleted, "failed": failures})
	return nil
}

func (a *App) updateKeeperAccountPriority(ctx context.Context, authName string, priority int) error {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return err
	}
	state, err := a.getKeeperState(ctx, authName)
	if err != nil {
		return err
	}
	if err := validateKeeperPriority(priority, state.AccountType, cfg.CodexKeeperPriorityRule); err != nil {
		return err
	}
	if err := a.setKeeperRemotePriority(ctx, cfg, authName, &priority); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `
		UPDATE codex_keeper_auth_states
		SET priority = ?, restore_priority = NULL, latest_action = NULL, last_error = NULL, updated_at = ?
		WHERE auth_name = ?
	`, priority, dbTime(time.Now()), authName)
	return err
}

func (a *App) createKeeperRun(ctx context.Context, mode string) (int, error) {
	now := dbTime(time.Now())
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO codex_keeper_runs (mode, state, detail, started_at, created_at, updated_at)
		VALUES (?, 'running', '', ?, ?, ?)
	`, mode, now, now, now)
	if err != nil {
		return 0, err
	}
	id, _ := result.LastInsertId()
	return int(id), nil
}

func (a *App) finishKeeperRun(ctx context.Context, runID int, state, detail string, stats keeperStats) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE codex_keeper_runs
		SET state = ?, detail = ?, finished_at = ?, total = ?, healthy = ?, status_disabled = ?,
		    status_enabled = ?, priority_degraded = ?, priority_restored = ?, skipped = ?,
		    network_error = ?, updated_at = ?
		WHERE id = ?
	`, state, detail, dbTime(time.Now()), stats.Total, stats.Healthy, stats.StatusDisabled, stats.StatusEnabled, stats.PriorityDegraded, stats.PriorityRestored, stats.Skipped, stats.NetworkError, dbTime(time.Now()), runID)
	return err
}

type keeperRunRecord struct {
	Mode       *string
	State      string
	Detail     string
	StartedAt  *time.Time
	FinishedAt *time.Time
	Stats      keeperStats
}

func (a *App) latestKeeperRun(ctx context.Context) (*keeperRunRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT mode, state, detail, CAST(started_at AS TEXT), CAST(finished_at AS TEXT), total, healthy, status_disabled,
		       status_enabled, priority_degraded, priority_restored, skipped, network_error
		FROM codex_keeper_runs ORDER BY id DESC LIMIT 1
	`)
	return scanKeeperRunRecord(row)
}

func (a *App) latestKeeperRunByMode(ctx context.Context, mode string) (*keeperRunRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT mode, state, detail, CAST(started_at AS TEXT), CAST(finished_at AS TEXT), total, healthy, status_disabled,
		       status_enabled, priority_degraded, priority_restored, skipped, network_error
		FROM codex_keeper_runs WHERE mode = ? ORDER BY id DESC LIMIT 1
	`, mode)
	return scanKeeperRunRecord(row)
}

func scanKeeperRunRecord(row interface {
	Scan(dest ...any) error
}) (*keeperRunRecord, error) {
	var run keeperRunRecord
	var mode, startedAt, finishedAt sql.NullString
	err := row.Scan(&mode, &run.State, &run.Detail, &startedAt, &finishedAt, &run.Stats.Total, &run.Stats.Healthy, &run.Stats.StatusDisabled, &run.Stats.StatusEnabled, &run.Stats.PriorityDegraded, &run.Stats.PriorityRestored, &run.Stats.Skipped, &run.Stats.NetworkError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.Mode = nullableString(mode)
	run.StartedAt = timePtr(startedAt)
	run.FinishedAt = timePtr(finishedAt)
	return &run, nil
}

func (a *App) recordKeeperRunAccount(ctx context.Context, runID int, result keeperAccountResult) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO codex_keeper_run_accounts (
			run_id, auth_name, email, result, account_type, priority, disabled,
			keeper_action, primary_used_percent, secondary_used_percent, quota_threshold,
			last_status_code, last_error, latest_action, checked_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runID, result.Name, result.Email, result.Result, result.AccountType, result.Priority, result.Disabled, valueOr(result.LatestAction, "none"), result.PrimaryUsedPercent, result.SecondaryUsedPercent, result.QuotaThreshold, result.LastStatusCode, result.LastError, result.LatestAction, dbTime(result.CheckedAt), dbTime(time.Now()))
	return err
}

func extractKeeperObjects(payload any, keys []string) []map[string]any {
	if items, ok := payload.([]any); ok {
		return mapItems(items)
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return []map[string]any{}
	}
	for _, key := range keys {
		if items, ok := object[key].([]any); ok {
			return mapItems(items)
		}
	}
	return []map[string]any{}
}

func mapItems(items []any) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func mergeKeeperObjects(left, right map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

func parseKeeperUsageInfo(payload map[string]any) keeperUsageInfo {
	usage := keeperUsageInfo{PlanType: "unknown"}
	if payload == nil {
		return usage
	}
	if value := keeperString(payload["plan_type"]); value != "" {
		usage.PlanType = value
	}
	rateLimit, _ := payload["rate_limit"].(map[string]any)
	primary, _ := rateLimit["primary_window"].(map[string]any)
	secondary, _ := rateLimit["secondary_window"].(map[string]any)
	if value := keeperIntPtr(primary["used_percent"]); value != nil {
		usage.PrimaryUsedPercent = *value
	}
	usage.SecondaryUsedPercent = keeperIntPtr(secondary["used_percent"])
	usage.PrimaryResetAt = quotaResetAt(primary, time.Now().In(appTimeLocation))
	usage.SecondaryResetAt = quotaResetAt(secondary, time.Now().In(appTimeLocation))
	return usage
}

func quotaResetAt(window map[string]any, base time.Time) *time.Time {
	if window == nil {
		return nil
	}
	if ts := keeperIntPtr(window["reset_at"], window["resetAt"], window["reset_at_seconds"], window["resetAtSeconds"]); ts != nil {
		seconds := int64(*ts)
		if seconds > 10_000_000_000 {
			seconds /= 1000
		}
		parsed := time.Unix(seconds, 0).In(appTimeLocation)
		return &parsed
	}
	if after := keeperIntPtr(window["reset_after_seconds"], window["resetAfterSeconds"]); after != nil && *after >= 0 {
		parsed := base.Add(time.Duration(*after) * time.Second)
		return &parsed
	}
	return nil
}

func accountTypeFromKeeperDetail(detail map[string]any, usage *keeperUsageInfo) *string {
	values := []string{}
	if usage != nil {
		values = append(values, usage.PlanType)
	}
	for _, key := range []string{"plan_type", "plan", "tier", "account_plan", "subscription_plan", "sku", "account_type"} {
		if value := keeperString(detail[key]); value != "" {
			values = append(values, value)
		}
	}
	text := strings.ToLower(strings.Join(values, " "))
	text = strings.NewReplacer("-", "_", " ", "_").Replace(text)
	var result string
	switch {
	case strings.Contains(text, "20x") || strings.Contains(text, "pro_20"):
		result = "pro_20x"
	case strings.Contains(text, "5x") || strings.Contains(text, "pro_5"):
		result = "pro_5x"
	case strings.Contains(text, "team") || strings.Contains(text, "business"):
		result = "team"
	case strings.Contains(text, "plus"):
		result = "plus"
	case strings.Contains(text, "free"):
		result = "free"
	default:
		return nil
	}
	return &result
}

func keeperPriorityForType(accountType *string, rules map[string]int) *int {
	if accountType == nil {
		return nil
	}
	value, ok := normalizePriorityRules(rules)[strings.ToLower(strings.TrimSpace(*accountType))]
	if !ok {
		return nil
	}
	return &value
}

func validateKeeperPriority(priority int, accountType *string, rules map[string]int) error {
	if priority < -1 || priority > 20 {
		return nil
	}
	expected := keeperPriorityForType(accountType, rules)
	if expected != nil && *expected == priority {
		return nil
	}
	if accountType == nil || expected == nil {
		return validationError("该账号类型没有可设置的系统 priority")
	}
	return validationError(fmt.Sprintf("只能设置小于 -1、大于 20，或当前账号类型 %s 对应的 priority %d", *accountType, *expected))
}

func isBadKeeperCredential(result keeperHTTPResult) bool {
	if result.StatusCode != nil && (*result.StatusCode == 401 || *result.StatusCode == 402) {
		return true
	}
	text := strings.ToLower(result.Brief)
	if result.JSONData != nil {
		payload, _ := json.Marshal(result.JSONData)
		text += " " + strings.ToLower(string(payload))
	}
	return strings.Contains(text, "workspace") && (strings.Contains(text, "disabled") || strings.Contains(text, "deactivated"))
}

func (a *App) preserveKeeperBadCredentialDiagnosis(ctx context.Context, result *keeperAccountResult) {
	state, err := a.getKeeperState(ctx, result.Name)
	if err != nil || !isKeeperBadCredentialDisableAction(state.LatestAction) {
		return
	}
	result.LastStatusCode = state.LastStatusCode
	result.LastError = cloneStringPtr(state.LastError)
	result.LatestAction = cloneStringPtr(state.LatestAction)
}

func isKeeperBadCredentialDisableAction(action *string) bool {
	if action == nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(*action), "禁用凭证")
}

func keeperBodyJSON(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	var object map[string]any
	if json.Unmarshal([]byte(text), &object) != nil {
		return nil
	}
	return object
}

func keeperAuthIndex(detail map[string]any) string {
	for _, key := range []string{"auth_index", "authIndex", "index", "name"} {
		if value := keeperString(detail[key]); value != "" {
			return value
		}
	}
	return "unknown"
}

func keeperString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func keeperStringPtr(values ...any) *string {
	for _, value := range values {
		if text := keeperString(value); text != "" {
			return &text
		}
	}
	return nil
}

func keeperIntPtr(values ...any) *int {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			continue
		case int:
			return &typed
		case int64:
			converted := int(typed)
			return &converted
		case float64:
			converted := int(typed)
			return &converted
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return &parsed
			}
		}
	}
	return nil
}

func keeperBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "1" || normalized == "true" || normalized == "yes" || normalized == "on"
	case float64:
		return typed != 0
	default:
		return false
	}
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func briefPayload(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 160 {
		return text[:160] + "..."
	}
	return text
}

func briefAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if len(typed) > 160 {
			return typed[:160] + "..."
		}
		return typed
	default:
		payload, _ := json.Marshal(typed)
		return briefPayload(payload)
	}
}

func normalizeKeeperAuthNames(raw []string) ([]string, error) {
	result, err := normalizeOptionalKeeperAuthNames(raw)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, validationError("账号名称不能为空")
	}
	return result, nil
}

func normalizeOptionalKeeperAuthNames(raw []string) ([]string, error) {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			return nil, validationError("账号名称不能为空")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result, nil
}

func waitForStop(stop <-chan struct{}, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-stop:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		return true
	case <-timer.C:
		return false
	}
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sortKeeperAccounts(accounts []keeperAccount) {
	sort.Slice(accounts, func(i, j int) bool {
		left := valueOr(accounts[i].Email, "") + accounts[i].Name
		right := valueOr(accounts[j].Email, "") + accounts[j].Name
		return left < right
	})
}
