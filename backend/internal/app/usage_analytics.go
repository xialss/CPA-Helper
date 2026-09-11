package app

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type usageAnalyticsCollectorOptions struct {
	Summary       bool
	Trends        bool
	Distributions bool
	Rankings      map[string]string
}

type usageAnalyticsCollector struct {
	filters       UsageFilters
	prices        modelPriceIndex
	versions      modelPriceVersionIndex
	matchContext  modelPriceMatchContext
	users         map[string]userInfo
	priceMatches  map[usageAnalyticsPriceMatchKey]usageAnalyticsPriceMatch
	userSummaries *userUsageSummaryAccumulator
	summary       *usageSummaryAccumulator
	trends        *usageTrendAccumulator
	rankings      map[string]*usageRankingAccumulator
	distributions *usageDistributionAccumulator
}

func newUsageAnalyticsCollector(filters UsageFilters, prices modelPriceIndex, matchContext modelPriceMatchContext, users map[string]userInfo, options usageAnalyticsCollectorOptions) *usageAnalyticsCollector {
	collector := &usageAnalyticsCollector{
		filters:      filters,
		prices:       prices,
		versions:     modelPriceVersionIndex{},
		matchContext: matchContext,
		users:        users,
		priceMatches: map[usageAnalyticsPriceMatchKey]usageAnalyticsPriceMatch{},
		rankings:     map[string]*usageRankingAccumulator{},
	}
	if options.Summary {
		collector.summary = &usageSummaryAccumulator{}
	}
	if options.Trends {
		collector.trends = newUsageTrendAccumulator(filters)
	}
	for groupBy, sortBy := range options.Rankings {
		collector.rankings[groupBy] = newUsageRankingAccumulator(groupBy, sortBy, users)
	}
	if options.Distributions {
		collector.distributions = newUsageDistributionAccumulator(matchContext)
	}
	return collector
}

func (c *usageAnalyticsCollector) setBillingPriceIndex(pricing modelPriceBillingIndex) {
	c.prices = pricing.Prices
	c.versions = pricing.Versions
	c.matchContext = pricing.MatchContext
	c.priceMatches = map[usageAnalyticsPriceMatchKey]usageAnalyticsPriceMatch{}
	if c.distributions != nil {
		c.distributions.matchContext = pricing.MatchContext
	}
}

func (c *usageAnalyticsCollector) Add(record UsageRecord) {
	matchKey := usageAnalyticsPriceMatchKeyForRecord(record)
	match, ok := c.priceMatches[matchKey]
	if !ok {
		matchedPrice, matchStatus := findMatchingChannelPrice(c.prices, record, c.matchContext)
		match = usageAnalyticsPriceMatch{
			price:  matchedPrice,
			status: matchStatus,
			brand:  matchedModelPriceChannelBrand(matchedPrice, record, c.matchContext),
		}
		c.priceMatches[matchKey] = match
	}
	versionedPrice := resolveVersionedPrice(record, match.price, c.versions)
	breakdown := calculateRecordCostForMatch(record, versionedPrice, match.status, match.brand, false, match.price)
	value := usageAnalyticsRecord{
		record:               record,
		matchedPrice:         match.price,
		matchStatus:          match.status,
		matchedBrand:         match.brand,
		normalInputTokens:    breakdown.NormalInputTokens,
		cacheReadTokens:      breakdown.CacheReadTokens,
		cacheCreationTokens:  breakdown.CacheCreationTokens,
		aggregateInputTokens: breakdown.ContextInputTokens,
		aggregateTotalTokens: usageAggregateTotalTokens(record, match.brand),
		estimatedCostUSD:     breakdown.TotalUSD,
		unpriced:             breakdown.Unpriced,
	}
	if c.summary != nil {
		c.summary.add(value)
	}
	if c.userSummaries != nil {
		c.userSummaries.add(value)
	}
	if c.trends != nil {
		c.trends.add(value)
	}
	for _, ranking := range c.rankings {
		ranking.add(value)
	}
	if c.distributions != nil {
		c.distributions.add(value)
	}
}

type usageAnalyticsPriceMatchKey struct {
	provider  string
	model     string
	auth      string
	authIndex string
}

