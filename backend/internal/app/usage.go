package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var usageEmailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

const (
	usageRankingSortTokens  = "tokens"
	usageRankingSortCost    = "cost"
	usageRankingSortRecords = "records"
)

type UsageFilters struct {
	Scope             string
	Start             *time.Time
	End               *time.Time
	UserID            *int
	UsageUsername     *string
	UsageUsernames    []string
	APIKeyDescription *string
	Provider          *string
	Model             *string
	SourceKey         *string
	Endpoint          *string
	Failed            *bool
	RequestID         *string
}

type UsageRecord struct {
	ID                  int
	Timestamp           time.Time
	UsageUsername       *string
	APIKeyDescription   *string
	Provider            *string
	Model               *string
	ResponseModel       *string
	RequestAlias        *string
	ServiceTier         *string
	ReasoningEffort     *string
	Endpoint            *string
	Source              *string
	SourceAccount       *string
	RequestID           *string
	Auth                *string
	AuthIndex           *string
	LatencyMS           *float64
	TTFTMS              *float64
	Failed              bool
	InputTokens         int
	OutputTokens        int
	CachedTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	ReasoningTokens     int
	TotalTokens         int
	DedupeKey           string
	RawJSON             string
	resolvedAuth        *string
	authResolved        bool
	analyticsSourceKey  *string
}

