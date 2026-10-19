package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const cpaHelperPluginManagementPrefix = "/v0/management/plugins/cpa-helper-plugin/v1"

const pluginKeyMetadataField = "helper_key_metadata"

type cpaHelperPluginKeyMetadata struct {
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	Description string `json:"description"`
	KeyPreview  string `json:"key_preview"`
}

type cpaHelperPluginClientError struct {
	status int
	code   string
}

func (e *cpaHelperPluginClientError) Error() string {
	return "CPA Helper Plugin management request failed"
}

func pluginCallerScope(rawKey string) string {
	sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(rawKey)))
	return hex.EncodeToString(sum[:])
}

func newIdempotencyKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("cpa-helper-%d", time.Now().UnixNano())
}

func pluginErrorFromResponse(status int, payload []byte) error {
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &problem)
	// Only recognize documented codes. The upstream message, body and unknown
	// codes never cross the browser boundary.
	code := ""
	switch problem.Error.Code {
	case "model_catalog_unavailable", "model_catalog_out_of_sync":
		code = problem.Error.Code
	}
	return &cpaHelperPluginClientError{status: status, code: code}
}

func (a *App) cpaHelperPluginRequest(ctx context.Context, cfg AppConfig, method, path string, body []byte, headers http.Header) (int, http.Header, []byte, error) {
	if strings.TrimSpace(cfg.Collector.CLIProxyURL) == "" || strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		return 0, nil, nil, appError("plugin_not_configured", http.StatusServiceUnavailable, apiKeySyncMissingConfigMessage)
	}
	managementURL, err := collectorManagementHTTPURL(cfg.Collector.CLIProxyURL)
	if err != nil {
		return 0, nil, nil, validationError("CLIProxyAPI 管理地址无效：" + err.Error())
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	target := strings.TrimRight(managementURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	for key, values := range managementHeaders(cfg.Collector.ManagementKey) {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(10 * time.Second).Do(req)
	if err != nil {
		return 0, nil, nil, appError("plugin_unavailable", http.StatusServiceUnavailable, "CPA Helper Plugin 不可达")
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return resp.StatusCode, resp.Header.Clone(), nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, resp.Header.Clone(), payload, pluginErrorFromResponse(resp.StatusCode, payload)
	}
	return resp.StatusCode, resp.Header.Clone(), payload, nil
}

func appErrorFromPlugin(err error, status int) error {
	var remote *cpaHelperPluginClientError
	if !errors.As(err, &remote) {
		return err
	}
	if remote.status >= 500 && remote.code != "" {
		return pluginModelCatalogError(remote.code)
	}
	switch remote.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return appError("plugin_auth_failed", http.StatusBadGateway, "CPA Helper Plugin 管理端认证失败，请检查管理密钥")
	case http.StatusConflict:
		return conflictError("CPA Helper Plugin 配置已被其他管理端修改，请刷新后重试")
	case http.StatusPreconditionRequired:
		return appError("precondition_required", http.StatusPreconditionRequired, "CPA Helper Plugin 写入缺少版本前置条件")
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return appError("plugin_validation_failed", http.StatusBadRequest, "CPA Helper Plugin 配置校验失败")
	case http.StatusNotFound:
		return appError("plugin_not_loaded", http.StatusServiceUnavailable, "CPA Helper Plugin 未加载或不支持当前管理契约")
	default:
		if remote.status >= 500 || status >= 500 {
			return appError("plugin_unavailable", http.StatusServiceUnavailable, fmt.Sprintf("CPA Helper Plugin 暂不可用（上游 HTTP %d）", remote.status))
		}
		return appError("plugin_request_failed", http.StatusBadGateway, fmt.Sprintf("CPA Helper Plugin 请求失败（上游 HTTP %d）", remote.status))
	}
}

func decodePluginObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, validationError("CPA Helper Plugin 返回的 JSON 不是对象")
	}
	return object, nil
}

func validatePluginPolicy(object map[string]json.RawMessage) error {
	if rawString(object["contract_version"]) != "v1" || rawUint(object["policy_revision"]) == 0 {
		return pluginIncompatibleError()
	}
	return validatePluginPolicyArrays(object, true)
}

func pluginIncompatibleError() error {
	return appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 管理契约不兼容")
}

func validatePluginPolicyArrays(object map[string]json.RawMessage, upstream bool) error {
	for _, field := range []string{"groups", "keys"} {
		raw, ok := object[field]
		if !ok || rawJSONNull(raw) {
			if upstream {
				return pluginIncompatibleError()
			}
			return validationError("CPA Helper Plugin 策略快照格式无效（groups/keys 必须为对象数组）")
		}
		if _, err := rawObjectArray(object, field); err != nil {
			if upstream {
				return pluginIncompatibleError()
			}
			return validationError("CPA Helper Plugin 策略快照格式无效（groups/keys 必须为对象数组）")
		}
	}
	return nil
}

func rawObjectArray(object map[string]json.RawMessage, key string) ([]map[string]json.RawMessage, error) {
	raw, ok := object[key]
	if !ok {
		return []map[string]json.RawMessage{}, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, validationError("CPA Helper Plugin 策略字段格式无效")
	}
	for _, item := range items {
		if item == nil {
			return nil, validationError("CPA Helper Plugin 策略字段格式无效")
		}
	}
	return items, nil
}

func cloneRawObject(object map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func rawUint(raw json.RawMessage) uint64 {
	var value uint64
	_ = json.Unmarshal(raw, &value)
	return value
}

func mapToRawObject(value map[string]json.RawMessage) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func (a *App) cpaHelperPluginPolicy(ctx context.Context, cfg AppConfig) (map[string]json.RawMessage, string, error) {
	status, headers, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/policy", nil, nil)
	if err != nil {
		return nil, "", appErrorFromPlugin(err, status)
	}
	object, err := decodePluginObject(payload)
	if err != nil {
		return nil, "", err
	}
	if err := validatePluginPolicy(object); err != nil {
		return nil, "", err
	}
	if err := a.mergeLocalAPIKeysIntoPluginPolicy(ctx, object); err != nil {
		return nil, "", err
	}
	if err := normalizePluginPolicy(object); err != nil {
		return nil, "", err
	}
	etag := strings.TrimSpace(headers.Get("ETag"))
	if etag == "" {
		if revision, ok := object["policy_revision"]; ok {
			etag = fmt.Sprintf("\"%d\"", rawUint(revision))
		}
	}
	return object, etag, nil
}

// mergeLocalAPIKeysIntoPluginPolicy makes every CPA-Helper API key visible to
// the super-admin route-rule editor. The plugin snapshot only materializes
// keys after a policy write, so an otherwise valid local key can be absent
// from the remote snapshot. Local key descriptions own their display labels;
// all other fields already returned by the plugin remain intact.
func (a *App) mergeLocalAPIKeysIntoPluginPolicy(ctx context.Context, policy map[string]json.RawMessage) error {
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return err
	}
	existing := make(map[string]int, len(keys))
	for index, key := range keys {
		id := rawString(key["id"])
		if id == "" {
			continue
		}
		existing[id] = index
		normalized := cloneRawObject(key)
		if _, ok := normalized["group_ids"]; !ok {
			normalized["group_ids"] = mustRawStringArray(nil)
		}
		if _, ok := normalized["enabled"]; !ok {
			normalized["enabled"] = mustRawBool(true)
		}
		if _, ok := normalized["max_concurrency"]; !ok {
			normalized["max_concurrency"] = mustRawNumber(0)
		}
		if _, ok := normalized["rule"]; !ok {
			normalized["rule"] = mustRawObject(map[string]json.RawMessage{})
		}
		keys[index] = normalized
	}
	localKeys, err := a.keySummaries(ctx)
	if err != nil {
		return err
	}
	for _, local := range localKeys {
		if local.APIKey == nil || strings.TrimSpace(*local.APIKey) == "" {
			continue
		}
		id := pluginCallerScope(*local.APIKey)
		label := pluginLocalKeyLabel(local.Description, id)
		if index, exists := existing[id]; exists {
			keys[index]["label"] = mustRawString(label)
			continue
		}
		keys = append(keys, map[string]json.RawMessage{
			"id": mustRawString(id), "label": mustRawString(label),
			"enabled": mustRawBool(true), "group_ids": mustRawStringArray(nil),
			"max_concurrency": mustRawNumber(0), "rule": mustRawObject(map[string]json.RawMessage{}),
		})
		existing[id] = len(keys) - 1
	}
	sort.Slice(keys, func(i, j int) bool { return rawString(keys[i]["id"]) < rawString(keys[j]["id"]) })
	policy["keys"] = mustRaw(keys)
	return nil
}

