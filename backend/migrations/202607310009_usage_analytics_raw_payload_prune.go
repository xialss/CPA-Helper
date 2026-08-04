package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUsageAnalyticsRawPayloadPrune, nil)
}

// upUsageAnalyticsRawPayloadPrune preserves pending-fact reconciliation for
// retained raw-payload corrections while leaving a retention-only clear alone.
// The compact fact is already current before scheduled retention runs, so
// clearing that payload must not invalidate the hourly baseline.
func upUsageAnalyticsRawPayloadPrune(ctx context.Context, db *sql.DB) (err error) {
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
		WHEN
			NEW.timestamp IS NOT OLD.timestamp OR
			NEW.usage_username IS NOT OLD.usage_username OR
			NEW.api_key_description IS NOT OLD.api_key_description OR
			NEW.provider IS NOT OLD.provider OR
			NEW.model IS NOT OLD.model OR
			NEW.service_tier IS NOT OLD.service_tier OR
			NEW.endpoint IS NOT OLD.endpoint OR
			NEW.source IS NOT OLD.source OR
			NEW.source_account IS NOT OLD.source_account OR
			NEW.auth IS NOT OLD.auth OR
			NEW.auth_index IS NOT OLD.auth_index OR
			NEW.ttft_ms IS NOT OLD.ttft_ms OR
			NEW.failed IS NOT OLD.failed OR
			NEW.input_tokens IS NOT OLD.input_tokens OR
			NEW.output_tokens IS NOT OLD.output_tokens OR
			NEW.cached_tokens IS NOT OLD.cached_tokens OR
			NEW.cache_read_tokens IS NOT OLD.cache_read_tokens OR
			NEW.cache_creation_tokens IS NOT OLD.cache_creation_tokens OR
			NEW.reasoning_tokens IS NOT OLD.reasoning_tokens OR
			NEW.total_tokens IS NOT OLD.total_tokens OR
			(
				NEW.raw_json IS NOT OLD.raw_json AND
				trim(COALESCE(NEW.raw_json, '')) <> ''
			)
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END
	`); err != nil {
		return err
	}
	return tx.Commit()
}
