package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type usageAnalyticsState struct {
	factsVersion        int64
	hourlyFactsVersion  int64
	hourlyMaxRecordID   int64
	hourlyNeedsRebuild  bool
	pricingVersion      int64
	selectorGeneration  int64
	selectorFingerprint string
	lastHourlyRebuildAt sql.NullString
}

type usageAnalyticsStateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type usageAnalyticsReadQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var errUsageAnalyticsHourlyReadGenerationChanged = errors.New("usage analytics hourly generation changed during read")

type usageAnalyticsHourlyDimension struct {
	value string
	valid bool
}

type usageAnalyticsHourlyKey struct {
	hourStart            string
	usageUsername        usageAnalyticsHourlyDimension
	apiKeyDescription    usageAnalyticsHourlyDimension
	provider             usageAnalyticsHourlyDimension
	model                usageAnalyticsHourlyDimension
	endpoint             usageAnalyticsHourlyDimension
	sourceKey            usageAnalyticsHourlyDimension
	failed               bool
	channelAuthType      usageAnalyticsHourlyDimension
	channelBrand         usageAnalyticsHourlyDimension
	channelKey           usageAnalyticsHourlyDimension
	channelLabel         usageAnalyticsHourlyDimension
	channelLabelFallback bool
}

type usageAnalyticsHourlyContribution struct {
	hourStart            time.Time
	usageUsername        *string
	apiKeyDescription    *string
	provider             *string
	model                *string
	endpoint             *string
	sourceKey            *string
	failed               bool
	channelAuthType      *string
	channelBrand         *string
	channelKey           *string
	channelLabel         *string
	channelLabelFallback bool
	recordCount          int64
	failedRecords        int64
	inputTokens          int64
	outputTokens         int64
	cachedTokens         int64
	reasoningTokens      int64
	normalInputTokens    int64
	cacheReadTokens      int64
	cacheCreationTokens  int64
	aggregateInputTokens int64
	aggregateTotalTokens int64
	estimatedCostUSD     float64
	unpricedRecords      int64
	ttftTotalMS          float64
	ttftCount            int64
}

type usageAnalyticsHourlyBuilder struct {
	prices       modelPriceIndex
	matchContext modelPriceMatchContext
	priceMatches map[usageAnalyticsPriceMatchKey]usageAnalyticsPriceMatch
	groups       map[usageAnalyticsHourlyKey]*usageAnalyticsHourlyContribution
}

func newUsageAnalyticsHourlyBuilder(prices modelPriceIndex, matchContext modelPriceMatchContext) *usageAnalyticsHourlyBuilder {
	return &usageAnalyticsHourlyBuilder{
		prices:       prices,
		matchContext: matchContext,
		priceMatches: map[usageAnalyticsPriceMatchKey]usageAnalyticsPriceMatch{},
		groups:       map[usageAnalyticsHourlyKey]*usageAnalyticsHourlyContribution{},
	}
}

