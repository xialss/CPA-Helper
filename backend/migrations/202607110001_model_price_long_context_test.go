package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpModelPriceLongContextSkipsTierUnsafeForExistingPriorityMultiplier(t *testing.T) {
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
			input_usd_per_million REAL NOT NULL,
			output_usd_per_million REAL NOT NULL,
			cache_read_usd_per_million REAL NOT NULL,
			cache_creation_usd_per_million REAL NOT NULL,
			priority_multiplier REAL
		);
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, priority_multiplier
		) VALUES ('openai', 'gpt-5.6-terra', 1e-299, 1e-299, 1e-299, 1e-299, 1e307)
	`); err != nil {
		t.Fatalf("seed pre-long-context schema: %v", err)
	}

	if err := upModelPriceLongContext(context.Background(), db); err != nil {
		t.Fatalf("upModelPriceLongContext failed: %v", err)
	}
	var threshold sql.NullInt64
	var input, output, cacheRead, cacheCreation sql.NullFloat64
	if err := db.QueryRow(`
		SELECT long_context_threshold_tokens, long_context_input_usd_per_million,
		       long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
		       long_context_cache_creation_usd_per_million
		FROM model_prices
		WHERE provider = 'openai' AND model = 'gpt-5.6-terra'
	`).Scan(&threshold, &input, &output, &cacheRead, &cacheCreation); err != nil {
		t.Fatalf("query migrated row: %v", err)
	}
	if threshold.Valid || input.Valid || output.Valid || cacheRead.Valid || cacheCreation.Valid {
		t.Fatalf("unsafe long-context tier was seeded: %#v/%#v/%#v/%#v/%#v", threshold, input, output, cacheRead, cacheCreation)
	}
}