type usageAnalyticsPriceMatch struct {
	price  *ModelPrice
	status string
	brand  *aiProviderBrand
}

func usageAnalyticsPriceMatchKeyForRecord(record UsageRecord) usageAnalyticsPriceMatchKey {
	return usageAnalyticsPriceMatchKey{
		provider:  usageAnalyticsOptionalString(record.Provider),
		model:     usageAnalyticsOptionalString(record.Model),
		auth:      usageAnalyticsOptionalString(usageRecordAuth(record)),
		authIndex: usageAnalyticsOptionalString(record.AuthIndex),
	}
}

func usageAnalyticsOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (c *usageAnalyticsCollector) summaryResponse() map[string]any {
	if c.summary == nil {
		return nil
	}
	return c.summary.response(c.filters)
}

func (c *usageAnalyticsCollector) trendResponse() []map[string]any {
	if c.trends == nil {
		return nil
	}
	return c.trends.response()
}

func (c *usageAnalyticsCollector) rankingResponse(groupBy string) map[string]any {
	ranking := c.rankings[groupBy]
	if ranking == nil {
		return map[string]any{"group_by": groupBy, "items": []any{}}
	}
	return ranking.response()
}

func (c *usageAnalyticsCollector) distributionResponse() map[string]any {
	if c.distributions == nil {
		return nil
	}
	return c.distributions.response()
}

type usageAnalyticsRecord struct {
	record               UsageRecord
	matchedPrice         *ModelPrice
	matchStatus          string
	matchedBrand         *aiProviderBrand
	normalInputTokens    int
	cacheReadTokens      int
	cacheCreationTokens  int
	aggregateInputTokens int
	aggregateTotalTokens int
	estimatedCostUSD     float64
	unpriced             bool
}

type usageSummaryAccumulator struct {
	records       int
	failed        int
	input         int
	output        int
	cached        int
	reasoning     int
	total         int
	normalInput   int
	cacheRead     int
	cacheCreation int
	estimated     float64
	unpriced      int
	ttftTotal     float64
	ttftCount     int
}

func (a *usageSummaryAccumulator) add(value usageAnalyticsRecord) {
	a.records++
	if value.record.Failed {
		a.failed++
	}
	a.input += value.aggregateInputTokens
	a.output += value.record.OutputTokens
	a.cached += value.record.CachedTokens
	a.reasoning += value.record.ReasoningTokens
	a.total += value.aggregateTotalTokens
	a.normalInput += value.normalInputTokens
	a.cacheRead += value.cacheReadTokens
	a.cacheCreation += value.cacheCreationTokens
	a.estimated = mathRound(a.estimated+value.estimatedCostUSD, 8)
	if value.unpriced {
		a.unpriced++
	}
	if value.record.TTFTMS != nil && *value.record.TTFTMS > 0 {
		a.ttftTotal += *value.record.TTFTMS
		a.ttftCount++
	}
}

func (a *usageSummaryAccumulator) response(filters UsageFilters) map[string]any {
	if filters.Start == nil || filters.End == nil {
		start, end := defaultTodayRange()
		filters.Start = &start
		filters.End = &end
	}
	return map[string]any{
		"start":                 usageAPITimePtr(filters.Start),
		"end":                   usageAPITimePtr(filters.End),
		"total_records":         a.records,
		"failed_records":        a.failed,
		"success_records":       a.records - a.failed,
		"input_tokens":          a.input,
		"output_tokens":         a.output,
		"cached_tokens":         a.cached,
		"normal_input_tokens":   a.normalInput,
		"cache_read_tokens":     a.cacheRead,
		"cache_creation_tokens": a.cacheCreation,
		"reasoning_tokens":      a.reasoning,
		"total_tokens":          a.total,
		"estimated_cost_usd":    a.estimated,
		"unpriced_records":      a.unpriced,
		"average_ttft_ms":       averageTTFTMS(a.ttftTotal, a.ttftCount),
	}
}

type usageTrendAccumulator struct {
	hourly  bool
	buckets map[string]*usageTrendBucket
}

type usageTrendBucket struct {
	records int
	failed  int
	tokens  int
	cost    float64
}

