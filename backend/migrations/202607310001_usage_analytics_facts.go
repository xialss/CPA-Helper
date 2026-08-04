package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsBackfillBatchSize = 500

var usageAnalyticsMigrationEmailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsFacts, nil)
}

func upUsageAnalyticsFacts(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err := createUsageAnalyticsTables(ctx, tx); err != nil {
		return err
	}
	if err := backfillUsageAnalyticsFacts(ctx, tx); err != nil {
		return err
	}
	if err := verifyUsageAnalyticsFacts(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_analytics_pending_facts`); err != nil {
		return err
	}
	return tx.Commit()
}

func createUsageAnalyticsTables(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS usage_analytics_facts (
			usage_record_id INTEGER PRIMARY KEY,
			timestamp DATETIME NOT NULL,
			usage_username VARCHAR(120),
			api_key_description VARCHAR(240),
			provider VARCHAR(120),
			model VARCHAR(180),
			service_tier VARCHAR(80),
			endpoint VARCHAR(240),
			source_key VARCHAR(64),
			auth VARCHAR(120),
			auth_index VARCHAR(500),
			source_account VARCHAR(320),
			ttft_ms REAL,
			failed BOOLEAN NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(usage_record_id) REFERENCES usage_records(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS usage_source_catalog (
			source_key VARCHAR(64) PRIMARY KEY,
			source TEXT NOT NULL,
			auth VARCHAR(120),
			auth_conflict BOOLEAN NOT NULL DEFAULT 0,
			source_account VARCHAR(320),
			auth_index VARCHAR(500),
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_analytics_pending_facts (
			usage_record_id INTEGER PRIMARY KEY,
			FOREIGN KEY(usage_record_id) REFERENCES usage_records(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS usage_analytics_hourly (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hour_start DATETIME NOT NULL,
			usage_username VARCHAR(120),
			api_key_description VARCHAR(240),
			provider VARCHAR(120),
			model VARCHAR(180),
			endpoint VARCHAR(240),
			source_key VARCHAR(64),
			failed BOOLEAN NOT NULL DEFAULT 0,
			channel_auth_type VARCHAR(120),
			channel_brand VARCHAR(120),
			channel_key VARCHAR(320),
			channel_label VARCHAR(320),
			channel_label_fallback BOOLEAN NOT NULL DEFAULT 0,
			facts_version INTEGER NOT NULL,
			pricing_version INTEGER NOT NULL,
			selector_generation INTEGER NOT NULL,
			record_count INTEGER NOT NULL DEFAULT 0,
			failed_records INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			normal_input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			aggregate_input_tokens INTEGER NOT NULL DEFAULT 0,
			aggregate_total_tokens INTEGER NOT NULL DEFAULT 0,
			estimated_cost_usd REAL NOT NULL DEFAULT 0,
			unpriced_records INTEGER NOT NULL DEFAULT 0,
			ttft_total_ms REAL NOT NULL DEFAULT 0,
			ttft_count INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_analytics_state (
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
		)`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_model_prices_insert
		AFTER INSERT ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_model_prices_update
		AFTER UPDATE ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_model_prices_delete
		AFTER DELETE ON model_prices
		BEGIN
			UPDATE usage_analytics_state
			SET pricing_version = pricing_version + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = 1;
		END`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_pending_facts_insert
		AFTER INSERT ON usage_records
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_pending_facts_update
		AFTER UPDATE OF timestamp, usage_username, api_key_description, provider, model,
			service_tier, endpoint, source, source_account, auth, auth_index, ttft_ms,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens ON usage_records
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS usage_analytics_pending_facts_delete
		AFTER DELETE ON usage_records
		BEGIN
			DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = OLD.id;
		END`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_timestamp ON usage_analytics_facts(timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_usage_username_timestamp ON usage_analytics_facts(usage_username, timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_failed_timestamp ON usage_analytics_facts(failed, timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_source_key_timestamp ON usage_analytics_facts(source_key, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_provider_timestamp ON usage_analytics_facts(provider, timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_model_timestamp ON usage_analytics_facts(model, timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_facts_endpoint_timestamp ON usage_analytics_facts(endpoint, timestamp)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_hourly_versions_hour ON usage_analytics_hourly(facts_version, pricing_version, selector_generation, hour_start)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_analytics_hourly_filters_hour ON usage_analytics_hourly(hour_start, usage_username, api_key_description, provider, model, endpoint, source_key, failed)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_analytics_state (id, updated_at)
		VALUES (1, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO NOTHING
	`); err != nil {
		return err
	}
	return nil
}

func backfillUsageAnalyticsFacts(ctx context.Context, tx *sql.Tx) error {
	var lastID int64
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, timestamp, usage_username, api_key_description, provider, model,
			       service_tier, endpoint, source, source_account, auth, auth_index, ttft_ms,
			       failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			       cache_creation_tokens, reasoning_tokens, total_tokens, raw_json
			FROM usage_records
			WHERE id > ?
			ORDER BY id
			LIMIT ?
		`, lastID, usageAnalyticsBackfillBatchSize)
		if err != nil {
			return err
		}

		batch := make([]usageAnalyticsMigrationRow, 0, usageAnalyticsBackfillBatchSize)
		for rows.Next() {
			var row usageAnalyticsMigrationRow
			if err := rows.Scan(
				&row.id, &row.timestamp, &row.usageUsername, &row.apiKeyDescription, &row.provider, &row.model,
				&row.serviceTier, &row.endpoint, &row.source, &row.sourceAccount, &row.auth, &row.authIndex, &row.ttftMS,
				&row.failed, &row.inputTokens, &row.outputTokens, &row.cachedTokens, &row.cacheReadTokens,
				&row.cacheCreationTokens, &row.reasoningTokens, &row.totalTokens, &row.rawJSON,
			); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, row := range batch {
			if err := upsertUsageAnalyticsMigrationFact(ctx, tx, row); err != nil {
				return err
			}
			lastID = row.id
		}
	}
}

type usageAnalyticsMigrationRow struct {
	id                  int64
	timestamp           string
	usageUsername       sql.NullString
	apiKeyDescription   sql.NullString
	provider            sql.NullString
	model               sql.NullString
	serviceTier         sql.NullString
	endpoint            sql.NullString
	source              sql.NullString
	sourceAccount       sql.NullString
	auth                sql.NullString
	authIndex           sql.NullString
	ttftMS              sql.NullFloat64
	failed              bool
	inputTokens         int
	outputTokens        int
	cachedTokens        int
	cacheReadTokens     int
	cacheCreationTokens int
	reasoningTokens     int
	totalTokens         int
	rawJSON             string
}

func upsertUsageAnalyticsMigrationFact(ctx context.Context, tx *sql.Tx, row usageAnalyticsMigrationRow) error {
	payload := usageAnalyticsMigrationPayload(row.rawJSON)
	source := migrationNonBlank(row.source)
	if source == "" {
		source = payload.source
	}
	auth := payload.auth
	if auth == "" {
		auth = migrationNonBlank(row.auth)
	}
	authIndex := migrationNonBlank(row.authIndex)
	if authIndex == "" {
		authIndex = payload.authIndex
	}
	sourceAccount := migrationNonBlank(row.sourceAccount)
	if sourceAccount == "" {
		sourceAccount = migrationUsageSourceAccount(source)
	}
	sourceKey := migrationUsageSourceKey(source)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, usage_username, api_key_description, provider, model,
			service_tier, endpoint, source_key, auth, auth_index, source_account, ttft_ms,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(usage_record_id) DO UPDATE SET
			timestamp = excluded.timestamp,
			usage_username = excluded.usage_username,
			api_key_description = excluded.api_key_description,
			provider = excluded.provider,
			model = excluded.model,
			service_tier = excluded.service_tier,
			endpoint = excluded.endpoint,
			source_key = excluded.source_key,
			auth = excluded.auth,
			auth_index = excluded.auth_index,
			source_account = excluded.source_account,
			ttft_ms = excluded.ttft_ms,
			failed = excluded.failed,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cached_tokens = excluded.cached_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_creation_tokens = excluded.cache_creation_tokens,
			reasoning_tokens = excluded.reasoning_tokens,
			total_tokens = excluded.total_tokens
	`, row.id, row.timestamp, nullableMigrationText(row.usageUsername), nullableMigrationText(row.apiKeyDescription), nullableMigrationText(row.provider), nullableMigrationText(row.model),
		nullableMigrationText(row.serviceTier), nullableMigrationText(row.endpoint), nullableMigrationOptionalText(sourceKey), nullableMigrationOptionalText(auth), nullableMigrationOptionalText(authIndex), nullableMigrationOptionalText(sourceAccount), nullableMigrationNullFloat(row.ttftMS),
		row.failed, row.inputTokens, row.outputTokens, row.cachedTokens, row.cacheReadTokens, row.cacheCreationTokens, row.reasoningTokens, row.totalTokens); err != nil {
		return err
	}
	if sourceKey == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_source_catalog (source_key, source, auth, auth_conflict, source_account, auth_index, updated_at)
		VALUES (?, ?, ?, 0, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(source_key) DO UPDATE SET
			auth_conflict = usage_source_catalog.auth_conflict OR (
				usage_source_catalog.auth IS NOT NULL AND excluded.auth IS NOT NULL AND
				lower(trim(usage_source_catalog.auth)) <> lower(trim(excluded.auth))
			),
			auth = CASE
				WHEN usage_source_catalog.auth IS NULL OR trim(usage_source_catalog.auth) = '' THEN excluded.auth
				ELSE usage_source_catalog.auth
			END,
			source_account = COALESCE(usage_source_catalog.source_account, excluded.source_account),
			auth_index = COALESCE(usage_source_catalog.auth_index, excluded.auth_index),
			updated_at = excluded.updated_at
	`, sourceKey, source, nullableMigrationOptionalText(auth), nullableMigrationOptionalText(sourceAccount), nullableMigrationOptionalText(authIndex)); err != nil {
		return err
	}
	return nil
}

func verifyUsageAnalyticsFacts(ctx context.Context, tx *sql.Tx) error {
	var records, facts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_records`).Scan(&records); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_facts`).Scan(&facts); err != nil {
		return err
	}
	if records != facts {
		return fmt.Errorf("usage analytics fact backfill mismatch: usage_records=%d facts=%d", records, facts)
	}
	_, err := tx.ExecContext(ctx, `UPDATE usage_analytics_state SET facts_version = facts_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`)
	return err
}

type usageAnalyticsMigrationDecoded struct {
	source    string
	auth      string
	authIndex string
}

func usageAnalyticsMigrationPayload(rawJSON string) usageAnalyticsMigrationDecoded {
	if strings.TrimSpace(rawJSON) == "" {
		return usageAnalyticsMigrationDecoded{}
	}
	var payload map[string]any
	if json.Unmarshal([]byte(rawJSON), &payload) != nil {
		return usageAnalyticsMigrationDecoded{}
	}
	return usageAnalyticsMigrationDecoded{
		source:    migrationJSONText(payload["source"]),
		auth:      migrationJSONText(payload["auth_type"]),
		authIndex: migrationFirstJSONText(payload, "auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId"),
	}
}

func migrationJSONText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strings.TrimSpace(fmt.Sprint(typed))
	case bool:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}

func migrationFirstJSONText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := migrationJSONText(payload[key]); value != "" {
			return value
		}
	}
	return ""
}

func migrationUsageSourceKey(source string) string {
	normalized := strings.TrimSpace(source)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func migrationUsageSourceAccount(source string) string {
	match := usageAnalyticsMigrationEmailPattern.FindString(source)
	if match == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match))
}

func migrationNonBlank(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func nullableMigrationText(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}

func nullableMigrationOptionalText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableMigrationNullFloat(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}
