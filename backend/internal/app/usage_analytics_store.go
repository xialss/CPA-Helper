package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type usageAnalyticsExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

const usageAnalyticsPendingFactBatchSize = 500

func (a *App) upsertUsageAnalyticsFact(ctx context.Context, executor usageAnalyticsExecutor, record UsageRecord) error {
	source := usageAnalyticsRecordSource(record)
	authIndex := usageAnalyticsRecordAuthIndex(record)
	sourceKey := usageSourceKey(source)
	sourceAccount := usageAnalyticsRecordSourceAccount(record, source)
	auth, authConflict := usageSourceCatalogAuth(record)
	if err := persistUsageRecordAnalyticsMetadata(ctx, executor, record); err != nil {
		return err
	}
	if _, err := executor.ExecContext(ctx, `
		INSERT INTO usage_analytics_facts (
			usage_record_id, timestamp, usage_username, api_key_description, provider, model,
			service_tier, endpoint, source_key, auth, auth_index, source_account, ttft_ms,
			failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(usage_record_id) DO UPDATE SET
			timestamp = excluded.timestamp,
			usage_username = excluded.usage_username,
			api_key_description = excluded.api_key_description,
			provider = excluded.provider,
			model = excluded.model,
			service_tier = excluded.service_tier,
			endpoint = excluded.endpoint,
			source_key = COALESCE(excluded.source_key, usage_analytics_facts.source_key),
			auth = COALESCE(excluded.auth, usage_analytics_facts.auth),
			auth_index = COALESCE(excluded.auth_index, usage_analytics_facts.auth_index),
			source_account = COALESCE(excluded.source_account, usage_analytics_facts.source_account),
			ttft_ms = excluded.ttft_ms,
			failed = excluded.failed,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cached_tokens = excluded.cached_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_creation_tokens = excluded.cache_creation_tokens,
			reasoning_tokens = excluded.reasoning_tokens,
			total_tokens = excluded.total_tokens
	`, record.ID, dbTime(record.Timestamp), nullableStringArg(record.UsageUsername), nullableStringArg(record.APIKeyDescription), nullableStringArg(record.Provider), nullableStringArg(record.Model),
		nullableStringArg(record.ServiceTier), nullableStringArg(record.Endpoint), nullableStringArg(sourceKey), nullableStringArg(auth), nullableStringArg(authIndex), nullableStringArg(sourceAccount), nullableFloatArg(record.TTFTMS),
		record.Failed, record.InputTokens, record.OutputTokens, record.CachedTokens, record.CacheReadTokens, record.CacheCreationTokens, record.ReasoningTokens, record.TotalTokens); err != nil {
		return err
	}
	if sourceKey != nil {
		if err := upsertUsageSourceCatalog(ctx, executor, *sourceKey, source, auth, sourceAccount, authIndex, authConflict); err != nil {
			return err
		}
	}
	if _, err := executor.ExecContext(ctx, `DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, record.ID); err != nil {
		return err
	}
	return markUsageAnalyticsFactUpserted(ctx, executor, record.ID)
}

func usageAnalyticsRecordSource(record UsageRecord) *string {
	if source := sourceFromUsageRawJSON(record.RawJSON); source != nil {
		return source
	}
	if record.Source != nil && strings.TrimSpace(*record.Source) != "" {
		return record.Source
	}
	return nil
}

func usageAnalyticsRecordAuthIndex(record UsageRecord) *string {
	if authIndex := usageAnalyticsRawAuthIndex(record.RawJSON); authIndex != nil {
		return authIndex
	}
	if record.AuthIndex != nil && strings.TrimSpace(*record.AuthIndex) != "" {
		return record.AuthIndex
	}
	return nil
}

func usageAnalyticsRawAuthIndex(rawJSON string) *string {
	return usagePayloadStringFromRawJSON(rawJSON, "auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId")
}

func usageAnalyticsRecordSourceAccount(record UsageRecord, source *string) *string {
	if sourceAccount := sourceAccountFromUsageRawJSON(record.RawJSON); sourceAccount != nil {
		return sourceAccount
	}
	if record.SourceAccount != nil {
		return normalizeUsageSourceAccount(*record.SourceAccount)
	}
	return sourceAccountFromUsageSource(source)
}

// Direct imports can bypass normalizeUsage and enter the pending-fact queue
// with metadata only in raw_json. Persist the retained scalar projection so a
// later raw-payload prune cannot undo a corrected fact.
func persistUsageRecordAnalyticsMetadata(ctx context.Context, executor usageAnalyticsExecutor, record UsageRecord) error {
	source := sourceFromUsageRawJSON(record.RawJSON)
	sourceAccount := sourceAccountFromUsageRawJSON(record.RawJSON)
	auth := authFromUsageRawJSON(record.RawJSON)
	authIndex := usageAnalyticsRawAuthIndex(record.RawJSON)
	if source == nil && sourceAccount == nil && auth == nil && authIndex == nil {
		return nil
	}
	sourceArg := nullableStringArg(source)
	sourceAccountArg := nullableStringArg(sourceAccount)
	authArg := nullableStringArg(auth)
	authIndexArg := nullableStringArg(authIndex)
	_, err := executor.ExecContext(ctx, `
		UPDATE usage_records
		SET source = COALESCE(?, source),
			source_account = COALESCE(?, source_account),
			auth = COALESCE(?, auth),
			auth_index = COALESCE(?, auth_index)
		WHERE id = ? AND (
			(? IS NOT NULL AND (source IS NULL OR source <> ?)) OR
			(? IS NOT NULL AND (source_account IS NULL OR source_account <> ?)) OR
			(? IS NOT NULL AND (auth IS NULL OR auth <> ?)) OR
			(? IS NOT NULL AND (auth_index IS NULL OR auth_index <> ?))
		)
	`, sourceArg, sourceAccountArg, authArg, authIndexArg, record.ID,
		sourceArg, sourceArg, sourceAccountArg, sourceAccountArg,
		authArg, authArg, authIndexArg, authIndexArg)
	return err
}

func usageSourceCatalogAuth(record UsageRecord) (*string, bool) {
	storedAuth := usageNonBlankText(record.Auth)
	rawAuth := authFromUsageRawJSON(record.RawJSON)
	if rawAuth != nil {
		return rawAuth, storedAuth != nil && usageAuthTypeKey(storedAuth) != usageAuthTypeKey(rawAuth)
	}
	// List/detail rendering retains the legacy stored-auth fallback. The source
	// catalog cannot prove that fallback agrees with an absent, blank, or
	// malformed raw auth_type, so options must keep the source masked.
	return storedAuth, true
}

func usageNonBlankText(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func upsertUsageSourceCatalog(ctx context.Context, executor usageAnalyticsExecutor, sourceKey string, source, auth, sourceAccount, authIndex *string, authConflict bool) error {
	if strings.TrimSpace(sourceKey) == "" || source == nil || strings.TrimSpace(*source) == "" {
		return nil
	}
	_, err := executor.ExecContext(ctx, `
		INSERT INTO usage_source_catalog (
			source_key, source, auth, auth_conflict, source_account, auth_index, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_key) DO UPDATE SET
			auth_conflict = usage_source_catalog.auth_conflict OR excluded.auth_conflict OR (
				usage_source_catalog.auth IS NOT NULL AND excluded.auth IS NOT NULL AND
				lower(replace(replace(trim(usage_source_catalog.auth), '-', ''), '_', '')) <>
					lower(replace(replace(trim(excluded.auth), '-', ''), '_', ''))
			),
			auth = CASE
				WHEN usage_source_catalog.auth IS NULL OR trim(usage_source_catalog.auth) = '' THEN excluded.auth
				ELSE usage_source_catalog.auth
			END,
			source_account = COALESCE(usage_source_catalog.source_account, excluded.source_account),
			auth_index = COALESCE(usage_source_catalog.auth_index, excluded.auth_index),
			updated_at = excluded.updated_at
	`, sourceKey, strings.TrimSpace(*source), nullableStringArg(auth), authConflict, nullableStringArg(sourceAccount), nullableStringArg(authIndex), dbTime(time.Now()))
	return err
}

func markUsageAnalyticsFactsChanged(ctx context.Context, executor usageAnalyticsExecutor) error {
	_, err := executor.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET facts_version = facts_version + 1, hourly_needs_rebuild = 1, updated_at = ?
		WHERE id = 1
	`, dbTime(time.Now()))
	return err
}

