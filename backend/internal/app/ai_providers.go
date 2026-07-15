package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	aiProviderManagementTimeout = 15 * time.Second
	aiProviderActionTimeout     = 45 * time.Second

	aiProviderMissingConfigMessage = "AI 提供商管理需要先到「系统设置」填写 CLIProxyAPI 地址和管理密钥。"
	aiProviderAPICallToken         = "$TOKEN$"
	aiProviderDisableAllModelsRule = "*"
	aiProviderClockBucketMinutes   = 10
	aiProviderHealthyRateNumerator = 9
	aiProviderHealthyRateDenom     = 10
)

var aiProviderRecentRequestClockPattern = regexp.MustCompile(`(^|[^0-9])([01]?[0-9]|2[0-3]):([0-5][0-9])([^0-9]|$)`)

type aiProviderBrand string

const (
	aiProviderBrandGemini              aiProviderBrand = "gemini"
	aiProviderBrandCodex               aiProviderBrand = "codex"
	aiProviderBrandClaude              aiProviderBrand = "claude"
	aiProviderBrandOpenAICompatibility aiProviderBrand = "openai_compatibility"
	aiProviderBrandVertex              aiProviderBrand = "vertex"
)

type aiProviderBrandConfig struct {
	Brand          aiProviderBrand
	Label          string
	UpstreamPath   string
	ConfigKey      string
	DefaultBaseURL string
}

var aiProviderBrandConfigs = []aiProviderBrandConfig{
	{Brand: aiProviderBrandGemini, Label: "Gemini", UpstreamPath: "/v0/management/gemini-api-key", ConfigKey: "gemini-api-key", DefaultBaseURL: "https://generativelanguage.googleapis.com"},
	{Brand: aiProviderBrandCodex, Label: "Codex", UpstreamPath: "/v0/management/codex-api-key", ConfigKey: "codex-api-key", DefaultBaseURL: "https://api.openai.com"},
	{Brand: aiProviderBrandClaude, Label: "Claude", UpstreamPath: "/v0/management/claude-api-key", ConfigKey: "claude-api-key", DefaultBaseURL: "https://api.anthropic.com"},
	{Brand: aiProviderBrandOpenAICompatibility, Label: "OpenAI-compatible", UpstreamPath: "/v0/management/openai-compatibility", ConfigKey: "openai-compatibility"},
	{Brand: aiProviderBrandVertex, Label: "Vertex", UpstreamPath: "/v0/management/vertex-api-key", ConfigKey: "vertex-api-key"},
}

type aiProvidersResponse struct {
	Providers  []aiProviderItem  `json:"providers"`
	Usage      []aiProviderUsage `json:"usage"`
	Summary    aiProviderSummary `json:"summary"`
	UsageError *string           `json:"usage_error,omitempty"`
}

type aiProviderSummary struct {
	Total         int `json:"total"`
	Gemini        int `json:"gemini"`
	Codex         int `json:"codex"`
	Claude        int `json:"claude"`
	OpenAI        int `json:"openai_compatibility"`
	Vertex        int `json:"vertex"`
	RecentSuccess int `json:"recent_success"`
	RecentFailure int `json:"recent_failure"`
}

type aiProviderItem struct {
	Brand                   aiProviderBrand           `json:"brand"`
	BrandLabel              string                    `json:"brand_label"`
	Index                   int                       `json:"index"`
	IdentityHash            string                    `json:"identity_hash"`
	APIKey                  string                    `json:"api_key,omitempty"`
	APIKeyHash              *string                   `json:"api_key_hash,omitempty"`
	APIKeyMasked            *string                   `json:"api_key_masked,omitempty"`
	AuthIndex               *string                   `json:"auth_index,omitempty"`
	Name                    *string                   `json:"name,omitempty"`
	Priority                *int                      `json:"priority,omitempty"`
	Disabled                *bool                     `json:"disabled,omitempty"`
	Prefix                  *string                   `json:"prefix,omitempty"`
	BaseURL                 *string                   `json:"base_url,omitempty"`
	OriginalBaseURL         *string                   `json:"original_base_url,omitempty"`
	ProxyURL                *string                   `json:"proxy_url,omitempty"`
	Models                  []aiProviderModel         `json:"models"`
	Headers                 []aiProviderHeader        `json:"headers"`
	ExcludedModels          []string                  `json:"excluded_models"`
	DisableCooling          *bool                     `json:"disable_cooling,omitempty"`
	Websockets              *bool                     `json:"websockets,omitempty"`
	RebuildMidSystemMessage *bool                     `json:"rebuild_mid_system_message,omitempty"`
	ExperimentalCCHSigning  *bool                     `json:"experimental_cch_signing,omitempty"`
	Cloak                   *aiProviderCloak          `json:"cloak,omitempty"`
	APIKeyEntries           []aiProviderKeyEntry      `json:"api_key_entries"`
	RecentSuccess           int                       `json:"recent_success"`
	RecentFailure           int                       `json:"recent_failure"`
	RecentStatus            string                    `json:"recent_status"`
	RecentStatusAvailable   bool                      `json:"recent_status_available"`
	RecentRequests          []aiProviderRecentRequest `json:"recent_requests"`
	Metadata                map[string]interface{}    `json:"metadata,omitempty"`

	prioritySet bool
	prefixSet   bool
	baseURLSet  bool
	proxyURLSet bool
	headersSet  bool
	entriesSet  bool

	excludedModelsSet bool
}

type aiProviderModel struct {
	Name         string         `json:"name"`
	Alias        string         `json:"alias,omitempty"`
	ForceMapping *bool          `json:"force_mapping,omitempty"`
	Image        *bool          `json:"image,omitempty"`
	Thinking     map[string]any `json:"thinking,omitempty"`
}

type aiProviderHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type aiProviderKeyEntry struct {
	APIKey       string  `json:"api_key,omitempty"`
	APIKeyHash   *string `json:"api_key_hash,omitempty"`
	APIKeyMasked *string `json:"api_key_masked,omitempty"`
	ProxyURL     *string `json:"proxy_url,omitempty"`

	proxyURLSet bool
}

type aiProviderRecentRequest struct {
	Time    *string `json:"time,omitempty"`
	Success int     `json:"success"`
	Failed  int     `json:"failed"`
}

type aiProviderCloak struct {
	Mode           *string  `json:"mode,omitempty"`
	StrictMode     *bool    `json:"strict_mode,omitempty"`
	SensitiveWords []string `json:"sensitive_words"`
	CacheUserID    *bool    `json:"cache_user_id,omitempty"`
}

func (item *aiProviderItem) UnmarshalJSON(data []byte) error {
	type alias aiProviderItem
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*item = aiProviderItem(decoded)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, item.prioritySet = raw["priority"]
	_, item.prefixSet = raw["prefix"]
	_, item.baseURLSet = raw["base_url"]
	_, item.proxyURLSet = raw["proxy_url"]
	_, item.headersSet = raw["headers"]
	_, item.entriesSet = raw["api_key_entries"]
	_, item.excludedModelsSet = raw["excluded_models"]
	return nil
}

func (entry *aiProviderKeyEntry) UnmarshalJSON(data []byte) error {
	type alias aiProviderKeyEntry
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*entry = aiProviderKeyEntry(decoded)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, entry.proxyURLSet = raw["proxy_url"]
	return nil
}

type aiProviderUsage struct {
	Provider                string                    `json:"provider,omitempty"`
	APIKeyHash              *string                   `json:"api_key_hash,omitempty"`
	APIKeyMasked            *string                   `json:"api_key_masked,omitempty"`
	AuthIndex               *string                   `json:"auth_index,omitempty"`
	Name                    *string                   `json:"name,omitempty"`
	BaseURL                 *string                   `json:"base_url,omitempty"`
	SuccessCount            int                       `json:"success_count"`
	FailureCount            int                       `json:"failure_count"`
	TotalCount              int                       `json:"total_count"`
	LastSeen                *string                   `json:"last_seen,omitempty"`
	IdentityHash            *string                   `json:"identity_hash,omitempty"`
	UpstreamLabel           *string                   `json:"upstream_label,omitempty"`
	RecentRequests          []aiProviderRecentRequest `json:"recent_requests"`
	RecentRequestsAvailable bool                      `json:"recent_requests_available"`
}

type aiProviderActionRequest struct {
	Brand    aiProviderBrand `json:"brand"`
	Provider aiProviderItem  `json:"provider"`
	Model    string          `json:"model"`
	Message  string          `json:"message"`
}

type aiProviderActionResponse struct {
	OK         bool              `json:"ok"`
	Status     string            `json:"status"`
	StatusCode int               `json:"status_code"`
	DurationMS int64             `json:"duration_ms"`
	Models     []aiProviderModel `json:"models,omitempty"`
	Reply      string            `json:"reply,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type aiProviderSelector struct {
	IdentityHash string
	APIKeyHash   string
	BaseURL      string
	Name         string
}

type aiProviderAPICallRequest struct {
	AuthIndex string            `json:"auth_index,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      string            `json:"data"`
}

type aiProviderAPICallResponse struct {
	StatusCode int             `json:"status_code"`
	Header     json.RawMessage `json:"header"`
	Body       string          `json:"body"`
}

