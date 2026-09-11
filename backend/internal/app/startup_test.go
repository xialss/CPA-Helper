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
	"time"

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

func TestUsageMaintenanceStartupProfiles(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	setup, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}

	now := time.Now().In(appTimeLocation)
	expiredAt := usageRawRetentionCutoff(now).Add(-time.Second)
	const rawJSON = `{"source":"startup-profile@example.com"}`
	result, err := setup.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-startup-profile', '/v1/responses', 0,
			1, 0, 0, 0, 0, 0, 1, 'startup-profile-expired-record', ?)
	`, dbTime(now), dbTime(expiredAt), rawJSON)
	if err != nil {
		setup.Close()
		t.Fatalf("insert expired raw usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		setup.Close()
		t.Fatalf("read expired raw usage record id: %v", err)
	}
	setup.Close()

	assertRawJSON := func(stage string, app *App, want string) {
		t.Helper()
		var got string
		if err := app.db.QueryRowContext(ctx, `SELECT raw_json FROM usage_records WHERE id = ?`, recordID).Scan(&got); err != nil {
			t.Fatalf("%s: query raw usage record: %v", stage, err)
		}
		if got != want {
			t.Fatalf("%s: raw_json = %q, want %q", stage, got, want)
		}
	}

	serveApp, err := NewWithOptions(ctx, NewOptions{
		RequireReady:          true,
		StartBackground:       true,
		StartUsageMaintenance: false,
	})
	if err != nil {
		t.Fatalf("initialize serve profile: %v", err)
	}
	if serveApp.usageMaintenance != nil {
		serveApp.Close()
		t.Fatal("serve profile created a usage maintenance runner")
	}
	if serveApp.collector == nil || serveApp.keeper == nil {
		serveApp.Close()
		t.Fatal("serve profile did not start the collector and Keeper background runners")
	}
	assertRawJSON("serve profile", serveApp, rawJSON)
	serveApp.Close()

	defaultApp, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer defaultApp.Close()
	if defaultApp.usageMaintenance == nil || defaultApp.usageMaintenance.done == nil {
		t.Fatal("New() did not start immediate and periodic usage maintenance")
	}
	assertRawJSON("default app", defaultApp, "")
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

func TestRequireSchemaShapeRejectsMissingModelMonitorColumns(t *testing.T) {
	for _, missing := range []string{"model_monitor_proxy_enabled", "model_monitor_proxy_url", "model_monitor_enabled_source_ids"} {
		t.Run(missing, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			statements := []string{
				appSettingsSchemaForStartupTest(missing),
				`CREATE TABLE users (username TEXT, is_super_admin BOOLEAN, must_change_password BOOLEAN, session_version INTEGER)`,
				`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
				codexKeeperAuthStateSchemaForStartupTest,
				modelPriceSchemaForStartupTest,
				modelPriceLibraryConflictSchemaForStartupTest,
				`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
			}
			for _, statement := range statements {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("create test schema: %v", err)
				}
			}
			err = requireSchemaShape(context.Background(), db)
			if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "app_settings."+missing) {
				t.Fatalf("requireSchemaShape error = %v, want missing app_settings.%s", err, missing)
			}
		})
	}
}

func TestRequireSchemaShapeRejectsMissingModelPriceChannelColumns(t *testing.T) {
	requiredModelPriceColumns := []string{
		"request_usd",
		"billing_unit",
		"priority_multiplier",
		"price_scope",
		"channel_auth_type",
		"channel_brand",
		"channel_key",
		"long_context_threshold_tokens",
		"long_context_input_usd_per_million",
		"long_context_output_usd_per_million",
		"long_context_cache_read_usd_per_million",
		"long_context_cache_creation_usd_per_million",
	}
	for _, missing := range []string{"price_scope", "channel_auth_type", "channel_brand", "channel_key"} {
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
				appSettingsSchemaForStartupTest(""),
				`CREATE TABLE users (username TEXT, is_super_admin BOOLEAN, must_change_password BOOLEAN, session_version INTEGER)`,
				`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
				codexKeeperAuthStateSchemaForStartupTest,
				`CREATE TABLE model_prices (` + strings.Join(columns, ", ") + `)`,
				modelPriceLibraryConflictSchemaForStartupTest,
				`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
			}
			statements = append(statements, usageAnalyticsSchemasForStartupTest...)
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
		appSettingsSchemaForStartupTest(""),
		`CREATE TABLE users (username TEXT, is_super_admin BOOLEAN, must_change_password BOOLEAN, session_version INTEGER)`,
		`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
		codexKeeperAuthStateSchemaForStartupTest,
		`CREATE TABLE model_prices (
			request_usd TEXT, billing_unit TEXT, priority_multiplier TEXT, price_scope TEXT, channel_auth_type TEXT, channel_brand TEXT, channel_key TEXT,
			long_context_threshold_tokens TEXT, long_context_input_usd_per_million TEXT,
			long_context_output_usd_per_million TEXT, long_context_cache_read_usd_per_million TEXT,
			long_context_cache_creation_usd_per_million TEXT
		)`,
		`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
	}
	statements = append(statements, usageAnalyticsSchemasForStartupTest...)
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

func TestRequireSchemaShapeRejectsMissingKeeperAuthIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	statements := []string{
		appSettingsSchemaForStartupTest(""),
		`CREATE TABLE users (username TEXT, is_super_admin BOOLEAN, must_change_password BOOLEAN, session_version INTEGER)`,
		`CREATE TABLE usage_records (dedupe_key TEXT, ttft_ms TEXT, service_tier TEXT)`,
		`CREATE TABLE codex_keeper_auth_states (auth_name TEXT)`,
		`CREATE TABLE model_prices (
			request_usd TEXT, billing_unit TEXT, priority_multiplier TEXT, price_scope TEXT, channel_auth_type TEXT, channel_brand TEXT, channel_key TEXT,
			long_context_threshold_tokens TEXT, long_context_input_usd_per_million TEXT,
			long_context_output_usd_per_million TEXT, long_context_cache_read_usd_per_million TEXT,
			long_context_cache_creation_usd_per_million TEXT
		)`,
		modelPriceLibraryConflictSchemaForStartupTest,
		`CREATE TABLE user_quota_charges (lifetime_deducted_usd TEXT)`,
	}
	statements = append(statements, usageAnalyticsSchemasForStartupTest...)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create test schema: %v", err)
		}
	}
	err = requireSchemaShape(context.Background(), db)
	if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "codex_keeper_auth_states.auth_index") {
		t.Fatalf("requireSchemaShape error = %v, want missing codex_keeper_auth_states.auth_index", err)
	}
}

func TestRequireSchemaShapeRejectsMissingModelPriceVersionID(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("create current schema: %v", err)
	}
	defer app.Close()

	if _, err := app.db.Exec(`
		CREATE TABLE model_price_versions_without_id AS
		SELECT price_id, provider, model, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
			request_usd, billing_unit, priority_multiplier,
			long_context_threshold_tokens, long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			effective_at, baseline, created_at, time_pricing
		FROM model_price_versions;
		DROP TABLE model_price_versions;
		ALTER TABLE model_price_versions_without_id RENAME TO model_price_versions;
	`); err != nil {
		t.Fatalf("remove model_price_versions.id from test schema: %v", err)
	}

	err = requireSchemaShape(context.Background(), app.db)
	if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), "model_price_versions.id") {
		t.Fatalf("requireSchemaShape error = %v, want missing model_price_versions.id", err)
	}
}

func TestRequireSchemaShapeRejectsMissingNewModelPricingSchema(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	sourcePath := prepareMigrationTestDatabase(t, dataDir, backendMigrations.LatestVersion)
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read current schema fixture: %v", err)
	}

	for _, test := range []struct {
		table     string
		column    string
		dropTable bool
	}{
		{table: "model_price_channel_aliases", column: "auth_type", dropTable: true},
		{table: "model_price_channel_aliases", column: "channel_brand"},
		{table: "model_price_channel_aliases", column: "channel_key"},
		{table: "model_price_channel_aliases", column: "label"},
		{table: "model_price_channel_aliases", column: "created_at"},
		{table: "model_price_channel_aliases", column: "updated_at"},
		{table: "model_prices", column: "time_pricing"},
		{table: "model_price_versions", column: "id", dropTable: true},
		{table: "model_price_versions", column: "price_id"},
		{table: "model_price_versions", column: "time_pricing"},
		{table: "model_price_versions", column: "provider"},
		{table: "model_price_versions", column: "model"},
		{table: "model_price_versions", column: "channel_auth_type"},
		{table: "model_price_versions", column: "channel_brand"},
		{table: "model_price_versions", column: "channel_key"},
		{table: "model_price_versions", column: "input_usd_per_million"},
		{table: "model_price_versions", column: "output_usd_per_million"},
		{table: "model_price_versions", column: "cache_read_usd_per_million"},
		{table: "model_price_versions", column: "cache_creation_usd_per_million"},
		{table: "model_price_versions", column: "request_usd"},
		{table: "model_price_versions", column: "billing_unit"},
		{table: "model_price_versions", column: "priority_multiplier"},
		{table: "model_price_versions", column: "long_context_threshold_tokens"},
		{table: "model_price_versions", column: "long_context_input_usd_per_million"},
		{table: "model_price_versions", column: "long_context_output_usd_per_million"},
		{table: "model_price_versions", column: "long_context_cache_read_usd_per_million"},
		{table: "model_price_versions", column: "long_context_cache_creation_usd_per_million"},
		{table: "model_price_versions", column: "effective_at"},
		{table: "model_price_versions", column: "baseline"},
		{table: "model_price_versions", column: "created_at"},
		{table: "model_price_time_templates", column: "name", dropTable: true},
		{table: "model_price_time_templates", column: "rule"},
	} {
		t.Run(test.table+"."+test.column, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "schema.sqlite3")
			if err := os.WriteFile(dbPath, source, 0o600); err != nil {
				t.Fatalf("copy current schema fixture: %v", err)
			}
			db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			if test.dropTable {
				if _, err := db.Exec(`DROP TABLE ` + quoteStartupTestIdentifier(test.table)); err != nil {
					t.Fatalf("drop %s: %v", test.table, err)
				}
			} else {
				removeStartupTestSchemaColumn(t, db, test.table, test.column)
			}

			err = requireSchemaShape(context.Background(), db)
			if !errors.Is(err, ErrDatabaseNeedsMigration) || !strings.Contains(err.Error(), test.table+"."+test.column) {
				t.Fatalf("requireSchemaShape error = %v, want missing %s.%s", err, test.table, test.column)
			}
		})
	}
}

func removeStartupTestSchemaColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + quoteStartupTestIdentifier(table) + `)`)
	if err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	columns := make([]string, 0)
	found := false
	for rows.Next() {
		var id, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&id, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if name == column {
			found = true
			continue
		}
		columns = append(columns, quoteStartupTestIdentifier(name))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("%s.%s was not present in current schema fixture", table, column)
	}

	const replacement = "__startup_schema_without_column"
	if _, err := db.Exec(`CREATE TABLE ` + quoteStartupTestIdentifier(replacement) + ` AS SELECT ` + strings.Join(columns, ", ") + ` FROM ` + quoteStartupTestIdentifier(table)); err != nil {
		t.Fatalf("rebuild %s without %s: %v", table, column, err)
	}
	if _, err := db.Exec(`DROP TABLE ` + quoteStartupTestIdentifier(table)); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
	if _, err := db.Exec(`ALTER TABLE ` + quoteStartupTestIdentifier(replacement) + ` RENAME TO ` + quoteStartupTestIdentifier(table)); err != nil {
		t.Fatalf("rename rebuilt %s: %v", table, err)
	}
}

func quoteStartupTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

const codexKeeperAuthStateSchemaForStartupTest = `CREATE TABLE codex_keeper_auth_states (auth_index TEXT)`

const modelPriceSchemaForStartupTest = `CREATE TABLE model_prices (
	request_usd TEXT, billing_unit TEXT, priority_multiplier TEXT, price_scope TEXT, channel_auth_type TEXT, channel_brand TEXT, channel_key TEXT,
	long_context_threshold_tokens TEXT, long_context_input_usd_per_million TEXT,
	long_context_output_usd_per_million TEXT, long_context_cache_read_usd_per_million TEXT,
	long_context_cache_creation_usd_per_million TEXT
)`

const modelPriceLibraryConflictSchemaForStartupTest = `CREATE TABLE model_price_library_conflicts (
	original_id TEXT, selected_price_id TEXT, conflict_reason TEXT, provider TEXT, model TEXT,
	input_usd_per_million TEXT, output_usd_per_million TEXT,
	cache_read_usd_per_million TEXT, cache_creation_usd_per_million TEXT,
	request_usd TEXT, billing_unit TEXT, priority_multiplier TEXT, long_context_threshold_tokens TEXT,
	long_context_input_usd_per_million TEXT, long_context_output_usd_per_million TEXT,
	long_context_cache_read_usd_per_million TEXT, long_context_cache_creation_usd_per_million TEXT,
	source TEXT, source_model TEXT, auto_synced TEXT, last_synced_at TEXT, updated_at TEXT
)`

var usageAnalyticsSchemasForStartupTest = []string{
	`CREATE TABLE usage_analytics_facts (
		usage_record_id TEXT, timestamp TEXT, source_key TEXT, auth TEXT, auth_index TEXT, source_account TEXT
	)`,
	`CREATE TABLE usage_source_catalog (source_key TEXT, source TEXT, auth_conflict TEXT)`,
	`CREATE TABLE usage_analytics_pending_facts (usage_record_id TEXT)`,
	`CREATE TABLE usage_analytics_hourly (
		hour_start TEXT, facts_version TEXT, source_key TEXT, aggregate_total_tokens TEXT, estimated_cost_usd TEXT
	)`,
	`CREATE TABLE usage_analytics_state (
		facts_version TEXT, hourly_facts_version TEXT, hourly_max_record_id TEXT, hourly_needs_rebuild TEXT,
		selector_fingerprint TEXT, last_pruned_records TEXT, last_maintenance_error TEXT
	)`,
}

func appSettingsSchemaForStartupTest(missing string) string {
	columns := []string{"session_secret TEXT", "model_monitor_proxy_enabled TEXT", "model_monitor_proxy_url TEXT", "model_monitor_enabled_source_ids TEXT"}
	kept := make([]string, 0, len(columns))
	for _, column := range columns {
		if !strings.HasPrefix(column, missing+" ") {
			kept = append(kept, column)
		}
	}
	return `CREATE TABLE app_settings (` + strings.Join(kept, ", ") + `)`
}
