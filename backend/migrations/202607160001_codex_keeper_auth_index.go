package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upCodexKeeperAuthIndex, nil)
}

func upCodexKeeperAuthIndex(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	columns, err := tableColumns(ctx, tx, "codex_keeper_auth_states")
	if err != nil {
		return err
	}
	if !columns["auth_index"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE codex_keeper_auth_states ADD COLUMN auth_index VARCHAR(500)`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS ix_codex_keeper_auth_states_auth_index
			ON codex_keeper_auth_states(auth_index)
	`); err != nil {
		return err
	}
	return tx.Commit()
}
