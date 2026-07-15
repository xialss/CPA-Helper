package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	backendMigrations "cpa-helper/backend/migrations"
)

func TestCheckStartupDoesNotCreateMissingDatabase(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	_, err := CheckStartup(context.Background())
	if !errors.Is(err, ErrDatabaseNotInitialized) {
		t.Fatalf("CheckStartup error = %v, want ErrDatabaseNotInitialized", err)
	}

	dbPath := filepath.Join(dataDir, "db", "cpa_helper.sqlite3")
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("startup check created database file or returned unexpected stat error: %v", statErr)
	}
}

func TestMigrateMakesStartupCheckReady(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	report, err := Migrate(context.Background())
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	if report.CurrentVersion != backendMigrations.LatestVersion {
		t.Fatalf("migration version = %d, want %d", report.CurrentVersion, backendMigrations.LatestVersion)
	}

	check, err := CheckStartup(context.Background())
	if err != nil {
		t.Fatalf("CheckStartup failed after migration: %v", err)
	}
	if check.CurrentVersion != backendMigrations.LatestVersion {
		t.Fatalf("startup version = %d, want %d", check.CurrentVersion, backendMigrations.LatestVersion)
	}
}

func TestCheckStartupDoesNotCreateVersionTableForUnmigratedDatabase(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbDir := filepath.Join(dataDir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "cpa_helper.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = CheckStartup(context.Background())
	if !errors.Is(err, ErrDatabaseNotInitialized) {
		t.Fatalf("CheckStartup error = %v, want ErrDatabaseNotInitialized", err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("goose_db_version table count = %d, want 0", count)
	}
}

func TestReadyEndpointReportsMigrationVersion(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/ready", nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireSchemaShapeRejectsMissingModelPriceChannelColumns(t *testing.T) {
	requiredModelPriceColumns := []string{
		"request_usd",
		"priority_multiplier",
		"price_scope",
		"channel_brand",
		"channel_key",
		"long_context_threshold_tokens",
		"long_context_input_usd_per_million",
		"long_context_output_usd_per_million",
		"long_context_cache_read_usd_per_million",
		"long_context_cache_creation_usd_per_million",
	}
	for _, missing := range []string{"price_scope", "channel_brand", "channel_key"} {
		t.Run(missing, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			columns := make([]string, 0, len(requiredModelPriceColumns))
			for _, column := range requiredModelPriceColumns {
				if column != missing {
					columns = append(columns, column+" TEXT")
				}
			}
			statements := []string{
				`CREATE TABLE app_settings (session_secret TEXT)`,
				`CREATE TABLE users (username TEXT)`,
				`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
				`CREATE TABLE model_prices (` + strings.Join(columns, ", ") + `)`,
				modelPriceLibraryConflictSchemaForStartupTest,
				`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
			}
			for _, statement := range statements {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("create test schema: %v", err)
				}
			}
			err = requireSchemaShape(context.Background(), db)
			if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "model_prices."+missing) {
				t.Fatalf("requireSchemaShape error = %v, want missing model_prices.%s", err, missing)
			}
		})
	}
}

func TestRequireSchemaShapeRejectsMissingModelPriceLibraryConflictsTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	statements := []string{
		`CREATE TABLE app_settings (session_secret TEXT)`,
		`CREATE TABLE users (username TEXT)`,
		`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
		`CREATE TABLE model_prices (
			request_usd TEXT, priority_multiplier TEXT, price_scope TEXT, channel_brand TEXT, channel_key TEXT,
			long_context_threshold_tokens TEXT, long_context_input_usd_per_million TEXT,
			long_context_output_usd_per_million TEXT, long_context_cache_read_usd_per_million TEXT,
			long_context_cache_creation_usd_per_million TEXT
		)`,
		`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create test schema: %v", err)
		}
	}
	err = requireSchemaShape(context.Background(), db)
	if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "model_price_library_conflicts.original_id") {
		t.Fatalf("requireSchemaShape error = %v, want missing model_price_library_conflicts table", err)
	}
}

const modelPriceLibraryConflictSchemaForStartupTest = `CREATE TABLE model_price_library_conflicts (
	original_id TEXT, selected_price_id TEXT, conflict_reason TEXT, provider TEXT, model TEXT,
	input_usd_per_million TEXT, output_usd_per_million TEXT,
	cache_read_usd_per_million TEXT, cache_creation_usd_per_million TEXT,
	request_usd TEXT, priority_multiplier TEXT, long_context_threshold_tokens TEXT,
	long_context_input_usd_per_million TEXT, long_context_output_usd_per_million TEXT,
	long_context_cache_read_usd_per_million TEXT, long_context_cache_creation_usd_per_million TEXT,
	source TEXT, source_model TEXT, auto_synced TEXT, last_synced_at TEXT, updated_at TEXT
)`