func pluginLocalKeyLabel(description, scope string) string {
	if label := strings.TrimSpace(description); label != "" {
		return label
	}
	return scope[:8]
}

// Only project labels for keys present in the submitted snapshot. A global
// policy edit may deliberately remove a key entry to restore its default rule.
func (a *App) projectLocalPluginKeyLabels(ctx context.Context, policy map[string]json.RawMessage) error {
	summaries, err := a.keySummaries(ctx)
	if err != nil {
		return err
	}
	labels := make(map[string]string, len(summaries))
	for _, summary := range summaries {
		if summary.APIKey != nil && strings.TrimSpace(*summary.APIKey) != "" {
			scope := pluginCallerScope(*summary.APIKey)
			labels[scope] = pluginLocalKeyLabel(summary.Description, scope)
		}
	}
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if label, local := labels[rawString(key["id"])]; local {
			key["label"] = mustRawString(label)
		}
	}
	policy["keys"] = mustRaw(keys)
	return nil
}

// normalizePluginPolicy keeps the Helper API stable when an older plugin
// snapshot omits optional key/group fields or returns them as null. The
// plugin still receives the original unknown fields unchanged.
func normalizePluginPolicy(policy map[string]json.RawMessage) error {
	if err := validatePluginPolicyArrays(policy, false); err != nil {
		return err
	}
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := normalizePluginPolicyKey(key); err != nil {
			return err
		}
	}
	rawKeys, _ := json.Marshal(keys)
	policy["keys"] = rawKeys
	groups, err := rawObjectArray(policy, "groups")
	if err != nil {
		return err
	}
	for _, group := range groups {
		if err := normalizePluginPolicyRule(group); err != nil {
			return err
		}
	}
	rawGroups, _ := json.Marshal(groups)
	policy["groups"] = rawGroups
	return nil
}

func normalizePluginPolicyKey(key map[string]json.RawMessage) error {
	if raw, ok := key["enabled"]; !ok || rawJSONNull(raw) {
		key["enabled"] = mustRawBool(true)
	}
	if raw, ok := key["group_ids"]; !ok || rawJSONNull(raw) {
		key["group_ids"] = mustRawStringArray(nil)
	} else {
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return validationError("CPA Helper Plugin API 密钥 group_ids 字段格式无效")
		}
	}
	if raw, ok := key["max_concurrency"]; !ok || rawJSONNull(raw) {
		key["max_concurrency"] = mustRawNumber(0)
	}
	return normalizePluginPolicyRule(key)
}

func normalizePluginPolicyRule(object map[string]json.RawMessage) error {
	raw, ok := object["rule"]
	if !ok || rawJSONNull(raw) {
		object["rule"] = mustRawObject(map[string]json.RawMessage{})
		return nil
	}
	var rule map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rule); err != nil || rule == nil {
		return validationError("CPA Helper Plugin 规则字段格式无效")
	}
	return nil
}

func rawJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// enrichPluginPolicyKeys makes every currently issued CPA-Helper key
// selectable in the super-admin route-rule editor. The plugin may only list
// keys that have already received a policy entry, while Helper can issue a
// key before its first policy edit. Missing entries remain unrestricted until
// the administrator explicitly binds a route rule.
func (a *App) enrichPluginPolicyKeys(ctx context.Context, policy map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	summaries, err := a.keySummaries(ctx)
	if err != nil {
		return nil, err
	}
	users, err := a.allUsers(ctx)
	if err != nil {
		return nil, err
	}
	usersByID := make(map[int]UserRecord, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}
	metadata := make(map[string]cpaHelperPluginKeyMetadata, len(summaries))
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]json.RawMessage, len(keys)+len(summaries))
	for _, key := range keys {
		if err := normalizePluginPolicyKey(key); err != nil {
			return nil, err
		}
		if id := rawString(key["id"]); id != "" {
			byID[id] = key
		}
	}
	for _, summary := range summaries {
		if summary.APIKey == nil || strings.TrimSpace(*summary.APIKey) == "" {
			continue
		}
		id := pluginCallerScope(*summary.APIKey)
		label := pluginLocalKeyLabel(summary.Description, id)
		if summary.UserID != nil {
			if owner, exists := usersByID[*summary.UserID]; exists {
				metadata[id] = cpaHelperPluginKeyMetadata{
					Username: owner.Username, Nickname: owner.Nickname,
					Description: summary.Description, KeyPreview: pluginMaskKey(*summary.APIKey),
				}
			}
		}
		if key, exists := byID[id]; exists {
			key["label"] = mustRawString(label)
			continue
		}
		byID[id] = map[string]json.RawMessage{
			"id": mustRawString(id), "label": mustRawString(label),
			"enabled": mustRawBool(true), "group_ids": mustRawStringArray(nil),
			"max_concurrency": mustRawNumber(0),
			"rule":            mustRawObject(map[string]json.RawMessage{}),
		}
	}
	// Preserve the plugin's existing order, then append newly discovered keys
	// in a stable order so route-rule edits do not reshuffle on refresh.
	seen := make(map[string]bool, len(keys))
	merged := make([]map[string]json.RawMessage, 0, len(byID))
	for _, key := range keys {
		id := rawString(key["id"])
		if id != "" && !seen[id] {
			merged = append(merged, byID[id])
			seen[id] = true
		}
	}
	missing := make([]string, 0, len(byID)-len(merged))
	for id := range byID {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		merged = append(merged, byID[id])
	}
	result := cloneRawObject(policy)
	result["keys"] = mustRaw(merged)
	// Helper-only display metadata is never part of the upstream policy.
	result[pluginKeyMetadataField] = mustRaw(metadata)
	return result, nil
}