func (builder *usageAnalyticsHourlyBuilder) add(record UsageRecord) {
	matchKey := usageAnalyticsPriceMatchKeyForRecord(record)
	match, ok := builder.priceMatches[matchKey]
	if !ok {
		matchedPrice, matchStatus := findMatchingChannelPrice(builder.prices, record, builder.matchContext)
		match = usageAnalyticsPriceMatch{
			price:  matchedPrice,
			status: matchStatus,
			brand:  matchedModelPriceChannelBrand(matchedPrice, record, builder.matchContext),
		}
		builder.priceMatches[matchKey] = match
	}
	breakdown := calculateRecordCostForMatch(record, match.price, match.status, match.brand, false)

	channelAuthType, channelBrand, channelKey, channelLabel, channelLabelFallback := usageAnalyticsHourlyChannel(match, builder.matchContext)
	hourStart := record.Timestamp.In(appTimeLocation).Truncate(time.Hour)
	key := usageAnalyticsHourlyKey{
		hourStart:            dbTime(hourStart),
		usageUsername:        usageAnalyticsHourlyDimensionFor(record.UsageUsername),
		apiKeyDescription:    usageAnalyticsHourlyDimensionFor(record.APIKeyDescription),
		provider:             usageAnalyticsHourlyDimensionFor(record.Provider),
		model:                usageAnalyticsHourlyDimensionFor(record.Model),
		endpoint:             usageAnalyticsHourlyDimensionFor(record.Endpoint),
		sourceKey:            usageAnalyticsHourlyDimensionFor(usageAnalyticsRecordSourceKey(record)),
		failed:               record.Failed,
		channelAuthType:      usageAnalyticsHourlyDimensionFor(channelAuthType),
		channelBrand:         usageAnalyticsHourlyDimensionFor(channelBrand),
		channelKey:           usageAnalyticsHourlyDimensionFor(channelKey),
		channelLabel:         usageAnalyticsHourlyDimensionFor(channelLabel),
		channelLabelFallback: channelLabelFallback,
	}
	item := builder.groups[key]
	if item == nil {
		item = &usageAnalyticsHourlyContribution{
			hourStart:            hourStart,
			usageUsername:        cloneUsageAnalyticsHourlyString(record.UsageUsername),
			apiKeyDescription:    cloneUsageAnalyticsHourlyString(record.APIKeyDescription),
			provider:             cloneUsageAnalyticsHourlyString(record.Provider),
			model:                cloneUsageAnalyticsHourlyString(record.Model),
			endpoint:             cloneUsageAnalyticsHourlyString(record.Endpoint),
			sourceKey:            cloneUsageAnalyticsHourlyString(usageAnalyticsRecordSourceKey(record)),
			failed:               record.Failed,
			channelAuthType:      channelAuthType,
			channelBrand:         channelBrand,
			channelKey:           channelKey,
			channelLabel:         channelLabel,
			channelLabelFallback: channelLabelFallback,
		}
		builder.groups[key] = item
	}
	item.recordCount++
	if record.Failed {
		item.failedRecords++
	}
	item.inputTokens += int64(record.InputTokens)
	item.outputTokens += int64(record.OutputTokens)
	item.cachedTokens += int64(record.CachedTokens)
	item.reasoningTokens += int64(record.ReasoningTokens)
	item.normalInputTokens += int64(breakdown.NormalInputTokens)
	item.cacheReadTokens += int64(breakdown.CacheReadTokens)
	item.cacheCreationTokens += int64(breakdown.CacheCreationTokens)
	item.aggregateInputTokens += int64(breakdown.ContextInputTokens)
	item.aggregateTotalTokens += int64(usageAggregateTotalTokens(record, match.brand))
	item.estimatedCostUSD = mathRound(item.estimatedCostUSD+breakdown.TotalUSD, 8)
	if breakdown.Unpriced {
		item.unpricedRecords++
	}
	if record.TTFTMS != nil && *record.TTFTMS > 0 {
		item.ttftTotalMS += *record.TTFTMS
		item.ttftCount++
	}
}

func usageAnalyticsHourlyChannel(match usageAnalyticsPriceMatch, matchContext modelPriceMatchContext) (*string, *string, *string, *string, bool) {
	if match.status != priceMatchStatusMatched || match.price == nil {
		return nil, nil, nil, nil, false
	}
	identity, ok := modelPriceChannelGroupIdentityForPrice(*match.price)
	if !ok {
		return nil, nil, nil, nil, false
	}
	display := modelPriceChannelDisplayForPrice(*match.price, matchContext)
	authType := identity.AuthType
	brand := string(identity.Brand)
	channelKey := identity.ChannelKey
	label := display.Label
	if display.LabelFallback {
		label = ""
	}
	return &authType, &brand, &channelKey, &label, display.LabelFallback
}

func usageAnalyticsHourlyDimensionFor(value *string) usageAnalyticsHourlyDimension {
	if value == nil {
		return usageAnalyticsHourlyDimension{}
	}
	return usageAnalyticsHourlyDimension{value: *value, valid: true}
}

func (dimension usageAnalyticsHourlyDimension) databaseValue() any {
	if !dimension.valid {
		return nil
	}
	return dimension.value
}

func cloneUsageAnalyticsHourlyString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func usageAnalyticsRecordSourceKey(record UsageRecord) *string {
	if record.analyticsSourceKey != nil {
		return record.analyticsSourceKey
	}
	return usageSourceKey(usageAnalyticsRecordSource(record))
}

func (a *App) ensureUsageAnalyticsHourly(ctx context.Context, pricing modelPriceBillingIndex) (modelPriceBillingIndex, error) {
	pricing, _, err := a.ensureUsageAnalyticsHourlyWithState(ctx, pricing)
	return pricing, err
}

// ensureUsageAnalyticsHourlyForMaintenance leaves a completed baseline alone,
// but folds any append-only fact tail into a new baseline. Readers can safely
// compose that tail with hourly rows; maintenance must not leave it behind.
func (a *App) ensureUsageAnalyticsHourlyForMaintenance(ctx context.Context, pricing modelPriceBillingIndex) error {
	a.usageHourlyMu.Lock()
	defer a.usageHourlyMu.Unlock()
	_, _, err := a.ensureUsageAnalyticsHourlyWithStateLocked(ctx, pricing, true)
	return err
}

