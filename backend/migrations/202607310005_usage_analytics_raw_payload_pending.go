package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsRawPayloadPending, nil)
}

// upUsageAnalyticsRawPayloadPending replaces the original pending-fact
// trigger without changing the already-applied 202607310001 migration. A raw
// payload correction can change persisted source metadata before retention.
func upUsageAnalyticsRawPayloadPending(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS usage_analytics_pending_facts_update`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TRIGGER usage_analytics_pending_facts_update
		AFTER UPDATE OF timestamp, usage_username, api_key_description, provider, model,
			service_tier, endpoint, source, source_account, auth, auth_index, ttft_ms,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, raw_json ON usage_records
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END
	`); err != nil {
		return err
	}
	return tx.Commit()
}