func (a *App) cpaHelperPluginCapabilities(ctx context.Context, cfg AppConfig) (map[string]json.RawMessage, error) {
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/capabilities", nil, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	capabilities, err := decodePluginObject(payload)
	if err != nil {
		return nil, err
	}
	if rawString(capabilities["contract_version"]) != "v1" {
		return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 管理契约不兼容")
	}
	if _, err := pluginUsesV8Management(capabilities); err != nil {
		return nil, err
	}
	return capabilities, nil
}

// pluginUsesV8Management selects the host management bridge for known releases.
// cpa_version is the plugin's build target, not the running CPA version.
func pluginUsesV8Management(capabilities map[string]json.RawMessage) (bool, error) {
	var expectedCPATarget string
	switch rawString(capabilities["plugin_version"]) {
	case "0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4":
		// Preserve the legacy config/auth-files contract for older releases.
		return false, nil
	case "0.1.5":
		expectedCPATarget = "v8.0.12"
	case "0.1.6":
		expectedCPATarget = "v8.0.17"
	case "0.1.7":
		expectedCPATarget = "v8.0.29"
	case "0.1.8":
		expectedCPATarget = pluginModelCatalogCPA
	default:
		return false, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 版本不兼容")
	}
	if rawString(capabilities["cpa_version"]) != expectedCPATarget {
		return false, pluginIncompatibleError()
	}
	return true, nil
}

type cpaHelperPluginKeySummary struct {
	APIKeyHash         string   `json:"api_key_hash"`
	Enabled            bool     `json:"enabled"`
	GroupNames         []string `json:"group_names"`
	HasCustomRule      bool     `json:"has_custom_rule"`
	CurrentConcurrency int      `json:"current_concurrency"`
	MaxConcurrency     int      `json:"max_concurrency"`
}

// pluginConcurrencySummary is deliberately aggregate-only. The plugin health
// payload contains caller-scope hashes; those are never exposed to the UI.
type pluginConcurrencySummary struct {
	Scope              string `json:"scope"`
	CurrentConcurrency int    `json:"current_concurrency"`
}

func pluginActiveConcurrency(health map[string]json.RawMessage) (int, error) {
	var active map[string]json.RawMessage
	if err := json.Unmarshal(health["active"], &active); err != nil || active == nil {
		return 0, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据缺失或无效")
	}
	total := 0
	maxInt := int(^uint(0) >> 1)
	for _, raw := range active {
		var count int
		if err := json.Unmarshal(raw, &count); err != nil || rawJSONNull(raw) || count < 0 {
			return 0, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据缺失或无效")
		}
		if count > maxInt-total {
			return 0, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据超出可表示范围")
		}
		total += count
	}
	return total, nil
}

func (a *App) pluginConcurrencySummary(ctx context.Context, user *AuthUser, cfg AppConfig, scope string) (pluginConcurrencySummary, error) {
	if scope != "admin" && scope != "account" {
		return pluginConcurrencySummary{}, validationError("scope 参数无效")
	}
	if scope == "admin" && !user.IsAdmin {
		return pluginConcurrencySummary{}, forbiddenError("需要管理员权限")
	}
	if scope == "account" {
		keys, err := a.pluginKeysSummary(ctx, user, cfg)
		if err != nil {
			return pluginConcurrencySummary{}, err
		}
		total := 0
		maxInt := int(^uint(0) >> 1)
		for _, key := range keys {
			if key.CurrentConcurrency > maxInt-total {
				return pluginConcurrencySummary{}, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据超出可表示范围")
			}
			total += key.CurrentConcurrency
		}
		return pluginConcurrencySummary{Scope: scope, CurrentConcurrency: total}, nil
	}
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
	if err != nil {
		return pluginConcurrencySummary{}, appErrorFromPlugin(err, status)
	}
	health, err := decodePluginObject(payload)
	if err != nil {
		return pluginConcurrencySummary{}, err
	}
	total, err := pluginActiveConcurrency(health)
	if err != nil {
		return pluginConcurrencySummary{}, err
	}
	return pluginConcurrencySummary{Scope: scope, CurrentConcurrency: total}, nil
}

