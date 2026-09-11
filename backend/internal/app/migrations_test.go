package app

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func TestRunMigrationsCreatesGooseVersionAndFinalSchema(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	if !testColumnExists(t, app.db, "usage_records", "usage_username") {
		t.Fatal("usage_records.usage_username was not created")
	}
	if testColumnExists(t, app.db, "usage_records", "api_key_hash") {
		t.Fatal("old usage_records.api_key_hash should not exist")
	}
	if !testColumnExists(t, app.db, "usage_records", "cache_read_tokens") {
		t.Fatal("usage_records.cache_read_tokens was not created")
	}
	if !testColumnExists(t, app.db, "usage_records", "cache_creation_tokens") {
		t.Fatal("usage_records.cache_creation_tokens was not created")
	}
	if !testColumnExists(t, app.db, "usage_records", "reasoning_effort") {
		t.Fatal("usage_records.reasoning_effort was not created")
	}
	if !testColumnExists(t, app.db, "usage_records", "ttft_ms") {
		t.Fatal("usage_records.ttft_ms was not created")
	}
	if !testColumnExists(t, app.db, "usage_records", "service_tier") {
		t.Fatal("usage_records.service_tier was not created")
	}
	if !testColumnExists(t, app.db, "model_prices", "cache_read_usd_per_million") {
		t.Fatal("model_prices.cache_read_usd_per_million was not created")
	}
	if !testColumnExists(t, app.db, "model_prices", "cache_creation_usd_per_million") {
		t.Fatal("model_prices.cache_creation_usd_per_million was not created")
	}
	if !testColumnExists(t, app.db, "model_prices", "request_usd") {
		t.Fatal("model_prices.request_usd was not created")
	}
	if !testColumnExists(t, app.db, "model_prices", "priority_multiplier") {
		t.Fatal("model_prices.priority_multiplier was not created")
	}
	for _, column := range []string{"price_scope", "channel_auth_type", "channel_brand", "channel_key"} {
		if !testColumnExists(t, app.db, "model_prices", column) {
			t.Fatalf("model_prices.%s was not created", column)
		}
	}
	for _, column := range []string{
		"long_context_threshold_tokens",
		"long_context_input_usd_per_million",
		"long_context_output_usd_per_million",
		"long_context_cache_read_usd_per_million",
		"long_context_cache_creation_usd_per_million",
	} {
		if !testColumnExists(t, app.db, "model_prices", column) {
			t.Fatalf("model_prices.%s was not created", column)
		}
	}
	if testColumnExists(t, app.db, "model_prices", "cached_usd_per_million") {
		t.Fatal("old model_prices.cached_usd_per_million should not exist")
	}
	if testColumnExists(t, app.db, "model_prices", "reasoning_usd_per_million") {
		t.Fatal("old model_prices.reasoning_usd_per_million should not exist")
	}
	if !testColumnExists(t, app.db, "app_settings", "litellm_proxy_enabled") {
		t.Fatal("app_settings.litellm_proxy_enabled was not created")
	}
	if !testColumnExists(t, app.db, "app_settings", "litellm_proxy_url") {
		t.Fatal("app_settings.litellm_proxy_url was not created")
	}
	if !testColumnExists(t, app.db, "app_settings", "model_request_url") {
		t.Fatal("app_settings.model_request_url was not created")
	}
	if !testColumnExists(t, app.db, "app_settings", "model_monitor_proxy_enabled") {
		t.Fatal("app_settings.model_monitor_proxy_enabled was not created")
	}
	if !testColumnExists(t, app.db, "app_settings", "model_monitor_proxy_url") {
		t.Fatal("app_settings.model_monitor_proxy_url was not created")
	}
	if !testColumnExists(t, app.db, "app_settings", "model_monitor_enabled_source_ids") {
		t.Fatal("app_settings.model_monitor_enabled_source_ids was not created")
	}
	if !testColumnExists(t, app.db, "users", "quota_lifetime_usd") {
		t.Fatal("users.quota_lifetime_usd was not created")
	}
	if testColumnExists(t, app.db, "users", "quota_total_usd") {
		t.Fatal("old users.quota_total_usd should not exist")
	}
	if !testColumnExists(t, app.db, "users", "quota_monthly_usd") {
		t.Fatal("users.quota_monthly_usd was not created")
	}
	if !testColumnExists(t, app.db, "users", "is_super_admin") {
		t.Fatal("users.is_super_admin was not created")
	}
	if !testTableExists(t, app.db, "user_quota_charges") {
		t.Fatal("user_quota_charges was not created")
	}
	for _, table := range []string{"model_price_channel_aliases", "model_price_versions", "model_price_time_templates"} {
		if !testTableExists(t, app.db, table) {
			t.Fatalf("%s was not created", table)
		}
	}
	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "model_prices", column: "time_pricing"},
		{table: "model_price_channel_aliases", column: "auth_type"},
		{table: "model_price_channel_aliases", column: "channel_brand"},
		{table: "model_price_channel_aliases", column: "channel_key"},
		{table: "model_price_channel_aliases", column: "label"},
		{table: "model_price_channel_aliases", column: "created_at"},
		{table: "model_price_channel_aliases", column: "updated_at"},
		{table: "model_price_versions", column: "id"},
		{table: "model_price_versions", column: "price_id"},
		{table: "model_price_versions", column: "time_pricing"},
		{table: "model_price_time_templates", column: "name"},
		{table: "model_price_time_templates", column: "rule"},
	} {
		if !testColumnExists(t, app.db, item.table, item.column) {
			t.Fatalf("%s.%s was not created", item.table, item.column)
		}
	}
	for _, table := range []string{"usage_analytics_facts", "usage_analytics_hourly", "usage_analytics_pending_facts", "usage_analytics_state", "usage_source_catalog"} {
		if !testTableExists(t, app.db, table) {
			t.Fatalf("%s was not created", table)
		}
	}
	for _, column := range []string{"hourly_facts_version", "hourly_max_record_id", "hourly_needs_rebuild"} {
		if !testColumnExists(t, app.db, "usage_analytics_state", column) {
			t.Fatalf("usage_analytics_state.%s was not created", column)
		}
	}
	if testTableExists(t, app.db, "user_card_shop_favorites") {
		t.Fatal("user_card_shop_favorites should not exist")
	}
	if testTableExists(t, app.db, "user_card_shop_tags") {
		t.Fatal("user_card_shop_tags should not exist")
	}
	if !testColumnExists(t, app.db, "user_quota_charges", "lifetime_deducted_usd") {
		t.Fatal("user_quota_charges.lifetime_deducted_usd was not created")
	}
	if !testColumnExists(t, app.db, "codex_keeper_auth_states", "auth_index") {
		t.Fatal("codex_keeper_auth_states.auth_index was not created")
	}
	if testColumnExists(t, app.db, "user_quota_charges", "total_deducted_usd") {
		t.Fatal("old user_quota_charges.total_deducted_usd should not exist")
	}
	for _, index := range []string{
		"ix_usage_records_failed_timestamp",
		"ix_usage_records_usage_username_timestamp",
		"ix_usage_analytics_facts_timestamp",
		"ix_usage_analytics_facts_usage_username_timestamp",
		"ix_usage_analytics_facts_source_key_timestamp",
	} {
		if !testIndexExists(t, app.db, index) {
			t.Fatalf("%s was not created", index)
		}
	}
	for _, index := range []string{
		"ix_usage_analytics_facts_failed_timestamp",
		"ix_usage_analytics_facts_provider_timestamp",
		"ix_usage_analytics_facts_model_timestamp",
		"ix_usage_analytics_facts_endpoint_timestamp",
	} {
		if testIndexExists(t, app.db, index) {
			t.Fatalf("%s should be removed after hourly aggregation index trim", index)
		}
	}

	var version int64
	if err := app.db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("query goose version: %v", err)
	}
	if version != backendMigrations.LatestVersion {
		t.Fatalf("goose version = %d, want %d", version, backendMigrations.LatestVersion)
	}

	var settingsCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE id = 1`).Scan(&settingsCount); err != nil {
		t.Fatalf("query app_settings singleton: %v", err)
	}
	if settingsCount != 1 {
		t.Fatalf("app_settings singleton count = %d, want 1", settingsCount)
	}
	var modelMonitorProxyEnabled bool
	var modelMonitorProxyURL, modelMonitorEnabledSourceIDs string
	if err := app.db.QueryRow(`SELECT model_monitor_proxy_enabled, model_monitor_proxy_url, model_monitor_enabled_source_ids FROM app_settings WHERE id = 1`).Scan(&modelMonitorProxyEnabled, &modelMonitorProxyURL, &modelMonitorEnabledSourceIDs); err != nil {
		t.Fatalf("query model monitor proxy defaults: %v", err)
	}
	if modelMonitorProxyEnabled || modelMonitorProxyURL != "" {
		t.Fatalf("model monitor proxy defaults = %t/%q, want false/empty", modelMonitorProxyEnabled, modelMonitorProxyURL)
	}
	if modelMonitorEnabledSourceIDs != `["ai-input-im","openai","anthropic","deepseek"]` {
		t.Fatalf("model monitor enabled source IDs default = %q", modelMonitorEnabledSourceIDs)
	}
}

func TestRunMigrationsUpgradesFromPreviousProductionHead(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202609040001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade from production head %d to %d: %v", 202609040001, backendMigrations.LatestVersion, err)
	}

	var current int64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&current); err != nil {
		t.Fatalf("query migrated version: %v", err)
	}
	if current != backendMigrations.LatestVersion {
		t.Fatalf("migrated version = %d, want %d", current, backendMigrations.LatestVersion)
	}
	for _, version := range []int64{202609050001, 202609060001, 202609070001, 202609070002, 202609080001} {
		var applied int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM goose_db_version WHERE is_applied = 1 AND version_id = ?`, version).Scan(&applied); err != nil {
			t.Fatalf("check migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied rows = %d, want 1", version, applied)
		}
	}
}

