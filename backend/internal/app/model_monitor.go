package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	modelMonitorSourceTypeBuiltIn = "built_in"

	modelMonitorDeepSeekCollectionBaseURL = "https://statuspage.flashcat.cloud"
	modelMonitorDeepSeekCollectionHost    = "status.deepseek.com"

	modelMonitorCollectionOK    = "ok"
	modelMonitorCollectionError = "error"

	modelMonitorHistoryDay    = "day"
	modelMonitorHistoryMinute = "minute"
)

type modelMonitorSourceDefinition struct {
	ID                 string
	Name               string
	SourceType         string
	BaseURL            string
	CollectionBaseURL  string
	Adapter            string
	HistoryGranularity string
	HistoryWindowLabel string
}

type ModelMonitorResponse struct {
	Sources []ModelMonitorSourceStatus `json:"sources"`
}

type ModelMonitorProxyConfig struct {
	Enabled  bool   `json:"enabled"`
	ProxyURL string `json:"proxy_url"`
}

type modelMonitorProxySettingsPayload struct {
	Enabled  *bool   `json:"enabled"`
	ProxyURL *string `json:"proxy_url"`
}

type ModelMonitorSourceSettings struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	SourceType         string `json:"source_type"`
	StatusPageURL      string `json:"status_page_url"`
	HistoryGranularity string `json:"history_granularity"`
	HistoryWindowLabel string `json:"history_window_label"`
	Enabled            bool   `json:"enabled"`
}

type ModelMonitorSettings struct {
	Sources          []ModelMonitorSourceSettings `json:"sources"`
	EnabledSourceIDs []string                     `json:"enabled_source_ids"`
}

type modelMonitorSourceSettingsPayload struct {
	EnabledSourceIDs *[]string `json:"enabled_source_ids"`
}

type ModelMonitorSourceStatus struct {
	ID                 string                      `json:"id"`
	Name               string                      `json:"name"`
	SourceType         string                      `json:"source_type"`
	StatusPageURL      string                      `json:"status_page_url"`
	CollectionState    string                      `json:"collection_state"`
	CollectionError    *string                     `json:"collection_error"`
	OverallStatus      string                      `json:"overall_status"`
	SourceUpdatedAt    *time.Time                  `json:"source_updated_at"`
	CheckedAt          time.Time                   `json:"checked_at"`
	LastSuccessAt      *time.Time                  `json:"last_success_at"`
	HistoryGranularity string                      `json:"history_granularity"`
	HistoryWindowLabel string                      `json:"history_window_label"`
	Groups             []ModelMonitorServiceGroup  `json:"groups"`
	Services           []ModelMonitorServiceStatus `json:"services"`
	Incidents          []ModelMonitorIncident      `json:"incidents"`
}

type ModelMonitorServiceGroup struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Status        string                      `json:"status"`
	UptimePercent *float64                    `json:"uptime_percent"`
	Samples       []ModelMonitorSample        `json:"samples"`
	Services      []ModelMonitorServiceStatus `json:"services"`
}

type ModelMonitorServiceStatus struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	UptimePercent *float64             `json:"uptime_percent"`
	LastStatus    *string              `json:"last_status"`
	LastLatencyMS *float64             `json:"last_latency_ms"`
	LastError     *string              `json:"last_error"`
	Samples       []ModelMonitorSample `json:"samples"`
}

type ModelMonitorSample struct {
	Timestamp        time.Time `json:"timestamp"`
	Status           string    `json:"status"`
	LatencyMS        *float64  `json:"latency_ms"`
	Error            *string   `json:"error"`
	RelatedIncidents []string  `json:"related_incidents"`
}

type ModelMonitorIncident struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Impact    string     `json:"impact"`
	URL       *string    `json:"url"`
	UpdatedAt *time.Time `json:"updated_at"`
}

