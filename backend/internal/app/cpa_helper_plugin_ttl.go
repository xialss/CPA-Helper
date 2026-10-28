package app

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
)

// This projection intentionally excludes upstream paths, errors and execution
// identities. Configured describes file validation, not provider acceptance.
type cpaHelperPluginClaudeTTLFeature struct {
	Supported       bool   `json:"supported"`
	Status          string `json:"status"`
	MetadataVersion string `json:"metadata_version,omitempty"`
}

func pluginCapabilityStrings(capabilities map[string]json.RawMessage, field string) ([]string, error) {
	raw, exists := capabilities[field]
	if !exists {
		return nil, nil
	}
	var entries []json.RawMessage
	if rawJSONNull(raw) || json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, pluginIncompatibleError()
	}
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		var value string
		if rawJSONNull(entry) || json.Unmarshal(entry, &value) != nil || !pluginValidCatalogText(value) {
			return nil, pluginIncompatibleError()
		}
		values = append(values, value)
	}
	return values, nil
}

func pluginContainsCapability(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func pluginSupportsResponseVerification(capabilities map[string]json.RawMessage) bool {
	modules, err := pluginCapabilityStrings(capabilities, "modules")
	if err != nil {
		return false
	}
	if _, exists := capabilities["modules"]; exists {
		return pluginContainsCapability(modules, "response_model_mismatch")
	}
	// Older published contracts exposed the config before modules was required.
	// The removed 0.1.10 feature must never be restored by a stale config field.
	if rawString(capabilities["plugin_version"]) == "0.1.10" {
		return false
	}
	raw, exists := capabilities["response_model_mismatch"]
	return exists && !rawJSONNull(raw)
}

func pluginResponseVerificationUnsupported() error {
	return appError("plugin_feature_unsupported", http.StatusServiceUnavailable, "当前 CPA Helper Plugin 不支持响应模型核验")
}

func (a *App) pluginClaudeTTLFeature(ctx context.Context, cfg AppConfig) (cpaHelperPluginClaudeTTLFeature, error) {
	capabilities, err := a.cpaHelperPluginCapabilities(ctx, cfg)
	if err != nil {
		return cpaHelperPluginClaudeTTLFeature{}, err
	}
	feature, err := a.pluginClaudeTTLFeatureFromState(ctx, cfg, capabilities, nil)
	if err != nil || !feature.Supported {
		return feature, err
	}
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
	if err != nil {
		// Metadata remains readable when the current config or policy is unhealthy.
		// Its supported protocol is independent of admitting new controlled traffic.
		if status == http.StatusServiceUnavailable {
			feature.Status = "unavailable"
			return feature, nil
		}
		return cpaHelperPluginClaudeTTLFeature{}, appErrorFromPlugin(err, status)
	}
	health, err := decodePluginObject(payload)
	if err != nil {
		return cpaHelperPluginClaudeTTLFeature{}, pluginIncompatibleError()
	}
	return pluginClaudeTTLHealth(feature, health)
}

func (a *App) pluginClaudeTTLFeatureFromState(ctx context.Context, cfg AppConfig, capabilities, health map[string]json.RawMessage) (cpaHelperPluginClaudeTTLFeature, error) {
	feature := cpaHelperPluginClaudeTTLFeature{Status: "unsupported"}
	if _, err := pluginUsesV8Management(capabilities); err != nil {
		return feature, err
	}
	if rawString(capabilities["plugin_version"]) != "0.1.10" {
		return feature, nil
	}
	modules, err := pluginCapabilityStrings(capabilities, "modules")
	if err != nil {
		return feature, err
	}
	fields, err := pluginCapabilityStrings(capabilities, "policy_fields")
	if err != nil {
		return feature, err
	}
	hasModule := pluginContainsCapability(modules, "claude_cache_ttl")
	hasField := pluginContainsCapability(fields, "keys[].claude_cache_ttl")
	if !hasModule && !hasField {
		return feature, nil
	}
	if !hasModule || !hasField {
		return feature, pluginIncompatibleError()
	}
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/claude-ttl/requirements", nil, nil)
	if err != nil {
		return feature, appErrorFromPlugin(err, status)
	}
	requirements, err := pluginStrictCatalogObject(payload, []string{"payload", "reserved_headers", "configuration_validation", "metadata_version"}, false)
	if err != nil || rawString(requirements["metadata_version"]) != "v1" || rawString(requirements["configuration_validation"]) != "pinned_file_requires_reload" {
		return feature, pluginIncompatibleError()
	}
	var headers []string
	if json.Unmarshal(requirements["reserved_headers"], &headers) != nil || !reflect.DeepEqual(headers, []string{"X-CPA-Helper-Claude-TTL", "X-CPA-Helper-TTL-Request"}) || !jsonValueEqual(requirements["payload"], mustRaw(pluginClaudeTTLRequiredPayload())) {
		return feature, pluginIncompatibleError()
	}
	feature = cpaHelperPluginClaudeTTLFeature{Supported: true, Status: "unavailable", MetadataVersion: "v1"}
	if health == nil {
		return feature, nil
	}
	return pluginClaudeTTLHealth(feature, health)
}

func pluginClaudeTTLHealth(feature cpaHelperPluginClaudeTTLFeature, health map[string]json.RawMessage) (cpaHelperPluginClaudeTTLFeature, error) {
	state, err := decodePluginObject(health["claude_cache_ttl"])
	if err != nil || rawString(state["ttl_source"]) != "enforced_request" || rawUint(state["metadata_retention_hours"]) != 24 || rawString(state["configuration_validation"]) != "pinned_file_requires_reload" {
		return cpaHelperPluginClaudeTTLFeature{}, pluginIncompatibleError()
	}
	switch rawString(state["status"]) {
	case "configured", "disabled", "unavailable":
		feature.Status = rawString(state["status"])
	default:
		return cpaHelperPluginClaudeTTLFeature{}, pluginIncompatibleError()
	}
	return feature, nil
}

// The fixed v1 requirements describe the complete supported rule set. Helper
// checks this contract but never installs, merges or rewrites the host payload.
func pluginClaudeTTLRequiredPayload() map[string]any {
	paths := []string{
		`tools.#(cache_control.type=="ephemeral")#.cache_control.ttl`,
		`system.#(cache_control.type=="ephemeral")#.cache_control.ttl`,
		`messages.#(content)#.content.#(cache_control.type=="ephemeral")#.cache_control.ttl`,
	}
	models := func(ttl string, root bool) []any {
		model := map[string]any{"name": "*", "protocol": "claude", "headers": map[string]any{"X-CPA-Helper-Claude-TTL": ttl}}
		if root {
			model["match"] = []any{map[string]any{"cache_control.type": "ephemeral"}}
		}
		return []any{model}
	}
	params := map[string]any{}
	for _, path := range paths {
		params[path] = "1h"
	}
	return map[string]any{
		"override": []any{
			map[string]any{"models": models("1h", false), "params": params},
			map[string]any{"models": models("1h", true), "params": map[string]any{"cache_control.ttl": "1h"}},
		},
		"filter": []any{
			map[string]any{"models": models("5m", false), "params": paths},
			map[string]any{"models": models("5m", true), "params": []string{"cache_control.ttl"}},
		},
	}
}

func pluginPolicyTTLs(policy map[string]json.RawMessage) map[string]string {
	keys, _ := rawObjectArray(policy, "keys")
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[rawString(key["id"])] = rawString(key["claude_cache_ttl"])
	}
	return values
}

func pluginPolicyEnablesTTL(current, next map[string]json.RawMessage) bool {
	previous := pluginPolicyTTLs(current)
	for id, ttl := range pluginPolicyTTLs(next) {
		if ttl != "" && previous[id] != ttl {
			return true
		}
	}
	return false
}

func pluginPolicyTTLMatches(expected, actual map[string]json.RawMessage) bool {
	actualTTLs := pluginPolicyTTLs(actual)
	for id, ttl := range pluginPolicyTTLs(expected) {
		if actualTTLs[id] != ttl {
			return false
		}
	}
	return true
}
