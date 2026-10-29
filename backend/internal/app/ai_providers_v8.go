package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

var aiProviderV8SharedFields = []string{
	"priority", "prefix", "proxy-url", "headers", "models", "excluded-models",
	"disable-cooling", "request-retry", "request-scoped-errors",
}

type aiProviderV8Entry struct {
	GroupIndex int
	KeyIndex   int
	Index      int
	Group      map[string]any
	Key        map[string]any
	Effective  map[string]any
	SourceHash string
}

// Only an absent v8 endpoint selects the legacy adapter. An unreadable v8
// configuration must never be replaced by its lossy flattened v0 view.
func (a *App) aiProviderV8Config(ctx context.Context, cfg AppConfig) (map[string]any, bool, error) {
	raw, supported, _, err := a.aiProviderV8Read(ctx, cfg)
	return raw, supported, err
}

func (a *App) aiProviderV8Read(ctx context.Context, cfg AppConfig) (map[string]any, bool, string, error) {
	response, body, err := a.aiProviderManagementResponse(ctx, cfg, http.MethodGet, "/v8/management/config", nil, aiProviderManagementTimeout)
	if err != nil {
		return nil, false, "", err
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, false, "", nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, true, "", validationError(fmt.Sprintf("读取 CPA v8 配置失败：HTTP %d", response.StatusCode))
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return nil, true, "", validationError("CPA v8 配置不是有效对象")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, true, "", validationError("CPA v8 配置包含无效数据")
	}
	for _, brand := range aiProviderBrandConfigs {
		if _, err := aiProviderV8Groups(raw, brand); err != nil {
			return nil, true, "", err
		}
	}
	return raw, true, response.Header.Get("ETag"), nil
}

func aiProviderV8Family(brand aiProviderBrandConfig) string {
	if brand.Brand == aiProviderBrandOpenAICompatibility {
		return "openai-compatibility"
	}
	return string(brand.Brand)
}

func aiProviderV8Groups(raw map[string]any, brand aiProviderBrandConfig) ([]map[string]any, error) {
	value, exists := raw["api-keys"]
	if !exists {
		return []map[string]any{}, nil
	}
	families, ok := value.(map[string]any)
	if !ok {
		return nil, validationError("CPA v8 api-keys 配置不是有效对象")
	}
	value, exists = families[aiProviderV8Family(brand)]
	if !exists {
		return []map[string]any{}, nil
	}
	if _, ok := value.([]any); !ok {
		return nil, validationError(brand.Label + " v8 配置不是有效分组列表")
	}
	groups, err := aiProviderListFromAny(value, "groups")
	if err != nil {
		return nil, validationError(brand.Label + " v8 配置不是有效分组列表")
	}
	for _, group := range groups {
		keys, ok := group["keys"].([]any)
		if !ok {
			return nil, validationError(brand.Label + " v8 分组缺少有效 keys 列表")
		}
		for _, value := range keys {
			key, ok := value.(map[string]any)
			if !ok {
				return nil, validationError(brand.Label + " v8 key 不是有效对象")
			}
			if brand.Brand != aiProviderBrandOpenAICompatibility {
				if _, exists := key["base-url"]; exists {
					return nil, validationError(brand.Label + " v8 key 中不允许配置 base-url")
				}
			}
		}
	}
	return groups, nil
}