func markUsageAnalyticsFactUpserted(ctx context.Context, executor usageAnalyticsExecutor, recordID int) error {
	_, err := executor.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET facts_version = facts_version + 1,
			hourly_needs_rebuild = CASE
					WHEN hourly_max_record_id > 0 AND ? <= hourly_max_record_id THEN 1
					ELSE hourly_needs_rebuild
				END,
				updated_at = ?
		WHERE id = 1
	`, recordID, dbTime(time.Now()))
	return err
}

func markUsageAnalyticsPricingChanged(ctx context.Context, executor usageAnalyticsExecutor) error {
	_, err := executor.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET pricing_version = pricing_version + 1, updated_at = ?
		WHERE id = 1
	`, dbTime(time.Now()))
	return err
}

func markUsageAnalyticsSelectorChanged(ctx context.Context, executor usageAnalyticsExecutor) error {
	_, err := executor.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET selector_generation = selector_generation + 1, updated_at = ?
		WHERE id = 1
	`, dbTime(time.Now()))
	return err
}

func (a *App) requireUsageAnalyticsFacts(ctx context.Context) error {
	var pending int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_analytics_pending_facts`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("usage analytics facts are pending repair: %d usage records", pending)
	}
	var missing int
	if err := a.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM usage_records AS records
		LEFT JOIN usage_analytics_facts AS facts ON facts.usage_record_id = records.id
		WHERE facts.usage_record_id IS NULL
	`).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("usage analytics facts are incomplete: %d usage records are missing; run cpa-helper migrate", missing)
	}
	var orphaned int
	if err := a.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM usage_analytics_facts AS facts
		LEFT JOIN usage_records AS records ON records.id = facts.usage_record_id
		WHERE records.id IS NULL
	`).Scan(&orphaned); err != nil {
		return err
	}
	if orphaned != 0 {
		return fmt.Errorf("usage analytics facts are inconsistent: %d orphaned facts", orphaned)
	}
	return nil
}

