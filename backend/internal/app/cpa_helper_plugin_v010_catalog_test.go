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

func configureAuthoritativeCatalogFixture(f *catalogHTTPFixture, version string) map[string]json.RawMessage {
	f.version, f.cpaTarget = version, "v8.0.45"
	f.registered = []pluginRegisteredCredentialModels{
		{Ref: catalogFixtureRef(catalogPrimaryID), AuthIndex: "Display-A", Provider: "codex", Models: []string{"registered-a", "registered-b"}},
		{Ref: catalogFixtureRef(catalogDuplicateID), AuthIndex: "Display-duplicate", Provider: "codex", Models: []string{}},
		{Ref: catalogFixtureRef(catalogSecondaryID), AuthIndex: "Display-B", Provider: "codex", Models: []string{"registered-c"}},
		{Ref: catalogFixtureRef(catalogKeylessID), AuthIndex: "Display-keyless", Provider: "openai-compatible-public relay", Models: []string{"registered-public"}},
		{Ref: catalogFixtureRef(catalogOAuthID), AuthIndex: "Display-OAuth", Provider: "claude", Models: []string{"registered-claude"}},
		// Some runtime Host entries are not in credentials inventory/config.
		{Ref: catalogFixtureRef("runtime-only-no-models"), AuthIndex: "Runtime-empty", Provider: "codex", Models: []string{}},
	}
	return map[string]json.RawMessage{"contract_version": mustRawString("v1"), "plugin_version": mustRawString(version), "cpa_version": mustRawString("v8.0.45"), "abi_version": mustRawNumber(1), "schema_version": mustRawNumber(7)}
}

func TestPluginAuthoritativeCatalogUsesFullHostRegistry(t *testing.T) {
	for _, version := range []string{"0.1.9", "0.1.10"} {
		t.Run(version, func(t *testing.T) {
			f, cfg := newCatalogHTTPFixture(t)
			caps := configureAuthoritativeCatalogFixture(f, version)
			f.seedCatalog(7) // upgrade reads the old producer without rewriting it first
			if version == "0.1.10" {
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"0.1.8"`), []byte(`"0.1.9"`))
				f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"v8.0.32"`), []byte(`"v8.0.45"`))
			}
			result, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, caps)
			if err != nil {
				t.Fatal(err)
			}
			if result.Snapshot.PluginVersion != version || result.Snapshot.CPAVersion != "v8.0.45" || result.Snapshot.Revision != 8 || len(result.Snapshot.Credentials) != 5 || f.registeredReads != 2 || f.configReads != 2 || f.credentialReads != 2 {
				t.Fatalf("wrong versioned/full-read catalog: %#v, reads=%d/%d/%d", result.Snapshot, f.registeredReads, f.configReads, f.credentialReads)
			}
			if got := pluginCatalogModels(result.Snapshot); !reflect.DeepEqual(got, []string{"registered-a", "registered-b", "registered-c", "registered-claude", "registered-public"}) {
				t.Fatalf("catalog did not use Host public models: %v", got)
			}
			requests, body := f.recorded()
			for _, req := range requests {
				if req.path == "/v8/management/credentials/models" {
					t.Fatal("new Host contract queried the obsolete per-credential path")
				}
			}
			for _, secret := range []string{catalogOAuthID, catalogPrimaryID, "Display-A", "runtime-only-no-models", "catalog-primary-secret", "unregistered-alias"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("catalog leaked non-storage metadata %q", secret)
				}
			}
		})
	}
}

