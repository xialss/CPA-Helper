package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-helper/backend/internal/pricingdefaults"
)

const defaultLiteLLMPricingURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
const modelBillingUnitToken = "token"
const modelBillingUnitRequest = "request"
const usageCostKindInput = "input"
const usageCostKindCacheRead = "cache_read"
const usageCostKindCacheCreation = "cache_creation"
const usageCostKindOutput = "output"
const usageCostKindRequest = "request"
const serviceTierPriority = "priority"
const modelPriceScopeLibrary = "library"
const modelPriceScopeChannel = "channel"
const priceMatchStatusMatched = "matched"
const priceMatchStatusMissingProvider = "missing_provider"
const priceMatchStatusMissingModel = "missing_model"
const priceMatchStatusMissingAuthIndex = "missing_auth_index"
const priceMatchStatusChannelUnpriced = "channel_unpriced"
const priceMatchStatusChannelConflict = "channel_conflict"
const priceMatchStatusChannelConfigUnavailable = "channel_config_unavailable"
const priceMatchStatusInvalidPrice = "invalid_price"
const modelPriceChannelStatusReady = "ready"
const modelPriceChannelStatusMissingSelector = "missing_selector"
const modelPriceChannelStatusConflict = "conflict"

const modelPriceSelectorSnapshotTTL = 4 * time.Minute
const modelPriceSelectorSnapshotRefreshInterval = time.Minute
const modelPriceSelectorSnapshotRetryInterval = 30 * time.Second

var defaultPriorityMultipliers = map[string]float64{
	"gpt-5.5":       2.5,
	"gpt-5.4":       2,
	"gpt-5.4-mini":  2,
	"gpt-5.6-sol":   2,
	"gpt-5.6-terra": 2,
	"gpt-5.6-luna":  2,
}

type ModelPrice struct {
	ID                         int                                   `json:"id"`
	Provider                   string                                `json:"provider"`
	Model                      string                                `json:"model"`
	PriceScope                 string                                `json:"price_scope"`
	ChannelBrand               *string                               `json:"channel_brand"`
	ChannelKey                 *string                               `json:"channel_key"`
	InputUSDPerMillion         float64                               `json:"input_usd_per_million"`
	OutputUSDPerMillion        float64                               `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     float64                               `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion float64                               `json:"cache_creation_usd_per_million"`
	RequestUSD                 *float64                              `json:"request_usd"`
	PriorityMultiplier         *float64                              `json:"priority_multiplier"`
	LongContext                *ModelPriceLongContext                `json:"long_context"`
	PreservedLongContext       *ModelPriceLibraryConflictLongContext `json:"preserved_long_context"`
	BillingUnit                string                                `json:"billing_unit"`
	Source                     string                                `json:"source"`
	SourceModel                *string                               `json:"source_model"`
	AutoSynced                 bool                                  `json:"auto_synced"`
	LastSyncedAt               *time.Time                            `json:"last_synced_at"`
	UpdatedAt                  time.Time                             `json:"updated_at"`
	longContextInvalid         bool
}

type ModelPriceLibraryConflict struct {
	OriginalID               int                                   `json:"original_id"`
	SelectedPriceID          int                                   `json:"selected_price_id"`
	ConflictReason           string                                `json:"conflict_reason"`
	Price                    ModelPrice                            `json:"price"`
	ArchivedLongContext      *ModelPriceLibraryConflictLongContext `json:"archived_long_context"`
	requestUSD               sql.NullFloat64
	priorityMultiplier       sql.NullFloat64
	longContextThreshold     sql.NullInt64
	longContextInput         sql.NullFloat64
	longContextOutput        sql.NullFloat64
	longContextCacheRead     sql.NullFloat64
	longContextCacheCreation sql.NullFloat64
	lastSyncedAtRaw          sql.NullString
	updatedAtRaw             string
}

type ModelPriceLibraryConflictLongContext struct {
	ThresholdInputTokens       *int64   `json:"threshold_input_tokens"`
	InputUSDPerMillion         *float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion        *float64 `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     *float64 `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion *float64 `json:"cache_creation_usd_per_million"`
}

type ModelPriceLongContext struct {
	ThresholdInputTokens       int64   `json:"threshold_input_tokens"`
	InputUSDPerMillion         float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion        float64 `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     float64 `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion float64 `json:"cache_creation_usd_per_million"`
}

type usageTokenBreakdown struct {
	NormalInputTokens   int
	CacheReadTokens     int
	CacheCreationTokens int
	OutputTokens        int
}

type usageCostBreakdown struct {
	BillingUnit                string                   `json:"billing_unit"`
	NormalInputTokens          int                      `json:"normal_input_tokens"`
	CacheReadTokens            int                      `json:"cache_read_tokens"`
	CacheCreationTokens        int                      `json:"cache_creation_tokens"`
	OutputTokens               int                      `json:"output_tokens"`
	Items                      []usageCostBreakdownItem `json:"items"`
	TotalUSD                   float64                  `json:"total_usd"`
	Unpriced                   bool                     `json:"unpriced"`
	UnpricedReason             *string                  `json:"unpriced_reason"`
	TierMultiplier             *float64                 `json:"tier_multiplier,omitempty"`
	ContextInputTokens         int                      `json:"context_input_tokens"`
	LongContextThresholdTokens *int64                   `json:"long_context_threshold_tokens"`
	LongContextApplied         bool                     `json:"long_context_applied"`
}

type usageCostBreakdownItem interface {
	isUsageCostBreakdownItem()
}

type usageTokenCostBreakdownItem struct {
	Kind          string  `json:"kind"`
	Tokens        int     `json:"tokens"`
	USDPerMillion float64 `json:"usd_per_million"`
	SubtotalUSD   float64 `json:"subtotal_usd"`
}

func (usageTokenCostBreakdownItem) isUsageCostBreakdownItem() {}

type usageRequestCostBreakdownItem struct {
	Kind          string  `json:"kind"`
	Requests      int     `json:"requests"`
	USDPerRequest float64 `json:"usd_per_request"`
	SubtotalUSD   float64 `json:"subtotal_usd"`
}

func (usageRequestCostBreakdownItem) isUsageCostBreakdownItem() {}

type modelPricePayload struct {
	Provider                   string                        `json:"provider"`
	Model                      string                        `json:"model"`
	PriceScope                 string                        `json:"price_scope"`
	ChannelBrand               *string                       `json:"channel_brand"`
	ChannelKey                 *string                       `json:"channel_key"`
	ChannelIdentityHash        *string                       `json:"channel_identity_hash"`
	InputUSDPerMillion         float64                       `json:"input_usd_per_million"`
	OutputUSDPerMillion        float64                       `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     float64                       `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion float64                       `json:"cache_creation_usd_per_million"`
	RequestUSD                 *float64                      `json:"request_usd"`
	LongContext                *modelPriceLongContextPayload `json:"long_context"`
	PreserveInvalidLongContext *bool                         `json:"preserve_invalid_long_context"`
}

type modelPriceLongContextPayload struct {
	ThresholdInputTokens       *int64   `json:"threshold_input_tokens"`
	InputUSDPerMillion         *float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion        *float64 `json:"output_usd_per_million"`
	CacheReadUSDPerMillion     *float64 `json:"cache_read_usd_per_million"`
	CacheCreationUSDPerMillion *float64 `json:"cache_creation_usd_per_million"`
}

type priorityMultiplierPayload struct {
	PriorityMultiplier *float64 `json:"priority_multiplier"`
}

type modelPriceLibraryConflictPromotePayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type modelPriceSyncRequest struct {
	SourceURL *string `json:"source_url"`
}

type liteLLMProxySettingsPayload struct {
	Enabled  *bool   `json:"enabled"`
	ProxyURL *string `json:"proxy_url"`
}

type ModelPriceCatalogItem struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name"`
	Alias                *string                `json:"alias"`
	Object               *string                `json:"object"`
	Owner                *string                `json:"owner"`
	Created              *int                   `json:"created"`
	Metadata             map[string]any         `json:"metadata"`
	SuggestedProvider    string                 `json:"suggested_provider"`
	ChannelBrand         string                 `json:"channel_brand"`
	ChannelKey           string                 `json:"channel_key"`
	ChannelLabel         string                 `json:"channel_label"`
	ChannelIdentityHash  string                 `json:"channel_identity_hash"`
	ChannelDisabled      bool                   `json:"channel_disabled"`
	ChannelStatus        string                 `json:"channel_status"`
	ChannelLabelFallback bool                   `json:"channel_label_fallback"`
	Price                *ModelPrice            `json:"price"`
	TemplatePrice        *ModelPrice            `json:"template_price"`
	Sources              []AvailableModelSource `json:"sources"`
}

type ModelPriceCatalogResponse struct {
	HasAPIKeys           bool                     `json:"has_api_keys"`
	APIKeyCount          int                      `json:"api_key_count"`
	QueryableAPIKeyCount int                      `json:"queryable_api_key_count"`
	ChannelsAvailable    bool                     `json:"channels_available"`
	ChannelError         *string                  `json:"channel_error"`
	Models               []ModelPriceCatalogItem  `json:"models"`
	Errors               []AvailableModelKeyError `json:"errors"`
	PricedModels         int                      `json:"priced_models"`
	UnpricedModels       int                      `json:"unpriced_models"`
}

type modelCatalogAPIKey struct {
	UserAPIKey
	UserLabel string
}

type modelPriceIndex map[[2]string]ModelPrice

type libraryPriceIndex map[[2]string]ModelPrice

type modelPriceChannelIdentity struct {
	Brand      aiProviderBrand
	ChannelKey string
	Model      string
}

type modelPriceChannelSelectorIndex map[modelPriceChannelIdentity]int

type modelPriceMatchContext struct {
	Selectors          modelPriceChannelSelectorIndex
	SelectorsRequired  bool
	SelectorsAvailable bool
}

type modelPriceBillingIndex struct {
	Prices       modelPriceIndex
	MatchContext modelPriceMatchContext
}

type modelPriceSelectorSnapshotCache struct {
	mu             sync.RWMutex
	configKey      string
	selectors      modelPriceChannelSelectorIndex
	available      bool
	loadedAt       time.Time
	expiresAt      time.Time
	refreshAfter   time.Time
	lastRefreshErr error
	generation     uint64
	refreshing     bool
	refreshDone    chan struct{}
}

func cloneModelPriceChannelSelectors(source modelPriceChannelSelectorIndex) modelPriceChannelSelectorIndex {
	cloned := make(modelPriceChannelSelectorIndex, len(source))
	for identity, count := range source {
		cloned[identity] = count
	}
	return cloned
}

func (cache *modelPriceSelectorSnapshotCache) snapshot() (modelPriceChannelSelectorIndex, bool) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if !cache.available || !time.Now().Before(cache.expiresAt) {
		return nil, false
	}
	return cloneModelPriceChannelSelectors(cache.selectors), true
}

func (cache *modelPriceSelectorSnapshotCache) snapshotForConfig(configKey string) (modelPriceChannelSelectorIndex, bool) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if configKey == "" || cache.configKey != configKey || !cache.available || !time.Now().Before(cache.expiresAt) {
		return nil, false
	}
	return cloneModelPriceChannelSelectors(cache.selectors), true
}

func (cache *modelPriceSelectorSnapshotCache) retainConfig(configKey string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.configKey != configKey {
		cache.resetLocked(configKey)
	}
}

func (cache *modelPriceSelectorSnapshotCache) currentGeneration(configKey string) (uint64, bool) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.generation, cache.configKey == configKey
}

func (cache *modelPriceSelectorSnapshotCache) refreshErrorForConfig(configKey string) error {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if cache.configKey != configKey {
		return nil
	}
	return cache.lastRefreshErr
}

func (cache *modelPriceSelectorSnapshotCache) invalidate(configKey string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.configKey != configKey {
		return
	}
	cache.resetLocked(configKey)
}

func (cache *modelPriceSelectorSnapshotCache) resetLocked(configKey string) {
	cache.configKey = configKey
	cache.selectors = nil
	cache.available = false
	cache.loadedAt = time.Time{}
	cache.expiresAt = time.Time{}
	cache.refreshAfter = time.Time{}
	cache.lastRefreshErr = nil
	cache.generation++
}

func (cache *modelPriceSelectorSnapshotCache) beginRefresh(configKey string, now time.Time) (bool, uint64, <-chan struct{}, chan struct{}) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.configKey != configKey {
		return false, cache.generation, nil, nil
	}
	if now.Before(cache.refreshAfter) {
		return false, cache.generation, nil, nil
	}
	if cache.refreshing {
		return false, cache.generation, cache.refreshDone, nil
	}
	cache.refreshing = true
	cache.refreshDone = make(chan struct{})
	return true, cache.generation, nil, cache.refreshDone
}

func (cache *modelPriceSelectorSnapshotCache) finishRefresh(configKey string, generation uint64, done chan struct{}, startedAt time.Time, selectors modelPriceChannelSelectorIndex, refreshErr error) {
	finishedAt := time.Now()
	var closeDone chan struct{}
	cache.mu.Lock()
	if cache.refreshDone == done {
		cache.refreshing = false
		cache.refreshDone = nil
		closeDone = done
	}
	if cache.configKey == configKey && cache.generation == generation && !startedAt.Before(cache.loadedAt) {
		if refreshErr == nil {
			cache.selectors = cloneModelPriceChannelSelectors(selectors)
			cache.available = true
			cache.loadedAt = startedAt
			cache.expiresAt = finishedAt.Add(modelPriceSelectorSnapshotTTL)
			cache.refreshAfter = finishedAt.Add(modelPriceSelectorSnapshotRefreshInterval)
			cache.lastRefreshErr = nil
		} else {
			cache.refreshAfter = finishedAt.Add(modelPriceSelectorSnapshotRetryInterval)
			cache.lastRefreshErr = refreshErr
		}
	}
	cache.mu.Unlock()
	if closeDone != nil {
		close(closeDone)
	}
}

