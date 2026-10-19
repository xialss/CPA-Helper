package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
)

// These transport types mirror the pinned Plugin v0.1.8 v1 contract. They stay
// in the acceptance tool and do not add a dependency on a deployed plugin.
type credentialSelector struct {
	Source   string `json:"source"`
	Provider string `json:"provider"`
}

type routeRule struct {
	Models                    []string             `json:"models,omitempty"`
	DeniedModels              []string             `json:"denied_models,omitempty"`
	CredentialIDs             []string             `json:"credential_ids,omitempty"`
	DeniedCredentialIDs       []string             `json:"denied_credential_ids,omitempty"`
	CredentialProviders       []credentialSelector `json:"credential_providers,omitempty"`
	DeniedCredentialProviders []credentialSelector `json:"denied_credential_providers,omitempty"`
}

type ruleGroup struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Note string    `json:"note,omitempty"`
	Rule routeRule `json:"rule"`
}

type keyPolicy struct {
	ID             string    `json:"id"`
	Label          string    `json:"label,omitempty"`
	Enabled        bool      `json:"enabled"`
	GroupIDs       []string  `json:"group_ids"`
	MaxConcurrency int       `json:"max_concurrency"`
	Rule           routeRule `json:"rule"`
}

type policySnapshot struct {
	ContractVersion string      `json:"contract_version"`
	PluginVersion   string      `json:"plugin_version"`
	Revision        uint64      `json:"policy_revision"`
	GeneratedAt     time.Time   `json:"generated_at"`
	Groups          []ruleGroup `json:"groups"`
	Keys            []keyPolicy `json:"keys"`
}

type catalogCredential struct {
	Ref      string   `json:"ref"`
	Source   string   `json:"source"`
	Provider string   `json:"provider"`
	Enabled  bool     `json:"enabled"`
	Models   []string `json:"models"`
}

type modelCatalog struct {
	ContractVersion string              `json:"contract_version"`
	PluginVersion   string              `json:"plugin_version"`
	CPAVersion      string              `json:"cpa_version"`
	Revision        uint64              `json:"catalog_revision"`
	GeneratedAt     time.Time           `json:"generated_at"`
	Credentials     []catalogCredential `json:"credentials"`
}

func strictObject(raw []byte, target any, required ...string) error {
	if err := checkJSONValue(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("expected a JSON object")
	}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(target).Elem()
	for index := 0; index < typ.NumField(); index++ {
		name := strings.Split(typ.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	for name, value := range fields {
		if !allowed[name] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("unknown or null contract field")
		}
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return errors.New("required contract field is missing")
		}
	}
	return json.Unmarshal(raw, target)
}

func (p *policySnapshot) UnmarshalJSON(raw []byte) error {
	type plain policySnapshot
	var value plain
	if err := strictObject(raw, &value, "contract_version", "plugin_version", "policy_revision", "generated_at", "groups", "keys"); err != nil {
		return err
	}
	*p = policySnapshot(value)
	return nil
}

func (k *keyPolicy) UnmarshalJSON(raw []byte) error {
	type plain keyPolicy
	var value plain
	if err := strictObject(raw, &value, "id", "enabled", "group_ids", "max_concurrency", "rule"); err != nil {
		return err
	}
	*k = keyPolicy(value)
	return nil
}

func (g *ruleGroup) UnmarshalJSON(raw []byte) error {
	type plain ruleGroup
	var value plain
	if err := strictObject(raw, &value, "id", "name", "rule"); err != nil {
		return err
	}
	*g = ruleGroup(value)
	return nil
}

func (r *routeRule) UnmarshalJSON(raw []byte) error {
	type plain routeRule
	var value plain
	if err := strictObject(raw, &value); err != nil {
		return err
	}
	*r = routeRule(value)
	return nil
}

func (s *credentialSelector) UnmarshalJSON(raw []byte) error {
	type plain credentialSelector
	var value plain
	if err := strictObject(raw, &value, "source", "provider"); err != nil {
		return err
	}
	*s = credentialSelector(value)
	return nil
}

func (c *modelCatalog) UnmarshalJSON(raw []byte) error {
	type plain modelCatalog
	var value plain
	if err := strictObject(raw, &value, "contract_version", "plugin_version", "cpa_version", "catalog_revision", "generated_at", "credentials"); err != nil {
		return err
	}
	*c = modelCatalog(value)
	return nil
}