// ensureUsageAnalyticsHourlyWithState returns the exact state generation that
// the returned pricing index was prepared against. Hybrid readers use that
// generation to reject a rebuild that commits between preparation and read.
func (a *App) ensureUsageAnalyticsHourlyWithState(ctx context.Context, pricing modelPriceBillingIndex) (modelPriceBillingIndex, usageAnalyticsState, error) {
	a.usageHourlyMu.Lock()
	defer a.usageHourlyMu.Unlock()
	return a.ensureUsageAnalyticsHourlyWithStateLocked(ctx, pricing, false)
}

func (a *App) ensureUsageAnalyticsHourlyWithStateLocked(ctx context.Context, pricing modelPriceBillingIndex, requireCompleteFactBaseline bool) (modelPriceBillingIndex, usageAnalyticsState, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	defer tx.Rollback()
	state, err := loadUsageAnalyticsState(ctx, tx)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	pricing, err = usageAnalyticsBillingPriceIndex(ctx, tx, pricing.MatchContext)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	fingerprint := usageAnalyticsSelectorFingerprint(pricing.MatchContext)
	if state.selectorFingerprint != fingerprint || !state.lastHourlyRebuildAt.Valid || state.hourlyNeedsRebuild {
		if err := tx.Rollback(); err != nil {
			return modelPriceBillingIndex{}, usageAnalyticsState{}, err
		}
		return a.rebuildUsageAnalyticsHourlyLocked(ctx, pricing)
	}
	baselineAvailable, err := usageAnalyticsHourlyBaselineAvailable(ctx, tx, state)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	if baselineAvailable && (!requireCompleteFactBaseline || state.hourlyFactsVersion == state.factsVersion) {
		if err := tx.Commit(); err != nil {
			return modelPriceBillingIndex{}, usageAnalyticsState{}, err
		}
		return pricing, state, nil
	}
	if err := tx.Rollback(); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	return a.rebuildUsageAnalyticsHourlyLocked(ctx, pricing)
}

func usageAnalyticsHourlyBaselineAvailable(ctx context.Context, queryer usageAnalyticsStateQueryer, state usageAnalyticsState) (bool, error) {
	var hasFacts bool
	if err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_analytics_facts)`).Scan(&hasFacts); err != nil {
		return false, err
	}
	if !hasFacts {
		return true, nil
	}
	if state.hourlyFactsVersion == 0 || state.hourlyMaxRecordID == 0 {
		return false, nil
	}
	var matchingHourly bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM usage_analytics_hourly
			WHERE facts_version = ? AND pricing_version = ? AND selector_generation = ?
		)
	`, state.hourlyFactsVersion, state.pricingVersion, state.selectorGeneration).Scan(&matchingHourly); err != nil {
		return false, err
	}
	return matchingHourly, nil
}

func (a *App) rebuildUsageAnalyticsHourly(ctx context.Context, pricing modelPriceBillingIndex) error {
	a.usageHourlyMu.Lock()
	defer a.usageHourlyMu.Unlock()
	_, _, err := a.rebuildUsageAnalyticsHourlyLocked(ctx, pricing)
	return err
}