func TestRunMigrationsUserSuperAdminRecoveryPreservesExistingAccess(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202608090001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		INSERT INTO users (id, username, is_admin, nickname, disabled_at, created_at, updated_at)
		VALUES
			(1, 'disabled-admin', 1, 'Disabled admin', '2026-09-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(2, 'active-user', 0, 'Active user', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(3, 'active-admin', 1, 'Active admin', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(4, 'second-active-admin', 1, 'Second active admin', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00')
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed previous-head users: %v", err)
	}
	ctx := context.Background()
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 202609070002); err != nil {
		t.Fatalf("upgrade to the previous schema head: %v", err)
	}
	for _, expected := range []struct {
		id           int
		isAdmin      bool
		isSuperAdmin bool
	}{
		{id: 1, isAdmin: true, isSuperAdmin: false},
		{id: 2, isAdmin: false, isSuperAdmin: false},
		{id: 3, isAdmin: true, isSuperAdmin: true},
		{id: 4, isAdmin: true, isSuperAdmin: false},
	} {
		var isAdmin, isSuperAdmin bool
		if err := db.QueryRowContext(ctx, `SELECT is_admin, is_super_admin FROM users WHERE id = ?`, expected.id).Scan(&isAdmin, &isSuperAdmin); err != nil {
			t.Fatalf("query pre-recovery user %d roles: %v", expected.id, err)
		}
		if isAdmin != expected.isAdmin || isSuperAdmin != expected.isSuperAdmin {
			t.Fatalf("pre-recovery user %d roles = admin=%t super_admin=%t, want admin=%t super_admin=%t", expected.id, isAdmin, isSuperAdmin, expected.isAdmin, expected.isSuperAdmin)
		}
	}
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade current schema head: %v", err)
	}
	for _, expected := range []struct {
		id           int
		isAdmin      bool
		isSuperAdmin bool
	}{
		{id: 1, isAdmin: true, isSuperAdmin: false},
		{id: 2, isAdmin: false, isSuperAdmin: false},
		{id: 3, isAdmin: true, isSuperAdmin: true},
		{id: 4, isAdmin: true, isSuperAdmin: false},
	} {
		var isAdmin, isSuperAdmin bool
		if err := db.QueryRowContext(ctx, `SELECT is_admin, is_super_admin FROM users WHERE id = ?`, expected.id).Scan(&isAdmin, &isSuperAdmin); err != nil {
			t.Fatalf("query recovered user %d roles: %v", expected.id, err)
		}
		if isAdmin != expected.isAdmin || isSuperAdmin != expected.isSuperAdmin {
			t.Fatalf("recovered user %d roles = admin=%t super_admin=%t, want admin=%t super_admin=%t", expected.id, isAdmin, isSuperAdmin, expected.isAdmin, expected.isSuperAdmin)
		}
	}
}

func TestRunMigrationsUserSuperAdminRecoveryPreservesNewOrdinaryAdministrator(t *testing.T) {
	for _, test := range []struct {
		name    string
		version int64
	}{
		{name: "production_head", version: 202609040001},
		{name: "previous_head", version: 202609070002},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
			dbPath := prepareMigrationTestDatabase(t, dataDir, test.version)
			db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`
				INSERT INTO users (id, username, is_admin, is_super_admin, nickname, created_at, updated_at)
				VALUES
					(1, 'active-super-admin', 1, 1, 'Super admin', '2026-09-04 00:00:00', '2026-09-04 00:00:00'),
					(2, 'new-ordinary-admin', 1, 0, 'Ordinary admin', '2026-09-05 00:00:00', '2026-09-05 00:00:00'),
					(3, 'other-ordinary-admin', 1, 0, 'Other admin', '2026-09-05 00:00:00', '2026-09-05 00:00:00')
			`); err != nil {
				t.Fatalf("create administrators after the super-admin migration: %v", err)
			}
			const disabledAt = "2026-09-06 00:00:00"
			if _, err := db.Exec(`UPDATE users SET disabled_at = ? WHERE id = 2`, disabledAt); err != nil {
				t.Fatalf("disable the new ordinary administrator: %v", err)
			}
			if err := goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion); err != nil {
				t.Fatalf("upgrade administrators to the current schema head: %v", err)
			}

			for _, expected := range []struct {
				id           int
				isSuperAdmin bool
				disabledAt   sql.NullString
			}{
				{id: 1, isSuperAdmin: true},
				{id: 2, disabledAt: sql.NullString{String: disabledAt, Valid: true}},
				{id: 3},
			} {
				var isAdmin, isSuperAdmin bool
				var gotDisabledAt sql.NullString
				if err := db.QueryRow(`SELECT is_admin, is_super_admin, CAST(disabled_at AS TEXT) FROM users WHERE id = ?`, expected.id).Scan(&isAdmin, &isSuperAdmin, &gotDisabledAt); err != nil {
					t.Fatalf("query user %d after recovery: %v", expected.id, err)
				}
				if !isAdmin || isSuperAdmin != expected.isSuperAdmin || gotDisabledAt != expected.disabledAt {
					t.Errorf("user %d changed during recovery: admin=%t super_admin=%t disabled_at=%v, want admin=true super_admin=%t disabled_at=%v", expected.id, isAdmin, isSuperAdmin, gotDisabledAt, expected.isSuperAdmin, expected.disabledAt)
				}
			}
		})
	}
}

func TestRunMigrationsUserSuperAdminAllowsFirstSetupAfterEmptyUpgrade(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	prepareMigrationTestDatabase(t, dataDir, 202608090001)

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("upgrade empty previous-head database: %v", err)
	}
	defer app.Close()

	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users after empty upgrade: %v", err)
	}
	if count != 0 {
		t.Fatalf("users after empty upgrade = %d, want 0", count)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"username":"first-admin","password":"password123","nickname":"First admin"}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create first admin status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var isAdmin, isSuperAdmin bool
	if err := app.db.QueryRow(`SELECT is_admin, is_super_admin FROM users WHERE username = 'first-admin'`).Scan(&isAdmin, &isSuperAdmin); err != nil {
		t.Fatalf("query first admin roles: %v", err)
	}
	if !isAdmin || !isSuperAdmin {
		t.Fatalf("first admin roles = admin=%t super_admin=%t, want both true", isAdmin, isSuperAdmin)
	}
}

func TestRunMigrationsUserSuperAdminPreservesDisabledAdminAndActiveAccess(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202608090001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		INSERT INTO users (id, username, is_admin, nickname, disabled_at, created_at, updated_at)
		VALUES
			(1, 'disabled-admin', 1, 'Disabled admin', '2026-09-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(2, 'active-user', 0, 'Active user', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00')
	`)
	if err != nil {
		t.Fatalf("seed users without an active administrator: %v", err)
	}

	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	err = goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion)
	if err != nil {
		t.Fatalf("upgrade with only a disabled administrator failed: %v", err)
	}

	var maxVersion int64
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("query applied migration version: %v", err)
	}
	if maxVersion != backendMigrations.LatestVersion {
		t.Fatalf("applied migration version = %d, want %d", maxVersion, backendMigrations.LatestVersion)
	}

	var isAdmin, isSuperAdmin bool
	var disabledAt sql.NullString
	if err := db.QueryRow(`SELECT is_admin, is_super_admin, CAST(disabled_at AS TEXT) FROM users WHERE id = 1`).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
		t.Fatalf("query recovered administrator: %v", err)
	}
	if !isAdmin || isSuperAdmin || !disabledAt.Valid || disabledAt.String != "2026-09-01 00:00:00" {
		t.Fatalf("first user = admin=%t super_admin=%t disabled_at=%q, want ordinary administrator with disabled state preserved", isAdmin, isSuperAdmin, disabledAt.String)
	}
	var activeUserAdmin, activeUserSuperAdmin bool
	if err := db.QueryRow(`SELECT is_admin, is_super_admin FROM users WHERE id = 2`).Scan(&activeUserAdmin, &activeUserSuperAdmin); err != nil {
		t.Fatalf("query active user role: %v", err)
	}
	if !activeUserAdmin || !activeUserSuperAdmin {
		t.Fatalf("active user roles = admin=%t super_admin=%t, want login-capable administrator", activeUserAdmin, activeUserSuperAdmin)
	}
}

func TestRunMigrationsUserSuperAdminRecoveryPreservesDisabledSuperAdministrator(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202609070002)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		INSERT INTO users (id, username, is_admin, is_super_admin, nickname, disabled_at, created_at, updated_at)
		VALUES
			(1, 'disabled-super-admin', 1, 1, 'Disabled super admin', '2026-09-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(2, 'active-user', 0, 0, 'Active user', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(3, 'active-admin', 1, 0, 'Active admin', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(4, 'disabled-admin', 1, 0, 'Disabled admin', '2026-09-02 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00')
	`); err != nil {
		t.Fatalf("seed previous-head users: %v", err)
	}
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade access-recovery database: %v", err)
	}

	for _, expected := range []struct {
		id           int
		isAdmin      bool
		isSuperAdmin bool
		disabledAt   sql.NullString
	}{
		{id: 1, isAdmin: true, isSuperAdmin: true, disabledAt: sql.NullString{String: "2026-09-01 00:00:00", Valid: true}},
		{id: 2},
		{id: 3, isAdmin: true},
		{id: 4, isAdmin: true, disabledAt: sql.NullString{String: "2026-09-02 00:00:00", Valid: true}},
	} {
		var isAdmin, isSuperAdmin bool
		var disabledAt sql.NullString
		if err := db.QueryRow(`SELECT is_admin, is_super_admin, CAST(disabled_at AS TEXT) FROM users WHERE id = ?`, expected.id).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
			t.Fatalf("query user %d after recovery: %v", expected.id, err)
		}
		if isAdmin != expected.isAdmin || isSuperAdmin != expected.isSuperAdmin || disabledAt != expected.disabledAt {
			t.Errorf("user %d changed during recovery: admin=%t super_admin=%t disabled_at=%v, want admin=%t super_admin=%t disabled_at=%v", expected.id, isAdmin, isSuperAdmin, disabledAt, expected.isAdmin, expected.isSuperAdmin, expected.disabledAt)
		}
	}
}