func (a *App) handleAIProviders(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	response, err := a.aiProvidersSnapshot(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (a *App) handleAIProviderByPath(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	parts := splitPath(r.URL.Path, "/api/ai-providers/")
	if len(parts) == 0 {
		return notFoundError("Not Found")
	}
	switch {
	case len(parts) == 1 && parts[0] == "discover-models":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		var payload aiProviderActionRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		result, err := a.discoverAIProviderModels(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, result)
		return nil
	case len(parts) == 1 && parts[0] == "test":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		var payload aiProviderActionRequest
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		result, err := a.testAIProvider(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, result)
		return nil
	case len(parts) == 1:
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		brandConfig, err := aiProviderConfigFor(parts[0])
		if err != nil {
			return err
		}
		var payload aiProviderItem
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		response, err := a.createAIProvider(r.Context(), brandConfig, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, response)
		return nil
	case len(parts) == 2:
		brandConfig, err := aiProviderConfigFor(parts[0])
		if err != nil {
			return err
		}
		index, err := parseAIProviderIndex(parts[1])
		if err != nil {
			return err
		}
		switch r.Method {
		case http.MethodPut:
			var payload aiProviderItem
			if err := decodeJSON(r, &payload); err != nil {
				return err
			}
			response, err := a.updateAIProvider(r.Context(), brandConfig, index, payload)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, response)
			return nil
		case http.MethodDelete:
			response, err := a.deleteAIProvider(r.Context(), brandConfig, index, aiProviderSelectorFromQuery(r.URL.Query()))
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, response)
			return nil
		default:
			return methodNotAllowed()
		}
	default:
		return notFoundError("Not Found")
	}
}

func (a *App) aiProvidersSnapshot(ctx context.Context) (aiProvidersResponse, error) {
	cfg, err := a.aiProviderManagementConfig(ctx)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	providers, err := a.aiProviderConfigSnapshotForConfig(ctx, cfg)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	response := aiProvidersResponse{
		Providers: providers,
		Usage:     []aiProviderUsage{},
	}
	usagePayload, err := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, "/v0/management/api-key-usage", nil, aiProviderManagementTimeout)
	if err != nil {
		message := err.Error()
		response.UsageError = &message
		applyAIProviderUsage(response.Providers, nil, false)
	} else {
		usage, ok := parseAIProviderUsage(usagePayload)
		response.Usage = usage
		if !ok {
			message := "api-key-usage 响应格式不支持近期状态条"
			response.UsageError = &message
		}
		applyAIProviderUsage(response.Providers, response.Usage, ok)
	}
	response.Summary = aiProviderSummaryFromItems(response.Providers)
	return response, nil
}

func (a *App) aiProviderConfigSnapshot(ctx context.Context) ([]aiProviderItem, error) {
	cfg, err := a.aiProviderManagementConfig(ctx)
	if err != nil {
		return nil, err
	}
	return a.aiProviderConfigSnapshotForConfig(ctx, cfg)
}

func (a *App) aiProviderConfigSnapshotForConfig(ctx context.Context, cfg AppConfig) ([]aiProviderItem, error) {
	startedAt := time.Now()
	generation, cacheable := a.priceSelectors.currentGeneration(modelPriceSelectorConfigKey(cfg))
	providers, err := a.aiProviderConfigSnapshotWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if cacheable {
		a.storeModelPriceSelectorSnapshot(cfg, generation, providers, startedAt)
	}
	return providers, nil
}

func (a *App) aiProviderConfigSnapshotWithConfig(ctx context.Context, cfg AppConfig) ([]aiProviderItem, error) {
	providers := []aiProviderItem{}
	for _, brandConfig := range aiProviderBrandConfigs {
		payload, err := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, brandConfig.UpstreamPath, nil, aiProviderManagementTimeout)
		if err != nil {
			return nil, err
		}
		items, err := parseAIProviderList(payload, brandConfig.ConfigKey)
		if err != nil {
			return nil, validationError(brandConfig.Label + " provider 响应不是有效列表")
		}
		for index, raw := range items {
			providers = append(providers, aiProviderItemFromRaw(brandConfig, index, raw))
		}
	}
	return providers, nil
}

func (a *App) createAIProvider(ctx context.Context, brandConfig aiProviderBrandConfig, payload aiProviderItem) (aiProvidersResponse, error) {
	cfg, rawConfig, err := a.aiProviderRemoteConfig(ctx)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list, err := rawAIProviderList(rawConfig, brandConfig.ConfigKey)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	next, err := aiProviderPayloadToUpstream(brandConfig, payload, nil, true)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list = append(list, next)
	if err := a.putAIProviderList(ctx, cfg, brandConfig, list); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProvidersSnapshot(ctx)
}

func (a *App) updateAIProvider(ctx context.Context, brandConfig aiProviderBrandConfig, index int, payload aiProviderItem) (aiProvidersResponse, error) {
	cfg, rawConfig, err := a.aiProviderRemoteConfig(ctx)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list, err := rawAIProviderList(rawConfig, brandConfig.ConfigKey)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	target, err := resolveAIProviderTarget(brandConfig, list, index, selectorFromAIProviderPayload(payload))
	if err != nil {
		return aiProvidersResponse{}, err
	}
	next, err := aiProviderPayloadToUpstream(brandConfig, payload, list[target], false)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list[target] = next
	if err := a.putAIProviderList(ctx, cfg, brandConfig, list); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProvidersSnapshot(ctx)
}

func (a *App) deleteAIProvider(ctx context.Context, brandConfig aiProviderBrandConfig, index int, selector aiProviderSelector) (aiProvidersResponse, error) {
	cfg, rawConfig, err := a.aiProviderRemoteConfig(ctx)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list, err := rawAIProviderList(rawConfig, brandConfig.ConfigKey)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	target, err := resolveAIProviderTarget(brandConfig, list, index, selector)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	list = append(list[:target], list[target+1:]...)
	if err := a.putAIProviderList(ctx, cfg, brandConfig, list); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProvidersSnapshot(ctx)
}

func (a *App) discoverAIProviderModels(ctx context.Context, payload aiProviderActionRequest) (aiProviderActionResponse, error) {
	brandConfig, err := aiProviderConfigFor(string(payload.Brand))
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	resolved, err := a.resolveAIProviderActionProvider(ctx, brandConfig, payload.Provider)
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	request, err := aiProviderDiscoveryAPICall(brandConfig, resolved)
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	start := time.Now()
	apiResponse, err := a.runAIProviderAPICall(ctx, request)
	durationMS := time.Since(start).Milliseconds()
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	if apiResponse.StatusCode < 200 || apiResponse.StatusCode >= 300 {
		return aiProviderActionResponse{
			OK:         false,
			Status:     "failed",
			StatusCode: apiResponse.StatusCode,
			DurationMS: durationMS,
			Error:      fmt.Sprintf("模型发现失败：上游返回 HTTP %d", apiResponse.StatusCode),
		}, nil
	}
	models := parseDiscoveredAIProviderModels(apiResponse.Body, brandConfig)
	return aiProviderActionResponse{
		OK:         true,
		Status:     "success",
		StatusCode: apiResponse.StatusCode,
		DurationMS: durationMS,
		Models:     models,
	}, nil
}

func (a *App) testAIProvider(ctx context.Context, payload aiProviderActionRequest) (aiProviderActionResponse, error) {
	brandConfig, err := aiProviderConfigFor(string(payload.Brand))
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	resolved, err := a.resolveAIProviderActionProvider(ctx, brandConfig, payload.Provider)
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	model := strings.TrimSpace(payload.Model)
	if model == "" && len(resolved.Models) > 0 {
		model = strings.TrimSpace(resolved.Models[0].Name)
	}
	if model == "" {
		return aiProviderActionResponse{}, validationError("测试模型不能为空")
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "请用一句中文回复：连接测试成功。"
	}
	request, err := aiProviderTestAPICall(brandConfig, resolved, model, message)
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	start := time.Now()
	apiResponse, err := a.runAIProviderAPICall(ctx, request)
	durationMS := time.Since(start).Milliseconds()
	if err != nil {
		return aiProviderActionResponse{}, err
	}
	if apiResponse.StatusCode < 200 || apiResponse.StatusCode >= 300 {
		return aiProviderActionResponse{
			OK:         false,
			Status:     "failed",
			StatusCode: apiResponse.StatusCode,
			DurationMS: durationMS,
			Error:      fmt.Sprintf("连通性测试失败：上游返回 HTTP %d", apiResponse.StatusCode),
		}, nil
	}
	reply := parseAIProviderTestReply(apiResponse.Body, brandConfig)
	return aiProviderActionResponse{
		OK:         true,
		Status:     "success",
		StatusCode: apiResponse.StatusCode,
		DurationMS: durationMS,
		Reply:      reply,
	}, nil
}

func (a *App) resolveAIProviderActionProvider(ctx context.Context, brandConfig aiProviderBrandConfig, payload aiProviderItem) (aiProviderItem, error) {
	payload.Brand = brandConfig.Brand
	payload.BrandLabel = brandConfig.Label
	if strings.TrimSpace(payload.APIKey) != "" || hasOpenAIProviderSubmittedKey(payload) {
		return aiProviderActionPayloadWithoutAuthIndex(payload), nil
	}
	if isOpenAINoAuthActionPayload(brandConfig.Brand, payload) {
		return aiProviderActionPayloadWithoutAuthIndex(payload), nil
	}
	cfg, rawConfig, err := a.aiProviderRemoteConfig(ctx)
	if err != nil {
		return aiProviderItem{}, err
	}
	_ = cfg
	list, err := rawAIProviderList(rawConfig, brandConfig.ConfigKey)
	if err != nil {
		return aiProviderItem{}, err
	}
	target, err := resolveAIProviderTarget(brandConfig, list, payload.Index, selectorFromAIProviderPayload(payload))
	if err != nil {
		return aiProviderItem{}, err
	}
	remote := aiProviderItemFromRaw(brandConfig, target, list[target])
	merged := payload
	if strings.TrimSpace(merged.APIKey) == "" {
		if key := aiProviderRawAPIKey(list[target]); key != "" {
			merged.APIKey = key
		}
	}
	if brandConfig.Brand == aiProviderBrandOpenAICompatibility {
		entries, err := mergeAIProviderActionEntries(merged.APIKeyEntries, list[target])
		if err != nil {
			return aiProviderItem{}, err
		}
		merged.APIKeyEntries = entries
	}
	merged.AuthIndex = remote.AuthIndex
	if merged.BaseURL == nil {
		merged.BaseURL = remote.BaseURL
	}
	if merged.Name == nil {
		merged.Name = remote.Name
	}
	if !merged.headersSet && len(merged.Headers) == 0 {
		merged.Headers = remote.Headers
	}
	return merged, nil
}