func (a *App) rebuildUsageAnalyticsHourlyLocked(ctx context.Context, pricing modelPriceBillingIndex) (modelPriceBillingIndex, usageAnalyticsState, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	defer tx.Rollback()
	state, err := loadUsageAnalyticsState(ctx, tx)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	pricing, err = usageAnalyticsBillingPriceIndex(ctx, tx, pricing.MatchContext)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	selectorFingerprint := usageAnalyticsSelectorFingerprint(pricing.MatchContext)
	if state.selectorFingerprint != selectorFingerprint {
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_analytics_state
			SET selector_generation = selector_generation + 1, selector_fingerprint = ?, updated_at = ?
			WHERE id = 1
		`, selectorFingerprint, dbTime(time.Now())); err != nil {
			return modelPriceBillingIndex{}, usageAnalyticsState{}, err
		}
		state.selectorGeneration++
		state.selectorFingerprint = selectorFingerprint
	}
	var hourlyMaxRecordID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(usage_record_id), 0) FROM usage_analytics_facts`).Scan(&hourlyMaxRecordID); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT usage_record_id, CAST(timestamp AS TEXT), usage_username, api_key_description,
		       provider, model, service_tier, endpoint, source_key, auth, auth_index, source_account, ttft_ms,
		       failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens,
		       cache_creation_tokens, reasoning_tokens, total_tokens
		FROM usage_analytics_facts
		ORDER BY timestamp, usage_record_id
	`)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	builder := newUsageAnalyticsHourlyBuilder(pricing.Prices, pricing.MatchContext)
	for rows.Next() {
		record, err := scanUsageAnalyticsFactRecord(rows)
		if err != nil {
			_ = rows.Close()
			return modelPriceBillingIndex{}, usageAnalyticsState{}, err
		}
		builder.add(record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	if err := rows.Close(); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_analytics_hourly`); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_analytics_hourly (
			hour_start, usage_username, api_key_description, provider, model, endpoint, source_key, failed,
			channel_auth_type, channel_brand, channel_key, channel_label, channel_label_fallback,
			facts_version, pricing_version, selector_generation,
			record_count, failed_records, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
			normal_input_tokens, cache_read_tokens, cache_creation_tokens, aggregate_input_tokens,
			aggregate_total_tokens, estimated_cost_usd, unpriced_records, ttft_total_ms, ttft_count, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	defer statement.Close()
	for _, item := range builder.groups {
		if _, err := statement.ExecContext(ctx,
			dbTime(item.hourStart), nullableStringArg(item.usageUsername), nullableStringArg(item.apiKeyDescription),
			nullableStringArg(item.provider), nullableStringArg(item.model), nullableStringArg(item.endpoint), nullableStringArg(item.sourceKey), item.failed,
			nullableStringArg(item.channelAuthType), nullableStringArg(item.channelBrand), nullableStringArg(item.channelKey), nullableStringArg(item.channelLabel), item.channelLabelFallback,
			state.factsVersion, state.pricingVersion, state.selectorGeneration,
			item.recordCount, item.failedRecords, item.inputTokens, item.outputTokens, item.cachedTokens, item.reasoningTokens,
			item.normalInputTokens, item.cacheReadTokens, item.cacheCreationTokens, item.aggregateInputTokens,
			item.aggregateTotalTokens, item.estimatedCostUSD, item.unpricedRecords, item.ttftTotalMS, item.ttftCount, dbTime(time.Now()),
		); err != nil {
			return modelPriceBillingIndex{}, usageAnalyticsState{}, err
		}
	}
	rebuildAt := dbTime(time.Now())
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET last_hourly_rebuild_at = ?, hourly_facts_version = ?, hourly_max_record_id = ?,
			hourly_needs_rebuild = 0, updated_at = ?
		WHERE id = 1
	`, rebuildAt, state.factsVersion, hourlyMaxRecordID, rebuildAt); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	if err := tx.Commit(); err != nil {
		return modelPriceBillingIndex{}, usageAnalyticsState{}, err
	}
	state.hourlyFactsVersion = state.factsVersion
	state.hourlyMaxRecordID = hourlyMaxRecordID
	state.hourlyNeedsRebuild = false
	state.lastHourlyRebuildAt = sql.NullString{String: rebuildAt, Valid: true}
	return pricing, state, nil
}

func loadUsageAnalyticsState(ctx context.Context, queryer usageAnalyticsStateQueryer) (usageAnalyticsState, error) {
	var state usageAnalyticsState
	err := queryer.QueryRowContext(ctx, `
		SELECT facts_version, hourly_facts_version, hourly_max_record_id, hourly_needs_rebuild,
		       pricing_version, selector_generation, selector_fingerprint, last_hourly_rebuild_at
		FROM usage_analytics_state WHERE id = 1
	`).Scan(&state.factsVersion, &state.hourlyFactsVersion, &state.hourlyMaxRecordID, &state.hourlyNeedsRebuild,
		&state.pricingVersion, &state.selectorGeneration, &state.selectorFingerprint, &state.lastHourlyRebuildAt)
	if err != nil {
		return usageAnalyticsState{}, err
	}
	return state, nil
}

func usageAnalyticsSelectorFingerprint(matchContext modelPriceMatchContext) string {
	parts := []string{
		"required=" + strconv.FormatBool(matchContext.SelectorsRequired),
		"available=" + strconv.FormatBool(matchContext.SelectorsAvailable),
	}
	for identity, count := range matchContext.Selectors {
		parts = append(parts, "selector="+string(identity.Brand)+"\x00"+identity.ChannelKey+"\x00"+identity.Model+"\x00"+strconv.Itoa(count))
	}
	for identity, display := range matchContext.ChannelLabels {
		parts = append(parts, "label="+identity.AuthType+"\x00"+string(identity.Brand)+"\x00"+identity.ChannelKey+"\x00"+display.Label+"\x00"+strconv.FormatBool(display.LabelFallback))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

type usageAnalyticsHourlyReadPlan struct {
	useHourly bool
	hourly    UsageFilters
	leading   *UsageFilters
	trailing  *UsageFilters
}

func usageAnalyticsHourlyReadPlanFor(filters UsageFilters) usageAnalyticsHourlyReadPlan {
	if filters.RequestID != nil {
		return usageAnalyticsHourlyReadPlan{}
	}
	if filters.Start == nil && filters.End == nil {
		return usageAnalyticsHourlyReadPlan{useHourly: true, hourly: filters}
	}

	plan := usageAnalyticsHourlyReadPlan{hourly: filters}
	if filters.Start != nil {
		hourlyStart := usageAnalyticsCeilHour(*filters.Start)
		plan.hourly.Start = &hourlyStart
		if filters.Start.Before(hourlyStart) {
			leading := filters
			leading.End = &hourlyStart
			plan.leading = &leading
		}
	}
	if filters.End != nil {
		hourlyEnd := usageAnalyticsFloorHour(*filters.End)
		plan.hourly.End = &hourlyEnd
		if hourlyEnd.Before(*filters.End) {
			trailing := filters
			trailing.Start = &hourlyEnd
			plan.trailing = &trailing
		}
	}
	if plan.hourly.Start != nil && plan.hourly.End != nil && !plan.hourly.Start.Before(*plan.hourly.End) {
		return usageAnalyticsHourlyReadPlan{}
	}
	plan.useHourly = true
	return plan
}

func usageAnalyticsFloorHour(value time.Time) time.Time {
	return value.In(appTimeLocation).Truncate(time.Hour)
}

func usageAnalyticsCeilHour(value time.Time) time.Time {
	floor := usageAnalyticsFloorHour(value)
	if value.Equal(floor) {
		return floor
	}
	return floor.Add(time.Hour)
}

func usageHourlyWhere(filters UsageFilters, alias string) (string, []any, error) {
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
		clauses = append(clauses, prefix+"hour_start >= ?")
		args = append(args, dbTime(*filters.Start))
	}
	if filters.End != nil {
		clauses = append(clauses, prefix+"hour_start < ?")
		args = append(args, dbTime(*filters.End))
	}
	for _, item := range []struct {
		column string
		value  *string
	}{
		{"usage_username", filters.UsageUsername},
		{"api_key_description", filters.APIKeyDescription},
		{"provider", filters.Provider},
		{"model", filters.Model},
		{"source_key", filters.SourceKey},
		{"endpoint", filters.Endpoint},
	} {
		if item.value == nil {
			continue
		}
		clauses = append(clauses, prefix+item.column+" = ?")
		args = append(args, *item.value)
	}
	if filters.Failed != nil {
		clauses = append(clauses, prefix+"failed = ?")
		args = append(args, *filters.Failed)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, nil
}

func visitFilteredUsageAnalyticsHourly(ctx context.Context, queryer usageAnalyticsReadQueryer, filters UsageFilters, state usageAnalyticsState, visit func(usageAnalyticsHourlyContribution)) error {
	where, args, err := usageHourlyWhere(filters, "hourly")
	if err != nil {
		return err
	}
	where += " AND hourly.facts_version = ? AND hourly.pricing_version = ? AND hourly.selector_generation = ?"
	args = append(args, state.hourlyFactsVersion, state.pricingVersion, state.selectorGeneration)
	rows, err := queryer.QueryContext(ctx, `
		SELECT CAST(hourly.hour_start AS TEXT), hourly.usage_username, hourly.api_key_description,
		       hourly.provider, hourly.model, hourly.endpoint, hourly.source_key, hourly.failed,
		       hourly.channel_auth_type, hourly.channel_brand, hourly.channel_key, hourly.channel_label,
		       hourly.channel_label_fallback, hourly.record_count, hourly.failed_records,
		       hourly.input_tokens, hourly.output_tokens, hourly.cached_tokens, hourly.reasoning_tokens,
		       hourly.normal_input_tokens, hourly.cache_read_tokens, hourly.cache_creation_tokens,
		       hourly.aggregate_input_tokens, hourly.aggregate_total_tokens, hourly.estimated_cost_usd,
		       hourly.unpriced_records, hourly.ttft_total_ms, hourly.ttft_count
		FROM usage_analytics_hourly AS hourly `+where+`
		ORDER BY hourly.hour_start, hourly.usage_username, hourly.api_key_description, hourly.provider,
		         hourly.model, hourly.endpoint, hourly.source_key, hourly.failed, hourly.id
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanUsageAnalyticsHourlyContribution(rows)
		if err != nil {
			return err
		}
		visit(item)
	}
	return rows.Err()
}

