package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpModelPriceXAIChannelPreservesRowsAndAllowsNativeXAI(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE model_prices (
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
			billing_unit VARCHAR(20),
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
		);
		CREATE UNIQUE INDEX uq_model_prices_library_provider_model
		 ON model_prices(provider COLLATE NOCASE, model COLLATE NOCASE)
		 WHERE price_scope = 'library';
		CREATE UNIQUE INDEX uq_model_prices_openai_channel_model
		 ON model_prices(COALESCE(channel_auth_type, 'apikey'), channel_key COLLATE NOCASE, model COLLATE NOCASE)
		 WHERE price_scope = 'channel' AND channel_brand = 'openai_compatibility';
		CREATE UNIQUE INDEX uq_model_prices_native_channel_model
		 ON model_prices(COALESCE(channel_auth_type, 'apikey'), channel_brand, channel_key, model COLLATE NOCASE)
		 WHERE price_scope = 'channel' AND channel_brand IN ('gemini', 'codex', 'claude', 'vertex');
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at, billing_unit
		) VALUES
			(10, 'openai', 'gpt-test', 'library', NULL, NULL, NULL,
			 1.25, 2.5, 0.125, 0.25, NULL,
			 NULL, NULL, NULL, NULL, NULL, NULL,
			 'manual', NULL, 0, NULL, '2026-08-09T10:00:00Z', 'token'),
			(11, 'custom', 'custom-test', 'channel', 'apikey', 'openai_compatibility', 'Custom Name',
			 3.25, 4.5, 0.325, 0.45, 0.01,
			 1.5, 200000, 5.5, 6.5, 0.55, 0.65,
			 'manual', 'source-test', 1, '2026-08-09T09:00:00Z', '2026-08-09T11:00:00Z', 'request'),
			(12, 'codex', 'gpt-oauth', 'channel', 'oauth', 'codex', 'oauth_pool',
			 7.25, 8.5, 0.725, 0.85, NULL,
			 2.0, NULL, NULL, NULL, NULL, NULL,
			 'manual', NULL, 0, NULL, '2026-08-09T12:00:00Z', 'token');
		CREATE TABLE usage_analytics_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			facts_version INTEGER NOT NULL DEFAULT 0,
			pricing_version INTEGER NOT NULL DEFAULT 0,
			selector_generation INTEGER NOT NULL DEFAULT 0,
			selector_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
			last_hourly_rebuild_at DATETIME,
			last_maintenance_at DATETIME,
			last_pruned_records INTEGER NOT NULL DEFAULT 0,
			last_maintenance_error TEXT,
			updated_at DATETIME NOT NULL
		);
		INSERT INTO usage_analytics_state (
			id, facts_version, pricing_version, selector_generation, selector_fingerprint, updated_at
		) VALUES (1, 7, 41, 13, 'selector-before', '2026-08-09T08:00:00Z');
		CREATE TRIGGER usage_analytics_model_prices_insert
		AFTER INSERT ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END;
		CREATE TRIGGER usage_analytics_model_prices_update
		AFTER UPDATE ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END;
		CREATE TRIGGER usage_analytics_model_prices_delete
		AFTER DELETE ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END;
	`); err != nil {
		t.Fatalf("seed previous schema: %v", err)
	}

	before := modelPriceRowsForMigrationTest(t, db)
	if err := upModelPriceXAIChannel(context.Background(), db); err != nil {
		t.Fatalf("upModelPriceXAIChannel failed: %v", err)
	}
	after := modelPriceRowsForMigrationTest(t, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rows changed during migration\nbefore: %#v\nafter:  %#v", before, after)
	}
	assertModelPriceUsageAnalyticsTriggers(t, db)
	assertUsageAnalyticsPricingState(t, db, 41)

	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, source, updated_at, billing_unit
		) VALUES ('xai', 'grok-test', 'channel', 'apikey', 'xai', 'auth-xai-1', 1, 'manual', '2026-08-09T13:00:00Z', 'token')
	`); err != nil {
		t.Fatalf("insert xAI API-key channel price: %v", err)
	}
	assertUsageAnalyticsPricingState(t, db, 42)
	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, source, updated_at, billing_unit
		) VALUES ('xai', 'GROK-TEST', 'channel', 'apikey', 'xai', 'auth-xai-1', 2, 'manual', '2026-08-09T14:00:00Z', 'token')
	`); err == nil {
		t.Fatal("duplicate xAI selector/model price should be rejected")
	}
	assertUsageAnalyticsPricingState(t, db, 42)
	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, source, updated_at, billing_unit
		) VALUES ('xai', 'grok-oauth', 'channel', 'oauth', 'xai', 'oauth_pool', 1, 'manual', '2026-08-09T15:00:00Z', 'token')
	`); err == nil {
		t.Fatal("xAI OAuth price should remain unsupported")
	}
	assertUsageAnalyticsPricingState(t, db, 42)
	if _, err := db.Exec(`
		UPDATE model_prices
		SET input_usd_per_million = 2, updated_at = '2026-08-09T16:00:00Z'
		WHERE price_scope = 'channel'
		  AND channel_brand = 'xai'
		  AND channel_key = 'auth-xai-1'
		  AND model = 'grok-test'
	`); err != nil {
		t.Fatalf("update xAI API-key channel price: %v", err)
	}
	assertUsageAnalyticsPricingState(t, db, 43)
	if _, err := db.Exec(`
		DELETE FROM model_prices
		WHERE price_scope = 'channel'
		  AND channel_brand = 'xai'
		  AND channel_key = 'auth-xai-1'
		  AND model = 'grok-test'
	`); err != nil {
		t.Fatalf("delete xAI API-key channel price: %v", err)
	}
	assertUsageAnalyticsPricingState(t, db, 44)
}

func assertModelPriceUsageAnalyticsTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'trigger'
		  AND tbl_name = 'model_prices'
		  AND name IN (
			'usage_analytics_model_prices_insert',
			'usage_analytics_model_prices_update',
			'usage_analytics_model_prices_delete'
		  )
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("model_prices usage analytics triggers = %d, want 3", count)
	}
}

func assertUsageAnalyticsPricingState(t *testing.T, db *sql.DB, wantPricingVersion int) {
	t.Helper()
	var factsVersion int
	var pricingVersion int
	var selectorGeneration int
	var selectorFingerprint string
	if err := db.QueryRow(`
		SELECT facts_version, pricing_version, selector_generation, selector_fingerprint
		FROM usage_analytics_state
		WHERE id = 1
	`).Scan(&factsVersion, &pricingVersion, &selectorGeneration, &selectorFingerprint); err != nil {
		t.Fatal(err)
	}
	if factsVersion != 7 || pricingVersion != wantPricingVersion || selectorGeneration != 13 || selectorFingerprint != "selector-before" {
		t.Fatalf(
			"usage analytics state = facts %d pricing %d selector %d fingerprint %q, want 7/%d/13/selector-before",
			factsVersion, pricingVersion, selectorGeneration, selectorFingerprint, wantPricingVersion,
		)
	}
}

func modelPriceRowsForMigrationTest(t *testing.T, db *sql.DB) [][]any {
	t.Helper()
	rows, err := db.Query(`
		SELECT id, provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, last_synced_at, updated_at, billing_unit
		FROM model_prices ORDER BY id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	result := [][]any{}
	for rows.Next() {
		values := make([]any, 24)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		for index, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[index] = string(bytes)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("migration test returned no rows")
	}
	return result
}
