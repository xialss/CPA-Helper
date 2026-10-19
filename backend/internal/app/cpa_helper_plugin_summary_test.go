package app

import (
	"encoding/json"
	"testing"
)

func TestPluginKeyHasCustomRule(t *testing.T) {
	for _, raw := range []string{
		"", "null", `{}`,
		`{"models":[],"denied_models":[],"credential_ids":[],"denied_credential_ids":[],"credential_providers":[],"denied_credential_providers":[]}`,
		`{"models":null,"credential_ids":null}`,
		`{"future_field":true,"enabled":false,"max_concurrency":7}`,
	} {
		custom, err := pluginKeyHasCustomRule(json.RawMessage(raw))
		if err != nil || custom {
			t.Errorf("empty/unknown rule %q = %v, %v; want no custom rule", raw, custom, err)
		}
	}
	for _, field := range []string{"models", "denied_models", "credential_ids", "denied_credential_ids", "credential_providers", "denied_credential_providers"} {
		t.Run(field, func(t *testing.T) {
			var selection any = []string{"selected-value"}
			if field == "credential_providers" || field == "denied_credential_providers" {
				selection = []map[string]string{{"source": "ai-providers", "provider": "codex"}}
			}
			raw, err := json.Marshal(map[string]any{field: selection})
			if err != nil {
				t.Fatal(err)
			}
			custom, err := pluginKeyHasCustomRule(raw)
			if err != nil || !custom {
				t.Fatalf("nonempty %s = %v, %v; want custom rule", field, custom, err)
			}
		})
	}
	for _, raw := range []string{
		`[]`, `{"models":{}}`, `{"models":[null]}`, `{"models":[""]}`,
		`{"credential_providers":[{}]}`, `{"denied_credential_providers":["codex"]}`,
	} {
		if _, err := pluginKeyHasCustomRule(json.RawMessage(raw)); err == nil {
			t.Errorf("malformed rule %s was accepted", raw)
		}
	}
}