func newUsageTrendAccumulator(filters UsageFilters) *usageTrendAccumulator {
	duration := 24 * time.Hour
	if filters.Start != nil && filters.End != nil {
		duration = filters.End.Sub(*filters.Start)
	}
	return &usageTrendAccumulator{
		hourly:  duration <= 48*time.Hour,
		buckets: map[string]*usageTrendBucket{},
	}
}

func (a *usageTrendAccumulator) add(value usageAnalyticsRecord) {
	bucket := value.record.Timestamp.In(appTimeLocation).Format("2006-01-02")
	if a.hourly {
		bucket = value.record.Timestamp.In(appTimeLocation).Format("2006-01-02 15:00")
	}
	item := a.buckets[bucket]
	if item == nil {
		item = &usageTrendBucket{}
		a.buckets[bucket] = item
	}
	item.records++
	if value.record.Failed {
		item.failed++
	}
	item.tokens += value.aggregateTotalTokens
	item.cost = mathRound(item.cost+value.estimatedCostUSD, 8)
}

func (a *usageTrendAccumulator) response() []map[string]any {
	keys := make([]string, 0, len(a.buckets))
	for key := range a.buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	points := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		item := a.buckets[key]
		points = append(points, map[string]any{
			"bucket":             key,
			"records":            item.records,
			"failed_records":     item.failed,
			"total_tokens":       item.tokens,
			"estimated_cost_usd": item.cost,
		})
	}
	return points
}

type usageRankingAccumulator struct {
	groupBy string
	sortBy  string
	users   map[string]userInfo
	groups  map[string]*usageRankingGroup
}

type usageRankingGroup struct {
	key         string
	label       string
	userID      *int
	description *string
	records     int
	failed      int
	tokens      int
	cost        float64
}

func newUsageRankingAccumulator(groupBy, sortBy string, users map[string]userInfo) *usageRankingAccumulator {
	return &usageRankingAccumulator{
		groupBy: groupBy,
		sortBy:  sortBy,
		users:   users,
		groups:  map[string]*usageRankingGroup{},
	}
}