func aiProviderActionPayloadWithoutAuthIndex(payload aiProviderItem) aiProviderItem {
	payload.AuthIndex = nil
	return payload
}

func isOpenAINoAuthActionPayload(brand aiProviderBrand, payload aiProviderItem) bool {
	return brand == aiProviderBrandOpenAICompatibility &&
		payload.entriesSet &&
		len(payload.APIKeyEntries) == 0 &&
		strings.TrimSpace(aiProviderOptionalString(payload.BaseURL)) != ""
}

func (a *App) runAIProviderAPICall(ctx context.Context, payload aiProviderAPICallRequest) (aiProviderAPICallResponse, error) {
	cfg, err := a.aiProviderManagementConfig(ctx)
	if err != nil {
		return aiProviderAPICallResponse{}, err
	}
	response, body, err := a.aiProviderManagementResponse(ctx, cfg, http.MethodPost, "/v0/management/api-call", payload, aiProviderActionTimeout)
	if err != nil {
		return aiProviderAPICallResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return aiProviderAPICallResponse{}, validationError(fmt.Sprintf("api-call 管理请求失败：HTTP %d", response.StatusCode))
	}
	var result aiProviderAPICallResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return aiProviderAPICallResponse{}, validationError("api-call 响应不是有效 JSON")
	}
	if result.StatusCode == 0 {
		return aiProviderAPICallResponse{}, validationError("api-call 响应缺少 status_code")
	}
	return result, nil
}

func (a *App) aiProviderManagementConfig(ctx context.Context) (AppConfig, error) {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return AppConfig{}, err
	}
	if strings.TrimSpace(cfg.Collector.CLIProxyURL) == "" || strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		return AppConfig{}, validationError(aiProviderMissingConfigMessage)
	}
	return cfg, nil
}

func (a *App) aiProviderRemoteConfig(ctx context.Context) (AppConfig, map[string]any, error) {
	cfg, err := a.aiProviderManagementConfig(ctx)
	if err != nil {
		return AppConfig{}, nil, err
	}
	payload, err := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, "/v0/management/config", nil, aiProviderManagementTimeout)
	if err != nil {
		return AppConfig{}, nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return AppConfig{}, nil, validationError("CLIProxyAPI 配置响应不是有效 JSON object")
	}
	return cfg, raw, nil
}

func (a *App) aiProviderManagementPayload(ctx context.Context, cfg AppConfig, method, path string, body any, timeout time.Duration) ([]byte, error) {
	response, payload, err := a.aiProviderManagementResponse(ctx, cfg, method, path, body, timeout)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, validationError(fmt.Sprintf("CLIProxyAPI 管理请求失败：HTTP %d", response.StatusCode))
	}
	return payload, nil
}

func (a *App) aiProviderManagementResponse(ctx context.Context, cfg AppConfig, method, path string, body any, timeout time.Duration) (*http.Response, []byte, error) {
	managementURL, err := collectorManagementHTTPURL(cfg.Collector.CLIProxyURL)
	if err != nil {
		return nil, nil, validationError("CLIProxyAPI 管理地址无效：" + err.Error())
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, payload, err := doJSON(requestCtx, httpClient(timeout), method, makeURL(managementURL, path, nil), managementHeaders(cfg.Collector.ManagementKey), body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return nil, nil, validationError("CLIProxyAPI 管理请求超时，请检查地址和管理密钥")
		}
		return nil, nil, validationError("CLIProxyAPI 管理请求失败：" + err.Error())
	}
	return response, payload, nil
}

func (a *App) putAIProviderList(ctx context.Context, cfg AppConfig, brandConfig aiProviderBrandConfig, list []map[string]any) error {
	response, _, err := a.aiProviderManagementResponse(ctx, cfg, http.MethodPut, brandConfig.UpstreamPath, list, aiProviderManagementTimeout)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return validationError(fmt.Sprintf("写入 %s provider 失败：HTTP %d", brandConfig.Label, response.StatusCode))
	}
	a.invalidateModelPriceSelectorSnapshot(cfg)
	return nil
}

func aiProviderConfigFor(value string) (aiProviderBrandConfig, error) {
	normalized := strings.TrimSpace(value)
	for _, config := range aiProviderBrandConfigs {
		if normalized == string(config.Brand) || normalized == config.ConfigKey {
			return config, nil
		}
	}
	return aiProviderBrandConfig{}, validationError("AI provider 类型无效")
}

func parseAIProviderIndex(value string) (int, error) {
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 {
		return 0, notFoundError("AI provider 不存在")
	}
	return index, nil
}

func parseAIProviderList(payload []byte, key string) ([]map[string]any, error) {
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	return aiProviderListFromAny(raw, key)
}

func rawAIProviderList(config map[string]any, key string) ([]map[string]any, error) {
	value, ok := config[key]
	if !ok {
		return []map[string]any{}, nil
	}
	return aiProviderListFromAny(value, key)
}

func aiProviderListFromAny(value any, key string) ([]map[string]any, error) {
	switch typed := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("provider item is not object")
			}
			result = append(result, object)
		}
		return result, nil
	case map[string]any:
		for _, candidate := range []string{"items", key, "value", "data"} {
			if nested, ok := typed[candidate]; ok {
				return aiProviderListFromAny(nested, key)
			}
		}
		return nil, fmt.Errorf("provider list wrapper missing items")
	case nil:
		return []map[string]any{}, nil
	default:
		return nil, fmt.Errorf("provider list has invalid shape")
	}
}

func aiProviderItemFromRaw(brandConfig aiProviderBrandConfig, index int, raw map[string]any) aiProviderItem {
	apiKey := aiProviderRawAPIKey(raw)
	apiKeyHash := aiProviderRawAPIKeyHash(raw)
	apiKeyMasked := aiProviderRawAPIKeyMasked(raw)
	if apiKey != "" {
		hashed := hashAPIKey(apiKey)
		apiKeyHash = &hashed
		masked := maskSecret(&apiKey)
		apiKeyMasked = &masked
	}
	name := aiProviderStringFromKeys(raw, "name")
	baseURL := aiProviderStringFromKeys(raw, "base-url", "base_url")
	authIndex := aiProviderStringFromKeys(raw, "auth-index", "auth_index")
	identity := ""
	if brandConfig.Brand == aiProviderBrandOpenAICompatibility {
		if name != nil {
			identity = hashAPIKey("openai_compatibility:" + strings.ToLower(strings.TrimSpace(*name)))
		}
	} else if apiKeyHash != nil {
		identity = *apiKeyHash
	} else if authIndex != nil {
		if value := strings.TrimSpace(*authIndex); value != "" {
			identity = hashAPIKey(fmt.Sprintf("%s:auth-index:%s", brandConfig.Brand, value))
		}
	}
	if identity == "" {
		identity = hashAPIKey(fmt.Sprintf("%s:%d:%s", brandConfig.Brand, index, aiProviderOptionalString(baseURL)))
	}
	usesExcludedModelsDisabled := aiProviderUsesExcludedModelsDisabled(brandConfig.Brand)
	excludedModels, disabledByExcludedModels := aiProviderExcludedModelsFromRaw(raw, usesExcludedModelsDisabled)
	disabled := aiProviderBoolPtrFromKeys(raw, "disabled")
	if usesExcludedModelsDisabled {
		isDisabled := disabledByExcludedModels
		if disabled != nil && *disabled {
			isDisabled = true
		}
		disabled = &isDisabled
	}
	return aiProviderItem{
		Brand:                   brandConfig.Brand,
		BrandLabel:              brandConfig.Label,
		Index:                   index,
		IdentityHash:            identity,
		APIKeyHash:              apiKeyHash,
		APIKeyMasked:            apiKeyMasked,
		AuthIndex:               authIndex,
		Name:                    name,
		Priority:                aiProviderIntPtrFromKeys(raw, "priority"),
		Disabled:                disabled,
		Prefix:                  aiProviderStringFromKeys(raw, "prefix"),
		BaseURL:                 baseURL,
		ProxyURL:                aiProviderStringFromKeys(raw, "proxy-url", "proxy_url"),
		Models:                  aiProviderModelsFromAny(raw["models"], brandConfig.Brand == aiProviderBrandOpenAICompatibility),
		Headers:                 aiProviderHeadersFromAny(raw["headers"]),
		ExcludedModels:          excludedModels,
		DisableCooling:          aiProviderBoolPtrFromKeys(raw, "disable-cooling", "disable_cooling"),
		Websockets:              aiProviderBoolPtrFromKeys(raw, "websockets"),
		RebuildMidSystemMessage: aiProviderBoolPtrFromKeys(raw, "rebuild-mid-system-message", "rebuild_mid_system_message"),
		ExperimentalCCHSigning:  aiProviderBoolPtrFromKeys(raw, "experimental-cch-signing", "experimental_cch_signing"),
		Cloak:                   aiProviderCloakFromAny(raw["cloak"]),
		APIKeyEntries:           aiProviderKeyEntriesFromAny(raw["api-key-entries"]),
		RecentStatus:            "unknown",
		RecentRequests:          []aiProviderRecentRequest{},
	}
}

