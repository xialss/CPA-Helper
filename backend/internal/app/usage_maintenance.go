package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	usageRawRetentionWindow  = 7 * 24 * time.Hour
	usageRawPruneBatchSize   = 500
	usageMaintenanceInterval = 24 * time.Hour
)

type usageMaintenanceRunner struct {
	app      *App
	mu       sync.Mutex
	stop     chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc
	interval time.Duration
}

type UsageDatabaseCompactionReport struct {
	DatabasePath string `json:"database_path"`
	BackupPath   string `json:"backup_path"`
	BeforeBytes  int64  `json:"before_bytes"`
	AfterBytes   int64  `json:"after_bytes"`
	BackupBytes  int64  `json:"backup_bytes"`
}

func newUsageMaintenanceRunner(app *App) *usageMaintenanceRunner {
	return &usageMaintenanceRunner{
		app:      app,
		interval: usageMaintenanceInterval,
	}
}

func (runner *usageMaintenanceRunner) Start() {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.done != nil {
		select {
		case <-runner.done:
		default:
			return
		}
	}
	runner.stop = make(chan struct{})
	runner.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	runner.cancel = cancel
	go runner.loop(ctx, runner.stop, runner.done, runner.interval)
}

func (runner *usageMaintenanceRunner) loop(ctx context.Context, stop <-chan struct{}, done chan<- struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := runner.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				// The failure is persisted by RunOnce and is intentionally visible in logs.
				log.Printf("usage maintenance failed: %v", err)
			}
		}
	}
}