func visitFilteredUsageAnalyticsFactsWithQueryer(ctx context.Context, queryer usageAnalyticsReadQueryer, filters UsageFilters, minimumRecordID int64, orderBy string, visit func(UsageRecord)) error {
	where, args, err := usageFactsWhere(filters, "facts")
	if err != nil {
		return err
	}
	query := `SELECT facts.usage_record_id, CAST(facts.timestamp AS TEXT), facts.usage_username,
		facts.api_key_description, facts.provider, facts.model, facts.service_tier, facts.endpoint,
		facts.source_key, facts.auth, facts.auth_index, facts.source_account, facts.ttft_ms, facts.failed,
		facts.input_tokens, facts.output_tokens, facts.cached_tokens, facts.cache_read_tokens,
		facts.cache_creation_tokens, facts.reasoning_tokens, facts.total_tokens, catalog.source
		FROM usage_analytics_facts AS facts
		LEFT JOIN usage_source_catalog AS catalog ON catalog.source_key = facts.source_key ` + where
	if minimumRecordID > 0 {
		query += " AND facts.usage_record_id > ?"
		args = append(args, minimumRecordID)
	}
	switch strings.TrimSpace(orderBy) {
	case "timestamp ASC":
		query += " ORDER BY facts.timestamp ASC, facts.usage_record_id ASC"
	case "timestamp DESC":
		query += " ORDER BY facts.timestamp DESC, facts.usage_record_id DESC"
	default:
		query += " ORDER BY facts.timestamp, facts.usage_record_id"
	}
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanUsageAnalyticsFactRecordWithSource(rows)
		if err != nil {
			return err
		}
		visit(record)
	}
	return rows.Err()
}