func aiProviderPayloadToUpstream(brandConfig aiProviderBrandConfig, payload aiProviderItem, current map[string]any, create bool) (map[string]any, error) {
	next := map[string]any{}
	if current != nil {
		next = cloneAIProviderMap(current)
	}
	removeAIProviderResponseOnlyFields(next)
	if brandConfig.Brand == aiProviderBrandOpenAICompatibility {
		name := strings.TrimSpace(aiProviderOptionalString(payload.Name))
		if name == "" {
			return nil, validationError("OpenAI-compatible provider 名称不能为空")
		}
		next["name"] = name
		if payload.Disabled != nil {
			next["disabled"] = *payload.Disabled
		}
		entries, err := aiProviderKeyEntriesToUpstream(payload.APIKeyEntries, current, create)
		if err != nil {
			return nil, err
		}
		next["api-key-entries"] = entries
	} else {
		delete(next, "disabled")
		apiKey := strings.TrimSpace(payload.APIKey)
		if apiKey == "" {
			if create {
				return nil, validationError(brandConfig.Label + " API key 不能为空")
			}
		} else {
			next["api-key"] = apiKey
		}
		preserveAIProviderAuthIndexReference(next, current)
	}
	if payload.prioritySet && payload.Priority == nil {
		delete(next, "priority")
	} else if payload.Priority != nil {
		next["priority"] = *payload.Priority
	}
	setOptionalStringField(next, "prefix", payload.Prefix, payload.prefixSet)
	setOptionalStringField(next, "base-url", payload.BaseURL, payload.baseURLSet)
	setOptionalStringField(next, "proxy-url", payload.ProxyURL, payload.proxyURLSet)
	if err := validateAIProviderBaseURLForSave(brandConfig, next); err != nil {
		return nil, err
	}
	next["models"] = aiProviderModelsToUpstream(payload.Models, brandConfig.Brand == aiProviderBrandOpenAICompatibility, current)
	next["headers"] = aiProviderHeadersToUpstream(payload.Headers)
	if aiProviderUsesExcludedModelsDisabled(brandConfig.Brand) {
		excludedModels := []string{}
		if payload.excludedModelsSet {
			excludedModels = aiProviderStringListToUpstream(payload.ExcludedModels)
		} else if current != nil {
			excludedModels, _ = aiProviderExcludedModelsFromRaw(current, true)
		}
		excludedModels = aiProviderExcludedModelsWithDisabled(excludedModels, payload.Disabled, current)
		next["excluded-models"] = excludedModels
	} else if payload.excludedModelsSet {
		next["excluded-models"] = aiProviderStringListToUpstream(payload.ExcludedModels)
	}
	if payload.DisableCooling != nil && brandConfig.Brand != aiProviderBrandVertex {
		next["disable-cooling"] = *payload.DisableCooling
	}
	if brandConfig.Brand == aiProviderBrandCodex && payload.Websockets != nil {
		next["websockets"] = *payload.Websockets
	}
	if brandConfig.Brand == aiProviderBrandClaude {
		if payload.RebuildMidSystemMessage != nil {
			next["rebuild-mid-system-message"] = *payload.RebuildMidSystemMessage
		}
		if payload.ExperimentalCCHSigning != nil {
			next["experimental-cch-signing"] = *payload.ExperimentalCCHSigning
		}
		if payload.Cloak != nil {
			next["cloak"] = aiProviderCloakToUpstream(*payload.Cloak, current)
		}
	}
	return next, nil
}

func preserveAIProviderAuthIndexReference(next map[string]any, current map[string]any) {
	if current == nil || aiProviderRawAPIKey(current) != "" || aiProviderRawAPIKey(next) != "" {
		return
	}
	authIndex := aiProviderOptionalString(aiProviderStringFromKeys(current, "auth-index", "auth_index"))
	if authIndex != "" {
		next["auth-index"] = authIndex
	}
}

func validateAIProviderBaseURLForSave(brandConfig aiProviderBrandConfig, next map[string]any) error {
	baseURL := strings.TrimSpace(aiProviderOptionalString(aiProviderStringFromKeys(next, "base-url", "base_url")))
	if baseURL == "" {
		if brandConfig.Brand == aiProviderBrandCodex || brandConfig.Brand == aiProviderBrandOpenAICompatibility || brandConfig.Brand == aiProviderBrandVertex {
			return validationError(brandConfig.Label + " base_url 不能为空")
		}
		return nil
	}
	if err := ensureHTTPSURL(baseURL); err != nil {
		return err
	}
	return nil
}

func aiProviderKeyEntriesToUpstream(entries []aiProviderKeyEntry, current map[string]any, create bool) ([]map[string]any, error) {
	currentEntries := aiProviderRawKeyEntries(current)
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		currentEntry, err := findAIProviderCurrentEntry(entry, currentEntries)
		if err != nil {
			return nil, err
		}
		next := map[string]any{}
		if currentEntry != nil {
			next = cloneAIProviderMap(currentEntry)
		}
		removeAIProviderResponseOnlyFields(next)
		apiKey := strings.TrimSpace(entry.APIKey)
		if apiKey == "" {
			if preserved := aiProviderRawAPIKey(currentEntry); preserved != "" {
				next["api-key"] = preserved
			} else {
				return nil, validationError("OpenAI-compatible API key 不能为空")
			}
		} else {
			next["api-key"] = apiKey
		}
		setOptionalStringField(next, "proxy-url", entry.ProxyURL, entry.proxyURLSet)
		result = append(result, next)
	}
	return result, nil
}

func findAIProviderCurrentEntry(entry aiProviderKeyEntry, currentEntries []map[string]any) (map[string]any, error) {
	if entry.APIKeyHash != nil {
		expectedHash := strings.TrimSpace(*entry.APIKeyHash)
		if expectedHash != "" {
			for _, current := range currentEntries {
				key := aiProviderRawAPIKey(current)
				if key != "" && hashAPIKey(key) == expectedHash {
					return current, nil
				}
			}
			return nil, conflictError("OpenAI-compatible API key entry 已被其他工具修改或删除，请刷新后重试")
		}
	}
	return nil, nil
}

func resolveAIProviderTarget(brandConfig aiProviderBrandConfig, list []map[string]any, index int, selector aiProviderSelector) (int, error) {
	if !selector.validFor(brandConfig) {
		return 0, validationError("AI provider selector 缺少非密钥标识，请刷新后重试")
	}
	if index >= 0 && index < len(list) && matchesAIProviderSelector(brandConfig, list[index], index, selector) {
		return index, nil
	}
	for currentIndex, item := range list {
		if matchesAIProviderSelector(brandConfig, item, currentIndex, selector) {
			return currentIndex, nil
		}
	}
	return 0, conflictError("目标 AI provider 已被其他工具修改或删除，请刷新后重试")
}

func (selector aiProviderSelector) validFor(brandConfig aiProviderBrandConfig) bool {
	if brandConfig.Brand == aiProviderBrandOpenAICompatibility {
		return strings.TrimSpace(selector.Name) != "" || strings.TrimSpace(selector.IdentityHash) != ""
	}
	return strings.TrimSpace(selector.IdentityHash) != "" || strings.TrimSpace(selector.APIKeyHash) != ""
}

func matchesAIProviderSelector(brandConfig aiProviderBrandConfig, raw map[string]any, index int, selector aiProviderSelector) bool {
	item := aiProviderItemFromRaw(brandConfig, index, raw)
	if selector.IdentityHash != "" && item.IdentityHash != selector.IdentityHash {
		return false
	}
	if selector.APIKeyHash != "" && (item.APIKeyHash == nil || *item.APIKeyHash != selector.APIKeyHash) {
		return false
	}
	if selector.BaseURL != "" && aiProviderOptionalString(item.BaseURL) != selector.BaseURL {
		return false
	}
	if selector.Name != "" && !strings.EqualFold(aiProviderOptionalString(item.Name), selector.Name) {
		return false
	}
	return true
}

func selectorFromAIProviderPayload(payload aiProviderItem) aiProviderSelector {
	selector := aiProviderSelector{
		IdentityHash: strings.TrimSpace(payload.IdentityHash),
	}
	if payload.APIKeyHash != nil {
		selector.APIKeyHash = strings.TrimSpace(*payload.APIKeyHash)
	}
	if payload.OriginalBaseURL != nil {
		selector.BaseURL = aiProviderOptionalString(payload.OriginalBaseURL)
	}
	if selector.IdentityHash == "" && selector.APIKeyHash == "" {
		selector.Name = aiProviderOptionalString(payload.Name)
	}
	return selector
}

func aiProviderSelectorFromQuery(query url.Values) aiProviderSelector {
	return aiProviderSelector{
		IdentityHash: strings.TrimSpace(query.Get("identity_hash")),
		APIKeyHash:   strings.TrimSpace(query.Get("api_key_hash")),
		BaseURL:      strings.TrimSpace(query.Get("base_url")),
		Name:         strings.TrimSpace(query.Get("name")),
	}
}

func aiProviderDiscoveryAPICall(brandConfig aiProviderBrandConfig, provider aiProviderItem) (aiProviderAPICallRequest, error) {
	baseURL, err := aiProviderBaseURL(brandConfig, provider)
	if err != nil {
		return aiProviderAPICallRequest{}, err
	}
	authIndex := aiProviderOptionalString(provider.AuthIndex)
	headers := aiProviderRequestHeaders(provider)
	credential := aiProviderRequestCredential(provider, authIndex)
	if credential == "" && aiProviderActionRequiresCredential(brandConfig.Brand) {
		return aiProviderAPICallRequest{}, validationError(brandConfig.Label + " API key 缺失，无法发现模型")
	}
	switch brandConfig.Brand {
	case aiProviderBrandGemini:
		headers["x-goog-api-key"] = credential
		return aiProviderAPICallRequest{AuthIndex: authIndex, Method: http.MethodGet, URL: aiProviderGeminiAPIBaseURL(baseURL) + "/models", Header: headers}, nil
	case aiProviderBrandClaude:
		headers["x-api-key"] = credential
		headers["anthropic-version"] = "2023-06-01"
		return aiProviderAPICallRequest{AuthIndex: authIndex, Method: http.MethodGet, URL: aiProviderClaudeAPIBaseURL(baseURL) + "/models", Header: headers}, nil
	case aiProviderBrandVertex:
		headers["x-goog-api-key"] = credential
		return aiProviderAPICallRequest{AuthIndex: authIndex, Method: http.MethodGet, URL: aiProviderVertexModelsEndpoint(baseURL), Header: headers}, nil
	default:
		if credential != "" {
			headers["Authorization"] = "Bearer " + credential
		}
		return aiProviderAPICallRequest{AuthIndex: authIndex, Method: http.MethodGet, URL: aiProviderOpenAIBaseURL(baseURL) + "/models", Header: headers}, nil
	}
}

