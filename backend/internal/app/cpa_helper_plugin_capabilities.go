package app

import (
	"encoding/json"
	"net/http"
)

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
	// Only historical contracts may infer this feature from the old config.
	// Modern producers must declare it, never revive it through a stale field.
	contract, err := pluginManagementCompatibility(capabilities)
	if err != nil || (contract.AuthoritativeModels && contract.PluginVersion != "0.1.9") {
		return false
	}
	raw, exists := capabilities["response_model_mismatch"]
	return exists && !rawJSONNull(raw)
}

func pluginResponseVerificationUnsupported() error {
	return appError("plugin_feature_unsupported", http.StatusServiceUnavailable, "当前 CPA Helper Plugin 不支持响应模型核验")
}