func TestPluginAuthoritativeCatalogRejectsDriftAndBrokenJoin(t *testing.T) {
	cases := []struct {
		name string
		edit func(*catalogHTTPFixture)
		code string
	}{
		{"provider mismatch", func(f *catalogHTTPFixture) { f.registered[0].Provider = "claude" }, "model_catalog_sync_failed"},
		{"index mismatch", func(f *catalogHTTPFixture) { f.registered[0].AuthIndex = "wrong-index" }, "model_catalog_sync_failed"},
		{"uncovered config entry", func(f *catalogHTTPFixture) { f.registered = f.registered[1:] }, "model_catalog_sync_failed"},
		{"nonempty unknown Host entry", func(f *catalogHTTPFixture) { f.registered[5].Models = []string{"cannot-guess-source"} }, "model_catalog_sync_failed"},
		{"null model array", func(f *catalogHTTPFixture) { f.registered[0].Models = nil }, "plugin_incompatible"},
		{"duplicate Host ref", func(f *catalogHTTPFixture) { f.registered = append(f.registered, f.registered[0]) }, "plugin_incompatible"},
		{"duplicate Host field", func(f *catalogHTTPFixture) { f.registeredResponse = []byte(`{"credentials":[],"credentials":[]}`) }, "plugin_incompatible"},
		{"models change between reads", func(f *catalogHTTPFixture) {
			f.onSecondRegistered = func(f *catalogHTTPFixture) { f.registered[0].Models = []string{"changed-model"} }
		}, "model_catalog_out_of_sync"},
		{"Host-only empty identity changes", func(f *catalogHTTPFixture) {
			f.onSecondRegistered = func(f *catalogHTTPFixture) { f.registered[5].AuthIndex = "changed-empty-index" }
		}, "model_catalog_out_of_sync"},
		{"metadata index changes", func(f *catalogHTTPFixture) {
			f.onSecondCredentials = func(f *catalogHTTPFixture) { f.files[1]["auth_index"] = "changed-oauth-index" }
		}, "model_catalog_out_of_sync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, cfg := newCatalogHTTPFixture(t)
			caps := configureAuthoritativeCatalogFixture(f, "0.1.10")
			f.seedCatalog(7)
			prior := append([]byte(nil), f.catalog...)
			tc.edit(f)
			_, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, caps)
			var problem *AppError
			if !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("error=%v; want %s", err, tc.code)
			}
			requests, after := f.recorded()
			if !bytes.Equal(prior, after) {
				t.Fatal("failed read/join overwrote the stored catalog")
			}
			for _, req := range requests {
				if req.method == http.MethodPut {
					t.Fatal("drift/broken join attempted catalog publication")
				}
			}
		})
	}
}

func TestPluginCatalogDoesNotReadNewProducerOnOldRelease(t *testing.T) {
	f, cfg := newCatalogHTTPFixture(t)
	f.seedCatalog(7)
	f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"0.1.8"`), []byte(`"0.1.10"`))
	f.catalog = bytes.ReplaceAll(f.catalog, []byte(`"v8.0.32"`), []byte(`"v8.0.45"`))
	if _, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities()); err == nil {
		t.Fatal("0.1.8 accepted a newer catalog producer")
	}
}

func TestPluginAuthoritativeCatalogKeyEditorKeepsCompleteModelsAfterSave(t *testing.T) {
	for _, version := range []string{"0.1.8", "0.1.9", "0.1.10"} {
		t.Run(version, func(t *testing.T) {
			f, cfg := newCatalogHTTPFixture(t)
			if version != "0.1.8" {
				configureAuthoritativeCatalogFixture(f, version)
			}
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
			a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.db.Close() })
			handler := a.Routes()
			admin := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, 200, nil)
			modelMonitorRequest(t, handler, http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": cfg.Collector.CLIProxyURL, "management_key": cfg.Collector.ManagementKey, "collector_enabled": false}, admin, 200, nil)
			var ownerID int
			if err := a.db.QueryRow("SELECT id FROM users WHERE username = 'admin'").Scan(&ownerID); err != nil {
				t.Fatal(err)
			}
			const key = "rule-editor-key"
			if err := a.upsertUserAPIKey(context.Background(), ownerID, hashAPIKey(key), key, "Rule editor"); err != nil {
				t.Fatal(err)
			}
			path := "/api/cpa-helper-plugin/policy/key/" + hashAPIKey(key)
			var policy struct {
				Revision uint64                     `json:"policy_revision"`
				Key      map[string]json.RawMessage `json:"key"`
			}
			modelMonitorRequest(t, handler, http.MethodGet, path, nil, admin, 200, &policy)
			directoryPath := "/api/cpa-helper-plugin/directory?api_key_hash=" + hashAPIKey(key)
			var before, after struct {
				Models []string `json:"models"`
			}
			modelMonitorRequest(t, handler, http.MethodGet, directoryPath, nil, admin, 200, &before)
			if len(before.Models) < 4 {
				t.Fatalf("fixture did not publish its complete model universe: %v", before.Models)
			}
			policy.Key["rule"] = mustRaw(map[string]any{"models": before.Models[:1], "credential_ids": []string{catalogFixtureRef(catalogPrimaryID), catalogFixtureRef(catalogSecondaryID)}})
			modelMonitorRequest(t, handler, http.MethodPut, path, map[string]any{"policy_revision": policy.Revision, "key": policy.Key}, admin, 200, nil)
			modelMonitorRequest(t, handler, http.MethodGet, directoryPath, nil, admin, 200, &after)
			if !reflect.DeepEqual(before.Models, after.Models) {
				t.Fatalf("saved restriction removed edit choices: before=%v after=%v", before.Models, after.Models)
			}
			requests, _ := f.recorded()
			for _, request := range requests {
				if request.path == "/v1/models" {
					t.Fatal("rule editor still queried a policy-filtered caller model list")
				}
			}
		})
	}
}
