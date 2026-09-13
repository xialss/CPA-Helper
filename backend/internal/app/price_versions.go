package app

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type modelPriceVersion struct {
	PriceID     int64
	Price       ModelPrice
	EffectiveAt time.Time
	Baseline    bool
}
type modelPriceVersionIndex map[[2]string]*modelPriceVersionHistory

type modelPriceVersionHistory struct {
	all       []*modelPriceVersion
	byPriceID map[int64][]*modelPriceVersion
}

// add keeps the loader's effective_at/id order in each lifecycle. Both indexes
// share immutable snapshots, so a request never copies or rebuilds a full chain.
func (index modelPriceVersionIndex) add(key [2]string, version modelPriceVersion) {
	history := index[key]
	if history == nil {
		history = &modelPriceVersionHistory{byPriceID: map[int64][]*modelPriceVersion{}}
		index[key] = history
	}
	history.all = append(history.all, &version)
	if version.PriceID > 0 {
		history.byPriceID[version.PriceID] = append(history.byPriceID[version.PriceID], &version)
	}
}

func modelPriceVersionFromPrice(price ModelPrice, effectiveAt time.Time, baseline bool) modelPriceVersion {
	return modelPriceVersion{PriceID: int64(price.ID), Price: price, EffectiveAt: effectiveAt, Baseline: baseline}
}

func (a *App) loadModelPriceVersions(ctx context.Context) (modelPriceVersionIndex, error) {
	return loadModelPriceVersionsWithQueryer(ctx, a.db)
}

