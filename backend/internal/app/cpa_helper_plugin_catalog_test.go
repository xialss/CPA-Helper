package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	catalogPrimaryID   = "codex:apikey:e1e98e1fcfb6"
	catalogDuplicateID = catalogPrimaryID + "-1"
	catalogSecondaryID = "codex:apikey:9ba4366c5bfa"
	catalogKeylessID   = "openai-compatibility:public relay:101485ff838d"
	catalogOAuthID     = "oauth client & one.json"
)

type catalogHTTPRequest struct {
	method string
	path   string
	authID string
	header http.Header
	body   []byte
}

type catalogHTTPFixture struct {
	mu                  sync.Mutex
	config              map[string]any
	files               []map[string]any
	models              map[string][]byte
	modelStatus         map[string]int
	requests            []catalogHTTPRequest
	catalog             []byte
	revision            uint64
	writeStatus         int
	acceptedRevision    *uint64
	healthRevision      *uint64
	healthStatus        string
	stateResponse       []byte
	catalogReads        int
	onSecondCatalogRead func(*catalogHTTPFixture)
	configReads         int
	credentialReads     int
	onSecondConfig      func(*catalogHTTPFixture)
	onSecondCredentials func(*catalogHTTPFixture)
	modelStarted        chan struct{}
	modelRelease        chan struct{}
	version             string
	cpaTarget           string
	capabilities        map[string]json.RawMessage
	registered          []pluginRegisteredCredentialModels
	registeredReads     int
	registeredResponse  []byte
	onSecondRegistered  func(*catalogHTTPFixture)
	onCatalogWrite      func(*catalogHTTPFixture)
	downstream          []string
	policy              map[string]json.RawMessage
	needsSync           map[string]string
	afterSyncError      map[string]string
}