// ensureUsageAnalyticsFacts repairs rows written outside the application write
// path (notably historic import tools). Normal ingestion updates the fact and
// clears its marker in the same transaction, so analytics requests remain
// facts-only after this bounded reconciliation step.
func (a *App) ensureUsageAnalyticsFacts(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	pending, err := a.usageAnalyticsFactsPending(ctx)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	for {
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		records, err := pendingUsageAnalyticsFactRecords(ctx, tx)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if len(records) == 0 {
			return tx.Commit()
		}
		for _, record := range records {
			if err := a.upsertUsageAnalyticsFact(ctx, tx, record); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
}

func (a *App) usageAnalyticsFactsPending(ctx context.Context) (bool, error) {
	var pending bool
	if err := a.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_analytics_pending_facts)`).Scan(&pending); err != nil {
		return false, err
	}
	return pending, nil
}

func pendingUsageAnalyticsFactRecords(ctx context.Context, tx *sql.Tx) ([]UsageRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT records.id, CAST(records.timestamp AS TEXT), records.usage_username,
		       records.api_key_description, records.provider, records.model, records.service_tier,
		       records.reasoning_effort, records.endpoint, records.source, records.source_account,
		       records.request_id, records.auth, records.auth_index, records.latency_ms, records.ttft_ms,
		       records.failed, records.input_tokens, records.output_tokens, records.cached_tokens,
		       records.cache_read_tokens, records.cache_creation_tokens, records.reasoning_tokens,
		       records.total_tokens, records.dedupe_key, records.raw_json
		FROM usage_analytics_pending_facts AS pending
		JOIN usage_records AS records ON records.id = pending.usage_record_id
		ORDER BY records.id
		LIMIT ?
	`, usageAnalyticsPendingFactBatchSize)
	if err != nil {
		return nil, err
	}
	records, scanErr := scanUsageRecords(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return records, nil
}

func usageFactsWhere(filters UsageFilters, alias string) (string, []any, error) {
	if filters.RequestID != nil {
		return "", nil, validationError("request_id 仅支持近 7 日请求明细查询")
	}
	prefix := strings.TrimSpace(alias)
	if prefix != "" {
		prefix += "."
	}
	clauses := []string{"1 = 1"}
	args := []any{}
	if filters.Start != nil {
		clauses = append(clauses, prefix+"timestamp >= ?")
		args = append(args, dbTime(*filters.Start))
	}
	if filters.End != nil {
		clauses = append(clauses, prefix+"timestamp < ?")
		args = append(args, dbTime(*filters.End))
	}
	if filters.UsageUsername != nil {
		clauses = append(clauses, prefix+"usage_username = ?")
		args = append(args, *filters.UsageUsername)
	}
	if filters.APIKeyDescription != nil {
		clauses = append(clauses, prefix+"api_key_description = ?")
		args = append(args, *filters.APIKeyDescription)
	}
	if filters.Provider != nil {
		clauses = append(clauses, prefix+"provider = ?")
		args = append(args, *filters.Provider)
	}
	if filters.Model != nil {
		clauses = append(clauses, prefix+"model = ?")
		args = append(args, *filters.Model)
	}
	if filters.SourceKey != nil {
		clauses = append(clauses, prefix+"source_key = ?")
		args = append(args, *filters.SourceKey)
	}
	if filters.Endpoint != nil {
		clauses = append(clauses, prefix+"endpoint = ?")
		args = append(args, *filters.Endpoint)
	}
	if filters.Failed != nil {
		clauses = append(clauses, prefix+"failed = ?")
		args = append(args, *filters.Failed)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, nil
}

func scanUsageAnalyticsFactRecord(scanner usageRecordScanner) (UsageRecord, error) {
	var record UsageRecord
	var timestamp, usageUsername, description, provider, model, serviceTier, endpoint, sourceKey, auth, authIndex, sourceAccount sql.NullString
	var ttftMS sql.NullFloat64
	if err := scanner.Scan(
		&record.ID, &timestamp, &usageUsername, &description, &provider, &model, &serviceTier, &endpoint,
		&sourceKey, &auth, &authIndex, &sourceAccount, &ttftMS, &record.Failed, &record.InputTokens, &record.OutputTokens,
		&record.CachedTokens, &record.CacheReadTokens, &record.CacheCreationTokens, &record.ReasoningTokens, &record.TotalTokens,
	); err != nil {
		return UsageRecord{}, err
	}
	if parsed, ok := parseDBTime(timestamp.String); ok {
		record.Timestamp = parsed
	}
	record.UsageUsername = nullableString(usageUsername)
	record.APIKeyDescription = nullableString(description)
	record.Provider = nullableString(provider)
	record.Model = nullableString(model)
	record.ServiceTier = nullableString(serviceTier)
	record.Endpoint = nullableString(endpoint)
	record.analyticsSourceKey = nullableString(sourceKey)
	record.Auth = nullableString(auth)
	record.AuthIndex = nullableString(authIndex)
	record.SourceAccount = nullableString(sourceAccount)
	record.TTFTMS = nullableFloat(ttftMS)
	record.resolvedAuth = record.Auth
	record.authResolved = true
	return record, nil
}

func scanUsageAnalyticsFactRecordWithSource(scanner usageRecordScanner) (UsageRecord, error) {
	var record UsageRecord
	var timestamp, usageUsername, description, provider, model, serviceTier, endpoint, sourceKey, auth, authIndex, sourceAccount, source sql.NullString
	var ttftMS sql.NullFloat64
	if err := scanner.Scan(
		&record.ID, &timestamp, &usageUsername, &description, &provider, &model, &serviceTier, &endpoint,
		&sourceKey, &auth, &authIndex, &sourceAccount, &ttftMS, &record.Failed, &record.InputTokens, &record.OutputTokens,
		&record.CachedTokens, &record.CacheReadTokens, &record.CacheCreationTokens, &record.ReasoningTokens, &record.TotalTokens,
		&source,
	); err != nil {
		return UsageRecord{}, err
	}
	if parsed, ok := parseDBTime(timestamp.String); ok {
		record.Timestamp = parsed
	}
	record.UsageUsername = nullableString(usageUsername)
	record.APIKeyDescription = nullableString(description)
	record.Provider = nullableString(provider)
	record.Model = nullableString(model)
	record.ServiceTier = nullableString(serviceTier)
	record.Endpoint = nullableString(endpoint)
	record.analyticsSourceKey = nullableString(sourceKey)
	record.Auth = nullableString(auth)
	record.AuthIndex = nullableString(authIndex)
	record.SourceAccount = nullableString(sourceAccount)
	record.Source = nullableString(source)
	record.TTFTMS = nullableFloat(ttftMS)
	record.resolvedAuth = record.Auth
	record.authResolved = true
	return record, nil
}
