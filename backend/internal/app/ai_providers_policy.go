package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var aiProviderPolicyInputFields = []string{
	"disable_cooling", "request_retry", "request_scoped_errors", "alpha_search",
	"disable_codex_cloaking", "support_prompt_cache_key", "fingerprint_profile",
	"rebuild_mid_system_message", "experimental_cch_signing", "cloak",
}

type aiProviderErrorRule struct {
	SourceHash string   `json:"source_hash,omitempty"`
	Status     *int     `json:"status,omitempty"`
	Match      []string `json:"match,omitempty"`
	MatchRegex []string `json:"match_regex,omitempty"`
	Action     *string  `json:"action,omitempty"`
}

type aiProviderRuntimePolicy struct {
	DisableCooling      *bool                  `json:"disable_cooling,omitempty"`
	RequestRetry        *int                   `json:"request_retry,omitempty"`
	RequestScopedErrors *[]aiProviderErrorRule `json:"request_scoped_errors,omitempty"`
}

func (cloak *aiProviderCloak) UnmarshalJSON(data []byte) error {
	type alias aiProviderCloak
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*cloak = aiProviderCloak(decoded)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	cloak.fieldsSet = make(map[string]bool, len(raw))
	for field := range raw {
		cloak.fieldsSet[field] = true
	}
	return nil
}

func aiProviderBehaviorBool(raw map[string]any, brand, expected aiProviderBrand, key string) *bool {
	if brand != expected {
		return nil
	}
	return aiProviderBoolPtrFromKeys(raw, key, strings.ReplaceAll(key, "-", "_"))
}

func aiProviderFingerprintFromRaw(raw map[string]any, brand aiProviderBrand) *string {
	if brand != aiProviderBrandClaude {
		return nil
	}
	value := aiProviderStringFromKeys(raw, "fingerprint-profile", "fingerprint_profile")
	if value != nil && *value == "oauth-cli" {
		canonical := "claude-code-cli"
		return &canonical
	}
	return value
}

func applyAIProviderPolicies(brand aiProviderBrandConfig, payload aiProviderItem, current, next map[string]any) error {
	setBool := func(input, wire string, value *bool) {
		if !payload.policyFieldsSet[input] && value == nil {
			return
		}
		delete(next, input)
		delete(next, wire)
		if value != nil {
			next[wire] = *value
		}
	}
	setBool("disable_cooling", "disable-cooling", payload.DisableCooling)
	if payload.policyFieldsSet["request_retry"] || payload.RequestRetry != nil {
		delete(next, "request_retry")
		delete(next, "request-retry")
		if payload.RequestRetry != nil {
			next["request-retry"] = *payload.RequestRetry
		}
	}
	if payload.policyFieldsSet["request_scoped_errors"] || payload.RequestScopedErrors != nil {
		if brand.Brand == aiProviderBrandVertex {
			return validationError("Vertex 不支持请求错误规则")
		}
		delete(next, "request_scoped_errors")
		delete(next, "request-scoped-errors")
		if payload.RequestScopedErrors != nil {
			rules, err := aiProviderErrorRulesToUpstream(*payload.RequestScopedErrors, current["request-scoped-errors"])
			if err != nil {
				return err
			}
			next["request-scoped-errors"] = rules
		}
	}
	if brand.Brand == aiProviderBrandCodex {
		setBool("alpha_search", "alpha-search", payload.AlphaSearch)
		setBool("disable_codex_cloaking", "disable-codex-cloaking", payload.DisableCodexCloaking)
	} else if payload.AlphaSearch != nil || payload.DisableCodexCloaking != nil {
		return validationError("当前提供商不支持 Codex 专属行为")
	}
	if brand.Brand == aiProviderBrandOpenAICompatibility {
		setBool("support_prompt_cache_key", "support-prompt-cache-key", payload.SupportPromptCacheKey)
	} else if payload.SupportPromptCacheKey != nil {
		return validationError("当前提供商不支持提示词缓存键注入")
	}
	if brand.Brand != aiProviderBrandClaude {
		if payload.FingerprintProfile != nil || payload.Cloak != nil {
			return validationError("当前提供商不支持 Claude 请求指纹或 Cloak")
		}
		return nil
	}
	setBool("rebuild_mid_system_message", "rebuild-mid-system-message", payload.RebuildMidSystemMessage)
	setBool("experimental_cch_signing", "experimental-cch-signing", payload.ExperimentalCCHSigning)
	if payload.policyFieldsSet["fingerprint_profile"] || payload.FingerprintProfile != nil {
		delete(next, "fingerprint_profile")
		delete(next, "fingerprint-profile")
		if payload.FingerprintProfile != nil {
			value := strings.TrimSpace(*payload.FingerprintProfile)
			if value == "oauth-cli" {
				value = "claude-code-cli"
			}
			previous := aiProviderOptionalString(aiProviderFingerprintFromRaw(current, brand.Brand))
			if value != "" && value != "claude-code-cli" && value != previous {
				return validationError("Claude 请求指纹配置无效")
			}
			if value != "" {
				next["fingerprint-profile"] = value
			}
		}
	}
	if payload.policyFieldsSet["cloak"] || payload.Cloak != nil {
		if payload.Cloak == nil {
			delete(next, "cloak")
		} else {
			if mode := payload.Cloak.Mode; mode != nil {
				value := strings.TrimSpace(*mode)
				previous := aiProviderCloakFromAny(current["cloak"])
				unchanged := previous != nil && aiProviderOptionalString(previous.Mode) == value
				if value != "" && value != "auto" && value != "always" && value != "never" && value != "uncloak" && !unchanged {
					return validationError("Claude Cloak 模式无效")
				}
			}
			next["cloak"] = aiProviderCloakToUpstream(*payload.Cloak, current)
		}
	}
	return nil
}