func (a *usageRankingAccumulator) add(value usageAnalyticsRecord) {
	key, label := "", ""
	userID := (*int)(nil)
	description := (*string)(nil)
	switch a.groupBy {
	case "model":
		provider := valueOr(value.record.Provider, "unknown")
		model := valueOr(value.record.Model, "unknown")
		key = provider + "::" + model
		label = provider + " / " + model
	case "user":
		if value.record.UsageUsername == nil {
			return
		}
		info, ok := a.users[*value.record.UsageUsername]
		if !ok {
			key = *value.record.UsageUsername
			label = *value.record.UsageUsername
		} else {
			key = strconv.Itoa(info.ID)
			label = info.Name
			id := info.ID
			userID = &id
		}
	default:
		descriptionValue := ""
		if value.record.APIKeyDescription != nil {
			descriptionValue = strings.TrimSpace(*value.record.APIKeyDescription)
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
	group := a.groups[key]
	if group == nil {
		group = &usageRankingGroup{
			key:         key,
			label:       label,
			userID:      userID,
			description: description,
		}
		a.groups[key] = group
	}
	group.records++
	if value.record.Failed {
		group.failed++
	}
	group.tokens += value.aggregateTotalTokens
	group.cost = mathRound(group.cost+value.estimatedCostUSD, 8)
}

func (a *usageRankingAccumulator) response() map[string]any {
	items := make([]map[string]any, 0, len(a.groups))
	for _, group := range a.groups {
		items = append(items, rankingItem(group.key, group.label, group.records, group.failed, group.tokens, group.cost, group.userID, group.description))
	}
	sort.Slice(items, func(i, j int) bool {
		leftMetric := rankingItemSortMetric(items[i], a.sortBy)
		rightMetric := rankingItemSortMetric(items[j], a.sortBy)
		if leftMetric != rightMetric {
			return leftMetric > rightMetric
		}
		leftTokens := items[i]["total_tokens"].(int)
		rightTokens := items[j]["total_tokens"].(int)
		if leftTokens != rightTokens {
			return leftTokens > rightTokens
		}
		leftRecords := items[i]["records"].(int)
		rightRecords := items[j]["records"].(int)
		if leftRecords != rightRecords {
			return leftRecords > rightRecords
		}
		return items[i]["label"].(string) < items[j]["label"].(string)
	})
	if len(items) > 20 {
		items = items[:20]
	}
	return map[string]any{"group_by": a.groupBy, "items": items}
}

type usageDistributionAccumulator struct {
	matchContext modelPriceMatchContext
	providers    map[string]*usageDistributionGroup
	models       map[string]*usageDistributionGroup
	endpoints    map[string]*usageDistributionGroup
	channelCosts map[modelPriceChannelGroupIdentity]usageChannelCostItem
}

type usageDistributionGroup struct {
	key     string
	records int
	tokens  int
	cost    float64
}

func newUsageDistributionAccumulator(matchContext modelPriceMatchContext) *usageDistributionAccumulator {
	return &usageDistributionAccumulator{
		matchContext: matchContext,
		providers:    map[string]*usageDistributionGroup{},
		models:       map[string]*usageDistributionGroup{},
		endpoints:    map[string]*usageDistributionGroup{},
		channelCosts: map[modelPriceChannelGroupIdentity]usageChannelCostItem{},
	}
}

func (a *usageDistributionAccumulator) add(value usageAnalyticsRecord) {
	a.addGroup(a.providers, valueOr(value.record.Provider, "unknown"), value)
	a.addGroup(a.models, valueOr(value.record.Model, "unknown"), value)
	a.addGroup(a.endpoints, valueOr(value.record.Endpoint, "unknown"), value)
	if value.matchStatus != priceMatchStatusMatched || value.matchedPrice == nil || value.unpriced || value.estimatedCostUSD <= 0 {
		return
	}
	identity, ok := modelPriceChannelGroupIdentityForPrice(*value.matchedPrice)
	if !ok {
		return
	}
	item := a.channelCosts[identity]
	if item.Key == "" {
		display := modelPriceChannelDisplayForPrice(*value.matchedPrice, a.matchContext)
		label := display.Label
		if display.LabelFallback {
			label = ""
		}
		item = usageChannelCostItem{
			Key:             hashAPIKey("usage-channel-cost\x00" + identity.AuthType + "\x00" + string(identity.Brand) + "\x00" + identity.ChannelKey),
			Label:           label,
			LabelFallback:   display.LabelFallback,
			ChannelAuthType: identity.AuthType,
			ChannelBrand:    string(identity.Brand),
		}
	}
	item.EstimatedCostUSD = mathRound(item.EstimatedCostUSD+value.estimatedCostUSD, 8)
	a.channelCosts[identity] = item
}

func (a *usageDistributionAccumulator) addGroup(groups map[string]*usageDistributionGroup, key string, value usageAnalyticsRecord) {
	group := groups[key]
	if group == nil {
		group = &usageDistributionGroup{key: key}
		groups[key] = group
	}
	group.records++
	group.tokens += value.aggregateTotalTokens
	group.cost = mathRound(group.cost+value.estimatedCostUSD, 8)
}

func (a *usageDistributionAccumulator) response() map[string]any {
	return map[string]any{
		"providers":     usageDistributionItems(a.providers),
		"models":        usageDistributionItems(a.models),
		"endpoints":     usageDistributionItems(a.endpoints),
		"channel_costs": usageChannelCostItems(a.channelCosts),
	}
}

func usageDistributionItems(groups map[string]*usageDistributionGroup) []map[string]any {
	items := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, map[string]any{
			"key":                group.key,
			"label":              group.key,
			"records":            group.records,
			"total_tokens":       group.tokens,
			"estimated_cost_usd": group.cost,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["records"].(int) > items[j]["records"].(int) })
	if len(items) > 20 {
		items = items[:20]
	}
	return items
}

func usageChannelCostItems(itemsByIdentity map[modelPriceChannelGroupIdentity]usageChannelCostItem) []usageChannelCostItem {
	items := make([]usageChannelCostItem, 0, len(itemsByIdentity))
	for _, item := range itemsByIdentity {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EstimatedCostUSD != items[j].EstimatedCostUSD {
			return items[i].EstimatedCostUSD > items[j].EstimatedCostUSD
		}
		if items[i].Label != items[j].Label {
			return items[i].Label < items[j].Label
		}
		return items[i].Key < items[j].Key
	})
	return items
}