var modelMonitorBuiltInSources = []modelMonitorSourceDefinition{
	{
		ID: "ai-input-im", Name: "AI.INPUT.IM", SourceType: modelMonitorSourceTypeBuiltIn,
		BaseURL: "https://status.input.im", Adapter: "input_im",
		HistoryGranularity: modelMonitorHistoryMinute, HistoryWindowLabel: "60 minutes",
	},
	{
		ID: "openai", Name: "OpenAI", SourceType: modelMonitorSourceTypeBuiltIn,
		BaseURL: "https://status.openai.com", Adapter: "openai",
		HistoryGranularity: modelMonitorHistoryDay, HistoryWindowLabel: "90 days",
	},
	{
		ID: "anthropic", Name: "Claude Status", SourceType: modelMonitorSourceTypeBuiltIn,
		BaseURL: "https://status.claude.com", Adapter: "anthropic",
		HistoryGranularity: modelMonitorHistoryDay, HistoryWindowLabel: "90 days",
	},
	{
		ID: "deepseek", Name: "DeepSeek", SourceType: modelMonitorSourceTypeBuiltIn,
		BaseURL: "https://status.deepseek.com", CollectionBaseURL: modelMonitorDeepSeekCollectionBaseURL, Adapter: "deepseek",
		HistoryGranularity: modelMonitorHistoryDay, HistoryWindowLabel: "90 days",
	},
}

func (a *App) handleModelMonitor(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	settings, err := a.loadModelMonitorSourceSettings(r.Context())
	if err != nil {
		return err
	}
	sources := modelMonitorSourceDefinitionsForIDs(settings.EnabledSourceIDs)
	if len(sources) == 0 {
		writeJSON(w, http.StatusOK, ModelMonitorResponse{Sources: []ModelMonitorSourceStatus{}})
		return nil
	}
	proxyCfg, err := a.loadModelMonitorProxyConfig(r.Context())
	if err != nil {
		return err
	}
	client, err := a.modelMonitorHTTPClient(proxyCfg)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()

	statuses := make([]ModelMonitorSourceStatus, len(sources))
	var wait sync.WaitGroup
	for index := range sources {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			statuses[index] = a.collectModelMonitorSource(r.Context(), client, sources[index])
		}()
	}
	wait.Wait()
	writeJSON(w, http.StatusOK, ModelMonitorResponse{Sources: statuses})
	return nil
}

func (a *App) handleModelMonitorSettings(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := a.loadModelMonitorSourceSettings(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, settings)
		return nil
	case http.MethodPut:
		var payload modelMonitorSourceSettingsPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		settings, err := a.updateModelMonitorSourceSettings(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, settings)
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) handleModelMonitorSource(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	settings, err := a.loadModelMonitorSourceSettings(r.Context())
	if err != nil {
		return err
	}
	sourceID := r.PathValue("id")
	source, found := modelMonitorSourceDefinitionByID(sourceID)
	if !found || !modelMonitorSourceIsEnabled(settings.EnabledSourceIDs, sourceID) {
		return notFoundError("模型监控来源不存在")
	}
	proxyCfg, err := a.loadModelMonitorProxyConfig(r.Context())
	if err != nil {
		return err
	}
	client, err := a.modelMonitorHTTPClient(proxyCfg)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	writeJSON(w, http.StatusOK, a.collectModelMonitorSource(r.Context(), client, source))
	return nil
}

func (a *App) handleModelMonitorProxy(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		proxyCfg, err := a.loadModelMonitorProxyConfig(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, proxyCfg)
		return nil
	case http.MethodPut:
		var payload modelMonitorProxySettingsPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		proxyCfg, err := a.updateModelMonitorProxyConfig(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, proxyCfg)
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) loadModelMonitorProxyConfig(ctx context.Context) (ModelMonitorProxyConfig, error) {
	var cfg ModelMonitorProxyConfig
	if err := a.db.QueryRowContext(ctx, `
		SELECT model_monitor_proxy_enabled, model_monitor_proxy_url
		FROM app_settings WHERE id = 1
	`).Scan(&cfg.Enabled, &cfg.ProxyURL); err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	return normalizeModelMonitorProxyConfig(cfg)
}