func aiProviderTestAPICall(brandConfig aiProviderBrandConfig, provider aiProviderItem, model, message string) (aiProviderAPICallRequest, error) {
	baseURL, err := aiProviderBaseURL(brandConfig, provider)
	if err != nil {
		return aiProviderAPICallRequest{}, err
	}
	authIndex := aiProviderOptionalString(provider.AuthIndex)
	headers := aiProviderRequestHeaders(provider)
	headers["Content-Type"] = "application/json"
	credential := aiProviderRequestCredential(provider, authIndex)
	if credential == "" && aiProviderActionRequiresCredential(brandConfig.Brand) {
		return aiProviderAPICallRequest{}, validationError(brandConfig.Label + " API key 缺失，无法执行连通性测试")
	}
	var body any
	var target string
	switch brandConfig.Brand {
	case aiProviderBrandGemini:
		headers["x-goog-api-key"] = credential
		target = aiProviderGeminiAPIBaseURL(baseURL) + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent"
		body = map[string]any{"contents": []map[string]any{{"parts": []map[string]string{{"text": message}}}}}
	case aiProviderBrandClaude:
		headers["x-api-key"] = credential
		headers["anthropic-version"] = "2023-06-01"
		target = aiProviderClaudeAPIBaseURL(baseURL) + "/messages"
		body = map[string]any{"model": model, "max_tokens": 64, "messages": []map[string]string{{"role": "user", "content": message}}}
	case aiProviderBrandVertex:
		headers["x-goog-api-key"] = credential
		target = aiProviderVertexModelEndpoint(baseURL, model) + ":generateContent"
		body = map[string]any{"contents": []map[string]any{{"parts": []map[string]string{{"text": message}}}}}
	case aiProviderBrandCodex:
		headers["Authorization"] = "Bearer " + credential
		target = aiProviderOpenAIBaseURL(baseURL) + "/responses"
		body = map[string]any{"model": model, "input": message}
	default:
		if credential != "" {
			headers["Authorization"] = "Bearer " + credential
		}
		target = aiProviderOpenAIBaseURL(baseURL) + "/chat/completions"
		body = map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": message}}, "stream": false}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return aiProviderAPICallRequest{}, err
	}
	return aiProviderAPICallRequest{AuthIndex: authIndex, Method: http.MethodPost, URL: target, Header: headers, Data: string(encoded)}, nil
}

func aiProviderActionRequiresCredential(brand aiProviderBrand) bool {
	return brand != aiProviderBrandOpenAICompatibility
}

func aiProviderBaseURL(brandConfig aiProviderBrandConfig, provider aiProviderItem) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(aiProviderOptionalString(provider.BaseURL)), "/")
	if baseURL == "" {
		baseURL = brandConfig.DefaultBaseURL
	}
	if baseURL == "" {
		return "", validationError(brandConfig.Label + " base_url 不能为空")
	}
	if err := ensureHTTPSURL(baseURL); err != nil {
		return "", err
	}
	return baseURL, nil
}

func aiProviderGeminiAPIBaseURL(baseURL string) string {
	normalized := strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasSuffix(normalized, "/v1beta/models"):
		return strings.TrimSuffix(normalized, "/models")
	case strings.HasSuffix(normalized, "/v1/models"):
		return strings.TrimSuffix(normalized, "/models")
	case strings.HasSuffix(normalized, "/v1beta"), strings.HasSuffix(normalized, "/v1"):
		return normalized
	default:
		return normalized + "/v1beta"
	}
}

func aiProviderClaudeAPIBaseURL(baseURL string) string {
	normalized := strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasSuffix(normalized, "/v1/models"):
		return strings.TrimSuffix(normalized, "/models")
	case strings.HasSuffix(normalized, "/v1/messages"):
		return strings.TrimSuffix(normalized, "/messages")
	case strings.HasSuffix(normalized, "/v1"):
		return normalized
	default:
		return normalized + "/v1"
	}
}

func aiProviderOpenAIBaseURL(baseURL string) string {
	normalized := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(normalized, "/v1") {
		return normalized
	}
	return normalized + "/v1"
}

func aiProviderVertexModelsEndpoint(baseURL string) string {
	normalized := strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasSuffix(normalized, "/publishers/google/models"):
		return normalized
	case strings.Contains(normalized, "/publishers/google/models/"):
		prefix, _, _ := strings.Cut(normalized, "/publishers/google/models/")
		return prefix + "/publishers/google/models"
	case strings.HasSuffix(normalized, "/v1") || strings.Contains(normalized, "/v1/"):
		return normalized + "/publishers/google/models"
	default:
		return normalized + "/v1/publishers/google/models"
	}
}

func aiProviderVertexModelEndpoint(baseURL, model string) string {
	name := strings.TrimSpace(model)
	name = strings.TrimPrefix(name, "models/")
	name = strings.TrimPrefix(name, "publishers/google/models/")
	return aiProviderVertexModelsEndpoint(baseURL) + "/" + url.PathEscape(name)
}

func aiProviderRequestCredential(provider aiProviderItem, authIndex string) string {
	if key := firstAIProviderRequestKey(provider); key != "" {
		return key
	}
	if strings.TrimSpace(authIndex) != "" {
		return aiProviderAPICallToken
	}
	return ""
}

func firstAIProviderRequestKey(provider aiProviderItem) string {
	if key := strings.TrimSpace(provider.APIKey); key != "" {
		return key
	}
	for _, entry := range provider.APIKeyEntries {
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			return key
		}
	}
	return ""
}

func aiProviderRequestHeaders(provider aiProviderItem) map[string]string {
	headers := map[string]string{}
	for _, header := range provider.Headers {
		name := strings.TrimSpace(header.Name)
		if name != "" {
			headers[name] = header.Value
		}
	}
	return headers
}

func parseDiscoveredAIProviderModels(body string, brandConfig aiProviderBrandConfig) []aiProviderModel {
	var raw any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return []aiProviderModel{}
	}
	names := []string{}
	collectModelNames(raw, &names)
	seen := map[string]bool{}
	models := []aiProviderModel{}
	for _, name := range names {
		normalized := strings.TrimSpace(name)
		normalized = strings.TrimPrefix(normalized, "models/")
		normalized = strings.TrimPrefix(normalized, "publishers/google/models/")
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		models = append(models, aiProviderModel{Name: normalized})
	}
	return models
}

func collectModelNames(value any, names *[]string) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectModelNames(item, names)
		}
	case map[string]any:
		if id := aiProviderFirstString(typed, "id", "name", "model"); id != nil {
			*names = append(*names, *id)
			return
		}
		for _, key := range []string{"data", "models", "items", "publisherModels", "publisher_models"} {
			if nested, ok := typed[key]; ok {
				collectModelNames(nested, names)
			}
		}
	}
}

func parseAIProviderTestReply(body string, brandConfig aiProviderBrandConfig) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return ""
	}
	switch brandConfig.Brand {
	case aiProviderBrandClaude:
		return extractClaudeMessagesReply(raw)
	case aiProviderBrandGemini, aiProviderBrandVertex:
		return extractGeminiReply(raw)
	default:
		return extractChatCompletionReply(raw)
	}
}

func extractGeminiReply(raw map[string]any) string {
	candidates, ok := raw["candidates"].([]any)
	if !ok {
		return ""
	}
	for _, item := range candidates {
		candidate, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if content := chatContentText(candidate["content"]); content != "" {
			return content
		}
	}
	return ""
}

func applyAIProviderUsage(providers []aiProviderItem, usage []aiProviderUsage, usageAvailable bool) {
	for index := range providers {
		providers[index].RecentStatusAvailable = usageAvailable
		providers[index].RecentSuccess = 0
		providers[index].RecentFailure = 0
		providers[index].RecentRequests = []aiProviderRecentRequest{}
		if !usageAvailable {
			providers[index].RecentStatus = "unavailable"
			continue
		}
		matched := false
		bucketUnavailable := false
		for _, item := range usage {
			if !matchesAIProviderUsage(providers[index], item) {
				continue
			}
			matched = true
			providers[index].RecentSuccess += item.SuccessCount
			providers[index].RecentFailure += item.FailureCount
			providers[index].RecentRequests = mergeAIProviderRecentRequests(providers[index].RecentRequests, item.RecentRequests)
			if aiProviderUsageRecentRequestsUnavailable(item) {
				bucketUnavailable = true
			}
		}
		if matched && bucketUnavailable {
			providers[index].RecentStatusAvailable = false
			providers[index].RecentStatus = "unavailable"
			continue
		}
		providers[index].RecentStatus = aiProviderRecentStatus(providers[index].RecentSuccess, providers[index].RecentFailure)
	}
}

func aiProviderRecentStatus(successCount int, failureCount int) string {
	totalCount := successCount + failureCount
	switch {
	case totalCount <= 0:
		return "unknown"
	case successCount > 0 && successCount*aiProviderHealthyRateDenom >= totalCount*aiProviderHealthyRateNumerator:
		return "healthy"
	case failureCount > 0:
		return "failing"
	default:
		return "healthy"
	}
}