func aiProviderV8Entries(raw map[string]any, cfg AppConfig, brand aiProviderBrandConfig) ([]aiProviderV8Entry, error) {
	groups, err := aiProviderV8Groups(raw, brand)
	if err != nil {
		return nil, err
	}
	entries := make([]aiProviderV8Entry, 0)
	for groupIndex, group := range groups {
		groupJSON, _ := json.Marshal(aiProviderV8PersistentGroup(group))
		source := hashAPIKey(modelPriceSelectorConfigKey(cfg) + "\x00" + aiProviderV8Family(brand) + "\x00" + string(groupJSON))
		if brand.Brand == aiProviderBrandOpenAICompatibility {
			effective := cloneAIProviderMap(group)
			effective["api-key-entries"] = effective["keys"]
			delete(effective, "keys")
			delete(effective, "auth-index")
			delete(effective, "auth_index")
			entries = append(entries, aiProviderV8Entry{GroupIndex: groupIndex, KeyIndex: -1,
				Index: len(entries), Group: group, Key: group, Effective: effective, SourceHash: source})
			continue
		}
		for keyIndex, value := range group["keys"].([]any) {
			key := value.(map[string]any)
			effective := map[string]any{}
			if baseURL, exists := group["base-url"]; exists {
				effective["base-url"] = cloneAIProviderValue(baseURL)
			}
			for _, field := range aiProviderV8SharedFields {
				if value, exists := group[field]; exists {
					effective[field] = cloneAIProviderValue(value)
				}
			}
			for field, value := range key {
				if value != nil && field != "auth-index" && field != "auth_index" {
					effective[field] = cloneAIProviderValue(value)
				}
			}
			if authIndex := aiProviderStringFromKeys(key, "auth_index", "auth-index"); authIndex != nil {
				effective["auth-index"] = *authIndex
			}
			entries = append(entries, aiProviderV8Entry{GroupIndex: groupIndex, KeyIndex: keyIndex,
				Index: len(entries), Group: group, Key: key, Effective: effective,
				SourceHash: hashAPIKey(source + "\x00" + strconv.Itoa(keyIndex))})
		}
	}
	return entries, nil
}

func aiProviderV8Snapshot(raw map[string]any, cfg AppConfig) ([]aiProviderItem, error) {
	providers := make([]aiProviderItem, 0)
	for _, brand := range aiProviderBrandConfigs {
		entries, err := aiProviderV8Entries(raw, cfg, brand)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			item := aiProviderItemFromRaw(brand, entry.Index, entry.Effective)
			item.SourceHash = entry.SourceHash
			item.RuntimePolicyEffective = &aiProviderRuntimePolicy{
				DisableCooling: item.DisableCooling, RequestRetry: item.RequestRetry, RequestScopedErrors: item.RequestScopedErrors,
			}
			item.DisableCooling = aiProviderBoolPtrFromKeys(entry.Key, "disable-cooling")
			item.RequestRetry = aiProviderIntPtrFromKeys(entry.Key, "request-retry")
			if brand.Brand != aiProviderBrandVertex {
				item.RequestScopedErrors = aiProviderErrorRulesFromAny(entry.Key["request-scoped-errors"])
			}
			providers = append(providers, item)
		}
	}
	return providers, nil
}

func aiProviderV8Target(entries []aiProviderV8Entry, brand aiProviderBrandConfig, selector aiProviderSelector) (aiProviderV8Entry, error) {
	if selector.SourceHash == "" || !selector.validForOrder(brand) {
		return aiProviderV8Entry{}, conflictError("CPA 提供商配置需要刷新后再操作")
	}
	sourceHash := selector.SourceHash
	selector.SourceHash = ""
	var target aiProviderV8Entry
	matches := 0
	for _, entry := range entries {
		if entry.SourceHash == sourceHash && matchesAIProviderSelector(brand, entry.Effective, entry.Index, selector) {
			target = entry
			matches++
		}
	}
	if matches != 1 {
		return aiProviderV8Entry{}, conflictError("目标 AI provider 或其继承配置已变化，请刷新后重试")
	}
	return target, nil
}