func (a *App) updateModelMonitorProxyConfig(ctx context.Context, payload modelMonitorProxySettingsPayload) (ModelMonitorProxyConfig, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var next ModelMonitorProxyConfig
	if err := tx.QueryRowContext(ctx, `
		SELECT model_monitor_proxy_enabled, model_monitor_proxy_url
		FROM app_settings WHERE id = 1
	`).Scan(&next.Enabled, &next.ProxyURL); err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	if payload.Enabled != nil {
		next.Enabled = *payload.Enabled
	}
	if payload.ProxyURL != nil {
		next.ProxyURL = *payload.ProxyURL
	}
	normalized, err := normalizeModelMonitorProxyConfig(next)
	if err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE app_settings
		SET model_monitor_proxy_enabled = ?, model_monitor_proxy_url = ?, updated_at = ?
		WHERE id = 1
	`, normalized.Enabled, normalized.ProxyURL, dbTime(time.Now())); err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelMonitorProxyConfig{}, err
	}
	return normalized, nil
}

func (a *App) loadModelMonitorSourceSettings(ctx context.Context) (ModelMonitorSettings, error) {
	var encoded string
	if err := a.db.QueryRowContext(ctx, `
		SELECT model_monitor_enabled_source_ids
		FROM app_settings WHERE id = 1
	`).Scan(&encoded); err != nil {
		return ModelMonitorSettings{}, err
	}
	enabledSourceIDs, err := parseModelMonitorEnabledSourceIDs(encoded)
	if err != nil {
		return ModelMonitorSettings{}, validationError(fmt.Sprintf("模型监控来源配置无效: %v", err))
	}
	return modelMonitorSettingsForEnabledSourceIDs(enabledSourceIDs), nil
}

func (a *App) updateModelMonitorSourceSettings(ctx context.Context, payload modelMonitorSourceSettingsPayload) (ModelMonitorSettings, error) {
	if payload.EnabledSourceIDs == nil {
		return ModelMonitorSettings{}, validationError("模型监控来源设置缺少 enabled_source_ids 字段")
	}
	enabledSourceIDs := append([]string{}, (*payload.EnabledSourceIDs)...)
	if err := validateModelMonitorEnabledSourceIDs(enabledSourceIDs); err != nil {
		return ModelMonitorSettings{}, validationError(err.Error())
	}
	encoded, err := json.Marshal(enabledSourceIDs)
	if err != nil {
		return ModelMonitorSettings{}, err
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelMonitorSettings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE app_settings
		SET model_monitor_enabled_source_ids = ?, updated_at = ?
		WHERE id = 1
	`, string(encoded), dbTime(time.Now()))
	if err != nil {
		return ModelMonitorSettings{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ModelMonitorSettings{}, err
	}
	if rowsAffected != 1 {
		return ModelMonitorSettings{}, errors.New("app_settings id=1 is missing")
	}
	if err := tx.Commit(); err != nil {
		return ModelMonitorSettings{}, err
	}
	return modelMonitorSettingsForEnabledSourceIDs(enabledSourceIDs), nil
}

func parseModelMonitorEnabledSourceIDs(encoded string) ([]string, error) {
	var sourceIDs []string
	if err := json.Unmarshal([]byte(encoded), &sourceIDs); err != nil || sourceIDs == nil {
		return nil, errors.New("enabled_source_ids 必须是 JSON 字符串数组")
	}
	if err := validateModelMonitorEnabledSourceIDs(sourceIDs); err != nil {
		return nil, err
	}
	return sourceIDs, nil
}

func validateModelMonitorEnabledSourceIDs(sourceIDs []string) error {
	seen := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if strings.TrimSpace(sourceID) == "" {
			return errors.New("模型监控来源 ID 不能为空")
		}
		if _, found := modelMonitorSourceDefinitionByID(sourceID); !found {
			return fmt.Errorf("模型监控来源 ID %q 不受支持", sourceID)
		}
		if _, exists := seen[sourceID]; exists {
			return fmt.Errorf("模型监控来源 ID %q 重复", sourceID)
		}
		seen[sourceID] = struct{}{}
	}
	return nil
}

