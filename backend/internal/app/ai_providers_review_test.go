package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAIProviderReviewNewRuleBeforeExistingKeepsSources(t *testing.T) {
	for _, protocol := range []string{"v0", "v8"} {
		t.Run(protocol, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			if protocol == "v8" {
				fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"rules","base-url":"https://rules.example","keys":[{"api-key":"review-rule-secret","request-scoped-errors":[{"status":429,"match":[" quota "],"action":"stop","future":"original-extension"}]}]}]}}`)
			} else {
				fake.config["codex-api-key"] = []map[string]any{{"api-key": "review-rule-secret", "base-url": "https://rules.example", "request-scoped-errors": []map[string]any{{"status": 429, "match": []string{" quota "}, "action": "stop", "future": "original-extension"}}}}
			}
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
			rules := provider["request_scoped_errors"].([]any)
			payload := aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = []any{map[string]any{"status": 503, "match": []string{" temporary "}, "action": "continue"}, rules[0]}
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			fake.mu.Lock()
			var written []map[string]any
			if err := json.Unmarshal(fake.providerPutBodies[0], &written); err != nil {
				t.Fatal(err)
			}
			entry := written[0]
			if protocol == "v8" {
				entry = entry["keys"].([]any)[0].(map[string]any)
			}
			result := entry["request-scoped-errors"].([]any)
			if _, exists := result[0].(map[string]any)["future"]; exists || result[1].(map[string]any)["future"] != "original-extension" {
				t.Fatalf("new rule consumed an existing rule's extension: %#v", result)
			}
			fake.mu.Unlock()

			// A newly added duplicate also has no source identity; it must not
			// consume the source of the existing, explicitly identified rule.
			provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
			rules = provider["request_scoped_errors"].([]any)
			payload = aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = []any{map[string]any{"status": 429, "match": []string{" quota "}, "action": "stop"}, rules[1], rules[0]}
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			fake.mu.Lock()
			if err := json.Unmarshal(fake.providerPutBodies[1], &written); err != nil {
				t.Fatal(err)
			}
			entry = written[0]
			if protocol == "v8" {
				entry = entry["keys"].([]any)[0].(map[string]any)
			}
			result = entry["request-scoped-errors"].([]any)
			if _, exists := result[0].(map[string]any)["future"]; exists || result[1].(map[string]any)["future"] != "original-extension" {
				t.Fatalf("duplicate new rule consumed an existing source: %#v", result)
			}
			if bytes.Contains(fake.providerPutBodies[1], []byte(`"source_hash"`)) {
				t.Fatal("rule source hash was persisted upstream")
			}
			writes := fake.providerPutCount
			fake.mu.Unlock()

			provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
			rules = provider["request_scoped_errors"].([]any)
			rules[1].(map[string]any)["source_hash"] = "stale-rule-source"
			payload = aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = rules
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.providerPutCount != writes {
				t.Fatal("stale rule source reached an upstream write")
			}
		})
	}
}

func TestAIProviderReviewCCHSigningPresence(t *testing.T) {
	for _, protocol := range []string{"v0", "v8"} {
		t.Run(protocol, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			if protocol == "v8" {
				fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"claude":[{"name":"cch","keys":[{"api-key":"review-cch-secret","experimental-cch-signing":true,"future":"preserve"}]}]}}`)
			} else {
				fake.config["claude-api-key"] = []map[string]any{{"api-key": "review-cch-secret", "experimental-cch-signing": true, "future": "preserve"}}
			}
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			for _, step := range []struct {
				name    string
				submit  bool
				value   any
				present bool
				want    bool
			}{
				{name: "omitted preserves true", present: true, want: true},
				{name: "null restores default", submit: true},
				{name: "omitted preserves absence"},
				{name: "explicit false", submit: true, value: false, present: true},
				{name: "omitted preserves false", present: true},
				{name: "explicit true", submit: true, value: true, present: true, want: true},
			} {
				t.Run(step.name, func(t *testing.T) {
					provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
					payload := aiProviderSnapshotPayload(provider)
					payload["request_retry"] = 1
					if step.submit {
						payload["experimental_cch_signing"] = step.value
					}
					requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusOK)
					fake.mu.Lock()
					defer fake.mu.Unlock()
					var entry map[string]any
					if protocol == "v8" {
						entry = aiProviderV8GroupsForTest(fake, "claude")[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
					} else {
						entry = fake.config["claude-api-key"].([]map[string]any)[0]
					}
					actual, present := entry["experimental-cch-signing"]
					if present != step.present || present && actual != step.want || entry["future"] != "preserve" {
						t.Fatalf("CCH presence/value or unknown field changed: %#v", entry)
					}
				})
			}
		})
	}
}

