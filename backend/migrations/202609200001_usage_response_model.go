package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() { goose.AddMigrationNoTxContext(upUsageResponseModel, nil) }

func upUsageResponseModel(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	columns, err := tableColumns(ctx, tx, "usage_records")
	if err != nil {
		return err
	}
	for _, name := range []string{"response_model", "request_alias", "collector_origin"} {
		if !columns[name] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_records ADD COLUMN `+name+` TEXT`); err != nil {
				return err
			}
		}
	}
	// Read only exact queue fields; nested request echoes are not response evidence.
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_records
		SET response_model = COALESCE(response_model, CASE WHEN json_type(raw_json, '$.response_model') = 'text' THEN NULLIF(trim(json_extract(raw_json, '$.response_model')), '') END),
		    request_alias = COALESCE(request_alias, CASE WHEN json_type(raw_json, '$.alias') = 'text' THEN NULLIF(trim(json_extract(raw_json, '$.alias')), '') END)
		WHERE (response_model IS NULL OR request_alias IS NULL)
		  AND trim(raw_json) <> '' AND json_valid(raw_json)
		  AND (json_type(raw_json, '$.response_model') = 'text' OR json_type(raw_json, '$.alias') = 'text')
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS usage_model_audits (
		usage_record_id INTEGER PRIMARY KEY REFERENCES usage_records(id) ON DELETE CASCADE,
		collector_origin TEXT NOT NULL,
		result_json TEXT NOT NULL
	)`); err != nil {
		return err
	}
	return tx.Commit()
}
