package app

import (
	"net/http"
	"sort"
	"strings"
)

// AuthID is the original scheduler ID used by the management models endpoint.
// It is intentionally absent from the browser and catalog projections.
type pluginHostCredential struct {
	AuthID    string
	Ref       string
	Source    string
	Provider  string
	AuthIndex string
	Label     string
	Status    string

	channelBrand aiProviderBrand
	channelKey   string
	channelName  string
	baseURL      string
	keyMasked    string
}

func pluginChannelIdentity(brand aiProviderBrand, authIndex, key, name string) string {
	item := aiProviderItem{Brand: brand, AuthIndex: &authIndex, Name: &name}
	if key != "" {
		hash := hashAPIKey(key)
		item.APIKeyHash = &hash
	}
	return modelPriceChannelAliasSelector(item)
}

func pluginChannelLabel(name, base, masked string) string {
	if name == masked {
		return pluginJoinLabel(name, base)
	}
	return pluginJoinLabel(name, base, masked)
}

func pluginChannelName(provider, name, key string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if mask := pluginMaskKey(key); mask != "" {
		return mask
	}
	for _, config := range aiProviderBrandConfigs {
		if string(config.Brand) == provider {
			return config.Label
		}
	}
	return provider
}

func (credential pluginHostCredential) directoryEntry(aliases map[modelPriceChannelGroupIdentity]string) map[string]any {
	label := credential.Label
	if credential.channelKey != "" {
		name := credential.channelName
		if alias := aliases[modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, string(credential.channelBrand), credential.channelKey)]; alias != "" {
			name = alias
		}
		label = pluginChannelLabel(name, credential.baseURL, credential.keyMasked)
	}
	entry := map[string]any{
		"ref": credential.Ref, "source": credential.Source, "provider": credential.Provider,
		"label": label, "status": credential.Status,
	}
	if credential.AuthIndex != "" {
		entry["auth_index"] = credential.AuthIndex
	}
	return entry
}

// pluginV8ConfiguredCredentials mirrors the pinned official directory.js ID
// bridge. Sharing this producer prevents display refs and model-query IDs from
// drifting, while local aliases remain display metadata only.
func pluginV8ConfiguredCredentials(config map[string]any) ([]pluginHostCredential, error) {
	result := make([]pluginHostCredential, 0)
	counters := map[string]int{}
	seen := map[string]bool{}
	add := func(kind string, parts []string, provider, authIndex, key, name, base string, disabled bool) {
		id := pluginConfiguredCredentialID(kind, parts, counters)
		brand := aiProviderBrand(provider)
		if strings.HasPrefix(kind, "openai-compatibility:") {
			brand = aiProviderBrandOpenAICompatibility
		}
		channelKey := pluginChannelIdentity(brand, authIndex, key, name)
		channelName := pluginChannelName(string(brand), name, key)
		masked := pluginMaskKey(key)
		status := "active"
		if disabled {
			status = "disabled"
		}
		result = append(result, pluginHostCredential{
			AuthID: id, Ref: pluginCredentialRef(id), Source: "ai-providers", Provider: provider,
			AuthIndex: authIndex, Status: status, Label: pluginChannelLabel(channelName, base, masked),
			channelBrand: brand, channelKey: channelKey, channelName: channelName, baseURL: base, keyMasked: masked,
		})
	}
	providerConfig, ok := config["api-keys"].(map[string]any)
	if !ok {
		return nil, validationError("CLIProxyAPI v8 配置缺少 api-keys")
	}
	providers := make([]string, 0, len(providerConfig))
	for provider := range providerConfig {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		groups, err := pluginV8ConfiguredEntries(config, provider)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if provider == "openai-compatibility" && pluginAnyBool(group["disabled"]) {
				continue
			}
			base := pluginV8String(group, "base-url", "base_url")
			keysRaw, hasKeys := group["keys"]
			if provider == "openai-compatibility" && !hasKeys {
				return nil, validationError("CPA 上游分组格式错误: " + provider)
			}
			if provider == "openai-compatibility" && keysRaw == nil {
				keysRaw = []any{}
			}
			keys, keysArray := keysRaw.([]any)
			if !keysArray {
				return nil, validationError("CPA 上游分组格式错误: " + provider)
			}
			name := pluginV8String(group, "name")
			providerName := strings.ToLower(name)
			if providerName == "" {
				providerName = "openai-compatibility"
			}
			compatProvider := providerName
			if compatProvider != "openai-compatibility" && !strings.HasPrefix(compatProvider, "openai-compatible-") {
				compatProvider = "openai-compatible-" + compatProvider
			}
			if provider == "openai-compatibility" && len(keys) == 0 {
				index, present, err := pluginV8AuthIndex(group)
				if err != nil {
					return nil, err
				}
				if present {
					add("openai-compatibility:"+providerName, []string{base}, compatProvider, index, "", name, base, pluginV8Disabled(group))
				}
				continue
			}
			for _, raw := range keys {
				entry, ok := raw.(map[string]any)
				if !ok {
					return nil, validationError("CPA 上游凭据格式错误: " + provider)
				}
				index, present, err := pluginV8AuthIndex(entry)
				if err != nil {
					return nil, err
				}
				if !present {
					continue
				}
				key, err := pluginV8APIKey(entry)
				if err != nil {
					return nil, err
				}
				proxy := pluginV8String(entry, "proxy-url", "proxy_url")
				if provider == "openai-compatibility" {
					add("openai-compatibility:"+providerName, []string{key, base, proxy}, compatProvider, index, key, name, base, pluginV8Disabled(entry))
					continue
				}
				if !map[string]bool{"gemini": true, "interactions": true, "claude": true, "codex": true, "xai": true, "meta": true, "vertex": true}[provider] {
					return nil, validationError("CPA 上游类型不受支持: " + provider)
				}
				runtimeProvider := provider
				if provider == "interactions" {
					runtimeProvider = "gemini-interactions"
				}
				proxy = pluginAnyText(pluginV8Inherited(entry, group, "proxy-url"))
				prefix := strings.Trim(pluginAnyText(pluginV8Inherited(entry, group, "prefix")), "/")
				if strings.Contains(prefix, "/") {
					prefix = ""
				}
				effectiveBase := base
				if effectiveBase == "" && provider == "meta" {
					effectiveBase = "https://api.meta.ai/v1"
				}
				parts := []string{key, effectiveBase, proxy}
				if provider != "vertex" {
					parts = append(parts, prefix, pluginV8Headers(pluginV8Inherited(entry, group, "headers")))
				}
				kind := runtimeProvider + ":apikey"
				dedup := provider + "\x00" + key + "|" + effectiveBase
				if provider != "vertex" {
					dedup = kind
					for _, part := range parts {
						dedup += "\x00" + strings.TrimSpace(part)
					}
				}
				if (provider == "gemini" || provider == "interactions" || provider == "vertex") && seen[dedup] {
					continue
				}
				seen[dedup] = true
				labelName := pluginV8String(entry, "name")
				if labelName == "" {
					labelName = name
				}
				add(kind, parts, runtimeProvider, index, key, labelName, effectiveBase, pluginV8Disabled(entry))
			}
		}
	}
	return result, nil
}

