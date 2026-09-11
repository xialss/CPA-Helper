package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata"
)

type PricePeakWindow struct {
	Weekdays []int  `json:"weekdays"`
	Start    string `json:"start"`
	End      string `json:"end"`
}
type PriceOffpeakRates struct {
	Input         float64 `json:"input_usd_per_million"`
	Output        float64 `json:"output_usd_per_million"`
	CacheRead     float64 `json:"cache_read_usd_per_million"`
	CacheCreation float64 `json:"cache_creation_usd_per_million"`
}

// A rule is a snapshot, never a reference to the mutable administrator template.
type PriceTimeRule struct {
	Timezone              string             `json:"timezone"`
	PeakWindows           []PricePeakWindow  `json:"peak_windows"`
	OffpeakMode           string             `json:"offpeak_mode"`
	OffpeakMultiplier     float64            `json:"offpeak_multiplier"`
	OffpeakRates          *PriceOffpeakRates `json:"offpeak_rates,omitempty"`
	LongContextMultiplier float64            `json:"long_context_multiplier"`
	location              *time.Location
	peakMinutes           [158]uint64
}

func (r *PriceTimeRule) UnmarshalJSON(data []byte) error {
	type plain PriceTimeRule
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	required := []string{"timezone", "peak_windows", "offpeak_mode", "long_context_multiplier"}
	if value.OffpeakMode == "multiplier" {
		required = append(required, "offpeak_multiplier")
	}
	for _, key := range required {
		if raw, ok := fields[key]; !ok || string(raw) == "null" {
			return validationError("峰谷规则缺少字段: " + key)
		}
	}
	if value.OffpeakMode == "explicit" {
		var rates map[string]json.RawMessage
		if err := json.Unmarshal(fields["offpeak_rates"], &rates); err != nil {
			return validationError("请完整设置空闲价格")
		}
		for _, key := range []string{"input_usd_per_million", "output_usd_per_million", "cache_read_usd_per_million", "cache_creation_usd_per_million"} {
			if raw, ok := rates[key]; !ok || string(raw) == "null" {
				return validationError("空闲价格缺少字段: " + key)
			}
		}
	}
	*r = PriceTimeRule(value)
	return nil
}

func priceClockMinute(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || t.Format("15:04") != s {
		return 0, validationError("峰谷时段时间必须为 HH:mm")
	}
	return t.Hour()*60 + t.Minute(), nil
}
func validatePriceTimeRule(r *PriceTimeRule) error {
	if r == nil {
		return nil
	}
	if r.Timezone == "" || r.Timezone == "Local" {
		return validationError("请选择明确的 IANA 时区")
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return validationError("峰谷时区无效")
	}
	r.location = loc
	r.peakMinutes = [158]uint64{}
	if len(r.PeakWindows) == 0 {
		return validationError("至少配置一个高峰时段")
	}
	for _, w := range r.PeakWindows {
		start, err := priceClockMinute(w.Start)
		if err != nil {
			return err
		}
		end, err := priceClockMinute(w.End)
		if err != nil {
			return err
		}
		if start == end || len(w.Weekdays) == 0 {
			return validationError("高峰时段必须有星期且开始结束不可相同")
		}
		if end < start {
			end += 1440
		}
		for _, day := range w.Weekdays {
			if day < 0 || day > 6 {
				return validationError("星期必须在 0 到 6 之间")
			}
			for minute := start; minute < end; minute++ {
				idx := (day*1440 + minute) % (7 * 1440)
				mask := uint64(1) << uint(idx%64)
				if r.peakMinutes[idx/64]&mask != 0 {
					return validationError("高峰时段不可重叠（包括跨午夜时段）")
				}
				r.peakMinutes[idx/64] |= mask
			}
		}
	}
	if !finiteNonNegative(r.LongContextMultiplier) {
		return validationError("长上下文空闲倍率必须为非负有限数")
	}
	if !finiteNonNegative(r.OffpeakMultiplier) {
		return validationError("空闲倍率必须为非负有限数")
	}
	if v := r.OffpeakRates; v != nil && (!finiteNonNegative(v.Input) || !finiteNonNegative(v.Output) || !finiteNonNegative(v.CacheRead) || !finiteNonNegative(v.CacheCreation)) {
		return validationError("空闲价格必须为非负有限数")
	}
	switch r.OffpeakMode {
	case "multiplier":
		if !finiteNonNegative(r.OffpeakMultiplier) {
			return validationError("空闲倍率必须为非负有限数")
		}
	case "explicit":
		v := r.OffpeakRates
		if v == nil || !finiteNonNegative(v.Input) || !finiteNonNegative(v.Output) || !finiteNonNegative(v.CacheRead) || !finiteNonNegative(v.CacheCreation) {
			return validationError("请完整设置空闲输入、输出和缓存价格")
		}
	default:
		return validationError("空闲定价方式无效")
	}
	return nil
}
func validateTimeRuleForPrice(p ModelPrice) error {
	if p.TimePricing == nil {
		return nil
	}
	if !supportsPriceTimeRule(p) {
		return validationError("峰谷定价仅支持 DeepSeek 渠道 Token 价格")
	}
	return validatePriceTimeRule(p.TimePricing)
}
func supportsPriceTimeRule(p ModelPrice) bool {
	name := strings.ToLower(normalizeModelPriceChannelModel(p.Model))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return p.PriceScope == modelPriceScopeChannel && p.ChannelBrand != nil && p.ChannelKey != nil &&
		strings.TrimSpace(*p.ChannelBrand) != "" && strings.TrimSpace(*p.ChannelKey) != "" &&
		strings.HasPrefix(name, "deepseek-") && p.BillingUnit == modelBillingUnitToken
}
func priceTimeRuleJSON(rule *PriceTimeRule) any {
	if rule == nil {
		return nil
	}
	return priceTimeSQLValue{rule}
}