func TestAIProviderReviewV0CCHSigningClearMustBeVerified(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.config["claude-api-key"] = []map[string]any{{"api-key": "review-retained-cch-secret", "experimental-cch-signing": true}}
	fake.preserveFields = []string{"experimental-cch-signing"}
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["experimental_cch_signing"] = nil
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusConflict)
	if !bytes.Contains(body, []byte("provider_config_committed_refresh_required")) || bytes.Contains(body, []byte("review-retained-cch-secret")) {
		t.Fatalf("retained CCH clear did not report a safe committed warning: %s", body)
	}
}

func TestAIProviderReviewV0CCHSigningFalseDefaultCanBeOmitted(t *testing.T) {
	fake, unusedServer := newFakeAIProviderManagement(t)
	defer unusedServer.Close()
	fake.config["claude-api-key"] = []map[string]any{{"api-key": "review-cch-omitempty-secret", "experimental-cch-signing": true}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/claude-api-key" {
			// The pinned CPA v0 DTO stores this compatibility flag as bool with
			// omitempty, so its false value is absent in the returned JSON.
			fake.mu.Lock()
			entry := fake.config["claude-api-key"].([]map[string]any)[0]
			if entry["experimental-cch-signing"] == false {
				delete(entry, "experimental-cch-signing")
			}
			fake.mu.Unlock()
		}
		fake.handle(w, r)
	}))
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["experimental_cch_signing"] = false
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/claude/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !bytes.Contains(fake.providerPutBodies[0], []byte(`"experimental-cch-signing":false`)) {
		t.Fatal("explicit false was dropped from the write payload")
	}
}

