package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() { goose.AddMigrationNoTxContext(upModelPriceVersionsHardening, nil) }

func upModelPriceVersionsHardening(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hasPriceID, err := modelPriceVersionsColumnExists(ctx, tx, "price_id")
	if err != nil {
		return err
	}
	if !hasPriceID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE model_price_versions ADD COLUMN price_id INTEGER`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_lookup_normalized ON model_price_versions(COALESCE(channel_auth_type, 'apikey'), channel_brand, channel_key, model, effective_at, id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_effective ON model_price_versions(effective_at ASC, id ASC)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_price_lifecycle ON model_price_versions(price_id, effective_at, id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE model_price_versions
		SET price_id = (
			SELECT p.id
			FROM model_prices p
			WHERE p.price_scope = 'channel'
			  AND COALESCE(p.channel_auth_type, 'apikey') = COALESCE(model_price_versions.channel_auth_type, 'apikey')
			  AND p.channel_brand = model_price_versions.channel_brand
			  AND p.channel_key = model_price_versions.channel_key
			  AND p.model = model_price_versions.model
		)
		WHERE price_id IS NULL
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO model_price_versions (
			price_id, provider, model, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
			request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			effective_at, baseline
		)
		SELECT p.id, p.provider, p.model, p.channel_auth_type, p.channel_brand, p.channel_key,
			p.input_usd_per_million, p.output_usd_per_million, p.cache_read_usd_per_million, p.cache_creation_usd_per_million,
			p.request_usd, p.billing_unit, p.priority_multiplier, p.long_context_threshold_tokens,
			p.long_context_input_usd_per_million, p.long_context_output_usd_per_million,
			p.long_context_cache_read_usd_per_million, p.long_context_cache_creation_usd_per_million,
			p.updated_at, 1
		FROM model_prices p
		WHERE p.price_scope = 'channel'
		  AND NOT EXISTS (
			SELECT 1 FROM model_price_versions v WHERE v.price_id = p.id
		  )
	`); err != nil {
		return err
	}
	return tx.Commit()
}

func modelPriceVersionsColumnExists(ctx context.Context, tx *sql.Tx, wanted string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(model_price_versions)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&id, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == wanted {
			return true, nil
		}
	}
	return false, rows.Err()
}