func modelMonitorSettingsForEnabledSourceIDs(enabledSourceIDs []string) ModelMonitorSettings {
	enabled := make(map[string]struct{}, len(enabledSourceIDs))
	sources := make([]ModelMonitorSourceSettings, 0, len(modelMonitorBuiltInSources))
	for _, sourceID := range enabledSourceIDs {
		source, _ := modelMonitorSourceDefinitionByID(sourceID)
		sources = append(sources, modelMonitorSourceSettingsFromDefinition(source, true))
		enabled[sourceID] = struct{}{}
	}
	for _, source := range modelMonitorBuiltInSources {
		if _, isEnabled := enabled[source.ID]; !isEnabled {
			sources = append(sources, modelMonitorSourceSettingsFromDefinition(source, false))
		}
	}
	return ModelMonitorSettings{
		Sources:          sources,
		EnabledSourceIDs: append([]string{}, enabledSourceIDs...),
	}
}

func modelMonitorSourceSettingsFromDefinition(source modelMonitorSourceDefinition, enabled bool) ModelMonitorSourceSettings {
	return ModelMonitorSourceSettings{
		ID: source.ID, Name: source.Name, SourceType: source.SourceType,
		StatusPageURL: source.BaseURL, HistoryGranularity: source.HistoryGranularity,
		HistoryWindowLabel: source.HistoryWindowLabel, Enabled: enabled,
	}
}

func modelMonitorSourceDefinitionByID(sourceID string) (modelMonitorSourceDefinition, bool) {
	for _, source := range modelMonitorBuiltInSources {
		if source.ID == sourceID {
			return source, true
		}
	}
	return modelMonitorSourceDefinition{}, false
}

func (source modelMonitorSourceDefinition) collectionBaseURL() string {
	if source.CollectionBaseURL != "" {
		return source.CollectionBaseURL
	}
	return source.BaseURL
}

func modelMonitorSourceDefinitionsForIDs(sourceIDs []string) []modelMonitorSourceDefinition {
	sources := make([]modelMonitorSourceDefinition, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		source, _ := modelMonitorSourceDefinitionByID(sourceID)
		sources = append(sources, source)
	}
	return sources
}

func modelMonitorSourceIsEnabled(enabledSourceIDs []string, sourceID string) bool {
	for _, enabledSourceID := range enabledSourceIDs {
		if enabledSourceID == sourceID {
			return true
		}
	}
	return false
}

func (a *App) collectModelMonitorSource(ctx context.Context, client *http.Client, source modelMonitorSourceDefinition) ModelMonitorSourceStatus {
	checkedAt := time.Now().UTC()
	status := ModelMonitorSourceStatus{
		ID: source.ID, Name: source.Name, SourceType: source.SourceType,
		StatusPageURL: source.BaseURL, CollectionState: modelMonitorCollectionError,
		OverallStatus: "unknown", CheckedAt: checkedAt,
		HistoryGranularity: source.HistoryGranularity,
		HistoryWindowLabel: source.HistoryWindowLabel,
		Groups:             []ModelMonitorServiceGroup{},
		Services:           []ModelMonitorServiceStatus{}, Incidents: []ModelMonitorIncident{},
	}
	var collected ModelMonitorSourceStatus
	var err error
	switch source.Adapter {
	case "openai":
		collected, err = collectOpenAIStatus(ctx, client, source.BaseURL)
	case "input_im":
		collected, err = collectInputIMStatus(ctx, client, source.BaseURL)
	case "anthropic":
		collected, err = collectAnthropicStatus(ctx, client, source.BaseURL)
	case "deepseek":
		collected, err = collectDeepSeekStatus(ctx, client, source.collectionBaseURL())
	default:
		err = fmt.Errorf("unknown model monitor adapter %q", source.Adapter)
	}
	if err != nil {
		message := err.Error()
		status.CollectionError = &message
		return status
	}
	collected.ID = source.ID
	collected.Name = source.Name
	collected.SourceType = source.SourceType
	collected.StatusPageURL = source.BaseURL
	collected.CollectionState = modelMonitorCollectionOK
	collected.CollectionError = nil
	collected.CheckedAt = checkedAt
	collected.LastSuccessAt = &checkedAt
	collected.HistoryGranularity = source.HistoryGranularity
	collected.HistoryWindowLabel = source.HistoryWindowLabel
	if collected.Groups == nil {
		collected.Groups = []ModelMonitorServiceGroup{}
	}
	if collected.Services == nil {
		collected.Services = []ModelMonitorServiceStatus{}
	}
	if collected.Incidents == nil {
		collected.Incidents = []ModelMonitorIncident{}
	}
	return collected
}
