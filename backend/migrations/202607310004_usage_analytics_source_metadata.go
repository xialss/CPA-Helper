package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsMetadataRepairBatchSize = 500

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsSourceMetadata, nil)
}

// upUsageAnalyticsSourceMetadata repairs derived values that older analytics
// backfills could only infer from the source text. Retained raw payloads are
// the only authority for account aliases and stored/raw auth ambiguity.
func upUsageAnalyticsSourceMetadata(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err := repairUsageAnalyticsSourceMetadata(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET facts_version = facts_version + 1,
			hourly_needs_rebuild = 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`); err != nil {
		return err
	}
	return tx.Commit()
}

func repairUsageAnalyticsSourceMetadata(ctx context.Context, tx *sql.Tx) error {
	var lastID int64
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT records.id, records.source, records.source_account, records.auth, records.raw_json,
			       facts.usage_record_id, facts.source_key, facts.auth, facts.source_account
			FROM usage_records AS records
			LEFT JOIN usage_analytics_facts AS facts ON facts.usage_record_id = records.id
			WHERE records.id > ?
			ORDER BY records.id
			LIMIT ?
		`, lastID, usageAnalyticsMetadataRepairBatchSize)
		if err != nil {
			return err
		}

		batch := make([]usageAnalyticsMetadataRepairRow, 0, usageAnalyticsMetadataRepairBatchSize)
		for rows.Next() {
			var row usageAnalyticsMetadataRepairRow
			if err := rows.Scan(
				&row.id, &row.source, &row.sourceAccount, &row.auth, &row.rawJSON,
				&row.factRecordID, &row.factSourceKey, &row.factAuth, &row.factSourceAccount,
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
			if err := repairUsageAnalyticsSourceMetadataRow(ctx, tx, row); err != nil {
				return err
			}
			lastID = row.id
		}
	}
}

type usageAnalyticsMetadataRepairRow struct {
	id                int64
	source            sql.NullString
	sourceAccount     sql.NullString
	auth              sql.NullString
	rawJSON           sql.NullString
	factRecordID      sql.NullInt64
	factSourceKey     sql.NullString
	factAuth          sql.NullString
	factSourceAccount sql.NullString
}

func repairUsageAnalyticsSourceMetadataRow(ctx context.Context, tx *sql.Tx, row usageAnalyticsMetadataRepairRow) error {
	payload := usageAnalyticsMetadataPayload(row.rawJSON.String)
	source := migrationNonBlank(row.source)
	if source == "" {
		source = payload.source
	}
	rawSourceAccount := payload.sourceAccount
	sourceAccount := rawSourceAccount
	if sourceAccount == "" {
		sourceAccount = migrationNonBlank(row.sourceAccount)
	}
	if sourceAccount == "" {
		sourceAccount = migrationNonBlank(row.factSourceAccount)
	}
	if sourceAccount == "" {
		sourceAccount = migrationUsageSourceAccount(source)
	}
	auth, authConflict := usageAnalyticsMetadataAuth(
		migrationNonBlank(row.auth), payload.auth, row.rawJSON.String, migrationNonBlank(row.factAuth),
	)
	sourceKey := migrationUsageSourceKey(source)
	if sourceKey == "" {
		sourceKey = migrationNonBlank(row.factSourceKey)
	}

	if sourceAccount != "" {
		if rawSourceAccount != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE usage_records
				SET source_account = ?
				WHERE id = ? AND (source_account IS NULL OR source_account <> ?)
			`, sourceAccount, row.id, sourceAccount); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE usage_records
			SET source_account = ?
			WHERE id = ? AND (source_account IS NULL OR trim(source_account) = '')
		`, sourceAccount, row.id); err != nil {
			return err
		}
	}

	if !row.factRecordID.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_facts
		SET source_key = CASE WHEN ? <> '' THEN ? ELSE source_key END,
			auth = ?,
			source_account = CASE WHEN ? <> '' THEN ? ELSE source_account END
		WHERE usage_record_id = ?
	`, sourceKey, sourceKey, nullableMigrationOptionalText(auth), sourceAccount, sourceAccount, row.id); err != nil {
		return err
	}
	if sourceKey != "" && source != "" {
		if err := upsertUsageAnalyticsMetadataCatalog(ctx, tx, sourceKey, source, auth, sourceAccount, authConflict); err != nil {
			return err
		}
	}
	// Updating source_account on a legacy row fires the pending-fact trigger.
	// This migration has already repaired its existing fact atomically, so do
	// not leave the whole retained database for request-time reconciliation.
	_, err := tx.ExecContext(ctx, `DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, row.id)
	return err
}

type usageAnalyticsMetadataPayloadFields struct {
	source        string
	auth          string
	sourceAccount string
}

func usageAnalyticsMetadataPayload(rawJSON string) usageAnalyticsMetadataPayloadFields {
	if strings.TrimSpace(rawJSON) == "" {
		return usageAnalyticsMetadataPayloadFields{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return usageAnalyticsMetadataPayloadFields{}
	}
	return usageAnalyticsMetadataPayloadFields{
		source:        migrationJSONText(payload["source"]),
		auth:          migrationJSONText(payload["auth_type"]),
		sourceAccount: usageAnalyticsMetadataSourceAccount(payload),
	}
}

func usageAnalyticsMetadataSourceAccount(payload map[string]any) string {
	for _, field := range []string{"email", "account_email", "accountEmail", "user_email", "userEmail"} {
		if value := migrationJSONText(payload[field]); value != "" {
			return usageAnalyticsMetadataNormalizeSourceAccount(value)
		}
	}
	return ""
}

func usageAnalyticsMetadataNormalizeSourceAccount(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return ""
	}
	if match := usageAnalyticsMigrationEmailPattern.FindString(normalized); match != "" {
		return strings.ToLower(strings.TrimSpace(match))
	}
	return normalized
}

func usageAnalyticsMetadataAuth(storedAuth, rawAuth, rawJSON, factAuth string) (string, bool) {
	if rawAuth != "" {
		return rawAuth, storedAuth != "" && usageAnalyticsMetadataAuthKey(storedAuth) != usageAnalyticsMetadataAuthKey(rawAuth)
	}
	if storedAuth != "" {
		return storedAuth, true
	}
	if strings.TrimSpace(rawJSON) == "" && factAuth != "" {
		return factAuth, true
	}
	return "", true
}

func usageAnalyticsMetadataAuthKey(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "")
	return strings.ReplaceAll(normalized, "_", "")
}

func upsertUsageAnalyticsMetadataCatalog(ctx context.Context, tx *sql.Tx, sourceKey, source, auth, sourceAccount string, authConflict bool) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_source_catalog (
			source_key, source, auth, auth_conflict, source_account, updated_at
		) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(source_key) DO UPDATE SET
			auth_conflict = usage_source_catalog.auth_conflict OR excluded.auth_conflict OR (
				usage_source_catalog.auth IS NOT NULL AND excluded.auth IS NOT NULL AND
				lower(replace(replace(trim(usage_source_catalog.auth), '-', ''), '_', '')) <>
					lower(replace(replace(trim(excluded.auth), '-', ''), '_', ''))
			),
			auth = CASE
				WHEN usage_source_catalog.auth IS NULL OR trim(usage_source_catalog.auth) = '' THEN excluded.auth
				ELSE usage_source_catalog.auth
			END,
			source_account = COALESCE(usage_source_catalog.source_account, excluded.source_account),
			updated_at = excluded.updated_at
	`, sourceKey, source, nullableMigrationOptionalText(auth), authConflict, nullableMigrationOptionalText(sourceAccount))
	return err
}
