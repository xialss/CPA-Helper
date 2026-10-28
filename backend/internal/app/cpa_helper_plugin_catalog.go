package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	pluginModelCatalogVersion = "0.1.8"
	pluginModelCatalogCPA     = "v8.0.32"
	pluginModelCatalogMaxRev  = uint64(9007199254740991)
)

type pluginModelCatalogCredential struct {
	Ref      string   `json:"ref"`
	Source   string   `json:"source"`
	Provider string   `json:"provider"`
	Enabled  bool     `json:"enabled"`
	Models   []string `json:"models"`
}

type pluginModelCatalogSnapshot struct {
	ContractVersion string                         `json:"contract_version"`
	PluginVersion   string                         `json:"plugin_version"`
	CPAVersion      string                         `json:"cpa_version"`
	Revision        uint64                         `json:"catalog_revision"`
	GeneratedAt     time.Time                      `json:"generated_at"`
	Credentials     []pluginModelCatalogCredential `json:"credentials"`
}

type pluginCatalogSyncResult struct {
	Config      map[string]any
	Credentials []pluginHostCredential
	Snapshot    pluginModelCatalogSnapshot
}

type pluginCatalogFlight struct {
	done   chan struct{}
	result pluginCatalogSyncResult
	err    error
}

type pluginCatalogRecovery struct {
	attempted bool
	err       error
}

func pluginSupportsModelCatalog(capabilities map[string]json.RawMessage) bool {
	target := pluginCatalogCPATarget(rawString(capabilities["plugin_version"]))
	return target != "" && rawString(capabilities["cpa_version"]) == target
}

func pluginCatalogCPATarget(version string) string {
	switch version {
	case "0.1.8":
		return pluginModelCatalogCPA
	case "0.1.9", "0.1.10":
		return "v8.0.45"
	default:
		return ""
	}
}

func pluginCatalogProducerCompatible(version, producer string) bool {
	switch version {
	case "0.1.8":
		return producer == "0.1.8"
	case "0.1.9":
		return producer == "0.1.8" || producer == "0.1.9"
	case "0.1.10":
		return producer == "0.1.8" || producer == "0.1.9" || producer == "0.1.10"
	default:
		return false
	}
}

func pluginModelCatalogError(code string) error {
	switch code {
	case "model_catalog_out_of_sync":
		return appError(code, http.StatusServiceUnavailable, "CPA Helper Plugin 模型目录已失效，请重新同步")
	default:
		return appError("model_catalog_unavailable", http.StatusServiceUnavailable, "CPA Helper Plugin 模型目录不可用，请同步目录")
	}
}

func pluginCatalogSyncFailed(message string) error {
	return appError("model_catalog_sync_failed", http.StatusServiceUnavailable, message)
}

// Coalesce only in-flight work for the same captured management configuration.
// Every later explicit refresh starts a new transaction; there is no TTL cache.
func (a *App) syncPluginModelCatalog(ctx context.Context, cfg AppConfig, capabilities map[string]json.RawMessage) (pluginCatalogSyncResult, error) {
	if !pluginSupportsModelCatalog(capabilities) {
		return pluginCatalogSyncResult{}, pluginIncompatibleError()
	}
	key := strings.TrimRight(strings.TrimSpace(cfg.Collector.CLIProxyURL), "/") + "\x00" + strings.TrimSpace(cfg.Collector.ManagementKey) + "\x00" + rawString(capabilities["plugin_version"])
	a.pluginCatalogMu.Lock()
	if flight := a.pluginCatalogFlights[key]; flight != nil {
		a.pluginCatalogMu.Unlock()
		select {
		case <-ctx.Done():
			return pluginCatalogSyncResult{}, ctx.Err()
		case <-flight.done:
			return flight.result, flight.err
		}
	}
	flight := &pluginCatalogFlight{done: make(chan struct{})}
	if a.pluginCatalogFlights == nil {
		a.pluginCatalogFlights = make(map[string]*pluginCatalogFlight)
	}
	a.pluginCatalogFlights[key] = flight
	a.pluginCatalogMu.Unlock()
	// The shared transaction must outlive the caller that happened to become
	// the leader. Each caller still observes its own cancellation while the
	// in-flight work keeps running for all other waiters.
	go func() {
		result, err := a.performPluginCatalogSync(context.WithoutCancel(ctx), cfg, capabilities)
		a.pluginCatalogMu.Lock()
		flight.result, flight.err = result, err
		delete(a.pluginCatalogFlights, key)
		close(flight.done)
		a.pluginCatalogMu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return pluginCatalogSyncResult{}, ctx.Err()
	case <-flight.done:
		return flight.result, flight.err
	}
}