func (a *App) createAIProviderV8(ctx context.Context, cfg AppConfig, raw map[string]any, etag string, brand aiProviderBrandConfig, payload aiProviderItem) (aiProvidersResponse, error) {
	groups, err := aiProviderV8Groups(raw, brand)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	next, err := aiProviderPayloadToUpstream(brand, payload, nil, true)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	var group map[string]any
	if brand.Brand == aiProviderBrandOpenAICompatibility {
		group = aiProviderV8OpenAIGroup(next)
	} else {
		group = map[string]any{"name": aiProviderV8NewGroupName(groups, brand), "keys": []any{next}}
		if value, exists := next["base-url"]; exists {
			group["base-url"] = value
			delete(next, "base-url")
		}
	}
	groups = append(groups, group)
	if err := a.putAIProviderV8Groups(ctx, cfg, etag, brand, groups); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProviderSnapshotAfterWrite(ctx, cfg)
}

func aiProviderV8NewGroupName(groups []map[string]any, brand aiProviderBrandConfig) string {
	names := make(map[string]bool, len(groups))
	for _, group := range groups {
		names[aiProviderOptionalString(aiProviderStringFromKeys(group, "name"))] = true
	}
	for index := 1; ; index++ {
		name := aiProviderV8Family(brand) + "-" + strconv.Itoa(index)
		if !names[name] {
			return name
		}
	}
}

func aiProviderV8OpenAIGroup(flat map[string]any) map[string]any {
	group := cloneAIProviderMap(flat)
	group["keys"] = group["api-key-entries"]
	delete(group, "api-key-entries")
	return group
}

func (a *App) updateAIProviderV8(ctx context.Context, cfg AppConfig, raw map[string]any, etag string, brand aiProviderBrandConfig, index int, payload aiProviderItem) (aiProvidersResponse, error) {
	entries, err := aiProviderV8Entries(raw, cfg, brand)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	target, err := aiProviderV8Target(entries, brand, selectorFromAIProviderPayload(payload))
	if err != nil {
		return aiProvidersResponse{}, err
	}
	groups, _ := aiProviderV8Groups(raw, brand)
	next, err := aiProviderPayloadToUpstream(brand, payload, target.Effective, false)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	if brand.Brand == aiProviderBrandOpenAICompatibility {
		beforePayload := aiProviderItemFromRaw(brand, target.Index, target.Effective)
		before, err := aiProviderPayloadToUpstream(brand, beforePayload, target.Effective, false)
		if err != nil {
			return aiProvidersResponse{}, err
		}
		group := cloneAIProviderMap(target.Group)
		for field := range before {
			if !aiProviderJSONEqual(before[field], next[field]) {
				if field == "api-key-entries" {
					group["keys"] = next[field]
				} else {
					aiProviderV8CopyField(group, next, field)
				}
			}
		}
		for field := range next {
			if _, exists := before[field]; !exists {
				aiProviderV8CopyField(group, next, field)
			}
		}
		for _, field := range aiProviderPolicyWriteFields(payload) {
			aiProviderV8CopyField(group, next, field)
		}
		groups[target.GroupIndex] = group
	} else {
		// Diff normalized views, then apply only changed fields to the raw key.
		// This keeps inherited defaults, explicit nulls, and opaque values intact.
		beforePayload := aiProviderItemFromRaw(brand, target.Index, target.Effective)
		before, err := aiProviderPayloadToUpstream(brand, beforePayload, target.Effective, false)
		if err != nil {
			return aiProvidersResponse{}, err
		}
		key := cloneAIProviderMap(target.Key)
		group := cloneAIProviderMap(target.Group)
		fields := make(map[string]bool, len(before)+len(next))
		for field := range before {
			fields[field] = true
		}
		for field := range next {
			fields[field] = true
		}
		forced := make(map[string]bool)
		for _, field := range aiProviderPolicyWriteFields(payload) {
			forced[field] = true
		}
		for field := range fields {
			if !forced[field] && aiProviderJSONEqual(before[field], next[field]) {
				continue
			}
			if field == "base-url" {
				if len(target.Group["keys"].([]any)) != 1 {
					return aiProvidersResponse{}, conflictError("此配置来自包含多把 Key 的 CPA 分组，修改地址会影响其他 Key；请先在 CPA 中调整分组")
				}
				aiProviderV8CopyField(group, next, field)
			} else {
				aiProviderV8CopyField(key, next, field)
			}
		}
		// A null clear can be absent in both normalized views.
		for field := range forced {
			aiProviderV8CopyField(key, next, field)
		}
		if _, exists := key["disabled"]; exists {
			delete(key, "disabled")
			aiProviderV8CopyField(key, next, "excluded-models")
		}
		if brand.Brand == aiProviderBrandXAI {
			delete(key, "alpha-search")
			delete(key, "alpha_search")
		}
		keys := group["keys"].([]any)
		keys[target.KeyIndex] = key
		group["keys"] = keys
		groups[target.GroupIndex] = group
	}
	if err := a.putAIProviderV8Groups(ctx, cfg, etag, brand, groups); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProviderSnapshotAfterWrite(ctx, cfg)
}