func (c *catalogCredential) UnmarshalJSON(raw []byte) error {
	type plain catalogCredential
	var value plain
	if err := strictObject(raw, &value, "ref", "source", "provider", "enabled", "models"); err != nil {
		return err
	}
	*c = catalogCredential(value)
	return nil
}

func validSelection(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n\t")
}

func validScope(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}

func validCredentialRef(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validScope(strings.TrimPrefix(value, "sha256:"))
}

func validateStringSelections(items []string) error {
	seen := map[string]bool{}
	for _, item := range items {
		key := strings.ToLower(item)
		if !validSelection(item) || seen[key] {
			return errors.New("invalid or duplicate selection")
		}
		seen[key] = true
	}
	return nil
}

func selectionContains(items []string, value string) bool {
	return slices.ContainsFunc(items, func(item string) bool { return strings.EqualFold(item, value) })
}

func (r routeRule) validate() error {
	for _, pair := range [][2][]string{{r.Models, r.DeniedModels}, {r.CredentialIDs, r.DeniedCredentialIDs}} {
		for _, items := range pair {
			if err := validateStringSelections(items); err != nil {
				return err
			}
		}
		for _, item := range pair[0] {
			if selectionContains(pair[1], item) {
				return errors.New("a selection cannot be allowed and denied within the same rule")
			}
		}
	}
	for _, refs := range [][]string{r.CredentialIDs, r.DeniedCredentialIDs} {
		for _, ref := range refs {
			if !validCredentialRef(ref) {
				return errors.New("invalid credential reference")
			}
		}
	}
	for _, selectors := range [][]credentialSelector{r.CredentialProviders, r.DeniedCredentialProviders} {
		seen := map[credentialSelector]bool{}
		for _, selector := range selectors {
			if (selector.Source != "auth-files" && selector.Source != "ai-providers") || !validSelection(selector.Provider) || selector.Provider != strings.ToLower(selector.Provider) || seen[selector] {
				return errors.New("invalid or duplicate credential selector")
			}
			seen[selector] = true
		}
	}
	for _, selector := range r.CredentialProviders {
		if slices.Contains(r.DeniedCredentialProviders, selector) {
			return errors.New("a credential selector cannot be allowed and denied within the same rule")
		}
	}
	return nil
}

