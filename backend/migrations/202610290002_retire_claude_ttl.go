package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(invalidateRetiredClaudeTTLPricing, invalidateRetiredClaudeTTLPricing)
}

// Rebuild cached estimates after retiring TTL pricing. Both upgrade and
// downgrade preserve usage, historical prices, and settled quota charges.
func invalidateRetiredClaudeTTLPricing(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET pricing_version = pricing_version + 1,
			hourly_needs_rebuild = 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`)
	return err
}