func (runner *usageMaintenanceRunner) Stop() {
	if runner == nil {
		return
	}
	runner.mu.Lock()
	stop := runner.stop
	done := runner.done
	cancel := runner.cancel
	if stop == nil || done == nil {
		runner.mu.Unlock()
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	if cancel != nil {
		cancel()
	}
	runner.mu.Unlock()
	<-done
}

func (runner *usageMaintenanceRunner) RunOnce(ctx context.Context) error {
	if runner == nil || runner.app == nil {
		return errors.New("usage maintenance runner is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runner.app.ensureUsageAnalyticsFacts(ctx); err != nil {
		return runner.recordFailure(ctx, err)
	}
	if err := runner.app.requireUsageAnalyticsFacts(ctx); err != nil {
		return runner.recordFailure(ctx, err)
	}
	pruned, err := runner.app.pruneExpiredUsageRawJSON(ctx, time.Now().In(appTimeLocation))
	if err != nil {
		return runner.recordFailure(ctx, err)
	}
	if err := runner.reconcileFactsAfterRawPrune(ctx, pruned); err != nil {
		return runner.recordFailure(ctx, err)
	}
	pricing, err := runner.app.billingPriceIndex(ctx)
	if err != nil {
		return runner.recordFailure(ctx, err)
	}
	if err := runner.app.ensureUsageAnalyticsHourlyForMaintenance(ctx, pricing); err != nil {
		return runner.recordFailure(ctx, err)
	}
	if _, err := runner.app.db.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET last_maintenance_at = ?, last_pruned_records = ?, last_maintenance_error = NULL, updated_at = ?
		WHERE id = 1
	`, dbTime(time.Now()), pruned, dbTime(time.Now())); err != nil {
		return err
	}
	if err := runner.reconcileQuotaSyncErrors(ctx); err != nil {
		return runner.recordFailure(ctx, err)
	}
	return nil
}

func (runner *usageMaintenanceRunner) reconcileQuotaSyncErrors(ctx context.Context) error {
	currentMonth := quotaMonth(time.Now())
	rows, err := runner.app.db.QueryContext(ctx, `
		SELECT id
		FROM users
		WHERE disabled_at IS NULL
		  AND (
			quota_sync_error IS NOT NULL
			OR (
				quota_paused_at IS NOT NULL
				AND quota_pause_reason = 'quota_exhausted'
				AND (quota_month <> ? OR quota_month IS NULL)
			)
		  )
	`, currentMonth)
	if err != nil {
		return err
	}
	defer rows.Close()
	var userIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		userIDs = append(userIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range userIDs {
		func() {
			unlock := runner.app.lockUserMutation(id)
			defer unlock()
			user, err := runner.app.getUser(ctx, id)
			if err != nil {
				log.Printf("quota maintenance: load user %d: %v", id, err)
				return
			}
			if user.DisabledAt != nil {
				return
			}
			user, err = runner.app.ensureQuotaMonth(ctx, user)
			if err != nil {
				log.Printf("quota maintenance: ensure quota month for user %d: %v", id, err)
				return
			}
			if quotaHasAvailable(user) {
				if syncErr := runner.app.restoreQuotaPausedUserIfAvailableLocked(ctx, id); syncErr != nil {
					log.Printf("quota maintenance: restore user %d: %v", id, syncErr)
				}
			} else if syncErr := runner.app.pauseUserKeysForQuotaLocked(ctx, id, quotaPauseReasonExhausted); syncErr != nil {
				log.Printf("quota maintenance: pause user %d: %v", id, syncErr)
			}
		}()
	}
	return nil
}

func (runner *usageMaintenanceRunner) reconcileFactsAfterRawPrune(ctx context.Context, pruned int64) error {
	pending, err := runner.app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		return err
	}
	if !pending && pruned == 0 {
		return nil
	}
	if pending {
		if err := runner.app.ensureUsageAnalyticsFacts(ctx); err != nil {
			return err
		}
	}
	return runner.app.requireUsageAnalyticsFacts(ctx)
}

func (runner *usageMaintenanceRunner) recordFailure(ctx context.Context, runErr error) error {
	if err := ctx.Err(); err != nil {
		// Shutdown cancellation remains visible to the caller without becoming a
		// persisted maintenance failure.
		return err
	}
	message := strings.TrimSpace(runErr.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	if _, err := runner.app.db.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET last_maintenance_error = ?, updated_at = ?
		WHERE id = 1
	`, message, dbTime(time.Now())); err != nil {
		return errors.Join(runErr, fmt.Errorf("record usage maintenance failure: %w", err))
	}
	return runErr
}

func usageRawRetentionCutoff(now time.Time) time.Time {
	// API timestamps are serialized at whole-second precision. Normalize the
	// rolling cutoff to that same precision so an exact 7×24h client range is
	// not rejected solely because the server clock has nanoseconds.
	return now.In(appTimeLocation).Add(-usageRawRetentionWindow).Truncate(time.Second)
}

func validateUsageRecordRetention(r *http.Request, filters UsageFilters, now time.Time) error {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("range")), "all") {
		return validationError("请求明细仅保留最近 7 天，不能使用 range=all")
	}
	if filters.Start == nil || filters.End == nil {
		return validationError("请求明细必须提供有效的时间范围")
	}
	if !filters.Start.Before(*filters.End) {
		return validationError("请求明细时间范围无效")
	}
	if filters.Start.Before(usageRawRetentionCutoff(now)) {
		return validationError("请求明细仅保留最近 7 天")
	}
	return nil
}

func usageRecordIsExpired(record UsageRecord, now time.Time) bool {
	return record.Timestamp.Before(usageRawRetentionCutoff(now)) || strings.TrimSpace(record.RawJSON) == ""
}

func (a *App) pruneExpiredUsageRawJSON(ctx context.Context, now time.Time) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cutoff := usageRawRetentionCutoff(now)
	if _, err := a.db.ExecContext(ctx, `DELETE FROM usage_model_audits AS audits WHERE EXISTS (
		SELECT 1 FROM usage_records AS records
		WHERE records.id = audits.usage_record_id
		  AND (records.timestamp < ? OR trim(records.raw_json) = '')
	)`, dbTime(cutoff)); err != nil {
		return 0, err
	}
	var pruned int64
	for {
		// Pending rows can have source/auth metadata only in raw_json. Keep the
		// queue check in the UPDATE selection so a direct import that lands after
		// maintenance's fact preflight cannot lose that evidence before repair.
		result, err := a.db.ExecContext(ctx, `
			UPDATE usage_records
			SET raw_json = ''
			WHERE id IN (
				SELECT records.id
				FROM usage_records AS records
				WHERE records.timestamp < ?
					AND trim(records.raw_json) <> ''
					AND NOT EXISTS (
						SELECT 1
						FROM usage_analytics_pending_facts AS pending
						WHERE pending.usage_record_id = records.id
					)
				ORDER BY records.id
				LIMIT ?
			)
		`, dbTime(cutoff), usageRawPruneBatchSize)
		if err != nil {
			return pruned, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return pruned, err
		}
		pruned += affected
		if affected == 0 {
			return pruned, nil
		}
	}
}