func catalogFixtureRef(id string) string {
	sum := sha256.Sum256([]byte("cpa-key-billing:credential:v1\x00" + id))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newCatalogHTTPFixture(t *testing.T) (*catalogHTTPFixture, AppConfig) {
	t.Helper()
	fixture := &catalogHTTPFixture{
		version: "0.1.8", cpaTarget: "v8.0.32", healthStatus: "ok",
		downstream:  []string{},
		modelStatus: map[string]int{}, needsSync: map[string]string{}, afterSyncError: map[string]string{},
		policy: map[string]json.RawMessage{
			"contract_version": mustRawString("v1"), "plugin_version": mustRawString("0.1.8"), "policy_revision": mustRawNumber(44),
			"groups": mustRaw([]any{}), "keys": mustRaw([]any{}),
		},
		config: map[string]any{
			"access": map[string]any{"api-keys": []any{}},
			"api-keys": map[string]any{
				"codex": []any{map[string]any{
					"base-url": "https://catalog-a.invalid/v1", "proxy-url": "https://proxy.invalid", "prefix": "/team/",
					"headers": map[string]any{"X-B": "b", "X-A": "a"},
					"models":  []any{map[string]any{"name": "upstream-private-model", "alias": "unregistered-alias"}},
					"keys": []any{
						map[string]any{"api-key": "catalog-primary-secret-1234", "auth_index": "Display-A"},
						map[string]any{"api-key": "catalog-primary-secret-1234", "auth_index": "Display-duplicate"},
						map[string]any{"api-key": "catalog-secondary-secret-5678", "auth_index": "Display-B", "proxy-url": "", "prefix": "", "headers": map[string]any{}},
					},
				}},
				"openai-compatibility": []any{map[string]any{"name": "Public Relay", "base-url": "https://keyless.invalid/v1", "keys": nil, "auth_index": "Display-keyless"}},
			},
			"plugins":        map[string]any{"configs": map[string]any{"cpa-helper-plugin": map[string]any{"enabled": true, "state_dir": "keep-state", "model_list_filter_enabled": true}}},
			"future_integer": json.Number("9007199254740993"),
		},
		files: []map[string]any{
			{"id": catalogPrimaryID, "auth_index": "Display-A", "source": "memory", "runtime_only": true, "provider": "CODEX", "name": "Old runtime name", "status": "cooldown", "weight": 0, "secret": "runtime-secret"},
			{"id": catalogOAuthID, "auth_index": "Display-OAuth", "source": "file", "provider": "CLAUDE", "name": "OAuth account", "status": "cooldown", "disabled": false, "weight": 1},
		},
		models: map[string][]byte{
			catalogPrimaryID:   []byte(`{"models":[{"id":"team/public-alias","name":"upstream-private-model"},{"id":"TEAM/public-alias"}]}`),
			catalogDuplicateID: []byte(`{"models":[{"id":"duplicate-public"}]}`),
			catalogSecondaryID: []byte(`{"models":[{"id":"secondary-public"}]}`),
			catalogKeylessID:   []byte(`{"models":[{"id":"keyless-public"}]}`),
			catalogOAuthID:     []byte(`{"models":[{"id":"oauth-public"}]}`),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.requests = append(fixture.requests, catalogHTTPRequest{r.Method, r.URL.Path, r.URL.Query().Get("name"), r.Header.Clone(), body})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/models" && r.Header.Get("Authorization") != "Bearer catalog-management-secret" {
			t.Error("management request lacks the captured management credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload any
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/capabilities":
			payload = map[string]any{"contract_version": "v1", "plugin_version": fixture.version, "cpa_version": fixture.cpaTarget, "abi_version": 1, "schema_version": 7, "modules": []string{"model_list_filter"}, "model_list_filter_enabled": true}
			if fixture.capabilities != nil {
				caps := cloneRawObject(fixture.capabilities)
				caps["plugin_version"], caps["cpa_version"] = mustRawString(fixture.version), mustRawString(fixture.cpaTarget)
				payload = caps
			}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/directory":
			payload = map[string]any{"credentials": []any{}}
		case "GET /v8/management/config":
			fixture.configReads++
			if fixture.configReads == 2 && fixture.onSecondConfig != nil {
				fixture.onSecondConfig(fixture)
			}
			payload = fixture.config
		case "GET /v8/management/credentials":
			fixture.credentialReads++
			if fixture.credentialReads == 2 && fixture.onSecondCredentials != nil {
				fixture.onSecondCredentials(fixture)
			}
			payload = map[string]any{"files": fixture.files}
		case "GET /v8/management/credentials/models":
			if fixture.modelStarted != nil {
				close(fixture.modelStarted)
				fixture.modelStarted = nil
				<-fixture.modelRelease
			}
			id := r.URL.Query().Get("name")
			models, exists := fixture.models[id]
			if !exists {
				t.Errorf("models requested with an auth_index/ref or wrong scheduler ID: %q", id)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if status := fixture.modelStatus[id]; status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"untrusted-code-catalog-secret","message":"catalog-management-secret"}}`))
				return
			}
			_, _ = w.Write(models)
			return
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/credential-models":
			fixture.registeredReads++
			if fixture.registeredReads == 2 && fixture.onSecondRegistered != nil {
				fixture.onSecondRegistered(fixture)
			}
			if fixture.registeredResponse != nil {
				_, _ = w.Write(fixture.registeredResponse)
				return
			}
			payload = map[string]any{"credentials": fixture.registered}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/model-catalog":
			fixture.catalogReads++
			if fixture.catalogReads == 2 && fixture.onSecondCatalogRead != nil {
				fixture.onSecondCatalogRead(fixture)
			}
			w.Header().Set("ETag", fmt.Sprintf("\"%d\"", fixture.revision))
			if fixture.stateResponse != nil {
				_, _ = w.Write(fixture.stateResponse)
				return
			}
			healthStatus := "unavailable"
			var snapshot any
			if fixture.catalog != nil {
				healthStatus, snapshot = "ok", json.RawMessage(fixture.catalog)
			}
			payload = map[string]any{"snapshot": snapshot, "health": map[string]any{"status": healthStatus, "catalog_revision": fixture.revision, "last_error": "untrusted-health-secret"}}
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/model-catalog":
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", fixture.revision) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusPreconditionRequired)
				return
			}
			if fixture.writeStatus != 0 {
				w.WriteHeader(fixture.writeStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"revision_conflict","message":"catalog-management-secret"}}`))
				return
			}
			if err := validateCatalogHTTPBody(body, fixture.revision+1, fixture.version, fixture.cpaTarget); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.revision++
			fixture.catalog = append([]byte(nil), body...)
			fixture.needsSync = map[string]string{}
			if fixture.onCatalogWrite != nil {
				fixture.onCatalogWrite(fixture)
			}
			revision := fixture.revision
			if fixture.acceptedRevision != nil {
				revision = *fixture.acceptedRevision
			}
			payload = map[string]any{"catalog_revision": revision}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/health":
			revision := fixture.revision
			if fixture.healthRevision != nil {
				revision = *fixture.healthRevision
			}
			payload = map[string]any{"policy_revision": fixture.policy["policy_revision"], "active": map[string]int{"untouched-scope": 7}, "model_catalog": map[string]any{"status": fixture.healthStatus, "catalog_revision": revision, "last_error": "untrusted-health-secret"}}
		case "GET /v0/management/plugins/cpa-helper-plugin/v1/policy":
			payload = fixture.policy
		case "PUT /v0/management/plugins/cpa-helper-plugin/v1/policy":
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", rawUint(fixture.policy["policy_revision"])) || r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if json.Unmarshal(body, &fixture.policy) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload = map[string]any{"policy_revision": fixture.policy["policy_revision"]}
		case "GET /v0/management/api-keys":
			payload = map[string]any{"api-keys": fixture.downstream}
		case "PUT /v0/management/api-keys":
			if json.Unmarshal(body, &fixture.downstream) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload = map[string]any{"ok": true}
		case "GET /v1/models":
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			code := fixture.needsSync[key]
			if code == "" {
				code = fixture.afterSyncError[key]
			}
			if code != "" {
				w.Header().Set("X-Cpa-Helper-Error", code)
				payload = map[string]any{"error": map[string]any{"code": code, "message": "catalog-management-secret"}}
			} else {
				payload = map[string]any{"data": fixture.visibleModels(key)}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return fixture, AppConfig{Collector: CollectorConfig{CLIProxyURL: server.URL, ManagementKey: "catalog-management-secret"}}
}

// The fake validates the published field sets independently of the production
// decoder, and implements actual conditional state changes rather than fixed OKs.
func validateCatalogHTTPBody(raw []byte, revision uint64, version, cpaTarget string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 6 {
		return errors.New("catalog body is not a complete six-field object")
	}
	for _, field := range []string{"contract_version", "plugin_version", "cpa_version", "catalog_revision", "generated_at", "credentials"} {
		if len(object[field]) == 0 || bytes.Equal(bytes.TrimSpace(object[field]), []byte("null")) {
			return fmt.Errorf("catalog lacks required field %s", field)
		}
	}
	if string(object["contract_version"]) != `"v1"` || rawString(object["plugin_version"]) != version || rawString(object["cpa_version"]) != cpaTarget || rawUint(object["catalog_revision"]) != revision {
		return errors.New("catalog envelope has wrong version or independent revision")
	}
	var generated time.Time
	if json.Unmarshal(object["generated_at"], &generated) != nil || generated.IsZero() {
		return errors.New("catalog lacks a timestamp")
	}
	var credentials []map[string]json.RawMessage
	if json.Unmarshal(object["credentials"], &credentials) != nil || credentials == nil {
		return errors.New("catalog credentials must be an explicit array")
	}
	for _, credential := range credentials {
		if len(credential) != 5 {
			return errors.New("catalog credential includes display or secret fields")
		}
		for _, field := range []string{"ref", "source", "provider", "enabled", "models"} {
			if len(credential[field]) == 0 || bytes.Equal(bytes.TrimSpace(credential[field]), []byte("null")) {
				return fmt.Errorf("catalog credential lacks %s", field)
			}
		}
	}
	return nil
}

func (fixture *catalogHTTPFixture) visibleModels(rawKey string) []any {
	var catalog pluginModelCatalogSnapshot
	if fixture.catalog == nil || json.Unmarshal(fixture.catalog, &catalog) != nil {
		return []any{}
	}
	return catalogFixtureVisibleModels(catalog.Credentials, fixture.policy, rawKey)
}

func catalogFixtureVisibleModels(credentials []pluginModelCatalogCredential, policy map[string]json.RawMessage, rawKey string) []any {
	rules := []map[string]json.RawMessage{}
	keys, _ := rawObjectArray(policy, "keys")
	groups, _ := rawObjectArray(policy, "groups")
	for _, key := range keys {
		if rawString(key["id"]) != pluginCallerScope(rawKey) {
			continue
		}
		if bytes.Equal(key["enabled"], []byte("false")) {
			return []any{}
		}
		var ids []string
		_ = json.Unmarshal(key["group_ids"], &ids)
		for _, id := range ids {
			for _, group := range groups {
				if rawString(group["id"]) == id {
					var rule map[string]json.RawMessage
					_ = json.Unmarshal(group["rule"], &rule)
					rules = append(rules, rule)
				}
			}
		}
		var rule map[string]json.RawMessage
		_ = json.Unmarshal(key["rule"], &rule)
		rules = append(rules, rule)
	}
	// The plugin unions the selections of linked groups and the independent
	// rule, then gives every denied selection precedence over every allow.
	merged := map[string]json.RawMessage{}
	for _, field := range []string{"models", "denied_models", "credential_ids", "denied_credential_ids", "credential_providers", "denied_credential_providers"} {
		selections := []json.RawMessage{}
		for _, rule := range rules {
			var values []json.RawMessage
			_ = json.Unmarshal(rule[field], &values)
			selections = append(selections, values...)
		}
		merged[field] = mustRaw(selections)
	}
	seen := map[string]bool{}
	result := []any{}
	for _, credential := range credentials {
		if !credential.Enabled {
			continue
		}
		for _, model := range credential.Models {
			allowed := catalogFixtureRuleAllows(merged, model, credential)
			if allowed && !seen[model] {
				seen[model] = true
				result = append(result, map[string]any{"id": model})
			}
		}
	}
	return result
}

func catalogFixtureRuleAllows(rule map[string]json.RawMessage, model string, credential pluginModelCatalogCredential) bool {
	for _, pair := range []struct{ allow, deny, value string }{
		{"models", "denied_models", model}, {"credential_ids", "denied_credential_ids", credential.Ref},
	} {
		var allow, deny []string
		_ = json.Unmarshal(rule[pair.allow], &allow)
		_ = json.Unmarshal(rule[pair.deny], &deny)
		matched := len(allow) == 0
		for _, value := range allow {
			matched = matched || strings.EqualFold(value, pair.value)
		}
		for _, value := range deny {
			if strings.EqualFold(value, pair.value) {
				return false
			}
		}
		if pair.allow == "credential_ids" {
			var categories, denied []struct {
				Source   string `json:"source"`
				Provider string `json:"provider"`
			}
			_ = json.Unmarshal(rule["credential_providers"], &categories)
			_ = json.Unmarshal(rule["denied_credential_providers"], &denied)
			matched = len(allow) == 0 && len(categories) == 0
			for _, value := range allow {
				matched = matched || strings.EqualFold(value, credential.Ref)
			}
			for _, category := range categories {
				matched = matched || (category.Source == credential.Source && category.Provider == credential.Provider)
			}
			for _, category := range denied {
				if category.Source == credential.Source && category.Provider == credential.Provider {
					return false
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (fixture *catalogHTTPFixture) recorded() ([]catalogHTTPRequest, []byte) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]catalogHTTPRequest(nil), fixture.requests...), append([]byte(nil), fixture.catalog...)
}

func catalogCapabilities() map[string]json.RawMessage {
	return map[string]json.RawMessage{"contract_version": mustRawString("v1"), "plugin_version": mustRawString("0.1.8"), "cpa_version": mustRawString("v8.0.32")}
}

func TestPluginModelCatalogSynchronizesRegisteredAuthIDs(t *testing.T) {
	fixture, cfg := newCatalogHTTPFixture(t)
	a := &App{}
	beforeConfig, _ := json.Marshal(fixture.config)
	beforePolicy := mapToRawObject(fixture.policy)
	result, err := a.syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	requests, body := fixture.recorded()
	if len(result.Snapshot.Credentials) != 5 || result.Snapshot.Revision != 1 {
		t.Fatalf("catalog size/revision = %d/%d", len(result.Snapshot.Credentials), result.Snapshot.Revision)
	}
	expectedIDs := []string{catalogPrimaryID, catalogDuplicateID, catalogSecondaryID, catalogKeylessID, catalogOAuthID}
	gotIDs := []string{}
	putIndex, lastModelIndex, healthIndex := -1, -1, -1
	for index, request := range requests {
		if request.path == "/v8/management/credentials/models" {
			gotIDs = append(gotIDs, request.authID)
			lastModelIndex = index
		}
		if request.method == http.MethodPut {
			if request.path != cpaHelperPluginManagementPrefix+"/model-catalog" {
				t.Fatalf("synchronizing catalog wrote another resource: %s", request.path)
			}
			putIndex = index
			if request.header.Get("If-Match") != `"0"` || request.header.Get("Idempotency-Key") == "" {
				t.Fatal("first catalog write lacks its own CAS/idempotency transaction")
			}
		}
		if request.path == cpaHelperPluginManagementPrefix+"/health" {
			healthIndex = index
		}
	}
	sort.Strings(gotIDs)
	sort.Strings(expectedIDs)
	if !reflect.DeepEqual(gotIDs, expectedIDs) || putIndex != lastModelIndex+3 || healthIndex != putIndex+1 || requests[len(requests)-1].path != cpaHelperPluginManagementPrefix+"/model-catalog" {
		t.Fatalf("catalog queries/transaction order invalid: IDs=%#v put=%d model=%d health=%d", gotIDs, putIndex, lastModelIndex, healthIndex)
	}
	byRef := map[string]pluginModelCatalogCredential{}
	for _, credential := range result.Snapshot.Credentials {
		byRef[credential.Ref] = credential
	}
	for _, id := range expectedIDs {
		if _, exists := byRef[catalogFixtureRef(id)]; !exists {
			t.Fatal("catalog ref is not the hash of the queried scheduler ID")
		}
	}
	primary := byRef[catalogFixtureRef(catalogPrimaryID)]
	if primary.Enabled || primary.Source != "ai-providers" || primary.Provider != "codex" || !reflect.DeepEqual(primary.Models, []string{"TEAM/public-alias"}) {
		t.Fatalf("runtime zero weight, normalization or public IDs lost: %#v", primary)
	}
	oauth := byRef[catalogFixtureRef(catalogOAuthID)]
	if !oauth.Enabled || oauth.Source != "auth-files" || oauth.Provider != "claude" {
		t.Fatalf("transient cooldown changed enabled state: %#v", oauth)
	}
	for _, secret := range append(expectedIDs, "catalog-management-secret", "catalog-primary-secret-1234", "catalog-secondary-secret-5678", "runtime-secret", "Old runtime name", "Display-A", "unregistered-alias", "upstream-private-model", "untrusted-health-secret") {
		if strings.Contains(string(body), secret) {
			t.Fatalf("catalog includes a secret, host ID or display/config field: %q", secret)
		}
	}
	afterConfig, _ := json.Marshal(fixture.config)
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforePolicy, mapToRawObject(fixture.policy)) {
		t.Fatal("catalog sync changed host settings or policy revision/rules")
	}
}

func (fixture *catalogHTTPFixture) seedCatalog(revision uint64) {
	fixture.revision = revision
	fixture.catalog, _ = json.Marshal(map[string]any{
		"contract_version": "v1", "plugin_version": "0.1.8", "cpa_version": "v8.0.32",
		"catalog_revision": revision, "generated_at": "2026-10-18T00:00:00Z", "credentials": []any{},
	})
}

func TestPluginModelCatalogFailuresDoNotPublishPartialSuccess(t *testing.T) {
	cases := []struct {
		name       string
		configure  func(*catalogHTTPFixture)
		code       string
		status     int
		putCount   int
		keepsPrior bool
	}{
		{"model authentication", func(f *catalogHTTPFixture) { f.modelStatus[catalogSecondaryID] = 401 }, "plugin_auth_failed", 502, 0, true},
		{"model forbidden", func(f *catalogHTTPFixture) { f.modelStatus[catalogSecondaryID] = 403 }, "plugin_auth_failed", 502, 0, true},
		{"model network service failure", func(f *catalogHTTPFixture) { f.modelStatus[catalogSecondaryID] = 503 }, "plugin_unavailable", 503, 0, true},
		{"missing v8 api-keys configuration", func(f *catalogHTTPFixture) { delete(f.config, "api-keys") }, "validation_error", 422, 0, true},
		{"null v8 api-keys configuration", func(f *catalogHTTPFixture) { f.config["api-keys"] = nil }, "validation_error", 422, 0, true},
		{"non JSON models", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte("catalog-management-secret") }, "model_catalog_sync_failed", 503, 0, true},
		{"null models", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{"models":null}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"missing models", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"non array models", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{"models":{}}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"null model entry", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{"models":[null]}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"non string ID", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{"models":[{"id":1}]}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"padded public ID", func(f *catalogHTTPFixture) { f.models[catalogSecondaryID] = []byte(`{"models":[{"id":" public "}]}`) }, "model_catalog_sync_failed", 503, 0, true},
		{"control in public ID", func(f *catalogHTTPFixture) {
			f.models[catalogSecondaryID] = []byte(`{"models":[{"id":"public\nsecret"}]}`)
		}, "model_catalog_sync_failed", 503, 0, true},
		{"semantic config drift", func(f *catalogHTTPFixture) {
			f.onSecondConfig = func(f *catalogHTTPFixture) { f.config["future_integer"] = json.Number("9007199254740994") }
		}, "model_catalog_out_of_sync", 409, 0, true},
		{"model config drift without ID change", func(f *catalogHTTPFixture) {
			f.onSecondConfig = func(f *catalogHTTPFixture) { f.config["model-prefix"] = "new-prefix" }
		}, "model_catalog_out_of_sync", 409, 0, true},
		{"runtime identity drift", func(f *catalogHTTPFixture) {
			f.onSecondCredentials = func(f *catalogHTTPFixture) { f.files[1]["id"] = "another-oauth-file" }
		}, "model_catalog_out_of_sync", 409, 0, true},
		{"runtime enabled drift", func(f *catalogHTTPFixture) {
			f.onSecondCredentials = func(f *catalogHTTPFixture) { f.files[1]["weight"] = 0 }
		}, "model_catalog_out_of_sync", 409, 0, true},
		{"CAS conflict", func(f *catalogHTTPFixture) { f.writeStatus = 409 }, "conflict", 409, 1, true},
		{"persistence failure", func(f *catalogHTTPFixture) { f.writeStatus = 500 }, "plugin_unavailable", 503, 1, true},
		{"old idempotency receipt", func(f *catalogHTTPFixture) { value := uint64(7); f.acceptedRevision = &value }, "model_catalog_sync_failed", 503, 1, false},
		{"stale health revision", func(f *catalogHTTPFixture) { value := uint64(7); f.healthRevision = &value }, "model_catalog_sync_failed", 503, 1, false},
		{"unavailable health", func(f *catalogHTTPFixture) { f.healthStatus = "unavailable" }, "model_catalog_sync_failed", 503, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, cfg := newCatalogHTTPFixture(t)
			fixture.seedCatalog(7)
			before := append([]byte(nil), fixture.catalog...)
			tc.configure(fixture)
			_, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities())
			var problem *AppError
			if !errors.As(err, &problem) || problem.Code != tc.code || problem.Status != tc.status {
				t.Fatalf("error = %v; want %d/%s", err, tc.status, tc.code)
			}
			if strings.Contains(problem.Message, "catalog-management-secret") || strings.Contains(problem.Message, "untrusted") {
				t.Fatal("upstream catalog error text escaped the safe boundary")
			}
			requests, after := fixture.recorded()
			puts := 0
			for _, request := range requests {
				if request.method == http.MethodPut {
					puts++
					if request.path != cpaHelperPluginManagementPrefix+"/model-catalog" || request.header.Get("If-Match") != `"7"` {
						t.Fatal("failed sync wrote policy/config or lost catalog CAS")
					}
				}
			}
			if puts != tc.putCount || (tc.keepsPrior && !bytes.Equal(before, after)) {
				t.Fatalf("failed catalog sync published a partial state: PUTs=%d (want %d), prior preserved=%v", puts, tc.putCount, bytes.Equal(before, after))
			}
		})
	}
}

