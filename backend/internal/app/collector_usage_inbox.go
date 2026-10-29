package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const collectorUsageBatchSize = 1000

// Unexported normalizer flags are part of a resumable input, not API fields.
type collectorUsageEnvelope struct {
	Usage                 normalizedUsage `json:"usage"`
	ClaudeApplicable      *bool           `json:"claude_applicable,omitempty"`
	InputFromBreakdown    bool            `json:"input_from_breakdown"`
	BreakdownUncached     int             `json:"breakdown_uncached"`
	BreakdownUncachedOK   bool            `json:"breakdown_uncached_ok"`
	BreakdownCacheRead    int             `json:"breakdown_cache_read"`
	BreakdownCacheReadOK  bool            `json:"breakdown_cache_read_ok"`
	BreakdownCacheWrite   int             `json:"breakdown_cache_write"`
	BreakdownCacheWriteOK bool            `json:"breakdown_cache_write_ok"`
}

type collectorUsageInboxItem struct {
	ID           int64
	Origin       string
	DedupeKey    string
	Envelope     collectorUsageEnvelope
	Owner        frozenUsageOwner
	State        string
	QuotaHandled bool
}

func newCollectorUsageEnvelope(usage normalizedUsage) collectorUsageEnvelope {
	return collectorUsageEnvelope{
		Usage: usage, InputFromBreakdown: usage.inputFromBreakdown,
		BreakdownUncached: usage.breakdownUncached, BreakdownUncachedOK: usage.breakdownUncachedOK,
		BreakdownCacheRead: usage.breakdownCacheRead, BreakdownCacheReadOK: usage.breakdownCacheReadOK,
		BreakdownCacheWrite: usage.breakdownCacheWrite, BreakdownCacheWriteOK: usage.breakdownCacheWriteOK,
	}
}

func (e collectorUsageEnvelope) normalized() normalizedUsage {
	usage := e.Usage
	usage.inputFromBreakdown = e.InputFromBreakdown
	usage.breakdownUncached, usage.breakdownUncachedOK = e.BreakdownUncached, e.BreakdownUncachedOK
	usage.breakdownCacheRead, usage.breakdownCacheReadOK = e.BreakdownCacheRead, e.BreakdownCacheReadOK
	usage.breakdownCacheWrite, usage.breakdownCacheWriteOK = e.BreakdownCacheWrite, e.BreakdownCacheWriteOK
	return usage
}

// Unlike presentation masking, the inbox keeps exact numeric token evidence.
// Secret fields and API-key sources use the existing masking policy.
func redactCollectorUsageJSON(value any, auth *string) any {
	switch object := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(object))
		for key, child := range object {
			lower := strings.ToLower(key)
			numericToken := false
			if _, ok := child.(json.Number); ok {
				numericToken = strings.HasSuffix(lower, "_tokens") || lower == "total"
			}
			container := lower == "tokens" || lower == "token_breakdown"
			if !numericToken && !container && shouldRedactJSONField(key, auth, usageRedactionOptions{}) {
				if child == nil {
					result[key] = nil
				} else {
					text := fmt.Sprint(child)
					result[key] = maskSecret(&text)
				}
			} else {
				result[key] = redactCollectorUsageJSON(child, auth)
			}
		}
		return result
	case []any:
		result := make([]any, len(object))
		for i, child := range object {
			result[i] = redactCollectorUsageJSON(child, auth)
		}
		return result
	default:
		return value
	}
}

