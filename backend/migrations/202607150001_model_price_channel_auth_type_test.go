package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpModelPriceChannelAuthTypePreservesRowsAndSeparatesOAuthPool(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE model_prices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL, model TEXT NOT NULL, price_scope TEXT NOT NULL,
			channel_brand TEXT, channel_key TEXT,
			input_usd_per_million REAL NOT NULL DEFAULT 0,
			output_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_read_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_creation_usd_per_million REAL NOT NULL DEFAULT 0,
			request_usd REAL, priority_multiplier REAL,
			long_context_threshold_tokens INTEGER,
			long_context_input_usd_per_million REAL,
			long_context_output_usd_per_million REAL,
			long_context_cache_read_usd_per_million REAL,
			long_context_cache_creation_usd_per_million REAL,
			source TEXT NOT NULL DEFAULT 'manual', source_model TEXT,
			auto_synced BOOLEAN NOT NULL DEFAULT 0, last_synced_at DATETIME,
			updated_at DATETIME NOT NULL
		);
		INSERT INTO model_prices (
			id, provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, source, updated_at
		) VALUES
			(10, 'openai', 'gpt-test', 'library', NULL, NULL, 1, 'manual', '2026-07-15T10:00:00Z'),
			(11, 'codex', 'gpt-test', 'channel', 'codex', 'account-a.json', 2, 'manual', '2026-07-15T11:00:00Z');
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	if err := upModelPriceChannelAuthType(context.Background(), db); err != nil {
		t.Fatalf("upModelPriceChannelAuthType failed: %v", err)
	}

	var libraryAuth, channelAuth sql.NullString
	if err := db.QueryRow(`SELECT channel_auth_type FROM model_prices WHERE id = 10`).Scan(&libraryAuth); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT channel_auth_type FROM model_prices WHERE id = 11`).Scan(&channelAuth); err != nil {
		t.Fatal(err)
	}
	if libraryAuth.Valid || !channelAuth.Valid || channelAuth.String != "apikey" {
		t.Fatalf("migrated auth types = library %#v channel %#v", libraryAuth, channelAuth)
	}

	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, source, updated_at
		) VALUES ('codex', 'gpt-test', 'channel', 'oauth', 'codex', 'oauth_pool', 3, 'manual', '2026-07-15T12:00:00Z')
	`); err != nil {
		t.Fatalf("insert OAuth pool price beside API key price: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, source, updated_at
		) VALUES ('codex', 'GPT-TEST', 'channel', 'oauth', 'codex', 'oauth_pool', 4, 'manual', '2026-07-15T13:00:00Z')
	`); err == nil {
		t.Fatal("duplicate OAuth pool/model price should be rejected")
	}
}
