package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsRetentionMetadataRepairBatchSize = 500

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsRetentionMetadata, nil)
}

// upUsageAnalyticsRetentionMetadata is a forward repair for databases that
// already ran the source-metadata migration before raw-payload pruning began.
// It persists every retained raw metadata projection onto usage_records so a
// later prune-trigger reconciliation can rebuild the same compact fact.
func upUsageAnalyticsRetentionMetadata(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	factsChanged, err := repairUsageAnalyticsRetentionMetadata(ctx, tx)
	if err != nil {
		return err
	}
	if factsChanged {
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_analytics_state
			SET facts_version = facts_version + 1,
				hourly_needs_rebuild = 1,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = 1
		`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func repairUsageAnalyticsRetentionMetadata(ctx context.Context, tx *sql.Tx) (bool, error) {
	var factsChanged bool
	var lastID int64
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT records.id,
			       EXISTS(SELECT 1 FROM usage_analytics_pending_facts AS pending WHERE pending.usage_record_id = records.id),
			       records.source, records.source_account, records.auth, records.auth_index,
			       records.raw_json, facts.usage_record_id, facts.source_key, facts.auth,
			       facts.auth_index, facts.source_account
			FROM usage_records AS records
			LEFT JOIN usage_analytics_facts AS facts ON facts.usage_record_id = records.id
			WHERE records.id > ? AND trim(COALESCE(records.raw_json, '')) <> ''
			ORDER BY records.id
			LIMIT ?
		`, lastID, usageAnalyticsRetentionMetadataRepairBatchSize)
		if err != nil {
			return false, err
		}

		batch := make([]usageAnalyticsRetentionMetadataRow, 0, usageAnalyticsRetentionMetadataRepairBatchSize)
		for rows.Next() {
			var row usageAnalyticsRetentionMetadataRow
			if err := rows.Scan(
				&row.id, &row.pending, &row.source, &row.sourceAccount, &row.auth, &row.authIndex,
				&row.rawJSON, &row.factRecordID, &row.factSourceKey, &row.factAuth,
				&row.factAuthIndex, &row.factSourceAccount,
			); err != nil {
				_ = rows.Close()
				return false, err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, err
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
		if len(batch) == 0 {
			return factsChanged, nil
		}

		for _, row := range batch {
			changed, err := repairUsageAnalyticsRetentionMetadataRow(ctx, tx, row)
			if err != nil {
				return false, err
			}
			factsChanged = factsChanged || changed
			lastID = row.id
		}
	}
}

type usageAnalyticsRetentionMetadataRow struct {
	id                int64
	pending           bool
	source            sql.NullString
	sourceAccount     sql.NullString
	auth              sql.NullString
	authIndex         sql.NullString
	rawJSON           sql.NullString
	factRecordID      sql.NullInt64
	factSourceKey     sql.NullString
	factAuth          sql.NullString
	factAuthIndex     sql.NullString
	factSourceAccount sql.NullString
}

type usageAnalyticsRetentionMetadataPayload struct {
	source        string
	sourceAccount string
	auth          string
	authIndex     string
}

func repairUsageAnalyticsRetentionMetadataRow(ctx context.Context, tx *sql.Tx, row usageAnalyticsRetentionMetadataRow) (bool, error) {
	payload := usageAnalyticsRetentionMetadataPayloadFromRawJSON(row.rawJSON.String)
	if payload.source == "" && payload.sourceAccount == "" && payload.auth == "" && payload.authIndex == "" {
		return false, nil
	}

	source := payload.source
	if source == "" {
		source = migrationNonBlank(row.source)
	}
	sourceAccount := payload.sourceAccount
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
	authIndex := payload.authIndex
	if authIndex == "" {
		authIndex = migrationNonBlank(row.authIndex)
	}
	if authIndex == "" {
		authIndex = migrationNonBlank(row.factAuthIndex)
	}
	sourceKey := migrationUsageSourceKey(source)
	if sourceKey == "" {
		sourceKey = migrationNonBlank(row.factSourceKey)
	}

	if err := persistUsageAnalyticsRetentionMetadataRecord(ctx, tx, row.id, payload, sourceAccount); err != nil {
		return false, err
	}
	if !row.factRecordID.Valid {
		return false, nil
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_facts
		SET source_key = CASE WHEN ? <> '' THEN ? ELSE source_key END,
			auth = CASE WHEN ? <> '' THEN ? ELSE auth END,
			auth_index = CASE WHEN ? <> '' THEN ? ELSE auth_index END,
			source_account = CASE WHEN ? <> '' THEN ? ELSE source_account END
		WHERE usage_record_id = ? AND (
			(? <> '' AND (source_key IS NULL OR source_key <> ?)) OR
			(? <> '' AND (auth IS NULL OR auth <> ?)) OR
			(? <> '' AND (auth_index IS NULL OR auth_index <> ?)) OR
			(? <> '' AND (source_account IS NULL OR source_account <> ?))
		)
	`, sourceKey, sourceKey, auth, auth, authIndex, authIndex, sourceAccount, sourceAccount, row.id,
		sourceKey, sourceKey, auth, auth, authIndex, authIndex, sourceAccount, sourceAccount)
	if err != nil {
		return false, err
	}
	if sourceKey != "" && source != "" {
		if err := upsertUsageAnalyticsRetentionMetadataCatalog(ctx, tx, sourceKey, source, auth, sourceAccount, authIndex, authConflict); err != nil {
			return false, err
		}
	}

	// The record update above can fire the 202607310005 pending-fact trigger.
	// Preserve any marker that existed before this migration: it may represent
	// an unrelated out-of-band correction that needs the normal reconciler.
	if !row.pending {
		if _, err := tx.ExecContext(ctx, `DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, row.id); err != nil {
			return false, err
		}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func usageAnalyticsRetentionMetadataPayloadFromRawJSON(rawJSON string) usageAnalyticsRetentionMetadataPayload {
	if strings.TrimSpace(rawJSON) == "" {
		return usageAnalyticsRetentionMetadataPayload{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return usageAnalyticsRetentionMetadataPayload{}
	}
	return usageAnalyticsRetentionMetadataPayload{
		source:        migrationJSONText(payload["source"]),
		sourceAccount: usageAnalyticsMetadataSourceAccount(payload),
		auth:          migrationJSONText(payload["auth_type"]),
		authIndex:     migrationFirstJSONText(payload, "auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId"),
	}
}

func persistUsageAnalyticsRetentionMetadataRecord(ctx context.Context, tx *sql.Tx, recordID int64, payload usageAnalyticsRetentionMetadataPayload, sourceAccount string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE usage_records
		SET source = CASE WHEN ? <> '' THEN ? ELSE source END,
			source_account = CASE
				WHEN ? <> '' THEN ?
				WHEN (source_account IS NULL OR trim(source_account) = '') AND ? <> '' THEN ?
				ELSE source_account
			END,
			auth = CASE WHEN ? <> '' THEN ? ELSE auth END,
			auth_index = CASE WHEN ? <> '' THEN ? ELSE auth_index END
		WHERE id = ? AND (
			(? <> '' AND (source IS NULL OR source <> ?)) OR
			(? <> '' AND (source_account IS NULL OR source_account <> ?)) OR
			((source_account IS NULL OR trim(source_account) = '') AND ? <> '') OR
			(? <> '' AND (auth IS NULL OR auth <> ?)) OR
			(? <> '' AND (auth_index IS NULL OR auth_index <> ?))
		)
	`, payload.source, payload.source,
		payload.sourceAccount, payload.sourceAccount, sourceAccount, sourceAccount,
		payload.auth, payload.auth, payload.authIndex, payload.authIndex,
		recordID,
		payload.source, payload.source,
		payload.sourceAccount, payload.sourceAccount, sourceAccount,
		payload.auth, payload.auth, payload.authIndex, payload.authIndex)
	return err
}

func upsertUsageAnalyticsRetentionMetadataCatalog(ctx context.Context, tx *sql.Tx, sourceKey, source, auth, sourceAccount, authIndex string, authConflict bool) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_source_catalog (
			source_key, source, auth, auth_conflict, source_account, auth_index, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
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
			auth_index = COALESCE(usage_source_catalog.auth_index, excluded.auth_index),
			updated_at = excluded.updated_at
	`, sourceKey, source, nullableMigrationOptionalText(auth), authConflict,
		nullableMigrationOptionalText(sourceAccount), nullableMigrationOptionalText(authIndex))
	return err
}