func protectedCollectorUsage(usage normalizedUsage) (normalizedUsage, error) {
	usage.SourceKey = usageSourceKey(usage.Source)
	var object any
	decoder := json.NewDecoder(strings.NewReader(usage.RawJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return usage, err
	}
	protected, err := json.Marshal(redactCollectorUsageJSON(object, usage.Auth))
	if err != nil {
		return usage, err
	}
	usage.RawJSON = string(protected)
	usage.Source = redactedUsageSource(usage.Source, usage.Auth, usageRedactionOptions{})
	return usage, nil
}

// A queue batch has already been removed upstream. Commit every input before
// accounting; failure leaves the batch with the runner for another attempt.
func (a *App) persistCollectorUsageBatch(ctx context.Context, collector CollectorConfig, messages []string, receivedAt time.Time, pricing ...modelPriceBillingIndex) error {
	if len(messages) == 0 {
		return nil
	}
	origin := usageCollectorOrigin(collector)
	if origin == "" {
		return fmt.Errorf("cannot persist usage batch without a collector origin")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, message := range messages {
		normalized, err := normalizeUsage([]byte(message))
		if err != nil {
			return err
		}
		normalized, err = protectedCollectorUsage(normalized)
		if err != nil {
			return err
		}
		input := newCollectorUsageEnvelope(normalized)
		envelope, err := json.Marshal(input)
		if err != nil {
			return err
		}
		var ownerID sql.NullInt64
		var username, description sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT users.id, users.username, keys.description
			FROM user_api_keys AS keys LEFT JOIN users ON users.id = keys.user_id
			WHERE keys.api_key_hash = ?`, normalized.APIKeyHash).Scan(&ownerID, &username, &description)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO usage_collector_inbox (
			collector_origin, dedupe_key, execution_id, caller_scope, payload_json,
			usage_user_id, usage_username, api_key_description, received_at, next_attempt_at, deadline_at, state
		) VALUES (?, ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, 'ready')
		ON CONFLICT(collector_origin, dedupe_key) DO NOTHING`, origin, normalized.DedupeKey,
			string(envelope),
			nullableUsageIntArg(nullableInt(ownerID)), nullableStringArg(nullableString(username)), nullableStringArg(nullableString(description)),
			dbTime(receivedAt), dbTime(receivedAt), dbTime(receivedAt))
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			continue
		}
		// A historical duplicate is a receipt, not permission to replay its quota.
		var recordID int
		err = tx.QueryRowContext(ctx, `SELECT id FROM usage_records WHERE dedupe_key = ?`, normalized.DedupeKey).Scan(&recordID)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE usage_collector_inbox SET state = 'complete',
				payload_json = '', caller_scope = NULL, usage_user_id = NULL, usage_username = NULL,
				api_key_description = NULL, usage_record_id = ?, completed_at = ?, last_error_code = NULL
				WHERE collector_origin = ? AND dedupe_key = ?`, recordID, dbTime(receivedAt), origin, normalized.DedupeKey); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) collectorUsageInboxDue(ctx context.Context, now time.Time) ([]collectorUsageInboxItem, error) {
	return a.collectorUsageInboxDueForPricing(ctx, now, true)
}

func (a *App) collectorUsageInboxDueForPricing(ctx context.Context, now time.Time, pricingAvailable bool) ([]collectorUsageInboxItem, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, collector_origin, dedupe_key,
		payload_json, usage_user_id, usage_username, api_key_description,
		state, quota_handled FROM usage_collector_inbox
		WHERE state IN ('pending', 'ready') AND (? OR quota_handled = 1)
		ORDER BY id LIMIT ?`, pricingAvailable, collectorUsageBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []collectorUsageInboxItem{}
	for rows.Next() {
		var item collectorUsageInboxItem
		var username, description sql.NullString
		var ownerID sql.NullInt64
		var payload string
		if err := rows.Scan(&item.ID, &item.Origin, &item.DedupeKey, &payload,
			&ownerID, &username, &description, &item.State, &item.QuotaHandled); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewBufferString(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item.Envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, fmt.Errorf("collector inbox %d contains an invalid persisted input", item.ID)
		}
		if item.Envelope.Usage.DedupeKey != item.DedupeKey {
			return nil, fmt.Errorf("collector inbox %d dedupe identity is inconsistent", item.ID)
		}
		item.Owner = frozenUsageOwner{InboxID: item.ID, UserID: nullableInt(ownerID), Username: nullableString(username), Description: nullableString(description)}
		items = append(items, item)
	}
	return items, rows.Err()
}

func sameOptionalUsageText(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (a *App) persistCollectorUsageResolution(ctx context.Context, item collectorUsageInboxItem, now time.Time) error {
	encoded, err := json.Marshal(item.Envelope)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE usage_collector_inbox SET payload_json = ?, state = ?,
		last_error_code = NULL, next_attempt_at = ?, attempts = attempts + 1 WHERE id = ? AND state <> 'complete'`,
		string(encoded), item.State, dbTime(now), item.ID)
	return err
}

func (a *App) finalizeCollectorUsage(ctx context.Context, item collectorUsageInboxItem, pricing modelPriceBillingIndex, now time.Time) (bool, error) {
	record, _, err := a.saveNormalizedUsage(ctx, item.Envelope.normalized(), pricing, item.Origin, &item.Owner)
	if err != nil {
		return false, err
	}
	return a.completeCollectorUsageReceipt(ctx, item, record.ID, now)
}

func (a *App) completeCollectorUsageReceipt(ctx context.Context, item collectorUsageInboxItem, recordID int, now time.Time) (bool, error) {
	// Erase the processing copy after idempotent quota completion. Only the
	// immutable receipt remains; raw retention and evidence belong to usage.
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE usage_collector_inbox SET state = 'complete', payload_json = '',
		caller_scope = NULL, usage_user_id = NULL, usage_username = NULL, api_key_description = NULL,
		last_error_code = NULL, usage_record_id = ?, completed_at = ? WHERE id = ? AND state IN ('pending', 'ready') AND quota_handled = 1`, recordID, dbTime(now), item.ID)
	if err != nil {
		return false, err
	}
	completed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if completed > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collector_state (id, running, records_collected, updated_at) VALUES (1, 0, 0, ?)`, dbTime(now)); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collector_state SET records_collected = records_collected + 1, updated_at = ? WHERE id = 1`, dbTime(now)); err != nil {
			return false, err
		}
	}
	return completed > 0, tx.Commit()
}