func aiProviderUsageRecentRequestsUnavailable(usage aiProviderUsage) bool {
	if usage.TotalCount <= 0 {
		return false
	}
	if !usage.RecentRequestsAvailable {
		return true
	}
	hasPositiveBucket := false
	for _, item := range usage.RecentRequests {
		if item.Success+item.Failed <= 0 {
			continue
		}
		hasPositiveBucket = true
		if item.Time == nil || strings.TrimSpace(*item.Time) == "" {
			return true
		}
	}
	return !hasPositiveBucket
}

func mergeAIProviderRecentRequests(existing []aiProviderRecentRequest, incoming []aiProviderRecentRequest) []aiProviderRecentRequest {
	if len(incoming) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return normalizeAIProviderRecentRequests(incoming)
	}
	hasTime := false
	positions := make(map[string]int, len(existing))
	for index, item := range existing {
		if item.Time == nil || strings.TrimSpace(*item.Time) == "" {
			continue
		}
		hasTime = true
		positions[strings.TrimSpace(*item.Time)] = index
	}
	for _, item := range incoming {
		if item.Time != nil && strings.TrimSpace(*item.Time) != "" {
			hasTime = true
			break
		}
	}
	for index, item := range incoming {
		if item.Time != nil && strings.TrimSpace(*item.Time) != "" {
			key := strings.TrimSpace(*item.Time)
			if position, ok := positions[key]; ok {
				existing[position].Success += item.Success
				existing[position].Failed += item.Failed
				continue
			}
			positions[key] = len(existing)
			existing = append(existing, item)
			continue
		}
		if !hasTime && index < len(existing) {
			existing[index].Success += item.Success
			existing[index].Failed += item.Failed
			continue
		}
		if hasTime && item.Success+item.Failed <= 0 {
			continue
		}
		existing = append(existing, item)
	}
	return normalizeAIProviderRecentRequests(existing)
}

func normalizeAIProviderRecentRequests(items []aiProviderRecentRequest) []aiProviderRecentRequest {
	items = filterAIProviderTimedSeriesBuckets(items)
	sortAIProviderTimedSeriesBuckets(items)
	return trimAIProviderRecentRequests(items)
}

func filterAIProviderTimedSeriesBuckets(items []aiProviderRecentRequest) []aiProviderRecentRequest {
	hasTime := false
	for _, item := range items {
		if aiProviderRecentRequestTime(item) != "" {
			hasTime = true
			break
		}
	}
	if !hasTime {
		return items
	}
	filtered := items[:0]
	for _, item := range items {
		if aiProviderRecentRequestTime(item) == "" && item.Success+item.Failed <= 0 {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func sortAIProviderTimedSeriesBuckets(items []aiProviderRecentRequest) {
	if sortAIProviderClockSeriesBuckets(items) {
		return
	}
	sort.SliceStable(items, func(leftIndex, rightIndex int) bool {
		leftTime := aiProviderRecentRequestTime(items[leftIndex])
		rightTime := aiProviderRecentRequestTime(items[rightIndex])
		if leftTime == "" || rightTime == "" {
			return leftTime != "" && rightTime == ""
		}
		leftParsed, leftOK := aiProviderParseRecentRequestTime(leftTime)
		rightParsed, rightOK := aiProviderParseRecentRequestTime(rightTime)
		if leftOK && rightOK {
			return leftParsed.Before(rightParsed)
		}
		return leftTime < rightTime
	})
}

func sortAIProviderClockSeriesBuckets(items []aiProviderRecentRequest) bool {
	minutes := map[int]struct{}{}
	hasClockTime := false
	for _, item := range items {
		value := aiProviderRecentRequestTime(item)
		if value == "" {
			continue
		}
		if _, ok := aiProviderParseRecentRequestTime(value); ok {
			return false
		}
		minute, ok := aiProviderParseRecentRequestClockMinute(value)
		if !ok {
			return false
		}
		minutes[minute] = struct{}{}
		hasClockTime = true
	}
	if !hasClockTime {
		return false
	}
	orderedMinutes := aiProviderOrderedClockMinutes(minutes)
	minuteRank := make(map[int]int, len(orderedMinutes))
	for rank, minute := range orderedMinutes {
		minuteRank[minute] = rank
	}
	sort.SliceStable(items, func(leftIndex, rightIndex int) bool {
		leftTime := aiProviderRecentRequestTime(items[leftIndex])
		rightTime := aiProviderRecentRequestTime(items[rightIndex])
		if leftTime == "" || rightTime == "" {
			return leftTime != "" && rightTime == ""
		}
		leftMinute, leftOK := aiProviderParseRecentRequestClockMinute(leftTime)
		rightMinute, rightOK := aiProviderParseRecentRequestClockMinute(rightTime)
		if !leftOK || !rightOK || leftMinute == rightMinute {
			return false
		}
		return minuteRank[leftMinute] < minuteRank[rightMinute]
	})
	return true
}

func aiProviderOrderedClockMinutes(minutes map[int]struct{}) []int {
	ordered := make([]int, 0, len(minutes))
	for minute := range minutes {
		ordered = append(ordered, minute)
	}
	sort.Ints(ordered)
	if len(ordered) <= 1 {
		return ordered
	}
	largestGapIndex := len(ordered) - 1
	largestGap := -1
	for index, current := range ordered {
		next := ordered[(index+1)%len(ordered)]
		if index == len(ordered)-1 {
			next += 24 * 60
		}
		gap := next - current
		if gap > largestGap {
			largestGap = gap
			largestGapIndex = index
		}
	}
	if largestGap <= aiProviderClockBucketMinutes {
		return ordered
	}
	result := append([]int{}, ordered[largestGapIndex+1:]...)
	result = append(result, ordered[:largestGapIndex+1]...)
	return result
}

func aiProviderRecentRequestTime(item aiProviderRecentRequest) string {
	if item.Time == nil {
		return ""
	}
	return strings.TrimSpace(*item.Time)
}

func aiProviderParseRecentRequestTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func aiProviderParseRecentRequestClockMinute(value string) (int, bool) {
	matches := aiProviderRecentRequestClockPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(matches) != 5 {
		return 0, false
	}
	hour, err := strconv.Atoi(matches[2])
	if err != nil {
		return 0, false
	}
	minute, err := strconv.Atoi(matches[3])
	if err != nil {
		return 0, false
	}
	return hour*60 + minute, true
}

func trimAIProviderRecentRequests(items []aiProviderRecentRequest) []aiProviderRecentRequest {
	if len(items) <= 20 {
		return items
	}
	return items[len(items)-20:]
}

func matchesAIProviderUsage(provider aiProviderItem, usage aiProviderUsage) bool {
	if !matchesAIProviderUsageMetadata(provider, usage) {
		return false
	}
	matchedSelector := false
	if usage.APIKeyHash != nil {
		if !aiProviderHasAPIKeyHash(provider, *usage.APIKeyHash) {
			return false
		}
		matchedSelector = true
	}
	if usage.IdentityHash != nil {
		if provider.IdentityHash != *usage.IdentityHash {
			return false
		}
		matchedSelector = true
	}
	if usage.AuthIndex != nil {
		if provider.AuthIndex == nil || *usage.AuthIndex != *provider.AuthIndex {
			return false
		}
		matchedSelector = true
	}
	if matchedSelector {
		return true
	}
	if strings.TrimSpace(usage.Provider) != "" {
		return true
	}
	return usage.Name != nil && strings.TrimSpace(*usage.Name) != ""
}

func aiProviderHasAPIKeyHash(provider aiProviderItem, hash string) bool {
	if provider.APIKeyHash != nil && *provider.APIKeyHash == hash {
		return true
	}
	for _, entry := range provider.APIKeyEntries {
		if entry.APIKeyHash != nil && *entry.APIKeyHash == hash {
			return true
		}
	}
	return false
}

func matchesAIProviderUsageMetadata(provider aiProviderItem, usage aiProviderUsage) bool {
	if strings.TrimSpace(usage.Provider) != "" && !matchesAIProviderUsageProvider(provider, usage.Provider) {
		return false
	}
	if usage.Name != nil && strings.TrimSpace(*usage.Name) != "" {
		if provider.Name == nil || !aiProviderUsageLabelsEqual(*provider.Name, *usage.Name) {
			return false
		}
	}
	if usage.BaseURL != nil && strings.TrimSpace(*usage.BaseURL) != "" {
		providerBaseURL := aiProviderUsageBaseURL(provider)
		if providerBaseURL == "" || !sameAIProviderUsageBaseURL(providerBaseURL, *usage.BaseURL) {
			return false
		}
	}
	return true
}

func matchesAIProviderUsageProvider(provider aiProviderItem, usageProvider string) bool {
	if aiProviderUsageLabelsEqual(usageProvider, string(provider.Brand)) {
		return true
	}
	if provider.Name != nil && aiProviderUsageLabelsEqual(usageProvider, *provider.Name) {
		return true
	}
	for _, config := range aiProviderBrandConfigs {
		if config.Brand != provider.Brand {
			continue
		}
		return aiProviderUsageLabelsEqual(usageProvider, config.ConfigKey) || aiProviderUsageLabelsEqual(usageProvider, config.Label)
	}
	return false
}

func aiProviderUsageLabelsEqual(left, right string) bool {
	left = normalizeAIProviderUsageLabel(left)
	right = normalizeAIProviderUsageLabel(right)
	return left != "" && (left == right || strings.ReplaceAll(left, "_", "") == strings.ReplaceAll(right, "_", ""))
}

func normalizeAIProviderUsageLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func aiProviderUsageBaseURL(provider aiProviderItem) string {
	if baseURL := aiProviderOptionalString(provider.BaseURL); baseURL != "" {
		return baseURL
	}
	for _, config := range aiProviderBrandConfigs {
		if config.Brand == provider.Brand {
			return config.DefaultBaseURL
		}
	}
	return ""
}

func sameAIProviderUsageBaseURL(left, right string) bool {
	left = strings.TrimRight(strings.TrimSpace(left), "/")
	right = strings.TrimRight(strings.TrimSpace(right), "/")
	return left != "" && right != "" && strings.EqualFold(left, right)
}

func aiProviderSummaryFromItems(items []aiProviderItem) aiProviderSummary {
	summary := aiProviderSummary{Total: len(items)}
	for _, item := range items {
		switch item.Brand {
		case aiProviderBrandGemini:
			summary.Gemini++
		case aiProviderBrandCodex:
			summary.Codex++
		case aiProviderBrandClaude:
			summary.Claude++
		case aiProviderBrandOpenAICompatibility:
			summary.OpenAI++
		case aiProviderBrandVertex:
			summary.Vertex++
		}
		summary.RecentSuccess += item.RecentSuccess
		summary.RecentFailure += item.RecentFailure
	}
	return summary
}

func parseAIProviderUsage(payload []byte) ([]aiProviderUsage, bool) {
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return []aiProviderUsage{}, false
	}
	items := []aiProviderUsage{}
	ok := collectAIProviderUsage(raw, &items)
	return items, ok
}

func collectAIProviderUsage(value any, items *[]aiProviderUsage) bool {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return true
		}
		ok := false
		for _, item := range typed {
			ok = collectAIProviderUsage(item, items) || ok
		}
		return ok
	case map[string]any:
		if len(typed) == 0 {
			return true
		}
		if usage, ok := aiProviderUsageFromMap(typed); ok {
			*items = append(*items, usage)
			return true
		}
		if collectAIProviderUsageBuckets(typed, items) {
			return true
		}
		ok := false
		for _, nested := range typed {
			ok = collectAIProviderUsage(nested, items) || ok
		}
		return ok
	default:
		return false
	}
}