func (cache *modelPriceSelectorSnapshotCache) store(configKey string, generation uint64, startedAt time.Time, selectors modelPriceChannelSelectorIndex) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.configKey != configKey || cache.generation != generation || startedAt.Before(cache.loadedAt) {
		return false
	}
	cache.selectors = cloneModelPriceChannelSelectors(selectors)
	cache.available = true
	cache.loadedAt = startedAt
	storedAt := time.Now()
	cache.expiresAt = storedAt.Add(modelPriceSelectorSnapshotTTL)
	cache.refreshAfter = storedAt.Add(modelPriceSelectorSnapshotRefreshInterval)
	cache.lastRefreshErr = nil
	return true
}

func (a *App) handleModelPrices(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		prices, err := a.listPrices(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, modelPricesForAPI(prices))
		return nil
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/sync/litellm") {
			return a.handleSyncLiteLLMPrices(w, r)
		}
		var payload modelPricePayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		price, err := a.createPrice(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, modelPriceForAPI(price))
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) handleModelPriceByPath(w http.ResponseWriter, r *http.Request) error {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/model-prices/"), "/")
	if path == "sync/litellm" {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		return a.handleSyncLiteLLMPrices(w, r)
	}
	if path == "litellm-proxy" {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		return a.handleLiteLLMProxySettings(w, r)
	}
	if path == "catalog" {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		response, err := a.modelPriceCatalog(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, modelPriceCatalogForAPI(response))
		return nil
	}
	if path == "library-conflicts" {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		conflicts, err := a.listModelPriceLibraryConflicts(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, modelPriceLibraryConflictsForAPI(conflicts))
		return nil
	}
	if strings.HasPrefix(path, "library-conflicts/") {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		parts := strings.Split(strings.TrimPrefix(path, "library-conflicts/"), "/")
		id, err := parseIntPath(parts[0])
		if err != nil {
			return err
		}
		if len(parts) == 1 {
			if err := requireMethod(r, http.MethodDelete); err != nil {
				return err
			}
			if err := a.deleteModelPriceLibraryConflict(r.Context(), id); err != nil {
				return err
			}
			writeNoContent(w)
			return nil
		}
		if len(parts) != 2 {
			return notFoundError("迁移冲突价格不存在")
		}
		switch parts[1] {
		case "promote":
			if err := requireMethod(r, http.MethodPut); err != nil {
				return err
			}
			var payload modelPriceLibraryConflictPromotePayload
			if err := decodeJSON(r, &payload); err != nil {
				return err
			}
			price, err := a.promoteModelPriceLibraryConflict(r.Context(), id, payload)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, modelPriceForAPI(price))
			return nil
		case "replace-active":
			if err := requireMethod(r, http.MethodPut); err != nil {
				return err
			}
			price, err := a.replaceActiveModelPriceLibraryConflict(r.Context(), id)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, modelPriceForAPI(price))
			return nil
		default:
			return notFoundError("迁移冲突价格不存在")
		}
	}
	if strings.HasSuffix(path, "/priority-multiplier") {
		if _, err := a.adminUser(r.Context(), r); err != nil {
			return err
		}
		if err := requireMethod(r, http.MethodPut); err != nil {
			return err
		}
		id, err := parseIntPath(strings.TrimSuffix(path, "/priority-multiplier"))
		if err != nil {
			return err
		}
		var payload priorityMultiplierPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		price, err := a.updatePriorityMultiplier(r.Context(), id, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, modelPriceForAPI(price))
		return nil
	}
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	idText := path
	id, err := parseIntPath(idText)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		var payload modelPricePayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		price, err := a.updatePrice(r.Context(), id, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, modelPriceForAPI(price))
		return nil
	case http.MethodDelete:
		if err := a.deletePrice(r.Context(), id); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) handleLiteLLMProxySettings(w http.ResponseWriter, r *http.Request) error {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.loadConfig(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, liteLLMProxySettingsResponse(cfg.LiteLLMProxy))
		return nil
	case http.MethodPut:
		var payload liteLLMProxySettingsPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		cfg, err := a.loadConfig(r.Context())
		if err != nil {
			return err
		}
		next := cfg.LiteLLMProxy
		if payload.Enabled != nil {
			next.Enabled = *payload.Enabled
		}
		if payload.ProxyURL != nil {
			next.ProxyURL = strings.TrimSpace(*payload.ProxyURL)
		}
		normalized, err := normalizeLiteLLMProxyConfig(next)
		if err != nil {
			return err
		}
		cfg.LiteLLMProxy = normalized
		if err := a.saveConfig(r.Context(), cfg); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, liteLLMProxySettingsResponse(cfg.LiteLLMProxy))
		return nil
	default:
		return methodNotAllowed()
	}
}

func liteLLMProxySettingsResponse(cfg LiteLLMProxyConfig) map[string]any {
	return map[string]any{
		"enabled":   cfg.Enabled,
		"proxy_url": cfg.ProxyURL,
	}
}

func validatePricePayload(payload modelPricePayload) (modelPricePayload, error) {
	payload.Provider = strings.TrimSpace(payload.Provider)
	payload.Model = strings.TrimSpace(payload.Model)
	payload.PriceScope = strings.ToLower(strings.TrimSpace(payload.PriceScope))
	if payload.PriceScope == "" {
		payload.PriceScope = modelPriceScopeLibrary
	}
	if payload.ChannelIdentityHash != nil {
		value := strings.TrimSpace(*payload.ChannelIdentityHash)
		if value == "" {
			payload.ChannelIdentityHash = nil
		} else {
			payload.ChannelIdentityHash = &value
		}
	}
	switch payload.PriceScope {
	case modelPriceScopeLibrary:
		if strings.TrimSpace(aiProviderOptionalString(payload.ChannelBrand)) != "" || strings.TrimSpace(aiProviderOptionalString(payload.ChannelKey)) != "" {
			return payload, validationError("通用价格不能包含渠道标识")
		}
		payload.ChannelBrand = nil
		payload.ChannelKey = nil
		payload.ChannelIdentityHash = nil
	case modelPriceScopeChannel:
		brand := strings.ToLower(strings.TrimSpace(aiProviderOptionalString(payload.ChannelBrand)))
		if !isModelPriceChannelBrand(brand) {
			return payload, validationError("渠道品牌无效")
		}
		key := canonicalModelPriceChannelKey(brand, aiProviderOptionalString(payload.ChannelKey))
		if key == "" || strings.ContainsRune(key, '\x00') {
			return payload, validationError("渠道标识不能为空")
		}
		model := normalizeModelPriceChannelModel(payload.Model)
		if model == "" {
			return payload, validationError("模型不能为空")
		}
		payload.ChannelBrand = &brand
		payload.ChannelKey = &key
		payload.Model = model
	default:
		return payload, validationError("价格范围无效")
	}
	if payload.Provider == "" || payload.Model == "" {
		return payload, validationError("provider/model 不能为空")
	}
	if !finiteNonNegative(payload.InputUSDPerMillion) ||
		!finiteNonNegative(payload.OutputUSDPerMillion) ||
		!finiteNonNegative(payload.CacheReadUSDPerMillion) ||
		!finiteNonNegative(payload.CacheCreationUSDPerMillion) ||
		(payload.RequestUSD != nil && !finiteNonNegative(*payload.RequestUSD)) {
		return payload, validationError("价格不能为负数")
	}
	if payload.LongContext != nil {
		if billingUnitForModel(payload.Model) != modelBillingUnitToken {
			return payload, validationError("按次计费模型不支持长上下文阶梯价格")
		}
		if _, err := longContextFromPayload(payload.LongContext); err != nil {
			return payload, err
		}
	}
	return payload, nil
}

func modelPriceFromPayload(payload modelPricePayload, priorityMultiplier *float64) ModelPrice {
	longContext, _ := longContextFromPayload(payload.LongContext)
	return ModelPrice{
		Provider:                   payload.Provider,
		Model:                      payload.Model,
		PriceScope:                 payload.PriceScope,
		ChannelBrand:               payload.ChannelBrand,
		ChannelKey:                 payload.ChannelKey,
		InputUSDPerMillion:         payload.InputUSDPerMillion,
		OutputUSDPerMillion:        payload.OutputUSDPerMillion,
		CacheReadUSDPerMillion:     payload.CacheReadUSDPerMillion,
		CacheCreationUSDPerMillion: payload.CacheCreationUSDPerMillion,
		RequestUSD:                 payload.RequestUSD,
		PriorityMultiplier:         priorityMultiplier,
		LongContext:                longContext,
	}
}

func isModelPriceChannelBrand(brand string) bool {
	switch aiProviderBrand(brand) {
	case aiProviderBrandGemini, aiProviderBrandCodex, aiProviderBrandClaude, aiProviderBrandOpenAICompatibility, aiProviderBrandVertex:
		return true
	default:
		return false
	}
}

func canonicalModelPriceChannelKey(brand, key string) string {
	key = strings.TrimSpace(key)
	if aiProviderBrand(brand) == aiProviderBrandOpenAICompatibility {
		return strings.ToLower(key)
	}
	return key
}

func normalizeModelPriceChannelModel(model string) string {
	return strings.TrimSpace(normalizeAIProviderModelName(model))
}

func (a *App) validateChannelPriceCreate(ctx context.Context, payload modelPricePayload) (modelPricePayload, error) {
	if payload.PriceScope != modelPriceScopeChannel {
		return payload, nil
	}
	identityHash := strings.TrimSpace(aiProviderOptionalString(payload.ChannelIdentityHash))
	if identityHash == "" {
		return payload, validationError("缺少渠道身份标识，请刷新页面后重试")
	}
	providers, err := a.aiProviderConfigSnapshot(ctx)
	if err != nil {
		return payload, validationError("读取渠道配置失败：" + err.Error())
	}
	brand := aiProviderBrand(aiProviderOptionalString(payload.ChannelBrand))
	requestedChannelKey := canonicalModelPriceChannelKey(string(brand), aiProviderOptionalString(payload.ChannelKey))
	requestedModel := normalizeModelPriceChannelModel(payload.Model)
	type channelPriceCreateMatch struct {
		provider        aiProviderItem
		channelKey      string
		configuredModel string
	}
	matches := make([]channelPriceCreateMatch, 0, 1)
	selectorMatched := false
	for _, provider := range providers {
		if provider.Brand != brand || strings.TrimSpace(provider.IdentityHash) != identityHash {
			continue
		}
		channelKey, _, _ := modelPriceChannelSelector(provider)
		if channelKey == "" || channelKey != requestedChannelKey {
			continue
		}
		selectorMatched = true
		for _, model := range provider.Models {
			candidate := normalizeModelPriceChannelModel(model.Name)
			if strings.EqualFold(candidate, requestedModel) {
				matches = append(matches, channelPriceCreateMatch{
					provider:        provider,
					channelKey:      channelKey,
					configuredModel: candidate,
				})
				break
			}
		}
	}
	if len(matches) == 0 {
		if selectorMatched {
			return payload, validationError("该渠道未配置此模型，请刷新页面后重试")
		}
		return payload, conflictError("渠道配置已变化，请刷新页面后重试")
	}
	if len(matches) > 1 {
		return payload, conflictError("渠道身份存在冲突，请先修正供应商配置")
	}
	provider := matches[0].provider
	channelKey := matches[0].channelKey
	configuredModel := matches[0].configuredModel
	selectors := modelPriceChannelSelectors(providers)
	if modelPriceChannelHasRuntimeConflict(selectors, provider, channelKey, configuredModel) {
		return payload, conflictError("渠道标识存在冲突，请先修正供应商配置")
	}
	payload.Model = configuredModel
	payload.ChannelKey = &channelKey
	if provider.Brand == aiProviderBrandOpenAICompatibility {
		payload.Provider = strings.TrimSpace(aiProviderOptionalString(provider.Name))
	} else {
		payload.Provider = string(provider.Brand)
	}
	return payload, nil
}

func longContextFromPayload(payload *modelPriceLongContextPayload) (*ModelPriceLongContext, error) {
	if payload == nil {
		return nil, nil
	}
	if payload.ThresholdInputTokens == nil || payload.InputUSDPerMillion == nil ||
		payload.OutputUSDPerMillion == nil || payload.CacheReadUSDPerMillion == nil ||
		payload.CacheCreationUSDPerMillion == nil {
		return nil, validationError("长上下文价格必须完整填写")
	}
	result := &ModelPriceLongContext{
		ThresholdInputTokens:       *payload.ThresholdInputTokens,
		InputUSDPerMillion:         *payload.InputUSDPerMillion,
		OutputUSDPerMillion:        *payload.OutputUSDPerMillion,
		CacheReadUSDPerMillion:     *payload.CacheReadUSDPerMillion,
		CacheCreationUSDPerMillion: *payload.CacheCreationUSDPerMillion,
	}
	if !validLongContextPrice(result) {
		return nil, validationError("长上下文阈值必须为正整数，价格必须是有限非负数")
	}
	return result, nil
}

func longContextPayloadFromPrice(price *ModelPriceLongContext) *modelPriceLongContextPayload {
	if price == nil {
		return nil
	}
	threshold := price.ThresholdInputTokens
	inputUSD := price.InputUSDPerMillion
	outputUSD := price.OutputUSDPerMillion
	cacheReadUSD := price.CacheReadUSDPerMillion
	cacheCreationUSD := price.CacheCreationUSDPerMillion
	return &modelPriceLongContextPayload{
		ThresholdInputTokens:       &threshold,
		InputUSDPerMillion:         &inputUSD,
		OutputUSDPerMillion:        &outputUSD,
		CacheReadUSDPerMillion:     &cacheReadUSD,
		CacheCreationUSDPerMillion: &cacheCreationUSD,
	}
}

