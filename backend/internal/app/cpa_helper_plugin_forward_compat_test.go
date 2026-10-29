package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// This is the v0.1.11 capability envelope. The simulated release deliberately
// changes both identifiers without adding a production version allowlist.
func pluginStableCapabilitiesFixture(version, origin string, ttl bool) map[string]json.RawMessage {
	modules := []string{"model_rules", "model_list_filter", "credential_routes", "concurrency"}
	fields := []string{"groups[].id", "groups[].name", "groups[].note", "groups[].rule", "keys[].id", "keys[].label", "keys[].enabled", "keys[].group_ids", "keys[].max_concurrency", "keys[].rule"}
	if ttl {
		modules = append(modules, "claude_cache_ttl")
		fields = append(fields, "keys[].claude_cache_ttl")
	}
	return map[string]json.RawMessage{
		"plugin_id": mustRawString("cpa-helper-plugin"), "plugin_version": mustRawString(version), "cpa_version": mustRawString(origin),
		"contract_version": mustRawString("v1"), "abi_version": mustRawNumber(1), "schema_version": mustRawNumber(7),
		"modules": mustRawStringArray(modules), "policy_fields": mustRawStringArray(fields), "model_list_filter_enabled": mustRawBool(true),
		"unknown_key": mustRawString("allow"), "concurrency_scope": mustRawString("instance"), "billing": mustRawBool(false),
		"claude_cache_ttl": mustRaw(map[string]any{"last_error": "D:/private/modern-plugin.yaml"}),
	}
}

func TestPluginStableManagementContractValidation(t *testing.T) {
	for _, producer := range []struct{ version, origin string }{{"0.1.11", "v8.0.45"}, {"0.1.42", "v8.0.47"}} {
		for _, test := range []struct {
			name string
			edit func(map[string]json.RawMessage)
		}{
			{"complete stable contract", nil},
			{"additive capabilities", func(c map[string]json.RawMessage) {
				c["future"] = mustRaw(map[string]any{"host_max_schema": 99})
				var modules []string
				_ = json.Unmarshal(c["modules"], &modules)
				c["modules"] = mustRawStringArray(append(modules, "future_optional_module"))
			}},
			{"unknown contract", func(c map[string]json.RawMessage) { c["contract_version"] = mustRawString("v2") }},
			{"unknown ABI", func(c map[string]json.RawMessage) { c["abi_version"] = mustRawNumber(2) }},
			{"plugin schema differs from host maximum", func(c map[string]json.RawMessage) {
				c["schema_version"], c["host_max_schema"] = mustRawNumber(8), mustRawNumber(9)
			}},
			{"missing schema", func(c map[string]json.RawMessage) { delete(c, "schema_version") }},
			{"missing provenance", func(c map[string]json.RawMessage) { delete(c, "cpa_version") }},
			{"wrong plugin identity", func(c map[string]json.RawMessage) { c["plugin_id"] = mustRawString("another-plugin") }},
			{"missing core module", func(c map[string]json.RawMessage) {
				c["modules"] = mustRawStringArray([]string{"model_rules", "model_list_filter", "concurrency"})
			}},
			{"missing core policy field", func(c map[string]json.RawMessage) { c["policy_fields"] = mustRawStringArray([]string{"keys[].rule"}) }},
			{"malformed module", func(c map[string]json.RawMessage) { c["modules"] = json.RawMessage(`["model_rules",true]`) }},
			{"null policy fields", func(c map[string]json.RawMessage) { c["policy_fields"] = mustRaw(nil) }},
			{"different unknown key behavior", func(c map[string]json.RawMessage) { c["unknown_key"] = mustRawString("deny") }},
			{"different concurrency scope", func(c map[string]json.RawMessage) { c["concurrency_scope"] = mustRawString("global") }},
			{"plugin billing", func(c map[string]json.RawMessage) { c["billing"] = mustRawBool(true) }},
			{"null billing", func(c map[string]json.RawMessage) { c["billing"] = mustRaw(nil) }},
		} {
			t.Run(producer.version+"/"+test.name, func(t *testing.T) {
				f, cfg := newPluginTTLHTTPFixture(t)
				f.caps = pluginStableCapabilitiesFixture(producer.version, producer.origin, true)
				if test.edit != nil {
					test.edit(f.caps)
				}
				caps, err := (&App{}).cpaHelperPluginCapabilities(context.Background(), cfg)
				if test.name == "complete stable contract" || test.name == "additive capabilities" {
					if err != nil {
						t.Fatal(err)
					}
					v8, err := pluginUsesV8Management(caps)
					if err != nil || !v8 || !pluginSupportsModelCatalog(caps) {
						t.Fatalf("stable contract did not select its bridge/catalog: %v", err)
					}
				} else {
					var problem *AppError
					if !errors.As(err, &problem) || problem.Code != "plugin_incompatible" {
						t.Fatalf("invalid required contract was accepted: %v", err)
					}
				}
				if !reflect.DeepEqual(f.requests, []string{"GET " + cpaHelperPluginManagementPrefix + "/capabilities"}) {
					t.Fatalf("capability check reached another management API: %v", f.requests)
				}
			})
		}
	}
}