func scanUsageAnalyticsHourlyContribution(scanner usageRecordScanner) (usageAnalyticsHourlyContribution, error) {
	var item usageAnalyticsHourlyContribution
	var hourStart sql.NullString
	var usageUsername, apiKeyDescription, provider, model, endpoint, sourceKey sql.NullString
	var channelAuthType, channelBrand, channelKey, channelLabel sql.NullString
	if err := scanner.Scan(
		&hourStart, &usageUsername, &apiKeyDescription, &provider, &model, &endpoint, &sourceKey, &item.failed,
		&channelAuthType, &channelBrand, &channelKey, &channelLabel, &item.channelLabelFallback,
		&item.recordCount, &item.failedRecords, &item.inputTokens, &item.outputTokens, &item.cachedTokens, &item.reasoningTokens,
		&item.normalInputTokens, &item.cacheReadTokens, &item.cacheCreationTokens, &item.aggregateInputTokens,
		&item.aggregateTotalTokens, &item.estimatedCostUSD, &item.unpricedRecords, &item.ttftTotalMS, &item.ttftCount,
	); err != nil {
		return usageAnalyticsHourlyContribution{}, err
	}
	parsed, ok := parseDBTime(hourStart.String)
	if !ok {
		return usageAnalyticsHourlyContribution{}, fmt.Errorf("parse usage analytics hourly timestamp %q", hourStart.String)
	}
	item.hourStart = parsed
	item.usageUsername = nullableString(usageUsername)
	item.apiKeyDescription = nullableString(apiKeyDescription)
	item.provider = nullableString(provider)
	item.model = nullableString(model)
	item.endpoint = nullableString(endpoint)
	item.sourceKey = nullableString(sourceKey)
	item.channelAuthType = nullableString(channelAuthType)
	item.channelBrand = nullableString(channelBrand)
	item.channelKey = nullableString(channelKey)
	item.channelLabel = nullableString(channelLabel)
	return item, nil
}

