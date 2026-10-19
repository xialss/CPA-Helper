package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	pluginPrefix      = "/v0/management/plugins/cpa-helper-plugin/v1"
	responseModelPath = "/v8/management/config/plugins/configs/cpa-helper-plugin/response_model_mismatch"
)

type transactionReceipt struct {
	Digest   string
	Revision uint64
}

func (s *fakeCPA) servePlugin(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimPrefix(r.URL.Path, pluginPrefix)
	switch resource {
	case "/capabilities":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, map[string]any{
			"plugin_id": "cpa-helper-plugin", "plugin_version": "0.1.8", "contract_version": "v1",
			"abi_version": 1, "schema_version": 6, "cpa_version": "v8.0.32",
			"model_list_filter_enabled": true, "response_model_mismatch": s.responseModel,
			"modules":       []string{"model_rules", "model_list_filter", "response_model_mismatch", "credential_routes", "concurrency"},
			"policy_fields": []string{"groups[].id", "groups[].name", "groups[].note", "groups[].rule", "keys[].id", "keys[].label", "keys[].enabled", "keys[].group_ids", "keys[].max_concurrency", "keys[].rule"},
			"unknown_key":   "allow", "concurrency_scope": "instance", "billing": false,
		})
	case "/health":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "policy_revision": s.policy.Revision, "contract_version": "v1", "plugin_version": "0.1.8",
			"generated_at": s.policy.GeneratedAt, "accepted_at": s.policyAccepted,
			"active": map[string]int{}, "response_model_tracked_requests": 0, "model_catalog": s.catalogHealth(),
		})
	case "/policy":
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(s.policy.Revision, 10)))
			fakeJSON(w, http.StatusOK, s.policy)
		case http.MethodPut:
			s.applySnapshot(w, r, false)
		default:
			fakeMethodNotAllowed(w, "GET, PUT")
		}
	case "/model-catalog":
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(s.catalogRevision(), 10)))
			fakeJSON(w, http.StatusOK, map[string]any{"snapshot": s.catalog, "health": s.catalogHealth()})
		case http.MethodPut:
			s.applySnapshot(w, r, true)
		default:
			fakeMethodNotAllowed(w, "GET, PUT")
		}
	case "/directory":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		items := []map[string]any{}
		for _, credential := range s.credentials() {
			status := "active"
			if !credential.Enabled {
				status = "disabled"
			}
			items = append(items, map[string]any{
				"ref": credential.Ref, "source": credential.Source, "provider": credential.Provider,
				"auth_index": credential.AuthIndex, "label": credential.Label, "status": status,
			})
		}
		fakeJSON(w, http.StatusOK, map[string]any{"credentials": items})
	case "/response-model/validate":
		if r.Method != http.MethodPost {
			fakeMethodNotAllowed(w, "POST")
			return
		}
		config := defaultResponseModelConfig()
		if err := decodeFakeBody(r, &config); err != nil || config.validate() != nil {
			fakeProblem(w, http.StatusBadRequest, "invalid_response_model_config", "Invalid response-model configuration")
			return
		}
		config.normalize()
		fakeJSON(w, http.StatusOK, config)
	default:
		fakeProblem(w, http.StatusNotFound, "not_found", "This plugin operation is not implemented by the acceptance fixture")
	}
}

