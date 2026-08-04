package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsOriginMetadataRepairBatchSize = 500

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsOriginMetadata, nil)
}

// upUsageAnalyticsOriginMetadata repairs retained direct-import payloads whose
// source projection uses the normalizer's source/origin aliases. It leaves
// already-pruned rows untouched and never replaces a populated fact field with
// an absent raw value.
func upUsageAnalyticsOriginMetadata(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	factsChanged, err := repairUsageAnalyticsOriginMetadata(ctx, tx)
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

type usageAnalyticsOriginMetadataRow struct {
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

type usageAnalyticsOriginMetadataPayload struct {
	source        string
	sourceAccount string
	auth          string
	authIndex     string
}

func repairUsageAnalyticsOriginMetadata(ctx context.Context, tx *sql.Tx) (bool, error) {
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
		`, lastID, usageAnalyticsOriginMetadataRepairBatchSize)
		if err != nil {
			return false, err
		}

		batch := make([]usageAnalyticsOriginMetadataRow, 0, usageAnalyticsOriginMetadataRepairBatchSize)
		for rows.Next() {
			var row usageAnalyticsOriginMetadataRow
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
			changed, err := repairUsageAnalyticsOriginMetadataRow(ctx, tx, row)
			if err != nil {
				return false, err
			}
			factsChanged = factsChanged || changed
			lastID = row.id
		}
	}
}

func repairUsageAnalyticsOriginMetadataRow(ctx context.Context, tx *sql.Tx, row usageAnalyticsOriginMetadataRow) (bool, error) {
	payload := usageAnalyticsOriginMetadataPayloadFromRawJSON(row.rawJSON.String)
	if payload.source == "" && payload.sourceAccount == "" && payload.auth == "" && payload.authIndex == "" {
		return false, nil
	}

	sourceAccount := payload.sourceAccount
	if sourceAccount == "" {
		sourceAccount = migrationNonBlank(row.sourceAccount)
	}
	if sourceAccount == "" {
		sourceAccount = migrationNonBlank(row.factSourceAccount)
	}
	if sourceAccount == "" {
		sourceAccount = migrationUsageSourceAccount(payload.source)
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
	sourceKey := migrationUsageSourceKey(payload.source)
	if sourceKey == "" {
		sourceKey = migrationNonBlank(row.factSourceKey)
	}

	if err := persistUsageAnalyticsOriginMetadataRecord(ctx, tx, row.id, payload, sourceAccount); err != nil {
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
	catalogSource := payload.source
	if catalogSource == "" {
		catalogSource = migrationNonBlank(row.source)
	}
	if sourceKey != "" && catalogSource != "" && migrationUsageSourceKey(catalogSource) == sourceKey {
		if err := upsertUsageAnalyticsRetentionMetadataCatalog(ctx, tx, sourceKey, catalogSource, auth, sourceAccount, authIndex, authConflict); err != nil {
			return false, err
		}
	}

	// Updating the scalar projection queues this row through the existing
	// trigger. Remove only that new marker; a marker present before migration
	// may describe a separate direct-import correction and must remain queued.
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

func usageAnalyticsOriginMetadataPayloadFromRawJSON(rawJSON string) usageAnalyticsOriginMetadataPayload {
	if strings.TrimSpace(rawJSON) == "" {
		return usageAnalyticsOriginMetadataPayload{}
	}
	var payload any
	if json.Unmarshal([]byte(rawJSON), &payload) != nil {
		return usageAnalyticsOriginMetadataPayload{}
	}
	// Keep the migration's source projection aligned with normalizeUsage:
	// source has precedence over origin, and both aliases are accepted
	// recursively and case-insensitively.
	source := usageAnalyticsOriginMetadataJSONText(usageAnalyticsOriginMetadataFindFirst(payload, "source", "origin"))
	return usageAnalyticsOriginMetadataPayload{
		source:        source,
		sourceAccount: usageAnalyticsOriginMetadataSourceAccount(payload),
		auth:          usageAnalyticsOriginMetadataAuth(payload),
		authIndex: usageAnalyticsOriginMetadataJSONText(usageAnalyticsOriginMetadataFindFirst(
			payload, "auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId",
		)),
	}
}

func usageAnalyticsOriginMetadataSourceAccount(payload any) string {
	for _, field := range []string{"email", "account_email", "accountEmail", "user_email", "userEmail"} {
		if account := usageAnalyticsOriginMetadataJSONText(usageAnalyticsOriginMetadataFindFirst(payload, field)); account != "" {
			return usageAnalyticsMetadataNormalizeSourceAccount(account)
		}
	}
	return ""
}

func usageAnalyticsOriginMetadataAuth(payload any) string {
	if auth := usageAnalyticsOriginMetadataJSONText(usageAnalyticsOriginMetadataFindFirst(payload, "auth_type")); auth != "" {
		return auth
	}
	return usageAnalyticsOriginMetadataJSONText(usageAnalyticsOriginMetadataFindFirst(payload, "auth", "authentication"))
}

// usageAnalyticsOriginMetadataJSONText mirrors toString, which the current
// raw-payload reconciliation path uses after encoding/json has decoded values.
func usageAnalyticsOriginMetadataJSONText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

// usageAnalyticsOriginMetadataFindFirst matches the source lookup behavior in
// normalizeUsage. It stays local to this forward migration because historic
// migrations must remain immutable.
func usageAnalyticsOriginMetadataFindFirst(value any, keys ...string) any {
	keySet := make(map[string]bool, len(keys))
	for _, key := range keys {
		keySet[strings.ToLower(key)] = true
	}
	var walk func(any) any
	walk = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if value, ok := typed[key]; ok {
					return value
				}
			}
			for key, child := range typed {
				if keySet[strings.ToLower(key)] {
					return child
				}
				if found := walk(child); found != nil {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := walk(child); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(value)
}

func persistUsageAnalyticsOriginMetadataRecord(ctx context.Context, tx *sql.Tx, recordID int64, payload usageAnalyticsOriginMetadataPayload, sourceAccount string) error {
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