func collectAIProviderUsageBuckets(raw map[string]any, items *[]aiProviderUsage) bool {
	collected := false
	for provider, buckets := range raw {
		bucketMap, ok := buckets.(map[string]any)
		if !ok {
			continue
		}
		for compositeKey, counts := range bucketMap {
			countMap, ok := counts.(map[string]any)
			if !ok {
				continue
			}
			usage, ok := aiProviderUsageFromMap(countMap)
			if !ok {
				continue
			}
			if strings.TrimSpace(usage.Provider) == "" {
				usage.Provider = provider
			}
			applyAIProviderUsageCompositeKey(&usage, compositeKey)
			*items = append(*items, usage)
			collected = true
		}
	}
	return collected
}

func applyAIProviderUsageCompositeKey(usage *aiProviderUsage, compositeKey string) {
	normalized := strings.TrimSpace(compositeKey)
	if normalized == "" {
		return
	}
	apiKey := normalized
	if parts := strings.SplitN(normalized, "|", 2); len(parts) == 2 {
		baseURL := strings.TrimSpace(parts[0])
		if baseURL != "" && usage.BaseURL == nil {
			usage.BaseURL = &baseURL
		}
		apiKey = strings.TrimSpace(parts[1])
	}
	if apiKey == "" {
		return
	}
	hashed := hashAPIKey(apiKey)
	masked := maskSecret(&apiKey)
	if usage.APIKeyHash == nil {
		usage.APIKeyHash = &hashed
	}
	if usage.APIKeyMasked == nil {
		usage.APIKeyMasked = &masked
	}
}

func aiProviderUsageFromMap(raw map[string]any) (aiProviderUsage, bool) {
	usage := aiProviderUsage{}
	if value := aiProviderFirstString(raw, "provider", "brand", "type"); value != nil {
		usage.Provider = *value
	}
	if value := aiProviderFirstString(raw, "api_key_hash", "api-key-hash", "key_hash"); value != nil {
		usage.APIKeyHash = value
	} else if key := aiProviderRawAPIKey(raw); key != "" {
		hashed := hashAPIKey(key)
		masked := maskSecret(&key)
		usage.APIKeyHash = &hashed
		usage.APIKeyMasked = &masked
	}
	if value := aiProviderFirstString(raw, "api_key_masked", "api-key-masked", "key_masked"); value != nil && usage.APIKeyMasked == nil {
		usage.APIKeyMasked = value
	}
	usage.AuthIndex = aiProviderFirstString(raw, "auth_index", "auth-index")
	usage.Name = aiProviderFirstString(raw, "name")
	usage.BaseURL = aiProviderFirstString(raw, "base_url", "base-url")
	usage.LastSeen = aiProviderFirstString(raw, "last_seen", "last-seen", "updated_at")
	usage.IdentityHash = aiProviderFirstString(raw, "identity_hash", "identity-hash")
	usage.UpstreamLabel = aiProviderFirstString(raw, "label", "description")
	usage.SuccessCount, _ = aiProviderFirstIntValue(raw, "success_count", "success", "successful", "ok_count", "succeeded")
	usage.FailureCount, _ = aiProviderFirstIntValue(raw, "failure_count", "failed_count", "failed", "error_count", "errors")
	usage.TotalCount = aiProviderFirstInt(raw, "total_count", "total", "request_count", "requests")
	if requests, ok := aiProviderRecentRequestsFromAny(raw["recent_requests"]); ok {
		usage.RecentRequests = requests
		usage.RecentRequestsAvailable = true
	} else if requests, ok := aiProviderRecentRequestsFromAny(raw["recentRequests"]); ok {
		usage.RecentRequests = requests
		usage.RecentRequestsAvailable = true
	}
	if usage.RecentRequestsAvailable {
		bucketSuccess, bucketFailure := aiProviderRecentRequestCounts(usage.RecentRequests)
		if usage.SuccessCount+usage.FailureCount == 0 && bucketSuccess+bucketFailure > 0 {
			usage.SuccessCount = bucketSuccess
			usage.FailureCount = bucketFailure
		}
	}
	if usage.TotalCount == 0 {
		usage.TotalCount = usage.SuccessCount + usage.FailureCount
	}
	ok := usage.Provider != "" || usage.APIKeyHash != nil || usage.AuthIndex != nil || usage.SuccessCount > 0 || usage.FailureCount > 0 || usage.RecentRequestsAvailable
	return usage, ok
}

func aiProviderRecentRequestCounts(items []aiProviderRecentRequest) (int, int) {
	success := 0
	failure := 0
	for _, item := range items {
		success += item.Success
		failure += item.Failed
	}
	return success, failure
}

func aiProviderRecentRequestsFromAny(value any) ([]aiProviderRecentRequest, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]aiProviderRecentRequest, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		request := aiProviderRecentRequest{
			Time:    aiProviderFirstString(object, "time", "timestamp", "bucket", "bucket_time"),
			Success: aiProviderFirstInt(object, "success", "success_count", "successful", "ok_count", "succeeded"),
			Failed:  aiProviderFirstInt(object, "failed", "failure_count", "failed_count", "error_count", "errors"),
		}
		result = append(result, request)
	}
	return normalizeAIProviderRecentRequests(result), true
}

func aiProviderRawAPIKey(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if value := aiProviderFirstString(raw, "api-key", "api_key", "key"); value != nil {
		return *value
	}
	return ""
}

func aiProviderRawAPIKeyHash(raw map[string]any) *string {
	if raw == nil {
		return nil
	}
	return aiProviderFirstString(raw, "api_key_hash", "api-key-hash")
}

func aiProviderRawAPIKeyMasked(raw map[string]any) *string {
	if raw == nil {
		return nil
	}
	return aiProviderFirstString(raw, "api_key_masked", "api-key-masked")
}

func aiProviderRawKeyEntries(raw map[string]any) []map[string]any {
	if raw == nil {
		return nil
	}
	entries, _ := aiProviderListFromAny(raw["api-key-entries"], "api-key-entries")
	return entries
}

func aiProviderKeyEntriesFromAny(value any) []aiProviderKeyEntry {
	entries, err := aiProviderListFromAny(value, "api-key-entries")
	if err != nil {
		return []aiProviderKeyEntry{}
	}
	result := make([]aiProviderKeyEntry, 0, len(entries))
	for _, entry := range entries {
		apiKey := aiProviderRawAPIKey(entry)
		apiKeyHash := aiProviderRawAPIKeyHash(entry)
		apiKeyMasked := aiProviderRawAPIKeyMasked(entry)
		if apiKey != "" {
			hashed := hashAPIKey(apiKey)
			masked := maskSecret(&apiKey)
			apiKeyHash = &hashed
			apiKeyMasked = &masked
		}
		result = append(result, aiProviderKeyEntry{
			APIKeyHash:   apiKeyHash,
			APIKeyMasked: apiKeyMasked,
			ProxyURL:     aiProviderStringFromKeys(entry, "proxy-url", "proxy_url"),
		})
	}
	return result
}

func mergeAIProviderActionEntries(payloadEntries []aiProviderKeyEntry, raw map[string]any) ([]aiProviderKeyEntry, error) {
	currentEntries := aiProviderRawKeyEntries(raw)
	result := make([]aiProviderKeyEntry, 0, len(payloadEntries))
	for _, entry := range payloadEntries {
		next := entry
		if strings.TrimSpace(next.APIKey) == "" {
			currentEntry, err := findAIProviderCurrentEntry(entry, currentEntries)
			if err != nil {
				return nil, err
			}
			if preserved := aiProviderRawAPIKey(currentEntry); preserved != "" {
				next.APIKey = preserved
			}
		}
		result = append(result, next)
	}
	return result, nil
}

func hasOpenAIProviderSubmittedKey(provider aiProviderItem) bool {
	for _, entry := range provider.APIKeyEntries {
		if strings.TrimSpace(entry.APIKey) != "" {
			return true
		}
	}
	return false
}

