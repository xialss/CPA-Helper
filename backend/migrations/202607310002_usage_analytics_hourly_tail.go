package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsHourlyTail, nil)
}

func upUsageAnalyticsHourlyTail(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	columns, err := tableColumns(ctx, tx, "usage_analytics_state")
	if err != nil {
		return err
	}
	if !columns["hourly_facts_version"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_analytics_state ADD COLUMN hourly_facts_version INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !columns["hourly_max_record_id"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_analytics_state ADD COLUMN hourly_max_record_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !columns["hourly_needs_rebuild"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_analytics_state ADD COLUMN hourly_needs_rebuild BOOLEAN NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET hourly_needs_rebuild = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS usage_analytics_pending_facts_delete`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TRIGGER usage_analytics_pending_facts_delete
		AFTER DELETE ON usage_records
		BEGIN
			DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = OLD.id;
			UPDATE usage_analytics_state
			SET facts_version = facts_version + 1,
				hourly_needs_rebuild = 1,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END
	`); err != nil {
		return err
	}
	return tx.Commit()
}