func TestRunMigrationsUserSuperAdminRecoveryInitializesOnlyWithoutSuperAdministrator(t *testing.T) {
	for _, test := range []struct {
		name                string
		activeAdministrator bool
		allDisabled         bool
		wantSuperAdminID    int
	}{
		{name: "prefer_active_administrator", activeAdministrator: true, wantSuperAdminID: 3},
		{name: "first_active_user", wantSuperAdminID: 2},
		{name: "all_disabled", allDisabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
			dbPath := prepareMigrationTestDatabase(t, dataDir, 202609070002)
			db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			const disabledTime = "2026-09-01 00:00:00"
			users := []struct {
				id         int
				username   string
				isAdmin    bool
				disabledAt sql.NullString
			}{
				{id: 1, username: "disabled-admin", isAdmin: true, disabledAt: sql.NullString{String: disabledTime, Valid: true}},
				{id: 2, username: "first-active-user", disabledAt: sql.NullString{String: disabledTime, Valid: test.allDisabled}},
				{id: 3, username: "second-active-user", isAdmin: test.activeAdministrator, disabledAt: sql.NullString{String: disabledTime, Valid: test.allDisabled}},
			}
			for _, user := range users {
				if _, err := db.Exec(`INSERT INTO users (id, username, is_admin, is_super_admin, nickname, disabled_at, created_at, updated_at)
					VALUES (?, ?, ?, 0, '', ?, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`, user.id, user.username, user.isAdmin, user.disabledAt); err != nil {
					t.Fatalf("seed user %d without a super-admin role: %v", user.id, err)
				}
			}
			if err := goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion); err != nil {
				t.Fatalf("upgrade uninitialized super-admin roles: %v", err)
			}
			for _, user := range users {
				var isAdmin, isSuperAdmin bool
				var disabledAt sql.NullString
				if err := db.QueryRow(`SELECT is_admin, is_super_admin, CAST(disabled_at AS TEXT) FROM users WHERE id = ?`, user.id).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
					t.Fatalf("query recovered user %d: %v", user.id, err)
				}
				wantSuperAdmin := user.id == test.wantSuperAdminID
				if isAdmin != (user.isAdmin || wantSuperAdmin) || isSuperAdmin != wantSuperAdmin || disabledAt.Valid != user.disabledAt.Valid || (disabledAt.Valid && disabledAt.String != user.disabledAt.String) {
					t.Errorf("recovered user %d: admin=%t super_admin=%t disabled_at=%v, want admin=%t super_admin=%t disabled_at=%v", user.id, isAdmin, isSuperAdmin, disabledAt, user.isAdmin || wantSuperAdmin, wantSuperAdmin, user.disabledAt)
				}
			}
		})
	}
}

func TestRunMigrationsBackfillsChannelPriceVersionFromPreviousHead(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202609050001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	updatedAt := "2026-09-05T08:09:10.123456Z"
	result, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, price_scope, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
			request_usd, billing_unit, priority_multiplier,
			long_context_threshold_tokens, long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (
			'codex', 'gpt-version-baseline', 'channel', 'oauth', 'codex', 'oauth_pool',
			1.25, 5, 0.125, 0.25,
			0.0042, 'token', 1.5,
			200000, 2.5, 10, 0.25, 0.5,
			'manual', 'gpt-version-baseline', 0, NULL, ?
		)
	`, updatedAt)
	if err != nil {
		t.Fatalf("seed channel price at previous head: %v", err)
	}
	priceID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read seeded price id: %v", err)
	}

	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", 202609060001); err != nil {
		t.Fatalf("upgrade channel price to version-history migration: %v", err)
	}

	var (
		gotPriceID, gotThreshold                         int64
		gotProvider, gotModel, gotAuth, gotBrand, gotKey string
		gotInput, gotOutput, gotCacheRead, gotCacheWrite float64
		gotRequest, gotPriority                          float64
		gotLongInput, gotLongOutput                      float64
		gotLongCacheRead, gotLongCacheWrite              float64
		gotBillingUnit, gotEffectiveAt                   string
		gotBaseline                                      bool
	)
	err = db.QueryRow(`
		SELECT price_id, provider, model, channel_auth_type, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
			request_usd, billing_unit, priority_multiplier,
			long_context_threshold_tokens, long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			effective_at, baseline
		FROM model_price_versions
		WHERE price_id = ?
	`, priceID).Scan(
		&gotPriceID, &gotProvider, &gotModel, &gotAuth, &gotBrand, &gotKey,
		&gotInput, &gotOutput, &gotCacheRead, &gotCacheWrite,
		&gotRequest, &gotBillingUnit, &gotPriority,
		&gotThreshold, &gotLongInput, &gotLongOutput, &gotLongCacheRead, &gotLongCacheWrite,
		&gotEffectiveAt, &gotBaseline,
	)
	if err != nil {
		t.Fatalf("load backfilled channel price version: %v", err)
	}
	if gotPriceID != priceID || gotProvider != "codex" || gotModel != "gpt-version-baseline" || gotAuth != "oauth" || gotBrand != "codex" || gotKey != "oauth_pool" ||
		gotInput != 1.25 || gotOutput != 5 || gotCacheRead != 0.125 || gotCacheWrite != 0.25 || gotRequest != 0.0042 || gotBillingUnit != "token" || gotPriority != 1.5 ||
		gotThreshold != 200000 || gotLongInput != 2.5 || gotLongOutput != 10 || gotLongCacheRead != 0.25 || gotLongCacheWrite != 0.5 || gotEffectiveAt != updatedAt || !gotBaseline {
		t.Fatalf("backfilled channel price version is incomplete: id=%d provider/model/auth/brand/key=%q/%q/%q/%q/%q rates=%g/%g/%g/%g request=%g unit=%q priority=%g long=%d/%g/%g/%g/%g effective=%q baseline=%t",
			gotPriceID, gotProvider, gotModel, gotAuth, gotBrand, gotKey, gotInput, gotOutput, gotCacheRead, gotCacheWrite, gotRequest, gotBillingUnit, gotPriority, gotThreshold, gotLongInput, gotLongOutput, gotLongCacheRead, gotLongCacheWrite, gotEffectiveAt, gotBaseline)
	}
}

func TestRunMigrationsUserSuperAdminBootstrapsImportedUsersWithoutAdministrator(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202608090001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		INSERT INTO users (id, username, password_hash, password_salt, is_admin, nickname, disabled_at, created_at, updated_at)
		VALUES
			(1, 'imported-active', 'hash', 'salt', 0, 'Imported active', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(2, 'imported-disabled', NULL, NULL, 0, 'Imported disabled', '2026-09-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00')
	`)
	if err != nil {
		t.Fatalf("seed imported users without administrator: %v", err)
	}

	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade imported database without administrator failed: %v", err)
	}

	var isAdmin, isSuperAdmin bool
	var disabledAt sql.NullString
	if err := db.QueryRow(`SELECT is_admin, is_super_admin, disabled_at FROM users WHERE id = 1`).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
		t.Fatalf("query bootstrapped imported user: %v", err)
	}
	if !isAdmin || !isSuperAdmin || disabledAt.Valid {
		t.Fatalf("bootstrapped imported user = admin=%t super_admin=%t disabled_at=%q, want admin/super-admin enabled", isAdmin, isSuperAdmin, disabledAt.String)
	}
}

