package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	modelMonitorSourceTypeBuiltIn = "built_in"

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
}

func (a *App) handleModelMonitor(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	proxyCfg, err := a.loadModelMonitorProxyConfig(r.Context())
	if err != nil {
		return err
	}
	client, err := a.modelMonitorHTTPClient(proxyCfg)
	if err != nil {
		return err
	}

	sources := modelMonitorBuiltInSources
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
