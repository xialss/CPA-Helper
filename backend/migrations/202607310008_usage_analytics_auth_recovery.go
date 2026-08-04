package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

const usageAnalyticsAuthRecoveryBatchSize = 500

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsAuthRecovery, nil)
}

// upUsageAnalyticsAuthRecovery restores fact auth values that
// 202607310004 could clear when retained raw JSON lacked a usable auth_type.
// The catalog remains fail-closed because its auth cannot prove the malformed
// payload's authentication type.
func upUsageAnalyticsAuthRecovery(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	factsChanged, err := recoverUsageAnalyticsFactAuth(ctx, tx)
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

type usageAnalyticsAuthRecoveryRow struct {
	id          int64
	rawJSON     sql.NullString
	sourceKey   string
	catalogAuth string
}

func recoverUsageAnalyticsFactAuth(ctx context.Context, tx *sql.Tx) (bool, error) {
	var factsChanged bool
	var lastID int64
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT records.id, records.raw_json, facts.source_key, catalog.auth
			FROM usage_records AS records
			JOIN usage_analytics_facts AS facts ON facts.usage_record_id = records.id
			JOIN usage_source_catalog AS catalog ON catalog.source_key = facts.source_key
			WHERE records.id > ?
			  AND trim(COALESCE(records.raw_json, '')) <> ''
			  AND (records.auth IS NULL OR trim(records.auth) = '')
			  AND (facts.auth IS NULL OR trim(facts.auth) = '')
			  AND trim(COALESCE(facts.source_key, '')) <> ''
			  AND trim(COALESCE(catalog.auth, '')) <> ''
			ORDER BY records.id
			LIMIT ?
		`, lastID, usageAnalyticsAuthRecoveryBatchSize)
		if err != nil {
			return false, err
		}

		batch := make([]usageAnalyticsAuthRecoveryRow, 0, usageAnalyticsAuthRecoveryBatchSize)
		for rows.Next() {
			var row usageAnalyticsAuthRecoveryRow
			if err := rows.Scan(&row.id, &row.rawJSON, &row.sourceKey, &row.catalogAuth); err != nil {
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
			lastID = row.id
			if usageAnalyticsMetadataPayload(row.rawJSON.String).auth != "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE usage_source_catalog
				SET auth_conflict = 1, updated_at = CURRENT_TIMESTAMP
				WHERE source_key = ? AND auth_conflict = 0
			`, row.sourceKey); err != nil {
				return false, err
			}
			conflicting, err := usageAnalyticsAuthRecoveryHasConflictingFact(ctx, tx, row.sourceKey, row.catalogAuth)
			if err != nil {
				return false, err
			}
			if conflicting {
				// The catalog cannot establish which auth belonged to this fact.
				continue
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE usage_analytics_facts
				SET auth = ?
				WHERE usage_record_id = ? AND (auth IS NULL OR trim(auth) = '')
			`, row.catalogAuth, row.id)
			if err != nil {
				return false, err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return false, err
			}
			if affected == 0 {
				continue
			}
			factsChanged = true
		}
	}
}

func usageAnalyticsAuthRecoveryHasConflictingFact(ctx context.Context, tx *sql.Tx, sourceKey, catalogAuth string) (bool, error) {
	var conflicting bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM usage_analytics_facts
			WHERE source_key = ?
			  AND trim(COALESCE(auth, '')) <> ''
			  AND lower(replace(replace(trim(auth), '-', ''), '_', '')) <> ?
		)
	`, sourceKey, usageAnalyticsMetadataAuthKey(catalogAuth)).Scan(&conflicting)
	if err != nil {
		return false, err
	}
	return conflicting, nil
}