func TestRunMigrationsUserSuperAdminPromotesFirstImportedUser(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202608090001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		INSERT INTO users (id, username, password_hash, password_salt, is_admin, nickname, disabled_at, created_at, updated_at)
		VALUES
			(1, 'active-without-credentials', NULL, NULL, 0, 'Active user', NULL, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			(2, 'disabled-with-credentials', 'hash', 'salt', 0, 'Disabled user', '2026-09-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00')
	`)
	if err != nil {
		t.Fatalf("seed imported users with mixed credential availability: %v", err)
	}

	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatalf("upgrade imported database with mixed credential availability failed: %v", err)
	}

	var isAdmin, isSuperAdmin bool
	var disabledAt sql.NullString
	if err := db.QueryRow(`SELECT is_admin, is_super_admin, disabled_at FROM users WHERE id = 1`).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
		t.Fatalf("query first imported user: %v", err)
	}
	if !isAdmin || !isSuperAdmin || disabledAt.Valid {
		t.Fatalf("first imported user = admin=%t super_admin=%t disabled_at=%q, want admin/super-admin enabled", isAdmin, isSuperAdmin, disabledAt.String)
	}
	if err := db.QueryRow(`SELECT is_super_admin FROM users WHERE id = 2`).Scan(&isSuperAdmin); err != nil {
		t.Fatalf("query later imported user role: %v", err)
	}
	if isSuperAdmin {
		t.Fatal("migration promoted a later imported user over the first user")
	}
}

func TestUsageHistoryRangeQueriesUseCompositeIndexes(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	tests := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{
			name:      "failed range",
			query:     `SELECT * FROM usage_records WHERE failed = ? AND timestamp >= ? AND timestamp < ? ORDER BY timestamp ASC`,
			args:      []any{true, "2026-07-01", "2026-07-20"},
			wantIndex: "ix_usage_records_failed_timestamp",
		},
		{
			name:      "account range",
			query:     `SELECT * FROM usage_records WHERE usage_username = ? AND timestamp >= ? AND timestamp < ? ORDER BY timestamp ASC`,
			args:      []any{"member", "2026-07-01", "2026-07-20"},
			wantIndex: "ix_usage_records_usage_username_timestamp",
		},
		{
			name:      "analytics source range",
			query:     `SELECT * FROM usage_analytics_facts WHERE source_key = ? AND timestamp >= ? AND timestamp < ? ORDER BY timestamp DESC`,
			args:      []any{"source-key", "2026-07-01", "2026-07-20"},
			wantIndex: "ix_usage_analytics_facts_source_key_timestamp",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			details := testQueryPlanDetails(t, app.db, test.query, test.args...)
			plan := strings.Join(details, "\n")
			if !strings.Contains(plan, test.wantIndex) {
				t.Fatalf("query plan = %q, want index %s", plan, test.wantIndex)
			}
			if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("query plan uses a temporary sort: %q", plan)
			}
		})
	}
}

func TestRunMigrationsRepairsOldPythonSchemaWithoutOldCode(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbDir := filepath.Join(dataDir, "db")
	if err := ensureTestDir(dbDir); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dbDir, "cpa_helper.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	apiKey := "sk-old-test"
	apiKeyHash := hashAPIKey(apiKey)
	oldSQL := []string{
		`CREATE TABLE app_settings (
			id INTEGER PRIMARY KEY,
			account_username VARCHAR(120),
			account_password_hash VARCHAR(200),
			account_password_salt VARCHAR(64)
		)`,
		`CREATE TABLE usage_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME NOT NULL,
			timestamp DATETIME NOT NULL,
			api_key_hash VARCHAR(64) NOT NULL,
			api_key_masked VARCHAR(80) NOT NULL,
			provider VARCHAR(120),
			model VARCHAR(180),
			endpoint VARCHAR(240),
			source VARCHAR(120),
			request_id VARCHAR(240),
			auth VARCHAR(120),
			latency_ms REAL,
			failed BOOLEAN NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			dedupe_key VARCHAR(80) NOT NULL UNIQUE,
			raw_json TEXT NOT NULL
		)`,
		`CREATE TABLE model_prices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider VARCHAR(120) NOT NULL,
			model VARCHAR(180) NOT NULL,
			input_usd_per_million REAL NOT NULL,
			output_usd_per_million REAL NOT NULL,
			cached_usd_per_million REAL NOT NULL,
			reasoning_usd_per_million REAL NOT NULL,
			updated_at DATETIME NOT NULL,
			CONSTRAINT uq_model_prices_provider_model UNIQUE (provider, model)
		)`,
		`CREATE TABLE api_key_aliases (
			api_key_hash VARCHAR(64) PRIMARY KEY,
			alias VARCHAR(120) NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE collector_state (
			id INTEGER PRIMARY KEY,
			running BOOLEAN NOT NULL,
			last_poll_at DATETIME,
			last_success_at DATETIME,
			last_error TEXT,
			remote_enabled BOOLEAN,
			records_collected INTEGER NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE alembic_version (
			version_num VARCHAR(32) NOT NULL
		)`,
		`INSERT INTO app_settings (id, account_username, account_password_hash, account_password_salt)
			VALUES (1, 'legacy-admin', 'legacy-password-hash', 'legacy-password-salt')`,
		`INSERT INTO alembic_version (version_num) VALUES ('20260513_0001')`,
	}
	for _, statement := range oldSQL {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("create old schema: %v", err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO api_key_aliases (api_key_hash, alias, updated_at)
		VALUES (?, 'alice', '2026-05-04 00:00:00')
	`, apiKeyHash); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, api_key_hash, api_key_masked, provider, model,
			endpoint, source, request_id, auth, latency_ms, failed, input_tokens,
			output_tokens, cached_tokens, reasoning_tokens, total_tokens,
			dedupe_key, raw_json
		) VALUES (
			'2026-05-04 00:00:00', '2026-05-04 00:00:00', ?, 'sk...test',
			'openai', 'gpt-test', '/v1/chat/completions', 'queue', 'req-1',
			'bearer', 12.5, 0, 10, 20, 0, 0, 30, 'dedupe-1', ?
		)
	`, apiKeyHash, `{"api_key":"`+apiKey+`","auth":"bearer","reasoning_effort":"xhigh","ttft_ms":710,"service_tier":"priority","tokens":{"cache_read_tokens":7,"cache_creation_tokens":8}}`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, api_key_hash, api_key_masked, provider, model,
			endpoint, source, request_id, auth, latency_ms, failed, input_tokens,
			output_tokens, cached_tokens, reasoning_tokens, total_tokens,
			dedupe_key, raw_json
		) VALUES (
			'2026-05-04 00:01:00', '2026-05-04 00:01:00', ?, 'sk...test',
			'openai', 'gpt-test', '/v1/chat/completions', 'queue', 'req-ttft-zero',
			'bearer', 12.5, 0, 10, 20, 0, 0, 30, 'dedupe-ttft-zero', ?
		)
	`, apiKeyHash, `{"api_key":"`+apiKey+`","auth":"bearer","reasoning_effort":"minimal","ttft_ms":0}`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cached_usd_per_million, reasoning_usd_per_million, updated_at
		) VALUES
			('openai', 'gpt-5.5', 1, 2, 0.5, 9, '2026-05-04 00:00:00'),
			('openai', 'gpt-5.4', 1e308, 0, 0, 0, '2026-05-04 00:00:00'),
			('openai', 'gpt-5.6-terra', 2.5, 15, 0.25, 0, '2026-05-04 00:00:00'),
			('openai', 'gpt-5.6-terra-preview', 2.5, 15, 0.25, 0, '2026-05-04 00:00:00'),
			('gemini', 'gemini-2.5-pro', 1.25, 10, 0.125, 0, '2026-05-04 00:00:00')
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	var quotaChargeCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM user_quota_charges`).Scan(&quotaChargeCount); err != nil {
		t.Fatalf("query migrated quota charges: %v", err)
	}
	if quotaChargeCount != 0 {
		t.Fatalf("migrated quota charges = %d, want 0", quotaChargeCount)
	}

	if testColumnExists(t, app.db, "usage_records", "api_key_hash") {
		t.Fatal("old usage_records.api_key_hash should be removed")
	}
	if testTableExists(t, app.db, "api_key_aliases") {
		t.Fatal("old api_key_aliases table should be removed")
	}
	if testTableExists(t, app.db, "alembic_version") {
		t.Fatal("old alembic_version table should be removed")
	}
	if !testColumnExists(t, app.db, "codex_keeper_auth_states", "auth_index") {
		t.Fatal("old schema migration did not create codex_keeper_auth_states.auth_index")
	}

	var username, storedAPIKey, usageUsername string
	if err := app.db.QueryRow(`SELECT username FROM users WHERE username = 'alice'`).Scan(&username); err != nil {
		t.Fatalf("migrated user not found: %v", err)
	}
	if err := app.db.QueryRow(`SELECT api_key FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash).Scan(&storedAPIKey); err != nil {
		t.Fatalf("migrated api key binding not found: %v", err)
	}
	if storedAPIKey != apiKey {
		t.Fatalf("stored api key = %q, want %q", storedAPIKey, apiKey)
	}
	if err := app.db.QueryRow(`SELECT usage_username FROM usage_records WHERE dedupe_key = 'dedupe-1'`).Scan(&usageUsername); err != nil {
		t.Fatalf("migrated usage record not found: %v", err)
	}
	if usageUsername != username {
		t.Fatalf("usage username = %q, want %q", usageUsername, username)
	}
	var timestamp string
	if err := app.db.QueryRow(`SELECT timestamp FROM usage_records WHERE dedupe_key = 'dedupe-1'`).Scan(&timestamp); err != nil {
		t.Fatalf("migrated usage timestamp not found: %v", err)
	}
	if timestamp != "2026-05-04T00:00:00+08:00" {
		t.Fatalf("migrated timestamp = %q, want Beijing offset timestamp", timestamp)
	}
	var cacheReadTokens, cacheCreationTokens int
	if err := app.db.QueryRow(`SELECT cache_read_tokens, cache_creation_tokens FROM usage_records WHERE dedupe_key = 'dedupe-1'`).Scan(&cacheReadTokens, &cacheCreationTokens); err != nil {
		t.Fatalf("migrated usage cache tokens not found: %v", err)
	}
	if cacheReadTokens != 7 || cacheCreationTokens != 8 {
		t.Fatalf("migrated cache tokens = read %d creation %d, want 7 and 8", cacheReadTokens, cacheCreationTokens)
	}
	var reasoningEffort string
	var serviceTier sql.NullString
	var ttftMS sql.NullFloat64
	if err := app.db.QueryRow(`SELECT reasoning_effort, ttft_ms, service_tier FROM usage_records WHERE dedupe_key = 'dedupe-1'`).Scan(&reasoningEffort, &ttftMS, &serviceTier); err != nil {
		t.Fatalf("migrated reasoning/ttft not found: %v", err)
	}
	if reasoningEffort != "xhigh" || !ttftMS.Valid || ttftMS.Float64 != 710 || !serviceTier.Valid || serviceTier.String != "priority" {
		t.Fatalf("migrated reasoning/ttft/tier = %q/%v/%#v, want xhigh/710/priority", reasoningEffort, ttftMS, serviceTier)
	}
	if err := app.db.QueryRow(`SELECT ttft_ms FROM usage_records WHERE dedupe_key = 'dedupe-ttft-zero'`).Scan(&ttftMS); err != nil {
		t.Fatalf("migrated zero ttft record not found: %v", err)
	}
	if ttftMS.Valid {
		t.Fatalf("migrated zero ttft = %v, want NULL", ttftMS.Float64)
	}
	if testColumnExists(t, app.db, "model_prices", "cached_usd_per_million") {
		t.Fatal("old model_prices.cached_usd_per_million should be removed")
	}
	if testColumnExists(t, app.db, "model_prices", "reasoning_usd_per_million") {
		t.Fatal("old model_prices.reasoning_usd_per_million should be removed")
	}
	var cacheReadPrice, cacheCreationPrice float64
	if err := app.db.QueryRow(`SELECT cache_read_usd_per_million, cache_creation_usd_per_million FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.5'`).Scan(&cacheReadPrice, &cacheCreationPrice); err != nil {
		t.Fatalf("migrated model price not found: %v", err)
	}
	if cacheReadPrice != 0.5 || cacheCreationPrice != 0 {
		t.Fatalf("migrated cache prices = read %v creation %v, want 0.5 and 0", cacheReadPrice, cacheCreationPrice)
	}
	var requestUSD sql.NullFloat64
	var priorityMultiplier sql.NullFloat64
	if err := app.db.QueryRow(`SELECT request_usd, priority_multiplier FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.5'`).Scan(&requestUSD, &priorityMultiplier); err != nil {
		t.Fatalf("migrated request price/multiplier not found: %v", err)
	}
	if requestUSD.Valid {
		t.Fatalf("migrated request_usd = %v, want NULL", requestUSD.Float64)
	}
	if !priorityMultiplier.Valid || priorityMultiplier.Float64 != 2.5 {
		t.Fatalf("migrated priority_multiplier = %v, want 2.5", priorityMultiplier)
	}
	if err := app.db.QueryRow(`SELECT priority_multiplier FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.4'`).Scan(&priorityMultiplier); err != nil {
		t.Fatalf("unsafe migrated priority multiplier not found: %v", err)
	}
	if priorityMultiplier.Valid {
		t.Fatalf("unsafe migrated priority_multiplier = %v, want NULL", priorityMultiplier.Float64)
	}
	var threshold sql.NullInt64
	var longInput, longOutput, longCacheRead, longCacheCreation sql.NullFloat64
	if err := app.db.QueryRow(`
		SELECT long_context_threshold_tokens, long_context_input_usd_per_million,
		       long_context_output_usd_per_million, long_context_cache_read_usd_per_million,
		       long_context_cache_creation_usd_per_million
		FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.6-terra'
	`).Scan(&threshold, &longInput, &longOutput, &longCacheRead, &longCacheCreation); err != nil {
		t.Fatalf("migrated long-context price not found: %v", err)
	}
	if !threshold.Valid || threshold.Int64 != 272000 || !longInput.Valid || longInput.Float64 != 5 ||
		!longOutput.Valid || longOutput.Float64 != 22.5 || !longCacheRead.Valid || longCacheRead.Float64 != 0.5 ||
		!longCacheCreation.Valid || longCacheCreation.Float64 != 6.25 {
		t.Fatalf("migrated OpenAI long-context price = %#v/%#v/%#v/%#v/%#v", threshold, longInput, longOutput, longCacheRead, longCacheCreation)
	}
	if err := app.db.QueryRow(`SELECT long_context_threshold_tokens FROM model_prices WHERE provider = 'openai' AND model = 'gpt-5.6-terra-preview'`).Scan(&threshold); err != nil {
		t.Fatalf("prefix model long-context lookup failed: %v", err)
	}
	if threshold.Valid {
		t.Fatalf("prefix model long-context threshold = %d, want NULL", threshold.Int64)
	}
	if err := app.db.QueryRow(`SELECT long_context_threshold_tokens, long_context_input_usd_per_million, long_context_output_usd_per_million FROM model_prices WHERE provider = 'gemini' AND model = 'gemini-2.5-pro'`).Scan(&threshold, &longInput, &longOutput); err != nil {
		t.Fatalf("migrated Gemini long-context price not found: %v", err)
	}
	if !threshold.Valid || threshold.Int64 != 200000 || !longInput.Valid || longInput.Float64 != 2.5 || !longOutput.Valid || longOutput.Float64 != 15 {
		t.Fatalf("migrated Gemini long-context price = %#v/%#v/%#v", threshold, longInput, longOutput)
	}
}

