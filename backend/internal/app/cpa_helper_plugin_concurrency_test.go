package app

import (
	"encoding/json"
	"testing"
)

func TestPluginActiveConcurrencyAggregatesAndValidates(t *testing.T) {
	encode := func(value any) json.RawMessage {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	got, err := pluginActiveConcurrency(map[string]json.RawMessage{
		"active": encode(map[string]int{"scope-a": 2, "scope-b": 3}),
	})
	if err != nil || got != 5 {
		t.Fatalf("aggregate = %d, %v; want 5", got, err)
	}

	for name, active := range map[string]any{
		"missing":  nil,
		"null":     nil,
		"array":    []int{},
		"negative": map[string]int{"scope-a": -1},
		"string":   map[string]string{"scope-a": "2"},
	} {
		t.Run(name, func(t *testing.T) {
			health := map[string]json.RawMessage{}
			if name != "missing" {
				health["active"] = encode(active)
			}
			if _, err := pluginActiveConcurrency(health); err == nil {
				t.Fatal("invalid active payload was accepted")
			}
		})
	}
}
