package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var providerKinds = []struct{ ConfigKey, Provider string }{
	{"gemini-api-key", "gemini"}, {"claude-api-key", "claude"},
	{"codex-api-key", "codex"}, {"openai-compatibility", "openai-compatibility"},
	{"vertex-api-key", "vertex"}, {"xai-api-key", "xai"},
}

type fixtureCredential struct {
	AuthID, AuthIndex, Ref, Source, Provider, Label string
	Enabled                                         bool
	Models                                          []string
	entry                                           map[string]any
}

func (s *fakeCPA) credentials() []fixtureCredential {
	items := []fixtureCredential{}
	counters := map[string]int{}
	seen := map[string]bool{}
	for _, kind := range providerKinds {
		for _, group := range s.providers[kind.ConfigKey] {
			entries := []map[string]any{group}
			keyless := false
			if kind.Provider == "openai-compatibility" {
				if boolean(group["disabled"]) {
					continue
				}
				entries = objectList(group["api-key-entries"])
				if len(entries) == 0 {
					entries, keyless = []map[string]any{group}, true
				}
			}
			for _, entry := range entries {
				provider := kind.Provider
				key, base := textValue(entry["api-key"]), textValue(group["base-url"])
				proxy := textValue(inherited(entry, group, "proxy-url"))
				prefix := normalizedPrefix(inherited(entry, group, "prefix"))
				idKind := provider + ":apikey"
				parts := []string{key, base, proxy}
				if provider == "openai-compatibility" {
					name := strings.ToLower(textValue(group["name"]))
					if name == "" {
						name = "openai-compatibility"
					}
					provider = name
					if name != "openai-compatibility" && !strings.HasPrefix(name, "openai-compatible-") {
						provider = "openai-compatible-" + name
					}
					idKind = "openai-compatibility:" + name
					parts = []string{key, base, textValue(entry["proxy-url"])}
					if keyless {
						parts = []string{base}
					}
				} else if provider != "vertex" {
					parts = append(parts, prefix, sortedHeaders(inherited(entry, group, "headers")))
				}
				dedup := idKind + "\x00" + strings.Join(parts, "\x00")
				if provider == "vertex" {
					dedup = provider + "\x00" + key + "|" + base
				}
				if (provider == "gemini" || provider == "vertex") && seen[dedup] {
					continue
				}
				seen[dedup] = true
				id := schedulerAuthID(idKind, parts, counters)
				index := textValue(entry["auth-index"])
				if index == "" {
					index = "fixture-" + hexSHA256(id)[:12]
				}
				name := textValue(entry["name"])
				if name == "" {
					name = textValue(group["name"])
				}
				if name == "" {
					name = kind.Provider
				}
				label := []string{name}
				if base != "" {
					label = append(label, base)
				}
				if key != "" {
					mask := "***"
					if len(key) > 10 {
						mask = key[:6] + "..." + key[len(key)-4:]
					}
					label = append(label, mask)
				}
				items = append(items, fixtureCredential{
					AuthID: id, AuthIndex: index, Ref: credentialRef(id), Source: "ai-providers",
					Provider: provider, Label: strings.Join(label, " · "), Enabled: !providerDisabled(entry, group),
					Models: fixturePublicModels(group), entry: entry,
				})
			}
		}
	}
	return append(items,
		fixtureCredential{
			AuthID: "acceptance-codex-oauth.json", AuthIndex: "oauth-codex-fixture", Ref: credentialRef("acceptance-codex-oauth.json"),
			Source: "auth-files", Provider: "codex", Label: "acceptance-codex-oauth.json · synthetic OAuth account",
			Enabled: true, Models: []string{"gpt-6-astra", "oauth-code-model"},
		},
		fixtureCredential{
			AuthID: "acceptance-disabled-oauth.json", AuthIndex: "oauth-disabled-fixture", Ref: credentialRef("acceptance-disabled-oauth.json"),
			Source: "auth-files", Provider: "claude", Label: "acceptance-disabled-oauth.json · weight 0",
			Enabled: false, Models: []string{"disabled-oauth-model"},
		},
	)
}