func (a *App) pluginKeysSummary(ctx context.Context, user *AuthUser, cfg AppConfig) ([]cpaHelperPluginKeySummary, error) {
	localKeys, err := a.keySummariesForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	policy, _, err := a.cpaHelperPluginPolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	health, err := decodePluginObject(payload)
	if err != nil {
		return nil, err
	}
	var active map[string]json.RawMessage
	if err := json.Unmarshal(health["active"], &active); err != nil || active == nil {
		return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据缺失或无效")
	}
	counts := make(map[string]int, len(active))
	for scope, raw := range active {
		var count int
		if err := json.Unmarshal(raw, &count); err != nil || rawJSONNull(raw) || count < 0 {
			return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 当前并发数据缺失或无效")
		}
		counts[scope] = count
	}
	groups, err := rawObjectArray(policy, "groups")
	if err != nil {
		return nil, err
	}
	groupNames := make(map[string]string, len(groups))
	for _, group := range groups {
		groupNames[rawString(group["id"])] = rawString(group["name"])
	}
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return nil, err
	}
	byScope := make(map[string]map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		byScope[rawString(key["id"])] = key
	}
	summaries := make([]cpaHelperPluginKeySummary, 0, len(localKeys))
	for _, localKey := range localKeys {
		if localKey.APIKey == nil || strings.TrimSpace(*localKey.APIKey) == "" {
			return nil, conflictError("当前 API 密钥缺少完整密钥，无法映射插件策略")
		}
		scope := pluginCallerScope(*localKey.APIKey)
		summary := cpaHelperPluginKeySummary{
			APIKeyHash: localKey.APIKeyHash, Enabled: true, GroupNames: []string{},
			// Counts omits scopes once their last admitted request completes.
			CurrentConcurrency: counts[scope],
		}
		if key, exists := byScope[scope]; exists {
			if err := json.Unmarshal(key["enabled"], &summary.Enabled); err != nil {
				return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin API 密钥启用状态无效")
			}
			var groupIDs []string
			if err := json.Unmarshal(key["group_ids"], &groupIDs); err != nil {
				return nil, validationError("CPA Helper Plugin API 密钥 group_ids 字段格式无效")
			}
			for _, groupID := range groupIDs {
				name, exists := groupNames[groupID]
				if !exists || name == "" {
					return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 路由规则关联无效")
				}
				summary.GroupNames = append(summary.GroupNames, name)
			}
			if err := json.Unmarshal(key["max_concurrency"], &summary.MaxConcurrency); err != nil || summary.MaxConcurrency < 0 {
				return nil, appError("plugin_incompatible", http.StatusServiceUnavailable, "CPA Helper Plugin 并发上限数据无效")
			}
			summary.HasCustomRule, err = pluginKeyHasCustomRule(key["rule"])
			if err != nil {
				return nil, err
			}
		}
		if apiKeyExpirationPassed(localKey.ExpiresAt, time.Now()) {
			summary.Enabled = false
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func pluginKeyHasCustomRule(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || rawJSONNull(raw) {
		return false, nil
	}
	var rule map[string]json.RawMessage
	if json.Unmarshal(raw, &rule) != nil || rule == nil {
		return false, pluginIncompatibleError()
	}
	custom := false
	for _, field := range []string{"models", "denied_models", "credential_ids", "denied_credential_ids", "credential_providers", "denied_credential_providers"} {
		value, exists := rule[field]
		if !exists || rawJSONNull(value) {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(value, &items) != nil {
			return false, pluginIncompatibleError()
		}
		for _, item := range items {
			if field == "credential_providers" || field == "denied_credential_providers" {
				var selector struct {
					Source   string `json:"source"`
					Provider string `json:"provider"`
				}
				if json.Unmarshal(item, &selector) != nil || strings.TrimSpace(selector.Source) == "" || strings.TrimSpace(selector.Provider) == "" {
					return false, pluginIncompatibleError()
				}
			} else {
				var selection string
				if json.Unmarshal(item, &selection) != nil || strings.TrimSpace(selection) == "" {
					return false, pluginIncompatibleError()
				}
			}
		}
		custom = custom || len(items) > 0
	}
	return custom, nil
}

func setPluginPolicyMeta(policy map[string]json.RawMessage, revision uint64) {
	revisionRaw, _ := json.Marshal(revision)
	generatedRaw, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	policy["policy_revision"] = revisionRaw
	policy["generated_at"] = generatedRaw
}

func jsonValueEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return bytes.Equal(a, b)
	}
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func (a *App) pluginKeyPolicy(ctx context.Context, user *AuthUser, localHash string, cfg AppConfig) (map[string]json.RawMessage, error) {
	key, err := a.getAPIKey(ctx, localHash)
	if err != nil {
		return nil, err
	}
	if key.UserID != user.ID && !user.IsSuperAdmin {
		return nil, notFoundError("API KEY 不存在")
	}
	if key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "" {
		return nil, conflictError("当前 API 密钥缺少完整密钥，无法映射插件策略")
	}
	scope := pluginCallerScope(*key.APIKey)
	policy, _, err := a.cpaHelperPluginPolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return nil, err
	}
	for _, item := range keys {
		if rawString(item["id"]) == scope {
			groups, _ := rawObjectArray(policy, "groups")
			result := map[string]json.RawMessage{"policy_revision": policy["policy_revision"], "key": mustRaw(item), "groups": mustRaw(groups)}
			return result, nil
		}
	}
	// A CPA key may be absent from the policy snapshot. It is unrestricted by
	// default, but the response still includes the complete group catalogue so
	// the caller can choose reusable rules before materializing the key.
	groups, err := rawObjectArray(policy, "groups")
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{
		"policy_revision": policy["policy_revision"],
		"key": mustRaw(map[string]json.RawMessage{
			"id": mustRawString(scope), "enabled": mustRawBool(true),
			"group_ids": mustRawStringArray(nil), "max_concurrency": mustRawNumber(0),
			"rule": mustRawObject(map[string]json.RawMessage{}),
		}),
		"groups": mustRaw(groups),
	}, nil
}

func mustRaw(value any) json.RawMessage          { raw, _ := json.Marshal(value); return raw }
func mustRawString(value string) json.RawMessage { return mustRaw(value) }
func mustRawBool(value bool) json.RawMessage     { return mustRaw(value) }
func mustRawNumber(value int) json.RawMessage    { return mustRaw(value) }
func mustRawStringArray(value []string) json.RawMessage {
	if value == nil {
		value = []string{}
	}
	return mustRaw(value)
}
func mustRawObject(value map[string]json.RawMessage) json.RawMessage { return mustRaw(value) }

func replacePluginKey(policy map[string]json.RawMessage, next map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	keys, err := rawObjectArray(policy, "keys")
	if err != nil {
		return nil, err
	}
	id := rawString(next["id"])
	if id == "" {
		return nil, validationError("插件 API 密钥策略缺少 id")
	}
	found := false
	for index, item := range keys {
		if rawString(item["id"]) == id {
			merged := cloneRawObject(item)
			for field, value := range next {
				merged[field] = value
			}
			keys[index] = merged
			found = true
			break
		}
	}
	if !found {
		// An authenticated CPA key is unrestricted when absent from the
		// snapshot. A direct per-key edit materializes that implicit entry.
		keys = append(keys, next)
	}
	result := make(map[string]json.RawMessage, len(policy))
	for key, value := range policy {
		result[key] = value
	}
	result["keys"] = mustRaw(keys)
	return result, nil
}

func (a *App) putCPAHelperPluginPolicy(ctx context.Context, cfg AppConfig, current map[string]json.RawMessage, policy map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if err := validatePluginPolicyArrays(policy, false); err != nil {
		return nil, err
	}
	delete(policy, pluginKeyMetadataField)
	if err := a.projectLocalPluginKeyLabels(ctx, policy); err != nil {
		return nil, err
	}
	base := rawUint(current["policy_revision"])
	setPluginPolicyMeta(policy, base+1)
	headers := http.Header{}
	headers.Set("If-Match", fmt.Sprintf("\"%d\"", base))
	headers.Set("Idempotency-Key", newIdempotencyKey())
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodPut, cpaHelperPluginManagementPrefix+"/policy", mapToRawObject(policy), headers)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	accepted, err := decodePluginObject(payload)
	if err != nil {
		return nil, err
	}
	acceptedRevision := rawUint(accepted["policy_revision"])
	if acceptedRevision != base+1 {
		return nil, appError("plugin_sync_failed", http.StatusServiceUnavailable, "插件策略已提交，但未确认目标修订号")
	}
	status, _, healthPayload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	health, err := decodePluginObject(healthPayload)
	if err != nil {
		return nil, err
	}
	if rawUint(health["policy_revision"]) != acceptedRevision {
		return nil, appError("plugin_sync_failed", http.StatusServiceUnavailable, "插件策略保存后未生效")
	}
	latest, _, err := a.cpaHelperPluginPolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if rawUint(latest["policy_revision"]) != acceptedRevision {
		return nil, appError("plugin_sync_failed", http.StatusServiceUnavailable, "插件策略保存后未生效")
	}
	return latest, nil
}

