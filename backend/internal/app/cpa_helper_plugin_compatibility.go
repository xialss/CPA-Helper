package app

import "encoding/json"

// A release number identifies a producer; the implemented contract determines
// how Helper may use it. Historical releases retain their original bridges.
type pluginManagementContract struct {
	PluginVersion       string
	CPAOrigin           string
	V8                  bool
	ModelCatalog        bool
	AuthoritativeModels bool
	Negotiated          bool
}

func pluginManagementCompatibility(capabilities map[string]json.RawMessage) (pluginManagementContract, error) {
	contract := pluginManagementContract{
		PluginVersion: rawString(capabilities["plugin_version"]),
		CPAOrigin:     rawString(capabilities["cpa_version"]),
	}
	if !pluginValidCatalogText(contract.PluginVersion) {
		return pluginManagementContract{}, pluginIncompatibleError()
	}
	var expectedOrigin string
	switch contract.PluginVersion {
	case "0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4":
		return contract, nil
	case "0.1.5":
		expectedOrigin = "v8.0.12"
	case "0.1.6":
		expectedOrigin = "v8.0.17"
	case "0.1.7":
		expectedOrigin = "v8.0.29"
	case "0.1.8":
		expectedOrigin, contract.ModelCatalog = pluginModelCatalogCPA, true
	case "0.1.9", "0.1.10":
		expectedOrigin, contract.ModelCatalog, contract.AuthoritativeModels = "v8.0.45", true, true
		if !pluginImplementsManagementSchema7(capabilities) {
			return pluginManagementContract{}, pluginIncompatibleError()
		}
	default:
		if !pluginImplementsManagementSchema7(capabilities) || !pluginValidCatalogText(contract.CPAOrigin) || !pluginStableManagementCapabilities(capabilities) {
			return pluginManagementContract{}, pluginIncompatibleError()
		}
		contract.ModelCatalog, contract.AuthoritativeModels, contract.Negotiated = true, true, true
	}
	if expectedOrigin != "" && contract.CPAOrigin != expectedOrigin {
		return pluginManagementContract{}, pluginIncompatibleError()
	}
	contract.V8 = true
	return contract, nil
}

// schema_version is the plugin's implemented RPC contract, not the Host's
// maximum schema. A newer Host must not make Helper opt into unknown semantics.
func pluginImplementsManagementSchema7(capabilities map[string]json.RawMessage) bool {
	return rawString(capabilities["contract_version"]) == "v1" && rawUint(capabilities["abi_version"]) == 1 && rawUint(capabilities["schema_version"]) == 7
}

func pluginStableManagementCapabilities(capabilities map[string]json.RawMessage) bool {
	if rawString(capabilities["plugin_id"]) != "cpa-helper-plugin" || rawString(capabilities["unknown_key"]) != "allow" || rawString(capabilities["concurrency_scope"]) != "instance" {
		return false
	}
	var billing bool
	if json.Unmarshal(capabilities["billing"], &billing) != nil || rawJSONNull(capabilities["billing"]) || billing {
		return false
	}
	for field, required := range map[string][]string{
		"modules":       {"model_rules", "model_list_filter", "credential_routes", "concurrency"},
		"policy_fields": {"groups[].id", "groups[].name", "groups[].note", "groups[].rule", "keys[].id", "keys[].label", "keys[].enabled", "keys[].group_ids", "keys[].max_concurrency", "keys[].rule"},
	} {
		values, err := pluginCapabilityStrings(capabilities, field)
		if err != nil {
			return false
		}
		for _, value := range required {
			if !pluginContainsCapability(values, value) {
				return false
			}
		}
	}
	return true
}