func aiProviderErrorRulesFromAny(value any) *[]aiProviderErrorRule {
	if value == nil {
		return nil
	}
	items, err := aiProviderListFromAny(value, "request-scoped-errors")
	if err != nil {
		return nil
	}
	rules := make([]aiProviderErrorRule, 0, len(items))
	for _, item := range items {
		rules = append(rules, aiProviderErrorRule{
			SourceHash: aiProviderErrorRuleSourceHash(item),
			Status:     aiProviderIntPtrFromKeys(item, "status"),
			Match:      aiProviderRuleStrings(item["match"]),
			MatchRegex: aiProviderRuleStrings(item["match-regexr"]),
			Action:     aiProviderRuleAction(item["action"]),
		})
	}
	return &rules
}

func aiProviderRuleStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func aiProviderRuleAction(value any) *string {
	if action, ok := value.(string); ok {
		return &action
	}
	return nil
}

func aiProviderErrorRulesToUpstream(rules []aiProviderErrorRule, current any) ([]map[string]any, error) {
	previous, _ := aiProviderListFromAny(current, "request-scoped-errors")
	used := make([]bool, len(previous))
	// Reserve explicit sources before matching source-less rules. A new rule,
	// including a duplicate, cannot claim a source referenced later in the list.
	reserved := make(map[string]bool)
	for _, rule := range rules {
		if rule.SourceHash != "" {
			reserved[rule.SourceHash] = true
		}
	}
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		next := aiProviderErrorRuleToMap(rule)
		matched := -1
		for currentIndex, raw := range previous {
			source := aiProviderErrorRuleSourceHash(raw)
			if !used[currentIndex] && ((rule.SourceHash != "" && rule.SourceHash == source) || (rule.SourceHash == "" && !reserved[source] && aiProviderErrorRuleMatchesRaw(rule, raw))) {
				matched = currentIndex
				break
			}
		}
		if rule.SourceHash != "" && matched < 0 {
			return nil, conflictError("请求错误规则来源已变化，请刷新后重试")
		}
		if matched >= 0 && aiProviderErrorRuleMatchesRaw(rule, previous[matched]) {
			used[matched] = true
			result = append(result, cloneAIProviderMap(previous[matched]))
			continue
		}
		if rule.Status == nil || *rule.Status <= 0 {
			return nil, validationError("请求错误规则需要正整数状态码")
		}
		action := strings.ToLower(strings.TrimSpace(aiProviderOptionalString(rule.Action)))
		if action != "stop" && action != "stop-and-cooldown" && action != "continue" && action != "continue-and-cooldown" {
			return nil, validationError("请求错误规则动作无效")
		}
		next["action"] = action
		if len(rule.Match)+len(rule.MatchRegex) == 0 {
			return nil, validationError("请求错误规则至少需要一个匹配条件")
		}
		var prior aiProviderErrorRule
		priorIndex := matched
		if priorIndex >= 0 {
			prior = (*aiProviderErrorRulesFromAny([]any{previous[priorIndex]}))[0]
		}
		for conditionIndex, value := range rule.Match {
			if value == "" && (conditionIndex >= len(prior.Match) || prior.Match[conditionIndex] != "") {
				return nil, validationError("请求错误规则匹配条件不能为空")
			}
		}
		for conditionIndex, pattern := range rule.MatchRegex {
			if pattern == "" && (conditionIndex >= len(prior.MatchRegex) || prior.MatchRegex[conditionIndex] != "") {
				return nil, validationError("请求错误规则正则条件不能为空")
			}
			if _, err := regexp.Compile(pattern); err != nil && (conditionIndex >= len(prior.MatchRegex) || prior.MatchRegex[conditionIndex] != pattern) {
				return nil, validationError("请求错误规则包含无效的 Go 正则表达式")
			}
		}
		// Only a matched source can supply extension fields. Positional fallback
		// would assign a removed or moved rule's extensions to a new rule.
		if priorIndex >= 0 {
			base := cloneAIProviderMap(previous[priorIndex])
			for _, key := range []string{"status", "match", "match-regexr", "action"} {
				delete(base, key)
			}
			for key, value := range next {
				base[key] = value
			}
			next = base
			used[priorIndex] = true
		}
		result = append(result, next)
	}
	return result, nil
}