func TestPluginStableContractPolicyPreservesUnmanagedFieldsAndRemovedVerifier(t *testing.T) {
	for _, version := range []string{"0.1.11", "0.1.42"} {
		t.Run(version, func(t *testing.T) {
			f, cfg := newPluginTTLHTTPFixture(t)
			f.caps = pluginStableCapabilitiesFixture(version, "v8.0.45", true)
			// A stale config field cannot bring the removed verifier back.
			f.caps["response_model_mismatch"] = mustRaw(map[string]any{"enabled": true})
			f.policy["plugin_version"] = mustRawString(version)
			a, handler, admin := newCatalogRouteApp(t, cfg)
			var owner int
			if err := a.db.QueryRow("SELECT id FROM users WHERE username = 'catalog-admin'").Scan(&owner); err != nil {
				t.Fatal(err)
			}
			const key = "modern-contract-owner-secret"
			if err := a.upsertUserAPIKey(context.Background(), owner, hashAPIKey(key), key, "Modern contract"); err != nil {
				t.Fatal(err)
			}
			f.policy["keys"] = mustRaw([]any{map[string]any{"id": pluginCallerScope(key), "enabled": true, "claude_cache_ttl": "1h", "future_remote": 99}})
			path := "/api/cpa-helper-plugin/policy/key/" + hashAPIKey(key)
			var policy struct {
				Revision uint64                     `json:"policy_revision"`
				Key      map[string]json.RawMessage `json:"key"`
			}
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, admin, 200, &policy)
			if _, exists := policy.Key["claude_cache_ttl"]; exists {
				t.Fatal("Key response still exposes the retired TTL control")
			}
			policy.Key["future"] = mustRawNumber(42)
			modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, admin, 200, nil)
			policy.Key = nil
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, admin, 200, &policy)
			remoteKeys, err := rawObjectArray(f.policy, "keys")
			if err != nil || len(remoteKeys) != 1 || rawString(remoteKeys[0]["claude_cache_ttl"]) != "1h" || rawUint(remoteKeys[0]["future_remote"]) != 99 {
				t.Fatalf("Key edit lost unmanaged remote fields: %#v/%v", remoteKeys, err)
			}
			if policy.Revision != 8 || rawUint(policy.Key["future"]) != 42 || f.writeCount != 1 {
				t.Fatalf("modern policy CAS/readback lost intended fields: %#v, writes=%d", policy, f.writeCount)
			}
			policy.Key["claude_cache_ttl"] = mustRawString("5m")
			modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, admin, 422, nil)
			delete(policy.Key, "claude_cache_ttl")
			if f.writeCount != 1 {
				t.Fatal("retired Key control reached a remote write")
			}
			var status map[string]json.RawMessage
			modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/status", nil, admin, 200, &status)
			if _, exists := status["claude_cache_ttl"]; exists {
				t.Fatal("retired TTL status is still public")
			}
			if strings.Contains(string(mustRaw(status)), "private/") || string(status["response_model_verification_supported"]) != "false" {
				t.Fatalf("modern status exposed secrets or resurrected verification: %s", mustRaw(status))
			}
			modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/response-model", nil, admin, 503, nil)
			modelMonitorRequest(t, handler, http.MethodPut, "/api/cpa-helper-plugin/response-model", map[string]any{"enabled": true}, admin, 503, nil)
			for _, request := range f.requests {
				if strings.Contains(request, "response-model/validate") || strings.Contains(request, "response_model_mismatch") || strings.Contains(request, "/claude-ttl/") {
					t.Fatalf("removed verifier reached upstream: %s", request)
				}
			}
			member := newAIRadarMemberCookies(t, handler, admin)
			before := len(f.requests)
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, member, 404, nil)
			modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, member, 404, nil)
			if len(f.requests) != before {
				t.Fatal("foreign Key ownership check reached management IO")
			}
		})
	}
}