func (s *fakeCPA) applySnapshot(w http.ResponseWriter, r *http.Request, catalog bool) {
	base, err := strconv.ParseUint(strings.Trim(r.Header.Get("If-Match"), "\""), 10, 64)
	if err != nil || (!catalog && base == 0) {
		fakeProblem(w, http.StatusPreconditionRequired, "precondition_required", "If-Match must contain the current resource revision")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		fakeProblem(w, http.StatusBadRequest, "invalid", "Idempotency-Key is required")
		return
	}
	current, field, receipts := s.policy.Revision, "policy_revision", s.policyReceipts
	var revision uint64
	var candidate any
	if catalog {
		var next modelCatalog
		if err := decodeFakeBody(r, &next); err != nil || next.validate() != nil {
			s.catalogError = "model catalog request was rejected"
			fakeProblem(w, http.StatusBadRequest, "invalid_model_catalog", "Expected a complete valid model catalog")
			return
		}
		current, field, receipts = s.catalogRevision(), "catalog_revision", s.catalogReceipts
		revision, candidate = next.Revision, next
	} else {
		var next policySnapshot
		if err := decodeFakeBody(r, &next); err != nil || next.validate() != nil {
			fakeProblem(w, http.StatusBadRequest, "invalid_policy", "Expected a complete valid policy snapshot")
			return
		}
		revision, candidate = next.Revision, next
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		fakeProblem(w, http.StatusInternalServerError, "invalid", "Cannot encode the accepted snapshot")
		return
	}
	digest := hexSHA256(field + ":" + strconv.FormatUint(base, 10) + ":" + string(raw))
	if receipt, exists := receipts[key]; exists {
		if receipt.Digest != digest {
			fakeProblem(w, http.StatusConflict, "revision_conflict", "The idempotency key was already used for a different transaction")
			return
		}
		fakeJSON(w, http.StatusOK, map[string]uint64{field: receipt.Revision})
		return
	}
	if base != current || revision != base+1 {
		fakeProblem(w, http.StatusConflict, "revision_conflict", "The resource revision has changed")
		return
	}
	if catalog {
		next := candidate.(modelCatalog)
		s.catalog = &next
		s.catalogAccepted, s.catalogError = time.Now().UTC(), ""
	} else {
		s.policy = candidate.(policySnapshot)
		s.policyAccepted = time.Now().UTC()
	}
	receipts[key] = transactionReceipt{Digest: digest, Revision: revision}
	fakeJSON(w, http.StatusOK, map[string]uint64{field: revision})
}

func (s *fakeCPA) catalogRevision() uint64 {
	if s.catalog == nil {
		return 0
	}
	return s.catalog.Revision
}

func (s *fakeCPA) catalogHealth() map[string]any {
	status, generatedAt, count := "unavailable", time.Time{}, 0
	if s.catalog != nil {
		status, generatedAt, count = "ok", s.catalog.GeneratedAt, len(s.catalog.Credentials)
	}
	age := int64(0)
	if !s.catalogAccepted.IsZero() {
		age = int64(time.Since(s.catalogAccepted).Seconds())
	}
	health := map[string]any{
		"status": status, "catalog_revision": s.catalogRevision(), "generated_at": generatedAt,
		"accepted_at": s.catalogAccepted, "catalog_age_seconds": age, "credential_count": count,
	}
	if s.catalogError != "" {
		health["last_error"] = s.catalogError
	}
	return health
}

func (s *fakeCPA) serveModels(w http.ResponseWriter, key string) {
	resolved := keyPolicy{Enabled: true}
	for _, configured := range s.policy.Keys {
		if configured.ID == callerScope(key) {
			resolved = configured
			for _, binding := range configured.GroupIDs {
				for _, group := range s.policy.Groups {
					if group.ID == binding {
						resolved.Rule = mergeRules(resolved.Rule, group.Rule)
					}
				}
			}
			break
		}
	}
	registered := map[string]map[string]any{}
	for _, credential := range s.credentials() {
		if !credential.Enabled {
			continue
		}
		for _, model := range credential.Models {
			registered[model] = map[string]any{"id": model, "object": "model", "owned_by": credential.Provider}
		}
	}
	models := make([]string, 0, len(registered))
	for model := range registered {
		models = append(models, model)
	}
	sort.Strings(models)
	items := []map[string]any{}
	rule := resolved.Rule
	for _, model := range models {
		if !resolved.Enabled || selectionContains(rule.DeniedModels, model) || (len(rule.Models) > 0 && !selectionContains(rule.Models, model)) {
			continue
		}
		if len(rule.CredentialIDs)+len(rule.DeniedCredentialIDs)+len(rule.CredentialProviders)+len(rule.DeniedCredentialProviders) > 0 {
			if s.catalog == nil {
				w.Header().Set("X-Cpa-Helper-Error", "model_catalog_unavailable")
				fakeProblem(w, http.StatusOK, "model_catalog_unavailable", "Synchronize the credential model catalog before reading restricted models")
				return
			}
			known, allowed := false, false
			for _, credential := range s.catalog.Credentials {
				if selectionContains(credential.Models, model) {
					known = true
					if credential.Enabled && rule.allowsCredential(credential) {
						allowed = true
					}
				}
			}
			if !known {
				w.Header().Set("X-Cpa-Helper-Error", "model_catalog_out_of_sync")
				fakeProblem(w, http.StatusOK, "model_catalog_out_of_sync", "The saved credential catalog does not cover the current registered models")
				return
			}
			if !allowed {
				continue
			}
		}
		items = append(items, registered[model])
	}
	fakeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}
