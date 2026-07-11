package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUsageServiceTierFastPricing, nil)
}

func upUsageServiceTierFastPricing(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err := ensureUsageServiceTierFastPricingColumns(ctx, tx); err != nil {
		return err
	}
	if err := backfillUsageServiceTier(ctx, tx); err != nil {
		return err
	}
	if err := seedPriorityMultipliers(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureUsageServiceTierFastPricingColumns(ctx context.Context, tx *sql.Tx) error {
	usageColumns, err := tableColumns(ctx, tx, "usage_records")
	if err != nil {
		return err
	}
	if !usageColumns["service_tier"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_records ADD COLUMN service_tier VARCHAR(80)`); err != nil {
			return err
		}
	}

	priceColumns, err := tableColumns(ctx, tx, "model_prices")
	if err != nil {
		return err
	}
	if !priceColumns["priority_multiplier"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE model_prices ADD COLUMN priority_multiplier REAL`); err != nil {
			return err
		}
	}
	return nil
}

func backfillUsageServiceTier(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, raw_json FROM usage_records`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id          int64
		serviceTier *string
	}
	updates := []update{}
	for rows.Next() {
		var id int64
		var rawJSON string
		if err := rows.Scan(&id, &rawJSON); err != nil {
			return err
		}
		var parsed any
		if json.Unmarshal([]byte(rawJSON), &parsed) != nil {
			continue
		}
		serviceTier := migrationString(migrationFindFirst(parsed, "service_tier", "serviceTier"))
		if serviceTier != nil {
			updates = append(updates, update{id: id, serviceTier: serviceTier})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_records
			SET service_tier = COALESCE(NULLIF(service_tier, ''), ?)
			WHERE id = ?
		`, nullableMigrationString(update.serviceTier), update.id); err != nil {
			return err
		}
	}
	return nil
}

func seedPriorityMultipliers(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, model, input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million
		FROM model_prices
		WHERE priority_multiplier IS NULL
		  AND lower(trim(provider)) IN ('openai', 'codex')
	`)
	if err != nil {
		return err
	}
	type multiplierUpdate struct {
		id         int64
		multiplier float64
	}
	updates := []multiplierUpdate{}
	for rows.Next() {
		var id int64
		var model string
		var inputUSD, outputUSD, cacheReadUSD, cacheCreationUSD float64
		if err := rows.Scan(&id, &model, &inputUSD, &outputUSD, &cacheReadUSD, &cacheCreationUSD); err != nil {
			_ = rows.Close()
			return err
		}
		multiplier, ok := migrationDefaultPriorityMultiplier(model)
		if !ok || !migrationPriorityMultiplierProducesRoundableCost(multiplier, inputUSD, outputUSD, cacheReadUSD, cacheCreationUSD) {
			continue
		}
		updates = append(updates, multiplierUpdate{id: id, multiplier: multiplier})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE model_prices
			SET priority_multiplier = ?
			WHERE id = ? AND priority_multiplier IS NULL
		`, update.multiplier, update.id); err != nil {
			return err
		}
	}
	return nil
}

func migrationDefaultPriorityMultiplier(model string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.5":
		return 2.5, true
	case "gpt-5.4", "gpt-5.4-mini", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return 2, true
	default:
		return 0, false
	}
}

func migrationPriorityMultiplierProducesRoundableCost(multiplier float64, prices ...float64) bool {
	if !migrationFinitePositive(multiplier) {
		return false
	}
	for _, price := range prices {
		if !migrationFiniteNonNegative(price) {
			return false
		}
		if price == 0 {
			continue
		}
		if !migrationRoundableUSD(price * multiplier) {
			return false
		}
	}
	return true
}

func migrationRoundableUSD(value float64) bool {
	if !migrationFiniteNonNegative(value) {
		return false
	}
	rounded := math.Round(value*1e8) / 1e8
	return migrationFiniteNonNegative(rounded)
}

func migrationFiniteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func migrationFinitePositive(value float64) bool {
	return value > 0 && migrationFiniteNonNegative(value)
}