func defaultLongContextPrice(provider, model string) *ModelPriceLongContext {
	value, ok := pricingdefaults.LookupLongContext(provider, model)
	if !ok {
		return nil
	}
	result := &ModelPriceLongContext{
		ThresholdInputTokens:       value.ThresholdInputTokens,
		InputUSDPerMillion:         value.InputUSDPerMillion,
		OutputUSDPerMillion:        value.OutputUSDPerMillion,
		CacheReadUSDPerMillion:     value.CacheReadUSDPerMillion,
		CacheCreationUSDPerMillion: value.CacheCreationUSDPerMillion,
	}
	if !validLongContextPrice(result) {
		return nil
	}
	return result
}

func validLongContextPrice(price *ModelPriceLongContext) bool {
	return price != nil && price.ThresholdInputTokens > 0 &&
		finiteNonNegative(price.InputUSDPerMillion) &&
		finiteNonNegative(price.OutputUSDPerMillion) &&
		finiteNonNegative(price.CacheReadUSDPerMillion) &&
		finiteNonNegative(price.CacheCreationUSDPerMillion)
}

func modelPriceForAPI(price ModelPrice) ModelPrice {
	if price.PriceScope == "" {
		price.PriceScope = modelPriceScopeLibrary
	}
	if price.PriorityMultiplier != nil && (math.IsNaN(*price.PriorityMultiplier) || math.IsInf(*price.PriorityMultiplier, 0)) {
		price.PriorityMultiplier = nil
	}
	if price.longContextInvalid || (price.LongContext != nil && !validLongContextPrice(price.LongContext)) {
		price.LongContext = nil
	}
	return price
}

func modelPricesForAPI(prices []ModelPrice) []ModelPrice {
	result := make([]ModelPrice, len(prices))
	for index, price := range prices {
		result[index] = modelPriceForAPI(price)
	}
	return result
}

func modelPriceLibraryConflictsForAPI(conflicts []ModelPriceLibraryConflict) []ModelPriceLibraryConflict {
	result := make([]ModelPriceLibraryConflict, len(conflicts))
	for index, conflict := range conflicts {
		conflict.Price = modelPriceForAPI(conflict.Price)
		result[index] = conflict
	}
	return result
}

func modelPriceCatalogForAPI(response ModelPriceCatalogResponse) ModelPriceCatalogResponse {
	for index := range response.Models {
		if response.Models[index].Price != nil {
			price := modelPriceForAPI(*response.Models[index].Price)
			response.Models[index].Price = &price
		}
		if response.Models[index].TemplatePrice != nil {
			price := modelPriceForAPI(*response.Models[index].TemplatePrice)
			response.Models[index].TemplatePrice = &price
		}
	}
	return response
}

func validatePriorityMultiplierForPrice(price ModelPrice) error {
	if price.LongContext != nil && !validLongContextPrice(price.LongContext) {
		return validationError("长上下文价格配置无效")
	}
	if price.PriorityMultiplier == nil || !modelPriceSupportsPriority(price) {
		return nil
	}
	if !priorityMultiplierProducesRoundableCost(price, *price.PriorityMultiplier) {
		return validationError("Fast 倍率会产生无法安全计价的金额")
	}
	return nil
}

func (a *App) listPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, provider, model, price_scope, channel_brand, channel_key,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, CAST(last_synced_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_prices
		ORDER BY price_scope DESC, auto_synced ASC, lower(provider), lower(model)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrices(rows)
}

func (a *App) billingPriceIndex(ctx context.Context) (modelPriceBillingIndex, error) {
	result, err := a.billingPriceIndexWithoutSelectors(ctx)
	if err != nil {
		return modelPriceBillingIndex{}, err
	}
	return a.attachCachedBillingPriceSelectors(result), nil
}

func (a *App) billingPriceIndexWithoutSelectors(ctx context.Context) (modelPriceBillingIndex, error) {
	prices, err := a.listPrices(ctx)
	if err != nil {
		return modelPriceBillingIndex{}, err
	}
	result := modelPriceBillingIndex{Prices: channelPricesByKey(prices)}
	result.MatchContext.SelectorsRequired = modelPriceIndexNeedsConfiguredSelectors(result.Prices)
	return result, nil
}

func (a *App) attachCachedBillingPriceSelectors(result modelPriceBillingIndex) modelPriceBillingIndex {
	selectors, available := a.priceSelectors.snapshot()
	if !available {
		return result
	}
	return attachBillingPriceSelectors(result, selectors)
}

func (a *App) attachCachedBillingPriceSelectorsForConfig(result modelPriceBillingIndex, cfg AppConfig) (modelPriceBillingIndex, bool) {
	selectors, available := a.priceSelectors.snapshotForConfig(modelPriceSelectorConfigKey(cfg))
	if !available {
		return result, false
	}
	return attachBillingPriceSelectors(result, selectors), true
}

func attachBillingPriceSelectors(result modelPriceBillingIndex, selectors modelPriceChannelSelectorIndex) modelPriceBillingIndex {
	result.MatchContext.Selectors = selectors
	result.MatchContext.SelectorsAvailable = true
	return result
}

func modelPriceSelectorConfigKey(cfg AppConfig) string {
	managementKey := strings.TrimSpace(cfg.Collector.ManagementKey)
	if strings.TrimSpace(cfg.Collector.CLIProxyURL) == "" || managementKey == "" {
		return ""
	}
	baseURL, err := collectorManagementHTTPURL(cfg.Collector.CLIProxyURL)
	if err != nil {
		return ""
	}
	return hashAPIKey(baseURL + "\x00" + managementKey)
}

func (a *App) refreshModelPriceSelectorsIfStale(ctx context.Context, cfg AppConfig) error {
	if strings.TrimSpace(cfg.Collector.CLIProxyURL) != "" && strings.TrimSpace(cfg.Collector.ManagementKey) != "" {
		if _, err := collectorManagementHTTPURL(cfg.Collector.CLIProxyURL); err != nil {
			return err
		}
	}
	configKey := modelPriceSelectorConfigKey(cfg)
	if configKey == "" {
		return nil
	}
	for {
		startedAt := time.Now()
		load, generation, wait, done := a.priceSelectors.beginRefresh(configKey, startedAt)
		if !load {
			if wait == nil {
				return a.priceSelectors.refreshErrorForConfig(configKey)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
				continue
			}
		}
		providers, err := a.aiProviderConfigSnapshotWithConfig(ctx, cfg)
		selectors := modelPriceChannelSelectorIndex(nil)
		if err == nil {
			selectors = modelPriceChannelSelectors(providers)
		}
		a.priceSelectors.finishRefresh(configKey, generation, done, startedAt, selectors, err)
		return err
	}
}

func (a *App) storeModelPriceSelectorSnapshot(cfg AppConfig, generation uint64, providers []aiProviderItem, startedAt time.Time) bool {
	configKey := modelPriceSelectorConfigKey(cfg)
	if configKey == "" {
		return false
	}
	return a.priceSelectors.store(configKey, generation, startedAt, modelPriceChannelSelectors(providers))
}

func (a *App) invalidateModelPriceSelectorSnapshot(cfg AppConfig) {
	a.priceSelectors.invalidate(modelPriceSelectorConfigKey(cfg))
}

func channelPricesByKey(prices []ModelPrice) modelPriceIndex {
	result := make(modelPriceIndex, len(prices))
	for _, price := range prices {
		if price.PriceScope != modelPriceScopeChannel || price.ChannelBrand == nil || price.ChannelKey == nil {
			continue
		}
		brand := strings.TrimSpace(*price.ChannelBrand)
		channelKey := canonicalModelPriceChannelKey(brand, *price.ChannelKey)
		model := normalizeModelPriceChannelModel(price.Model)
		if channelKey == "" || model == "" {
			continue
		}
		if aiProviderBrand(brand) == aiProviderBrandOpenAICompatibility {
			result[priceKey(channelKey, model)] = price
			continue
		}
		result[nativeModelPriceKey(brand, channelKey, model)] = price
	}
	return result
}

func modelPriceIndexNeedsConfiguredSelectors(prices modelPriceIndex) bool {
	for _, price := range prices {
		if price.PriceScope == modelPriceScopeChannel {
			return true
		}
	}
	return false
}

func (a *App) libraryPriceMap(ctx context.Context) (libraryPriceIndex, error) {
	prices, err := a.listPrices(ctx)
	if err != nil {
		return nil, err
	}
	return libraryPricesByKey(prices), nil
}

func nativeModelPriceKey(brand, channelKey, model string) [2]string {
	return [2]string{strings.ToLower(strings.TrimSpace(brand)) + "\x00" + strings.TrimSpace(channelKey), strings.ToLower(normalizeModelPriceChannelModel(model))}
}

func (a *App) modelPriceCatalog(ctx context.Context) (ModelPriceCatalogResponse, error) {
	response := ModelPriceCatalogResponse{
		Models: []ModelPriceCatalogItem{},
		Errors: []AvailableModelKeyError{},
	}
	prices, err := a.listPrices(ctx)
	if err != nil {
		return ModelPriceCatalogResponse{}, err
	}
	providers, err := a.aiProviderConfigSnapshot(ctx)
	if err != nil {
		message := err.Error()
		response.ChannelError = &message
		return response, nil
	}
	response.ChannelsAvailable = true
	response.HasAPIKeys = len(providers) > 0
	response.APIKeyCount = len(providers)
	response.QueryableAPIKeyCount = len(providers)
	libraryLookup := libraryPricesByKey(prices)
	channelLookup := channelPricesByKey(prices)
	selectors := modelPriceChannelSelectors(providers)
	for _, provider := range providers {
		channelKey, channelLabel, labelFallback := modelPriceChannelSelector(provider)
		baseChannelStatus := modelPriceChannelStatusReady
		if channelKey == "" {
			baseChannelStatus = modelPriceChannelStatusMissingSelector
		}
		seenModels := map[string]bool{}
		for _, configuredModel := range provider.Models {
			model := normalizeModelPriceChannelModel(configuredModel.Name)
			modelKey := strings.ToLower(model)
			if model == "" || seenModels[modelKey] {
				continue
			}
			seenModels[modelKey] = true
			channelStatus := baseChannelStatus
			if channelStatus == modelPriceChannelStatusReady && modelPriceChannelHasRuntimeConflict(selectors, provider, channelKey, model) {
				channelStatus = modelPriceChannelStatusConflict
			}
			alias := optionalCatalogModelAlias(configuredModel.Alias)
			suggestedProvider := suggestedPriceProviderForChannel(provider.Brand, channelLabel, model)
			price := findCatalogChannelPrice(channelLookup, provider.Brand, channelKey, model)
			templatePrice := findCatalogPrice(libraryLookup, prices, suggestedProvider, nil, configuredModel.Name)
			item := ModelPriceCatalogItem{
				ID:                   modelPriceCatalogItemID(provider, channelKey, modelKey),
				Name:                 model,
				Alias:                alias,
				Metadata:             map[string]any{},
				SuggestedProvider:    suggestedProvider,
				ChannelBrand:         string(provider.Brand),
				ChannelKey:           channelKey,
				ChannelLabel:         channelLabel,
				ChannelIdentityHash:  provider.IdentityHash,
				ChannelDisabled:      provider.Disabled != nil && *provider.Disabled,
				ChannelStatus:        channelStatus,
				ChannelLabelFallback: labelFallback,
				Price:                price,
				TemplatePrice:        templatePrice,
				Sources:              []AvailableModelSource{},
			}
			if channelStatus != modelPriceChannelStatusReady || !modelPriceReadyForBilling(item.Price, item.Name) {
				response.UnpricedModels++
			} else {
				response.PricedModels++
			}
			response.Models = append(response.Models, item)
		}
	}
	sort.Slice(response.Models, func(i, j int) bool {
		left, right := response.Models[i], response.Models[j]
		if left.ChannelBrand != right.ChannelBrand {
			return left.ChannelBrand < right.ChannelBrand
		}
		if !strings.EqualFold(left.ChannelLabel, right.ChannelLabel) {
			return strings.ToLower(left.ChannelLabel) < strings.ToLower(right.ChannelLabel)
		}
		return strings.ToLower(left.Name) < strings.ToLower(right.Name)
	})
	return response, nil
}

func modelPriceCatalogItemID(provider aiProviderItem, channelKey, modelKey string) string {
	return fmt.Sprintf("%s:%s:%d:%s:%d:%s", provider.Brand, provider.IdentityHash, len(channelKey), channelKey, provider.Index, modelKey)
}

func modelPriceChannelSelectors(providers []aiProviderItem) modelPriceChannelSelectorIndex {
	selectors := modelPriceChannelSelectorIndex{}
	for _, provider := range providers {
		channelKey, _, _ := modelPriceChannelSelector(provider)
		if channelKey == "" && provider.Brand == aiProviderBrandOpenAICompatibility {
			continue
		}
		seenModels := map[string]bool{}
		for _, configuredModel := range provider.Models {
			model := normalizeModelPriceChannelModel(configuredModel.Name)
			modelKey := strings.ToLower(model)
			if model == "" || seenModels[modelKey] {
				continue
			}
			seenModels[modelKey] = true
			selectors[modelPriceChannelIdentityKey(provider.Brand, channelKey, model)]++
		}
	}
	return selectors
}

func modelPriceChannelIdentityKey(brand aiProviderBrand, channelKey, model string) modelPriceChannelIdentity {
	return modelPriceChannelIdentity{
		Brand:      brand,
		ChannelKey: canonicalModelPriceChannelKey(string(brand), channelKey),
		Model:      strings.ToLower(normalizeModelPriceChannelModel(model)),
	}
}

func modelPriceChannelSelector(provider aiProviderItem) (string, string, bool) {
	if provider.Brand == aiProviderBrandOpenAICompatibility {
		name := strings.TrimSpace(aiProviderOptionalString(provider.Name))
		return canonicalModelPriceChannelKey(string(provider.Brand), name), name, false
	}
	authIndex := strings.TrimSpace(aiProviderOptionalString(provider.AuthIndex))
	if label := strings.TrimSpace(aiProviderOptionalString(provider.APIKeyMasked)); label != "" {
		return authIndex, label, false
	}
	if authIndex != "" {
		return authIndex, authIndex, true
	}
	identity := strings.TrimSpace(provider.IdentityHash)
	if identity == "" {
		return authIndex, "未识别密钥", true
	}
	if len(identity) > 12 {
		identity = identity[:12]
	}
	return authIndex, "密钥 " + identity, true
}