type usageChannelCostItem struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	LabelFallback    bool    `json:"label_fallback"`
	ChannelAuthType  string  `json:"channel_auth_type"`
	ChannelBrand     string  `json:"channel_brand"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

func usageAPITime(value time.Time) string {
	return apiDateTime(value)
}

func usageAPITimePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := usageAPITime(*value)
	return &formatted
}

type usageAccessScope struct {
	UserID   int
	Username string
	IsAdmin  bool
}

type usageRedactionOptions struct {
	MaskSource    bool
	MaskAuthIndex bool
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) error {
	user, err := a.readyUser(r.Context(), r)
	if err != nil {
		return err
	}
	parts := splitPath(r.URL.Path, "/api/usage/")
	if len(parts) == 0 {
		return notFoundError("Not Found")
	}
	filters, err := parseUsageFilters(r)
	if err != nil {
		return err
	}
	switch parts[0] {
	case "summary":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageSummary(w, r, filters, user)
	case "trends":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageTrends(w, r, filters, user)
	case "rankings":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageRankings(w, r, filters, user)
	case "distributions":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageDistributions(w, r, filters, user)
	case "overview":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageOverview(w, r, filters, user)
	case "records":
		if len(parts) == 3 && (parts[2] == "model-audit" || parts[2] == "cpa-log") {
			return a.handleUsageModelAudit(w, r, parts[1], parts[2])
		}
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		if len(parts) == 2 {
			id, err := parseIntPath(parts[1])
			if err != nil {
				return err
			}
			return a.usageRecordDetail(w, r, id, user)
		}
		return a.usageRecords(w, r, filters, user)
	case "options":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		return a.usageOptions(w, r, filters, user)
	default:
		return notFoundError("Not Found")
	}
}

func parseUsageFilters(r *http.Request) (UsageFilters, error) {
	query := r.URL.Query()
	filters := UsageFilters{Scope: query.Get("scope")}
	if filters.Scope != "" && filters.Scope != "admin" && filters.Scope != "account" {
		return filters, validationError("scope 参数无效")
	}
	if value := strings.TrimSpace(query.Get("start")); value != "" {
		parsed, err := parseQueryTime(value)
		if err != nil {
			return filters, validationError("start 时间格式无效")
		}
		filters.Start = &parsed
	}
	if value := strings.TrimSpace(query.Get("end")); value != "" {
		parsed, err := parseQueryTime(value)
		if err != nil {
			return filters, validationError("end 时间格式无效")
		}
		filters.End = &parsed
	}
	if value := strings.TrimSpace(query.Get("user_id")); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil {
			return filters, validationError("user_id 参数无效")
		}
		filters.UserID = &id
	}
	filters.APIKeyDescription = stringPtrFromQuery(query.Get("api_key_description"))
	filters.Provider = stringPtrFromQuery(query.Get("provider"))
	filters.Model = stringPtrFromQuery(query.Get("model"))
	filters.SourceKey = stringPtrFromQuery(query.Get("source_key"))
	filters.Endpoint = stringPtrFromQuery(query.Get("endpoint"))
	filters.RequestID = stringPtrFromQuery(query.Get("request_id"))
	if value := strings.TrimSpace(query.Get("failed")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return filters, validationError("failed 参数无效")
		}
		filters.Failed = &parsed
	}
	return filters, nil
}

func parseQueryTime(value string) (time.Time, error) {
	if parsed, ok := parseInputTime(value); ok {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

func stringPtrFromQuery(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func defaultTodayRange() (time.Time, time.Time) {
	now := time.Now().In(appTimeLocation)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, appTimeLocation)
	return start, start.Add(24 * time.Hour)
}

func normalizedUsageFilters(filters UsageFilters) UsageFilters {
	if filters.Start != nil && filters.End != nil {
		return filters
	}
	start, end := defaultTodayRange()
	if filters.Start == nil {
		filters.Start = &start
	}
	if filters.End == nil {
		filters.End = &end
	}
	return filters
}

func accessScope(user *AuthUser, requested string) usageAccessScope {
	accountScoped := requested == "account" || !user.IsAdmin
	return usageAccessScope{
		UserID:   user.ID,
		Username: user.Username,
		IsAdmin:  !accountScoped,
	}
}

func (a *App) scopedFilters(ctx context.Context, filters UsageFilters, scope usageAccessScope) (UsageFilters, error) {
	if !scope.IsAdmin {
		filters.UsageUsername = &scope.Username
		filters.UserID = &scope.UserID
		filters.SourceKey = nil
		return filters, nil
	}
	if filters.UserID != nil && filters.UsageUsername == nil {
		user, err := a.getUser(ctx, *filters.UserID)
		if err != nil {
			return filters, nil
		}
		filters.UsageUsername = &user.Username
	}
	return filters, nil
}

func (a *App) usageSummary(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(r.Context(), normalizedUsageFilters(filters), scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	collector := newUsageAnalyticsCollector(scoped, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Summary: true})
	if err := a.collectUsageAnalytics(r.Context(), scoped, pricing, collector, ""); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collector.summaryResponse())
	return nil
}

func (a *App) usageTrends(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(r.Context(), normalizedUsageFilters(filters), scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	collector := newUsageAnalyticsCollector(scoped, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Trends: true})
	if err := a.collectUsageAnalytics(r.Context(), scoped, pricing, collector, "timestamp ASC"); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collector.trendResponse())
	return nil
}

func (a *App) usageRankings(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" || groupBy == "api_key" {
		groupBy = "api_key_description"
	}
	if groupBy != "api_key_description" && groupBy != "model" && groupBy != "user" {
		return validationError("group_by 参数无效")
	}
	sortBy, ok := usageRankingSort(r.URL.Query().Get("sort_by"))
	if !ok {
		return validationError("sort_by 参数无效")
	}
	scope := accessScope(user, filters.Scope)
	if !scope.IsAdmin && groupBy == "user" {
		writeJSON(w, http.StatusOK, map[string]any{"group_by": "user", "items": []any{}})
		return nil
	}
	scoped, err := a.scopedFilters(r.Context(), filters, scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	users, err := a.userLookup(r.Context(), scope)
	if err != nil {
		return err
	}
	collector := newUsageAnalyticsCollector(scoped, pricing.Prices, pricing.MatchContext, users, usageAnalyticsCollectorOptions{Rankings: map[string]string{groupBy: sortBy}})
	if err := a.collectUsageAnalytics(r.Context(), scoped, pricing, collector, ""); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collector.rankingResponse(groupBy))
	return nil
}

func (a *App) usageDistributions(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(r.Context(), filters, scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	collector := newUsageAnalyticsCollector(scoped, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{Distributions: true})
	if err := a.collectUsageAnalytics(r.Context(), scoped, pricing, collector, ""); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collector.distributionResponse())
	return nil
}

func (a *App) usageOverview(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	primaryRankingSort, ok := usageRankingSort(r.URL.Query().Get("primary_ranking_sort"))
	if !ok {
		return validationError("primary_ranking_sort 参数无效")
	}
	modelRankingSort, ok := usageRankingSort(r.URL.Query().Get("model_ranking_sort"))
	if !ok {
		return validationError("model_ranking_sort 参数无效")
	}
	includeOptions, err := usageOverviewIncludesOptions(r)
	if err != nil {
		return err
	}
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(r.Context(), normalizedUsageFilters(filters), scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	users, err := a.userLookup(r.Context(), scope)
	if err != nil {
		return err
	}
	rankingSorts := map[string]string{
		"api_key_description": primaryRankingSort,
		"model":               modelRankingSort,
	}
	if scope.IsAdmin {
		rankingSorts["user"] = primaryRankingSort
	}
	collector := newUsageAnalyticsCollector(scoped, pricing.Prices, pricing.MatchContext, users, usageAnalyticsCollectorOptions{
		Summary:       true,
		Trends:        true,
		Distributions: true,
		Rankings:      rankingSorts,
	})
	if err := a.collectUsageAnalytics(r.Context(), scoped, pricing, collector, "timestamp ASC"); err != nil {
		return err
	}
	apiKeyRanking := collector.rankingResponse("api_key_description")
	userRanking := map[string]any{"group_by": "user", "items": []any{}}
	if scope.IsAdmin {
		userRanking = collector.rankingResponse("user")
	}
	response := map[string]any{
		"summary":                     collector.summaryResponse(),
		"trends":                      collector.trendResponse(),
		"user_ranking":                userRanking,
		"api_key_description_ranking": apiKeyRanking,
		"api_key_ranking":             apiKeyRanking,
		"model_ranking":               collector.rankingResponse("model"),
		"distributions":               collector.distributionResponse(),
	}
	if includeOptions {
		options, err := a.usageOptionsResponse(r.Context(), user, usageOptionFilters(filters))
		if err != nil {
			return err
		}
		response["options"] = options
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func usageOverviewIncludesOptions(r *http.Request) (bool, error) {
	value := strings.TrimSpace(r.URL.Query().Get("include_options"))
	if value == "" {
		return true, nil
	}
	includeOptions, err := strconv.ParseBool(value)
	if err != nil {
		return false, validationError("include_options 参数无效")
	}
	return includeOptions, nil
}

func (a *App) usageRecords(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	if err := validateUsageRecordRetention(r, filters, time.Now().In(appTimeLocation)); err != nil {
		return err
	}
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(r.Context(), filters, scope)
	if err != nil {
		return err
	}
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), 50)
	if pageSize > 200 {
		pageSize = 200
	}
	var total int
	var records []UsageRecord
	if hasUsagePostFilters(scoped) {
		total, records, err = a.filteredUsageRecordsPage(r.Context(), scoped, page, pageSize)
	} else {
		total, err = a.countUsageRecords(r.Context(), scoped)
		if err == nil {
			records, err = a.pagedUsageRecords(r.Context(), scoped, page, pageSize)
		}
	}
	if err != nil {
		return err
	}
	users, err := a.userLookup(r.Context(), scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(records))
	redaction := usageRedactionOptions{MaskAuthIndex: !scope.IsAdmin}
	var sourceLabels *usageSourceLabelContext
	if scope.IsAdmin {
		sourceLabels, err = a.usageSourceLabels(r.Context(), pricing.MatchContext)
		if err != nil {
			return err
		}
	}
	for _, record := range records {
		item := listItemFromRecordVersioned(record, users, pricing, redaction)
		item["source_label"] = sourceLabels.labelFor(record)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"start":     usageAPITimePtr(scoped.Start),
		"end":       usageAPITimePtr(scoped.End),
	})
	return nil
}

func (a *App) usageRecordDetail(w http.ResponseWriter, r *http.Request, recordID int, user *AuthUser) error {
	scope := accessScope(user, r.URL.Query().Get("scope"))
	record, err := a.getUsageRecord(r.Context(), recordID)
	if err != nil {
		return err
	}
	if !scope.IsAdmin && (record.UsageUsername == nil || *record.UsageUsername != scope.Username) {
		return notFoundError("usage 记录不存在")
	}
	if usageRecordIsExpired(record, time.Now().In(appTimeLocation)) {
		return appError("usage_record_expired", http.StatusGone, "使用明细已超过 7 天保留期")
	}
	users, err := a.userLookup(r.Context(), scope)
	if err != nil {
		return err
	}
	pricing, err := a.billingPriceIndex(r.Context())
	if err != nil {
		return err
	}
	cacheUsageRecordAuth(&record)
	redaction := usageRedactionOptions{MaskSource: !scope.IsAdmin, MaskAuthIndex: !scope.IsAdmin}
	item := listItemFromRecordVersioned(record, users, pricing, redaction)
	if scope.IsAdmin {
		sourceLabels, err := a.usageSourceLabels(r.Context(), pricing.MatchContext)
		if err != nil {
			return err
		}
		item["source_label"] = sourceLabels.labelFor(record)
	}
	item["raw_json"] = redactedRawJSON(record.RawJSON, usageRecordAuth(record), redaction)
	writeJSON(w, http.StatusOK, item)
	return nil
}

func (a *App) usageOptions(w http.ResponseWriter, r *http.Request, filters UsageFilters, user *AuthUser) error {
	response, err := a.usageOptionsResponse(r.Context(), user, usageOptionFilters(filters))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func usageOptionFilters(filters UsageFilters) UsageFilters {
	return normalizedUsageFilters(UsageFilters{
		Scope: filters.Scope,
		Start: filters.Start,
		End:   filters.End,
	})
}

func (a *App) usageOptionsResponse(ctx context.Context, user *AuthUser, filters UsageFilters) (map[string]any, error) {
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return nil, err
	}
	scope := accessScope(user, filters.Scope)
	scoped, err := a.scopedFilters(ctx, filters, scope)
	if err != nil {
		return nil, err
	}
	where, args, err := usageFactsWhere(scoped, "facts")
	if err != nil {
		return nil, err
	}
	users := []map[string]any{}
	distinctStrings := func(column string) ([]string, error) {
		rows, err := a.db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT facts.%s FROM usage_analytics_facts AS facts %s AND facts.%s IS NOT NULL`, column, where, column), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var values []string
		for rows.Next() {
			var value sql.NullString
			if err := rows.Scan(&value); err != nil {
				return nil, err
			}
			if value.Valid && strings.TrimSpace(value.String) != "" {
				values = append(values, value.String)
			}
		}
		sort.Strings(values)
		return values, rows.Err()
	}
	distinctSourceOptions := func() ([]map[string]string, error) {
		type sourceOption struct {
			Key   string
			Label string
			Match *usageSourceLabelMatch
		}
		// Load local names before opening rows: the SQLite pool has one connection.
		labels, err := a.usageSourceLabels(ctx, a.attachCachedBillingPriceSelectors(modelPriceBillingIndex{}).MatchContext)
		if err != nil {
			return nil, err
		}
		rows, err := a.db.QueryContext(ctx, `
			SELECT DISTINCT catalog.source_key, catalog.source, catalog.auth, catalog.auth_conflict,
				facts.provider, facts.model, facts.auth, facts.auth_index
			FROM usage_analytics_facts AS facts
			JOIN usage_source_catalog AS catalog ON catalog.source_key = facts.source_key
			`+where+` AND catalog.source IS NOT NULL AND trim(catalog.source) <> ''
		`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		byKey := map[string]sourceOption{}
		for rows.Next() {
			var key, source string
			var auth, provider, model, factAuth, authIndex sql.NullString
			var conflict bool
			if err := rows.Scan(&key, &source, &auth, &conflict, &provider, &model, &factAuth, &authIndex); err != nil {
				return nil, err
			}
			sourceValue := strings.TrimSpace(source)
			if sourceValue == "" || strings.TrimSpace(key) == "" {
				continue
			}
			authValue := nullableString(auth)
			if authValue != nil && strings.TrimSpace(*authValue) == "" {
				authValue = nil
			}
			displaySource := redactedUsageSource(&sourceValue, authValue, usageRedactionOptions{MaskSource: conflict || authValue == nil})
			if displaySource == nil {
				continue
			}
			var matched *usageSourceLabelMatch
			if !conflict && isAPIKeyAuth(authValue) && usageAuthTypeKey(authValue) == usageAuthTypeKey(nullableString(factAuth)) {
				matched = labels.resolve(usageSourceLabelEvidence{
					Provider: provider.String, Model: model.String, Source: &sourceValue, AuthIndex: nullableString(authIndex),
				})
			}
			option, exists := byKey[key]
			if !exists {
				option = sourceOption{Key: key, Label: *displaySource, Match: matched}
			} else if matched == nil || option.Match == nil || option.Match.Identity != matched.Identity {
				// All evidence must agree on identity, even when aliases are equal.
				// An unresolved or conflicting row cannot be healed by a later match.
				option.Match = nil
			}
			byKey[key] = option
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		options := make([]sourceOption, 0, len(byKey))
		for _, option := range byKey {
			if option.Match != nil && option.Match.Label != option.Label {
				option.Label = option.Match.Label + " · " + option.Label
			}
			options = append(options, option)
		}
		sort.Slice(options, func(i, j int) bool {
			if options[i].Label == options[j].Label {
				return options[i].Key < options[j].Key
			}
			return options[i].Label < options[j].Label
		})
		sources := make([]map[string]string, 0, len(options))
		for _, option := range options {
			sources = append(sources, map[string]string{
				"key":   option.Key,
				"label": option.Label,
			})
		}
		return sources, nil
	}
	if scope.IsAdmin {
		usernames, err := distinctStrings("usage_username")
		if err != nil {
			return nil, err
		}
		userLookup, err := a.userLookup(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, username := range usernames {
			if info, ok := userLookup[username]; ok {
				id := info.ID
				users = append(users, rankingItem(strconv.Itoa(id), info.Name, 0, 0, 0, 0, &id, nil))
			}
		}
	}
	providers, err := distinctStrings("provider")
	if err != nil {
		return nil, err
	}
	models, err := distinctStrings("model")
	if err != nil {
		return nil, err
	}
	sources := []map[string]string{}
	if scope.IsAdmin {
		sources, err = distinctSourceOptions()
		if err != nil {
			return nil, err
		}
	}
	endpoints, err := distinctStrings("endpoint")
	if err != nil {
		return nil, err
	}
	descriptions, err := distinctStrings("api_key_description")
	if err != nil {
		return nil, err
	}
	descriptionItems := make([]map[string]any, 0, len(descriptions))
	for _, description := range descriptions {
		value := description
		descriptionItems = append(descriptionItems, rankingItem(value, value, 0, 0, 0, 0, nil, &value))
	}
	return map[string]any{
		"users":                users,
		"api_key_descriptions": descriptionItems,
		"providers":            providers,
		"models":               models,
		"sources":              sources,
		"endpoints":            endpoints,
	}, nil
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func (a *App) filteredUsageRecords(ctx context.Context, filters UsageFilters, orderBy string) ([]UsageRecord, error) {
	where, args := usageWhere(filters)
	query := `SELECT id, CAST(timestamp AS TEXT), usage_username, api_key_description, provider, model, service_tier, reasoning_effort, endpoint, source,
		source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, response_model, request_alias FROM usage_records ` + where
	if strings.TrimSpace(orderBy) != "" {
		query += " ORDER BY " + orderBy
	} else {
		query += " ORDER BY timestamp"
	}
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records, err := scanUsageRecords(rows)
	if err != nil {
		return nil, err
	}
	return applyUsagePostFilters(records, filters), nil
}

func (a *App) filteredUsageAnalyticsRecords(ctx context.Context, filters UsageFilters, orderBy string) ([]UsageRecord, error) {
	records := []UsageRecord{}
	err := a.visitFilteredUsageAnalyticsRecords(ctx, filters, orderBy, func(record UsageRecord) {
		records = append(records, record)
	})
	return records, err
}

func (a *App) visitFilteredUsageAnalyticsRecords(ctx context.Context, filters UsageFilters, orderBy string, visit func(UsageRecord)) error {
	return a.visitFilteredUsageAnalyticsRecordsAfterID(ctx, filters, 0, orderBy, visit)
}

func (a *App) visitFilteredUsageAnalyticsRecordsAfterID(ctx context.Context, filters UsageFilters, minimumRecordID int64, orderBy string, visit func(UsageRecord)) error {
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return err
	}
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
	rows, err := a.db.QueryContext(ctx, query, args...)
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

func (a *App) countUsageRecords(ctx context.Context, filters UsageFilters) (int, error) {
	if hasUsagePostFilters(filters) {
		records, err := a.filteredUsageRecords(ctx, filters, "")
		if err != nil {
			return 0, err
		}
		return len(records), nil
	}
	where, args := usageWhere(filters)
	var total int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_records `+where, args...).Scan(&total)
	return total, err
}

func (a *App) pagedUsageRecords(ctx context.Context, filters UsageFilters, page, pageSize int) ([]UsageRecord, error) {
	if hasUsagePostFilters(filters) {
		records, err := a.filteredUsageRecords(ctx, filters, "timestamp DESC")
		if err != nil {
			return nil, err
		}
		start := (page - 1) * pageSize
		if start >= len(records) {
			return []UsageRecord{}, nil
		}
		end := start + pageSize
		if end > len(records) {
			end = len(records)
		}
		return records[start:end], nil
	}
	where, args := usageWhere(filters)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := a.db.QueryContext(ctx, `SELECT id, CAST(timestamp AS TEXT), usage_username, api_key_description, provider, model, service_tier, reasoning_effort, endpoint, source,
		source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, response_model, request_alias FROM usage_records `+where+` ORDER BY timestamp DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageRecords(rows)
}

func (a *App) filteredUsageRecordsPage(ctx context.Context, filters UsageFilters, page, pageSize int) (int, []UsageRecord, error) {
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return 0, nil, err
	}
	factFilters := filters
	factFilters.RequestID = nil
	where, args, err := usageFactsWhere(factFilters, "facts")
	if err != nil {
		return 0, nil, err
	}
	join := ""
	requestIDWhere := ""
	if filters.RequestID != nil {
		join = " JOIN usage_records AS records ON records.id = facts.usage_record_id"
		requestIDWhere = " AND records.request_id LIKE ?"
		args = append(args, "%"+*filters.RequestID+"%")
	}
	pageStart := (page - 1) * pageSize
	// Traverse the fact index once: retain only IDs for the requested page.
	rows, err := a.db.QueryContext(ctx, `
		SELECT facts.usage_record_id
		FROM usage_analytics_facts AS facts `+join+` `+where+requestIDWhere+`
		ORDER BY facts.timestamp DESC
	`, args...)
	if err != nil {
		return 0, nil, err
	}
	var total int
	ids := make([]int, 0, pageSize)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		if total >= pageStart && len(ids) < pageSize {
			ids = append(ids, id)
		}
		total++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, nil, err
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	records, err := a.usageRecordsByID(ctx, ids)
	if err != nil {
		return 0, nil, err
	}
	return total, records, nil
}

func (a *App) usageRecordsByID(ctx context.Context, ids []int) ([]UsageRecord, error) {
	if len(ids) == 0 {
		return []UsageRecord{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, CAST(timestamp AS TEXT), usage_username, api_key_description, provider, model, service_tier, reasoning_effort, endpoint, source,
		source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, response_model, request_alias FROM usage_records WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records, err := scanUsageRecords(rows)
	if err != nil {
		return nil, err
	}
	recordsByID := make(map[int]UsageRecord, len(records))
	for _, record := range records {
		recordsByID[record.ID] = record
	}
	ordered := make([]UsageRecord, 0, len(records))
	for _, id := range ids {
		if record, ok := recordsByID[id]; ok {
			ordered = append(ordered, record)
		}
	}
	return ordered, nil
}

func hasUsagePostFilters(filters UsageFilters) bool {
	return filters.SourceKey != nil
}

func applyUsagePostFilters(records []UsageRecord, filters UsageFilters) []UsageRecord {
	if !hasUsagePostFilters(filters) {
		return records
	}
	filtered := records[:0]
	for _, record := range records {
		if !usageRecordMatchesPostFilters(record, filters) {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

func usageRecordMatchesPostFilters(record UsageRecord, filters UsageFilters) bool {
	if filters.SourceKey != nil {
		key := usageSourceKey(record.Source)
		if key == nil || *key != *filters.SourceKey {
			return false
		}
	}
	return true
}

func (a *App) getUsageRecord(ctx context.Context, id int) (UsageRecord, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, CAST(timestamp AS TEXT), usage_username, api_key_description, provider, model, service_tier, reasoning_effort, endpoint, source,
		source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, response_model, request_alias FROM usage_records WHERE id = ?`, id)
	if err != nil {
		return UsageRecord{}, err
	}
	defer rows.Close()
	records, err := scanUsageRecords(rows)
	if err != nil {
		return UsageRecord{}, err
	}
	if len(records) == 0 {
		return UsageRecord{}, notFoundError("usage 记录不存在")
	}
	return records[0], nil
}

func usageWhere(filters UsageFilters) (string, []any) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if filters.Start != nil {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, dbTime(*filters.Start))
	}
	if filters.End != nil {
		clauses = append(clauses, "timestamp < ?")
		args = append(args, dbTime(*filters.End))
	}
	if filters.UsageUsername != nil {
		clauses = append(clauses, "usage_username = ?")
		args = append(args, *filters.UsageUsername)
	}
	if filters.APIKeyDescription != nil {
		clauses = append(clauses, "api_key_description = ?")
		args = append(args, *filters.APIKeyDescription)
	}
	if filters.Provider != nil {
		clauses = append(clauses, "provider = ?")
		args = append(args, *filters.Provider)
	}
	if filters.Model != nil {
		clauses = append(clauses, "model = ?")
		args = append(args, *filters.Model)
	}
	if filters.Endpoint != nil {
		clauses = append(clauses, "endpoint = ?")
		args = append(args, *filters.Endpoint)
	}
	if filters.Failed != nil {
		clauses = append(clauses, "failed = ?")
		args = append(args, *filters.Failed)
	}
	if filters.RequestID != nil {
		clauses = append(clauses, "request_id LIKE ?")
		args = append(args, "%"+*filters.RequestID+"%")
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func scanUsageRecords(rows *sql.Rows) ([]UsageRecord, error) {
	var records []UsageRecord
	for rows.Next() {
		record, err := scanUsageRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type usageRecordScanner interface {
	Scan(dest ...any) error
}

func scanUsageRecord(scanner usageRecordScanner) (UsageRecord, error) {
	var record UsageRecord
	var timestamp, usageUsername, description, provider, model, serviceTier, reasoningEffort, endpoint, source, sourceAccount, requestID, auth, authIndex sql.NullString
	var latencyFloat, ttftFloat sql.NullFloat64
	if err := scanner.Scan(&record.ID, &timestamp, &usageUsername, &description, &provider, &model, &serviceTier, &reasoningEffort, &endpoint, &source, &sourceAccount, &requestID, &auth, &authIndex, &latencyFloat, &ttftFloat, &record.Failed, &record.InputTokens, &record.OutputTokens, &record.CachedTokens, &record.CacheReadTokens, &record.CacheCreationTokens, &record.ReasoningTokens, &record.TotalTokens, &record.DedupeKey, &record.RawJSON, &record.ResponseModel, &record.RequestAlias); err != nil {
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
	record.ReasoningEffort = nullableString(reasoningEffort)
	record.Endpoint = nullableString(endpoint)
	record.Source = nullableString(source)
	record.SourceAccount = nullableString(sourceAccount)
	record.RequestID = nullableString(requestID)
	record.Auth = nullableString(auth)
	record.AuthIndex = nullableString(authIndex)
	record.LatencyMS = nullableFloat(latencyFloat)
	record.TTFTMS = nullableFloat(ttftFloat)
	return record, nil
}

func scanUsageAnalyticsRecord(scanner usageRecordScanner) (UsageRecord, error) {
	var record UsageRecord
	var timestamp, usageUsername, description, provider, model, serviceTier, endpoint, source, storedAuth, rawAuth, authIndex sql.NullString
	var ttftFloat sql.NullFloat64
	if err := scanner.Scan(&record.ID, &timestamp, &usageUsername, &description, &provider, &model, &serviceTier, &endpoint, &source, &storedAuth, &rawAuth, &authIndex, &ttftFloat, &record.Failed, &record.InputTokens, &record.OutputTokens, &record.CachedTokens, &record.CacheReadTokens, &record.CacheCreationTokens, &record.ReasoningTokens, &record.TotalTokens); err != nil {
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
	record.Source = nullableString(source)
	record.Auth = resolveProjectedUsageRecordAuth(nullableString(rawAuth), nullableString(storedAuth))
	record.AuthIndex = nullableString(authIndex)
	record.TTFTMS = nullableFloat(ttftFloat)
	record.resolvedAuth = record.Auth
	record.authResolved = true
	return record, nil
}

func resolveProjectedUsageRecordAuth(rawAuth, storedAuth *string) *string {
	if rawAuth == nil {
		return storedAuth
	}
	normalized := strings.TrimSpace(*rawAuth)
	if normalized == "" {
		return storedAuth
	}
	return &normalized
}

type userInfo struct {
	ID       int
	Username string
	Name     string
}

func (a *App) userLookup(ctx context.Context, scope usageAccessScope) (map[string]userInfo, error) {
	if !scope.IsAdmin {
		return map[string]userInfo{
			scope.Username: {ID: scope.UserID, Username: scope.Username, Name: scope.Username},
		}, nil
	}
	users, err := a.allUsers(ctx)
	if err != nil {
		return nil, err
	}
	lookup := map[string]userInfo{}
	for _, user := range users {
		name := displayUserName(user)
		if user.DisabledAt != nil {
			name += " (已禁用)"
		}
		lookup[user.Username] = userInfo{ID: user.ID, Username: user.Username, Name: name}
	}
	return lookup, nil
}

func listItemFromRecord(record UsageRecord, users map[string]userInfo, prices map[[2]string]ModelPrice, redaction usageRedactionOptions, matchContexts ...modelPriceMatchContext) map[string]any {
	costBreakdown := calculateRecordCostBreakdown(record, prices, matchContexts...)
	return listItemFromRecordWithBreakdown(record, users, redaction, costBreakdown)
}

func listItemFromRecordWithBreakdown(record UsageRecord, users map[string]userInfo, redaction usageRedactionOptions, costBreakdown usageCostBreakdown) map[string]any {
	cacheUsageRecordAuth(&record)
	userID := (*int)(nil)
	userLabel := "未绑定"
	if record.UsageUsername != nil {
		if info, ok := users[*record.UsageUsername]; ok {
			id := info.ID
			userID = &id
			userLabel = info.Name
		} else {
			userLabel = *record.UsageUsername
		}
	}
	authIndex := usageAnalyticsRecordAuthIndex(record)
	authIndex = redactedAuthIndex(authIndex, redaction)
	auth := usageRecordAuth(record)
	source := usageAnalyticsRecordSource(record)
	return map[string]any{
		"id":                    record.ID,
		"timestamp":             usageAPITime(record.Timestamp),
		"api_key_description":   record.APIKeyDescription,
		"user_id":               userID,
		"user_label":            userLabel,
		"provider":              record.Provider,
		"model":                 record.Model,
		"response_model":        record.ResponseModel,
		"request_alias":         record.RequestAlias,
		"service_tier":          record.ServiceTier,
		"reasoning_effort":      record.ReasoningEffort,
		"endpoint":              record.Endpoint,
		"source":                redactedUsageSource(source, auth, redaction),
		"source_label":          (*string)(nil),
		"request_id":            record.RequestID,
		"auth_index":            authIndex,
		"auth":                  auth,
		"latency_ms":            record.LatencyMS,
		"ttft_ms":               record.TTFTMS,
		"failed":                record.Failed,
		"input_tokens":          record.InputTokens,
		"output_tokens":         record.OutputTokens,
		"cached_tokens":         record.CachedTokens,
		"cache_read_tokens":     record.CacheReadTokens,
		"cache_creation_tokens": record.CacheCreationTokens,
		"reasoning_tokens":      record.ReasoningTokens,
		"total_tokens":          record.TotalTokens,
		"estimated_cost_usd":    costBreakdown.TotalUSD,
		"unpriced":              costBreakdown.Unpriced,
		"cost_breakdown":        costBreakdown,
	}
}

func listItemFromRecordVersioned(record UsageRecord, users map[string]userInfo, pricing modelPriceBillingIndex, redaction usageRedactionOptions) map[string]any {
	breakdown := versionedCostBreakdown(record, pricing, true)
	return listItemFromRecordWithBreakdown(record, users, redaction, breakdown)
}

func usageSummaryFromRecords(filters UsageFilters, records []UsageRecord, prices map[[2]string]ModelPrice, matchContexts ...modelPriceMatchContext) map[string]any {
	failed := 0
	input, output, cached, reasoning, total := 0, 0, 0, 0, 0
	normalInput, cacheRead, cacheCreation := 0, 0, 0
	estimated := 0.0
	unpriced := 0
	ttftTotal := 0.0
	ttftCount := 0
	for _, record := range records {
		matchedPrice, _ := findMatchingChannelPrice(prices, record, matchContexts...)
		matchedBrand := matchedModelPriceChannelBrand(matchedPrice, record, matchContexts...)
		if record.Failed {
			failed++
		}
		input += usageAggregateInputTokens(record, matchedBrand)
		output += record.OutputTokens
		cached += record.CachedTokens
		reasoning += record.ReasoningTokens
		total += usageAggregateTotalTokens(record, matchedBrand)
		tokens := normalizedUsageTokenBreakdown(record, matchedBrand)
		normalInput += tokens.NormalInputTokens
		cacheRead += tokens.CacheReadTokens
		cacheCreation += tokens.CacheCreationTokens
		amount, isUnpriced := recordCost(record, prices, matchContexts...)
		estimated = mathRound(estimated+amount, 8)
		if isUnpriced {
			unpriced++
		}
		if record.TTFTMS != nil && *record.TTFTMS > 0 {
			ttftTotal += *record.TTFTMS
			ttftCount++
		}
	}
	if filters.Start == nil || filters.End == nil {
		start, end := defaultTodayRange()
		filters.Start = &start
		filters.End = &end
	}
	return map[string]any{
		"start":                 usageAPITimePtr(filters.Start),
		"end":                   usageAPITimePtr(filters.End),
		"total_records":         len(records),
		"failed_records":        failed,
		"success_records":       len(records) - failed,
		"input_tokens":          input,
		"output_tokens":         output,
		"cached_tokens":         cached,
		"normal_input_tokens":   normalInput,
		"cache_read_tokens":     cacheRead,
		"cache_creation_tokens": cacheCreation,
		"reasoning_tokens":      reasoning,
		"total_tokens":          total,
		"estimated_cost_usd":    estimated,
		"unpriced_records":      unpriced,
		"average_ttft_ms":       averageTTFTMS(ttftTotal, ttftCount),
	}
}

func averageTTFTMS(total float64, count int) *float64 {
	if count == 0 {
		return nil
	}
	average := mathRound(total/float64(count), 2)
	return &average
}

func trendPointsFromRecords(filters UsageFilters, records []UsageRecord, prices map[[2]string]ModelPrice, matchContexts ...modelPriceMatchContext) []map[string]any {
	buckets := map[string][]UsageRecord{}
	duration := 24 * time.Hour
	if filters.Start != nil && filters.End != nil {
		duration = filters.End.Sub(*filters.Start)
	}
	for _, record := range records {
		timestamp := record.Timestamp.In(appTimeLocation)
		bucket := timestamp.Format("2006-01-02")
		if duration <= 48*time.Hour {
			bucket = timestamp.Format("2006-01-02 15:00")
		}
		buckets[bucket] = append(buckets[bucket], record)
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	points := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		group := buckets[key]
		failed, tokens, cost := 0, 0, 0.0
		for _, record := range group {
			matchedPrice, _ := findMatchingChannelPrice(prices, record, matchContexts...)
			matchedBrand := matchedModelPriceChannelBrand(matchedPrice, record, matchContexts...)
			if record.Failed {
				failed++
			}
			tokens += usageAggregateTotalTokens(record, matchedBrand)
			amount, _ := recordCost(record, prices, matchContexts...)
			cost = mathRound(cost+amount, 8)
		}
		points = append(points, map[string]any{
			"bucket":             key,
			"records":            len(group),
			"failed_records":     failed,
			"total_tokens":       tokens,
			"estimated_cost_usd": cost,
		})
	}
	return points
}

func rankingFromRecords(records []UsageRecord, prices map[[2]string]ModelPrice, groupBy string, users map[string]userInfo, matchContexts ...modelPriceMatchContext) map[string]any {
	return rankingFromRecordsBySort(records, prices, groupBy, users, usageRankingSortTokens, matchContexts...)
}

func rankingFromRecordsBySort(records []UsageRecord, prices map[[2]string]ModelPrice, groupBy string, users map[string]userInfo, sortBy string, matchContexts ...modelPriceMatchContext) map[string]any {
	grouped := map[string][]UsageRecord{}
	labels := map[string]string{}
	userIDs := map[string]*int{}
	descriptions := map[string]*string{}
	for _, record := range records {
		key := ""
		label := ""
		switch groupBy {
		case "model":
			provider := valueOr(record.Provider, "unknown")
			model := valueOr(record.Model, "unknown")
			key = provider + "::" + model
			label = provider + " / " + model
		case "user":
			if record.UsageUsername == nil {
				continue
			}
			info, ok := users[*record.UsageUsername]
			if !ok {
				key = *record.UsageUsername
				label = *record.UsageUsername
			} else {
				key = strconv.Itoa(info.ID)
				label = info.Name
				id := info.ID
				userIDs[key] = &id
			}
		default:
			description := ""
			if record.APIKeyDescription != nil {
				description = strings.TrimSpace(*record.APIKeyDescription)
			}
			key = description
			if key == "" {
				key = "unlabeled"
				label = "未设置 KEY 描述"
			} else {
				label = description
				value := description
				descriptions[key] = &value
			}
		}
		grouped[key] = append(grouped[key], record)
		labels[key] = label
	}
	items := make([]map[string]any, 0, len(grouped))
	for key, group := range grouped {
		failed, tokens, cost := 0, 0, 0.0
		for _, record := range group {
			matchedPrice, _ := findMatchingChannelPrice(prices, record, matchContexts...)
			matchedBrand := matchedModelPriceChannelBrand(matchedPrice, record, matchContexts...)
			if record.Failed {
				failed++
			}
			tokens += usageAggregateTotalTokens(record, matchedBrand)
			amount, _ := recordCost(record, prices, matchContexts...)
			cost = mathRound(cost+amount, 8)
		}
		items = append(items, rankingItem(key, labels[key], len(group), failed, tokens, cost, userIDs[key], descriptions[key]))
	}
	sort.Slice(items, func(i, j int) bool {
		leftMetric := rankingItemSortMetric(items[i], sortBy)
		rightMetric := rankingItemSortMetric(items[j], sortBy)
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
	return map[string]any{"group_by": groupBy, "items": items}
}

func usageRankingSort(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return usageRankingSortTokens, true
	}
	switch normalized {
	case usageRankingSortTokens, usageRankingSortCost, usageRankingSortRecords:
		return normalized, true
	default:
		return "", false
	}
}

func rankingItemSortMetric(item map[string]any, sortBy string) float64 {
	switch sortBy {
	case usageRankingSortCost:
		return item["estimated_cost_usd"].(float64)
	case usageRankingSortRecords:
		return float64(item["records"].(int))
	default:
		return float64(item["total_tokens"].(int))
	}
}

func rankingItem(key, label string, records, failed, tokens int, cost float64, userID *int, description *string) map[string]any {
	return map[string]any{
		"key":                 key,
		"label":               label,
		"records":             records,
		"failed_records":      failed,
		"total_tokens":        tokens,
		"estimated_cost_usd":  cost,
		"user_id":             userID,
		"api_key_description": description,
	}
}

func distributionsFromRecords(records []UsageRecord, prices map[[2]string]ModelPrice, matchContexts ...modelPriceMatchContext) map[string]any {
	return map[string]any{
		"providers":     distributionItems(records, prices, func(record UsageRecord) string { return valueOr(record.Provider, "unknown") }, matchContexts...),
		"models":        distributionItems(records, prices, func(record UsageRecord) string { return valueOr(record.Model, "unknown") }, matchContexts...),
		"endpoints":     distributionItems(records, prices, func(record UsageRecord) string { return valueOr(record.Endpoint, "unknown") }, matchContexts...),
		"channel_costs": channelCostItems(records, prices, matchContexts...),
	}
}

func channelCostItems(records []UsageRecord, prices map[[2]string]ModelPrice, matchContexts ...modelPriceMatchContext) []usageChannelCostItem {
	// Display helpers need a concrete context; cost helpers must receive the
	// variadic as-is, because an explicit empty context is treated as
	// "selectors required but unavailable" and yields unpriced.
	displayContext := modelPriceMatchContext{}
	if len(matchContexts) > 0 {
		displayContext = matchContexts[0]
	}
	itemsByIdentity := map[modelPriceChannelGroupIdentity]usageChannelCostItem{}
	for _, record := range records {
		matchedPrice, status := findMatchingChannelPrice(prices, record, matchContexts...)
		if status != priceMatchStatusMatched || matchedPrice == nil {
			continue
		}
		amount, unpriced := recordCost(record, prices, matchContexts...)
		if unpriced || amount <= 0 {
			continue
		}
		identity, ok := modelPriceChannelGroupIdentityForPrice(*matchedPrice)
		if !ok {
			continue
		}
		display := modelPriceChannelDisplayForPrice(*matchedPrice, displayContext)
		item := itemsByIdentity[identity]
		if item.Key == "" {
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
		item.EstimatedCostUSD = mathRound(item.EstimatedCostUSD+amount, 8)
		itemsByIdentity[identity] = item
	}
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

func distributionItems(records []UsageRecord, prices map[[2]string]ModelPrice, keyFn func(UsageRecord) string, matchContexts ...modelPriceMatchContext) []map[string]any {
	grouped := map[string][]UsageRecord{}
	for _, record := range records {
		key := keyFn(record)
		grouped[key] = append(grouped[key], record)
	}
	items := make([]map[string]any, 0, len(grouped))
	for key, group := range grouped {
		tokens, cost := 0, 0.0
		for _, record := range group {
			matchedPrice, _ := findMatchingChannelPrice(prices, record, matchContexts...)
			matchedBrand := matchedModelPriceChannelBrand(matchedPrice, record, matchContexts...)
			tokens += usageAggregateTotalTokens(record, matchedBrand)
			amount, _ := recordCost(record, prices, matchContexts...)
			cost = mathRound(cost+amount, 8)
		}
		items = append(items, map[string]any{
			"key":                key,
			"label":              key,
			"records":            len(group),
			"total_tokens":       tokens,
			"estimated_cost_usd": cost,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["records"].(int) > items[j]["records"].(int) })
	if len(items) > 20 {
		items = items[:20]
	}
	return items
}

func valueOr(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

func rawJSONStringField(rawJSON, fieldName string) *string {
	var payload map[string]any
	if json.Unmarshal([]byte(rawJSON), &payload) != nil {
		return nil
	}
	value, ok := payload[fieldName]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		normalized := strings.TrimSpace(typed)
		if normalized == "" {
			return nil
		}
		return &normalized
	case float64:
		text := strconv.FormatFloat(typed, 'f', -1, 64)
		return &text
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		text := strconv.FormatFloat(parsed, 'f', -1, 64)
		return &text
	case bool:
		text := strconv.FormatBool(typed)
		return &text
	default:
		return nil
	}
}

var usageSourcePayloadFields = []string{"source", "origin"}

// usagePayloadStringFromRawJSON intentionally uses the same aliases and
// recursive lookup as normalizeUsage. Direct imports can bypass
// normalizeUsage, but their retained raw payload must still produce the same
// scalar and compact metadata projections.
func usagePayloadStringFromRawJSON(rawJSON string, fields ...string) *string {
	if strings.TrimSpace(rawJSON) == "" {
		return nil
	}
	var payload any
	if json.Unmarshal([]byte(rawJSON), &payload) != nil {
		return nil
	}
	return toString(findFirst(payload, fields...))
}

func sourceFromUsageRawJSON(rawJSON string) *string {
	return usagePayloadStringFromRawJSON(rawJSON, usageSourcePayloadFields...)
}

func sourceAccountFromUsageSource(source *string) *string {
	if source == nil {
		return nil
	}
	if match := usageEmailPattern.FindString(*source); match != "" {
		return normalizeUsageSourceAccount(match)
	}
	return nil
}

var usageSourceAccountPayloadFields = []string{
	"email",
	"account_email",
	"accountEmail",
	"user_email",
	"userEmail",
}

func sourceAccountFromUsagePayload(value any, source *string) *string {
	if account := sourceAccountFromUsagePayloadValue(value); account != nil {
		return account
	}
	return sourceAccountFromUsageSource(source)
}

func sourceAccountFromUsageRawJSON(rawJSON string) *string {
	if strings.TrimSpace(rawJSON) == "" {
		return nil
	}
	var payload any
	if json.Unmarshal([]byte(rawJSON), &payload) != nil {
		return nil
	}
	return sourceAccountFromUsagePayloadValue(payload)
}

func sourceAccountFromUsagePayloadValue(value any) *string {
	for _, field := range usageSourceAccountPayloadFields {
		if account := toString(findFirst(value, field)); account != nil {
			return normalizeUsageSourceAccount(*account)
		}
	}
	return nil
}

func normalizeUsageSourceAccount(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	if match := usageEmailPattern.FindString(normalized); match != "" {
		normalized = strings.ToLower(strings.TrimSpace(match))
	}
	return &normalized
}

func authIndexFromUsagePayload(parsed any) *string {
	return toString(findFirst(parsed, "auth_index", "authIndex", "index", "auth_name", "authName", "account_id", "accountId"))
}

func authFromUsageRawJSON(rawJSON string) *string {
	if authType := usagePayloadStringFromRawJSON(rawJSON, "auth_type"); authType != nil {
		return authType
	}
	return usagePayloadStringFromRawJSON(rawJSON, "auth", "authentication")
}

func usageRecordAuth(record UsageRecord) *string {
	if record.authResolved {
		return record.resolvedAuth
	}
	return resolveUsageRecordAuth(record.RawJSON, record.Auth)
}

func cacheUsageRecordAuth(record *UsageRecord) {
	if record == nil || record.authResolved {
		return
	}
	record.resolvedAuth = resolveUsageRecordAuth(record.RawJSON, record.Auth)
	record.authResolved = true
}

func resolveUsageRecordAuth(rawJSON string, storedAuth *string) *string {
	if strings.TrimSpace(rawJSON) == "" {
		return storedAuth
	}
	auth := authFromUsageRawJSON(rawJSON)
	if auth == nil {
		auth = storedAuth
	}
	return auth
}

func redactedUsageSource(source *string, authType *string, redaction usageRedactionOptions) *string {
	if source == nil {
		return nil
	}
	if !redaction.MaskSource && !isAPIKeyAuth(authType) {
		return source
	}
	masked := maskSecret(source)
	return &masked
}

func usageSourceKey(source *string) *string {
	if source == nil {
		return nil
	}
	normalized := strings.TrimSpace(*source)
	if normalized == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(normalized))
	key := hex.EncodeToString(sum[:])
	return &key
}

func redactedAuthIndex(authIndex *string, redaction usageRedactionOptions) *string {
	if authIndex == nil || !redaction.MaskAuthIndex {
		return authIndex
	}
	masked := maskSecret(authIndex)
	return &masked
}

func isAPIKeyAuth(authType *string) bool {
	return usageAuthTypeKey(authType) == "apikey"
}

func isOAuthAuth(authType *string) bool {
	return usageAuthTypeKey(authType) == "oauth"
}

func usageAuthTypeKey(authType *string) string {
	if authType == nil {
		return ""
	}
	normalized := strings.ToLower(strings.TrimSpace(*authType))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	return normalized
}

func redactedRawJSON(rawJSON string, authType *string, redaction usageRedactionOptions) any {
	var payload any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		value := rawJSON
		return maskSecret(&value)
	}
	return redactJSON(payload, authType, redaction)
}

func redactJSON(value any, inheritedAuthType *string, redaction usageRedactionOptions) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		authType := jsonStringField(typed, "auth_type")
		if authType == nil {
			authType = jsonStringField(typed, "authType")
		}
		if authType == nil {
			authType = jsonStringField(typed, "auth")
		}
		if authType == nil {
			authType = inheritedAuthType
		}
		for key, child := range typed {
			if shouldRedactJSONField(key, authType, redaction) {
				if child == nil {
					result[key] = nil
				} else {
					text := fmt.Sprint(child)
					result[key] = maskSecret(&text)
				}
			} else {
				result[key] = redactJSON(child, authType, redaction)
			}
		}
		return result
	case []any:
		result := make([]any, 0, len(typed))
		for _, child := range typed {
			result = append(result, redactJSON(child, inheritedAuthType, redaction))
		}
		return result
	default:
		return value
	}
}

func jsonStringField(payload map[string]any, fieldName string) *string {
	value, ok := payload[fieldName]
	if !ok || value == nil {
		return nil
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return nil
	}
	return &text
}

func shouldRedactJSONField(key string, authType *string, redaction usageRedactionOptions) bool {
	lower := strings.ToLower(key)
	if (redaction.MaskSource || isAPIKeyAuth(authType)) && (lower == "source" || lower == "origin") {
		return true
	}
	if redaction.MaskAuthIndex && (lower == "auth_index" || lower == "authindex") {
		return true
	}
	return strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "bearer") ||
		strings.Contains(lower, "cookie") ||
		strings.Contains(lower, "key") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token")
}

func (a *App) saveUsageMessage(ctx context.Context, raw []byte, pricing modelPriceBillingIndex, collectorOrigin ...string) (UsageRecord, bool, error) {
	normalized, err := normalizeUsage(raw)
	if err != nil {
		return UsageRecord{}, false, err
	}
	if err := normalized.applyBreakdownInputSemantics(pricing); err != nil {
		return UsageRecord{}, false, err
	}
	usageUsername, description, err := a.usageOwnerSnapshot(ctx, normalized.APIKeyHash)
	if err != nil {
		return UsageRecord{}, false, err
	}
	now := dbTime(time.Now())
	responseModel, requestAlias := usageResponseModelFields(normalized.RawJSON)
	var origin *string
	if len(collectorOrigin) > 0 && collectorOrigin[0] != "" {
		origin = &collectorOrigin[0]
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return UsageRecord{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, api_key_description, provider, model, service_tier, endpoint,
			reasoning_effort, source, source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json,
			response_model, request_alias, collector_origin
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, now, dbTime(normalized.Timestamp), usageUsername, description, normalized.Provider, normalized.Model, normalized.ServiceTier, normalized.Endpoint, normalized.ReasoningEffort, normalized.Source, normalized.SourceAccount, normalized.RequestID, normalized.Auth, normalized.AuthIndex, normalized.LatencyMS, normalized.TTFTMS, normalized.Failed, normalized.InputTokens, normalized.OutputTokens, normalized.CachedTokens, normalized.CacheReadTokens, normalized.CacheCreationTokens, normalized.ReasoningTokens, normalized.TotalTokens, normalized.DedupeKey, normalized.RawJSON, responseModel, requestAlias, origin)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			_ = tx.Rollback()
			record, getErr := a.usageRecordByDedupe(ctx, normalized.DedupeKey)
			return record, false, getErr
		}
		return UsageRecord{}, false, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return UsageRecord{}, false, err
	}
	record := UsageRecord{
		ID:                  int(id),
		Timestamp:           normalized.Timestamp,
		UsageUsername:       usageUsername,
		APIKeyDescription:   description,
		Provider:            normalized.Provider,
		Model:               normalized.Model,
		ResponseModel:       responseModel,
		RequestAlias:        requestAlias,
		ServiceTier:         normalized.ServiceTier,
		ReasoningEffort:     normalized.ReasoningEffort,
		Endpoint:            normalized.Endpoint,
		Source:              normalized.Source,
		SourceAccount:       normalized.SourceAccount,
		RequestID:           normalized.RequestID,
		Auth:                normalized.Auth,
		AuthIndex:           normalized.AuthIndex,
		LatencyMS:           normalized.LatencyMS,
		TTFTMS:              normalized.TTFTMS,
		Failed:              normalized.Failed,
		InputTokens:         normalized.InputTokens,
		OutputTokens:        normalized.OutputTokens,
		CachedTokens:        normalized.CachedTokens,
		CacheReadTokens:     normalized.CacheReadTokens,
		CacheCreationTokens: normalized.CacheCreationTokens,
		ReasoningTokens:     normalized.ReasoningTokens,
		TotalTokens:         normalized.TotalTokens,
		DedupeKey:           normalized.DedupeKey,
		RawJSON:             normalized.RawJSON,
	}
	if err := a.upsertUsageAnalyticsFact(ctx, tx, record); err != nil {
		return UsageRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return UsageRecord{}, false, err
	}
	if err := a.applyQuotaCharge(ctx, record, pricing); err != nil {
		return UsageRecord{}, false, err
	}
	return record, true, nil
}

func (a *App) usageRecordByDedupe(ctx context.Context, dedupeKey string) (UsageRecord, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, CAST(timestamp AS TEXT), usage_username, api_key_description, provider, model, service_tier, reasoning_effort, endpoint, source,
		source_account, request_id, auth, auth_index, latency_ms, ttft_ms, failed, input_tokens, output_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, response_model, request_alias FROM usage_records WHERE dedupe_key = ?`, dedupeKey)
	if err != nil {
		return UsageRecord{}, err
	}
	defer rows.Close()
	records, err := scanUsageRecords(rows)
	if err != nil {
		return UsageRecord{}, err
	}
	if len(records) == 0 {
		return UsageRecord{}, notFoundError("usage 记录不存在")
	}
	return records[0], nil
}

type normalizedUsage struct {
	Timestamp             time.Time
	APIKeyHash            string
	Provider              *string
	Model                 *string
	ServiceTier           *string
	Endpoint              *string
	Source                *string
	SourceAccount         *string
	RequestID             *string
	Auth                  *string
	AuthIndex             *string
	LatencyMS             *float64
	ReasoningEffort       *string
	TTFTMS                *float64
	Failed                bool
	InputTokens           int
	OutputTokens          int
	CachedTokens          int
	CacheReadTokens       int
	CacheCreationTokens   int
	ReasoningTokens       int
	TotalTokens           int
	DedupeKey             string
	RawJSON               string
	inputFromBreakdown    bool
	breakdownUncached     int
	breakdownUncachedOK   bool
	breakdownCacheRead    int
	breakdownCacheReadOK  bool
	breakdownCacheWrite   int
	breakdownCacheWriteOK bool
}

func normalizeUsage(raw []byte) (normalizedUsage, error) {
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		parsed = map[string]any{"message": string(raw)}
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return normalizedUsage{}, err
	}
	apiKey := toString(findFirst(parsed, "api_key", "apiKey", "apikey", "key"))
	if apiKey == nil {
		unknown := "unknown"
		apiKey = &unknown
	}
	input, inputFromBreakdown := usageInputTokenValue(parsed)
	breakdownUncached, breakdownUncachedOK := usageTokenAtPath(parsed, "token_breakdown", "input", "uncached_tokens")
	breakdownCacheRead, breakdownCacheReadOK := usageTokenAtPath(parsed, "token_breakdown", "input", "cache_read_tokens")
	breakdownCacheWrite, breakdownCacheWriteOK := usageTokenAtPath(parsed, "token_breakdown", "input", "cache_write_tokens")
	output, _ := usageTokenValue(parsed,
		[]string{"tokens", "output_tokens"},
		[]string{"token_breakdown", "output", "total_tokens"},
		"output_tokens", "completion_tokens", "completionTokens", "output",
	)
	cached, _ := usageTokenValue(parsed,
		[]string{"tokens", "cached_tokens"},
		[]string{"token_breakdown", "input", "cache_read_tokens"},
		"cached_tokens", "cached_input_tokens", "cached",
	)
	cacheRead, _ := usageTokenValue(parsed,
		[]string{"tokens", "cache_read_tokens"},
		[]string{"token_breakdown", "input", "cache_read_tokens"},
		"cache_read_tokens", "cache_read_input_tokens",
	)
	cacheCreation, _ := usageTokenValue(parsed,
		[]string{"tokens", "cache_creation_tokens"},
		[]string{"token_breakdown", "input", "cache_write_tokens"},
		"cache_creation_tokens", "cache_creation_input_tokens",
	)
	reasoning, _ := usageTokenValue(parsed,
		[]string{"tokens", "reasoning_tokens"},
		[]string{"token_breakdown", "output", "reasoning_tokens"},
		"reasoning_tokens", "reasoning",
	)
	total, totalFound := usageTotalTokenValue(parsed)
	if !totalFound {
		total = input + output
		if total == 0 {
			total = cached + reasoning
		}
	}
	source := toString(findFirst(parsed, usageSourcePayloadFields...))
	sum := sha256.Sum256(canonical)
	return normalizedUsage{
		Timestamp:             parseUsageTimestamp(findFirst(parsed, "timestamp", "time", "created_at", "createdAt", "request_time")),
		APIKeyHash:            hashAPIKey(*apiKey),
		Provider:              toString(findFirst(parsed, "provider", "provider_name")),
		Model:                 toString(findFirst(parsed, "model", "model_name")),
		ServiceTier:           toString(findFirst(parsed, "service_tier", "serviceTier")),
		Endpoint:              toString(findFirst(parsed, "endpoint", "path", "route")),
		Source:                source,
		SourceAccount:         sourceAccountFromUsagePayload(parsed, source),
		RequestID:             toString(findFirst(parsed, "request_id", "requestId", "id")),
		Auth:                  authLabel(parsed),
		AuthIndex:             authIndexFromUsagePayload(parsed),
		LatencyMS:             toFloat(findFirst(parsed, "latency_ms", "latency", "duration_ms", "duration")),
		ReasoningEffort:       toString(findFirst(parsed, "reasoning_effort", "reasoningEffort")),
		TTFTMS:                toPositiveFloat(findFirst(parsed, "ttft_ms", "ttftMs")),
		Failed:                isUsageFailed(parsed),
		InputTokens:           input,
		OutputTokens:          output,
		CachedTokens:          cached,
		CacheReadTokens:       cacheRead,
		CacheCreationTokens:   cacheCreation,
		ReasoningTokens:       reasoning,
		TotalTokens:           total,
		DedupeKey:             "raw:" + hex.EncodeToString(sum[:]),
		RawJSON:               string(canonical),
		inputFromBreakdown:    inputFromBreakdown,
		breakdownUncached:     breakdownUncached,
		breakdownUncachedOK:   breakdownUncachedOK,
		breakdownCacheRead:    breakdownCacheRead,
		breakdownCacheReadOK:  breakdownCacheReadOK,
		breakdownCacheWrite:   breakdownCacheWrite,
		breakdownCacheWriteOK: breakdownCacheWriteOK,
	}, nil
}

func usageInputTokenValue(value any) (int, bool) {
	if token, ok := usageTokenAtPath(value, "tokens", "input_tokens"); ok {
		return token, false
	}
	if token, ok := usageTokenAtPath(value, "token_breakdown", "input", "total_tokens"); ok {
		return token, true
	}
	if token, ok := findFirstUsageToken(value, "input_tokens", "prompt_tokens", "promptTokens", "input"); ok {
		return token, false
	}
	return 0, false
}

func (usage *normalizedUsage) applyBreakdownInputSemantics(pricing modelPriceBillingIndex) error {
	if !usage.inputFromBreakdown {
		return nil
	}
	record := UsageRecord{
		Provider:  usage.Provider,
		Model:     usage.Model,
		Auth:      usage.Auth,
		AuthIndex: usage.AuthIndex,
	}
	price, _ := findMatchingChannelPrice(pricing.Prices, record, pricing.MatchContext)
	brand := matchedModelPriceChannelBrand(price, record, pricing.MatchContext)
	if !usageUsesClaudeTokenSemantics(record, brand) {
		return nil
	}
	if usage.breakdownUncachedOK {
		usage.InputTokens = usage.breakdownUncached
		return nil
	}
	if !usage.breakdownCacheReadOK || !usage.breakdownCacheWriteOK ||
		usage.CacheReadTokens != usage.breakdownCacheRead ||
		usage.CacheCreationTokens != usage.breakdownCacheWrite ||
		usage.breakdownCacheRead > usage.InputTokens ||
		usage.breakdownCacheWrite > usage.InputTokens-usage.breakdownCacheRead {
		return fmt.Errorf("native Claude token breakdown is missing a reliable uncached token value")
	}
	usage.InputTokens -= usage.breakdownCacheRead + usage.breakdownCacheWrite
	return nil
}

func usageTotalTokenValue(value any) (int, bool) {
	if token, ok := usageTokenAtPath(value, "tokens", "total_tokens"); ok {
		return token, true
	}
	if token, ok := usageTokenAtPath(value, "token_breakdown", "total_tokens"); ok {
		return token, true
	}
	for _, key := range []string{"total_tokens", "totalTokens", "total"} {
		if token, ok := findLegacyTotalTokenByKey(value, key); ok {
			return token, true
		}
	}
	return 0, false
}

func findLegacyTotalTokenByKey(value any, key string) (int, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if candidate, ok := typed[key]; ok {
			if token, ok := usageTokenInt(candidate); ok {
				return token, true
			}
		}
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if name == key || !strings.EqualFold(name, key) {
				continue
			}
			if token, ok := usageTokenInt(typed[name]); ok {
				return token, true
			}
		}
		for _, name := range names {
			if strings.EqualFold(name, "tokens") || strings.EqualFold(name, "token_breakdown") {
				continue
			}
			if token, ok := findLegacyTotalTokenByKey(typed[name], key); ok {
				return token, true
			}
		}
	case []any:
		for _, child := range typed {
			if token, ok := findLegacyTotalTokenByKey(child, key); ok {
				return token, true
			}
		}
	}
	return 0, false
}

func usageTokenValue(value any, primaryPath, breakdownPath []string, legacyKeys ...string) (int, bool) {
	if token, ok := usageTokenAtPath(value, primaryPath...); ok {
		return token, true
	}
	if token, ok := usageTokenAtPath(value, breakdownPath...); ok {
		return token, true
	}
	if token, ok := findFirstUsageToken(value, legacyKeys...); ok {
		return token, true
	}
	return 0, false
}

func usageTokenAtPath(value any, path ...string) (int, bool) {
	current := value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = object[key]
		if !ok {
			return 0, false
		}
	}
	return usageTokenInt(current)
}

func findFirstUsageToken(value any, keys ...string) (int, bool) {
	for _, key := range keys {
		if token, ok := findUsageTokenByKey(value, key); ok {
			return token, true
		}
	}
	return 0, false
}

func findUsageTokenByKey(value any, key string) (int, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if candidate, ok := typed[key]; ok {
			if token, ok := usageTokenInt(candidate); ok {
				return token, true
			}
		}
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if name == key || !strings.EqualFold(name, key) {
				continue
			}
			if token, ok := usageTokenInt(typed[name]); ok {
				return token, true
			}
		}
		for _, name := range names {
			if token, ok := findUsageTokenByKey(typed[name], key); ok {
				return token, true
			}
		}
	case []any:
		for _, child := range typed {
			if token, ok := findUsageTokenByKey(child, key); ok {
				return token, true
			}
		}
	}
	return 0, false
}

func usageTokenInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		if typed < 0 {
			return 0, true
		}
		if math.Trunc(typed) != typed || typed >= math.Ldexp(1, strconv.IntSize-1) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		return usageTokenTextInt(typed.String())
	case string:
		return usageTokenTextInt(typed)
	default:
		return 0, false
	}
}

func usageTokenTextInt(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return 0, false
	}
	parsed, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return 0, false
	}
	if parsed.Sign() < 0 {
		return 0, true
	}
	if !parsed.IsInt() {
		return 0, false
	}
	maxInt := new(big.Int).SetUint64(uint64(^uint(0) >> 1))
	if parsed.Num().Cmp(maxInt) > 0 {
		return 0, false
	}
	return int(parsed.Num().Int64()), true
}

func (a *App) usageOwnerSnapshot(ctx context.Context, apiKeyHash string) (*string, *string, error) {
	var userID int
	var description sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT user_id, description FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash).Scan(&userID, &description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var username string
	err = a.db.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, userID).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nullableString(description), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &username, nullableString(description), nil
}

func findFirst(value any, keys ...string) any {
	keySet := map[string]bool{}
	for _, key := range keys {
		keySet[strings.ToLower(key)] = true
	}
	var walk func(any) any
	walk = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if value, ok := typed[key]; ok {
					return value
				}
			}
			for key, child := range typed {
				if keySet[strings.ToLower(key)] {
					return child
				}
				if found := walk(child); found != nil {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := walk(child); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(value)
}

func toString(value any) *string {
	switch typed := value.(type) {
	case string:
		normalized := strings.TrimSpace(typed)
		if normalized == "" {
			return nil
		}
		return &normalized
	case float64:
		text := strconv.FormatFloat(typed, 'f', -1, 64)
		return &text
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		text := strconv.FormatFloat(parsed, 'f', -1, 64)
		return &text
	case bool:
		text := strconv.FormatBool(typed)
		return &text
	default:
		return nil
	}
}

func toFloat(value any) *float64 {
	switch typed := value.(type) {
	case float64:
		return &typed
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return &parsed
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func toPositiveFloat(value any) *float64 {
	parsed := toFloat(value)
	if parsed == nil || *parsed <= 0 {
		return nil
	}
	return parsed
}

func toInt(value any) int {
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			return 0
		}
		return int(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil && parsed >= 0 {
			return int(parsed)
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil && parsed > 0 {
			return int(parsed)
		}
	case bool:
		if typed {
			return 1
		}
	}
	return 0
}

func parseUsageTimestamp(value any) time.Time {
	switch typed := value.(type) {
	case float64:
		seconds := typed
		if typed > 10_000_000_000 {
			seconds = typed / 1000
		}
		return time.Unix(int64(seconds), 0).In(appTimeLocation)
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return parseUsageTimestamp(parsed)
		}
	case string:
		if parsed, ok := parseInputTime(typed); ok {
			return parsed
		}
	}
	return time.Now().In(appTimeLocation)
}

func authLabel(value any) *string {
	if authType := toString(findFirst(value, "auth_type")); authType != nil {
		return authType
	}
	return toString(findFirst(value, "auth", "authentication"))
}

func isUsageFailed(value any) bool {
	failed := findFirst(value, "failed", "is_failed", "error")
	switch typed := failed.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "failed" || normalized == "error"
	default:
		if failed != nil {
			return true
		}
	}
	success := findFirst(value, "success", "ok")
	if typed, ok := success.(bool); ok {
		return !typed
	}
	status := toInt(findFirst(value, "status", "status_code", "statusCode"))
	return status >= 400
}