func (p policySnapshot) validate() error {
	versions := []string{"0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4", "0.1.5", "0.1.6", "0.1.7", "0.1.8"}
	if p.ContractVersion != "v1" || !slices.Contains(versions, p.PluginVersion) || p.Revision == 0 || p.Revision > 9007199254740991 || p.GeneratedAt.IsZero() || p.Groups == nil || p.Keys == nil {
		return errors.New("invalid policy envelope")
	}
	groups := map[string]bool{}
	for _, group := range p.Groups {
		if !validSelection(group.ID) || !validSelection(group.Name) || groups[group.ID] {
			return errors.New("invalid group identity")
		}
		if err := group.Rule.validate(); err != nil {
			return err
		}
		groups[group.ID] = true
	}
	keys := map[string]bool{}
	for _, key := range p.Keys {
		if !validScope(key.ID) || keys[key.ID] || key.GroupIDs == nil || key.MaxConcurrency < 0 || uint64(key.MaxConcurrency) > 9007199254740991 {
			return errors.New("invalid key configuration")
		}
		keys[key.ID] = true
		seen := map[string]bool{}
		for _, group := range key.GroupIDs {
			if !groups[group] || seen[group] {
				return errors.New("missing or duplicate group binding")
			}
			seen[group] = true
		}
		if err := key.Rule.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (c modelCatalog) validate() error {
	if c.ContractVersion != "v1" || c.PluginVersion != "0.1.8" || c.CPAVersion != "v8.0.32" || c.Revision == 0 || c.Revision > 9007199254740991 || c.GeneratedAt.IsZero() || c.Credentials == nil {
		return errors.New("invalid model catalog envelope")
	}
	refs := map[string]bool{}
	for _, credential := range c.Credentials {
		if !validCredentialRef(credential.Ref) || refs[credential.Ref] || (credential.Source != "auth-files" && credential.Source != "ai-providers") || !validSelection(credential.Provider) || credential.Provider != strings.ToLower(credential.Provider) || credential.Models == nil {
			return errors.New("invalid model catalog credential")
		}
		refs[credential.Ref] = true
		if err := validateStringSelections(credential.Models); err != nil {
			return err
		}
	}
	return nil
}

func mergeRules(left, right routeRule) routeRule {
	return routeRule{
		Models: append(slices.Clone(left.Models), right.Models...), DeniedModels: append(slices.Clone(left.DeniedModels), right.DeniedModels...),
		CredentialIDs: append(slices.Clone(left.CredentialIDs), right.CredentialIDs...), DeniedCredentialIDs: append(slices.Clone(left.DeniedCredentialIDs), right.DeniedCredentialIDs...),
		CredentialProviders: append(slices.Clone(left.CredentialProviders), right.CredentialProviders...), DeniedCredentialProviders: append(slices.Clone(left.DeniedCredentialProviders), right.DeniedCredentialProviders...),
	}
}

func (r routeRule) allowsCredential(credential catalogCredential) bool {
	selector := credentialSelector{Source: credential.Source, Provider: credential.Provider}
	if selectionContains(r.DeniedCredentialIDs, credential.Ref) || slices.Contains(r.DeniedCredentialProviders, selector) {
		return false
	}
	return len(r.CredentialIDs)+len(r.CredentialProviders) == 0 || selectionContains(r.CredentialIDs, credential.Ref) || slices.Contains(r.CredentialProviders, selector)
}

type responseModelConfig struct {
	Enabled              bool                `json:"enabled"`
	StreamEnabled        bool                `json:"stream_enabled"`
	MaxBufferBytes       int                 `json:"max_buffer_bytes"`
	MaxTotalBufferBytes  int                 `json:"max_total_buffer_bytes"`
	Action               string              `json:"action"`
	UnknownAction        string              `json:"unknown_action"`
	CaseSensitive        bool                `json:"case_sensitive"`
	IgnoreThinkingSuffix bool                `json:"ignore_thinking_suffix"`
	IgnoredModels        []string            `json:"ignored_models"`
	AcceptedModels       map[string][]string `json:"accepted_models"`
}

func defaultResponseModelConfig() responseModelConfig {
	return responseModelConfig{
		StreamEnabled: true, MaxBufferBytes: 2 * 1024 * 1024, MaxTotalBufferBytes: 8 * 1024 * 1024,
		Action: "reject", UnknownAction: "pass", IgnoreThinkingSuffix: true,
		IgnoredModels: []string{}, AcceptedModels: map[string][]string{},
	}
}

func (c *responseModelConfig) normalize() {
	if c.IgnoredModels == nil {
		c.IgnoredModels = []string{}
	}
	if c.AcceptedModels == nil {
		c.AcceptedModels = map[string][]string{}
	}
}

func (c responseModelConfig) normalizedModel(value string) string {
	value = strings.TrimSpace(value)
	if c.IgnoreThinkingSuffix {
		if index := strings.LastIndex(value, "("); index >= 0 && strings.HasSuffix(value, ")") {
			value = strings.TrimSpace(value[:index])
		}
	}
	value = strings.TrimPrefix(value, "models/")
	if !c.CaseSensitive {
		value = strings.ToLower(value)
	}
	return value
}

func (c responseModelConfig) validate() error {
	if (c.Action != "reject" && c.Action != "audit") || (c.UnknownAction != "pass" && c.UnknownAction != "reject") || c.MaxBufferBytes < 1024*1024 || c.MaxBufferBytes > 256*1024*1024 || c.MaxTotalBufferBytes < c.MaxBufferBytes || c.MaxTotalBufferBytes > 1024*1024*1024 {
		return errors.New("invalid response-model settings")
	}
	seen := map[string]bool{}
	for model, aliases := range c.AcceptedModels {
		key := c.normalizedModel(model)
		if key == "" || seen[key] || len(aliases) == 0 {
			return errors.New("invalid response-model mapping")
		}
		seen[key] = true
		for _, alias := range aliases {
			if c.normalizedModel(alias) == "" {
				return errors.New("invalid response-model alias")
			}
		}
	}
	for _, model := range c.IgnoredModels {
		if c.normalizedModel(model) == "" {
			return errors.New("invalid ignored model")
		}
	}
	return nil
}