func TestPluginModelCatalogRejectsMalformedCurrentSnapshots(t *testing.T) {
	validCredential := `{"ref":"` + catalogFixtureRef(catalogSecondaryID) + `","source":"ai-providers","provider":"codex","enabled":true,"models":["public"]}`
	valid := `{"contract_version":"v1","plugin_version":"0.1.8","cpa_version":"v8.0.32","catalog_revision":7,"generated_at":"2026-10-18T00:00:00Z","credentials":[` + validCredential + `]}`
	cases := map[string]string{
		"missing required array":   strings.Replace(valid, `,"credentials":[`+validCredential+`]`, "", 1),
		"null required array":      strings.Replace(valid, `[`+validCredential+`]`, "null", 1),
		"case alias array":         strings.Replace(valid, `"credentials"`, `"Credentials"`, 1),
		"unknown snapshot field":   strings.TrimSuffix(valid, "}") + `,"label":"secret"}`,
		"duplicate snapshot field": strings.TrimSuffix(valid, "}") + `,"credentials":[]}`,
		"null credential":          strings.Replace(valid, validCredential, "null", 1),
		"duplicate credential":     strings.Replace(valid, validCredential, validCredential+","+validCredential, 1),
		"missing enabled":          strings.Replace(valid, `,"enabled":true`, "", 1),
		"null enabled":             strings.Replace(valid, `"enabled":true`, `"enabled":null`, 1),
		"duplicate enabled":        strings.Replace(valid, `"enabled":true`, `"enabled":true,"enabled":false`, 1),
		"credential case alias":    strings.Replace(valid, `"enabled":true`, `"Enabled":true`, 1),
		"credential display field": strings.Replace(valid, `"enabled":true`, `"enabled":true,"label":"secret"`, 1),
		"null model ID":            strings.Replace(valid, `["public"]`, `[null]`, 1),
		"duplicate public ID":      strings.Replace(valid, `["public"]`, `["public","PUBLIC"]`, 1),
		"old catalog producer":     strings.Replace(valid, `"0.1.8"`, `"0.1.7"`, 1),
		"catalog zero revision":    strings.Replace(valid, `"catalog_revision":7`, `"catalog_revision":0`, 1),
	}
	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			fixture, cfg := newCatalogHTTPFixture(t)
			fixture.seedCatalog(7)
			fixture.stateResponse = []byte(`{"snapshot":` + snapshot + `,"health":{"catalog_revision":7,"status":"ok"}}`)
			_, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities())
			var problem *AppError
			if !errors.As(err, &problem) || problem.Code != "plugin_incompatible" {
				t.Fatalf("malformed catalog accepted: %v", err)
			}
			requests, _ := fixture.recorded()
			for _, request := range requests {
				if request.method == http.MethodPut || request.path == "/v8/management/credentials/models" {
					t.Fatal("malformed current catalog reached collection/publication")
				}
			}
		})
	}
}