func TestAIProviderReviewLegacyRuleMatchingDoesNotGuessNewSources(t *testing.T) {
	for _, protocol := range []string{"v0", "v8"} {
		t.Run(protocol, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			if protocol == "v8" {
				fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"rules","base-url":"https://rules.example","keys":[{"api-key":"review-legacy-rule-secret","request-scoped-errors":[{"match":[""],"future":"untouched-compatible-rule"}]}]}]}}`)
			} else {
				fake.config["codex-api-key"] = []map[string]any{{"api-key": "review-legacy-rule-secret", "base-url": "https://rules.example", "request-scoped-errors": []map[string]any{{"match": []string{""}, "future": "untouched-compatible-rule"}}}}
			}
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
			rule := provider["request_scoped_errors"].([]any)[0].(map[string]any)
			delete(rule, "source_hash")
			payload := aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = []any{rule}
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			fake.mu.Lock()
			if !bytes.Contains(fake.providerPutBodies[0], []byte("untouched-compatible-rule")) {
				t.Fatal("unchanged legacy rule lost its opaque extension")
			}
			fake.mu.Unlock()
			provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
			payload = aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = []any{map[string]any{"status": 503, "match": []string{"busy"}, "action": "continue"}}
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if bytes.Contains(fake.providerPutBodies[1], []byte("untouched-compatible-rule")) {
				t.Fatal("replacement rule inherited the deleted rule's opaque extension")
			}
		})
	}
}

func TestAIProviderReviewV8UnchangedModelPresencePreservesInheritance(t *testing.T) {
	fake, server := newFakeAIProviderManagement(t)
	defer server.Close()
	fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"inherited-models","base-url":"https://models.example","models":[{"name":"gpt-5","future":{"n":9007199254740993123}}],"keys":[{"api-key":"review-inherited-model-secret","future":"key-preserved"}]}]}}`)
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
	model := provider["models"].([]any)[0].(map[string]any)
	if _, exists := model["force_mapping"]; exists {
		t.Fatal("missing model flag was projected as an explicit value")
	}
	// This is the actual editor contract: unchanged omitted flags stay omitted,
	// while the full visible models list may be included with another edit.
	payload := aiProviderSnapshotPayload(provider)
	payload["models"] = []any{map[string]any{"name": "gpt-5"}}
	payload["request_retry"] = 2
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	group := aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)
	key := group["keys"].([]any)[0].(map[string]any)
	if _, exists := key["models"]; exists || key["future"] != "key-preserved" {
		t.Fatalf("unmodified inherited models became a key override: %#v", key)
	}
	group["models"].([]any)[0].(map[string]any)["name"] = "gpt-5.1"
	fake.mu.Unlock()
	provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
	model = provider["models"].([]any)[0].(map[string]any)
	if model["name"] != "gpt-5.1" {
		t.Fatalf("provider stopped inheriting group model updates: %#v", model)
	}
	payload = aiProviderSnapshotPayload(provider)
	payload["models"] = []any{map[string]any{"name": "gpt-5.1", "force_mapping": false}}
	requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
	fake.mu.Lock()
	group = aiProviderV8GroupsForTest(fake, "codex")[0].(map[string]any)
	key = group["keys"].([]any)[0].(map[string]any)
	model = key["models"].([]any)[0].(map[string]any)
	if model["force-mapping"] != false || model["future"].(map[string]any)["n"] != json.Number("9007199254740993123") {
		t.Fatalf("explicit false or unknown model extension was lost: %#v", model)
	}
	group["models"].([]any)[0].(map[string]any)["name"] = "gpt-6"
	fake.mu.Unlock()
	provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
	model = provider["models"].([]any)[0].(map[string]any)
	if model["name"] != "gpt-5.1" || model["force_mapping"] != false {
		t.Fatalf("explicit model override was treated as inherited/default: %#v", model)
	}
}

func TestAIProviderReviewErrorRuleActions(t *testing.T) {
	for _, protocol := range []string{"v0", "v8"} {
		t.Run(protocol, func(t *testing.T) {
			fake, server := newFakeAIProviderManagement(t)
			defer server.Close()
			if protocol == "v8" {
				fake.v8Config = aiProviderV8Fixture(t, `{"api-keys":{"codex":[{"name":"actions","base-url":"https://rules.example","keys":[{"api-key":"review-actions-secret","request-scoped-errors":[{"status":418,"match":["legacy"],"action":" FUTURE-ACTION ","future":"preserved"}]}]}]}}`)
			} else {
				fake.config["codex-api-key"] = []map[string]any{{"api-key": "review-actions-secret", "base-url": "https://rules.example", "request-scoped-errors": []map[string]any{{"status": 418, "match": []string{"legacy"}, "action": " FUTURE-ACTION ", "future": "preserved"}}}}
			}
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			provider := aiProviderSnapshotMaps(t, handler, cookies)[0]
			rules := provider["request_scoped_errors"].([]any)
			for _, action := range []string{" STOP ", "\tSTOP-AND-COOLDOWN\n", " CONTINUE ", "\nCONTINUE-AND-COOLDOWN\t"} {
				rules = append(rules, map[string]any{"status": 429, "match": []string{"rate"}, "action": action})
			}
			payload := aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = rules
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			assertWrittenActions := func(wantFirst string) {
				t.Helper()
				fake.mu.Lock()
				written := append([]byte(nil), fake.providerPutBodies[len(fake.providerPutBodies)-1]...)
				fake.mu.Unlock()
				var entries []map[string]any
				if err := json.Unmarshal(written, &entries); err != nil {
					t.Fatal(err)
				}
				entry := entries[0]
				if protocol == "v8" {
					entry = entry["keys"].([]any)[0].(map[string]any)
				}
				actual := entry["request-scoped-errors"].([]any)
				legacy := actual[0].(map[string]any)
				if legacy["action"] != " FUTURE-ACTION " || legacy["future"] != "preserved" {
					t.Fatalf("untouched compatibility rule changed: %#v", legacy)
				}
				for index, expected := range []string{wantFirst, "stop-and-cooldown", "continue", "continue-and-cooldown"} {
					if action := actual[index+1].(map[string]any)["action"]; action != expected {
						t.Errorf("written action %d = %q, want %q", index+1, action, expected)
					}
				}
			}
			assertWrittenActions("stop")
			provider = aiProviderSnapshotMaps(t, handler, cookies)[0]
			rules = provider["request_scoped_errors"].([]any)
			rules[1].(map[string]any)["action"] = " CONTINUE-AND-COOLDOWN "
			payload = aiProviderSnapshotPayload(provider)
			payload["request_scoped_errors"] = rules
			requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusOK)
			assertWrittenActions("continue-and-cooldown")
		})
	}
}