func pluginAddV8ConfiguredCredentials(target map[string]map[string]any, config map[string]any) error {
	credentials, err := pluginV8ConfiguredCredentials(config)
	if err != nil {
		return err
	}
	for _, credential := range credentials {
		if existing, ok := target[credential.Ref]; ok {
			if index := pluginAnyText(existing["auth_index"]); index != "" && index != credential.AuthIndex {
				return validationError("CPA 配置与运行时凭据索引不一致")
			}
			existing["label"] = credential.Label
			continue
		}
		target[credential.Ref] = credential.directoryEntry(nil)
	}
	return nil
}

func pluginV8HostCredentials(config map[string]any, files []map[string]any) ([]pluginHostCredential, error) {
	configured, err := pluginV8ConfiguredCredentials(config)
	if err != nil {
		return nil, err
	}
	byRef := make(map[string]pluginHostCredential, len(configured)+len(files))
	for _, credential := range configured {
		byRef[credential.Ref] = credential
	}
	seenIDs := make(map[string]bool, len(files))
	for _, file := range files {
		id := pluginAnyText(file["id"])
		if id == "" {
			return nil, validationError("CPA 凭据缺少宿主 ID")
		}
		index, present, err := pluginV8AuthIndex(file)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		if seenIDs[id] {
			return nil, validationError("CPA 凭据目录存在重复标识")
		}
		seenIDs[id] = true
		ref := pluginCredentialRef(id)
		credential, exists := byRef[ref]
		if exists && credential.AuthIndex != index {
			return nil, validationError("CPA 配置与运行时凭据索引不一致")
		}
		if !exists {
			credential = pluginHostCredential{AuthID: id, Ref: ref,
				Label: pluginJoinLabel(pluginAnyString(file, "name", "id"), pluginAnyString(file, "label"), pluginAnyString(file, "email", "account")),
			}
		}
		credential.AuthIndex = index
		// Runtime metadata is authoritative when present, but sanitized CPA
		// records can omit fields that are already known from config. Preserve
		// the configured identity instead of replacing it with an empty provider
		// or guessing auth-files for a configured AI provider.
		if pluginDirectoryFileExcluded(file) {
			credential.Source = "ai-providers"
		} else if pluginAnyString(file, "source") != "" || credential.Source == "" {
			// Host storage labels such as "file" are not catalog source values.
			credential.Source = "auth-files"
		}
		if provider := strings.ToLower(pluginAnyString(file, "provider", "type")); provider != "" {
			credential.Provider = provider
		}
		if status := pluginAnyString(file, "status"); status != "" {
			credential.Status = status
		}
		if pluginV8Disabled(file) {
			credential.Status = "disabled"
		}
		byRef[ref] = credential
	}
	result := make([]pluginHostCredential, 0, len(byRef))
	for _, credential := range byRef {
		result = append(result, credential)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result, nil
}

func pluginMergeHostDirectory(target map[string]map[string]any, credentials []pluginHostCredential, aliases map[modelPriceChannelGroupIdentity]string) error {
	for _, credential := range credentials {
		if existing, ok := target[credential.Ref]; ok {
			if index := pluginAnyText(existing["auth_index"]); index != "" && index != credential.AuthIndex {
				return appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA 配置与运行时凭据索引不一致")
			}
		}
		target[credential.Ref] = credential.directoryEntry(aliases)
	}
	return nil
}