func aiProviderModelsFromAny(value any, includeOpenAIFields bool) []aiProviderModel {
	items, err := aiProviderListFromAny(value, "models")
	if err != nil {
		return []aiProviderModel{}
	}
	models := make([]aiProviderModel, 0, len(items))
	for _, item := range items {
		name := aiProviderStringFromKeys(item, "name")
		if name == nil {
			continue
		}
		model := aiProviderModel{
			Name:         *name,
			Alias:        aiProviderOptionalString(aiProviderStringFromKeys(item, "alias")),
			ForceMapping: aiProviderBoolPtrFromKeys(item, "force-mapping", "force_mapping"),
		}
		if includeOpenAIFields {
			model.Image = aiProviderBoolPtrFromKeys(item, "image")
			model.Thinking = aiProviderObjectFromAny(item["thinking"])
		}
		models = append(models, model)
	}
	return models
}

func aiProviderModelsToUpstream(models []aiProviderModel, includeOpenAIFields bool, current map[string]any) []map[string]any {
	currentModels := aiProviderRawModels(current)
	usedCurrent := make([]bool, len(currentModels))
	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		item := map[string]any{}
		if currentModel := findAIProviderCurrentModel(name, currentModels, usedCurrent); currentModel != nil {
			item = cloneAIProviderMap(currentModel)
		}
		removeAIProviderKnownModelFields(item, includeOpenAIFields)
		item["name"] = name
		if strings.TrimSpace(model.Alias) != "" {
			item["alias"] = strings.TrimSpace(model.Alias)
		}
		if model.ForceMapping != nil {
			item["force-mapping"] = *model.ForceMapping
		}
		if includeOpenAIFields {
			if model.Image != nil {
				item["image"] = *model.Image
			}
			if model.Thinking != nil {
				item["thinking"] = model.Thinking
			}
		}
		result = append(result, item)
	}
	return result
}

func aiProviderRawModels(raw map[string]any) []map[string]any {
	models, err := aiProviderListFromAny(raw["models"], "models")
	if err != nil {
		return []map[string]any{}
	}
	return models
}

func findAIProviderCurrentModel(name string, currentModels []map[string]any, used []bool) map[string]any {
	target := normalizeAIProviderModelName(name)
	if target == "" {
		return nil
	}
	for index, current := range currentModels {
		if index < len(used) && used[index] {
			continue
		}
		currentName := aiProviderStringFromKeys(current, "name")
		if currentName == nil || normalizeAIProviderModelName(*currentName) != target {
			continue
		}
		if index < len(used) {
			used[index] = true
		}
		return current
	}
	return nil
}

func normalizeAIProviderModelName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "models/")
	name = strings.TrimPrefix(name, "publishers/google/models/")
	return name
}

func removeAIProviderKnownModelFields(item map[string]any, includeOpenAIFields bool) {
	for _, key := range []string{"alias", "force-mapping", "force_mapping"} {
		delete(item, key)
	}
	if includeOpenAIFields {
		for _, key := range []string{"image", "thinking"} {
			delete(item, key)
		}
	}
}

func aiProviderHeadersFromAny(value any) []aiProviderHeader {
	switch typed := value.(type) {
	case map[string]any:
		headers := make([]aiProviderHeader, 0, len(typed))
		for key, value := range typed {
			if text := aiProviderStringFromAny(value); text != nil {
				headers = append(headers, aiProviderHeader{Name: key, Value: *text})
			}
		}
		return headers
	case []any:
		headers := make([]aiProviderHeader, 0, len(typed))
		for _, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := aiProviderStringFromKeys(object, "name", "key")
			value := aiProviderStringFromKeys(object, "value")
			if name != nil && value != nil {
				headers = append(headers, aiProviderHeader{Name: *name, Value: *value})
			}
		}
		return headers
	default:
		return []aiProviderHeader{}
	}
}

func aiProviderHeadersToUpstream(headers []aiProviderHeader) map[string]string {
	result := map[string]string{}
	for _, header := range headers {
		name := strings.TrimSpace(header.Name)
		if name != "" {
			result[name] = header.Value
		}
	}
	return result
}

func aiProviderCloakFromAny(value any) *aiProviderCloak {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return &aiProviderCloak{
		Mode:           aiProviderStringFromKeys(raw, "mode"),
		StrictMode:     aiProviderBoolPtrFromKeys(raw, "strict-mode", "strict_mode"),
		SensitiveWords: aiProviderStringListFromAny(raw["sensitive-words"]),
		CacheUserID:    aiProviderBoolPtrFromKeys(raw, "cache-user-id", "cache_user_id"),
	}
}

func aiProviderCloakToUpstream(cloak aiProviderCloak, current map[string]any) map[string]any {
	result := map[string]any{}
	if currentCloak, ok := current["cloak"].(map[string]any); ok {
		result = cloneAIProviderMap(currentCloak)
	}
	removeAIProviderKnownCloakFields(result)
	result["sensitive-words"] = aiProviderStringListToUpstream(cloak.SensitiveWords)
	if cloak.Mode != nil {
		if mode := strings.TrimSpace(*cloak.Mode); mode != "" {
			result["mode"] = mode
		}
	}
	if cloak.StrictMode != nil {
		result["strict-mode"] = *cloak.StrictMode
	}
	if cloak.CacheUserID != nil {
		result["cache-user-id"] = *cloak.CacheUserID
	}
	return result
}

func removeAIProviderKnownCloakFields(item map[string]any) {
	for _, key := range []string{
		"mode",
		"strict-mode",
		"strict_mode",
		"sensitive-words",
		"sensitive_words",
		"cache-user-id",
		"cache_user_id",
	} {
		delete(item, key)
	}
}

func aiProviderUsesExcludedModelsDisabled(brand aiProviderBrand) bool {
	return brand != aiProviderBrandOpenAICompatibility
}

func aiProviderExcludedModelsFromRaw(raw map[string]any, useDisableSentinel bool) ([]string, bool) {
	if raw == nil {
		return []string{}, false
	}
	items := aiProviderStringListFromAny(raw["excluded-models"])
	result := make([]string, 0, len(items))
	disabled := false
	for _, item := range items {
		normalized := strings.TrimSpace(item)
		if normalized == "" {
			continue
		}
		if useDisableSentinel && normalized == aiProviderDisableAllModelsRule {
			disabled = true
			continue
		}
		result = append(result, normalized)
	}
	return result, disabled
}

func aiProviderExcludedModelsWithDisabled(items []string, disabled *bool, current map[string]any) []string {
	next := make([]string, 0, len(items)+1)
	for _, item := range items {
		normalized := strings.TrimSpace(item)
		if normalized != "" && normalized != aiProviderDisableAllModelsRule {
			next = append(next, normalized)
		}
	}
	isDisabled := false
	if disabled != nil {
		isDisabled = *disabled
	} else if current != nil {
		isDisabled = aiProviderNonOpenAIDisabledFromRaw(current)
	}
	if isDisabled {
		next = append(next, aiProviderDisableAllModelsRule)
	}
	return next
}

func aiProviderNonOpenAIDisabledFromRaw(raw map[string]any) bool {
	if raw == nil {
		return false
	}
	_, disabledByExcludedModels := aiProviderExcludedModelsFromRaw(raw, true)
	if disabledByExcludedModels {
		return true
	}
	disabled := aiProviderBoolPtrFromKeys(raw, "disabled")
	return disabled != nil && *disabled
}

func aiProviderStringListFromAny(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := aiProviderStringFromAny(item); text != nil {
			result = append(result, *text)
		}
	}
	return result
}

func aiProviderStringListToUpstream(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		normalized := strings.TrimSpace(item)
		if normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func aiProviderObjectFromAny(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return cloneAIProviderMap(object)
}

func aiProviderStringFromKeys(raw map[string]any, keys ...string) *string {
	return aiProviderFirstString(raw, keys...)
}

func aiProviderFirstString(raw map[string]any, keys ...string) *string {
	for _, key := range keys {
		if value := aiProviderStringFromAny(raw[key]); value != nil {
			return value
		}
	}
	return nil
}

func aiProviderStringFromAny(value any) *string {
	if text := stringValue(value); text != nil {
		return text
	}
	return nil
}

func aiProviderOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func aiProviderIntPtrFromKeys(raw map[string]any, keys ...string) *int {
	for _, key := range keys {
		if value, ok := aiProviderIntFromAny(raw[key]); ok {
			return &value
		}
	}
	return nil
}

func aiProviderFirstInt(raw map[string]any, keys ...string) int {
	value, _ := aiProviderFirstIntValue(raw, keys...)
	return value
}

func aiProviderFirstIntValue(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		if value, ok := aiProviderIntFromAny(raw[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func aiProviderIntFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func aiProviderBoolPtrFromKeys(raw map[string]any, keys ...string) *bool {
	for _, key := range keys {
		if value, ok := aiProviderBoolFromAny(raw[key]); ok {
			return &value
		}
	}
	return nil
}

func aiProviderBoolFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func setOptionalString(target map[string]any, key string, value *string) {
	setOptionalStringField(target, key, value, value != nil)
}

func setOptionalStringField(target map[string]any, key string, value *string, present bool) {
	if !present {
		return
	}
	if value == nil {
		delete(target, key)
		return
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		delete(target, key)
		return
	}
	target[key] = normalized
}

func cloneAIProviderMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneAIProviderValue(value)
	}
	return result
}

func cloneAIProviderValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAIProviderMap(typed)
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, cloneAIProviderValue(item))
		}
		return items
	default:
		return typed
	}
}

func removeAIProviderResponseOnlyFields(raw map[string]any) {
	for _, key := range []string{
		"auth-index", "auth_index", "api_key_hash", "api-key-hash", "api_key_masked", "api-key-masked",
		"identity_hash", "identity-hash", "brand", "brand_label", "index", "recent_success", "recent_failure", "recent_status",
		"recent_status_available", "recent_requests", "recent-requests",
	} {
		delete(raw, key)
	}
	if entries, ok := raw["api-key-entries"].([]any); ok {
		for _, entry := range entries {
			if object, ok := entry.(map[string]any); ok {
				removeAIProviderResponseOnlyFields(object)
			}
		}
	}
}