func TestRunMigrationsUpgradesPreviousForkVersionWithKeeperAuthIndex(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	prepareMigrationTestDatabase(t, dataDir, 202607150001)

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	if !testColumnExists(t, app.db, "codex_keeper_auth_states", "auth_index") {
		t.Fatal("codex_keeper_auth_states.auth_index was not created during upgrade")
	}
	var indexCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'ix_codex_keeper_auth_states_auth_index'`).Scan(&indexCount); err != nil {
		t.Fatalf("query auth index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("auth index count = %d, want 1", indexCount)
	}
}

func TestRunMigrationsAcceptsPreexistingKeeperAuthIndexColumn(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607150001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`ALTER TABLE codex_keeper_auth_states ADD COLUMN auth_index VARCHAR(500)`); err != nil {
		_ = db.Close()
		t.Fatalf("add preexisting auth_index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	var indexCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'ix_codex_keeper_auth_states_auth_index'`).Scan(&indexCount); err != nil {
		t.Fatalf("query auth index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("auth index count = %d, want 1", indexCount)
	}
}

func TestRunMigrationsUpgradesModelMonitorProxySettingsIndependently(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607200001)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`UPDATE app_settings SET litellm_proxy_enabled = 1, litellm_proxy_url = 'http://legacy-proxy.local:7890' WHERE id = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy LiteLLM proxy settings: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	if !testColumnExists(t, app.db, "app_settings", "model_monitor_proxy_enabled") || !testColumnExists(t, app.db, "app_settings", "model_monitor_proxy_url") {
		t.Fatal("model monitor proxy columns were not created during upgrade")
	}
	cfg, err := app.loadModelMonitorProxyConfig(context.Background())
	if err != nil {
		t.Fatalf("loadModelMonitorProxyConfig failed: %v", err)
	}
	if cfg.Enabled || cfg.ProxyURL != "" {
		t.Fatalf("model monitor proxy inherited legacy LiteLLM settings: %#v", cfg)
	}
}

func TestRunMigrationsUpgradesModelMonitorSourceSettings(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	prepareMigrationTestDatabase(t, dataDir, 202607310010)

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()
	if !testColumnExists(t, app.db, "app_settings", "model_monitor_enabled_source_ids") {
		t.Fatal("model monitor source settings column was not created during upgrade")
	}
	settings, err := app.loadModelMonitorSourceSettings(context.Background())
	if err != nil {
		t.Fatalf("loadModelMonitorSourceSettings failed: %v", err)
	}
	want := []string{"ai-input-im", "openai", "anthropic", "deepseek"}
	if !equalStrings(settings.EnabledSourceIDs, want) {
		t.Fatalf("migrated enabled source IDs = %#v, want %#v", settings.EnabledSourceIDs, want)
	}
}

func TestRunMigrationsRepairsUsageAnalyticsSourceMetadataFromRetainedRawPayload(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310003)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := dbTime(time.Now().In(appTimeLocation))
	source := "opaque-migration-source"
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		_ = db.Close()
		t.Fatal("usage source key is nil")
	}
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, source, request_id, auth,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'migration-request', 'oauth',
			0, 1, 1, 0, 0, 0, 0, 2, 'migration-source-metadata', ?)
	`, now, now, source, `{"accountEmail":"Migration-Alias@Example.com","auth_type":"bearer"}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pre-upgrade usage record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint, source_key, auth,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'oauth',
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, now, *sourceKey); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade usage fact: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_source_catalog (source_key, source, auth, auth_conflict, updated_at)
		VALUES (?, ?, 'oauth', 0, ?)
	`, *sourceKey, source, now); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade source catalog: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-upgrade database: %v", err)
	}

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate retained usage metadata: %v", err)
	}
	defer app.Close()

	var recordSourceAccount, factSourceAccount, factAuth, catalogSourceAccount string
	var authConflict bool
	if err := app.db.QueryRow(`SELECT source_account FROM usage_records WHERE id = ?`, recordID).Scan(&recordSourceAccount); err != nil {
		t.Fatalf("query migrated usage record source account: %v", err)
	}
	if err := app.db.QueryRow(`SELECT source_account, auth FROM usage_analytics_facts WHERE usage_record_id = ?`, recordID).Scan(&factSourceAccount, &factAuth); err != nil {
		t.Fatalf("query migrated usage fact metadata: %v", err)
	}
	if err := app.db.QueryRow(`SELECT source_account, auth_conflict FROM usage_source_catalog WHERE source_key = ?`, *sourceKey).Scan(&catalogSourceAccount, &authConflict); err != nil {
		t.Fatalf("query migrated source catalog metadata: %v", err)
	}
	for _, value := range []string{recordSourceAccount, factSourceAccount, catalogSourceAccount} {
		if value != "migration-alias@example.com" {
			t.Fatalf("migrated source account = %q, want normalized retained raw alias", value)
		}
	}
	if factAuth != "bearer" {
		t.Fatalf("migrated fact auth = %q, want retained raw auth_type", factAuth)
	}
	if !authConflict {
		t.Fatal("migrated source catalog auth_conflict = false, want stored/raw ambiguity retained")
	}
	if err := app.ensureUsageAnalyticsFacts(context.Background()); err != nil {
		t.Fatalf("reconcile recovered usage metadata fact: %v", err)
	}
	var pending int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query migrated pending fact marker: %v", err)
	}
	if pending != 0 {
		t.Fatalf("migrated pending fact marker = %d, want 0", pending)
	}
}