func TestPluginModelCatalogEmptyAndTransientSnapshots(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			fixture, cfg := newCatalogHTTPFixture(t)
			if empty {
				fixture.config["api-keys"] = map[string]any{}
				fixture.files = []map[string]any{}
			} else {
				fixture.models[catalogSecondaryID] = []byte(`{"models":[]}`)
				fixture.onSecondCredentials = func(f *catalogHTTPFixture) {
					f.files[1]["status"] = "error"
					f.files[1]["observed_at"] = "a-later-observation"
					f.files[1]["cooldown_until"] = "a-later-cooldown"
				}
			}
			result, err := (&App{}).syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities())
			if err != nil || result.Snapshot.Credentials == nil {
				t.Fatalf("legal empty/transient catalog failed: %v", err)
			}
			if empty && len(result.Snapshot.Credentials) != 0 {
				t.Fatal("empty host config published credentials")
			}
			for _, credential := range result.Snapshot.Credentials {
				if credential.Ref == catalogFixtureRef(catalogSecondaryID) && (credential.Models == nil || len(credential.Models) != 0) {
					t.Fatal("valid empty registered model array was not preserved")
				}
			}
		})
	}
}

func TestPluginV8HostCredentialsKeepsConfiguredIdentityWhenRuntimeMetadataIsOmitted(t *testing.T) {
	const apiKey = "configured-identity-secret"
	config := map[string]any{
		"api-keys": map[string]any{
			"codex": []any{map[string]any{
				"base-url": "https://codex.invalid",
				"keys":     []any{map[string]any{"api-key": apiKey, "auth_index": "configured-index"}},
			}},
		},
	}
	configuredID := pluginConfiguredCredentialID("codex:apikey", []string{apiKey, "https://codex.invalid", "", "", ""}, map[string]int{})
	credentials, err := pluginV8HostCredentials(config, []map[string]any{{
		"id": configuredID, "auth_index": "configured-index",
		// CPA may sanitize runtime metadata while retaining the identity/index.
		"runtime_only": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 {
		t.Fatalf("credentials = %#v, want one configured credential", credentials)
	}
	credential := credentials[0]
	if credential.Provider != "codex" || credential.Source != "ai-providers" || credential.Status != "active" {
		t.Fatalf("runtime omission erased configured identity: %#v", credential)
	}
	if credential.Label == "" || strings.Contains(credential.Label, apiKey) {
		t.Fatalf("configured label was not retained safely: %#v", credential)
	}
}

func TestPluginModelCatalogCoalescesInFlightAndRefreshesLater(t *testing.T) {
	fixture, cfg := newCatalogHTTPFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	fixture.modelStarted, fixture.modelRelease = started, release
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	a := &App{}
	completed := make(chan error, 1)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	go func() {
		_, err := a.syncPluginModelCatalog(leaderCtx, cfg, catalogCapabilities())
		completed <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog model request did not start")
	}
	cancelLeader()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("leader cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader did not observe its own cancellation")
	}
	go func() {
		_, err := a.syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities())
		completed <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.syncPluginModelCatalog(ctx, cfg, catalogCapabilities()); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting caller did not join the in-flight transaction: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog transaction did not finish")
	}
	if _, err := a.syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities()); err != nil {
		t.Fatal(err)
	}
	requests, _ := fixture.recorded()
	keys := []string{}
	for _, request := range requests {
		if request.method == http.MethodPut {
			keys = append(keys, request.header.Get("Idempotency-Key"))
			if request.header.Get("If-Match") != fmt.Sprintf("\"%d\"", len(keys)-1) {
				t.Fatal("refresh reused an older catalog revision")
			}
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("in-flight caller duplicated a write, or refresh reused its idempotency key: %#v", keys)
	}
}

func newCatalogRouteApp(t *testing.T, cfg AppConfig) (*App, http.Handler, []*http.Cookie) {
	t.Helper()
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	a, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	handler := a.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "catalog-admin", "password": "catalog-password", "nickname": "Catalog"}, nil, nil)
	local, err := a.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	local.Collector.CLIProxyURL, local.Collector.ManagementKey = cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey
	if err := a.saveConfig(context.Background(), local); err != nil {
		t.Fatal(err)
	}
	return a, handler, cookies
}