func modelPriceChannelHasRuntimeConflict(selectors modelPriceChannelSelectorIndex, provider aiProviderItem, channelKey, model string) bool {
	if provider.Brand == aiProviderBrandOpenAICompatibility {
		runtimeProvider := strings.TrimSpace(aiProviderOptionalString(provider.Name))
		if strings.TrimSpace(aiProviderOptionalString(provider.AuthIndex)) == "" {
			compatibleCount, nativeCount := configuredMissingAuthModelPriceCandidateCounts(selectors, runtimeProvider, model)
			if compatibleCount > 0 && nativeCount > 0 {
				return true
			}
		}
		_, matchCount := configuredModelPriceCandidates(selectors, runtimeProvider, model, provider.AuthIndex)
		return matchCount > 1
	}
	authIndex := channelKey
	for _, runtimeProvider := range nativeModelPriceRuntimeProviders(selectors, provider.Brand, model) {
		_, matchCount := configuredModelPriceCandidates(selectors, runtimeProvider, model, &authIndex)
		if matchCount > 1 {
			return true
		}
	}
	return false
}

func nativeModelPriceRuntimeProviders(selectors modelPriceChannelSelectorIndex, brand aiProviderBrand, model string) []string {
	providers := make([]string, 0, 6)
	seen := map[string]bool{}
	appendProvider := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		providers = append(providers, value)
	}
	appendProvider(string(brand))
	for _, config := range aiProviderBrandConfigs {
		if config.Brand != brand {
			continue
		}
		appendProvider(config.ConfigKey)
		appendProvider(config.Label)
		break
	}
	switch brand {
	case aiProviderBrandGemini, aiProviderBrandVertex:
		appendProvider("google")
	case aiProviderBrandClaude:
		appendProvider("anthropic")
	}
	modelKey := strings.ToLower(normalizeModelPriceChannelModel(model))
	for identity := range selectors {
		if identity.Brand != aiProviderBrandOpenAICompatibility || identity.Model != modelKey {
			continue
		}
		for _, matchedBrand := range matchingNativePriceBrands(identity.ChannelKey) {
			if matchedBrand == brand {
				appendProvider(identity.ChannelKey)
				break
			}
		}
	}
	return providers
}

func optionalCatalogModelAlias(alias string) *string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil
	}
	return &alias
}

func suggestedPriceProviderForChannel(brand aiProviderBrand, channelLabel, model string) string {
	switch brand {
	case aiProviderBrandGemini:
		return string(aiProviderBrandGemini)
	case aiProviderBrandVertex:
		return "google"
	case aiProviderBrandCodex:
		return string(aiProviderBrandCodex)
	case aiProviderBrandClaude:
		return string(aiProviderBrandClaude)
	case aiProviderBrandOpenAICompatibility:
		if index := strings.Index(model, "/"); index > 0 {
			return strings.TrimSpace(model[:index])
		}
		return strings.TrimSpace(channelLabel)
	default:
		return ""
	}
}

func findCatalogChannelPrice(prices modelPriceIndex, brand aiProviderBrand, channelKey, model string) *ModelPrice {
	var key [2]string
	if brand == aiProviderBrandOpenAICompatibility {
		key = priceKey(channelKey, model)
	} else {
		key = nativeModelPriceKey(string(brand), channelKey, model)
	}
	price, ok := prices[key]
	if !ok {
		return nil
	}
	return &price
}

func (a *App) modelCatalogAPIKeys(ctx context.Context) ([]modelCatalogAPIKey, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT k.api_key_hash, k.user_id, k.api_key, k.description, CAST(k.created_at AS TEXT), CAST(k.updated_at AS TEXT),
		       u.username, u.nickname
		FROM user_api_keys k
		INNER JOIN users u ON u.id = k.user_id
		WHERE u.disabled_at IS NULL
		ORDER BY lower(u.username), lower(k.description), k.api_key_hash
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []modelCatalogAPIKey
	for rows.Next() {
		var item modelCatalogAPIKey
		var apiKey, createdAt, updatedAt, username, nickname sql.NullString
		if err := rows.Scan(&item.APIKeyHash, &item.UserID, &apiKey, &item.Description, &createdAt, &updatedAt, &username, &nickname); err != nil {
			return nil, err
		}
		item.APIKey = nullableString(apiKey)
		item.CreatedAt = timePtr(createdAt)
		item.UpdatedAt = timePtr(updatedAt)
		item.UserLabel = userLabelFromParts(username, nickname)
		result = append(result, item)
	}
	return result, rows.Err()
}

func libraryPricesByKey(prices []ModelPrice) libraryPriceIndex {
	result := make(libraryPriceIndex, len(prices))
	for _, price := range prices {
		scope := price.PriceScope
		if scope == "" {
			scope = modelPriceScopeLibrary
		}
		if scope != modelPriceScopeLibrary {
			continue
		}
		result[priceKey(price.Provider, price.Model)] = price
	}
	return result
}

func catalogAvailableModelSource(binding modelCatalogAPIKey) AvailableModelSource {
	source := availableModelSource(binding.UserAPIKey)
	userID := binding.UserID
	source.UserID = &userID
	source.UserLabel = binding.UserLabel
	return source
}

func userLabelFromParts(username, nickname sql.NullString) string {
	label := strings.TrimSpace(nickname.String)
	if label == "" {
		label = strings.TrimSpace(username.String)
	}
	if label == "" {
		label = "未知用户"
	}
	return label
}

func suggestedPriceProvider(model AvailableModelItem) string {
	if model.Owner != nil {
		if owner := strings.TrimSpace(*model.Owner); owner != "" {
			return owner
		}
	}
	if idx := strings.Index(model.ID, "/"); idx > 0 {
		return strings.TrimSpace(model.ID[:idx])
	}
	return ""
}

func findCatalogPrice(prices libraryPriceIndex, allPrices []ModelPrice, suggestedProvider string, owner *string, modelID string) *ModelPrice {
	providers := []string{}
	if owner != nil {
		providers = append(providers, *owner)
	}
	if strings.TrimSpace(suggestedProvider) != "" {
		providers = append(providers, suggestedProvider)
	}
	modelCandidates := catalogModelCandidates(modelID)
	geminiLookup := strings.EqualFold(strings.TrimSpace(suggestedProvider), string(aiProviderBrandGemini))
	if geminiLookup {
		modelCandidates = geminiCatalogModelCandidates(modelCandidates)
	}
	for _, provider := range providers {
		for _, candidate := range modelCandidates {
			if price := findMatchingLibraryPrice(prices, &provider, &candidate); price != nil {
				return price
			}
		}
	}
	if geminiLookup {
		provider := "google"
		for _, candidate := range catalogModelCandidates(modelID) {
			if price := findMatchingLibraryPrice(prices, &provider, &candidate); price != nil {
				return price
			}
		}
		return nil
	}
	var matched *ModelPrice
	for _, price := range allPrices {
		scope := price.PriceScope
		if scope == "" {
			scope = modelPriceScopeLibrary
		}
		if scope != modelPriceScopeLibrary {
			continue
		}
		priceModel := strings.ToLower(strings.TrimSpace(price.Model))
		modelMatches := false
		for _, candidate := range modelCandidates {
			if priceModel == strings.ToLower(strings.TrimSpace(candidate)) {
				modelMatches = true
				break
			}
		}
		if !modelMatches {
			continue
		}
		if matched != nil {
			return nil
		}
		candidate := price
		matched = &candidate
	}
	return matched
}

func catalogModelCandidates(modelID string) []string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	candidates := make([]string, 0, 3)
	seen := make(map[string]bool, 3)
	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		key := strings.ToLower(candidate)
		if candidate == "" || seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}
	appendCandidate(modelID)
	appendCandidate(normalizeModelPriceChannelModel(modelID))
	if idx := strings.Index(modelID, "/"); idx > 0 && idx < len(modelID)-1 {
		appendCandidate(modelID[idx+1:])
	}
	return candidates
}

func geminiCatalogModelCandidates(candidates []string) []string {
	result := make([]string, 0, len(candidates)*2)
	seen := make(map[string]bool, len(candidates)*2)
	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		key := strings.ToLower(candidate)
		if candidate == "" || seen[key] {
			return
		}
		seen[key] = true
		result = append(result, candidate)
	}
	for _, candidate := range candidates {
		appendCandidate(candidate)
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.Contains(candidate, "/") {
			continue
		}
		appendCandidate("gemini/" + candidate)
	}
	return result
}

func scanPrices(rows *sql.Rows) ([]ModelPrice, error) {
	var prices []ModelPrice
	for rows.Next() {
		var price ModelPrice
		var channelBrand, channelKey, sourceModel, lastSynced, updatedAt sql.NullString
		var requestUSD, priorityMultiplier sql.NullFloat64
		var longContextThreshold sql.NullInt64
		var longContextInput, longContextOutput, longContextCacheRead, longContextCacheCreation sql.NullFloat64
		if err := rows.Scan(&price.ID, &price.Provider, &price.Model, &price.PriceScope, &channelBrand, &channelKey,
			&price.InputUSDPerMillion, &price.OutputUSDPerMillion, &price.CacheReadUSDPerMillion, &price.CacheCreationUSDPerMillion, &requestUSD, &priorityMultiplier,
			&longContextThreshold, &longContextInput, &longContextOutput, &longContextCacheRead, &longContextCacheCreation,
			&price.Source, &sourceModel, &price.AutoSynced, &lastSynced, &updatedAt); err != nil {
			return nil, err
		}
		if requestUSD.Valid {
			price.RequestUSD = &requestUSD.Float64
		}
		if priorityMultiplier.Valid {
			price.PriorityMultiplier = &priorityMultiplier.Float64
		}
		price.ChannelBrand = nullableString(channelBrand)
		price.ChannelKey = nullableString(channelKey)
		longContextConfigured := longContextThreshold.Valid || longContextInput.Valid || longContextOutput.Valid || longContextCacheRead.Valid || longContextCacheCreation.Valid
		if longContextConfigured {
			price.LongContext = &ModelPriceLongContext{
				ThresholdInputTokens:       longContextThreshold.Int64,
				InputUSDPerMillion:         longContextInput.Float64,
				OutputUSDPerMillion:        longContextOutput.Float64,
				CacheReadUSDPerMillion:     longContextCacheRead.Float64,
				CacheCreationUSDPerMillion: longContextCacheCreation.Float64,
			}
			price.longContextInvalid = !longContextThreshold.Valid || !longContextInput.Valid || !longContextOutput.Valid || !longContextCacheRead.Valid || !longContextCacheCreation.Valid || !validLongContextPrice(price.LongContext)
			if price.longContextInvalid {
				price.PreservedLongContext = &ModelPriceLibraryConflictLongContext{
					ThresholdInputTokens:       nullableInt64(longContextThreshold),
					InputUSDPerMillion:         nullableFloat(longContextInput),
					OutputUSDPerMillion:        nullableFloat(longContextOutput),
					CacheReadUSDPerMillion:     nullableFloat(longContextCacheRead),
					CacheCreationUSDPerMillion: nullableFloat(longContextCacheCreation),
				}
			}
		}
		price.BillingUnit = billingUnitForModel(price.Model)
		price.SourceModel = nullableString(sourceModel)
		price.LastSyncedAt = timePtr(lastSynced)
		if parsed, ok := parseDBTime(updatedAt.String); ok {
			price.UpdatedAt = parsed
		}
		prices = append(prices, price)
	}
	return prices, rows.Err()
}