func (a *App) pluginV8HostDirectory(ctx context.Context, cfg AppConfig) (map[string]any, []pluginHostCredential, error) {
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, "/v8/management/config", nil, nil)
	if err != nil {
		return nil, nil, appErrorFromPlugin(err, status)
	}
	config, err := pluginDecodeNumberObject(payload)
	if err != nil {
		return nil, nil, pluginIncompatibleError()
	}
	status, _, payload, err = a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, "/v8/management/credentials", nil, nil)
	if err != nil {
		return nil, nil, appErrorFromPlugin(err, status)
	}
	files, err := pluginDirectoryObjects(payload, []string{"files"})
	if err != nil {
		return nil, nil, pluginIncompatibleError()
	}
	credentials, err := pluginV8HostCredentials(config, files)
	return config, credentials, err
}

func (a *App) pluginCatalogState(ctx context.Context, cfg AppConfig, capabilities map[string]json.RawMessage) (uint64, *pluginModelCatalogSnapshot, error) {
	status, headers, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/model-catalog", nil, nil)
	if err != nil {
		return 0, nil, appErrorFromPlugin(err, status)
	}
	object, err := pluginStrictCatalogObject(payload, []string{"snapshot", "health"}, true)
	if err != nil {
		return 0, nil, err
	}
	health, err := decodePluginObject(object["health"])
	if err != nil {
		return 0, nil, pluginIncompatibleError()
	}
	revision, err := pluginCatalogRevision(health["catalog_revision"], true)
	if err != nil || strings.TrimSpace(headers.Get("ETag")) != fmt.Sprintf("\"%d\"", revision) {
		return 0, nil, pluginIncompatibleError()
	}
	if rawJSONNull(object["snapshot"]) {
		if revision != 0 || rawString(health["status"]) != "unavailable" {
			return 0, nil, pluginModelCatalogError("model_catalog_unavailable")
		}
		return 0, nil, nil
	}
	snapshot, err := decodePluginCatalogSnapshot(object["snapshot"])
	if err != nil {
		return 0, nil, err
	}
	if !pluginCatalogProducerCompatible(rawString(capabilities["plugin_version"]), snapshot.PluginVersion) {
		return 0, nil, pluginIncompatibleError()
	}
	if snapshot.Revision != revision || rawString(health["status"]) != "ok" {
		return 0, nil, pluginModelCatalogError("model_catalog_unavailable")
	}
	return revision, &snapshot, nil
}

