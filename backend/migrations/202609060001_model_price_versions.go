package migrations

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
)

func init() { goose.AddMigrationNoTxContext(upModelPriceVersions, downModelPriceVersions) }

func upModelPriceVersions(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS model_price_versions (
   id INTEGER PRIMARY KEY AUTOINCREMENT,
   price_id INTEGER,
   provider TEXT NOT NULL, model TEXT NOT NULL,
   channel_auth_type TEXT, channel_brand TEXT NOT NULL, channel_key TEXT NOT NULL,
   input_usd_per_million REAL NOT NULL, output_usd_per_million REAL NOT NULL,
   cache_read_usd_per_million REAL NOT NULL, cache_creation_usd_per_million REAL NOT NULL,
   request_usd REAL, billing_unit TEXT NOT NULL DEFAULT 'token', priority_multiplier REAL,
   long_context_threshold_tokens INTEGER, long_context_input_usd_per_million REAL,
   long_context_output_usd_per_million REAL, long_context_cache_read_usd_per_million REAL,
   long_context_cache_creation_usd_per_million REAL,
   effective_at DATETIME NOT NULL, baseline INTEGER NOT NULL DEFAULT 0,
   created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	   )`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_lookup ON model_price_versions(channel_auth_type, channel_brand, channel_key, model, effective_at, id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_lookup_normalized ON model_price_versions(COALESCE(channel_auth_type, 'apikey'), channel_brand, channel_key, model, effective_at, id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_model_price_versions_effective ON model_price_versions(effective_at ASC, id ASC)`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_price_versions (price_id, provider, model, channel_auth_type, channel_brand, channel_key,
   input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
   request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens, long_context_input_usd_per_million,
   long_context_output_usd_per_million, long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
   effective_at, baseline)
   SELECT id, provider, model, channel_auth_type, channel_brand, channel_key,
   input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
   request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens, long_context_input_usd_per_million,
   long_context_output_usd_per_million, long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
   updated_at, 1 FROM model_prices WHERE price_scope='channel'
   AND NOT EXISTS (SELECT 1 FROM model_price_versions v WHERE COALESCE(v.channel_auth_type, 'apikey') = COALESCE(model_prices.channel_auth_type, 'apikey') AND v.channel_brand=model_prices.channel_brand AND v.channel_key=model_prices.channel_key AND v.model=model_prices.model)`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func downModelPriceVersions(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS model_price_versions`)
	return err
}
