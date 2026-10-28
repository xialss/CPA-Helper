package migrations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upCollectorTTLMetadata, downCollectorTTLMetadata)
}

func upCollectorTTLMetadata(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "ALTER TABLE usage_records ADD COLUMN source_key TEXT"); err != nil {
		return err
	}
	for _, table := range []string{"usage_records", "usage_analytics_facts"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN cache_creation_metadata TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE usage_collector_inbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collector_origin TEXT NOT NULL,
			dedupe_key TEXT NOT NULL,
			execution_id TEXT,
			caller_scope TEXT,
			payload_json TEXT NOT NULL,
			usage_user_id INTEGER,
			usage_username TEXT,
			api_key_description TEXT,
			received_at TEXT NOT NULL,
			next_attempt_at TEXT NOT NULL,
			deadline_at TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ready', 'complete')),
			last_error_code TEXT,
			usage_record_id INTEGER,
			quota_handled INTEGER NOT NULL DEFAULT 0 CHECK (quota_handled IN (0, 1)),
			completed_at TEXT,
			UNIQUE(collector_origin, dedupe_key)
		);
		CREATE INDEX idx_usage_collector_inbox_due ON usage_collector_inbox(state, next_attempt_at, id);
		CREATE INDEX idx_usage_collector_inbox_execution ON usage_collector_inbox(collector_origin, execution_id);
	`); err != nil {
		return err
	}
	if err := backfillCollectorTTLMetadata(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TRIGGER usage_analytics_pending_cache_metadata_update
		AFTER UPDATE OF cache_creation_metadata, source_key ON usage_records
		WHEN NEW.cache_creation_metadata IS NOT OLD.cache_creation_metadata OR NEW.source_key IS NOT OLD.source_key
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END;
	`); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillCollectorTTLMetadata(ctx context.Context, tx *sql.Tx) error {
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id, cache_creation_tokens, cache_creation_ttl,
			cache_creation_5m_tokens, cache_creation_1h_tokens, raw_json
			FROM usage_records WHERE id > ? ORDER BY id LIMIT 1000`, after)
		if err != nil {
			return err
		}
		type item struct {
			id   int64
			data string
		}
		batch := []item{}
		for rows.Next() {
			var id, count int64
			var ttl sql.NullString
			var five, hour sql.NullInt64
			var raw string
			if err := rows.Scan(&id, &count, &ttl, &five, &hour, &raw); err != nil {
				rows.Close()
				return err
			}
			metadata := map[string]any{"version": "v1", "status": "legacy", "reason": "legacy_no_metadata"}
			var object map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &object) == nil {
				var execution string
				if json.Unmarshal(object["execution_id"], &execution) == nil && migrationValidExecutionID(execution) {
					metadata["execution_id"] = execution
				}
			}
			if count <= 0 {
				metadata["status"], metadata["reason"] = "not_applicable", "no_cache_creation"
			} else {
				metadata["ttl_source"] = "unknown"
				switch strings.TrimSpace(ttl.String) {
				case "5m", "1h", "mixed":
					metadata["ttl_source"], metadata["reason"] = "upstream", "historical_upstream"
					if strings.TrimSpace(ttl.String) == "mixed" && (!five.Valid || !hour.Valid || five.Int64 < 0 || hour.Int64 < 0 || five.Int64 > count || hour.Int64 != count-five.Int64) {
						metadata["status"], metadata["reason"] = "conflict", "invalid_upstream_buckets"
					}
				}
			}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, item{id: id, data: string(encoded)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if _, err := tx.ExecContext(ctx, `UPDATE usage_records SET cache_creation_metadata = ? WHERE id = ?`, row.data, row.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE usage_analytics_facts SET cache_creation_metadata = ? WHERE usage_record_id = ?`, row.data, row.id); err != nil {
				return err
			}
			after = row.id
		}
	}
	if after > 0 {
		_, err := tx.ExecContext(ctx, `UPDATE usage_analytics_state SET facts_version = facts_version + 1,
			hourly_needs_rebuild = 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`)
		return err
	}
	return nil
}

func migrationValidExecutionID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || strings.ToLower(id) != id {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

func downCollectorTTLMetadata(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_collector_inbox) OR
		EXISTS(SELECT 1 FROM usage_records WHERE json_valid(cache_creation_metadata)
		AND json_extract(cache_creation_metadata, '$.ttl_source') = 'enforced_request') OR
		EXISTS(SELECT 1 FROM usage_records WHERE source_key IS NOT NULL)`).Scan(&used); err != nil {
		return err
	}
	if used {
		return errors.New("cannot roll back collector TTL metadata after ingestion: preserve pending messages and frozen accounting evidence; restore a consistent pre-upgrade database backup instead")
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS usage_analytics_pending_cache_metadata_update;
		DROP TABLE usage_collector_inbox;
		ALTER TABLE usage_records DROP COLUMN cache_creation_metadata;
		ALTER TABLE usage_records DROP COLUMN source_key;
		ALTER TABLE usage_analytics_facts DROP COLUMN cache_creation_metadata;
		UPDATE usage_analytics_state SET hourly_needs_rebuild = 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1;`); err != nil {
		return err
	}
	return tx.Commit()
}