func loadModelPriceVersionsWithQueryer(ctx context.Context, q modelPriceQueryer) (modelPriceVersionIndex, error) {
	rows, err := q.QueryContext(ctx, `SELECT price_id, provider, model, channel_auth_type, channel_brand, channel_key,
      input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
      request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens,
      long_context_input_usd_per_million, long_context_output_usd_per_million,
      long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		 effective_at, baseline, time_pricing FROM model_price_versions ORDER BY effective_at ASC, id ASC`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") && strings.Contains(strings.ToLower(err.Error()), "model_price_versions") {
			return modelPriceVersionIndex{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	result := modelPriceVersionIndex{}
	for rows.Next() {
		var p ModelPrice
		var priceID sql.NullInt64
		var auth, brand, key sql.NullString
		var req, pri sql.NullFloat64
		var threshold sql.NullInt64
		var li, lo, lcr, lcc sql.NullFloat64
		var effective string
		var baseline int
		var rule sql.NullString
		if err := rows.Scan(&priceID, &p.Provider, &p.Model, &auth, &brand, &key, &p.InputUSDPerMillion, &p.OutputUSDPerMillion, &p.CacheReadUSDPerMillion, &p.CacheCreationUSDPerMillion, &req, &p.BillingUnit, &pri, &threshold, &li, &lo, &lcr, &lcc, &effective, &baseline, &rule); err != nil {
			return nil, err
		}
		p.TimePricing, err = parsePriceTimeRule(rule)
		if err != nil {
			return nil, err
		}
		if priceID.Valid {
			p.ID = int(priceID.Int64)
		}
		if auth.Valid {
			p.ChannelAuthType = &auth.String
		}
		if brand.Valid {
			p.ChannelBrand = &brand.String
		}
		if key.Valid {
			p.ChannelKey = &key.String
		}
		if req.Valid {
			p.RequestUSD = &req.Float64
		}
		// Keep legacy non-finite values in the billing index. The shared cost
		// calculator uses them to fail closed for Fast requests; JSON projections
		// are sanitized separately at the API boundary.
		if pri.Valid {
			p.PriorityMultiplier = &pri.Float64
		}
		longContextConfigured := threshold.Valid || li.Valid || lo.Valid || lcr.Valid || lcc.Valid
		if longContextConfigured {
			candidate := &ModelPriceLongContext{ThresholdInputTokens: threshold.Int64, InputUSDPerMillion: li.Float64, OutputUSDPerMillion: lo.Float64, CacheReadUSDPerMillion: lcr.Float64, CacheCreationUSDPerMillion: lcc.Float64}
			p.LongContext = candidate
			p.longContextInvalid = !threshold.Valid || !li.Valid || !lo.Valid || !lcr.Valid || !lcc.Valid || !validLongContextPrice(candidate)
			if p.longContextInvalid {
				p.PreservedLongContext = &ModelPriceLibraryConflictLongContext{
					ThresholdInputTokens:       nullableInt64(threshold),
					InputUSDPerMillion:         nullableFloat(li),
					OutputUSDPerMillion:        nullableFloat(lo),
					CacheReadUSDPerMillion:     nullableFloat(lcr),
					CacheCreationUSDPerMillion: nullableFloat(lcc),
				}
			}
		}
		t, ok := parseDBTime(effective)
		if !ok {
			return nil, fmt.Errorf("parse model price version effective_at %q", effective)
		}
		p.PriceScope = modelPriceScopeChannel
		k := channelModelPriceKey(modelPriceChannelAuthType(p), valueOrEmpty(brand), valueOrEmpty(key), p.Model)
		version := modelPriceVersionFromPrice(p, t, baseline != 0)
		version.PriceID = priceID.Int64
		result.add(k, version)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func valueOrEmpty(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func bindLegacyPriceVersions(ctx context.Context, tx *sql.Tx, price ModelPrice) error {
	if price.ChannelBrand == nil || price.ChannelKey == nil {
		// Only channel prices have a version chain; a library price has
		// nothing to bind and must not be dereferenced.
		return nil
	}
	if price.ID > 0 {
		// Bind a genuinely legacy chain before adding its first lifecycle-aware
		// version. A prior identified lifecycle must never be adopted on recreate.
		_, err := tx.ExecContext(ctx, `UPDATE model_price_versions SET price_id=?
		 WHERE price_id IS NULL AND COALESCE(channel_auth_type,'apikey')=? AND channel_brand=? AND channel_key=? AND model=?
		 AND NOT EXISTS (SELECT 1 FROM model_price_versions v WHERE v.price_id IS NOT NULL AND COALESCE(v.channel_auth_type,'apikey')=? AND v.channel_brand=? AND v.channel_key=? AND v.model=?)`,
			price.ID, modelPriceChannelAuthType(price), *price.ChannelBrand, *price.ChannelKey, price.Model, modelPriceChannelAuthType(price), *price.ChannelBrand, *price.ChannelKey, price.Model)
		if err != nil {
			return err
		}
	}
	return nil
}
func (a *App) appendModelPriceVersion(ctx context.Context, tx *sql.Tx, price ModelPrice, effectiveAt time.Time, baseline bool) error {
	if price.PriceScope != modelPriceScopeChannel || price.ChannelBrand == nil || price.ChannelKey == nil {
		return nil
	}
	if err := bindLegacyPriceVersions(ctx, tx, price); err != nil {
		return err
	}
	threshold, input, output, read, write := snapshotLongContextArgs(price)
	_, err := tx.ExecContext(ctx, `INSERT INTO model_price_versions (
      price_id, provider, model, channel_auth_type, channel_brand, channel_key,
      input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
      request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens,
      long_context_input_usd_per_million, long_context_output_usd_per_million,
      long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
      effective_at, baseline, time_pricing) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		func() any {
			if price.ID > 0 {
				return price.ID
			}
			return nil
		}(), price.Provider, price.Model, nullableStringArg(price.ChannelAuthType), nullableStringArg(price.ChannelBrand), nullableStringArg(price.ChannelKey),
		price.InputUSDPerMillion, price.OutputUSDPerMillion, price.CacheReadUSDPerMillion, price.CacheCreationUSDPerMillion,
		nullableFloatArg(price.RequestUSD), price.BillingUnit, nullableFloatArg(price.PriorityMultiplier), threshold, input, output, read, write, dbTime(effectiveAt), baseline, priceTimeRuleJSON(price.TimePricing))
	return err
}

// correctLatestModelPriceVersion overwrites the price columns of the newest
// version for the channel identity while keeping its effective_at and baseline
// flag, so requests since that moment re-resolve to the corrected price. When
// no version exists yet (legacy data before baselines were seeded), it appends
// a version at effectiveAt instead of failing the save.
func (a *App) correctLatestModelPriceVersion(ctx context.Context, tx *sql.Tx, price ModelPrice, effectiveAt time.Time) error {
	if price.PriceScope != modelPriceScopeChannel || price.ChannelBrand == nil || price.ChannelKey == nil {
		return nil
	}
	if err := bindLegacyPriceVersions(ctx, tx, price); err != nil {
		return err
	}
	var latestID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM model_price_versions
      WHERE (price_id = ? OR (price_id IS NULL AND COALESCE(channel_auth_type, 'apikey') = ? AND channel_brand = ? AND channel_key = ? AND model = ?
        AND NOT EXISTS (SELECT 1 FROM model_price_versions current_lifecycle WHERE current_lifecycle.price_id = ?)))
      ORDER BY effective_at DESC, id DESC LIMIT 1`,
		price.ID, modelPriceChannelAuthType(price), *price.ChannelBrand, *price.ChannelKey, price.Model, price.ID).Scan(&latestID)
	if err != nil {
		if err == sql.ErrNoRows {
			return a.appendModelPriceVersion(ctx, tx, price, effectiveAt, false)
		}
		return err
	}
	threshold, input, output, read, write := snapshotLongContextArgs(price)
	result, err := tx.ExecContext(ctx, `UPDATE model_price_versions SET price_id = COALESCE(price_id, ?),
      provider = ?, input_usd_per_million = ?, output_usd_per_million = ?, cache_read_usd_per_million = ?, cache_creation_usd_per_million = ?,
      request_usd = ?, billing_unit = ?, priority_multiplier = ?, long_context_threshold_tokens = ?,
      long_context_input_usd_per_million = ?, long_context_output_usd_per_million = ?,
      long_context_cache_read_usd_per_million = ?, long_context_cache_creation_usd_per_million = ?, time_pricing = ?
      WHERE id = ?`,
		price.ID, price.Provider, price.InputUSDPerMillion, price.OutputUSDPerMillion, price.CacheReadUSDPerMillion, price.CacheCreationUSDPerMillion,
		nullableFloatArg(price.RequestUSD), price.BillingUnit, nullableFloatArg(price.PriorityMultiplier), threshold,
		input, output, read, write,
		priceTimeRuleJSON(price.TimePricing), latestID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("correct model price version %d: %d rows affected", latestID, affected)
	}
	return nil
}

// ModelPriceVersion is the read-only API projection of one channel price version.
type ModelPriceVersion struct {
	TimePricing                *PriceTimeRule                        `json:"time_pricing"`
	ID                         int64                                 `json:"id"`
	EffectiveAt                time.Time                             `json:"effective_at"`
	Baseline                   bool                                  `json:"baseline"`
	InputUSDPerMillion         float64                               `json:"input_usd_per_million"`
	OutputUSDPerMillion        float64                               `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     float64                               `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion float64                               `json:"cache_creation_usd_per_million"`
	RequestUSD                 *float64                              `json:"request_usd"`
	BillingUnit                string                                `json:"billing_unit"`
	PriorityMultiplier         *float64                              `json:"priority_multiplier"`
	LongContext                *ModelPriceLongContext                `json:"long_context"`
	PreservedLongContext       *ModelPriceLibraryConflictLongContext `json:"preserved_long_context"`
}

func modelPriceVersionsForAPI(versions []ModelPriceVersion) []map[string]any {
	result := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		version = modelPriceVersionForAPI(version)
		item := apiJSONValue(version).(map[string]any)
		// Billing boundaries require the stored microsecond precision, unlike
		// ordinary display timestamps normalized by the shared JSON writer.
		item["effective_at"] = dbTime(version.EffectiveAt)
		result = append(result, item)
	}
	return result
}

func modelPriceVersionForAPI(version ModelPriceVersion) ModelPriceVersion {
	if version.PriorityMultiplier != nil && (math.IsNaN(*version.PriorityMultiplier) || math.IsInf(*version.PriorityMultiplier, 0)) {
		version.PriorityMultiplier = nil
	}
	if version.LongContext != nil && !validLongContextPrice(version.LongContext) {
		version.LongContext = nil
	}
	version.PreservedLongContext = modelPriceLongContextAuditForAPI(version.PreservedLongContext)
	return version
}

// listModelPriceVersionsForPrice returns the ordered version history for a
// channel price. Library prices have no versions and return an empty list.
func (a *App) listModelPriceVersionsForPrice(ctx context.Context, id int) ([]ModelPriceVersion, error) {
	price, err := a.getPrice(ctx, id)
	if err != nil {
		return nil, err
	}
	versions := []ModelPriceVersion{}
	if price.PriceScope != modelPriceScopeChannel || price.ChannelBrand == nil || price.ChannelKey == nil {
		return versions, nil
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, effective_at, baseline,
      input_usd_per_million, output_usd_per_million, cache_read_usd_per_million, cache_creation_usd_per_million,
      request_usd, billing_unit, priority_multiplier, long_context_threshold_tokens,
      long_context_input_usd_per_million, long_context_output_usd_per_million,
      long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million, time_pricing
      FROM model_price_versions
		WHERE (price_id = ? OR (price_id IS NULL AND COALESCE(channel_auth_type, 'apikey') = ? AND channel_brand = ? AND channel_key = ? AND model = ?
		  AND NOT EXISTS (SELECT 1 FROM model_price_versions current_lifecycle WHERE current_lifecycle.price_id = ?)))
      ORDER BY effective_at ASC, id ASC`,
		price.ID, modelPriceChannelAuthType(price), *price.ChannelBrand, *price.ChannelKey, price.Model, price.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v ModelPriceVersion
		var effective string
		var baseline int
		var req, pri, li, lo, lcr, lcc sql.NullFloat64
		var threshold sql.NullInt64
		var rule sql.NullString
		if err := rows.Scan(&v.ID, &effective, &baseline, &v.InputUSDPerMillion, &v.OutputUSDPerMillion, &v.CacheReadUSDPerMillion, &v.CacheCreationUSDPerMillion,
			&req, &v.BillingUnit, &pri, &threshold, &li, &lo, &lcr, &lcc, &rule); err != nil {
			return nil, err
		}
		v.TimePricing, err = parsePriceTimeRule(rule)
		if err != nil {
			return nil, err
		}
		t, ok := parseDBTime(effective)
		if !ok {
			return nil, fmt.Errorf("parse model price version effective_at %q", effective)
		}
		v.EffectiveAt = t
		v.Baseline = baseline != 0
		if req.Valid {
			v.RequestUSD = &req.Float64
		}
		if pri.Valid {
			v.PriorityMultiplier = &pri.Float64
		}
		longContextConfigured := threshold.Valid || li.Valid || lo.Valid || lcr.Valid || lcc.Valid
		if longContextConfigured {
			candidate := &ModelPriceLongContext{ThresholdInputTokens: threshold.Int64, InputUSDPerMillion: li.Float64, OutputUSDPerMillion: lo.Float64, CacheReadUSDPerMillion: lcr.Float64, CacheCreationUSDPerMillion: lcc.Float64}
			if threshold.Valid && li.Valid && lo.Valid && lcr.Valid && lcc.Valid && validLongContextPrice(candidate) {
				v.LongContext = candidate
			} else {
				v.PreservedLongContext = &ModelPriceLibraryConflictLongContext{
					ThresholdInputTokens:       nullableInt64(threshold),
					InputUSDPerMillion:         nullableFloat(li),
					OutputUSDPerMillion:        nullableFloat(lo),
					CacheReadUSDPerMillion:     nullableFloat(lcr),
					CacheCreationUSDPerMillion: nullableFloat(lcc),
				}
			}
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return versions, nil
}

func resolveVersionedPrice(record UsageRecord, price *ModelPrice, versions modelPriceVersionIndex) *ModelPrice {
	if price == nil || price.PriceScope != modelPriceScopeChannel || versions == nil {
		return price
	}
	if price.ChannelBrand == nil || price.ChannelKey == nil {
		return price
	}
	key := channelModelPriceKey(modelPriceChannelAuthType(*price), *price.ChannelBrand, *price.ChannelKey, price.Model)
	chain := versions[key]
	if chain == nil {
		return price
	}
	history := chain.all
	if price.ID > 0 && len(chain.byPriceID) > 0 {
		history = chain.byPriceID[int64(price.ID)]
	}
	if len(history) == 0 {
		return price
	}
	// Only the channel's first lifecycle may price requests before its first
	// save; a recreated price must not reach back into an earlier lifecycle.
	if record.Timestamp.Before(history[0].EffectiveAt) && !history[0].Baseline && history[0] != chain.all[0] {
		return nil
	}
	latest := sort.Search(len(history), func(i int) bool {
		return record.Timestamp.Before(history[i].EffectiveAt)
	}) - 1
	if latest < 0 {
		latest = 0 // The first price or a baseline estimates earlier requests.
	}
	selected := history[latest].Price
	return &selected
}

func recordCostWithBilling(record UsageRecord, pricing modelPriceBillingIndex) (float64, bool) {
	return recordCostWithVersions(record, pricing.Prices, pricing.Versions, pricing.MatchContext)
}

// recordCostWithVersions keeps recordCost's variadic contract: with no match
// context supplied, matching runs without configured-selector requirements.
// An explicit empty context is not equivalent to omitting it.
func recordCostWithVersions(record UsageRecord, prices modelPriceIndex, versions modelPriceVersionIndex, matchContexts ...modelPriceMatchContext) (float64, bool) {
	matched, status := findMatchingChannelPrice(prices, record, matchContexts...)
	if status != priceMatchStatusMatched {
		fallback := calculateRecordCost(record, prices, false, matchContexts...)
		return fallback.TotalUSD, fallback.Unpriced
	}
	brand := matchedModelPriceChannelBrand(matched, record, matchContexts...)
	price := resolveVersionedPrice(record, matched, versions)
	out := calculateRecordCostForMatch(record, price, status, brand, false, matched)
	return out.TotalUSD, out.Unpriced
}

func versionedCostBreakdown(record UsageRecord, pricing modelPriceBillingIndex, collectItems bool) usageCostBreakdown {
	matched, status := findMatchingChannelPrice(pricing.Prices, record, pricing.MatchContext)
	brand := matchedModelPriceChannelBrand(matched, record, pricing.MatchContext)
	price := resolveVersionedPrice(record, matched, pricing.Versions)
	return calculateRecordCostForMatch(record, price, status, brand, collectItems, matched)
}
