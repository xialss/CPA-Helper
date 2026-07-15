package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelPriceChannelAuthType, nil)
}

func upModelPriceChannelAuthType(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err := dropTableIfExists(ctx, tx, "__goose_model_prices"); err != nil {
		return err
	}
	if err := execStatements(ctx, tx, `
		CREATE TABLE "__goose_model_prices" (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider VARCHAR(120) NOT NULL,
			model VARCHAR(180) NOT NULL,
			price_scope VARCHAR(20) NOT NULL DEFAULT 'library',
			channel_auth_type VARCHAR(20),
			channel_brand VARCHAR(40),
			channel_key VARCHAR(500),
			input_usd_per_million REAL NOT NULL DEFAULT 0,
			output_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_read_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_creation_usd_per_million REAL NOT NULL DEFAULT 0,
			request_usd REAL,
			priority_multiplier REAL,
			long_context_threshold_tokens INTEGER,
			long_context_input_usd_per_million REAL,
			long_context_output_usd_per_million REAL,
			long_context_cache_read_usd_per_million REAL,
			long_context_cache_creation_usd_per_million REAL,
			source VARCHAR(40) NOT NULL DEFAULT 'manual',
			source_model VARCHAR(180),
			auto_synced BOOLEAN NOT NULL DEFAULT 0,
			last_synced_at DATETIME,
			updated_at DATETIME NOT NULL,
			CONSTRAINT ck_model_prices_scope CHECK (
				(price_scope = 'library' AND channel_auth_type IS NULL AND channel_brand IS NULL AND channel_key IS NULL)
				OR
				(price_scope = 'channel'
					AND (channel_auth_type IS NULL OR channel_auth_type IN ('apikey', 'oauth'))
					AND channel_brand IN ('gemini', 'codex', 'claude', 'openai_compatibility', 'vertex')
					AND (channel_auth_type <> 'oauth' OR channel_brand <> 'openai_compatibility')
					AND channel_key IS NOT NULL
					AND length(trim(channel_key)) > 0)
			),
			CONSTRAINT ck_model_prices_litellm_scope CHECK (source <> 'litellm' OR price_scope = 'library')
		)
	`, `
		INSERT INTO "__goose_model_prices" (
			id, provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		)
		SELECT id, provider, model, price_scope,
		       CASE WHEN price_scope = 'channel' THEN 'apikey' ELSE NULL END,
		       channel_brand, channel_key,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, last_synced_at, updated_at
		FROM model_prices
	`); err != nil {
		return err
	}
	if err := dropTableIfExists(ctx, tx, "model_prices"); err != nil {
		return err
	}
	if err := renameTable(ctx, tx, "__goose_model_prices", "model_prices"); err != nil {
		return err
	}
	if err := execStatements(ctx, tx,
		`CREATE UNIQUE INDEX uq_model_prices_library_provider_model
		 ON model_prices(provider COLLATE NOCASE, model COLLATE NOCASE)
		 WHERE price_scope = 'library'`,
		`CREATE UNIQUE INDEX uq_model_prices_openai_channel_model
		 ON model_prices(COALESCE(channel_auth_type, 'apikey'), channel_key COLLATE NOCASE, model COLLATE NOCASE)
		 WHERE price_scope = 'channel' AND channel_brand = 'openai_compatibility'`,
		`CREATE UNIQUE INDEX uq_model_prices_native_channel_model
		 ON model_prices(COALESCE(channel_auth_type, 'apikey'), channel_brand, channel_key, model COLLATE NOCASE)
		 WHERE price_scope = 'channel' AND channel_brand IN ('gemini', 'codex', 'claude', 'vertex')`,
	); err != nil {
		return err
	}
	return tx.Commit()
}