func (a *App) pluginResponseModelConfig(ctx context.Context, cfg AppConfig) (map[string]json.RawMessage, error) {
	capabilities, err := a.cpaHelperPluginCapabilities(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var config map[string]json.RawMessage
	if raw, ok := capabilities["response_model_mismatch"]; ok {
		if json.Unmarshal(raw, &config) == nil && len(config) > 0 {
			v8, versionErr := pluginUsesV8Management(capabilities)
			if versionErr != nil {
				return nil, versionErr
			}
			if v8 {
				// 0.1.5 removed this field; retaining it would make the next
				// validate request fail because the plugin rejects unknown fields.
				delete(config, "responses_only_stream")
			}
			if err := normalizePluginResponseModelConfig(config); err != nil {
				return nil, err
			}
			return config, nil
		}
	}
	return nil, validationError("CPA Helper Plugin capabilities 缺少响应核验配置")
}

func normalizePluginResponseModelConfig(config map[string]json.RawMessage) error {
	if raw, ok := config["ignored_models"]; !ok || rawJSONNull(raw) {
		config["ignored_models"] = mustRawStringArray(nil)
	} else {
		var models []any
		if err := json.Unmarshal(raw, &models); err != nil {
			return pluginIncompatibleError()
		}
		for _, model := range models {
			if _, ok := model.(string); !ok {
				return pluginIncompatibleError()
			}
		}
	}
	accepted, ok := config["accepted_models"]
	if !ok || rawJSONNull(accepted) {
		config["accepted_models"] = mustRawObject(map[string]json.RawMessage{})
		return nil
	}
	var aliases map[string]json.RawMessage
	if err := json.Unmarshal(accepted, &aliases); err != nil || aliases == nil {
		return pluginIncompatibleError()
	}
	for _, value := range aliases {
		if rawJSONNull(value) {
			return pluginIncompatibleError()
		}
		var names []any
		if err := json.Unmarshal(value, &names); err != nil || len(names) == 0 {
			return pluginIncompatibleError()
		}
		for _, name := range names {
			if _, ok := name.(string); !ok {
				return pluginIncompatibleError()
			}
		}
	}
	return nil
}

func (a *App) updatePluginResponseModelConfig(ctx context.Context, cfg AppConfig, requested map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	capabilities, capErr := a.cpaHelperPluginCapabilities(ctx, cfg)
	if capErr != nil {
		return nil, capErr
	}
	v8, versionErr := pluginUsesV8Management(capabilities)
	if versionErr != nil {
		return nil, versionErr
	}
	validateRequest := cloneRawObject(requested)
	if v8 {
		delete(validateRequest, "responses_only_stream")
	}
	validateBody, _ := json.Marshal(validateRequest)
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodPost, cpaHelperPluginManagementPrefix+"/response-model/validate", validateBody, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	validated, err := decodePluginObject(payload)
	if err != nil {
		return nil, err
	}
	if err := normalizePluginResponseModelConfig(validated); err != nil {
		return nil, err
	}
	var method, path string
	var body []byte
	if v8 {
		// The v8 bridge requires a complete object PUT so cleared mappings are
		// removed instead of recursively retained by a PATCH.
		method, path = http.MethodPut, "/v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch"
		body, _ = json.Marshal(validated)
	} else {
		method, path = http.MethodPatch, "/v0/management/plugins/cpa-helper-plugin/config"
		body, _ = json.Marshal(map[string]any{"response_model_mismatch": validated})
	}
	status, _, _, err = a.cpaHelperPluginRequest(ctx, cfg, method, path, body, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		active, readErr := a.pluginResponseModelConfig(ctx, cfg)
		if readErr != nil {
			return nil, readErr
		}
		if jsonValueEqual(mapToRawObject(active), mapToRawObject(validated)) {
			return active, nil
		}
		if time.Now().After(deadline) {
			return nil, appError("plugin_sync_failed", http.StatusServiceUnavailable, "响应核验配置已提交，但插件未确认生效")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// pluginDirectory is the small, secret-free directory consumed by both the
// route-rule editor and the per-key policy editor.  The plugin management
// directory contains observed credentials, while configured CPA credentials
// and models are exposed through CPA's own management surfaces.  Keep the
// aggregation here so raw config/API keys never cross the Helper API boundary.
func (a *App) pluginDirectory(ctx context.Context, cfg AppConfig, user *AuthUser, localHash string) (map[string]any, error) {
	var keys []string
	if localHash != "" {
		key, err := a.getAPIKey(ctx, localHash)
		if err != nil {
			return nil, err
		}
		if key.UserID != user.ID && !user.IsSuperAdmin {
			return nil, notFoundError("API KEY 不存在")
		}
		if key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "" {
			return nil, conflictError("当前 API 密钥缺少完整密钥，无法映射插件策略")
		}
		if key.expired(time.Now()) {
			return nil, conflictError("API KEY 已过期，请先修改密钥有效期")
		}
		keys = []string{*key.APIKey}
	} else if !user.IsSuperAdmin {
		localKeys, err := a.keySummariesForUser(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		for _, key := range localKeys {
			if key.APIKey != nil && strings.TrimSpace(*key.APIKey) != "" && !apiKeyExpirationPassed(key.ExpiresAt, time.Now()) {
				keys = append(keys, *key.APIKey)
			}
		}
	}
	capabilities, err := a.cpaHelperPluginCapabilities(ctx, cfg)
	if err != nil {
		return nil, err
	}
	v8Management, err := pluginUsesV8Management(capabilities)
	if err != nil {
		return nil, err
	}
	aliases, err := a.modelPriceChannelAliasLabels(ctx)
	if err != nil {
		return nil, err
	}
	status, _, payload, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/directory", nil, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	value, err := decodePluginObject(payload)
	if err != nil {
		return nil, validationError("插件目录响应格式无效")
	}
	var pluginCredentials []map[string]any
	if raw, ok := value["credentials"]; ok {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return nil, validationError("插件目录响应格式无效")
		}
		for _, entry := range entries {
			if entry == nil {
				return nil, validationError("插件目录响应格式无效")
			}
			// Deliberately copy only the documented display fields.  A future
			// plugin field must not accidentally expose credential material.
			credential := map[string]any{
				"ref": pluginRawString(entry["ref"]), "provider": pluginRawString(entry["provider"]),
				"source": pluginRawString(entry["source"]), "label": pluginRawString(entry["label"]),
				"status": pluginRawString(entry["status"]),
			}
			if authIndex := pluginRawString(entry["auth_index"]); authIndex != "" {
				credential["auth_index"] = authIndex
			}
			pluginCredentials = append(pluginCredentials, credential)
		}
	} else {
		return nil, validationError("插件目录响应缺少 credentials")
	}

	catalogSupported := pluginSupportsModelCatalog(capabilities)
	var catalog pluginCatalogSyncResult
	var config map[string]any
	var hostCredentials []pluginHostCredential
	var files []map[string]any
	if catalogSupported {
		catalog, err = a.syncPluginModelCatalog(ctx, cfg, capabilities)
		if err != nil {
			return nil, err
		}
		config, hostCredentials = catalog.Config, catalog.Credentials
	} else {
		configPath, credentialsPath := "/v0/management/config", "/v0/management/auth-files"
		if v8Management {
			configPath, credentialsPath = "/v8/management/config", "/v8/management/credentials"
		}
		rawConfig, readErr := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, configPath, nil, aiProviderManagementTimeout)
		if readErr != nil {
			return nil, readErr
		}
		config, err = pluginDecodeNumberObject(rawConfig)
		if err != nil {
			return nil, validationError("CLIProxyAPI 配置响应不是有效 JSON object")
		}
		rawCredentials, readErr := a.aiProviderManagementPayload(ctx, cfg, http.MethodGet, credentialsPath, nil, aiProviderManagementTimeout)
		if readErr != nil {
			return nil, readErr
		}
		files, err = pluginDirectoryObjects(rawCredentials, []string{"credentials", "files", "items", "data", "value"})
		if err != nil {
			return nil, validationError("CPA 认证文件目录格式无效")
		}
		if v8Management {
			hostCredentials, err = pluginV8HostCredentials(config, files)
			if err != nil {
				return nil, err
			}
		}
	}

	credentialsByRef := make(map[string]map[string]any, len(pluginCredentials))
	for _, entry := range pluginCredentials {
		if ref := strings.TrimSpace(pluginAnyText(entry["ref"])); ref != "" {
			credentialsByRef[ref] = entry
		}
	}
	if v8Management {
		if err := pluginMergeHostDirectory(credentialsByRef, hostCredentials, aliases); err != nil {
			return nil, err
		}
	} else {
		if err := pluginAddConfiguredCredentialsWithAliases(credentialsByRef, config, aliases); err != nil {
			return nil, err
		}
	}
	if !v8Management {
		for _, file := range files {
			id := pluginAnyText(file["id"])
			if id == "" {
				continue
			}
			if pluginDirectoryFileExcluded(file) {
				continue
			}
			provider := strings.ToLower(pluginAnyString(file, "provider", "type"))
			label := pluginJoinLabel(pluginAnyString(file, "name", "id"), pluginAnyString(file, "label"), pluginAnyString(file, "email", "account"))
			status := pluginAnyString(file, "status")
			if pluginAnyBool(file["disabled"]) {
				status = "disabled"
			}
			ref := pluginCredentialRef(id)
			entry := map[string]any{"ref": ref, "source": "auth-files", "provider": provider, "label": label, "status": status}
			credentialsByRef[ref] = entry
		}
	}
	models := []string{}
	// Global rules need the union of every downstream key's catalog. Account
	// and per-key editors only query their own selected keys. Downstream keys
	// are query identities, never scheduler credentials or provider categories.
	if user.IsSuperAdmin && localHash == "" {
		if catalogSupported {
			models = pluginCatalogModels(catalog.Snapshot)
		} else if v8Management {
			keys, err = pluginV8DownstreamKeys(config)
		} else {
			keys, err = a.remoteAPIKeys(ctx, cfg)
		}
		if err != nil {
			return nil, err
		}
	}
	modelConfig := cfg
	modelConfig.Collector.CLIProxyURL, err = collectorManagementHTTPURL(cfg.Collector.CLIProxyURL)
	if err != nil {
		return nil, validationError("CLIProxyAPI 管理地址无效：" + err.Error())
	}
	seenKeys := make(map[string]bool, len(keys))
	seenModels := make(map[string]bool)
	for _, rawKey := range keys {
		rawKey = strings.TrimSpace(rawKey)
		if rawKey == "" || seenKeys[rawKey] {
			continue
		}
		seenKeys[rawKey] = true
		items, fetchErr := fetchAvailableModelItems(ctx, modelConfig, rawKey)
		if fetchErr != nil {
			var problem *AppError
			if errors.As(fetchErr, &problem) {
				return nil, fetchErr
			}
			return nil, validationError("查询 CPA 模型目录失败：" + fetchErr.Error())
		}
		for _, item := range items {
			model := ""
			switch typed := item.(type) {
			case string:
				model = strings.TrimSpace(typed)
			case map[string]any:
				model = strings.TrimSpace(pluginAnyString(typed, "id", "model", "name"))
			}
			if model != "" && !seenModels[model] {
				seenModels[model] = true
				models = append(models, model)
			}
		}
	}
	sort.Strings(models)
	credentials := make([]map[string]any, 0, len(credentialsByRef))
	for _, entry := range credentialsByRef {
		credentials = append(credentials, entry)
	}
	sort.Slice(credentials, func(i, j int) bool {
		return pluginAnyText(credentials[i]["ref"]) < pluginAnyText(credentials[j]["ref"])
	})
	providersByID := map[string]map[string]any{}
	for _, entry := range credentials {
		source, provider := pluginAnyText(entry["source"]), pluginAnyText(entry["provider"])
		if source == "" || provider == "" {
			continue
		}
		id := source + "/" + provider
		if _, exists := providersByID[id]; !exists {
			providersByID[id] = map[string]any{"source": source, "provider": provider}
		}
	}
	providers := make([]map[string]any, 0, len(providersByID))
	for _, provider := range providersByID {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool {
		left := pluginAnyText(providers[i]["source"]) + "/" + pluginAnyText(providers[i]["provider"])
		right := pluginAnyText(providers[j]["source"]) + "/" + pluginAnyText(providers[j]["provider"])
		return left < right
	})
	return map[string]any{"models": models, "credentials": credentials, "credential_providers": providers}, nil
}

func pluginRawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func pluginDecodeNumberObject(raw []byte) (map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("invalid JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return object, nil
}

func pluginDirectoryObjects(raw []byte, keys []string) ([]map[string]any, error) {
	value, err := pluginDecodeNumberObject(raw)
	if err != nil {
		return nil, err
	}
	var list any = value
	for _, key := range keys {
		if candidate, ok := value[key]; ok {
			list = candidate
			break
		}
	}
	items, ok := list.([]any)
	if !ok {
		return nil, errors.New("directory list is not an array")
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("directory entry is not an object")
		}
		result = append(result, object)
	}
	return result, nil
}

func pluginAnyString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func pluginAnyText(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func pluginAnyBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func pluginDirectoryFileExcluded(file map[string]any) bool {
	if pluginAnyBool(file["runtime_only"]) {
		return true
	}
	source := strings.ToLower(pluginAnyString(file, "source"))
	return source == "memory" || source == "config" || strings.HasPrefix(source, "config:")
}

func pluginJoinLabel(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " · ")
}

func pluginCredentialRef(id string) string {
	sum := sha256.Sum256([]byte("cpa-key-billing:credential:v1\x00" + strings.TrimSpace(id)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func pluginConfiguredEntries(config map[string]any, key string) ([]map[string]any, error) {
	raw, exists := config[key]
	if !exists || raw == nil {
		return nil, nil
	}
	value, ok := raw.([]any)
	if !ok {
		return nil, validationError("CPA 上游配置格式错误: " + key)
	}
	entries := make([]map[string]any, 0, len(value))
	for _, item := range value {
		if object, ok := item.(map[string]any); ok {
			entries = append(entries, object)
			continue
		}
		return nil, validationError("CPA 上游配置条目格式错误: " + key)
	}
	return entries, nil
}

func pluginConfiguredString(value map[string]any, keys ...string) string {
	return pluginAnyString(value, keys...)
}

func pluginConfiguredDisabled(value map[string]any) bool {
	if pluginAnyBool(value["disabled"]) {
		return true
	}
	weight, ok := value["weight"].(json.Number)
	if !ok {
		return false
	}
	parsed, err := weight.Float64()
	return err == nil && parsed <= 0
}

func pluginConfiguredCredentialID(kind string, parts []string, counters map[string]int) string {
	base := kind
	for _, part := range parts {
		base += "\x00" + strings.TrimSpace(part)
	}
	sum := sha256.Sum256([]byte(base))
	id := kind + ":" + hex.EncodeToString(sum[:])[:12]
	count := counters[id]
	counters[id] = count + 1
	if count > 0 {
		id += fmt.Sprintf("-%d", count)
	}
	return id
}

func pluginConfiguredCredentialRef(kind string, parts []string, counters map[string]int) string {
	return pluginCredentialRef(pluginConfiguredCredentialID(kind, parts, counters))
}

func pluginMaskKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return "***"
	}
	return value[:6] + "..." + value[len(value)-4:]
}

func pluginConfiguredHeaders(value map[string]any) string {
	headers, ok := value["headers"].(map[string]any)
	if !ok {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result strings.Builder
	for _, key := range keys {
		result.WriteString(key)
		result.WriteByte('\x00')
		result.WriteString(fmt.Sprint(headers[key]))
		result.WriteByte('\x00')
	}
	return result.String()
}

func pluginAddConfiguredCredentials(target map[string]map[string]any, config map[string]any) error {
	return pluginAddConfiguredCredentialsWithAliases(target, config, nil)
}

func pluginAddConfiguredCredentialsWithAliases(target map[string]map[string]any, config map[string]any, aliases map[modelPriceChannelGroupIdentity]string) error {
	counters := map[string]int{}
	fields := []struct{ key, provider string }{
		{"gemini-api-key", "gemini"}, {"interactions-api-key", "gemini-interactions"}, {"claude-api-key", "claude"},
		{"codex-api-key", "codex"}, {"xai-api-key", "xai"}, {"meta-api-key", "meta"},
	}
	add := func(kind string, parts []string, provider string, disabled bool, label string) {
		ref := pluginConfiguredCredentialRef(kind, parts, counters)
		status := "active"
		if disabled {
			status = "disabled"
		}
		target[ref] = map[string]any{"ref": ref, "source": "ai-providers", "provider": provider, "label": label, "status": status}
	}
	for _, field := range fields {
		entries, err := pluginConfiguredEntries(config, field.key)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			apiKey := pluginConfiguredString(entry, "api-key", "api_key")
			baseURL := pluginConfiguredString(entry, "base-url", "base_url")
			if apiKey == "" && baseURL == "" {
				continue
			}
			name := pluginChannelName(field.provider, pluginConfiguredString(entry, "name"), apiKey)
			channel := pluginChannelIdentity(aiProviderBrand(field.provider), pluginConfiguredString(entry, "auth-index", "auth_index"), apiKey, name)
			if alias := aliases[modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, field.provider, channel)]; alias != "" {
				name = alias
			}
			label := pluginChannelLabel(name, baseURL, pluginMaskKey(apiKey))
			add(field.provider+":apikey", []string{apiKey, baseURL, pluginConfiguredString(entry, "proxy-url", "proxy_url"), pluginConfiguredString(entry, "prefix"), pluginConfiguredHeaders(entry)}, field.provider, pluginConfiguredDisabled(entry), label)
		}
	}
	compatEntries, err := pluginConfiguredEntries(config, "openai-compatibility")
	if err != nil {
		return err
	}
	for _, entry := range compatEntries {
		if pluginAnyBool(entry["disabled"]) {
			continue
		}
		name := strings.ToLower(pluginConfiguredString(entry, "name"))
		if name == "" {
			name = "openai-compatibility"
		}
		provider := name
		if provider != "openai-compatibility" && !strings.HasPrefix(provider, "openai-compatible-") {
			provider = "openai-compatible-" + provider
		}
		baseURL := pluginConfiguredString(entry, "base-url", "base_url")
		rawEntries, exists := entry["api-key-entries"]
		if !exists || rawEntries == nil {
			if baseURL != "" {
				add("openai-compatibility:"+name, []string{baseURL}, provider, false, pluginJoinLabel(pluginConfiguredString(entry, "name"), baseURL))
			}
			continue
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			return validationError("CPA 上游配置格式错误: openai-compatibility.api-key-entries")
		}
		if len(entries) == 0 {
			if baseURL != "" {
				add("openai-compatibility:"+name, []string{baseURL}, provider, false, pluginJoinLabel(pluginConfiguredString(entry, "name"), baseURL))
			}
			continue
		}
		for _, raw := range entries {
			item, ok := raw.(map[string]any)
			if !ok {
				return validationError("CPA 上游配置条目格式错误: openai-compatibility.api-key-entries")
			}
			apiKey := pluginConfiguredString(item, "api-key", "api_key")
			add("openai-compatibility:"+name, []string{apiKey, baseURL, pluginConfiguredString(item, "proxy-url", "proxy_url")}, provider, pluginConfiguredDisabled(item), pluginJoinLabel(pluginConfiguredString(entry, "name"), baseURL, pluginMaskKey(apiKey)))
		}
	}
	vertexEntries, err := pluginConfiguredEntries(config, "vertex-api-key")
	if err != nil {
		return err
	}
	for _, entry := range vertexEntries {
		apiKey := pluginConfiguredString(entry, "api-key", "api_key")
		baseURL := pluginConfiguredString(entry, "base-url", "base_url")
		if apiKey == "" && baseURL == "" {
			continue
		}
		name := pluginChannelName("vertex", pluginConfiguredString(entry, "name"), apiKey)
		channel := pluginChannelIdentity(aiProviderBrandVertex, pluginConfiguredString(entry, "auth-index", "auth_index"), apiKey, name)
		if alias := aliases[modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, "vertex", channel)]; alias != "" {
			name = alias
		}
		add("vertex:apikey", []string{apiKey, baseURL, pluginConfiguredString(entry, "proxy-url", "proxy_url")}, "vertex", pluginConfiguredDisabled(entry), pluginChannelLabel(name, baseURL, pluginMaskKey(apiKey)))
	}
	return nil
}

// pluginV8DownstreamKeys reads CPA v8's access.api-keys list. A malformed
// response is a synchronization error; it must not be treated as an empty list.
func pluginV8DownstreamKeys(config map[string]any) ([]string, error) {
	access, ok := config["access"].(map[string]any)
	if !ok {
		return nil, validationError("CLIProxyAPI v8 配置缺少 access")
	}
	raw, ok := access["api-keys"]
	if !ok || raw == nil {
		return []string{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, validationError("读取 CPA API KEY 失败：v8 配置格式无效")
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		key, ok := item.(string)
		if !ok {
			return nil, validationError("读取 CPA API KEY 失败：v8 配置格式无效")
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func pluginV8ConfiguredEntries(config map[string]any, provider string) ([]map[string]any, error) {
	providers, ok := config["api-keys"].(map[string]any)
	if !ok {
		return nil, validationError("CLIProxyAPI v8 配置缺少 api-keys")
	}
	rawGroups, ok := providers[provider]
	if !ok {
		return nil, nil
	}
	groups, ok := rawGroups.([]any)
	if !ok {
		return nil, validationError("CPA 上游配置格式错误: " + provider)
	}
	result := make([]map[string]any, 0, len(groups))
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			return nil, validationError("CPA 上游分组格式错误: " + provider)
		}
		result = append(result, group)
	}
	return result, nil
}

func pluginV8String(value map[string]any, keys ...string) string {
	return pluginAnyString(value, keys...)
}

func pluginV8Inherited(entry, group map[string]any, key string) any {
	if value, ok := entry[key]; ok && value != nil {
		return value
	}
	return group[key]
}

// pluginV8AuthIndex follows the browser adapter: omitted/null indexes belong
// to entries removed by CPA's sanitizer and are skipped; malformed values are
// synchronization errors rather than silently hidden credentials.
func pluginV8AuthIndex(value map[string]any) (string, bool, error) {
	raw, exists := value["auth_index"]
	if !exists || raw == nil {
		return "", false, nil
	}
	index, ok := raw.(string)
	if !ok || strings.TrimSpace(index) == "" {
		return "", false, validationError("CPA 凭据索引格式错误")
	}
	return strings.TrimSpace(index), true, nil
}

func pluginV8APIKey(value map[string]any) (string, error) {
	raw, exists := value["api-key"]
	if !exists {
		raw, exists = value["api_key"]
	}
	if !exists || raw == nil {
		return "", nil
	}
	key, ok := raw.(string)
	if !ok {
		return "", validationError("CPA 上游 Key 格式错误")
	}
	return strings.TrimSpace(key), nil
}

func pluginV8Disabled(value map[string]any) bool {
	if pluginAnyBool(value["disabled"]) {
		return true
	}
	raw, ok := value["weight"]
	if !ok || raw == nil {
		return false
	}
	var weight float64
	switch typed := raw.(type) {
	case json.Number:
		weight, _ = typed.Float64()
	case float64:
		weight = typed
	case float32:
		weight = float64(typed)
	case int:
		weight = float64(typed)
	case int64:
		weight = float64(typed)
	default:
		return false
	}
	return weight <= 0
}

func pluginV8Headers(value any) string {
	headers, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	names := make([]string, 0, len(headers))
	values := make(map[string]string, len(headers))
	for name, raw := range headers {
		name, text := strings.TrimSpace(name), strings.TrimSpace(fmt.Sprint(raw))
		if name != "" && text != "" {
			names = append(names, name)
			values[name] = text
		}
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte('\x00')
		builder.WriteString(values[name])
		builder.WriteByte('\x00')
	}
	return builder.String()
}

func (a *App) handleCPAHelperPlugin(w http.ResponseWriter, r *http.Request) error {
	prefix := "/api/cpa-helper-plugin"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	user, err := a.readyUser(r.Context(), r)
	if err != nil {
		return err
	}
	cfg, err := a.loadConfig(r.Context())
	if err != nil {
		return err
	}
	if strings.HasPrefix(path, "/policy/key/") {
		localHash := strings.TrimPrefix(path, "/policy/key/")
		if localHash == "" {
			return notFoundError("Not Found")
		}
		if r.Method == http.MethodGet {
			result, err := a.pluginKeyPolicy(r.Context(), user, localHash, cfg)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, result)
			return nil
		}
		if r.Method == http.MethodPut {
			var request struct {
				PolicyRevision uint64                     `json:"policy_revision"`
				Key            map[string]json.RawMessage `json:"key"`
			}
			if err := decodeJSON(r, &request); err != nil {
				return err
			}
			if request.PolicyRevision == 0 || len(request.Key) == 0 {
				return validationError("插件 API 密钥策略必须包含 policy_revision 和 key")
			}
			key, err := a.getAPIKey(r.Context(), localHash)
			if err != nil {
				return err
			}
			if key.UserID != user.ID && !user.IsSuperAdmin {
				return notFoundError("API KEY 不存在")
			}
			if key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "" {
				return conflictError("当前 API 密钥缺少完整密钥，无法映射插件策略")
			}
			if rawString(request.Key["id"]) != pluginCallerScope(*key.APIKey) {
				return forbiddenError("插件 API 密钥身份映射不匹配")
			}
			current, _, err := a.cpaHelperPluginPolicy(r.Context(), cfg)
			if err != nil {
				return err
			}
			if request.PolicyRevision != rawUint(current["policy_revision"]) {
				return conflictError("插件配置已被其他管理端修改，请刷新后重试")
			}
			updated, err := replacePluginKey(current, request.Key)
			if err != nil {
				return err
			}
			if err := normalizePluginPolicy(updated); err != nil {
				return err
			}
			if _, err = a.putCPAHelperPluginPolicy(r.Context(), cfg, current, updated); err != nil {
				return err
			}
			result, err := a.pluginKeyPolicy(r.Context(), user, localHash, cfg)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, result)
			return nil
		}
		return methodNotAllowed()
	}
	switch path {
	case "/concurrency-summary":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		result, err := a.pluginConcurrencySummary(r.Context(), user, cfg, r.URL.Query().Get("scope"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, result)
		return nil
	case "/keys-summary":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		keys, err := a.pluginKeysSummary(r.Context(), user, cfg)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return nil
	case "", "/", "/status":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		capabilities, capErr := a.cpaHelperPluginCapabilities(r.Context(), cfg)
		if capErr != nil {
			return capErr
		}
		status, _, health, healthErr := a.cpaHelperPluginRequest(r.Context(), cfg, http.MethodGet, cpaHelperPluginManagementPrefix+"/health", nil, nil)
		if healthErr != nil {
			return appErrorFromPlugin(healthErr, status)
		}
		var healthObject map[string]json.RawMessage
		healthObject, err = decodePluginObject(health)
		if err != nil {
			return err
		}
		if !user.IsSuperAdmin {
			capabilities = cloneRawObject(capabilities)
			delete(capabilities, "response_model_mismatch")
		}
		// Live activity is available through the authorized account/admin
		// summary endpoints, without exposing caller-scope identities.
		delete(healthObject, "active")
		writeJSON(w, http.StatusOK, map[string]any{"loaded": true, "capabilities": capabilities, "health": healthObject})
		return nil
	case "/policy":
		if !user.IsSuperAdmin {
			return forbiddenError("需要超级管理员权限")
		}
		policy, _, err := a.cpaHelperPluginPolicy(r.Context(), cfg)
		if err != nil {
			return err
		}
		switch r.Method {
		case http.MethodGet:
			policy, err = a.enrichPluginPolicyKeys(r.Context(), policy)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, policy)
			return nil
		case http.MethodPut:
			var incoming map[string]json.RawMessage
			if err := decodeJSON(r, &incoming); err != nil {
				return err
			}
			delete(incoming, pluginKeyMetadataField)
			current, _, err := a.cpaHelperPluginPolicy(r.Context(), cfg)
			if err != nil {
				return err
			}
			revision, ok := incoming["policy_revision"]
			if !ok || rawUint(revision) == 0 || rawUint(revision) != rawUint(current["policy_revision"]) {
				return conflictError("插件配置已被其他管理端修改，请刷新后重试")
			}
			if err := validatePluginPolicyArrays(incoming, false); err != nil {
				return err
			}
			merged := cloneRawObject(current)
			for field, value := range incoming {
				merged[field] = value
			}
			if err := normalizePluginPolicy(merged); err != nil {
				return err
			}
			updated, err := a.putCPAHelperPluginPolicy(r.Context(), cfg, current, merged)
			if err != nil {
				return err
			}
			updated, err = a.enrichPluginPolicyKeys(r.Context(), updated)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, updated)
			return nil
		default:
			return methodNotAllowed()
		}
	case "/directory":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		directory, err := a.pluginDirectory(r.Context(), cfg, user, strings.TrimSpace(r.URL.Query().Get("api_key_hash")))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, directory)
		return nil
	case "/response-model":
		if !user.IsSuperAdmin {
			return forbiddenError("需要超级管理员权限")
		}
		switch r.Method {
		case http.MethodGet:
			config, err := a.pluginResponseModelConfig(r.Context(), cfg)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, config)
			return nil
		case http.MethodPut:
			var config map[string]json.RawMessage
			if err := decodeJSON(r, &config); err != nil {
				return err
			}
			updated, err := a.updatePluginResponseModelConfig(r.Context(), cfg, config)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, updated)
			return nil
		default:
			return methodNotAllowed()
		}
	default:
		return notFoundError("Not Found")
	}
}