func TestRunMigrationsUpgradesRawPayloadPruneTriggerAndRetainsCorrectedMetadata(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310008)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := dbTime(time.Now().In(appTimeLocation))
	initialSource := "raw-payload-initial-source"
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', 'oauth', 0,
			1, 1, 0, 0, 0, 0, 2, 'raw-payload-trigger-metadata', ?)
	`, now, now, `{"source":"`+initialSource+`","accountEmail":"initial@example.com","auth_type":"oauth"}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pre-upgrade usage record id: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-upgrade database: %v", err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate raw-payload prune trigger: %v", err)
	}
	defer app.Close()
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("materialize initial raw-payload fact: %v", err)
	}

	correctedSource := "raw-payload-corrected-source"
	correctedRaw := `{"source":"` + correctedSource + `","accountEmail":"corrected@example.com","auth_type":"api_key","authIndex":"corrected-auth-index"}`
	if _, err := app.db.ExecContext(ctx, `UPDATE usage_records SET raw_json = ? WHERE id = ?`, correctedRaw, recordID); err != nil {
		t.Fatalf("correct raw payload only: %v", err)
	}
	var pending int
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query raw-payload pending marker: %v", err)
	}
	if pending != 1 {
		t.Fatalf("raw-only correction pending markers = %d, want 1", pending)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile corrected raw payload: %v", err)
	}

	correctedKey := usageSourceKey(&correctedSource)
	if correctedKey == nil {
		t.Fatal("corrected source key is nil")
	}
	var recordSource, recordAccount, recordAuth, recordAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_records WHERE id = ?
	`, recordID).Scan(&recordSource, &recordAccount, &recordAuth, &recordAuthIndex); err != nil {
		t.Fatalf("query corrected usage record metadata: %v", err)
	}
	if recordSource != correctedSource || recordAccount != "corrected@example.com" || recordAuth != "api_key" || recordAuthIndex != "corrected-auth-index" {
		t.Fatalf("corrected usage record metadata = %q/%q/%q/%q", recordSource, recordAccount, recordAuth, recordAuthIndex)
	}
	var factKey, factAccount, factAuth, factAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query corrected analytics fact metadata: %v", err)
	}
	if factKey != *correctedKey || factAccount != "corrected@example.com" || factAuth != "api_key" || factAuthIndex != "corrected-auth-index" {
		t.Fatalf("corrected analytics fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}
	var catalogSource, catalogAccount, catalogAuthIndex string
	var authConflict bool
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth_index, auth_conflict
		FROM usage_source_catalog WHERE source_key = ?
	`, *correctedKey).Scan(&catalogSource, &catalogAccount, &catalogAuthIndex, &authConflict); err != nil {
		t.Fatalf("query corrected source catalog metadata: %v", err)
	}
	if catalogSource != correctedSource || catalogAccount != "corrected@example.com" || catalogAuthIndex != "corrected-auth-index" || !authConflict {
		t.Fatalf("corrected source catalog metadata = %q/%q/%q/conflict=%v", catalogSource, catalogAccount, catalogAuthIndex, authConflict)
	}

	if _, err := app.db.ExecContext(ctx, `UPDATE usage_records SET raw_json = '' WHERE id = ?`, recordID); err != nil {
		t.Fatalf("clear corrected raw payload for retention: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query retention raw-payload pending marker: %v", err)
	}
	if pending != 0 {
		t.Fatalf("retention raw-payload clear pending markers = %d, want 0", pending)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("verify pruned raw payload facts: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query retained analytics fact metadata: %v", err)
	}
	if factKey != *correctedKey || factAccount != "corrected@example.com" || factAuth != "api_key" || factAuthIndex != "corrected-auth-index" {
		t.Fatalf("retained analytics fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}

	if _, err := app.db.ExecContext(ctx, `
		UPDATE usage_records
		SET input_tokens = 3, total_tokens = 4
		WHERE id = ?
	`, recordID); err != nil {
		t.Fatalf("correct scalar fields after raw payload prune: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query scalar-correction pending marker: %v", err)
	}
	if pending != 1 {
		t.Fatalf("post-prune scalar correction pending markers = %d, want 1", pending)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile post-prune scalar correction: %v", err)
	}
	var factInputTokens, factTotalTokens int
	if err := app.db.QueryRowContext(ctx, `
		SELECT input_tokens, total_tokens
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factInputTokens, &factTotalTokens); err != nil {
		t.Fatalf("query post-prune corrected analytics fact: %v", err)
	}
	if factInputTokens != 3 || factTotalTokens != 4 {
		t.Fatalf("post-prune corrected analytics fact tokens = %d/%d, want 3/4", factInputTokens, factTotalTokens)
	}
}

func TestRunMigrationsPersistsRetainedMetadataBeforeRawPrune(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310005)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	source := "opaque-retained-metadata-source"
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		_ = db.Close()
		t.Fatal("usage source key is nil")
	}
	account := "retained-alias@example.com"
	rawJSON := `{"source":"` + source + `","accountEmail":"Retained-Alias@Example.com","auth_type":"api_key","authIndex":"retained-auth-index"}`
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, source_account, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 0,
			1, 1, 0, 0, 0, 0, 2, 'retained-metadata-before-prune', ?)
	`, dbTime(now), dbTime(timestamp), account, rawJSON)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade retained metadata record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read retained metadata record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint, source_key, auth, source_account,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'api_key', ?,
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, dbTime(timestamp), *sourceKey, account); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade retained metadata fact: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_source_catalog (source_key, source, auth, auth_conflict, source_account, updated_at)
		VALUES (?, ?, 'api_key', 0, ?, ?)
	`, *sourceKey, source, account, dbTime(now)); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade retained metadata catalog: %v", err)
	}
	// This models 202607310004 completing the existing fact repair before the
	// 202607310005 trigger began queueing raw-payload writes.
	if _, err := db.Exec(`DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID); err != nil {
		_ = db.Close()
		t.Fatalf("clear pre-upgrade pending marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-upgrade database: %v", err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate retained metadata before prune: %v", err)
	}
	defer app.Close()

	var recordSource, recordAccount, recordAuth, recordAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_records WHERE id = ?
	`, recordID).Scan(&recordSource, &recordAccount, &recordAuth, &recordAuthIndex); err != nil {
		t.Fatalf("query persisted retained record metadata: %v", err)
	}
	if recordSource != source || recordAccount != account || recordAuth != "api_key" || recordAuthIndex != "retained-auth-index" {
		t.Fatalf("persisted retained record metadata = %q/%q/%q/%q", recordSource, recordAccount, recordAuth, recordAuthIndex)
	}

	var factKey, factAccount, factAuth, factAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query repaired retained fact metadata: %v", err)
	}
	if factKey != *sourceKey || factAccount != account || factAuth != "api_key" || factAuthIndex != "retained-auth-index" {
		t.Fatalf("repaired retained fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}
	var catalogAccount, catalogAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_account, auth_index FROM usage_source_catalog WHERE source_key = ?
	`, *sourceKey).Scan(&catalogAccount, &catalogAuthIndex); err != nil {
		t.Fatalf("query repaired retained catalog metadata: %v", err)
	}
	if catalogAccount != account || catalogAuthIndex != "retained-auth-index" {
		t.Fatalf("repaired retained catalog metadata = %q/%q", catalogAccount, catalogAuthIndex)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile recovered retained metadata fact: %v", err)
	}
	pending, err := app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("query migration-created pending fact: %v", err)
	}
	if pending {
		t.Fatal("forward metadata repair left its own pending marker behind")
	}

	pruned, err := app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune retained raw payload: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned retained raw payloads = %d, want 1", pruned)
	}
	pending, err = app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("query pending fact after prune: %v", err)
	}
	if pending {
		t.Fatal("raw payload prune queued a fact whose metadata was already persisted")
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("verify fact after retained raw payload prune: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query fact after retained raw payload prune: %v", err)
	}
	if factKey != *sourceKey || factAccount != account || factAuth != "api_key" || factAuthIndex != "retained-auth-index" {
		t.Fatalf("fact after retained raw payload prune = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}
}

func TestRunMigrationsRetentionMetadataPreservesExistingPendingFact(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310005)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := dbTime(time.Now().In(appTimeLocation))
	source := "pending-retention-metadata-source"
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		_ = db.Close()
		t.Fatal("usage source key is nil")
	}
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'retention-metadata-pending', ?)
	`, now, now, `{"source":"`+source+`","accountEmail":"pending@example.com","auth_type":"api_key","authIndex":"pending-index"}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pending pre-upgrade usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pending pre-upgrade usage record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint, source_key, auth, source_account,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'api_key', 'pending@example.com',
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, now, *sourceKey); err != nil {
		_ = db.Close()
		t.Fatalf("insert pending pre-upgrade usage fact: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pending pre-upgrade database: %v", err)
	}

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate pending retained metadata: %v", err)
	}
	defer app.Close()
	pending, err := app.usageAnalyticsFactsPending(context.Background())
	if err != nil {
		t.Fatalf("query preserved pending fact: %v", err)
	}
	if !pending {
		t.Fatal("forward metadata repair consumed a pre-existing pending fact")
	}

	var recordSource, recordAuth, recordAuthIndex string
	if err := app.db.QueryRow(`SELECT source, auth, auth_index FROM usage_records WHERE id = ?`, recordID).Scan(&recordSource, &recordAuth, &recordAuthIndex); err != nil {
		t.Fatalf("query repaired pending record metadata: %v", err)
	}
	if recordSource != source || recordAuth != "api_key" || recordAuthIndex != "pending-index" {
		t.Fatalf("repaired pending record metadata = %q/%q/%q", recordSource, recordAuth, recordAuthIndex)
	}
}

