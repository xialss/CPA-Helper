package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpOpenAICompatiblePricingRebuildInvalidatesHourlyAggregates(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE usage_analytics_state (
			id INTEGER PRIMARY KEY,
			pricing_version INTEGER NOT NULL,
			hourly_needs_rebuild BOOLEAN NOT NULL,
			updated_at DATETIME
		);
		INSERT INTO usage_analytics_state (id, pricing_version, hourly_needs_rebuild, updated_at)
		VALUES (1, 41, 0, '2026-09-02T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed analytics state: %v", err)
	}

	if err := upOpenAICompatiblePricingRebuild(context.Background(), db); err != nil {
		t.Fatalf("upOpenAICompatiblePricingRebuild failed: %v", err)
	}

	var pricingVersion int64
	var needsRebuild bool
	var updatedAt string
	if err := db.QueryRow(`
		SELECT pricing_version, hourly_needs_rebuild, CAST(updated_at AS TEXT)
		FROM usage_analytics_state WHERE id = 1
	`).Scan(&pricingVersion, &needsRebuild, &updatedAt); err != nil {
		t.Fatalf("read analytics state: %v", err)
	}
	if pricingVersion != 42 || !needsRebuild || updatedAt == "" {
		t.Fatalf("analytics state = pricing %d rebuild %v updated %q, want 42/true/nonempty", pricingVersion, needsRebuild, updatedAt)
	}
}
