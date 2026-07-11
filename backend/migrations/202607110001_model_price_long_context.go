package migrations

import (
	"context"
	"database/sql"

	"cpa-helper/backend/internal/pricingdefaults"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelPriceLongContext, nil)
}

func upModelPriceLongContext(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	columns, err := tableColumns(ctx, tx, "model_prices")
	if err != nil {
		return err
	}
	definitions := []struct {
		name       string
		definition string
	}{
		{"long_context_threshold_tokens", "INTEGER"},
		{"long_context_input_usd_per_million", "REAL"},
		{"long_context_output_usd_per_million", "REAL"},
		{"long_context_cache_read_usd_per_million", "REAL"},
		{"long_context_cache_creation_usd_per_million", "REAL"},
	}
	for _, column := range definitions {
		if columns[column.name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE model_prices ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return err
		}
	}

	if err := seedLongContextDefaults(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func seedLongContextDefaults(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, provider, model, input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, priority_multiplier
		FROM model_prices
		WHERE long_context_threshold_tokens IS NULL
		  AND long_context_input_usd_per_million IS NULL
		  AND long_context_output_usd_per_million IS NULL
		  AND long_context_cache_read_usd_per_million IS NULL
		  AND long_context_cache_creation_usd_per_million IS NULL
	`)
	if err != nil {
		return err
	}
	type longContextUpdate struct {
		id    int64
		price pricingdefaults.LongContextPrice
	}
	updates := []longContextUpdate{}
	for rows.Next() {
		var id int64
		var provider, model string
		var inputUSD, outputUSD, cacheReadUSD, cacheCreationUSD float64
		var priorityMultiplier sql.NullFloat64
		if err := rows.Scan(&id, &provider, &model, &inputUSD, &outputUSD, &cacheReadUSD, &cacheCreationUSD, &priorityMultiplier); err != nil {
			_ = rows.Close()
			return err
		}
		price, ok := pricingdefaults.LookupLongContext(provider, model)
		if !ok {
			continue
		}
		if priorityMultiplier.Valid && !migrationPriorityMultiplierProducesRoundableCost(priorityMultiplier.Float64,
			inputUSD, outputUSD, cacheReadUSD, cacheCreationUSD,
			price.InputUSDPerMillion, price.OutputUSDPerMillion, price.CacheReadUSDPerMillion, price.CacheCreationUSDPerMillion) {
			continue
		}
		updates = append(updates, longContextUpdate{id: id, price: price})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, update := range updates {
		price := update.price
		if _, err := tx.ExecContext(ctx, `
			UPDATE model_prices
			SET long_context_threshold_tokens = ?,
			    long_context_input_usd_per_million = ?,
			    long_context_output_usd_per_million = ?,
			    long_context_cache_read_usd_per_million = ?,
			    long_context_cache_creation_usd_per_million = ?
			WHERE id = ?
			  AND long_context_threshold_tokens IS NULL
			  AND long_context_input_usd_per_million IS NULL
			  AND long_context_output_usd_per_million IS NULL
			  AND long_context_cache_read_usd_per_million IS NULL
			  AND long_context_cache_creation_usd_per_million IS NULL
		`, price.ThresholdInputTokens, price.InputUSDPerMillion, price.OutputUSDPerMillion,
			price.CacheReadUSDPerMillion, price.CacheCreationUSDPerMillion, update.id); err != nil {
			return err
		}
	}
	return nil
}
