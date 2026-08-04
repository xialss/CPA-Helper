package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsPendingFactsRecoveryBatchSize = 500

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsPendingFactsRecovery, nil)
}

// upUsageAnalyticsPendingFactsRecovery requeues every record because the
// previous source-metadata migration could have removed a marker whose
// originating correction is no longer distinguishable. INSERT OR IGNORE keeps
// genuine pre-existing markers intact for the normal reconciler.
func upUsageAnalyticsPendingFactsRecovery(ctx context.Context, db *sql.DB) error {
	var lastID int64
	for {
		ids, err := enqueueUsageAnalyticsPendingFactsRecoveryBatch(ctx, db, lastID)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return invalidateUsageAnalyticsHourlyForPendingFactsRecovery(ctx, db)
		}
		lastID = ids[len(ids)-1]
	}
}

func enqueueUsageAnalyticsPendingFactsRecoveryBatch(ctx context.Context, db *sql.DB, lastID int64) ([]int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM usage_records
		WHERE id > ?
		ORDER BY id
		LIMIT ?
	`, lastID, usageAnalyticsPendingFactsRecoveryBatchSize)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, usageAnalyticsPendingFactsRecoveryBatchSize)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	statement, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id)
		VALUES (?)
	`)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := statement.ExecContext(ctx, id); err != nil {
			_ = statement.Close()
			return nil, err
		}
	}
	if err := statement.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func invalidateUsageAnalyticsHourlyForPendingFactsRecovery(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET hourly_needs_rebuild = 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`); err != nil {
		return err
	}
	return tx.Commit()
}
