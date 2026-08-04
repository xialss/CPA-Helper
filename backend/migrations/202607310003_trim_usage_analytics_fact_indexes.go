package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upTrimUsageAnalyticsFactIndexes, nil)
}

func upTrimUsageAnalyticsFactIndexes(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, index := range []string{
		"ix_usage_analytics_facts_failed_timestamp",
		"ix_usage_analytics_facts_provider_timestamp",
		"ix_usage_analytics_facts_model_timestamp",
		"ix_usage_analytics_facts_endpoint_timestamp",
	} {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
			return err
		}
	}
	return tx.Commit()
}
