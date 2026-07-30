package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelMonitorProxySettings, nil)
}

func upModelMonitorProxySettings(ctx context.Context, db *sql.DB) (err error) {
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
	if !cols["model_monitor_proxy_enabled"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE app_settings ADD COLUMN model_monitor_proxy_enabled BOOLEAN NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !cols["model_monitor_proxy_url"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE app_settings ADD COLUMN model_monitor_proxy_url VARCHAR(1000) NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