func aiProviderErrorRuleToMap(rule aiProviderErrorRule) map[string]any {
	result := map[string]any{}
	if rule.Status != nil {
		result["status"] = *rule.Status
	}
	if rule.Match != nil {
		result["match"] = rule.Match
	}
	if rule.MatchRegex != nil {
		result["match-regexr"] = rule.MatchRegex
	}
	if rule.Action != nil {
		result["action"] = *rule.Action
	}
	return result
}

func aiProviderErrorRuleMatchesRaw(rule aiProviderErrorRule, raw map[string]any) bool {
	previous := aiProviderErrorRulesFromAny([]any{raw})
	if previous == nil || len(*previous) != 1 {
		return false
	}
	known := (*previous)[0]
	known.SourceHash = ""
	rule.SourceHash = ""
	left, _ := json.Marshal(rule)
	right, _ := json.Marshal(known)
	return bytes.Equal(left, right)
}

func aiProviderErrorRuleSourceHash(raw map[string]any) string {
	encoded, _ := json.Marshal(raw)
	return hashAPIKey(string(encoded))
}

func (a *App) verifyAIProviderPolicyWrite(ctx context.Context, cfg AppConfig, brand aiProviderBrandConfig, expected []map[string]any, index int, payload aiProviderItem) error {
	fields := aiProviderPolicyWriteFields(payload)
	if len(fields) == 0 {
		return nil
	}
	body, err := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, brand.UpstreamPath, nil, aiProviderManagementTimeout)
	if err != nil {
		return aiProviderCommittedVerificationError()
	}
	actual, err := parseAIProviderList(body, brand.ConfigKey)
	if err != nil || len(actual) != len(expected) || index < 0 || index >= len(actual) {
		return aiProviderCommittedVerificationError()
	}
	wanted := aiProviderItemFromRaw(brand, index, expected[index])
	observed := aiProviderItemFromRaw(brand, index, actual[index])
	if wanted.IdentityHash != observed.IdentityHash || aiProviderOptionalString(wanted.BaseURL) != aiProviderOptionalString(observed.BaseURL) || aiProviderOptionalString(wanted.Name) != aiProviderOptionalString(observed.Name) {
		return aiProviderCommittedVerificationError()
	}
	for _, field := range fields {
		if (field == "alpha-search" || field == "support-prompt-cache-key" || field == "rebuild-mid-system-message" || field == "experimental-cch-signing") && expected[index][field] == false && actual[index][field] == nil {
			// CPA represents these documented plain bool defaults with omitempty.
			continue
		}
		if !aiProviderJSONEqual(expected[index][field], actual[index][field]) {
			return aiProviderCommittedVerificationError()
		}
	}
	return nil
}

func aiProviderPolicyWriteFields(payload aiProviderItem) []string {
	values := map[string]bool{
		"disable_cooling":            payload.DisableCooling != nil,
		"request_retry":              payload.RequestRetry != nil,
		"request_scoped_errors":      payload.RequestScopedErrors != nil,
		"alpha_search":               payload.AlphaSearch != nil,
		"disable_codex_cloaking":     payload.DisableCodexCloaking != nil,
		"support_prompt_cache_key":   payload.SupportPromptCacheKey != nil,
		"fingerprint_profile":        payload.FingerprintProfile != nil,
		"rebuild_mid_system_message": payload.RebuildMidSystemMessage != nil,
		"experimental_cch_signing":   payload.ExperimentalCCHSigning != nil,
		"cloak":                      payload.Cloak != nil,
	}
	fields := make([]string, 0, len(values))
	for _, field := range aiProviderPolicyInputFields {
		if payload.policyFieldsSet[field] || values[field] {
			fields = append(fields, strings.ReplaceAll(field, "_", "-"))
		}
	}
	return fields
}

func aiProviderJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func aiProviderCommittedVerificationError() error {
	return appError("provider_config_committed_refresh_required", http.StatusConflict,
		"AI provider 配置已提交，但部分配置未生效或无法核验，请刷新后检查 CPA 兼容性")
}

func (a *App) aiProviderSnapshotAfterWrite(ctx context.Context, cfg AppConfig) (aiProvidersResponse, error) {
	response, err := a.aiProvidersSnapshotForConfig(ctx, cfg)
	if err != nil {
		return aiProvidersResponse{}, appError("provider_config_committed_refresh_required", http.StatusConflict,
			"AI provider 配置已提交，但无法刷新列表，请刷新后检查配置")
	}
	return response, nil
}
