package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpModelPriceBillingUnitBackfillsLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE model_prices (id INTEGER PRIMARY KEY, model TEXT NOT NULL);
		CREATE TABLE model_price_library_conflicts (original_id INTEGER PRIMARY KEY, model TEXT NOT NULL);
		INSERT INTO model_prices (id, model) VALUES (1, 'gpt-5'), (2, 'custom-image-v1');
		INSERT INTO model_price_library_conflicts (original_id, model) VALUES (3, 'image-edit-v1'), (4, 'claude-3');
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	if err := upModelPriceBillingUnit(context.Background(), db); err != nil {
		t.Fatalf("upModelPriceBillingUnit failed: %v", err)
	}

	for _, testCase := range []struct {
		table, column string
		id            int
		want          string
	}{
		{table: "model_prices", column: "id", id: 1, want: "token"},
		{table: "model_prices", column: "id", id: 2, want: "request"},
		{table: "model_price_library_conflicts", column: "original_id", id: 3, want: "request"},
		{table: "model_price_library_conflicts", column: "original_id", id: 4, want: "token"},
	} {
		var actual string
		if err := db.QueryRow("SELECT billing_unit FROM "+testCase.table+" WHERE "+testCase.column+" = ?", testCase.id).Scan(&actual); err != nil {
			t.Fatalf("read %s/%d: %v", testCase.table, testCase.id, err)
		}
		if actual != testCase.want {
			t.Errorf("%s/%d billing_unit = %q, want %q", testCase.table, testCase.id, actual, testCase.want)
		}
	}

	if _, err := db.Exec(`UPDATE model_prices SET billing_unit = 'request' WHERE id = 1`); err != nil {
		t.Fatalf("set explicit billing unit: %v", err)
	}
	if err := upModelPriceBillingUnit(context.Background(), db); err != nil {
		t.Fatalf("repeat upModelPriceBillingUnit failed: %v", err)
	}
	var explicitUnit string
	if err := db.QueryRow(`SELECT billing_unit FROM model_prices WHERE id = 1`).Scan(&explicitUnit); err != nil {
		t.Fatalf("read preserved billing unit: %v", err)
	}
	if explicitUnit != "request" {
		t.Fatalf("repeated migration changed explicit billing unit to %q", explicitUnit)
	}
}