func (a *App) collectUsageAnalytics(ctx context.Context, filters UsageFilters, pricing modelPriceBillingIndex, collector *usageAnalyticsCollector, orderBy string) error {
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return err
	}
	plan := usageAnalyticsHourlyReadPlanFor(filters)
	if !plan.useHourly {
		return a.visitFilteredUsageAnalyticsRecords(ctx, filters, orderBy, collector.Add)
	}
	if err := a.collectUsageAnalyticsHourlyAttempt(ctx, plan, pricing, collector, orderBy); !errors.Is(err, errUsageAnalyticsHourlyReadGenerationChanged) {
		return err
	}
	// A writer committed after preparation but before the transaction captured its
	// read snapshot. Reconcile/rebuild once against that observed generation;
	// another change is surfaced rather than returning a mixed response.
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return err
	}
	if err := a.collectUsageAnalyticsHourlyAttempt(ctx, plan, pricing, collector, orderBy); err != nil {
		if errors.Is(err, errUsageAnalyticsHourlyReadGenerationChanged) {
			return fmt.Errorf("usage analytics changed during hourly read; retry the request: %w", err)
		}
		return err
	}
	return nil
}

func usageAnalyticsHourlyReadGenerationMatches(expected, actual usageAnalyticsState) bool {
	return expected.factsVersion == actual.factsVersion &&
		expected.hourlyFactsVersion == actual.hourlyFactsVersion &&
		expected.hourlyMaxRecordID == actual.hourlyMaxRecordID &&
		expected.hourlyNeedsRebuild == actual.hourlyNeedsRebuild &&
		expected.pricingVersion == actual.pricingVersion &&
		expected.selectorGeneration == actual.selectorGeneration &&
		expected.selectorFingerprint == actual.selectorFingerprint &&
		expected.lastHourlyRebuildAt == actual.lastHourlyRebuildAt
}

func (a *App) collectUsageAnalyticsHourlyAttempt(ctx context.Context, plan usageAnalyticsHourlyReadPlan, pricing modelPriceBillingIndex, collector *usageAnalyticsCollector, orderBy string) error {
	hourlyPricing, expectedState, err := a.ensureUsageAnalyticsHourlyWithState(ctx, pricing)
	if err != nil {
		return err
	}
	return a.collectUsageAnalyticsHourlySnapshot(ctx, plan, hourlyPricing, expectedState, collector, orderBy)
}

