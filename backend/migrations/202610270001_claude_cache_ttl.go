package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upClaudeCacheTTL, downClaudeCacheTTL)
}

func upClaudeCacheTTL(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"usage_records", "usage_analytics_facts"} {
		for _, column := range []string{"cache_creation_ttl TEXT", "cache_creation_5m_tokens INTEGER", "cache_creation_1h_tokens INTEGER"} {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"model_prices", "model_price_library_conflicts", "model_price_versions"} {
		for _, column := range []string{"cache_creation_1h_usd_per_million", "long_context_cache_creation_1h_usd_per_million"} {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" REAL"); err != nil {
				return err
			}
		}
	}
	if err := backfillClaudeCacheTTL(ctx, tx); err != nil {
		return err
	}
	if err := replaceClaudeCacheTTLFactTrigger(ctx, tx, true); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillClaudeCacheTTL(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, raw_json FROM usage_records WHERE cache_creation_tokens > 0 AND trim(raw_json) <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type update struct {
		id      int64
		ttl     string
		five    *int
		oneHour *int
	}
	updates := []update{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if !json.Valid([]byte(raw)) {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			continue
		}
		tokens, ok := payload["tokens"].(map[string]any)
		if !ok {
			continue
		}
		ttl, _ := tokens["cache_creation_ttl"].(string)
		ttl = strings.TrimSpace(ttl)
		current := update{id: id, ttl: ttl}
		switch ttl {
		case "5m", "1h", "unknown":
		case "mixed":
			current.five = migrationClaudeCacheBucket(tokens["cache_creation_5m_tokens"])
			current.oneHour = migrationClaudeCacheBucket(tokens["cache_creation_1h_tokens"])
		default:
			continue
		}
		updates = append(updates, current)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, current := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE usage_records SET cache_creation_ttl = ?, cache_creation_5m_tokens = ?, cache_creation_1h_tokens = ? WHERE id = ?`, current.ttl, current.five, current.oneHour, current.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_facts
		SET cache_creation_ttl = (SELECT records.cache_creation_ttl FROM usage_records AS records WHERE records.id = usage_analytics_facts.usage_record_id),
		    cache_creation_5m_tokens = (SELECT records.cache_creation_5m_tokens FROM usage_records AS records WHERE records.id = usage_analytics_facts.usage_record_id),
		    cache_creation_1h_tokens = (SELECT records.cache_creation_1h_tokens FROM usage_records AS records WHERE records.id = usage_analytics_facts.usage_record_id)
		WHERE EXISTS (SELECT 1 FROM usage_records AS records WHERE records.id = usage_analytics_facts.usage_record_id)
	`); err != nil {
		return err
	}
	if len(updates) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE usage_analytics_state SET facts_version = facts_version + 1, hourly_needs_rebuild = 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`)
	}
	return err
}

// Match live canonical buckets without converting JSON numbers through float64
// or SQLite REAL, both of which lose precision near the Go int boundary.
func migrationClaudeCacheBucket(value any) *int {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	parsed, ok := new(big.Rat).SetString(number.String())
	if !ok || parsed.Sign() < 0 || !parsed.IsInt() || parsed.Num().Cmp(new(big.Int).SetUint64(uint64(^uint(0)>>1))) > 0 {
		return nil
	}
	tokens := int(parsed.Num().Int64())
	return &tokens
}

func replaceClaudeCacheTTLFactTrigger(ctx context.Context, tx *sql.Tx, includeTTL bool) error {
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS usage_analytics_pending_facts_update`); err != nil {
		return err
	}
	columns, predicate := "", ""
	if includeTTL {
		columns = "cache_creation_ttl, cache_creation_5m_tokens, cache_creation_1h_tokens,"
		predicate = `NEW.cache_creation_ttl IS NOT OLD.cache_creation_ttl OR
			NEW.cache_creation_5m_tokens IS NOT OLD.cache_creation_5m_tokens OR
			NEW.cache_creation_1h_tokens IS NOT OLD.cache_creation_1h_tokens OR`
	}
	_, err := tx.ExecContext(ctx, `
		CREATE TRIGGER usage_analytics_pending_facts_update
		AFTER UPDATE OF timestamp, usage_username, api_key_description, provider, model,
			service_tier, endpoint, source, source_account, auth, auth_index, ttft_ms,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, `+columns+` reasoning_tokens, total_tokens, raw_json ON usage_records
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
			`+predicate+`
			NEW.reasoning_tokens IS NOT OLD.reasoning_tokens OR
			NEW.total_tokens IS NOT OLD.total_tokens OR
			(NEW.raw_json IS NOT OLD.raw_json AND trim(COALESCE(NEW.raw_json, '')) <> '')
		BEGIN
			INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (NEW.id);
		END
	`)
	return err
}

func downClaudeCacheTTL(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceClaudeCacheTTLFactTrigger(ctx, tx, false); err != nil {
		return err
	}
	for _, table := range []string{"model_price_versions", "model_price_library_conflicts", "model_prices"} {
		for _, column := range []string{"long_context_cache_creation_1h_usd_per_million", "cache_creation_1h_usd_per_million"} {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN "+column); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"usage_analytics_facts", "usage_records"} {
		for _, column := range []string{"cache_creation_1h_tokens", "cache_creation_5m_tokens", "cache_creation_ttl"} {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN "+column); err != nil {
				return err
			}
		}
	}
	// Existing hourly rows may still contain TTL-specific costs. The previous
	// schema must rebuild them with its aggregate-only cache-write semantics.
	if _, err := tx.ExecContext(ctx, `UPDATE usage_analytics_state SET hourly_needs_rebuild = 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		return err
	}
	return tx.Commit()
}