// Pinned to Plugin v0.1.8 web/directory.js: scheduler ID is kind plus the first
// 12 SHA256 hex digits; only duplicate instances receive -1, -2 suffixes.
func schedulerAuthID(kind string, parts []string, counters map[string]int) string {
	input := kind
	for _, part := range parts {
		input += "\x00" + strings.TrimSpace(part)
	}
	base := kind + ":" + hexSHA256(input)[:12]
	count := counters[base]
	counters[base]++
	if count > 0 {
		return base + "-" + strconv.Itoa(count)
	}
	return base
}

func hexSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func credentialRef(id string) string {
	return "sha256:" + hexSHA256("cpa-key-billing:credential:v1\x00"+strings.TrimSpace(id))
}

func callerScope(key string) string {
	return hexSHA256("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(key))
}

func (s *fakeCPA) fillAuthIndexes(previous []fixtureCredential) {
	indexes := map[string]string{}
	for _, credential := range previous {
		indexes[credential.AuthID] = credential.AuthIndex
	}
	for _, credential := range s.credentials() {
		if credential.entry != nil && textValue(credential.entry["auth-index"]) == "" {
			index := indexes[credential.AuthID]
			if index == "" {
				index = credential.AuthIndex
			}
			credential.entry["auth-index"] = index
		}
	}
}

func (s *fakeCPA) v8Config() map[string]any {
	providers := map[string]any{}
	for _, kind := range providerKinds {
		groups := []map[string]any{}
		for _, entry := range s.providers[kind.ConfigKey] {
			group := copyObject(entry)
			delete(group, "api-key")
			delete(group, "auth-index")
			if kind.Provider == "openai-compatibility" {
				delete(group, "api-key-entries")
				keys := []map[string]any{}
				for _, raw := range objectList(entry["api-key-entries"]) {
					key := copyObject(raw)
					key["auth_index"] = key["auth-index"]
					delete(key, "auth-index")
					keys = append(keys, key)
				}
				group["keys"] = keys
				if len(keys) == 0 {
					group["keys"] = nil
					group["auth_index"] = entry["auth-index"]
				}
			} else {
				key := copyObject(entry)
				key["auth_index"] = key["auth-index"]
				key["disabled"] = providerDisabled(entry, entry)
				delete(key, "auth-index")
				group["keys"] = []map[string]any{key}
			}
			groups = append(groups, group)
		}
		providers[kind.Provider] = groups
	}
	return map[string]any{
		"api-keys": providers,
		"access":   map[string]any{"api-keys": s.downstreamKeys},
		"plugins": map[string]any{"configs": map[string]any{"cpa-helper-plugin": map[string]any{
			"enabled": true, "state_dir": "acceptance-in-memory", "model_list_filter_enabled": true,
			"response_model_mismatch": s.responseModel,
		}}},
	}
}

func (s *fakeCPA) runtimeCredentials() []map[string]any {
	files := []map[string]any{}
	configOverlapAdded := false
	for _, credential := range s.credentials() {
		if credential.Source != "auth-files" {
			if credential.Provider != "codex" || configOverlapAdded {
				continue
			}
			configOverlapAdded = true
		}
		source, name, weight := "file", credential.AuthID, 1
		if credential.Source == "ai-providers" {
			source, name = "config:codex", "Codex runtime label before local rename"
		}
		if !credential.Enabled {
			weight = 0
		}
		files = append(files, map[string]any{
			"id": credential.AuthID, "auth_index": credential.AuthIndex, "source": source,
			"runtime_only": credential.Source == "ai-providers", "provider": credential.Provider,
			"name": name, "status": "active", "disabled": false, "weight": weight,
		})
	}
	return files
}

// These synthetic model rows define the fixture's registry. Alias and prefix
// changes update the mock registry; the Helper still must query by real auth ID.
func fixturePublicModels(entry map[string]any) []string {
	items := []string{}
	prefix := normalizedPrefix(entry["prefix"])
	for _, model := range objectList(entry["models"]) {
		id := textValue(model["alias"])
		if id == "" {
			id = textValue(model["name"])
		}
		if id == "" || selectionContains(stringList(entry["excluded-models"]), id) {
			continue
		}
		if prefix != "" {
			id = prefix + "/" + id
		}
		if !selectionContains(items, id) {
			items = append(items, id)
		}
	}
	return items
}

func validateProviderEntries(kind string, entries []map[string]any) error {
	if entries == nil {
		return errors.New("provider list must be non-null")
	}
	for _, entry := range entries {
		if entry == nil {
			return errors.New("provider must be an object")
		}
		for _, name := range []string{"api-key", "auth-index", "base-url", "name", "prefix", "proxy-url"} {
			if value := entry[name]; value != nil {
				if _, ok := value.(string); !ok {
					return errors.New("invalid provider text field")
				}
			}
		}
		if value := entry["disabled"]; value != nil {
			if _, ok := value.(bool); !ok {
				return errors.New("invalid disabled flag")
			}
		}
		if value := entry["weight"]; value != nil {
			if _, ok := value.(json.Number); !ok {
				return errors.New("invalid weight")
			}
		}
		if value := entry["headers"]; value != nil {
			headers, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid headers")
			}
			for _, value := range headers {
				if _, ok := value.(string); !ok {
					return errors.New("invalid header value")
				}
			}
		}
		if raw := entry["models"]; raw != nil {
			models, ok := raw.([]any)
			if !ok {
				return errors.New("invalid model array")
			}
			for _, model := range models {
				value, ok := model.(map[string]any)
				if !ok || !validSelection(textValue(value["name"])) {
					return errors.New("invalid model")
				}
				if alias := value["alias"]; alias != nil {
					if _, ok := alias.(string); !ok {
						return errors.New("invalid model alias")
					}
				}
			}
		}
		if kind == "openai-compatibility" {
			if !validSelection(textValue(entry["name"])) {
				return errors.New("missing compatibility provider name")
			}
			if raw := entry["api-key-entries"]; raw != nil {
				keys, ok := raw.([]any)
				if !ok {
					return errors.New("invalid compatibility key array")
				}
				for _, rawKey := range keys {
					key, ok := rawKey.(map[string]any)
					if !ok || !validSelection(textValue(key["api-key"])) {
						return errors.New("invalid compatibility key")
					}
					for _, name := range []string{"api-key", "auth-index", "proxy-url"} {
						if value := key[name]; value != nil {
							if _, ok := value.(string); !ok {
								return errors.New("invalid compatibility credential field")
							}
						}
					}
				}
			}
		} else if textValue(entry["api-key"]) == "" {
			return errors.New("a native fixture credential requires an API key")
		}
	}
	return nil
}

func textValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolean(value any) bool { result, _ := value.(bool); return result }

func inherited(entry, group map[string]any, field string) any {
	if value := entry[field]; value != nil {
		return value
	}
	return group[field]
}

func normalizedPrefix(value any) string {
	prefix := strings.Trim(textValue(value), "/")
	if strings.Contains(prefix, "/") {
		return ""
	}
	return prefix
}

func sortedHeaders(value any) string {
	headers, _ := value.(map[string]any)
	normalized := map[string]string{}
	for key, value := range headers {
		if key = strings.TrimSpace(key); key != "" && textValue(value) != "" {
			normalized[key] = textValue(value)
		}
	}
	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result strings.Builder
	for _, key := range keys {
		result.WriteString(key + "\x00" + normalized[key] + "\x00")
	}
	return result.String()
}

func objectList(value any) []map[string]any {
	if items, ok := value.([]map[string]any); ok {
		return items
	}
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.(map[string]any))
	}
	return result
}

func stringList(value any) []string {
	if items, ok := value.([]string); ok {
		return items
	}
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.(string))
	}
	return result
}

func providerDisabled(entry, group map[string]any) bool {
	if boolean(entry["disabled"]) || boolean(group["disabled"]) || slices.Contains(stringList(group["excluded-models"]), "*") {
		return true
	}
	if weight, ok := inherited(entry, group, "weight").(json.Number); ok {
		value, err := weight.Float64()
		return err == nil && value <= 0
	}
	return false
}

func copyObject(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, value := range value {
		result[key] = value
	}
	return result
}