func TestPluginStableContractCatalogProvenance(t *testing.T) {
	for _, producer := range []struct{ version, origin string }{{"0.1.11", "v8.0.45"}, {"0.1.42", "v8.0.47"}} {
		t.Run(producer.version, func(t *testing.T) {
			f, cfg := newCatalogHTTPFixture(t)
			configureAuthoritativeCatalogFixture(f, producer.version)
			f.cpaTarget, f.capabilities = producer.origin, pluginStableCapabilitiesFixture(producer.version, producer.origin, true)
			caps, err := (&App{}).cpaHelperPluginCapabilities(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			contract, err := pluginManagementCompatibility(caps)
			if err != nil {
				t.Fatal(err)
			}
			for _, prior := range []struct{ version, origin string }{
				{"0.1.8", "v8.0.32"}, {"0.1.9", "v8.0.45"}, {"0.1.10", "v8.0.45"}, {"0.1.11", "v8.0.45"}, {"0.1.21", "v8.0.46"}, producer,
			} {
				f.seedCatalog(7)
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"0.1.8"`), mustRawString(prior.version))
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"v8.0.32"`), mustRawString(prior.origin))
				before := append([]byte(nil), f.catalog...)
				revision, snapshot, err := (&App{}).pluginCatalogState(context.Background(), cfg, contract)
				if err != nil || snapshot == nil || revision != 7 || snapshot.PluginVersion != prior.version || snapshot.CPAVersion != prior.origin || !bytes.Equal(before, f.catalog) {
					t.Fatalf("valid stored producer %s/%s was rejected or rewritten: %#v, %v", prior.version, prior.origin, snapshot, err)
				}
			}
			result, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, caps)
			if err != nil {
				t.Fatal(err)
			}
			if result.Snapshot.PluginVersion != producer.version || result.Snapshot.CPAVersion != producer.origin || result.Snapshot.Revision != 8 || len(result.Snapshot.Credentials) != 5 || f.registeredReads != 2 {
				t.Fatalf("publication/readback did not use captured producer and full registry: %#v", result.Snapshot)
			}
			for _, bad := range []struct{ version, origin string }{{producer.version, "wrong-origin"}, {"0.1.8", "v8.0.45"}, {"0.1.7", "v8.0.29"}} {
				f.seedCatalog(7)
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"0.1.8"`), mustRawString(bad.version))
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"v8.0.32"`), mustRawString(bad.origin))
				_, _, err := (&App{}).pluginCatalogState(context.Background(), cfg, contract)
				var problem *AppError
				if !errors.As(err, &problem) || problem.Code != "plugin_incompatible" {
					t.Fatalf("invalid producer provenance %s/%s was accepted: %v", bad.version, bad.origin, err)
				}
			}
		})
	}
}

func TestPluginStableContractDirectoryUsesAuthoritativeModels(t *testing.T) {
	for _, version := range []string{"0.1.11", "0.1.42"} {
		t.Run(version, func(t *testing.T) {
			f, cfg := newCatalogHTTPFixture(t)
			configureAuthoritativeCatalogFixture(f, version)
			f.capabilities = pluginStableCapabilitiesFixture(version, f.cpaTarget, true)
			_, handler, admin := newCatalogRouteApp(t, cfg)
			var directory struct {
				Models []string `json:"models"`
			}
			modelMonitorRequest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, admin, 200, &directory)
			if !reflect.DeepEqual(directory.Models, []string{"registered-a", "registered-b", "registered-c", "registered-claude", "registered-public"}) {
				t.Fatalf("modern directory omitted Host public models: %v", directory.Models)
			}
			for _, request := range f.requests {
				if request.path == "/v1/models" || strings.HasPrefix(request.path, "/v0/management/config") || request.path == "/v0/management/auth-files" || request.path == "/v8/management/credentials/models" {
					t.Fatalf("modern directory used a legacy/filtered inventory API: %s", request.path)
				}
			}
			if f.registeredReads != 2 || f.revision != 1 {
				t.Fatalf("modern directory skipped full catalog synchronization: reads=%d, revision=%d", f.registeredReads, f.revision)
			}
		})
	}
}

func TestPluginStableContractCatalogRejectsIdentityDrift(t *testing.T) {
	for _, stage := range []string{"before publication", "after publication", "during readback"} {
		for _, field := range []string{"producer", "origin"} {
			t.Run(stage+"/"+field, func(t *testing.T) {
				f, cfg := newCatalogHTTPFixture(t)
				configureAuthoritativeCatalogFixture(f, "0.1.11")
				f.capabilities = pluginStableCapabilitiesFixture(f.version, f.cpaTarget, true)
				f.seedCatalog(7)
				before := append([]byte(nil), f.catalog...)
				drift := func(f *catalogHTTPFixture) {
					if field == "producer" {
						f.version = "0.1.42"
					} else {
						f.cpaTarget = "v8.0.47"
					}
				}
				if stage == "before publication" {
					f.onSecondRegistered = drift
				} else if stage == "after publication" {
					f.onCatalogWrite = drift
				} else {
					f.onSecondCatalogRead = drift
				}
				_, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, f.capabilities)
				var problem *AppError
				if !errors.As(err, &problem) || problem.Code != "model_catalog_out_of_sync" {
					t.Fatalf("identity drift returned success or wrong error: %v", err)
				}
				requests, after := f.recorded()
				writes := 0
				for _, request := range requests {
					if request.method == http.MethodPut && strings.HasSuffix(request.path, "/model-catalog") {
						writes++
					}
				}
				if stage == "before publication" && (writes != 0 || !bytes.Equal(before, after)) {
					t.Fatal("known identity drift overwrote the existing catalog")
				}
				if stage != "before publication" && writes != 1 {
					t.Fatalf("post-write drift fixture did not exercise accepted publication: writes=%d", writes)
				}
			})
		}
	}
}