func aiProviderV8CopyField(target, source map[string]any, field string) {
	if value, exists := source[field]; exists {
		target[field] = cloneAIProviderValue(value)
	} else {
		delete(target, field)
	}
}

func (a *App) deleteAIProviderV8(ctx context.Context, cfg AppConfig, raw map[string]any, etag string, brand aiProviderBrandConfig, index int, selector aiProviderSelector) (aiProvidersResponse, error) {
	entries, err := aiProviderV8Entries(raw, cfg, brand)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	target, err := aiProviderV8Target(entries, brand, selector)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	groups, _ := aiProviderV8Groups(raw, brand)
	if target.KeyIndex < 0 {
		groups = append(groups[:target.GroupIndex], groups[target.GroupIndex+1:]...)
	} else {
		group := cloneAIProviderMap(target.Group)
		keys := group["keys"].([]any)
		group["keys"] = append(keys[:target.KeyIndex], keys[target.KeyIndex+1:]...)
		groups[target.GroupIndex] = group
	}
	if err := a.putAIProviderV8Groups(ctx, cfg, etag, brand, groups); err != nil {
		return aiProvidersResponse{}, err
	}
	return a.aiProviderSnapshotAfterWrite(ctx, cfg)
}

func (a *App) reorderAIProvidersV8(ctx context.Context, cfg AppConfig, raw map[string]any, etag string, brand aiProviderBrandConfig, payload aiProviderOrderRequest) (aiProvidersResponse, error) {
	entries, err := aiProviderV8Entries(raw, cfg, brand)
	if err != nil {
		return aiProvidersResponse{}, err
	}
	if payload.Order == nil {
		return aiProvidersResponse{}, validationError("AI provider 排序顺序不能为空")
	}
	order := *payload.Order
	seen := make(map[int]bool, len(order))
	for _, reference := range order {
		if reference.Index == nil || *reference.Index < 0 || *reference.Index >= len(entries) {
			return aiProvidersResponse{}, validationError("AI provider 排序索引无效")
		}
		if seen[*reference.Index] {
			return aiProvidersResponse{}, validationError("AI provider 排序索引不能重复")
		}
		seen[*reference.Index] = true
	}
	if len(order) != len(entries) {
		return aiProvidersResponse{}, conflictError("AI provider 列表已变化，请刷新后重试")
	}
	groups, _ := aiProviderV8Groups(raw, brand)
	groupOrder := make([]int, 0, len(groups))
	groupKeys := make(map[int][]any)
	lastGroup := -1
	changed := false
	for position, reference := range order {
		target, err := aiProviderV8Target(entries, brand, selectorFromAIProviderOrderItem(reference))
		if err != nil {
			return aiProvidersResponse{}, err
		}
		if target.Index != *reference.Index {
			return aiProvidersResponse{}, conflictError("AI provider 顺序已变化，请刷新后重试")
		}
		changed = changed || target.Index != position
		if target.GroupIndex != lastGroup {
			if _, exists := groupKeys[target.GroupIndex]; exists {
				return aiProvidersResponse{}, conflictError("此排序会拆开 CPA 中的多 Key 分组；请保持同一分组连续，或先在 CPA 中调整分组")
			}
			groupOrder = append(groupOrder, target.GroupIndex)
			groupKeys[target.GroupIndex] = []any{}
			lastGroup = target.GroupIndex
		}
		if target.KeyIndex >= 0 {
			groupKeys[target.GroupIndex] = append(groupKeys[target.GroupIndex], target.Key)
		}
	}
	if !changed {
		return a.aiProvidersSnapshotForConfig(ctx, cfg)
	}
	result := make([]map[string]any, 0, len(groups))
	nextGroup := 0
	for _, original := range groups {
		if brand.Brand != aiProviderBrandOpenAICompatibility && len(original["keys"].([]any)) == 0 {
			result = append(result, original)
			continue
		}
		groupIndex := groupOrder[nextGroup]
		nextGroup++
		group := cloneAIProviderMap(groups[groupIndex])
		if brand.Brand != aiProviderBrandOpenAICompatibility {
			group["keys"] = groupKeys[groupIndex]
		}
		result = append(result, group)
	}
	if err := a.putAIProviderV8Groups(ctx, cfg, etag, brand, result); err != nil {
		return aiProvidersResponse{}, err
	}
	response, err := a.aiProvidersSnapshotForConfig(ctx, cfg)
	if err != nil {
		return aiProvidersResponse{}, appError(aiProviderOrderRefreshCode, http.StatusConflict,
			"AI provider 顺序已写入远端，列表需要刷新后才能继续操作")
	}
	return response, nil
}

