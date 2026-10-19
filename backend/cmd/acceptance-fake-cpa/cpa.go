package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const acceptanceCallerKey = "sk-cpa-acceptance-unrestricted-0001cafe"

type fakeCPA struct {
	mu              sync.Mutex
	providers       map[string][]map[string]any
	downstreamKeys  []string
	policy          policySnapshot
	policyAccepted  time.Time
	policyReceipts  map[string]transactionReceipt
	catalog         *modelCatalog
	catalogAccepted time.Time
	catalogError    string
	catalogReceipts map[string]transactionReceipt
	responseModel   responseModelConfig
}

func newFakeCPA() *fakeCPA {
	now := time.Now().UTC()
	s := &fakeCPA{
		providers:      initialProviderConfig(),
		downstreamKeys: []string{acceptanceCallerKey},
		policy: policySnapshot{
			ContractVersion: "v1", PluginVersion: "0.1.8", Revision: 1,
			GeneratedAt: now, Groups: []ruleGroup{}, Keys: []keyPolicy{},
		},
		policyAccepted:  now,
		policyReceipts:  map[string]transactionReceipt{},
		catalogReceipts: map[string]transactionReceipt{},
		catalogError:    "model catalog has not been synchronized",
		responseModel:   defaultResponseModelConfig(),
	}
	s.fillAuthIndexes(nil)
	return s
}

func (s *fakeCPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer log.Printf("%s %s", r.Method, r.URL.Path)
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || strings.TrimSpace(token) == "" {
		fakeProblem(w, http.StatusUnauthorized, "unauthorized", "A synthetic Bearer token is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/v1/models" {
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		if !slices.Contains(s.downstreamKeys, strings.TrimSpace(token)) {
			fakeProblem(w, http.StatusUnauthorized, "invalid_api_key", "The API key is not registered in this acceptance fixture")
			return
		}
		s.serveModels(w, strings.TrimSpace(token))
		return
	}
	if strings.HasPrefix(r.URL.Path, pluginPrefix+"/") {
		s.servePlugin(w, r)
		return
	}
	if r.URL.Path == "/v0/management/api-keys" {
		s.serveAPIKeys(w, r)
		return
	}
	switch r.URL.Path {
	case "/v0/management/config":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		config := map[string]any{"api-keys": s.downstreamKeys}
		for key, entries := range s.providers {
			config[key] = entries
		}
		fakeJSON(w, http.StatusOK, config)
		return
	case "/v8/management/config":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, s.v8Config())
		return
	case "/v8/management/credentials", "/v0/management/auth-files":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, map[string]any{"files": s.runtimeCredentials()})
		return
	case "/v8/management/credentials/models":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			fakeProblem(w, http.StatusBadRequest, "invalid", "A scheduler auth ID or auth filename is required")
			return
		}
		items := []map[string]any{}
		for _, credential := range s.credentials() {
			if credential.AuthID == name {
				for _, model := range credential.Models {
					items = append(items, map[string]any{"id": model, "object": "model", "owned_by": credential.Provider})
				}
				break
			}
		}
		// The real CPA also returns an empty list for an unknown auth ID. An
		// auth_index therefore cannot be substituted for this query's name.
		fakeJSON(w, http.StatusOK, map[string]any{"models": items})
		return
	case responseModelPath:
		if r.Method != http.MethodPut {
			fakeMethodNotAllowed(w, "PUT")
			return
		}
		config := defaultResponseModelConfig()
		if err := decodeFakeBody(r, &config); err != nil || config.validate() != nil {
			fakeProblem(w, http.StatusBadRequest, "invalid_response_model_config", "Invalid response-model configuration")
			return
		}
		config.normalize()
		s.responseModel = config
		fakeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	case "/v0/management/api-key-usage":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, []map[string]any{})
		return
	case "/v0/management/usage-statistics-enabled":
		if r.Method != http.MethodGet {
			fakeMethodNotAllowed(w, "GET")
			return
		}
		fakeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	key, exists := pathToKey[r.URL.Path]
	if !exists {
		fakeProblem(w, http.StatusNotFound, "not_found", "This operation is not implemented by the acceptance fixture")
		return
	}
	switch r.Method {
	case http.MethodGet:
		fakeJSON(w, http.StatusOK, s.providers[key])
	case http.MethodPut:
		var entries []map[string]any
		if err := decodeFakeBody(r, &entries); err != nil || validateProviderEntries(key, entries) != nil {
			fakeProblem(w, http.StatusBadRequest, "invalid", "Expected a complete provider array with valid credential and model fields")
			return
		}
		previous := s.credentials()
		s.providers[key] = entries
		s.fillAuthIndexes(previous)
		fakeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		fakeMethodNotAllowed(w, "GET, PUT")
	}
}

func (s *fakeCPA) serveAPIKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		fakeJSON(w, http.StatusOK, map[string]any{"api-keys": s.downstreamKeys})
	case http.MethodPut:
		var keys []string
		if err := decodeFakeBody(r, &keys); err != nil || validateStringSelections(keys) != nil || keys == nil {
			fakeProblem(w, http.StatusBadRequest, "invalid", "Expected a complete API key array")
			return
		}
		s.downstreamKeys = keys
		fakeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	case http.MethodPatch:
		var change struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := decodeFakeBody(r, &change); err != nil || !validSelection(change.Old) || !validSelection(change.New) {
			fakeProblem(w, http.StatusBadRequest, "invalid", "Expected non-empty old and new API keys")
			return
		}
		keys := slices.Clone(s.downstreamKeys)
		if index := slices.Index(keys, change.Old); index >= 0 {
			keys[index] = change.New
		} else {
			keys = append(keys, change.New)
		}
		s.downstreamKeys = slices.Compact(keys)
		fakeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		fakeMethodNotAllowed(w, "GET, PUT, PATCH")
	}
}

func decodeFakeBody(r *http.Request, target any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if err := checkJSONValue(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func checkJSONValue(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var readValue func() error
	readValue = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				name, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := name.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON field")
				}
				seen[key] = true
				if err := readValue(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func fakeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "acceptance fixture response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(raw); err != nil {
		log.Printf("acceptance fixture response write failed: %v", err)
	}
}

func fakeProblem(w http.ResponseWriter, status int, code, message string) {
	fakeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func fakeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	fakeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "This method is not supported by the acceptance fixture")
}