func catalogRouteResponse(t *testing.T, handler http.Handler, method, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, reader)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestPluginModelDirectoryRejectsExpiredKeyBeforeUpstreamIO(t *testing.T) {
	fixture, cfg := newCatalogHTTPFixture(t)
	a, handler, admin := newCatalogRouteApp(t, cfg)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "expired-catalog-member", "password": "member-password", "nickname": "Member", "is_admin": false}, admin, nil)
	member := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "expired-catalog-member", "password": "member-password"}, nil, nil)
	member = latestCookiesByName(requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, member, nil))

	var key UserApiKeySummary
	expiresAt := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "expired catalog key", "expires_at": expiresAt}, member, &key)
	if _, err := a.db.Exec(`UPDATE user_api_keys SET expires_at = ? WHERE api_key_hash = ?`, dbTime(time.Now().Add(-time.Minute)), key.APIKeyHash); err != nil {
		t.Fatal(err)
	}

	before, _ := fixture.recorded()
	response := catalogRouteResponse(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+key.APIKeyHash, nil, member)
	after, _ := fixture.recorded()
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"conflict"`) || !strings.Contains(response.Body.String(), "API KEY 已过期，请先修改密钥有效期") {
		t.Fatalf("expired Key directory response = %d %s; want a translatable conflict", response.Code, response.Body.String())
	}
	if len(after) != len(before) {
		t.Fatalf("expired Key directory lookup made upstream requests: before=%d after=%d", len(before), len(after))
	}
}

func TestPluginModelCatalogRecoveryKeepsUserScopeAndGlobalRegistry(t *testing.T) {
	for _, code := range []string{"model_catalog_unavailable", "model_catalog_out_of_sync"} {
		t.Run(code, func(t *testing.T) {
			fixture, cfg := newCatalogHTTPFixture(t)
			a, handler, admin := newCatalogRouteApp(t, cfg)
			var adminKey, memberKey UserApiKeySummary
			requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "admin-catalog-key"}, admin, &adminKey)
			requestJSONForPricingTest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "catalog-member", "password": "member-password", "nickname": "Member", "is_admin": false}, admin, nil)
			member := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "catalog-member", "password": "member-password"}, nil, nil)
			member = latestCookiesByName(requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, member, nil))
			requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "member-catalog-key"}, member, &memberKey)
			fixture.mu.Lock()
			fixture.policy["groups"] = mustRaw([]any{map[string]any{"id": "secondary-only", "name": "Secondary", "rule": map[string]any{"credential_ids": []string{catalogFixtureRef(catalogSecondaryID), catalogFixtureRef(catalogDuplicateID)}, "denied_credential_ids": []string{catalogFixtureRef(catalogDuplicateID)}}}})
			fixture.policy["keys"] = mustRaw([]any{map[string]any{"id": pluginCallerScope(*memberKey.APIKey), "enabled": true, "group_ids": []string{"secondary-only"}, "rule": map[string]any{"models": []string{"secondary-public", "duplicate-public"}}}})
			fixture.needsSync[*memberKey.APIKey] = code
			fixture.mu.Unlock()
			var models AvailableModelsResponse
			requestJSONForPricingTest(t, handler, http.MethodGet, "/api/account/models", nil, member, &models)
			if len(models.Models) != 1 || models.Models[0].ID != "secondary-public" || len(models.Errors) != 0 || models.ModelListFilterStatus != "enabled" || len(models.Models[0].Sources) != 1 || models.Models[0].Sources[0].APIKeyHash != memberKey.APIKeyHash {
				t.Fatalf("recovered model list broadened the user's rule/Key scope: %#v", models)
			}
			beforeGlobal, _ := fixture.recorded()
			modelReads, catalogWrites := 0, 0
			for _, request := range beforeGlobal {
				if request.path == "/v1/models" {
					modelReads++
					if request.header.Get("Authorization") != "Bearer "+*memberKey.APIKey {
						t.Fatal("model recovery retried with a different user's Key")
					}
				}
				if request.path == cpaHelperPluginManagementPrefix+"/model-catalog" && request.method == http.MethodPut {
					catalogWrites++
				}
			}
			if modelReads != 2 || catalogWrites != 1 {
				t.Fatalf("model recovery was not one sync and one retry: reads=%d writes=%d", modelReads, catalogWrites)
			}
			var directory struct {
				Models      []string         `json:"models"`
				Credentials []map[string]any `json:"credentials"`
			}
			requestJSONForPricingTest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory", nil, admin, &directory)
			want := []string{"TEAM/public-alias", "duplicate-public", "keyless-public", "oauth-public", "secondary-public"}
			if !reflect.DeepEqual(directory.Models, want) {
				t.Fatalf("global editor used a restricted Key instead of public registry union: %#v", directory.Models)
			}
			afterGlobal, priorCatalog := fixture.recorded()
			for _, request := range afterGlobal[len(beforeGlobal):] {
				if request.path == "/v1/models" {
					t.Fatal("global editor queried a downstream Key to build the catalog")
				}
			}
			requestJSONForPricingTest(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+memberKey.APIKeyHash, nil, member, &directory)
			if !reflect.DeepEqual(directory.Models, want) {
				t.Fatalf("per-Key editor lost candidates outside its current rule: %#v", directory.Models)
			}
			afterKeyDirectory, _ := fixture.recorded()
			for _, request := range afterKeyDirectory[len(afterGlobal):] {
				if request.path == "/v1/models" {
					t.Fatal("per-Key editor used the caller-filtered downstream model list")
				}
			}
			requestJSONForPricingTest(t, handler, http.MethodGet, "/api/account/models", nil, member, &models)
			if len(models.Models) != 1 || models.Models[0].ID != "secondary-public" {
				t.Fatalf("editor candidates changed account model permissions: %#v", models.Models)
			}
			for _, endpoint := range []struct{ method, prefix string }{
				{http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="},
				{http.MethodGet, "/api/cpa-helper-plugin/policy/key/"},
				{http.MethodPut, "/api/cpa-helper-plugin/policy/key/"},
			} {
				before, _ := fixture.recorded()
				payload := map[string]any{"policy_revision": 44, "key": map[string]any{"id": "not-the-caller-scope", "enabled": false}}
				missing := catalogRouteResponse(t, handler, endpoint.method, endpoint.prefix+strings.Repeat("0", 64), payload, member)
				foreign := catalogRouteResponse(t, handler, endpoint.method, endpoint.prefix+adminKey.APIKeyHash, payload, member)
				after, _ := fixture.recorded()
				if missing.Code != 404 || foreign.Code != 404 || missing.Body.String() != foreign.Body.String() || !strings.Contains(missing.Body.String(), `"message":"API KEY 不存在"`) || len(before) != len(after) {
					t.Fatal("missing/foreign Key lookup leaked existence or performed catalog IO")
				}
			}
			before, _ := fixture.recorded()
			requestJSONForPricingTestExpectStatus(t, handler, http.MethodGet, "/api/cpa-helper-plugin/policy", nil, member, http.StatusForbidden)
			after, _ := fixture.recorded()
			if len(before) != len(after) {
				t.Fatal("ordinary user global-policy rejection reached CPA")
			}
			fixture.mu.Lock()
			fixture.modelStatus[catalogSecondaryID] = http.StatusServiceUnavailable
			priorCatalog = append([]byte(nil), fixture.catalog...)
			fixture.mu.Unlock()
			failed := catalogRouteResponse(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+memberKey.APIKeyHash, nil, member)
			_, afterCatalog := fixture.recorded()
			if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), `"code":"plugin_unavailable"`) || !bytes.Equal(priorCatalog, afterCatalog) || strings.Contains(failed.Body.String(), "catalog-management-secret") {
				t.Fatalf("directory collection failure was hidden or published: %d %s", failed.Code, failed.Body.String())
			}
			if _, err := a.db.Exec(`DROP TABLE user_api_keys`); err != nil {
				t.Fatal(err)
			}
			before, _ = fixture.recorded()
			failed = catalogRouteResponse(t, handler, http.MethodGet, "/api/cpa-helper-plugin/directory?api_key_hash="+memberKey.APIKeyHash, nil, member)
			after, _ = fixture.recorded()
			if failed.Code != http.StatusInternalServerError || len(before) != len(after) {
				t.Fatal("database lookup failure became a missing-Key response or triggered catalog IO")
			}
		})
	}
}

func TestPluginModelCatalogRecoveryPreservesPartialAndAllKeyFailures(t *testing.T) {
	fixture, cfg := newCatalogHTTPFixture(t)
	a, handler, admin := newCatalogRouteApp(t, cfg)
	var first, second UserApiKeySummary
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "first-recovery-key"}, admin, &first)
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "second-recovery-key"}, admin, &second)
	if _, err := a.syncPluginModelCatalog(context.Background(), cfg, catalogCapabilities()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.afterSyncError[*first.APIKey] = "model_catalog_unavailable"
	fixture.mu.Unlock()
	var partial AvailableModelsResponse
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/account/models", nil, admin, &partial)
	if len(partial.Models) == 0 || len(partial.Errors) != 1 || partial.Errors[0].APIKeyHash != first.APIKeyHash || partial.Errors[0].Message != "CPA Helper Plugin 模型目录不可用，请同步目录" {
		t.Fatalf("partial Key failure contract changed: %#v", partial)
	}
	fixture.mu.Lock()
	fixture.afterSyncError[*second.APIKey] = "model_catalog_out_of_sync"
	fixture.mu.Unlock()
	before, _ := fixture.recorded()
	failed := catalogRouteResponse(t, handler, http.MethodGet, "/api/account/models", nil, admin)
	after, _ := fixture.recorded()
	puts, modelReads := 0, 0
	for _, request := range after[len(before):] {
		if request.method == http.MethodPut {
			puts++
		}
		if request.path == "/v1/models" {
			modelReads++
		}
	}
	if failed.Code != http.StatusUnprocessableEntity || !strings.Contains(failed.Body.String(), `"code":"validation_error"`) || puts != 1 || modelReads != 3 {
		t.Fatalf("all Key failure became empty success or repeated recovery: status=%d PUTs=%d reads=%d body=%s", failed.Code, puts, modelReads, failed.Body.String())
	}
	for _, secret := range []string{*first.APIKey, *second.APIKey, "catalog-management-secret"} {
		if strings.Contains(failed.Body.String(), secret) {
			t.Fatal("model-list plugin error exposed raw secrets")
		}
	}
}

func TestPluginModelCatalogRecoveryWithoutManagementOrOnOldRelease(t *testing.T) {
	for _, scenario := range []string{"missing management", "old plugin", "unknown model error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, cfg := newCatalogHTTPFixture(t)
			a, handler, admin := newCatalogRouteApp(t, cfg)
			var key UserApiKeySummary
			requestJSONForPricingTest(t, handler, http.MethodPost, "/api/api-keys", map[string]any{"description": "recovery-boundary-key"}, admin, &key)
			fixture.mu.Lock()
			fixture.afterSyncError[*key.APIKey] = "model_catalog_unavailable"
			if scenario == "old plugin" {
				fixture.version, fixture.cpaTarget = "0.1.7", "v8.0.29"
			}
			if scenario == "unknown model error" {
				fixture.afterSyncError[*key.APIKey] = "untrusted-code-catalog-management-secret"
			}
			fixture.mu.Unlock()
			if scenario == "missing management" {
				local, err := a.loadConfig(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				local.Collector.ManagementKey = ""
				if err := a.saveConfig(context.Background(), local); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := fixture.recorded()
			failed := catalogRouteResponse(t, handler, http.MethodGet, "/api/account/models", nil, admin)
			after, _ := fixture.recorded()
			if failed.Code != http.StatusUnprocessableEntity || strings.Contains(failed.Body.String(), "untrusted-code") || strings.Contains(failed.Body.String(), "catalog-management-secret") {
				t.Fatalf("unsupported recovery boundary did not fail safely: %d %s", failed.Code, failed.Body.String())
			}
			for _, request := range after[len(before):] {
				if request.path == cpaHelperPluginManagementPrefix+"/model-catalog" || strings.HasPrefix(request.path, "/v8/") || (scenario == "missing management" && request.path != "/v1/models") {
					t.Fatal("recovery crossed an unsupported/missing management boundary")
				}
			}
			if scenario == "missing management" && !strings.Contains(failed.Body.String(), "模型目录需要管理员同步") {
				t.Fatal("missing management catalog error did not explain administrator synchronization")
			}
		})
	}
}