func TestMigrateRecoversPreexistingPendingFactFrom202607310003(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	prepareMigrationTestDatabase(t, dataDir, 202607310003)

	ctx := context.Background()
	legacyApp, err := NewWithOptions(ctx, NewOptions{Migrate: false, StartBackground: false})
	if err != nil {
		t.Fatalf("open 202607310003 database: %v", err)
	}
	now := dbTime(time.Now().In(appTimeLocation))
	initialProvider := "legacy-provider"
	correctedProvider := "corrected-provider"
	source := "preexisting-pending-recovery-source"
	result, err := legacyApp.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, source, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, ?, 'gpt-test', '/v1/responses', ?, 'oauth', 0,
			2, 3, 0, 0, 0, 0, 5, 'preexisting-pending-recovery', '{}')
	`, now, now, initialProvider, source)
	if err != nil {
		legacyApp.Close()
		t.Fatalf("insert 202607310003 usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		legacyApp.Close()
		t.Fatalf("read 202607310003 usage record id: %v", err)
	}
	if err := legacyApp.ensureUsageAnalyticsFacts(ctx); err != nil {
		legacyApp.Close()
		t.Fatalf("materialize initial usage fact: %v", err)
	}
	pricing, err := legacyApp.billingPriceIndex(ctx)
	if err != nil {
		legacyApp.Close()
		t.Fatalf("build initial pricing index: %v", err)
	}
	if err := legacyApp.rebuildUsageAnalyticsHourly(ctx, pricing); err != nil {
		legacyApp.Close()
		t.Fatalf("build initial hourly baseline: %v", err)
	}
	var baselineFactsVersion, baselineHourlyFactsVersion int64
	var baselineNeedsRebuild bool
	if err := legacyApp.db.QueryRowContext(ctx, `
		SELECT facts_version, hourly_facts_version, hourly_needs_rebuild
		FROM usage_analytics_state WHERE id = 1
	`).Scan(&baselineFactsVersion, &baselineHourlyFactsVersion, &baselineNeedsRebuild); err != nil {
		legacyApp.Close()
		t.Fatalf("query initial hourly state: %v", err)
	}
	if baselineFactsVersion != baselineHourlyFactsVersion || baselineNeedsRebuild {
		legacyApp.Close()
		t.Fatalf("initial hourly state = facts=%d hourly=%d needs_rebuild=%v", baselineFactsVersion, baselineHourlyFactsVersion, baselineNeedsRebuild)
	}

	if _, err := legacyApp.db.ExecContext(ctx, `
		UPDATE usage_records
		SET provider = ?, input_tokens = ?, output_tokens = ?, total_tokens = ?
		WHERE id = ?
	`, correctedProvider, 17, 11, 28, recordID); err != nil {
		legacyApp.Close()
		t.Fatalf("apply direct 202607310003 correction: %v", err)
	}
	var pending int
	if err := legacyApp.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?
	`, recordID).Scan(&pending); err != nil {
		legacyApp.Close()
		t.Fatalf("query pre-upgrade pending fact: %v", err)
	}
	if pending != 1 {
		legacyApp.Close()
		t.Fatalf("pre-upgrade pending markers = %d, want 1", pending)
	}
	legacyApp.Close()

	report, err := Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate pre-existing pending fact database: %v", err)
	}
	if report.CurrentVersion != backendMigrations.LatestVersion {
		t.Fatalf("migrated version = %d, want %d", report.CurrentVersion, backendMigrations.LatestVersion)
	}

	app, err := NewWithOptions(ctx, NewOptions{RequireReady: true, StartBackground: false})
	if err != nil {
		t.Fatalf("open migrated read-only service profile: %v", err)
	}
	defer app.Close()
	if err := app.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?
	`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query reconciled pending fact: %v", err)
	}
	if pending != 0 {
		t.Fatalf("migrate left pending markers = %d, want 0", pending)
	}
	var staleProvider string
	var staleInputTokens int
	if err := app.db.QueryRowContext(ctx, `
		SELECT provider, input_tokens FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&staleProvider, &staleInputTokens); err != nil {
		t.Fatalf("query reconciled fact: %v", err)
	}
	if staleProvider != correctedProvider || staleInputTokens != 17 {
		t.Fatalf("migrated fact = provider %q input_tokens %d, want %q/17", staleProvider, staleInputTokens, correctedProvider)
	}
	var reconciledFactsVersion, reconciledHourlyFactsVersion int64
	var reconciledNeedsRebuild bool
	if err := app.db.QueryRowContext(ctx, `
		SELECT facts_version, hourly_facts_version, hourly_needs_rebuild
		FROM usage_analytics_state WHERE id = 1
	`).Scan(&reconciledFactsVersion, &reconciledHourlyFactsVersion, &reconciledNeedsRebuild); err != nil {
		t.Fatalf("query reconciled hourly state: %v", err)
	}
	if reconciledFactsVersion <= baselineFactsVersion || !reconciledNeedsRebuild {
		t.Fatalf("migrated hourly state = facts=%d hourly=%d needs_rebuild=%v", reconciledFactsVersion, reconciledHourlyFactsVersion, reconciledNeedsRebuild)
	}
	var factOutputTokens, factTotalTokens int
	if err := app.db.QueryRowContext(ctx, `
		SELECT provider, input_tokens, output_tokens, total_tokens
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&staleProvider, &staleInputTokens, &factOutputTokens, &factTotalTokens); err != nil {
		t.Fatalf("query migrated fact tokens: %v", err)
	}
	if staleProvider != correctedProvider || staleInputTokens != 17 || factOutputTokens != 11 || factTotalTokens != 28 {
		t.Fatalf("migrated fact = provider %q input/output/total %d/%d/%d", staleProvider, staleInputTokens, factOutputTokens, factTotalTokens)
	}

	pricing, err = app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("build reconciled pricing index: %v", err)
	}
	if err := app.rebuildUsageAnalyticsHourly(ctx, pricing); err != nil {
		t.Fatalf("rebuild corrected hourly baseline: %v", err)
	}
	var finalFactsVersion, finalHourlyFactsVersion int64
	var finalNeedsRebuild bool
	if err := app.db.QueryRowContext(ctx, `
		SELECT facts_version, hourly_facts_version, hourly_needs_rebuild
		FROM usage_analytics_state WHERE id = 1
	`).Scan(&finalFactsVersion, &finalHourlyFactsVersion, &finalNeedsRebuild); err != nil {
		t.Fatalf("query final hourly state: %v", err)
	}
	if finalFactsVersion != finalHourlyFactsVersion || finalNeedsRebuild {
		t.Fatalf("final hourly state = facts=%d hourly=%d needs_rebuild=%v", finalFactsVersion, finalHourlyFactsVersion, finalNeedsRebuild)
	}
	var hourlyRecords, hourlyInputTokens, hourlyOutputTokens int
	if err := app.db.QueryRowContext(ctx, `
		SELECT record_count, input_tokens, output_tokens
		FROM usage_analytics_hourly WHERE provider = ?
	`, correctedProvider).Scan(&hourlyRecords, &hourlyInputTokens, &hourlyOutputTokens); err != nil {
		t.Fatalf("query corrected hourly row: %v", err)
	}
	if hourlyRecords != 1 || hourlyInputTokens != 17 || hourlyOutputTokens != 11 {
		t.Fatalf("corrected hourly row = records/input/output %d/%d/%d", hourlyRecords, hourlyInputTokens, hourlyOutputTokens)
	}
}

func TestRunMigrationsPendingRecoveryPreservesPrunedFactMetadata(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310006)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := dbTime(time.Now().In(appTimeLocation))
	source := "pruned-pending-recovery-source"
	sourceKey := usageSourceKey(&source)
	if sourceKey == nil {
		_ = db.Close()
		t.Fatal("usage source key is nil")
	}
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'pruned-pending-recovery', '')
	`, now, now)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pruned legacy usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pruned legacy usage record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint, source_key, auth, auth_index, source_account,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'oauth', 'pruned-auth-index', 'pruned@example.com',
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, now, *sourceKey); err != nil {
		_ = db.Close()
		t.Fatalf("insert pruned legacy usage fact: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_source_catalog (
			source_key, source, auth, auth_conflict, source_account, auth_index, updated_at
		) VALUES (?, ?, 'oauth', 0, 'pruned@example.com', 'pruned-auth-index', ?)
	`, *sourceKey, source, now); err != nil {
		_ = db.Close()
		t.Fatalf("insert pruned legacy source catalog: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID); err != nil {
		_ = db.Close()
		t.Fatalf("clear pre-recovery pending marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate pruned pending recovery database: %v", err)
	}
	defer app.Close()
	var pending int
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query pending recovery marker: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending recovery markers = %d, want 1", pending)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile pruned pending fact: %v", err)
	}

	var factKey, factAuth, factAuthIndex, factAccount string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, auth, auth_index, source_account
		FROM usage_analytics_facts
		WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAuth, &factAuthIndex, &factAccount); err != nil {
		t.Fatalf("query reconciled pruned fact metadata: %v", err)
	}
	if factKey != *sourceKey || factAuth != "oauth" || factAuthIndex != "pruned-auth-index" || factAccount != "pruned@example.com" {
		t.Fatalf("reconciled pruned fact metadata = %q/%q/%q/%q", factKey, factAuth, factAuthIndex, factAccount)
	}
	var catalogAuth, catalogAuthIndex, catalogAccount string
	if err := app.db.QueryRowContext(ctx, `
		SELECT auth, auth_index, source_account
		FROM usage_source_catalog
		WHERE source_key = ?
	`, *sourceKey).Scan(&catalogAuth, &catalogAuthIndex, &catalogAccount); err != nil {
		t.Fatalf("query retained source catalog metadata: %v", err)
	}
	if catalogAuth != "oauth" || catalogAuthIndex != "pruned-auth-index" || catalogAccount != "pruned@example.com" {
		t.Fatalf("retained source catalog metadata = %q/%q/%q", catalogAuth, catalogAuthIndex, catalogAccount)
	}
}

func TestRunMigrationsRestoresLegacyFactAuthFromCatalog(t *testing.T) {
	type legacyAuthCase struct {
		name        string
		malformed   bool
		conflicting bool
		recordID    int64
		source      string
		sourceKey   string
	}

	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310003)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := dbTime(time.Now().In(appTimeLocation))
	cases := []legacyAuthCase{
		{name: "missing"},
		{name: "malformed", malformed: true},
		{name: "conflicting", conflicting: true},
	}
	for index := range cases {
		testCase := &cases[index]
		testCase.source = "legacy-auth-" + testCase.name
		sourceKey := usageSourceKey(&testCase.source)
		if sourceKey == nil {
			_ = db.Close()
			t.Fatal("usage source key is nil")
		}
		testCase.sourceKey = *sourceKey
		rawJSON := `{"source":"` + testCase.source + `"}`
		if testCase.malformed {
			rawJSON = `{"source":"` + testCase.source + `","auth_type":{"unexpected":true}}`
		}
		result, err := db.Exec(`
			INSERT INTO usage_records (
				created_at, timestamp, provider, model, endpoint, failed,
				input_tokens, output_tokens, cached_tokens, cache_read_tokens,
				cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
			) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', 0,
				1, 1, 0, 0, 0, 0, 2, ?, ?)
		`, now, now, "legacy-auth-"+testCase.name, rawJSON)
		if err != nil {
			_ = db.Close()
			t.Fatalf("insert %s legacy auth record: %v", testCase.name, err)
		}
		recordID, err := result.LastInsertId()
		if err != nil {
			_ = db.Close()
			t.Fatalf("read %s legacy auth record id: %v", testCase.name, err)
		}
		testCase.recordID = recordID
		if _, err := db.Exec(`
			INSERT INTO usage_analytics_facts (
				usage_record_id, timestamp, provider, model, endpoint, source_key, auth,
				failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
				cache_creation_tokens, reasoning_tokens, total_tokens
			) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'oauth',
				0, 1, 1, 0, 0, 0, 0, 2)
		`, recordID, now, testCase.sourceKey); err != nil {
			_ = db.Close()
			t.Fatalf("insert %s legacy auth fact: %v", testCase.name, err)
		}
		if _, err := db.Exec(`
			INSERT INTO usage_source_catalog (source_key, source, auth, auth_conflict, updated_at)
			VALUES (?, ?, 'oauth', 0, ?)
		`, testCase.sourceKey, testCase.source, now); err != nil {
			_ = db.Close()
			t.Fatalf("insert %s legacy auth catalog: %v", testCase.name, err)
		}
		if testCase.conflicting {
			conflictResult, err := db.Exec(`
				INSERT INTO usage_records (
					created_at, timestamp, provider, model, endpoint, failed,
					input_tokens, output_tokens, cached_tokens, cache_read_tokens,
					cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
				) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', 0,
					1, 1, 0, 0, 0, 0, 2, 'legacy-auth-conflicting-sibling', ?)
			`, now, now, `{"source":"`+testCase.source+`","auth_type":"api_key"}`)
			if err != nil {
				_ = db.Close()
				t.Fatalf("insert conflicting auth sibling record: %v", err)
			}
			conflictRecordID, err := conflictResult.LastInsertId()
			if err != nil {
				_ = db.Close()
				t.Fatalf("read conflicting auth sibling record id: %v", err)
			}
			if _, err := db.Exec(`
				INSERT INTO usage_analytics_facts (
					usage_record_id, timestamp, provider, model, endpoint, source_key, auth,
					failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
					cache_creation_tokens, reasoning_tokens, total_tokens
				) VALUES (?, ?, 'codex', 'gpt-test', '/v1/responses', ?, 'api_key',
					0, 1, 1, 0, 0, 0, 0, 2)
			`, conflictRecordID, now, testCase.sourceKey); err != nil {
				_ = db.Close()
				t.Fatalf("insert conflicting auth sibling fact: %v", err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", 202607310007); err != nil {
		_ = db.Close()
		t.Fatalf("migrate legacy auth database to 202607310007: %v", err)
	}
	for _, testCase := range cases {
		var factAuth sql.NullString
		var catalogAuth string
		var authConflict bool
		if err := db.QueryRow(`SELECT auth FROM usage_analytics_facts WHERE usage_record_id = ?`, testCase.recordID).Scan(&factAuth); err != nil {
			_ = db.Close()
			t.Fatalf("query %s fact before auth recovery: %v", testCase.name, err)
		}
		if factAuth.Valid {
			_ = db.Close()
			t.Fatalf("legacy 202607310004 fact auth = %q, want NULL", factAuth.String)
		}
		if err := db.QueryRow(`SELECT auth, auth_conflict FROM usage_source_catalog WHERE source_key = ?`, testCase.sourceKey).Scan(&catalogAuth, &authConflict); err != nil {
			_ = db.Close()
			t.Fatalf("query %s catalog before auth recovery: %v", testCase.name, err)
		}
		if catalogAuth != "oauth" || !authConflict {
			_ = db.Close()
			t.Fatalf("legacy catalog before auth recovery = %q/conflict=%v", catalogAuth, authConflict)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate legacy auth database to current head: %v", err)
	}
	defer app.Close()
	assertRecovered := func(stage string) {
		t.Helper()
		for _, testCase := range cases {
			var factAuth sql.NullString
			var catalogAuth string
			var authConflict bool
			if err := app.db.QueryRowContext(ctx, `SELECT auth FROM usage_analytics_facts WHERE usage_record_id = ?`, testCase.recordID).Scan(&factAuth); err != nil {
				t.Fatalf("query %s fact %s: %v", testCase.name, stage, err)
			}
			if testCase.conflicting {
				if factAuth.Valid {
					t.Fatalf("conflicting fact auth %s = %q, want NULL", stage, factAuth.String)
				}
			} else if !factAuth.Valid || factAuth.String != "oauth" {
				t.Fatalf("recovered %s fact auth %s = %#v, want oauth", testCase.name, stage, factAuth)
			}
			if err := app.db.QueryRowContext(ctx, `SELECT auth, auth_conflict FROM usage_source_catalog WHERE source_key = ?`, testCase.sourceKey).Scan(&catalogAuth, &authConflict); err != nil {
				t.Fatalf("query %s catalog %s: %v", testCase.name, stage, err)
			}
			if catalogAuth != "oauth" || !authConflict {
				t.Fatalf("catalog %s = %q/conflict=%v", stage, catalogAuth, authConflict)
			}
		}
	}
	assertRecovered("after migration")
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile legacy auth facts: %v", err)
	}
	assertRecovered("after reconciliation")
}

func TestRunMigrationsRepairsRetainedOriginOnlyMetadataAndPreservesPendingFacts(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310009)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	const source = "migration-origin-only-source"
	const factAuth = "oauth"
	const factAuthIndex = "retained-fact-auth-index"
	const factSourceAccount = "retained-fact@example.com"
	staleSource := "stale-source"
	staleSourceKey := usageSourceKey(&staleSource)
	sourceKey := usageSourceKey(stringPtr(source))
	if staleSourceKey == nil || sourceKey == nil {
		_ = db.Close()
		t.Fatal("migration source key is nil")
	}
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-origin-migration', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'migration-origin-only', ?)
	`, dbTime(now), dbTime(timestamp), `{"event":{"Origin":"`+source+`"}}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade origin-only record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pre-upgrade origin-only record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint, source_key, auth, auth_index, source_account,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-origin-migration', '/v1/responses', ?, ?, ?, ?,
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, dbTime(timestamp), *staleSourceKey, factAuth, factAuthIndex, factSourceAccount); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade origin-only fact: %v", err)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		_ = db.Close()
		t.Fatalf("query pre-upgrade origin pending marker: %v", err)
	}
	if pending != 1 {
		_ = db.Close()
		t.Fatalf("pre-upgrade origin pending markers = %d, want 1", pending)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate origin-only metadata database: %v", err)
	}
	defer app.Close()
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query preserved origin pending marker: %v", err)
	}
	if pending != 1 {
		t.Fatalf("preserved origin pending markers = %d, want 1", pending)
	}

	var recordSource string
	if err := app.db.QueryRowContext(ctx, `SELECT source FROM usage_records WHERE id = ?`, recordID).Scan(&recordSource); err != nil {
		t.Fatalf("query migrated origin record source: %v", err)
	}
	if recordSource != source {
		t.Fatalf("migrated origin record source = %q, want %q", recordSource, source)
	}
	var migratedFactKey, migratedFactAuth, migratedFactAuthIndex, migratedFactAccount string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, auth, auth_index, source_account
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&migratedFactKey, &migratedFactAuth, &migratedFactAuthIndex, &migratedFactAccount); err != nil {
		t.Fatalf("query migrated origin fact metadata: %v", err)
	}
	if migratedFactKey != *sourceKey || migratedFactAuth != factAuth || migratedFactAuthIndex != factAuthIndex || migratedFactAccount != factSourceAccount {
		t.Fatalf("migrated origin fact metadata = %q/%q/%q/%q", migratedFactKey, migratedFactAuth, migratedFactAuthIndex, migratedFactAccount)
	}
	var catalogSource, catalogAccount, catalogAuthIndex string
	var catalogAuth sql.NullString
	var authConflict bool
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index, auth_conflict
		FROM usage_source_catalog WHERE source_key = ?
	`, *sourceKey).Scan(&catalogSource, &catalogAccount, &catalogAuth, &catalogAuthIndex, &authConflict); err != nil {
		t.Fatalf("query migrated origin source catalog: %v", err)
	}
	if catalogSource != source || catalogAccount != factSourceAccount || catalogAuth.Valid || catalogAuthIndex != factAuthIndex || !authConflict {
		t.Fatalf("migrated origin catalog = %q/%q/%#v/%q/conflict=%v", catalogSource, catalogAccount, catalogAuth, catalogAuthIndex, authConflict)
	}

	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile migrated origin pending fact: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query reconciled origin pending marker: %v", err)
	}
	if pending != 0 {
		t.Fatalf("reconciled origin pending markers = %d, want 0", pending)
	}
	pruned, err := app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune migrated origin raw payload: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned migrated origin payloads = %d, want 1", pruned)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("verify migrated origin fact after prune: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, auth, auth_index, source_account
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&migratedFactKey, &migratedFactAuth, &migratedFactAuthIndex, &migratedFactAccount); err != nil {
		t.Fatalf("query retained migrated origin fact metadata: %v", err)
	}
	if migratedFactKey != *sourceKey || migratedFactAuth != factAuth || migratedFactAuthIndex != factAuthIndex || migratedFactAccount != factSourceAccount {
		t.Fatalf("retained migrated origin fact metadata = %q/%q/%q/%q", migratedFactKey, migratedFactAuth, migratedFactAuthIndex, migratedFactAccount)
	}
}