func (a *App) collectUsageAnalyticsHourlySnapshot(ctx context.Context, plan usageAnalyticsHourlyReadPlan, hourlyPricing modelPriceBillingIndex, expectedState usageAnalyticsState, collector *usageAnalyticsCollector, orderBy string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := loadUsageAnalyticsState(ctx, tx)
	if err != nil {
		return err
	}
	if !usageAnalyticsHourlyReadGenerationMatches(expectedState, state) {
		return errUsageAnalyticsHourlyReadGenerationChanged
	}
	var factsPending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_analytics_pending_facts)`).Scan(&factsPending); err != nil {
		return err
	}
	if factsPending {
		return errUsageAnalyticsHourlyReadGenerationChanged
	}
	collector.setBillingPriceIndex(hourlyPricing)
	if plan.leading != nil {
		if err := visitFilteredUsageAnalyticsFactsWithQueryer(ctx, tx, *plan.leading, 0, orderBy, collector.Add); err != nil {
			return err
		}
	}
	if err := visitFilteredUsageAnalyticsHourly(ctx, tx, plan.hourly, state, collector.AddHourly); err != nil {
		return err
	}
	if state.factsVersion > state.hourlyFactsVersion {
		if err := visitFilteredUsageAnalyticsFactsWithQueryer(ctx, tx, plan.hourly, state.hourlyMaxRecordID, orderBy, collector.Add); err != nil {
			return err
		}
	}
	if plan.trailing != nil {
		if err := visitFilteredUsageAnalyticsFactsWithQueryer(ctx, tx, *plan.trailing, 0, orderBy, collector.Add); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (collector *usageAnalyticsCollector) AddHourly(value usageAnalyticsHourlyContribution) {
	if collector.summary != nil {
		collector.summary.addHourly(value)
	}
	if collector.trends != nil {
		collector.trends.addHourly(value)
	}
	for _, ranking := range collector.rankings {
		ranking.addHourly(value)
	}
	if collector.distributions != nil {
		collector.distributions.addHourly(value)
	}
}

func (accumulator *usageSummaryAccumulator) addHourly(value usageAnalyticsHourlyContribution) {
	accumulator.records += int(value.recordCount)
	accumulator.failed += int(value.failedRecords)
	accumulator.input += int(value.aggregateInputTokens)
	accumulator.output += int(value.outputTokens)
	accumulator.cached += int(value.cachedTokens)
	accumulator.reasoning += int(value.reasoningTokens)
	accumulator.total += int(value.aggregateTotalTokens)
	accumulator.normalInput += int(value.normalInputTokens)
	accumulator.cacheRead += int(value.cacheReadTokens)
	accumulator.cacheCreation += int(value.cacheCreationTokens)
	accumulator.estimated = mathRound(accumulator.estimated+value.estimatedCostUSD, 8)
	accumulator.unpriced += int(value.unpricedRecords)
	accumulator.ttftTotal += value.ttftTotalMS
	accumulator.ttftCount += int(value.ttftCount)
}

func (accumulator *usageTrendAccumulator) addHourly(value usageAnalyticsHourlyContribution) {
	bucket := value.hourStart.In(appTimeLocation).Format("2006-01-02")
	if accumulator.hourly {
		bucket = value.hourStart.In(appTimeLocation).Format("2006-01-02 15:00")
	}
	item := accumulator.buckets[bucket]
	if item == nil {
		item = &usageTrendBucket{}
		accumulator.buckets[bucket] = item
	}
	item.records += int(value.recordCount)
	item.failed += int(value.failedRecords)
	item.tokens += int(value.aggregateTotalTokens)
	item.cost = mathRound(item.cost+value.estimatedCostUSD, 8)
}

func (accumulator *usageRankingAccumulator) addHourly(value usageAnalyticsHourlyContribution) {
	key, label := "", ""
	userID := (*int)(nil)
	description := (*string)(nil)
	switch accumulator.groupBy {
	case "model":
		provider := valueOr(value.provider, "unknown")
		model := valueOr(value.model, "unknown")
		key = provider + "::" + model
		label = provider + " / " + model
	case "user":
		if value.usageUsername == nil {
			return
		}
		info, ok := accumulator.users[*value.usageUsername]
		if !ok {
			key = *value.usageUsername
			label = *value.usageUsername
		} else {
			key = strconv.Itoa(info.ID)
			label = info.Name
			id := info.ID
			userID = &id
		}
	default:
		descriptionValue := ""
		if value.apiKeyDescription != nil {
			descriptionValue = strings.TrimSpace(*value.apiKeyDescription)
		}
		key = descriptionValue
		if key == "" {
			key = "unlabeled"
			label = "未设置 KEY 描述"
		} else {
			label = descriptionValue
			description = &descriptionValue
		}
	}
	group := accumulator.groups[key]
	if group == nil {
		group = &usageRankingGroup{key: key, label: label, userID: userID, description: description}
		accumulator.groups[key] = group
	}
	group.records += int(value.recordCount)
	group.failed += int(value.failedRecords)
	group.tokens += int(value.aggregateTotalTokens)
	group.cost = mathRound(group.cost+value.estimatedCostUSD, 8)
}

func (accumulator *usageDistributionAccumulator) addHourly(value usageAnalyticsHourlyContribution) {
	accumulator.addHourlyGroup(accumulator.providers, valueOr(value.provider, "unknown"), value)
	accumulator.addHourlyGroup(accumulator.models, valueOr(value.model, "unknown"), value)
	accumulator.addHourlyGroup(accumulator.endpoints, valueOr(value.endpoint, "unknown"), value)
	if value.channelAuthType == nil || value.channelBrand == nil || value.channelKey == nil || value.estimatedCostUSD <= 0 {
		return
	}
	identity := modelPriceChannelGroupIdentity{
		AuthType:   *value.channelAuthType,
		Brand:      aiProviderBrand(*value.channelBrand),
		ChannelKey: *value.channelKey,
	}
	item := accumulator.channelCosts[identity]
	if item.Key == "" {
		label := ""
		if value.channelLabel != nil && !value.channelLabelFallback {
			label = *value.channelLabel
		}
		item = usageChannelCostItem{
			Key:             hashAPIKey("usage-channel-cost\x00" + identity.AuthType + "\x00" + string(identity.Brand) + "\x00" + identity.ChannelKey),
			Label:           label,
			LabelFallback:   value.channelLabelFallback,
			ChannelAuthType: identity.AuthType,
			ChannelBrand:    string(identity.Brand),
		}
	}
	item.EstimatedCostUSD = mathRound(item.EstimatedCostUSD+value.estimatedCostUSD, 8)
	accumulator.channelCosts[identity] = item
}

func (accumulator *usageDistributionAccumulator) addHourlyGroup(groups map[string]*usageDistributionGroup, key string, value usageAnalyticsHourlyContribution) {
	group := groups[key]
	if group == nil {
		group = &usageDistributionGroup{key: key}
		groups[key] = group
	}
	group.records += int(value.recordCount)
	group.tokens += int(value.aggregateTotalTokens)
	group.cost = mathRound(group.cost+value.estimatedCostUSD, 8)
}