func (a *App) listModelPriceLibraryConflicts(ctx context.Context) ([]ModelPriceLibraryConflict, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT original_id, selected_price_id, conflict_reason, provider, model,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, CAST(last_synced_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_price_library_conflicts
		ORDER BY lower(provider), lower(model), original_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanModelPriceLibraryConflicts(rows)
}

func scanModelPriceLibraryConflicts(rows *sql.Rows) ([]ModelPriceLibraryConflict, error) {
	conflicts := []ModelPriceLibraryConflict{}
	for rows.Next() {
		var conflict ModelPriceLibraryConflict
		var sourceModel sql.NullString
		if err := rows.Scan(
			&conflict.OriginalID, &conflict.SelectedPriceID, &conflict.ConflictReason,
			&conflict.Price.Provider, &conflict.Price.Model,
			&conflict.Price.InputUSDPerMillion, &conflict.Price.OutputUSDPerMillion,
			&conflict.Price.CacheReadUSDPerMillion, &conflict.Price.CacheCreationUSDPerMillion,
			&conflict.requestUSD, &conflict.priorityMultiplier, &conflict.longContextThreshold,
			&conflict.longContextInput, &conflict.longContextOutput,
			&conflict.longContextCacheRead, &conflict.longContextCacheCreation,
			&conflict.Price.Source, &sourceModel, &conflict.Price.AutoSynced,
			&conflict.lastSyncedAtRaw, &conflict.updatedAtRaw,
		); err != nil {
			return nil, err
		}
		conflict.Price.ID = conflict.OriginalID
		conflict.Price.PriceScope = modelPriceScopeLibrary
		conflict.Price.RequestUSD = nullableFloat(conflict.requestUSD)
		conflict.Price.PriorityMultiplier = nullableFloat(conflict.priorityMultiplier)
		conflict.Price.SourceModel = nullableString(sourceModel)
		conflict.Price.LastSyncedAt = timePtr(conflict.lastSyncedAtRaw)
		if parsed, ok := parseDBTime(conflict.updatedAtRaw); ok {
			conflict.Price.UpdatedAt = parsed
		}
		longContextConfigured := conflict.longContextThreshold.Valid || conflict.longContextInput.Valid || conflict.longContextOutput.Valid || conflict.longContextCacheRead.Valid || conflict.longContextCacheCreation.Valid
		if longContextConfigured {
			conflict.ArchivedLongContext = &ModelPriceLibraryConflictLongContext{
				ThresholdInputTokens:       nullableInt64(conflict.longContextThreshold),
				InputUSDPerMillion:         nullableFloat(conflict.longContextInput),
				OutputUSDPerMillion:        nullableFloat(conflict.longContextOutput),
				CacheReadUSDPerMillion:     nullableFloat(conflict.longContextCacheRead),
				CacheCreationUSDPerMillion: nullableFloat(conflict.longContextCacheCreation),
			}
			conflict.Price.LongContext = &ModelPriceLongContext{
				ThresholdInputTokens:       conflict.longContextThreshold.Int64,
				InputUSDPerMillion:         conflict.longContextInput.Float64,
				OutputUSDPerMillion:        conflict.longContextOutput.Float64,
				CacheReadUSDPerMillion:     conflict.longContextCacheRead.Float64,
				CacheCreationUSDPerMillion: conflict.longContextCacheCreation.Float64,
			}
			conflict.Price.longContextInvalid = !conflict.longContextThreshold.Valid || !conflict.longContextInput.Valid || !conflict.longContextOutput.Valid || !conflict.longContextCacheRead.Valid || !conflict.longContextCacheCreation.Valid || !validLongContextPrice(conflict.Price.LongContext)
		}
		conflict.Price.BillingUnit = billingUnitForModel(conflict.Price.Model)
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func getModelPriceLibraryConflictWithQuerier(ctx context.Context, querier priceRowsQuerier, originalID int) (ModelPriceLibraryConflict, error) {
	rows, err := querier.QueryContext(ctx, `
		SELECT original_id, selected_price_id, conflict_reason, provider, model,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, CAST(last_synced_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_price_library_conflicts
		WHERE original_id = ?
	`, originalID)
	if err != nil {
		return ModelPriceLibraryConflict{}, err
	}
	defer rows.Close()
	conflicts, err := scanModelPriceLibraryConflicts(rows)
	if err != nil {
		return ModelPriceLibraryConflict{}, err
	}
	if len(conflicts) == 0 {
		return ModelPriceLibraryConflict{}, notFoundError("迁移冲突价格不存在")
	}
	return conflicts[0], nil
}

type priceRowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func modelPriceLibraryConflictReferencesPrice(ctx context.Context, querier priceRowsQuerier, priceID int) (bool, error) {
	var referenced int
	if err := querier.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM model_price_library_conflicts
			WHERE selected_price_id = ?
		)
	`, priceID).Scan(&referenced); err != nil {
		return false, err
	}
	return referenced != 0, nil
}

func getPriceWithQuerier(ctx context.Context, querier priceRowsQuerier, id int) (ModelPrice, error) {
	rows, err := querier.QueryContext(ctx, `
		SELECT id, provider, model, price_scope, channel_brand, channel_key,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, CAST(last_synced_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_prices WHERE id = ?
	`, id)
	if err != nil {
		return ModelPrice{}, err
	}
	defer rows.Close()
	prices, err := scanPrices(rows)
	if err != nil {
		return ModelPrice{}, err
	}
	if len(prices) == 0 {
		return ModelPrice{}, notFoundError("模型价格不存在")
	}
	return prices[0], nil
}

func (a *App) createPrice(ctx context.Context, payload modelPricePayload) (ModelPrice, error) {
	payload, err := validatePricePayload(payload)
	if err != nil {
		return ModelPrice{}, err
	}
	if payload.PreserveInvalidLongContext != nil && *payload.PreserveInvalidLongContext {
		return ModelPrice{}, validationError("新价格不能保留历史部分长上下文字段")
	}
	payload, err = a.validateChannelPriceCreate(ctx, payload)
	if err != nil {
		return ModelPrice{}, err
	}
	now := dbTime(time.Now())
	priorityMultiplier := defaultPriorityMultiplierForPayload(payload)
	priceCandidate := modelPriceFromPayload(payload, priorityMultiplier)
	if err := validatePriorityMultiplierForPrice(priceCandidate); err != nil {
		return ModelPrice{}, err
	}
	longContext := priceCandidate.LongContext
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'manual', NULL, 0, NULL, ?)
	`, payload.Provider, payload.Model, payload.PriceScope, nullableStringArg(payload.ChannelBrand), nullableStringArg(payload.ChannelKey),
		payload.InputUSDPerMillion, payload.OutputUSDPerMillion, payload.CacheReadUSDPerMillion, payload.CacheCreationUSDPerMillion, nullableFloatArg(payload.RequestUSD), nullableFloatArg(priorityMultiplier),
		nullableLongContextThreshold(longContext), nullableLongContextInput(longContext), nullableLongContextOutput(longContext), nullableLongContextCacheRead(longContext), nullableLongContextCacheCreation(longContext), now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ModelPrice{}, conflictError(modelPriceConflictMessage(payload))
		}
		return ModelPrice{}, err
	}
	id, _ := result.LastInsertId()
	return a.getPrice(ctx, int(id))
}

func modelPriceConflictMessage(payload modelPricePayload) string {
	if payload.PriceScope == modelPriceScopeChannel {
		return "该渠道/model 价格已存在"
	}
	return "该 provider/model 通用价格已存在"
}

func (a *App) updatePrice(ctx context.Context, id int, payload modelPricePayload) (ModelPrice, error) {
	payload, err := validatePricePayload(payload)
	if err != nil {
		return ModelPrice{}, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelPrice{}, err
	}
	defer tx.Rollback()
	existing, err := getPriceWithQuerier(ctx, tx, id)
	if err != nil {
		return ModelPrice{}, err
	}
	existingScope := existing.PriceScope
	if existingScope == "" {
		existingScope = modelPriceScopeLibrary
	}
	if existingScope != payload.PriceScope {
		return ModelPrice{}, validationError("价格范围不可修改")
	}
	if existingScope == modelPriceScopeChannel {
		if existing.ChannelBrand == nil || existing.ChannelKey == nil ||
			!strings.EqualFold(strings.TrimSpace(*existing.ChannelBrand), aiProviderOptionalString(payload.ChannelBrand)) ||
			canonicalModelPriceChannelKey(*existing.ChannelBrand, *existing.ChannelKey) != canonicalModelPriceChannelKey(aiProviderOptionalString(payload.ChannelBrand), aiProviderOptionalString(payload.ChannelKey)) ||
			!strings.EqualFold(normalizeModelPriceChannelModel(existing.Model), normalizeModelPriceChannelModel(payload.Model)) ||
			!strings.EqualFold(strings.TrimSpace(existing.Provider), strings.TrimSpace(payload.Provider)) {
			return ModelPrice{}, validationError("渠道价格的供应商、渠道和模型标识不可修改")
		}
		payload.Provider = existing.Provider
		payload.Model = existing.Model
		payload.ChannelBrand = existing.ChannelBrand
		payload.ChannelKey = existing.ChannelKey
	}
	libraryIdentityChanged := existingScope == modelPriceScopeLibrary && priceKey(existing.Provider, existing.Model) != priceKey(payload.Provider, payload.Model)
	if libraryIdentityChanged {
		referenced, err := modelPriceLibraryConflictReferencesPrice(ctx, tx, id)
		if err != nil {
			return ModelPrice{}, err
		}
		if referenced {
			return ModelPrice{}, conflictError("该通用价格仍被未解决的迁移冲突引用，请先解决冲突")
		}
	}
	priorityMultiplier := existing.PriorityMultiplier
	if libraryIdentityChanged {
		priorityMultiplier = defaultPriorityMultiplierForPayload(payload)
	}
	priceCandidate := modelPriceFromPayload(payload, priorityMultiplier)
	if err := validatePriorityMultiplierForPrice(priceCandidate); err != nil {
		return ModelPrice{}, err
	}
	longContext := priceCandidate.LongContext
	preserveInvalidLongContext := existing.longContextInvalid && existing.PreservedLongContext != nil && longContext == nil &&
		payload.PreserveInvalidLongContext != nil && *payload.PreserveInvalidLongContext
	if payload.PreserveInvalidLongContext != nil && *payload.PreserveInvalidLongContext && !preserveInvalidLongContext {
		return ModelPrice{}, validationError("当前价格没有可保留的历史部分长上下文字段")
	}
	longContextThreshold := nullableLongContextThreshold(longContext)
	longContextInput := nullableLongContextInput(longContext)
	longContextOutput := nullableLongContextOutput(longContext)
	longContextCacheRead := nullableLongContextCacheRead(longContext)
	longContextCacheCreation := nullableLongContextCacheCreation(longContext)
	if preserveInvalidLongContext {
		longContextThreshold = nullableInt64PtrArg(existing.PreservedLongContext.ThresholdInputTokens)
		longContextInput = nullableFloatArg(existing.PreservedLongContext.InputUSDPerMillion)
		longContextOutput = nullableFloatArg(existing.PreservedLongContext.OutputUSDPerMillion)
		longContextCacheRead = nullableFloatArg(existing.PreservedLongContext.CacheReadUSDPerMillion)
		longContextCacheCreation = nullableFloatArg(existing.PreservedLongContext.CacheCreationUSDPerMillion)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE model_prices
		SET provider = ?, model = ?, price_scope = ?, channel_brand = ?, channel_key = ?,
		    input_usd_per_million = ?, output_usd_per_million = ?,
		    cache_read_usd_per_million = ?, cache_creation_usd_per_million = ?,
		    request_usd = ?, priority_multiplier = ?, long_context_threshold_tokens = ?,
		    long_context_input_usd_per_million = ?, long_context_output_usd_per_million = ?,
		    long_context_cache_read_usd_per_million = ?, long_context_cache_creation_usd_per_million = ?, source = 'manual',
		    source_model = NULL, auto_synced = 0, last_synced_at = NULL, updated_at = ?
		WHERE id = ?
	`, payload.Provider, payload.Model, payload.PriceScope, nullableStringArg(payload.ChannelBrand), nullableStringArg(payload.ChannelKey),
		payload.InputUSDPerMillion, payload.OutputUSDPerMillion, payload.CacheReadUSDPerMillion, payload.CacheCreationUSDPerMillion, nullableFloatArg(payload.RequestUSD), nullableFloatArg(priorityMultiplier),
		longContextThreshold, longContextInput, longContextOutput, longContextCacheRead, longContextCacheCreation, dbTime(time.Now()), id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ModelPrice{}, conflictError(modelPriceConflictMessage(payload))
		}
		return ModelPrice{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ModelPrice{}, notFoundError("模型价格不存在")
	}
	updated, err := getPriceWithQuerier(ctx, tx, id)
	if err != nil {
		return ModelPrice{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelPrice{}, err
	}
	return updated, nil
}

func (a *App) updatePriorityMultiplier(ctx context.Context, id int, payload priorityMultiplierPayload) (ModelPrice, error) {
	if payload.PriorityMultiplier == nil || !finitePositive(*payload.PriorityMultiplier) {
		return ModelPrice{}, validationError("Fast 倍率必须是大于 0 的有限数值")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelPrice{}, err
	}
	defer tx.Rollback()
	price, err := getPriceWithQuerier(ctx, tx, id)
	if err != nil {
		return ModelPrice{}, err
	}
	if !modelPriceSupportsPriority(price) {
		return ModelPrice{}, validationError("Fast 倍率仅支持 OpenAI 兼容或 Codex 渠道")
	}
	price.PriorityMultiplier = payload.PriorityMultiplier
	if err := validatePriorityMultiplierForPrice(price); err != nil {
		return ModelPrice{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE model_prices
		SET priority_multiplier = ?, updated_at = ?
		WHERE id = ?
	`, *payload.PriorityMultiplier, dbTime(time.Now()), id)
	if err != nil {
		return ModelPrice{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ModelPrice{}, notFoundError("模型价格不存在")
	}
	updated, err := getPriceWithQuerier(ctx, tx, id)
	if err != nil {
		return ModelPrice{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelPrice{}, err
	}
	return updated, nil
}

func (a *App) deletePrice(ctx context.Context, id int) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := getPriceWithQuerier(ctx, tx, id); err != nil {
		return err
	}
	referenced, err := modelPriceLibraryConflictReferencesPrice(ctx, tx, id)
	if err != nil {
		return err
	}
	if referenced {
		return conflictError("该通用价格仍被未解决的迁移冲突引用，请先解决冲突")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM model_prices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return notFoundError("模型价格不存在")
	}
	return tx.Commit()
}

func (a *App) promoteModelPriceLibraryConflict(ctx context.Context, originalID int, payload modelPriceLibraryConflictPromotePayload) (ModelPrice, error) {
	payload.Provider = strings.TrimSpace(payload.Provider)
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Provider == "" || payload.Model == "" {
		return ModelPrice{}, validationError("provider/model 不能为空")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelPrice{}, err
	}
	defer tx.Rollback()
	conflict, err := getModelPriceLibraryConflictWithQuerier(ctx, tx, originalID)
	if err != nil {
		return ModelPrice{}, err
	}
	priceID, err := insertModelPriceLibraryConflictAsActive(ctx, tx, conflict, payload.Provider, payload.Model)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ModelPrice{}, conflictError("该 provider/model 通用价格已存在，请修改后再提升")
		}
		return ModelPrice{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_price_library_conflicts WHERE original_id = ?`, originalID); err != nil {
		return ModelPrice{}, err
	}
	price, err := getPriceWithQuerier(ctx, tx, priceID)
	if err != nil {
		return ModelPrice{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelPrice{}, err
	}
	return price, nil
}

func (a *App) replaceActiveModelPriceLibraryConflict(ctx context.Context, originalID int) (ModelPrice, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelPrice{}, err
	}
	defer tx.Rollback()
	conflict, err := getModelPriceLibraryConflictWithQuerier(ctx, tx, originalID)
	if err != nil {
		return ModelPrice{}, err
	}
	active, err := getPriceWithQuerier(ctx, tx, conflict.SelectedPriceID)
	if err != nil {
		return ModelPrice{}, conflictError("活动价格已变化，请刷新后重试")
	}
	if active.PriceScope != modelPriceScopeLibrary || priceKey(active.Provider, active.Model) != priceKey(conflict.Price.Provider, conflict.Price.Model) {
		return ModelPrice{}, conflictError("活动价格与迁移冲突身份不一致，请刷新后重试")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_price_library_conflicts WHERE original_id = ?`, originalID); err != nil {
		return ModelPrice{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO model_price_library_conflicts (
			original_id, selected_price_id, conflict_reason, provider, model,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		)
		SELECT ?, ?, ?, provider, model,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
		       priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, last_synced_at, updated_at
		FROM model_prices WHERE id = ?
	`, conflict.OriginalID, active.ID, conflict.ConflictReason, active.ID); err != nil {
		return ModelPrice{}, err
	}
	if err := updateActiveModelPriceFromLibraryConflict(ctx, tx, active.ID, conflict); err != nil {
		return ModelPrice{}, err
	}
	price, err := getPriceWithQuerier(ctx, tx, active.ID)
	if err != nil {
		return ModelPrice{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelPrice{}, err
	}
	return price, nil
}

func insertModelPriceLibraryConflictAsActive(ctx context.Context, tx *sql.Tx, conflict ModelPriceLibraryConflict, provider, model string) (int, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO model_prices (
			provider, model, price_scope, channel_brand, channel_key,
			input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (?, ?, 'library', NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'manual', NULL, 0, NULL, ?)
	`, provider, model,
		conflict.Price.InputUSDPerMillion, conflict.Price.OutputUSDPerMillion,
		conflict.Price.CacheReadUSDPerMillion, conflict.Price.CacheCreationUSDPerMillion,
		nullableSQLFloatArg(conflict.requestUSD), nullableSQLFloatArg(conflict.priorityMultiplier), nullableInt64Arg(conflict.longContextThreshold),
		nullableSQLFloatArg(conflict.longContextInput), nullableSQLFloatArg(conflict.longContextOutput),
		nullableSQLFloatArg(conflict.longContextCacheRead), nullableSQLFloatArg(conflict.longContextCacheCreation),
		dbTime(time.Now()))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(id), nil
}

func updateActiveModelPriceFromLibraryConflict(ctx context.Context, tx *sql.Tx, activeID int, conflict ModelPriceLibraryConflict) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE model_prices
		SET provider = ?, model = ?, price_scope = 'library', channel_brand = NULL, channel_key = NULL,
		    input_usd_per_million = ?, output_usd_per_million = ?,
		    cache_read_usd_per_million = ?, cache_creation_usd_per_million = ?, request_usd = ?,
		    priority_multiplier = ?, long_context_threshold_tokens = ?,
		    long_context_input_usd_per_million = ?, long_context_output_usd_per_million = ?,
		    long_context_cache_read_usd_per_million = ?, long_context_cache_creation_usd_per_million = ?,
		    source = 'manual', source_model = NULL, auto_synced = 0, last_synced_at = NULL, updated_at = ?
		WHERE id = ?
	`, conflict.Price.Provider, conflict.Price.Model,
		conflict.Price.InputUSDPerMillion, conflict.Price.OutputUSDPerMillion,
		conflict.Price.CacheReadUSDPerMillion, conflict.Price.CacheCreationUSDPerMillion,
		nullableSQLFloatArg(conflict.requestUSD), nullableSQLFloatArg(conflict.priorityMultiplier), nullableInt64Arg(conflict.longContextThreshold),
		nullableSQLFloatArg(conflict.longContextInput), nullableSQLFloatArg(conflict.longContextOutput),
		nullableSQLFloatArg(conflict.longContextCacheRead), nullableSQLFloatArg(conflict.longContextCacheCreation),
		dbTime(time.Now()), activeID)
	return err
}

func (a *App) deleteModelPriceLibraryConflict(ctx context.Context, originalID int) error {
	result, err := a.db.ExecContext(ctx, `DELETE FROM model_price_library_conflicts WHERE original_id = ?`, originalID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return notFoundError("迁移冲突价格不存在")
	}
	return nil
}

func (a *App) getPrice(ctx context.Context, id int) (ModelPrice, error) {
	return getPriceWithQuerier(ctx, a.db, id)
}

func (a *App) handleSyncLiteLLMPrices(w http.ResponseWriter, r *http.Request) error {
	body := readAllAndRestore(r)
	var payload modelPriceSyncRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
	}
	sourceURL := defaultLiteLLMPricingURL
	if payload.SourceURL != nil && strings.TrimSpace(*payload.SourceURL) != "" {
		sourceURL = strings.TrimSpace(*payload.SourceURL)
	}
	if err := ensureHTTPSURL(sourceURL); err != nil {
		return err
	}
	cfg, err := a.loadConfig(r.Context())
	if err != nil {
		return err
	}
	client, err := liteLLMHTTPClient(30*time.Second, cfg.LiteLLMProxy)
	if err != nil {
		return err
	}
	response, rawPayload, err := doJSON(r.Context(), client, http.MethodGet, sourceURL, nil, nil)
	if err != nil {
		return validationError("下载 LiteLLM 价格数据失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return validationError(fmt.Sprintf("下载 LiteLLM 价格数据失败：HTTP %d", response.StatusCode))
	}
	var rawData map[string]any
	if err := json.Unmarshal(rawPayload, &rawData); err != nil {
		return validationError("LiteLLM 价格数据不是有效 JSON")
	}
	result, err := a.syncLiteLLMPrices(r.Context(), sourceURL, rawData)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (a *App) syncLiteLLMPrices(ctx context.Context, sourceURL string, rawData map[string]any) (map[string]any, error) {
	now := dbTime(time.Now())
	type litellmPriceRow struct {
		modelName string
		payload   modelPricePayload
	}
	rows := make([]litellmPriceRow, 0, len(rawData))
	skippedInvalid := 0
	for modelName, rawEntry := range rawData {
		if modelName == "sample_spec" {
			skippedInvalid++
			continue
		}
		payload, ok := litellmEntryToPrice(modelName, rawEntry)
		if !ok {
			skippedInvalid++
			continue
		}
		validatedPayload, err := validatePricePayload(payload)
		if err != nil {
			skippedInvalid++
			continue
		}
		rows = append(rows, litellmPriceRow{modelName: modelName, payload: validatedPayload})
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	overrides, err := liteLLMPriceOverrideSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM model_prices
		WHERE source = 'litellm'
		  AND price_scope = 'library'
		  AND NOT EXISTS (
			SELECT 1
			FROM model_price_library_conflicts
			WHERE selected_price_id = model_prices.id
		  )
	`); err != nil {
		return nil, err
	}
	inserted, skippedManual := 0, 0
	for _, row := range rows {
		payload := row.payload
		override, hasOverride := overrides[priceKey(payload.Provider, payload.Model)]
		longContext, _ := longContextFromPayload(payload.LongContext)
		longContextThreshold := nullableLongContextThreshold(longContext)
		longContextInput := nullableLongContextInput(longContext)
		longContextOutput := nullableLongContextOutput(longContext)
		longContextCacheRead := nullableLongContextCacheRead(longContext)
		longContextCacheCreation := nullableLongContextCacheCreation(longContext)
		finalLongContext := longContext
		finalLongContextValid := true
		if hasOverride {
			longContextThreshold = nullableInt64Arg(override.longContextThreshold)
			longContextInput = nullableSQLFloatArg(override.longContextInput)
			longContextOutput = nullableSQLFloatArg(override.longContextOutput)
			longContextCacheRead = nullableSQLFloatArg(override.longContextCacheRead)
			longContextCacheCreation = nullableSQLFloatArg(override.longContextCacheCreation)
			finalLongContext, finalLongContextValid = longContextFromLiteLLMOverride(override)
		}
		var priorityMultiplier *float64
		priorityPreserved := hasOverride && override.priorityMultiplier.Valid
		if priorityPreserved {
			value := override.priorityMultiplier.Float64
			priorityMultiplier = &value
		} else {
			priorityMultiplier = defaultPriorityMultiplier(payload.Provider, payload.Model)
			candidate := modelPriceFromPayload(payload, priorityMultiplier)
			candidate.LongContext = finalLongContext
			if !finalLongContextValid || validatePriorityMultiplierForPrice(candidate) != nil {
				priorityMultiplier = nil
			}
		}
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO model_prices (
				provider, model, price_scope, channel_brand, channel_key,
				input_usd_per_million, output_usd_per_million,
				cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
				priority_multiplier, long_context_threshold_tokens,
				long_context_input_usd_per_million, long_context_output_usd_per_million,
				long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
				source, source_model, auto_synced, last_synced_at, updated_at
			) VALUES (?, ?, 'library', NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'litellm', ?, 1, ?, ?)
		`, payload.Provider, payload.Model, payload.InputUSDPerMillion, payload.OutputUSDPerMillion, payload.CacheReadUSDPerMillion, payload.CacheCreationUSDPerMillion, nullableFloatArg(payload.RequestUSD), nullableFloatArg(priorityMultiplier),
			longContextThreshold, longContextInput, longContextOutput, longContextCacheRead, longContextCacheCreation, row.modelName, now, now)
		if err != nil {
			return nil, err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			skippedManual++
			continue
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return map[string]any{
		"source_url":      sourceURL,
		"total_entries":   len(rawData),
		"imported":        inserted,
		"created":         inserted,
		"updated":         0,
		"unchanged":       0,
		"skipped_manual":  skippedManual,
		"skipped_invalid": skippedInvalid,
	}, nil
}

type liteLLMPriceOverride struct {
	priorityMultiplier       sql.NullFloat64
	longContextThreshold     sql.NullInt64
	longContextInput         sql.NullFloat64
	longContextOutput        sql.NullFloat64
	longContextCacheRead     sql.NullFloat64
	longContextCacheCreation sql.NullFloat64
}

func longContextFromLiteLLMOverride(override liteLLMPriceOverride) (*ModelPriceLongContext, bool) {
	configured := override.longContextThreshold.Valid || override.longContextInput.Valid || override.longContextOutput.Valid ||
		override.longContextCacheRead.Valid || override.longContextCacheCreation.Valid
	if !configured {
		return nil, true
	}
	if !override.longContextThreshold.Valid || !override.longContextInput.Valid || !override.longContextOutput.Valid ||
		!override.longContextCacheRead.Valid || !override.longContextCacheCreation.Valid {
		return nil, false
	}
	price := &ModelPriceLongContext{
		ThresholdInputTokens:       override.longContextThreshold.Int64,
		InputUSDPerMillion:         override.longContextInput.Float64,
		OutputUSDPerMillion:        override.longContextOutput.Float64,
		CacheReadUSDPerMillion:     override.longContextCacheRead.Float64,
		CacheCreationUSDPerMillion: override.longContextCacheCreation.Float64,
	}
	return price, validLongContextPrice(price)
}

func liteLLMPriceOverrideSnapshot(ctx context.Context, tx *sql.Tx) (map[[2]string]liteLLMPriceOverride, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT provider, model, priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million
		FROM model_prices
		WHERE source = 'litellm' AND price_scope = 'library'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[[2]string]liteLLMPriceOverride{}
	for rows.Next() {
		var provider, model string
		var override liteLLMPriceOverride
		if err := rows.Scan(&provider, &model, &override.priorityMultiplier, &override.longContextThreshold,
			&override.longContextInput, &override.longContextOutput, &override.longContextCacheRead, &override.longContextCacheCreation); err != nil {
			return nil, err
		}
		result[priceKey(provider, model)] = override
	}
	return result, rows.Err()
}

func litellmEntryToPrice(modelName string, rawEntry any) (modelPricePayload, bool) {
	entry, ok := rawEntry.(map[string]any)
	if !ok {
		return modelPricePayload{}, false
	}
	provider := strings.ToLower(strings.TrimSpace(fmt.Sprint(entry["litellm_provider"])))
	model := strings.TrimSpace(modelName)
	if provider == "" || model == "" || len(provider) > 120 || len(model) > 180 {
		return modelPricePayload{}, false
	}
	payload := modelPricePayload{
		Provider:                   provider,
		Model:                      model,
		InputUSDPerMillion:         usdPerMillion(entry["input_cost_per_token"]),
		OutputUSDPerMillion:        usdPerMillion(entry["output_cost_per_token"]),
		CacheReadUSDPerMillion:     usdPerMillion(entry["cache_read_input_token_cost"]),
		CacheCreationUSDPerMillion: usdPerMillion(entry["cache_creation_input_token_cost"]),
	}
	if longContext := defaultLongContextPrice(provider, model); longContext != nil {
		payload.LongContext = longContextPayloadFromPrice(longContext)
	}
	if payload.InputUSDPerMillion == 0 && payload.OutputUSDPerMillion == 0 && payload.CacheReadUSDPerMillion == 0 && payload.CacheCreationUSDPerMillion == 0 {
		return modelPricePayload{}, false
	}
	return payload, true
}

func usdPerMillion(value any) float64 {
	number, ok := numeric(value)
	if !ok || number < 0 {
		return 0
	}
	return mathRound(number*1_000_000, 12)
}

func numeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func mathRound(value float64, places int) float64 {
	factor := 1.0
	for i := 0; i < places; i++ {
		factor *= 10
	}
	scaled := value * factor
	if math.IsInf(scaled, 0) && !math.IsInf(value, 0) {
		// Decimal rounding cannot change a finite float at this magnitude.
		return value
	}
	return math.Round(scaled) / factor
}

func pricesEqual(item ModelPrice, payload modelPricePayload) bool {
	payloadLongContext, _ := longContextFromPayload(payload.LongContext)
	itemScope := item.PriceScope
	if itemScope == "" {
		itemScope = modelPriceScopeLibrary
	}
	return itemScope == payload.PriceScope &&
		item.Provider == payload.Provider &&
		item.Model == payload.Model &&
		item.InputUSDPerMillion == payload.InputUSDPerMillion &&
		item.OutputUSDPerMillion == payload.OutputUSDPerMillion &&
		item.CacheReadUSDPerMillion == payload.CacheReadUSDPerMillion &&
		item.CacheCreationUSDPerMillion == payload.CacheCreationUSDPerMillion &&
		floatPtrEqual(item.RequestUSD, payload.RequestUSD) &&
		longContextPriceEqual(item.LongContext, payloadLongContext)
}

func priceKey(provider, model string) [2]string {
	return [2]string{strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(model))}
}

func findMatchingLibraryPrice(prices libraryPriceIndex, provider, model *string) *ModelPrice {
	if provider == nil || model == nil {
		return nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(*provider))
	modelKey := strings.ToLower(strings.TrimSpace(*model))
	if providerKey == "" || modelKey == "" {
		return nil
	}
	candidates := []string{providerKey}
	if providerKey == "codex" {
		candidates = append(candidates, "openai")
	}
	if providerKey == "claude" {
		candidates = append(candidates, "anthropic")
	}
	for _, candidate := range candidates {
		if price, ok := prices[[2]string{candidate, modelKey}]; ok {
			return &price
		}
	}
	return nil
}

func findMatchingChannelPrice(prices modelPriceIndex, record UsageRecord, matchContexts ...modelPriceMatchContext) (*ModelPrice, string) {
	if record.Provider == nil || strings.TrimSpace(*record.Provider) == "" {
		return nil, priceMatchStatusMissingProvider
	}
	if record.Model == nil || normalizeModelPriceChannelModel(*record.Model) == "" {
		return nil, priceMatchStatusMissingModel
	}
	provider := strings.TrimSpace(*record.Provider)
	model := normalizeModelPriceChannelModel(*record.Model)
	if len(matchContexts) > 0 {
		if matchContexts[0].SelectorsAvailable {
			return findMatchingConfiguredChannelPrice(prices, matchContexts[0].Selectors, provider, model, record.AuthIndex)
		}
		if modelPriceRecordRequiresConfiguredSelectors(prices, provider, model, record.AuthIndex) {
			return nil, priceMatchStatusChannelConfigUnavailable
		}
	}
	return findMatchingStoredChannelPrice(prices, provider, model, record.AuthIndex)
}

func modelPriceRecordRequiresConfiguredSelectors(prices modelPriceIndex, provider, model string, authIndexValue *string) bool {
	_, status := findMatchingStoredChannelPrice(prices, provider, model, authIndexValue)
	return status == priceMatchStatusMatched || status == priceMatchStatusChannelConflict
}

func findMatchingConfiguredChannelPrice(prices modelPriceIndex, selectors modelPriceChannelSelectorIndex, provider, model string, authIndexValue *string) (*ModelPrice, string) {
	nativeBrands := matchingNativePriceBrands(provider)
	authIndex := strings.TrimSpace(aiProviderOptionalString(authIndexValue))
	if authIndex == "" {
		openAICompatibleCount, nativeCount := configuredMissingAuthModelPriceCandidateCounts(selectors, provider, model)
		if nativeCount > 0 {
			if openAICompatibleCount > 0 {
				return nil, priceMatchStatusChannelConflict
			}
			return nil, priceMatchStatusMissingAuthIndex
		}
	}
	matchedIdentity, matchCount := configuredModelPriceCandidates(selectors, provider, model, authIndexValue)
	if matchCount > 1 {
		return nil, priceMatchStatusChannelConflict
	}
	if matchCount == 0 {
		if len(nativeBrands) > 0 && authIndex == "" {
			return nil, priceMatchStatusMissingAuthIndex
		}
		return nil, priceMatchStatusChannelUnpriced
	}

	key := priceKey(matchedIdentity.ChannelKey, matchedIdentity.Model)
	if matchedIdentity.Brand != aiProviderBrandOpenAICompatibility {
		key = nativeModelPriceKey(string(matchedIdentity.Brand), matchedIdentity.ChannelKey, matchedIdentity.Model)
	}
	price, ok := prices[key]
	if !ok {
		return nil, priceMatchStatusChannelUnpriced
	}
	return &price, priceMatchStatusMatched
}

func configuredMissingAuthModelPriceCandidateCounts(selectors modelPriceChannelSelectorIndex, provider, model string) (int, int) {
	openAICompatibleCount := selectors[modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model)]
	nativeCount := configuredNativeModelPriceCandidateCount(selectors, matchingNativePriceBrands(provider), model)
	return openAICompatibleCount, nativeCount
}

func configuredNativeModelPriceCandidateCount(selectors modelPriceChannelSelectorIndex, brands []aiProviderBrand, model string) int {
	if len(brands) == 0 {
		return 0
	}
	modelKey := strings.ToLower(normalizeModelPriceChannelModel(model))
	count := 0
	for identity, candidateCount := range selectors {
		if identity.Model != modelKey {
			continue
		}
		for _, brand := range brands {
			if identity.Brand == brand {
				count += candidateCount
				break
			}
		}
	}
	return count
}

func configuredModelPriceCandidates(selectors modelPriceChannelSelectorIndex, provider, model string, authIndexValue *string) (modelPriceChannelIdentity, int) {
	matchedIdentity := modelPriceChannelIdentity{}
	matchCount := 0
	appendIdentity := func(identity modelPriceChannelIdentity) {
		count := selectors[identity]
		if count == 0 {
			return
		}
		if matchCount == 0 {
			matchedIdentity = identity
		}
		matchCount += count
	}
	appendIdentity(modelPriceChannelIdentityKey(aiProviderBrandOpenAICompatibility, provider, model))
	authIndex := strings.TrimSpace(aiProviderOptionalString(authIndexValue))
	if authIndex != "" {
		for _, brand := range matchingNativePriceBrands(provider) {
			appendIdentity(modelPriceChannelIdentityKey(brand, authIndex, model))
		}
	}
	return matchedIdentity, matchCount
}

func findMatchingStoredChannelPrice(prices modelPriceIndex, provider, model string, authIndexValue *string) (*ModelPrice, string) {
	type candidate struct {
		key   [2]string
		price ModelPrice
	}
	candidates := make([]candidate, 0, 3)
	seenKeys := map[[2]string]bool{}
	appendCandidate := func(key [2]string, legacyOnly bool) {
		if seenKeys[key] {
			return
		}
		price, ok := prices[key]
		if !ok {
			return
		}
		if legacyOnly && price.PriceScope == modelPriceScopeChannel {
			return
		}
		for _, existing := range candidates {
			if price.ID > 0 && existing.price.ID == price.ID {
				seenKeys[key] = true
				return
			}
		}
		seenKeys[key] = true
		candidates = append(candidates, candidate{key: key, price: price})
	}

	appendCandidate(priceKey(provider, model), false)
	for _, fallback := range legacyPriceProviderCandidates(provider) {
		appendCandidate(priceKey(fallback, model), true)
	}

	nativeBrands := matchingNativePriceBrands(provider)
	authIndex := strings.TrimSpace(aiProviderOptionalString(authIndexValue))
	if authIndex != "" {
		for _, brand := range nativeBrands {
			appendCandidate(nativeModelPriceKey(string(brand), authIndex, model), false)
		}
	}

	if len(candidates) > 1 {
		return nil, priceMatchStatusChannelConflict
	}
	if len(candidates) == 1 {
		if len(nativeBrands) > 0 && authIndex == "" && modelPriceIsNativeChannel(candidates[0].price) {
			return nil, priceMatchStatusMissingAuthIndex
		}
		price := candidates[0].price
		return &price, priceMatchStatusMatched
	}
	if len(nativeBrands) > 0 && authIndex == "" {
		return nil, priceMatchStatusMissingAuthIndex
	}
	return nil, priceMatchStatusChannelUnpriced
}

func modelPriceIsOpenAICompatibleChannel(price ModelPrice) bool {
	return price.PriceScope == modelPriceScopeChannel && price.ChannelBrand != nil &&
		aiProviderBrand(strings.TrimSpace(*price.ChannelBrand)) == aiProviderBrandOpenAICompatibility
}

func modelPriceIsNativeChannel(price ModelPrice) bool {
	return price.PriceScope == modelPriceScopeChannel && price.ChannelBrand != nil &&
		aiProviderBrand(strings.TrimSpace(*price.ChannelBrand)) != aiProviderBrandOpenAICompatibility
}

func legacyPriceProviderCandidates(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return []string{"openai"}
	case "claude":
		return []string{"anthropic"}
	default:
		return nil
	}
}

func matchingNativePriceBrands(provider string) []aiProviderBrand {
	result := make([]aiProviderBrand, 0, 2)
	for _, config := range aiProviderBrandConfigs {
		if config.Brand == aiProviderBrandOpenAICompatibility {
			continue
		}
		if aiProviderUsageLabelsEqual(provider, string(config.Brand)) ||
			aiProviderUsageLabelsEqual(provider, config.ConfigKey) ||
			aiProviderUsageLabelsEqual(provider, config.Label) ||
			matchesNativePriceProviderAlias(config.Brand, provider) {
			result = append(result, config.Brand)
		}
	}
	return result
}

func matchesNativePriceProviderAlias(brand aiProviderBrand, provider string) bool {
	switch brand {
	case aiProviderBrandGemini, aiProviderBrandVertex:
		return aiProviderUsageLabelsEqual(provider, "google")
	case aiProviderBrandClaude:
		return aiProviderUsageLabelsEqual(provider, "anthropic")
	default:
		return false
	}
}

func defaultPriorityMultiplier(provider, model string) *float64 {
	if !isFastPricingProvider(provider) {
		return nil
	}
	value, ok := defaultPriorityMultipliers[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return nil
	}
	return &value
}

func defaultPriorityMultiplierForPayload(payload modelPricePayload) *float64 {
	price := modelPriceFromPayload(payload, nil)
	if price.PriceScope == modelPriceScopeChannel {
		return nil
	}
	if !modelPriceSupportsPriority(price) {
		return nil
	}
	return defaultPriorityMultiplier(price.Provider, price.Model)
}

func modelPriceSupportsPriority(price ModelPrice) bool {
	scope := price.PriceScope
	if scope == "" {
		scope = modelPriceScopeLibrary
	}
	if scope == modelPriceScopeChannel {
		if price.ChannelBrand == nil {
			return false
		}
		switch aiProviderBrand(strings.TrimSpace(*price.ChannelBrand)) {
		case aiProviderBrandCodex, aiProviderBrandOpenAICompatibility:
			return true
		default:
			return false
		}
	}
	return isFastPricingProvider(price.Provider)
}

func isFastPricingProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "codex":
		return true
	default:
		return false
	}
}

func priorityMultiplierForRecord(record UsageRecord, price *ModelPrice) (*float64, bool) {
	if price == nil || record.Provider == nil || record.ServiceTier == nil {
		return nil, false
	}
	if !modelPriceSupportsPriority(*price) || !strings.EqualFold(strings.TrimSpace(*record.ServiceTier), serviceTierPriority) {
		return nil, false
	}
	if price.PriorityMultiplier == nil {
		return nil, false
	}
	if !priorityMultiplierProducesRoundableCost(*price, *price.PriorityMultiplier) {
		return nil, true
	}
	value := *price.PriorityMultiplier
	return &value, false
}

func applyTierMultiplier(value float64, multiplier *float64) float64 {
	if multiplier == nil {
		return value
	}
	return value * *multiplier
}

func priorityMultiplierProducesRoundableCost(price ModelPrice, multiplier float64) bool {
	if !finitePositive(multiplier) {
		return false
	}
	if billingUnitForModel(price.Model) == modelBillingUnitRequest {
		if price.RequestUSD == nil {
			return true
		}
		_, ok := roundCostUSD(applyTierMultiplier(*price.RequestUSD, &multiplier))
		return ok
	}
	prices := []float64{
		price.InputUSDPerMillion,
		price.CacheReadUSDPerMillion,
		price.CacheCreationUSDPerMillion,
		price.OutputUSDPerMillion,
	}
	if price.LongContext != nil {
		prices = append(prices,
			price.LongContext.InputUSDPerMillion,
			price.LongContext.CacheReadUSDPerMillion,
			price.LongContext.CacheCreationUSDPerMillion,
			price.LongContext.OutputUSDPerMillion,
		)
	}
	for _, usdPerMillion := range prices {
		if usdPerMillion == 0 {
			continue
		}
		_, ok := roundCostUSD(applyTierMultiplier(usdPerMillion, &multiplier))
		if !ok {
			return false
		}
	}
	return true
}

func selectLongContextPrice(price ModelPrice, contextInputTokens int) (ModelPrice, bool, bool) {
	selected := price
	selected.LongContext = nil
	if price.LongContext == nil {
		return selected, false, false
	}
	threshold := price.LongContext.ThresholdInputTokens
	if threshold > 0 && int64(contextInputTokens) <= threshold {
		return selected, false, false
	}
	if price.longContextInvalid || !validLongContextPrice(price.LongContext) {
		return selected, false, contextInputTokens > 0
	}
	selected.InputUSDPerMillion = price.LongContext.InputUSDPerMillion
	selected.OutputUSDPerMillion = price.LongContext.OutputUSDPerMillion
	selected.CacheReadUSDPerMillion = price.LongContext.CacheReadUSDPerMillion
	selected.CacheCreationUSDPerMillion = price.LongContext.CacheCreationUSDPerMillion
	return selected, true, false
}

func calculateRecordCostBreakdown(record UsageRecord, prices modelPriceIndex, matchContexts ...modelPriceMatchContext) usageCostBreakdown {
	return calculateRecordCost(record, prices, true, matchContexts...)
}

func calculateRecordCost(record UsageRecord, prices modelPriceIndex, collectItems bool, matchContexts ...modelPriceMatchContext) usageCostBreakdown {
	price, matchStatus := findMatchingChannelPrice(prices, record, matchContexts...)
	channelBrand := matchedModelPriceChannelBrand(price, record, matchContexts...)
	tokens := normalizedUsageTokenBreakdown(record, channelBrand)
	contextInputTokens := usageAggregateInputTokens(record, channelBrand)
	breakdown := usageCostBreakdown{
		BillingUnit:         billingUnitForModelPtr(record.Model),
		NormalInputTokens:   tokens.NormalInputTokens,
		CacheReadTokens:     tokens.CacheReadTokens,
		CacheCreationTokens: tokens.CacheCreationTokens,
		OutputTokens:        tokens.OutputTokens,
		ContextInputTokens:  contextInputTokens,
	}
	if collectItems {
		itemCapacity := 4
		if breakdown.BillingUnit == modelBillingUnitRequest {
			itemCapacity = 1
		}
		breakdown.Items = make([]usageCostBreakdownItem, 0, itemCapacity)
	}
	if price != nil && price.LongContext != nil && price.LongContext.ThresholdInputTokens > 0 {
		threshold := price.LongContext.ThresholdInputTokens
		breakdown.LongContextThresholdTokens = &threshold
	}
	if breakdown.BillingUnit == modelBillingUnitRequest {
		if record.Failed {
			return breakdown
		}
		if price == nil || price.RequestUSD == nil {
			reason := matchStatus
			if price != nil {
				reason = priceMatchStatusInvalidPrice
			}
			markCostBreakdownUnpriced(&breakdown, reason)
			return breakdown
		}
		multiplier, invalidMultiplier := priorityMultiplierForRecord(record, price)
		if invalidMultiplier {
			markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
			return breakdown
		}
		if multiplier != nil {
			breakdown.TierMultiplier = multiplier
		}
		requestUSD := applyTierMultiplier(*price.RequestUSD, multiplier)
		totalUSD, ok := roundCostUSD(requestUSD)
		if !ok {
			markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
			return breakdown
		}
		breakdown.TotalUSD = totalUSD
		if collectItems {
			breakdown.Items = append(breakdown.Items, usageRequestCostBreakdownItem{
				Kind:          usageCostKindRequest,
				Requests:      1,
				USDPerRequest: requestUSD,
				SubtotalUSD:   breakdown.TotalUSD,
			})
		}
		return breakdown
	}
	if price == nil {
		breakdown.Unpriced = usageAggregateTotalTokens(record, channelBrand) > 0
		if breakdown.Unpriced {
			reason := matchStatus
			breakdown.UnpricedReason = &reason
		}
		return breakdown
	}
	selectedPrice, longContextApplied, invalidLongContext := selectLongContextPrice(*price, contextInputTokens)
	breakdown.LongContextApplied = longContextApplied
	if invalidLongContext {
		markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
		return breakdown
	}
	price = &selectedPrice
	multiplier, invalidMultiplier := priorityMultiplierForRecord(record, price)
	if invalidMultiplier {
		markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
		return breakdown
	}
	if multiplier != nil {
		breakdown.TierMultiplier = multiplier
	}
	cacheCreationHasSeparatePrice := modelPriceIsClaudeChannel(*price) || price.CacheCreationUSDPerMillion > 0
	billableInputTokens := tokens.NormalInputTokens
	if !cacheCreationHasSeparatePrice {
		billableInputTokens += tokens.CacheCreationTokens
	}
	if !appendUsageTokenCostItem(&breakdown, collectItems, usageCostKindInput, billableInputTokens, applyTierMultiplier(price.InputUSDPerMillion, multiplier)) ||
		!appendUsageTokenCostItem(&breakdown, collectItems, usageCostKindCacheRead, tokens.CacheReadTokens, applyTierMultiplier(price.CacheReadUSDPerMillion, multiplier)) {
		markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
		return breakdown
	}
	if cacheCreationHasSeparatePrice {
		if !appendUsageTokenCostItem(&breakdown, collectItems, usageCostKindCacheCreation, tokens.CacheCreationTokens, applyTierMultiplier(price.CacheCreationUSDPerMillion, multiplier)) {
			markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
			return breakdown
		}
	}
	if !appendUsageTokenCostItem(&breakdown, collectItems, usageCostKindOutput, tokens.OutputTokens, applyTierMultiplier(price.OutputUSDPerMillion, multiplier)) {
		markCostBreakdownUnpriced(&breakdown, priceMatchStatusInvalidPrice)
	}
	return breakdown
}

func normalizedUsageTokenBreakdown(record UsageRecord, matchedChannelBrands ...*aiProviderBrand) usageTokenBreakdown {
	inputTokens := nonNegativeTokens(record.InputTokens)
	outputTokens := nonNegativeTokens(record.OutputTokens)
	if usageUsesClaudeTokenSemantics(record, matchedChannelBrands...) {
		return usageTokenBreakdown{
			NormalInputTokens:   inputTokens,
			CacheReadTokens:     nonNegativeTokens(record.CacheReadTokens),
			CacheCreationTokens: nonNegativeTokens(record.CacheCreationTokens),
			OutputTokens:        outputTokens,
		}
	}

	cacheReadTokens := nonNegativeTokens(record.CacheReadTokens)
	if cacheReadTokens == 0 {
		cacheReadTokens = nonNegativeTokens(record.CachedTokens)
	}
	cacheReadTokens = boundedTokens(cacheReadTokens, inputTokens)
	remainingInputTokens := inputTokens - cacheReadTokens
	cacheCreationTokens := boundedTokens(record.CacheCreationTokens, remainingInputTokens)
	return usageTokenBreakdown{
		NormalInputTokens:   remainingInputTokens - cacheCreationTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
		OutputTokens:        outputTokens,
	}
}

func appendUsageTokenCostItem(breakdown *usageCostBreakdown, collectItem bool, kind string, tokens int, usdPerMillion float64) bool {
	if breakdown == nil || tokens <= 0 {
		return true
	}
	subtotal, ok := roundCostUSD(millionTokenCost(tokens, usdPerMillion))
	if !ok {
		return false
	}
	totalUSD, ok := roundCostUSD(breakdown.TotalUSD + subtotal)
	if !ok {
		return false
	}
	if collectItem {
		breakdown.Items = append(breakdown.Items, usageTokenCostBreakdownItem{
			Kind:          kind,
			Tokens:        tokens,
			USDPerMillion: usdPerMillion,
			SubtotalUSD:   subtotal,
		})
	}
	breakdown.TotalUSD = totalUSD
	return true
}

func modelPriceIsClaudeChannel(price ModelPrice) bool {
	return price.PriceScope == modelPriceScopeChannel && price.ChannelBrand != nil && aiProviderBrand(strings.TrimSpace(*price.ChannelBrand)) == aiProviderBrandClaude
}

func matchedModelPriceChannelBrand(price *ModelPrice, record UsageRecord, matchContexts ...modelPriceMatchContext) *aiProviderBrand {
	if price != nil && price.PriceScope == modelPriceScopeChannel && price.ChannelBrand != nil {
		brand := aiProviderBrand(strings.TrimSpace(*price.ChannelBrand))
		return &brand
	}
	if len(matchContexts) == 0 || !matchContexts[0].SelectorsAvailable || record.Provider == nil || record.Model == nil {
		return nil
	}
	provider := strings.TrimSpace(*record.Provider)
	model := normalizeModelPriceChannelModel(*record.Model)
	if provider == "" || model == "" {
		return nil
	}
	if strings.TrimSpace(aiProviderOptionalString(record.AuthIndex)) == "" {
		_, nativeCount := configuredMissingAuthModelPriceCandidateCounts(matchContexts[0].Selectors, provider, model)
		if nativeCount > 0 {
			return nil
		}
	}
	identity, matchCount := configuredModelPriceCandidates(matchContexts[0].Selectors, provider, model, record.AuthIndex)
	if matchCount != 1 {
		return nil
	}
	brand := identity.Brand
	return &brand
}

func usageUsesClaudeTokenSemantics(record UsageRecord, matchedChannelBrands ...*aiProviderBrand) bool {
	if len(matchedChannelBrands) > 0 && matchedChannelBrands[0] != nil {
		return *matchedChannelBrands[0] == aiProviderBrandClaude
	}
	return isClaudeProvider(record.Provider)
}

func markCostBreakdownUnpriced(breakdown *usageCostBreakdown, reason string) {
	breakdown.TotalUSD = 0
	breakdown.Unpriced = true
	breakdown.UnpricedReason = &reason
	breakdown.LongContextApplied = false
	breakdown.TierMultiplier = nil
	breakdown.Items = breakdown.Items[:0]
}

func roundCostUSD(value float64) (float64, bool) {
	if !finiteNonNegative(value) {
		return 0, false
	}
	if !finiteNonNegative(value * math.Pow10(8)) {
		return 0, false
	}
	rounded := mathRound(value, 8)
	if !finiteNonNegative(rounded) {
		return 0, false
	}
	return rounded, true
}

func recordCost(record UsageRecord, prices modelPriceIndex, matchContexts ...modelPriceMatchContext) (float64, bool) {
	result := calculateRecordCost(record, prices, false, matchContexts...)
	return result.TotalUSD, result.Unpriced
}

func liteLLMHTTPClient(timeout time.Duration, proxyCfg LiteLLMProxyConfig) (*http.Client, error) {
	client := httpClient(timeout)
	if !proxyCfg.Enabled {
		return client, nil
	}
	normalized, err := normalizeLiteLLMProxyConfig(proxyCfg)
	if err != nil {
		return nil, err
	}
	proxyURL, err := url.Parse(normalized.ProxyURL)
	if err != nil {
		return nil, validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has unexpected type")
	}
	cloned := transport.Clone()
	cloned.Proxy = http.ProxyURL(proxyURL)
	client.Transport = cloned
	return client, nil
}

func normalizeLiteLLMProxyConfig(input LiteLLMProxyConfig) (LiteLLMProxyConfig, error) {
	proxyURL, err := normalizeLiteLLMProxyURL(input.ProxyURL)
	if err != nil {
		return LiteLLMProxyConfig{}, err
	}
	if input.Enabled && proxyURL == "" {
		return LiteLLMProxyConfig{}, validationError("启用代理时必须填写代理地址")
	}
	return LiteLLMProxyConfig{Enabled: input.Enabled, ProxyURL: proxyURL}, nil
}

func normalizeLiteLLMProxyURL(value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", nil
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	case "sock5":
		parsed.Scheme = "socks5"
	default:
		return "", validationError("代理地址必须是有效的 http://、https:// 或 socks5:// 地址")
	}
	return parsed.String(), nil
}

func usageAggregateInputTokens(record UsageRecord, matchedChannelBrands ...*aiProviderBrand) int {
	inputTokens := nonNegativeTokens(record.InputTokens)
	if usageUsesClaudeTokenSemantics(record, matchedChannelBrands...) {
		return inputTokens + nonNegativeTokens(record.CacheReadTokens) + nonNegativeTokens(record.CacheCreationTokens)
	}
	return inputTokens
}

func usageAggregateTotalTokens(record UsageRecord, matchedChannelBrands ...*aiProviderBrand) int {
	if usageUsesClaudeTokenSemantics(record, matchedChannelBrands...) {
		return usageAggregateInputTokens(record, matchedChannelBrands...) + nonNegativeTokens(record.OutputTokens) + nonNegativeTokens(record.ReasoningTokens)
	}
	return nonNegativeTokens(record.TotalTokens)
}

func isClaudeProvider(provider *string) bool {
	if provider == nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(*provider))
	return normalized == "claude" || normalized == "anthropic"
}

func boundedTokens(tokens, max int) int {
	tokens = nonNegativeTokens(tokens)
	max = nonNegativeTokens(max)
	if tokens > max {
		return max
	}
	return tokens
}

func nonNegativeTokens(tokens int) int {
	if tokens < 0 {
		return 0
	}
	return tokens
}

func millionTokenCost(tokens int, usdPerMillion float64) float64 {
	return float64(tokens) / 1_000_000 * usdPerMillion
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finitePositive(value float64) bool {
	return value > 0 && finiteNonNegative(value)
}

func nullableFloatArg(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableStringArg(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableSQLFloatArg(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

func nullableInt64Arg(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullableInt64PtrArg(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableSQLStringArg(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableLongContextThreshold(value *ModelPriceLongContext) any {
	if value == nil {
		return nil
	}
	return value.ThresholdInputTokens
}

func nullableLongContextInput(value *ModelPriceLongContext) any {
	if value == nil {
		return nil
	}
	return value.InputUSDPerMillion
}

func nullableLongContextOutput(value *ModelPriceLongContext) any {
	if value == nil {
		return nil
	}
	return value.OutputUSDPerMillion
}

func nullableLongContextCacheRead(value *ModelPriceLongContext) any {
	if value == nil {
		return nil
	}
	return value.CacheReadUSDPerMillion
}

func nullableLongContextCacheCreation(value *ModelPriceLongContext) any {
	if value == nil {
		return nil
	}
	return value.CacheCreationUSDPerMillion
}

func floatPtrEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func longContextPriceEqual(left, right *ModelPriceLongContext) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ThresholdInputTokens == right.ThresholdInputTokens &&
		left.InputUSDPerMillion == right.InputUSDPerMillion &&
		left.OutputUSDPerMillion == right.OutputUSDPerMillion &&
		left.CacheReadUSDPerMillion == right.CacheReadUSDPerMillion &&
		left.CacheCreationUSDPerMillion == right.CacheCreationUSDPerMillion
}

func billingUnitForModel(model string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "image") {
		return modelBillingUnitRequest
	}
	return modelBillingUnitToken
}

func billingUnitForModelPtr(model *string) string {
	if model == nil {
		return modelBillingUnitToken
	}
	return billingUnitForModel(*model)
}

func modelPriceReadyForBilling(price *ModelPrice, fallbackModel string) bool {
	if price == nil {
		return false
	}
	model := price.Model
	if strings.TrimSpace(model) == "" {
		model = fallbackModel
	}
	if billingUnitForModel(model) == modelBillingUnitRequest {
		return price.RequestUSD != nil
	}
	return true
}
