package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUltrafastMultiplier, downUltrafastMultiplier)
}

func upUltrafastMultiplier(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN ultrafast_multiplier REAL"); err != nil {
			return fmt.Errorf("add %s.ultrafast_multiplier: %w", table, err)
		}
	}
	return tx.Commit()
}

func downUltrafastMultiplier(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
		var configured bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE ultrafast_multiplier IS NOT NULL)").Scan(&configured); err != nil {
			return fmt.Errorf("check %s.ultrafast_multiplier before downgrade: %w", table, err)
		}
		if configured {
			return fmt.Errorf("cannot downgrade: %s contains ultrafast multiplier configuration; restore a consistent backup instead", table)
		}
	}
	for _, table := range []string{"model_prices", "model_price_versions", "model_price_library_conflicts"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN ultrafast_multiplier"); err != nil {
			return fmt.Errorf("drop %s.ultrafast_multiplier: %w", table, err)
		}
	}
	return tx.Commit()
}
