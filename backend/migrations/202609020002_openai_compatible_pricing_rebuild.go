package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upOpenAICompatiblePricingRebuild, nil)
}

// upOpenAICompatiblePricingRebuild invalidates hourly aggregates created
// before OpenAI-compatible runtime provider names were normalized for billing.
func upOpenAICompatiblePricingRebuild(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET pricing_version = pricing_version + 1,
			hourly_needs_rebuild = 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`)
	return err
}