func TestAIProviderReviewV0ListDocuments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
		valid  bool
	}{
		{name: "trailing HTML", suffix: "<html>upstream failed</html>"},
		{name: "second array", suffix: " []"},
		{name: "second scalar", suffix: " true"},
		{name: "trailing whitespace preserves large integer", suffix: " \t\r\n", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, unusedServer := newFakeAIProviderManagement(t)
			defer unusedServer.Close()
			fake.config["xai-api-key"] = []map[string]any{{"api-key": "review-list-secret", "base-url": "https://lists.example", "models": []map[string]any{{"name": "grok-test", "thinking": map[string]any{"future-id": json.Number("9007199254740993123")}}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fake.handle(w, r)
				if r.Method == http.MethodGet && r.URL.Path == "/v0/management/xai-api-key" {
					_, _ = w.Write([]byte(tc.suffix))
				}
			}))
			defer server.Close()
			handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
			defer closeApp()
			status := http.StatusUnprocessableEntity
			if tc.valid {
				status = http.StatusOK
			}
			body := requestRawJSON(t, handler, http.MethodGet, "/api/ai-providers", nil, cookies, status)
			if tc.valid {
				if !bytes.Contains(body, []byte(`"future-id":9007199254740993123`)) {
					t.Fatalf("valid whitespace response lost numeric precision: %s", body)
				}
			} else if !bytes.Contains(body, []byte("validation_error")) {
				t.Fatalf("malformed response did not return validation error: %s", body)
			}
			if bytes.Contains(body, []byte("review-list-secret")) {
				t.Fatal("provider list response exposed the raw credential")
			}
		})
	}
}

func TestAIProviderReviewV0MalformedPolicyVerification(t *testing.T) {
	fake, unusedServer := newFakeAIProviderManagement(t)
	defer unusedServer.Close()
	fake.config["codex-api-key"] = []map[string]any{{"api-key": "review-verification-secret", "base-url": "https://lists.example"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.handle(w, r)
		fake.mu.Lock()
		written := fake.providerPutCount > 0
		fake.mu.Unlock()
		if written && r.Method == http.MethodGet && r.URL.Path == "/v0/management/codex-api-key" {
			_, _ = w.Write([]byte(" []"))
		}
	}))
	defer server.Close()
	handler, cookies, closeApp := setupAIProviderTestApp(t, server.URL)
	defer closeApp()
	payload := aiProviderSnapshotPayload(aiProviderSnapshotMaps(t, handler, cookies)[0])
	payload["request_retry"] = 2
	body := requestRawJSON(t, handler, http.MethodPut, "/api/ai-providers/codex/0", payload, cookies, http.StatusConflict)
	if !bytes.Contains(body, []byte("provider_config_committed_refresh_required")) || bytes.Contains(body, []byte("review-verification-secret")) {
		t.Fatalf("invalid write verification did not return a safe committed warning: %s", body)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.providerPutCount != 1 || fake.config["codex-api-key"].([]map[string]any)[0]["request-retry"] != float64(2) {
		t.Fatal("committed change was retried or reverted after malformed verification")
	}
}
