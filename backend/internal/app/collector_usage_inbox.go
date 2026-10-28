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
	ID            int64
	Origin        string
	DedupeKey     string
	ExecutionID   *string
	CallerScope   *string
	Envelope      collectorUsageEnvelope
	Owner         frozenUsageOwner
	ReceivedAt    time.Time
	Deadline      time.Time
	Attempts      int
	State         string
	QuotaHandled  bool
	LastErrorCode *string
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

func collectorUsageScope(raw string) *string {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil {
		return nil
	}
	var key string
	if json.Unmarshal(object["api_key"], &key) != nil || strings.TrimSpace(key) == "" {
		return nil
	}
	scope := pluginCallerScope(key)
	return &scope
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
// probing metadata; failure leaves the batch with the runner for another attempt.
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
		scope := collectorUsageScope(normalized.RawJSON)
		normalized, err = protectedCollectorUsage(normalized)
		if err != nil {
			return err
		}
		input := newCollectorUsageEnvelope(normalized)
		if len(pricing) > 0 {
			input.ClaudeApplicable = boolPtr(collectorUsageIsClaude(normalized, pricing[0]))
		}
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
			usage_user_id, usage_username, api_key_description, received_at, next_attempt_at, deadline_at, last_error_code
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'metadata_pending')
		ON CONFLICT(collector_origin, dedupe_key) DO NOTHING`, origin, normalized.DedupeKey,
			nullableStringArg(normalized.ExecutionID), nullableStringArg(scope), string(envelope),
			nullableUsageIntArg(nullableInt(ownerID)), nullableStringArg(nullableString(username)), nullableStringArg(nullableString(description)),
			dbTime(receivedAt), dbTime(receivedAt), dbTime(receivedAt.Add(collectorTTLMetadataWait)))
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
	rows, err := a.db.QueryContext(ctx, `SELECT id, collector_origin, dedupe_key, execution_id, caller_scope,
		payload_json, usage_user_id, usage_username, api_key_description, received_at, deadline_at,
		attempts, state, last_error_code, quota_handled FROM usage_collector_inbox
		WHERE state IN ('pending', 'ready') AND (state = 'ready' OR next_attempt_at <= ? OR deadline_at <= ?)
		AND (? OR state = 'pending' OR quota_handled = 1)
		ORDER BY id LIMIT ?`, dbTime(now), dbTime(now), pricingAvailable, collectorTTLBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []collectorUsageInboxItem{}
	for rows.Next() {
		var item collectorUsageInboxItem
		var execution, scope, username, description, reason sql.NullString
		var ownerID sql.NullInt64
		var payload, received, deadline string
		if err := rows.Scan(&item.ID, &item.Origin, &item.DedupeKey, &execution, &scope, &payload,
			&ownerID, &username, &description, &received, &deadline, &item.Attempts, &item.State, &reason, &item.QuotaHandled); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewBufferString(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item.Envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, fmt.Errorf("collector inbox %d contains an invalid persisted input", item.ID)
		}
		var ok bool
		if item.ReceivedAt, ok = parseDBTime(received); !ok {
			return nil, fmt.Errorf("collector inbox %d has an invalid receipt time", item.ID)
		}
		if item.Deadline, ok = parseDBTime(deadline); !ok {
			return nil, fmt.Errorf("collector inbox %d has an invalid metadata deadline", item.ID)
		}
		if item.Envelope.Usage.DedupeKey != item.DedupeKey {
			return nil, fmt.Errorf("collector inbox %d dedupe identity is inconsistent", item.ID)
		}
		if _, err := encodeUsageCacheCreationMetadata(item.Envelope.Usage.CacheCreationMetadata); err != nil {
			return nil, fmt.Errorf("collector inbox %d: %w", item.ID, err)
		}
		item.ExecutionID, item.CallerScope = nullableString(execution), nullableString(scope)
		if !sameOptionalUsageText(item.ExecutionID, item.Envelope.Usage.ExecutionID) ||
			!sameOptionalUsageText(item.ExecutionID, item.Envelope.Usage.CacheCreationMetadata.ExecutionID) {
			return nil, fmt.Errorf("collector inbox %d execution identity is inconsistent", item.ID)
		}
		item.Owner = frozenUsageOwner{InboxID: item.ID, UserID: nullableInt(ownerID), Username: nullableString(username), Description: nullableString(description)}
		item.LastErrorCode = nullableString(reason)
		items = append(items, item)
	}
	return items, rows.Err()
}

func sameOptionalUsageText(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func collectorUsageIsClaude(usage normalizedUsage, pricing modelPriceBillingIndex) bool {
	record := UsageRecord{Provider: usage.Provider, Model: usage.Model, Auth: usage.Auth, AuthIndex: usage.AuthIndex, RawJSON: usage.RawJSON}
	price, _ := findMatchingChannelPrice(pricing.Prices, record, pricing.MatchContext)
	return usageUsesClaudeTokenSemantics(record, matchedModelPriceChannelBrand(price, record, pricing.MatchContext))
}

func collectorUsageHasUpstreamTTL(usage normalizedUsage) bool {
	return usage.CacheCreationTTL != nil && (*usage.CacheCreationTTL == "5m" || *usage.CacheCreationTTL == "1h" || *usage.CacheCreationTTL == "mixed")
}

func collectorUsageTerminal(item *collectorUsageInboxItem, status, reason string) {
	metadata := item.Envelope.Usage.CacheCreationMetadata
	metadata.Status, metadata.Reason = ttlText(status), ttlText(reason)
	item.Envelope.Usage.CacheCreationMetadata = metadata
	item.State, item.LastErrorCode = "ready", nil
}

func (a *App) persistCollectorUsageResolution(ctx context.Context, item collectorUsageInboxItem, now time.Time) error {
	encoded, err := json.Marshal(item.Envelope)
	if err != nil {
		return err
	}
	delay := 10 * time.Second
	if item.Attempts == 1 {
		delay = 30 * time.Second
	} else if item.Attempts > 1 {
		delay = time.Minute
	}
	next := now.Add(delay)
	if next.After(item.Deadline) {
		next = item.Deadline
	}
	_, err = a.db.ExecContext(ctx, `UPDATE usage_collector_inbox SET payload_json = ?, state = ?,
		last_error_code = ?, next_attempt_at = ?, attempts = attempts + 1 WHERE id = ? AND state <> 'complete'`,
		string(encoded), item.State, nullableStringArg(item.LastErrorCode), dbTime(next), item.ID)
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

func resolveCollectorUsageMetadata(item *collectorUsageInboxItem, fact usageTTLExecutionMetadata) {
	usage := &item.Envelope.Usage
	if item.CallerScope == nil || *item.CallerScope != fact.CallerScope {
		collectorUsageTerminal(item, "conflict", "caller_scope_mismatch")
		return
	}
	metadata := usage.CacheCreationMetadata
	metadata.PolicyRevision, metadata.ObservedAt = &fact.PolicyRevision, &fact.ObservedAt
	if collectorUsageHasUpstreamTTL(*usage) {
		usage.CacheCreationMetadata = metadata
		if *usage.CacheCreationTTL != fact.TTL {
			collectorUsageTerminal(item, "conflict", "upstream_policy_conflict")
		} else {
			collectorUsageTerminal(item, "matched", "upstream_reported")
		}
		return
	}
	usage.CacheCreationTTL = ttlText(fact.TTL)
	metadata.Source, metadata.Status, metadata.Reason = ttlText("enforced_request"), ttlText("matched"), ttlText("metadata_matched")
	usage.CacheCreationMetadata = metadata
	item.State, item.LastErrorCode = "ready", nil
}

func (a *App) processCollectorUsageInbox(ctx context.Context, cfg AppConfig, pricing modelPriceBillingIndex, now time.Time) (int, error) {
	return a.processCollectorUsageInboxWithPricing(ctx, cfg, &pricing, now)
}

// Nil pricing allows only metadata progress and already handled quota receipts.
// It must never create a usage/fact or execute a first quota calculation.
func (a *App) processCollectorUsageInboxWithPricing(ctx context.Context, cfg AppConfig, pricing *modelPriceBillingIndex, now time.Time) (int, error) {
	// Exclude ready inputs that cannot advance without pricing before LIMIT,
	// so a full batch of them cannot starve metadata or handled receipts.
	items, err := a.collectorUsageInboxDueForPricing(ctx, now, pricing != nil)
	if err != nil {
		return 0, err
	}
	inserted := 0
	for index := range items {
		item := &items[index]
		if item.State != "ready" || !item.QuotaHandled {
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
	origin := usageCollectorOrigin(cfg.Collector)
	candidates := []int{}
	ids, seen := []string{}, map[string]bool{}
	persistNeeded := make([]bool, len(items))
	for index := range items {
		item := &items[index]
		if item.State == "complete" {
			continue
		}
		if item.Envelope.ClaudeApplicable == nil && pricing != nil {
			item.Envelope.ClaudeApplicable = boolPtr(collectorUsageIsClaude(item.Envelope.Usage, *pricing))
			persistNeeded[index] = true
		} else if item.Envelope.ClaudeApplicable == nil && item.State == "pending" && item.LastErrorCode != nil {
			// Before this field existed these reasons were written only after
			// reliable pricing had established Claude applicability.
			switch *item.LastErrorCode {
			case "metadata_missing", "metadata_unavailable", "collector_origin_changed":
				item.Envelope.ClaudeApplicable = boolPtr(true)
			}
		}
		if item.State == "ready" {
			metadata := item.Envelope.Usage.CacheCreationMetadata
			if item.Envelope.ClaudeApplicable != nil && !*item.Envelope.ClaudeApplicable && item.Envelope.Usage.CacheCreationTokens > 0 &&
				(metadata.Status == nil || *metadata.Status != "not_applicable") {
				collectorUsageTerminal(item, "not_applicable", "non_claude")
				persistNeeded[index] = true
			}
			continue
		}
		persistNeeded[index] = true
		usage := item.Envelope.Usage
		switch {
		case usage.CacheCreationTokens <= 0:
			collectorUsageTerminal(item, "not_applicable", "no_cache_creation")
		case usage.Endpoint != nil && strings.HasSuffix(strings.Split(strings.ToLower(*usage.Endpoint), "?")[0], "/count_tokens"):
			collectorUsageTerminal(item, "not_applicable", "count_tokens")
		case item.Envelope.ClaudeApplicable != nil && !*item.Envelope.ClaudeApplicable:
			collectorUsageTerminal(item, "not_applicable", "non_claude")
		case usage.CacheCreationTTL != nil && *usage.CacheCreationTTL == "mixed" && !validUsageMixedBuckets(usage.CacheCreationTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens):
			collectorUsageTerminal(item, "conflict", "invalid_upstream_buckets")
		case item.ExecutionID == nil:
			if collectorUsageHasUpstreamTTL(usage) {
				collectorUsageTerminal(item, "matched", "upstream_reported")
			} else {
				collectorUsageTerminal(item, "missing", "execution_id_missing")
			}
		case item.CallerScope == nil || !validUsageCallerScope(*item.CallerScope):
			collectorUsageTerminal(item, "conflict", "caller_scope_missing")
		case !now.Before(item.Deadline):
			if collectorUsageHasUpstreamTTL(usage) {
				collectorUsageTerminal(item, "matched", "upstream_reported")
			} else if !now.Before(item.ReceivedAt.Add(collectorTTLRetention)) {
				collectorUsageTerminal(item, "expired", "metadata_expired")
			} else {
				reason := "metadata_missing"
				status := "missing"
				if item.LastErrorCode != nil && *item.LastErrorCode != "metadata_pending" {
					reason = *item.LastErrorCode
					if reason == "metadata_unavailable" {
						status = "error"
					}
				}
				collectorUsageTerminal(item, status, reason)
			}
		case item.Origin != origin:
			item.LastErrorCode = ttlText("collector_origin_changed")
		case item.Envelope.ClaudeApplicable == nil:
			// No reliable original classification remains. Keep the deadline,
			// but do not infer Claude applicability from a model or provider name.
			persistNeeded[index] = false
		default:
			candidates = append(candidates, index)
			if !seen[*item.ExecutionID] {
				ids = append(ids, *item.ExecutionID)
				seen[*item.ExecutionID] = true
			}
		}
	}
	if len(candidates) > 0 {
		feature, featureErr := a.pluginClaudeTTLFeature(ctx, cfg)
		var facts map[string]usageTTLExecutionMetadata
		lookupErr := featureErr
		if lookupErr == nil && feature.Supported && feature.MetadataVersion == "v1" {
			facts, lookupErr = a.lookupUsageTTLMetadata(ctx, cfg, ids, now)
		}
		for _, index := range candidates {
			item := &items[index]
			switch {
			case lookupErr != nil:
				reason, terminal := usageTTLMetadataErrorReason(lookupErr)
				if collectorUsageHasUpstreamTTL(item.Envelope.Usage) {
					var invalid *usageTTLMetadataProtocolError
					if errors.As(lookupErr, &invalid) {
						collectorUsageTerminal(item, "conflict", reason)
					} else {
						collectorUsageTerminal(item, "matched", "upstream_reported")
					}
				} else if terminal {
					status := "conflict"
					if strings.HasPrefix(reason, "unsupported_") || reason == "metadata_feature_incompatible" || reason == "metadata_unsupported" {
						status = "unsupported"
					}
					collectorUsageTerminal(item, status, reason)
				} else {
					item.LastErrorCode = ttlText(reason)
				}
			case !feature.Supported || feature.MetadataVersion != "v1":
				if collectorUsageHasUpstreamTTL(item.Envelope.Usage) {
					collectorUsageTerminal(item, "matched", "upstream_reported")
				} else {
					collectorUsageTerminal(item, "unsupported", "metadata_unsupported")
				}
			case facts[*item.ExecutionID].ExecutionID != "":
				resolveCollectorUsageMetadata(item, facts[*item.ExecutionID])
			case collectorUsageHasUpstreamTTL(item.Envelope.Usage):
				collectorUsageTerminal(item, "matched", "upstream_reported")
			default:
				item.LastErrorCode = ttlText("metadata_missing")
			}
		}
	}
	for index, item := range items {
		if item.State == "complete" {
			continue
		}
		if persistNeeded[index] {
			if err := a.persistCollectorUsageResolution(ctx, item, now); err != nil {
				return inserted, err
			}
		}
		if item.State == "ready" && pricing != nil {
			created, err := a.finalizeCollectorUsage(ctx, item, *pricing, now)
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