func (a *App) processCollectorUsageInbox(ctx context.Context, cfg AppConfig, pricing modelPriceBillingIndex, now time.Time) (int, error) {
	return a.processCollectorUsageInboxWithPricing(ctx, cfg, &pricing, now)
}

// Nil pricing only completes receipts whose quota result is already durable.
// Pending receipts from older releases no longer wait for remote TTL evidence.
func (a *App) processCollectorUsageInboxWithPricing(ctx context.Context, cfg AppConfig, pricing *modelPriceBillingIndex, now time.Time) (int, error) {
	// Exclude inputs that cannot advance before LIMIT, so they cannot starve
	// receipts whose accounting has already completed.
	items, err := a.collectorUsageInboxDueForPricing(ctx, now, pricing != nil)
	if err != nil {
		return 0, err
	}
	inserted := 0
	for index := range items {
		item := &items[index]
		if !item.QuotaHandled {
			continue
		}
		record, err := a.usageRecordByDedupe(ctx, item.DedupeKey)
		if err != nil {
			return inserted, err
		}
		completed, err := a.completeCollectorUsageReceipt(ctx, *item, record.ID, now)
		if err != nil {
			return inserted, err
		}
		if completed {
			inserted++
		}
		item.State = "complete"
	}
	for index := range items {
		item := &items[index]
		if item.State == "complete" {
			continue
		}
		if item.State == "pending" {
			item.State = "ready"
			if err := a.persistCollectorUsageResolution(ctx, *item, now); err != nil {
				return inserted, err
			}
		}
		if item.State == "ready" && pricing != nil {
			created, err := a.finalizeCollectorUsage(ctx, *item, *pricing, now)
			if err != nil {
				return inserted, err
			}
			if created {
				inserted++
			}
		}
	}
	return inserted, nil
}