type priceTimeSQLValue struct{ rule *PriceTimeRule }

func (v priceTimeSQLValue) Value() (driver.Value, error) {
	b, err := json.Marshal(v.rule)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
func parsePriceTimeRule(raw sql.NullString) (*PriceTimeRule, error) {
	if !raw.Valid {
		return nil, nil
	}
	var r PriceTimeRule
	if err := json.Unmarshal([]byte(raw.String), &r); err != nil {
		return nil, fmt.Errorf("decode price time rule: %w", err)
	}
	if err := validatePriceTimeRule(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
func priceForRequestTime(p *ModelPrice, at time.Time) *ModelPrice {
	if p == nil || p.TimePricing == nil {
		return p
	}
	r := p.TimePricing
	if r.location == nil {
		// The rule reached the pricing path without passing through
		// parsePriceTimeRule/validatePriceTimeRule (e.g. a plain
		// json.Unmarshal or struct literal). Build the derived fields on a
		// private copy so shared rule pointers are never mutated concurrently.
		// An invalid rule fails closed to the peak price instead of panicking
		// or silently granting the off-peak discount.
		prepared := *r
		if err := validatePriceTimeRule(&prepared); err != nil {
			return p
		}
		r = &prepared
	}
	local := at.In(r.location)
	idx := int(local.Weekday())*1440 + local.Hour()*60 + local.Minute()
	if r.peakMinutes[idx/64]&(uint64(1)<<uint(idx%64)) != 0 {
		return p
	}
	copy := *p
	if r.OffpeakMode == "explicit" {
		// Explicit zero is a free cache-write rate, not the legacy unset sentinel.
		copy.cacheCreationExplicit = true
		copy.InputUSDPerMillion = r.OffpeakRates.Input
		copy.OutputUSDPerMillion = r.OffpeakRates.Output
		copy.CacheReadUSDPerMillion = r.OffpeakRates.CacheRead
		copy.CacheCreationUSDPerMillion = r.OffpeakRates.CacheCreation
	} else {
		copy.InputUSDPerMillion *= r.OffpeakMultiplier
		copy.OutputUSDPerMillion *= r.OffpeakMultiplier
		copy.CacheReadUSDPerMillion *= r.OffpeakMultiplier
		copy.CacheCreationUSDPerMillion *= r.OffpeakMultiplier
	}
	if p.LongContext != nil {
		tier := *p.LongContext
		tier.InputUSDPerMillion *= r.LongContextMultiplier
		tier.OutputUSDPerMillion *= r.LongContextMultiplier
		tier.CacheReadUSDPerMillion *= r.LongContextMultiplier
		tier.CacheCreationUSDPerMillion *= r.LongContextMultiplier
		copy.LongContext = &tier
	}
	return &copy
}

func (a *App) handleDeepSeekTimeTemplate(w http.ResponseWriter, r *http.Request, path string) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if path == "deepseek-template" {
		switch r.Method {
		case http.MethodGet:
			var raw sql.NullString
			if err := a.db.QueryRowContext(r.Context(), `SELECT rule FROM model_price_time_templates WHERE name='deepseek'`).Scan(&raw); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return notFoundError("峰谷时段模板不存在")
				}
				return err
			}
			rule, err := parsePriceTimeRule(raw)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, rule)
			return nil
		case http.MethodPut:
			var rule PriceTimeRule
			if err := decodeJSON(r, &rule); err != nil {
				return err
			}
			if err := validatePriceTimeRule(&rule); err != nil {
				return err
			}
			// Upsert so a missing seed row is recreated instead of an UPDATE that
			// touches 0 rows and reports a false success.
			result, err := a.db.ExecContext(r.Context(), `INSERT INTO model_price_time_templates(name, rule) VALUES ('deepseek', ?)
				ON CONFLICT(name) DO UPDATE SET rule=excluded.rule`, priceTimeRuleJSON(&rule))
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				return fmt.Errorf("deepseek time pricing template was not persisted")
			}
			writeJSON(w, http.StatusOK, rule)
			return nil
		default:
			return requireMethod(r, http.MethodGet, http.MethodPut)
		}
	}
	if path != "deepseek-template/preview" && path != "deepseek-template/apply" {
		return notFoundError("接口不存在")
	}
	if err := requireMethod(r, http.MethodPost); err != nil {
		return err
	}
	var payload priceTimeBatchPayload
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	result, err := a.applyPriceTimeBatch(r.Context(), payload, path == "deepseek-template/apply")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

