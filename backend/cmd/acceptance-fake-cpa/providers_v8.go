package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

var fixtureV8SharedFields = []string{
	"priority", "prefix", "proxy-url", "headers", "models", "excluded-models",
	"disable-cooling", "request-retry", "request-scoped-errors",
}

func (s *fakeCPA) v8ProviderGroups(configKey, family string) []map[string]any {
	if groups, exists := s.providerGroups[family]; exists {
		return groups
	}
	groups := []map[string]any{}
	for index, entry := range s.providers[configKey] {
		if family == "openai-compatibility" {
			group := copyObject(entry)
			delete(group, "api-key-entries")
			delete(group, "auth-index")
			keys := []map[string]any{}
			for _, raw := range objectList(entry["api-key-entries"]) {
				key := copyObject(raw)
				key["auth_index"] = key["auth-index"]
				delete(key, "auth-index")
				keys = append(keys, key)
			}
			group["keys"] = keys
			groups = append(groups, group)
			continue
		}
		group := map[string]any{"name": family + "-" + strconv.Itoa(index+1)}
		key := copyObject(entry)
		for _, field := range append([]string{"base-url"}, fixtureV8SharedFields...) {
			if value, exists := key[field]; exists {
				group[field] = value
				delete(key, field)
			}
		}
		key["auth_index"] = key["auth-index"]
		delete(key, "auth-index")
		group["keys"] = []map[string]any{key}
		groups = append(groups, group)
	}
	return groups
}

func (s *fakeCPA) serveV8ProviderFamily(w http.ResponseWriter, r *http.Request) {
	family := strings.TrimPrefix(r.URL.Path, "/v8/management/config/api-keys/")
	configKey := ""
	for _, kind := range providerKinds {
		if kind.Provider == family {
			configKey = kind.ConfigKey
			break
		}
	}
	if configKey == "" {
		fakeProblem(w, http.StatusNotFound, "not_found", "Unknown provider family")
		return
	}
	if r.Method == http.MethodGet {
		fakeJSON(w, http.StatusOK, s.v8ProviderGroups(configKey, family))
		return
	}
	if r.Method != http.MethodPut {
		fakeMethodNotAllowed(w, "GET, PUT")
		return
	}
	var groups []map[string]any
	if err := decodeFakeBody(r, &groups); err != nil || groups == nil {
		fakeProblem(w, http.StatusBadRequest, "invalid", "Expected a complete provider group array")
		return
	}
	// This fixture models the v8.0.47 OpenAI exclusion-field rejection only,
	// not the complete KnownFields validator in internal/config/config_v8.go.
	if family == "openai-compatibility" {
		for _, group := range groups {
			if _, exists := group["excluded-models"]; exists {
				fakeProblem(w, http.StatusBadRequest, "invalid_config", "OpenAI-compatible providers do not support excluded-models")
				return
			}
		}
	}
	entries, err := flattenFixtureV8Groups(groups, family)
	if err != nil || validateProviderEntries(configKey, entries) != nil {
		fakeProblem(w, http.StatusBadRequest, "invalid", "Invalid provider group or key fields")
		return
	}
	previous := s.credentials()
	if s.providerGroups == nil {
		s.providerGroups = map[string][]map[string]any{}
	}
	s.providerGroups[family] = groups
	s.providers[configKey] = entries
	s.fillAuthIndexes(previous)
	fakeJSON(w, http.StatusOK, map[string]any{"status": "ok", "config-version": 8})
}

func flattenFixtureV8Groups(groups []map[string]any, family string) ([]map[string]any, error) {
	entries := []map[string]any{}
	for _, group := range groups {
		if group == nil {
			return nil, errors.New("invalid group")
		}
		values, ok := group["keys"].([]any)
		if !ok {
			return nil, errors.New("keys must be an array")
		}
		keys := []any{}
		for _, value := range values {
			key, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("key must be an object")
			}
			delete(key, "auth-index")
			delete(key, "auth_index")
			if family != "openai-compatibility" {
				if _, exists := key["base-url"]; exists {
					return nil, errors.New("base-url belongs to group")
				}
			}
			keys = append(keys, key)
		}
		delete(group, "auth-index")
		delete(group, "auth_index")
		if family == "openai-compatibility" {
			entry := copyObject(group)
			delete(entry, "keys")
			entry["api-key-entries"] = keys
			entries = append(entries, entry)
			continue
		}
		for _, value := range keys {
			entry := map[string]any{}
			for _, field := range append([]string{"base-url"}, fixtureV8SharedFields...) {
				if value, exists := group[field]; exists {
					entry[field] = value
				}
			}
			for field, value := range value.(map[string]any) {
				if value != nil {
					entry[field] = value
				}
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