func CompactUsageDatabase(ctx context.Context, backupPath string, writersPaused bool) (UsageDatabaseCompactionReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !writersPaused {
		return UsageDatabaseCompactionReport{}, errors.New("compact-usage-database requires --confirm-writers-paused")
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return UsageDatabaseCompactionReport{}, err
	}
	absBackupPath, err := prepareUsageTokenRepairBackupPath(paths.DBPath, backupPath)
	if err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("prepare usage database backup: %w", err)
	}
	db, err := openRuntimeDB(paths, false)
	if err != nil {
		return UsageDatabaseCompactionReport{}, err
	}
	defer db.Close()
	if _, err := checkDatabaseReady(ctx, db, paths.DBPath); err != nil {
		return UsageDatabaseCompactionReport{}, err
	}
	analytics := &App{db: db}
	if err := analytics.requireUsageAnalyticsFacts(ctx); err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("compact-usage-database requires reconciled analytics facts: %w", err)
	}
	before, err := os.Stat(paths.DBPath)
	if err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("inspect usage database: %w", err)
	}
	if err := createAndVerifyUsageDatabaseBackup(ctx, db, absBackupPath); err != nil {
		return UsageDatabaseCompactionReport{}, err
	}
	backup, err := os.Stat(absBackupPath)
	if err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("inspect usage database backup: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("compact usage database: %w", err)
	}
	if err := verifyUsageDatabaseIntegrity(ctx, db); err != nil {
		return UsageDatabaseCompactionReport{}, err
	}
	after, err := os.Stat(paths.DBPath)
	if err != nil {
		return UsageDatabaseCompactionReport{}, fmt.Errorf("inspect compacted usage database: %w", err)
	}
	return UsageDatabaseCompactionReport{
		DatabasePath: paths.DBPath,
		BackupPath:   absBackupPath,
		BeforeBytes:  before.Size(),
		AfterBytes:   after.Size(),
		BackupBytes:  backup.Size(),
	}, nil
}

func createAndVerifyUsageDatabaseBackup(ctx context.Context, db *sql.DB, backupPath string) (err error) {
	backupFile, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("reserve usage database backup destination: %w", err)
	}
	verified := false
	defer func() {
		if verified {
			return
		}
		if removeErr := os.Remove(backupPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove incomplete usage database backup: %w", removeErr))
		}
	}()
	if err := backupFile.Close(); err != nil {
		return fmt.Errorf("reserve usage database backup destination: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		return fmt.Errorf("create usage database backup: %w", err)
	}
	backupDB, err := sql.Open("sqlite", sqliteDSN(backupPath, true))
	if err != nil {
		return fmt.Errorf("open usage database backup: %w", err)
	}
	defer backupDB.Close()
	backupDB.SetMaxOpenConns(1)
	if err := verifyUsageDatabaseIntegrity(ctx, backupDB); err != nil {
		return fmt.Errorf("verify usage database backup: %w", err)
	}
	if _, err := checkDatabaseReady(ctx, backupDB, backupPath); err != nil {
		return fmt.Errorf("verify usage database backup schema: %w", err)
	}
	verified = true
	return nil
}

func verifyUsageDatabaseIntegrity(ctx context.Context, db *sql.DB) error {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity_check returned %q", integrity)
	}
	return nil
}
