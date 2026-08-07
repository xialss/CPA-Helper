package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelPriceBillingUnit, nil)
}

func upModelPriceBillingUnit(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, table := range []string{"model_prices", "model_price_library_conflicts"} {
		columns, err := tableColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if !columns["billing_unit"] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN billing_unit VARCHAR(20)`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE `+table+`
			SET billing_unit = CASE
				WHEN instr(lower(model), 'image') > 0 THEN 'request'
				ELSE 'token'
			END
			WHERE billing_unit IS NULL OR trim(billing_unit) = ''
		`); err != nil {
			return err
		}
	}

	return tx.Commit()
}
