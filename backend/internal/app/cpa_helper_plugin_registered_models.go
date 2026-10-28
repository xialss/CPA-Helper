package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Models and their credential refs come only from the Host registry. Config and
// credential files provide display/routing metadata and must join exactly.
type pluginRegisteredCredentialModels struct {
	Ref       string   `json:"ref"`
	AuthIndex string   `json:"auth_index"`
	Provider  string   `json:"provider"`
	Models    []string `json:"models"`
}

func (a *App) pluginRegisteredCredentialModels(ctx context.Context, cfg AppConfig) ([]pluginRegisteredCredentialModels, error) {
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/credential-models", nil, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	object, err := pluginStrictCatalogObject(payload, []string{"credentials"}, false)
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if json.Unmarshal(object["credentials"], &entries) != nil || entries == nil {
		return nil, pluginIncompatibleError()
	}
	result := make([]pluginRegisteredCredentialModels, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		entry, err := pluginStrictCatalogObject(raw, []string{"ref", "auth_index", "provider", "models"}, false)
		if err != nil {
			return nil, err
		}
		var credential pluginRegisteredCredentialModels
		if json.Unmarshal(raw, &credential) != nil || !pluginValidCredentialRef(credential.Ref) || seen[credential.Ref] || !pluginValidCatalogText(credential.AuthIndex) || !pluginValidCatalogText(credential.Provider) || strings.ToLower(credential.Provider) != credential.Provider {
			return nil, pluginIncompatibleError()
		}
		var modelEntries []json.RawMessage
		if json.Unmarshal(entry["models"], &modelEntries) != nil || modelEntries == nil {
			return nil, pluginIncompatibleError()
		}
		models := make(map[string]bool, len(modelEntries))
		for _, modelEntry := range modelEntries {
			var model string
			if rawJSONNull(modelEntry) || json.Unmarshal(modelEntry, &model) != nil || !pluginValidCatalogText(model) || models[strings.ToLower(model)] {
				return nil, pluginIncompatibleError()
			}
			models[strings.ToLower(model)] = true
		}
		sort.Strings(credential.Models)
		seen[credential.Ref] = true
		result = append(result, credential)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result, nil
}

func pluginValidCredentialRef(ref string) bool {
	if !strings.HasPrefix(ref, "sha256:") {
		return false
	}
	hash := strings.TrimPrefix(ref, "sha256:")
	_, err := hex.DecodeString(hash)
	return len(hash) == 64 && strings.ToLower(hash) == hash && err == nil
}

func pluginJoinRegisteredCredentialModels(metadata []pluginHostCredential, registered []pluginRegisteredCredentialModels) ([]pluginModelCatalogCredential, []pluginHostCredential, error) {
	byRef := make(map[string]pluginHostCredential, len(metadata))
	for _, credential := range metadata {
		if _, exists := byRef[credential.Ref]; exists {
			return nil, nil, pluginIncompatibleError()
		}
		byRef[credential.Ref] = credential
	}
	entries := make([]pluginModelCatalogCredential, 0, len(metadata))
	joined := make([]pluginHostCredential, 0, len(metadata))
	for _, entry := range registered {
		credential, exists := byRef[entry.Ref]
		// Empty Host-only identities own no models and have no reliable source or
		// enabled metadata. They still remain in both full registry comparisons.
		if !exists && len(entry.Models) == 0 {
			continue
		}
		if !exists || entry.AuthIndex != credential.AuthIndex || entry.Provider != credential.Provider || (credential.Source != "auth-files" && credential.Source != "ai-providers") {
			return nil, nil, pluginCatalogSyncFailed("CPA 注册凭据与元数据不一致，请重新同步")
		}
		delete(byRef, entry.Ref)
		entries = append(entries, pluginModelCatalogCredential{Ref: entry.Ref, Source: credential.Source, Provider: entry.Provider, Enabled: credential.Status != "disabled", Models: entry.Models})
		joined = append(joined, credential)
	}
	if len(byRef) != 0 {
		return nil, nil, pluginCatalogSyncFailed("CPA 凭据元数据未被宿主注册目录完整覆盖，请重新同步")
	}
	return entries, joined, nil
}