func (a *App) performPluginCatalogSync(ctx context.Context, cfg AppConfig, capabilities map[string]json.RawMessage) (pluginCatalogSyncResult, error) {
	config, credentials, err := a.pluginV8HostDirectory(ctx, cfg)
	if err != nil {
		return pluginCatalogSyncResult{}, err
	}
	version := rawString(capabilities["plugin_version"])
	var registered []pluginRegisteredCredentialModels
	if version != pluginModelCatalogVersion {
		registered, err = a.pluginRegisteredCredentialModels(ctx, cfg)
		if err != nil {
			return pluginCatalogSyncResult{}, err
		}
	}
	base, _, err := a.pluginCatalogState(ctx, cfg, capabilities)
	if err != nil {
		return pluginCatalogSyncResult{}, err
	}
	if base == pluginModelCatalogMaxRev {
		return pluginCatalogSyncResult{}, pluginIncompatibleError()
	}
	snapshot := pluginModelCatalogSnapshot{
		ContractVersion: "v1", PluginVersion: version, CPAVersion: pluginCatalogCPATarget(version),
		Revision: base + 1, GeneratedAt: time.Now().UTC(), Credentials: make([]pluginModelCatalogCredential, 0, len(credentials)),
	}
	if version != pluginModelCatalogVersion {
		snapshot.Credentials, credentials, err = pluginJoinRegisteredCredentialModels(credentials, registered)
		if err != nil {
			return pluginCatalogSyncResult{}, err
		}
	} else {
		for _, credential := range credentials {
			query := url.Values{"name": []string{credential.AuthID}}
			status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, "/v8/management/credentials/models?"+query.Encode(), nil, nil)
			if err != nil {
				return pluginCatalogSyncResult{}, appErrorFromPlugin(err, status)
			}
			models, err := pluginRegisteredModels(payload)
			if err != nil {
				return pluginCatalogSyncResult{}, err
			}
			snapshot.Credentials = append(snapshot.Credentials, pluginModelCatalogCredential{
				Ref: credential.Ref, Source: credential.Source, Provider: credential.Provider,
				Enabled: credential.Status != "disabled", Models: models,
			})
		}
	}
	// Validate the complete storage projection before any write. Browser labels,
	// auth IDs, auth indexes and raw configuration are never part of this body.
	body, err := json.Marshal(snapshot)
	if err != nil {
		return pluginCatalogSyncResult{}, err
	}
	if _, err := decodePluginCatalogSnapshot(body); err != nil {
		return pluginCatalogSyncResult{}, err
	}
	latestConfig, latestCredentials, err := a.pluginV8HostDirectory(ctx, cfg)
	if err != nil {
		return pluginCatalogSyncResult{}, err
	}
	if !reflect.DeepEqual(config, latestConfig) || !pluginHostIdentityEqual(credentials, latestCredentials) {
		return pluginCatalogSyncResult{}, appError("model_catalog_out_of_sync", http.StatusConflict, "同步期间 CPA 配置或凭据已变化，请刷新后重试")
	}
	if version != pluginModelCatalogVersion {
		latestRegistered, err := a.pluginRegisteredCredentialModels(ctx, cfg)
		if err != nil {
			return pluginCatalogSyncResult{}, err
		}
		if !reflect.DeepEqual(registered, latestRegistered) {
			return pluginCatalogSyncResult{}, appError("model_catalog_out_of_sync", http.StatusConflict, "同步期间 CPA 凭据模型注册已变化，请刷新后重试")
		}
	}
	headers := http.Header{}
	headers.Set("If-Match", fmt.Sprintf("\"%d\"", base))
	headers.Set("Idempotency-Key", newIdempotencyKey())
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodPut, cpaHelperPluginManagementPrefix+"/model-catalog", body, headers)
	if err != nil {
		if status == http.StatusConflict {
			return pluginCatalogSyncResult{}, conflictError("CPA Helper Plugin 模型目录已被其他管理端修改，请刷新后重试")
		}
		return pluginCatalogSyncResult{}, appErrorFromPlugin(err, status)
	}
	accepted, err := pluginStrictCatalogObject(payload, []string{"catalog_revision"}, false)
	if err != nil || rawUint(accepted["catalog_revision"]) != snapshot.Revision {
		return pluginCatalogSyncResult{}, pluginCatalogSyncFailed("CPA Helper Plugin 模型目录已提交，但未确认目标修订号")
	}
	status, _, payload, err = a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
	if err != nil {
		return pluginCatalogSyncResult{}, appErrorFromPlugin(err, status)
	}
	health, err := decodePluginObject(payload)
	if err != nil {
		return pluginCatalogSyncResult{}, pluginIncompatibleError()
	}
	modelHealth, err := decodePluginObject(health["model_catalog"])
	if err != nil || rawString(modelHealth["status"]) != "ok" || rawUint(modelHealth["catalog_revision"]) != snapshot.Revision {
		return pluginCatalogSyncResult{}, pluginCatalogSyncFailed("CPA Helper Plugin 模型目录已提交，但未确认生效")
	}
	_, confirmed, err := a.pluginCatalogState(ctx, cfg, capabilities)
	if err != nil {
		return pluginCatalogSyncResult{}, err
	}
	if confirmed == nil || !reflect.DeepEqual(*confirmed, snapshot) {
		return pluginCatalogSyncResult{}, pluginCatalogSyncFailed("CPA Helper Plugin 模型目录已提交，但未确认生效")
	}
	return pluginCatalogSyncResult{Config: config, Credentials: credentials, Snapshot: snapshot}, nil
}

func pluginHostIdentityEqual(left, right []pluginHostCredential) bool {
	if len(left) != len(right) {
		return false
	}
	for index, a := range left {
		b := right[index]
		if a.Ref != b.Ref || a.AuthID != b.AuthID || a.AuthIndex != b.AuthIndex || a.Source != b.Source || a.Provider != b.Provider || (a.Status == "disabled") != (b.Status == "disabled") {
			return false
		}
	}
	return true
}