type priceTimeBatchPayload struct {
	PriceIDs    []int          `json:"price_ids"`
	Rule        *PriceTimeRule `json:"rule"`
	EffectiveAt *time.Time     `json:"effective_at"`
}

func (p *priceTimeBatchPayload) UnmarshalJSON(data []byte) error {
	type plain priceTimeBatchPayload
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, ok := fields["rule"]; !ok {
		return validationError("请明确提供峰谷规则；null 表示关闭")
	}
	*p = priceTimeBatchPayload(value)
	return nil
}

// priceTimeBatchMaxIDs bounds a single batch so one transaction cannot hold
// the SQLite write lock across thousands of version inserts.
const priceTimeBatchMaxIDs = 200

type priceTimeBatchItem struct {
	Before ModelPrice `json:"before"`
	After  ModelPrice `json:"after"`
}

func (a *App) applyPriceTimeBatch(ctx context.Context, payload priceTimeBatchPayload, apply bool) (any, error) {
	if len(payload.PriceIDs) == 0 {
		return nil, validationError("请选择渠道模型价格")
	}
	if len(payload.PriceIDs) > priceTimeBatchMaxIDs {
		return nil, validationError(fmt.Sprintf("单次最多批量应用 %d 个渠道模型价格", priceTimeBatchMaxIDs))
	}
	if err := validatePriceTimeRule(payload.Rule); err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The only SQLite connection can be busy with another price save. Resolve
	// immediate and scheduled times only after acquiring its committed state.
	now := time.Now()
	effective := now
	if payload.EffectiveAt != nil {
		effective = *payload.EffectiveAt
		if effective.Before(now) {
			return nil, validationError("生效时间不能早于当前时间")
		}
	}
	effective = effective.Truncate(time.Microsecond)
	versions, err := loadModelPriceVersionsWithQueryer(ctx, tx)
	if err != nil {
		return nil, err
	}
	items := []priceTimeBatchItem{}
	seen := map[int]bool{}
	for _, id := range payload.PriceIDs {
		if seen[id] {
			return nil, validationError("渠道价格不可重复")
		}
		seen[id] = true
		p, err := getPriceWithQuerier(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		p = effectivePriceFromVersions(p, versions, effective)
		after := p
		after.TimePricing = payload.Rule
		// Disabling also requires a DeepSeek channel target.
		if !supportsPriceTimeRule(after) {
			return nil, validationError("请选择 DeepSeek 渠道 Token 价格")
		}
		if err := validateTimeRuleForPrice(after); err != nil {
			return nil, err
		}
		items = append(items, priceTimeBatchItem{Before: p, After: after})
	}
	if apply {
		for _, item := range items {
			key := channelModelPriceKey(modelPriceChannelAuthType(item.Before), *item.Before.ChannelBrand, *item.Before.ChannelKey, item.Before.Model)
			history := versions[key]
			if history == nil || len(history.byPriceID[int64(item.Before.ID)]) == 0 {
				if err := a.appendModelPriceVersion(ctx, tx, item.Before, now, true); err != nil {
					return nil, err
				}
			}
			if err := a.appendModelPriceVersion(ctx, tx, item.After, effective, false); err != nil {
				return nil, err
			}
			// Invalidate analytics without materializing future amounts in the current row.
			if _, err := tx.ExecContext(ctx, `UPDATE model_prices SET updated_at=? WHERE id=?`, dbTime(now), item.After.ID); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	// Keep legacy billing sentinels in the persisted snapshots, and sanitize
	// only the response copies, as with the price list and version endpoints.
	for i := range items {
		items[i].Before = modelPriceForAPI(items[i].Before)
		items[i].After = modelPriceForAPI(items[i].After)
	}
	return map[string]any{"effective_at": dbTime(effective), "items": items, "applied": apply}, nil
}

func snapshotLongContextArgs(p ModelPrice) (threshold, input, output, read, write any) {
	threshold, input, output, read, write = nullableLongContextThreshold(p.LongContext), nullableLongContextInput(p.LongContext), nullableLongContextOutput(p.LongContext), nullableLongContextCacheRead(p.LongContext), nullableLongContextCacheCreation(p.LongContext)
	if p.longContextInvalid && p.PreservedLongContext != nil {
		v := p.PreservedLongContext
		threshold = nullableInt64PtrArg(v.ThresholdInputTokens)
		input = nullableFloatArg(v.InputUSDPerMillion)
		output = nullableFloatArg(v.OutputUSDPerMillion)
		read = nullableFloatArg(v.CacheReadUSDPerMillion)
		write = nullableFloatArg(v.CacheCreationUSDPerMillion)
	}
	return
}

func materializePriceSnapshot(ctx context.Context, tx *sql.Tx, p ModelPrice) error {
	threshold, input, output, read, write := snapshotLongContextArgs(p)
	_, err := tx.ExecContext(ctx, `UPDATE model_prices SET input_usd_per_million=?,output_usd_per_million=?,cache_read_usd_per_million=?,cache_creation_usd_per_million=?,request_usd=?,billing_unit=?,priority_multiplier=?,long_context_threshold_tokens=?,long_context_input_usd_per_million=?,long_context_output_usd_per_million=?,long_context_cache_read_usd_per_million=?,long_context_cache_creation_usd_per_million=?,time_pricing=? WHERE id=?`, p.InputUSDPerMillion, p.OutputUSDPerMillion, p.CacheReadUSDPerMillion, p.CacheCreationUSDPerMillion, nullableFloatArg(p.RequestUSD), p.BillingUnit, nullableFloatArg(p.PriorityMultiplier), threshold, input, output, read, write, priceTimeRuleJSON(p.TimePricing), p.ID)
	return err
}

func effectivePriceWithQueryer(ctx context.Context, q modelPriceQueryer, p ModelPrice, at time.Time) (ModelPrice, error) {
	if p.PriceScope != modelPriceScopeChannel {
		return p, nil
	}
	versions, err := loadModelPriceVersionsWithQueryer(ctx, q)
	if err != nil {
		return p, err
	}
	return effectivePriceFromVersions(p, versions, at), nil
}

// latestVersionedPrice overlays the newest recorded version (including a
// scheduled one) onto p; with no versions it returns p unchanged.
func latestVersionedPrice(p ModelPrice, versions modelPriceVersionIndex) ModelPrice {
	return effectivePriceFromVersions(p, versions, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC))
}

func effectivePriceFromVersions(p ModelPrice, versions modelPriceVersionIndex, at time.Time) ModelPrice {
	selected := resolveVersionedPrice(UsageRecord{Timestamp: at}, &p, versions)
	if selected == nil {
		return p
	}
	p.InputUSDPerMillion = selected.InputUSDPerMillion
	p.OutputUSDPerMillion = selected.OutputUSDPerMillion
	p.CacheReadUSDPerMillion = selected.CacheReadUSDPerMillion
	p.CacheCreationUSDPerMillion = selected.CacheCreationUSDPerMillion
	p.LongContext = selected.LongContext
	p.PriorityMultiplier = selected.PriorityMultiplier
	p.RequestUSD = selected.RequestUSD
	p.BillingUnit = selected.BillingUnit
	p.longContextInvalid = selected.longContextInvalid
	p.PreservedLongContext = selected.PreservedLongContext
	p.TimePricing = selected.TimePricing
	return p
}
