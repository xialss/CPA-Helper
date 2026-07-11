package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	backendMigrations "cpa-helper/backend/migrations"
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
	if !testColumnExists(t, app.db, "users", "quota_lifetime_usd") {
		t.Fatal("users.quota_lifetime_usd was not created")
	}
	if testColumnExists(t, app.db, "users", "quota_total_usd") {
		t.Fatal("old users.quota_total_usd should not exist")
	}
	if !testColumnExists(t, app.db, "users", "quota_monthly_usd") {
		t.Fatal("users.quota_monthly_usd was not created")
	}
	if !testTableExists(t, app.db, "user_quota_charges") {
		t.Fatal("user_quota_charges was not created")
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
	if testColumnExists(t, app.db, "user_quota_charges", "total_deducted_usd") {
		t.Fatal("old user_quota_charges.total_deducted_usd should not exist")
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

func testTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	return err == nil
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