func (a *App) putAIProviderV8Groups(ctx context.Context, cfg AppConfig, etag string, brand aiProviderBrandConfig, groups []map[string]any) error {
	path := "/v8/management/config/api-keys/" + aiProviderV8Family(brand)
	headers := http.Header{}
	if etag != "" {
		headers.Set("If-Match", etag)
	}
	persistent := aiProviderV8PersistentGroups(groups)
	response, _, err := a.aiProviderManagementResponseWithHeaders(ctx, cfg, http.MethodPut, path, nil, headers, persistent, aiProviderManagementTimeout)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusPreconditionFailed {
			return conflictError("CPA 配置已被其他工具修改，请刷新后重试")
		}
		return validationError(fmt.Sprintf("写入 %s provider 失败：HTTP %d", brand.Label, response.StatusCode))
	}
	a.invalidateModelPriceSelectorSnapshot(cfg)
	raw, supported, err := a.aiProviderV8Config(ctx, cfg)
	if err != nil || !supported {
		return aiProviderCommittedVerificationError()
	}
	actual, err := aiProviderV8Groups(raw, brand)
	if err != nil || !aiProviderJSONEqual(persistent, aiProviderV8PersistentGroups(actual)) {
		return aiProviderCommittedVerificationError()
	}
	return nil
}

func aiProviderV8PersistentGroups(groups []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		result = append(result, aiProviderV8PersistentGroup(group))
	}
	return result
}

func aiProviderV8PersistentGroup(group map[string]any) map[string]any {
	result := cloneAIProviderMap(group)
	delete(result, "auth_index")
	delete(result, "auth-index")
	if keys, ok := result["keys"].([]any); ok {
		for _, value := range keys {
			key := value.(map[string]any)
			delete(key, "auth_index")
			delete(key, "auth-index")
		}
	}
	return result
}

func aiProviderV8ActionList(raw map[string]any, cfg AppConfig, brand aiProviderBrandConfig, payload aiProviderItem) ([]map[string]any, int, error) {
	entries, err := aiProviderV8Entries(raw, cfg, brand)
	if err != nil {
		return nil, 0, err
	}
	target, err := aiProviderV8Target(entries, brand, selectorFromAIProviderPayload(payload))
	if err != nil {
		return nil, 0, err
	}
	list := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry.Effective)
	}
	return list, target.Index, nil
}