func pluginValidCatalogText(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func pluginCatalogRevision(raw json.RawMessage, allowZero bool) (uint64, error) {
	var revision uint64
	if len(raw) == 0 || rawJSONNull(raw) || json.Unmarshal(raw, &revision) != nil || revision > pluginModelCatalogMaxRev || (!allowZero && revision == 0) {
		return 0, pluginIncompatibleError()
	}
	return revision, nil
}

// Catalog snapshots have a strict contract, unlike extensible policy maps.
// Decode exact field names and reject duplicates, case aliases and nulls.
func pluginStrictCatalogObject(raw []byte, fields []string, nullable bool) (map[string]json.RawMessage, error) {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, pluginIncompatibleError()
	}
	object := make(map[string]json.RawMessage, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || object[key] != nil {
			return nil, pluginIncompatibleError()
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || (!nullable && rawJSONNull(value)) {
			return nil, pluginIncompatibleError()
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, pluginIncompatibleError()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(object) != len(fields) {
		return nil, pluginIncompatibleError()
	}
	return object, nil
}

func decodePluginCatalogSnapshot(raw []byte) (pluginModelCatalogSnapshot, error) {
	object, err := pluginStrictCatalogObject(raw, []string{"contract_version", "plugin_version", "cpa_version", "catalog_revision", "generated_at", "credentials"}, false)
	if err != nil {
		return pluginModelCatalogSnapshot{}, err
	}
	var snapshot pluginModelCatalogSnapshot
	if json.Unmarshal(mapToRawObject(object), &snapshot) != nil || snapshot.ContractVersion != "v1" || pluginCatalogCPATarget(snapshot.PluginVersion) == "" || snapshot.CPAVersion != pluginCatalogCPATarget(snapshot.PluginVersion) || snapshot.GeneratedAt.IsZero() {
		return pluginModelCatalogSnapshot{}, pluginIncompatibleError()
	}
	if _, err := pluginCatalogRevision(object["catalog_revision"], false); err != nil {
		return pluginModelCatalogSnapshot{}, err
	}
	var entries []json.RawMessage
	if json.Unmarshal(object["credentials"], &entries) != nil || entries == nil {
		return pluginModelCatalogSnapshot{}, pluginIncompatibleError()
	}
	refs := make(map[string]bool, len(entries))
	for index, rawEntry := range entries {
		if _, err := pluginStrictCatalogObject(rawEntry, []string{"ref", "source", "provider", "enabled", "models"}, false); err != nil {
			return pluginModelCatalogSnapshot{}, err
		}
		entry := snapshot.Credentials[index]
		hash := strings.TrimPrefix(entry.Ref, "sha256:")
		_, hashErr := hex.DecodeString(hash)
		if !strings.HasPrefix(entry.Ref, "sha256:") || len(hash) != 64 || hashErr != nil || strings.ToLower(hash) != hash || refs[entry.Ref] || (entry.Source != "auth-files" && entry.Source != "ai-providers") || !pluginValidCatalogText(entry.Provider) || strings.ToLower(entry.Provider) != entry.Provider || entry.Models == nil {
			return pluginModelCatalogSnapshot{}, pluginIncompatibleError()
		}
		refs[entry.Ref] = true
		seenModels := make(map[string]bool, len(entry.Models))
		for _, model := range entry.Models {
			key := strings.ToLower(model)
			if !pluginValidCatalogText(model) || seenModels[key] {
				return pluginModelCatalogSnapshot{}, pluginIncompatibleError()
			}
			seenModels[key] = true
		}
	}
	return snapshot, nil
}

func pluginRegisteredModels(raw []byte) ([]string, error) {
	object, err := decodePluginObject(raw)
	if err != nil || rawJSONNull(object["models"]) {
		return nil, pluginCatalogSyncFailed("CPA 凭据模型目录格式错误")
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(object["models"], &entries) != nil || entries == nil {
		return nil, pluginCatalogSyncFailed("CPA 凭据模型目录格式错误")
	}
	byID := make(map[string]string, len(entries))
	for _, entry := range entries {
		id := rawString(entry["id"])
		if !pluginValidCatalogText(id) {
			return nil, pluginCatalogSyncFailed("CPA 凭据模型目录格式错误")
		}
		byID[strings.ToLower(id)] = id
	}
	models := make([]string, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}

func pluginCatalogModels(snapshot pluginModelCatalogSnapshot) []string {
	byID := map[string]string{}
	for _, credential := range snapshot.Credentials {
		for _, model := range credential.Models {
			byID[strings.ToLower(model)] = model
		}
	}
	models := make([]string, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// Recovery is bounded to the two documented HTTP-200 plugin errors. A
// successful synchronization is new evidence for exactly one user-Key retry.
func (a *App) fetchAvailableModelsWithCatalogRecovery(ctx context.Context, cfg AppConfig, apiKey string, recovery *pluginCatalogRecovery) ([]any, error) {
	items, err := fetchAvailableModelItems(ctx, cfg, apiKey)
	var problem *AppError
	if !errors.As(err, &problem) || (problem.Code != "model_catalog_unavailable" && problem.Code != "model_catalog_out_of_sync") {
		return items, err
	}
	if recovery.attempted {
		if recovery.err != nil {
			return nil, recovery.err
		}
		return nil, problem
	}
	recovery.attempted = true
	if strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		recovery.err = appError(problem.Code, http.StatusServiceUnavailable, "CPA Helper Plugin 模型目录需要管理员同步，请检查管理配置")
		return nil, recovery.err
	}
	capabilities, err := a.cpaHelperPluginCapabilities(ctx, cfg)
	if err != nil {
		recovery.err = err
		return nil, err
	}
	if !pluginSupportsModelCatalog(capabilities) {
		return nil, problem
	}
	if _, err := a.syncPluginModelCatalog(ctx, cfg, capabilities); err != nil {
		recovery.err = err
		return nil, err
	}
	return fetchAvailableModelItems(ctx, cfg, apiKey)
}
