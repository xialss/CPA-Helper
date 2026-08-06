package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelMonitorSourceSettings, nil)
}

func upModelMonitorSourceSettings(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	cols, err := tableColumns(ctx, tx, "app_settings")
	if err != nil {
		return err
	}
	if !cols["model_monitor_enabled_source_ids"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE app_settings ADD COLUMN model_monitor_enabled_source_ids TEXT NOT NULL DEFAULT '["ai-input-im","openai","anthropic","deepseek"]'`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