func TestRunMigrationsRepairsRetainedNestedMetadataWithoutSource(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	dbPath := prepareMigrationTestDatabase(t, dataDir, 202607310009)

	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	result, err := db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-metadata-migration', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'migration-metadata-without-source', ?)
	`, dbTime(now), dbTime(timestamp), `{"event":{"AccountEmail":"No-Source@Example.com","Authentication":"api_key","AccountId":"no-source-auth-index"}}`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade nested metadata record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read pre-upgrade nested metadata record id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, provider, model, endpoint,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, 'codex', 'gpt-metadata-migration', '/v1/responses',
			0, 1, 1, 0, 0, 0, 0, 2)
	`, recordID, dbTime(timestamp)); err != nil {
		_ = db.Close()
		t.Fatalf("insert pre-upgrade nested metadata fact: %v", err)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		_ = db.Close()
		t.Fatalf("query pre-upgrade nested metadata pending marker: %v", err)
	}
	if pending != 1 {
		_ = db.Close()
		t.Fatalf("pre-upgrade nested metadata pending markers = %d, want 1", pending)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	app, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("migrate nested metadata database: %v", err)
	}
	defer app.Close()
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query preserved nested metadata pending marker: %v", err)
	}
	if pending != 1 {
		t.Fatalf("preserved nested metadata pending markers = %d, want 1", pending)
	}

	var recordSource sql.NullString
	var recordAccount, recordAuth, recordAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_records WHERE id = ?
	`, recordID).Scan(&recordSource, &recordAccount, &recordAuth, &recordAuthIndex); err != nil {
		t.Fatalf("query migrated nested metadata record: %v", err)
	}
	if recordSource.Valid || recordAccount != "no-source@example.com" || recordAuth != "api_key" || recordAuthIndex != "no-source-auth-index" {
		t.Fatalf("migrated nested metadata record = %#v/%q/%q/%q", recordSource, recordAccount, recordAuth, recordAuthIndex)
	}

	var factSourceKey sql.NullString
	var factAccount, factAuth, factAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factSourceKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query migrated nested metadata fact: %v", err)
	}
	if factSourceKey.Valid || factAccount != "no-source@example.com" || factAuth != "api_key" || factAuthIndex != "no-source-auth-index" {
		t.Fatalf("migrated nested metadata fact = %#v/%q/%q/%q", factSourceKey, factAccount, factAuth, factAuthIndex)
	}
	var catalogs int
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_source_catalog`).Scan(&catalogs); err != nil {
		t.Fatalf("count migrated nested metadata catalogs: %v", err)
	}
	if catalogs != 0 {
		t.Fatalf("migrated nested metadata catalogs = %d, want 0", catalogs)
	}

	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile migrated nested metadata pending fact: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query reconciled nested metadata pending marker: %v", err)
	}
	if pending != 0 {
		t.Fatalf("reconciled nested metadata pending markers = %d, want 0", pending)
	}
	pruned, err := app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune migrated nested metadata raw payload: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned migrated nested metadata payloads = %d, want 1", pruned)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("verify nested metadata fact after prune: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factSourceKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query retained nested metadata fact: %v", err)
	}
	if factSourceKey.Valid || factAccount != "no-source@example.com" || factAuth != "api_key" || factAuthIndex != "no-source-auth-index" {
		t.Fatalf("retained nested metadata fact = %#v/%q/%q/%q", factSourceKey, factAccount, factAuth, factAuthIndex)
	}
}

func prepareMigrationTestDatabase(t *testing.T, dataDir string, version int64) string {
	t.Helper()
	dbDir := filepath.Join(dataDir, "db")
	if err := ensureTestDir(dbDir); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "cpa_helper.sqlite3")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := goose.UpToContext(context.Background(), db, ".", version); err != nil {
		_ = db.Close()
		t.Fatalf("migrate test database to %d: %v", version, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func testTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	return err == nil
}

func testIndexExists(t *testing.T, db *sql.DB, index string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name)
	return err == nil
}

func testQueryPlanDetails(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return details
}

func testColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}

func ensureTestDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
