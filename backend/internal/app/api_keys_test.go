package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	backendApp "cpa-helper/backend/internal/app"
)

type apiKeyCreateResponse struct {
	APIKey     string `json:"api_key"`
	APIKeyHash string `json:"api_key_hash"`
}

func TestCreateAPIKeyUsesPatchAppendWhenRemoteListIsEmpty(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	remoteKeys := []string{}
	getCalls := 0
	putCalls := 0
	patchCalls := 0
	badPatchPayload := ""
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPatch:
			patchCalls++
			var payload struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Old == "" || payload.New == "" || payload.Old != payload.New {
				badPatchPayload = "old/new key must be the same non-empty value"
				http.Error(w, badPatchPayload, http.StatusBadRequest)
				return
			}
			replaced := false
			for index, key := range remoteKeys {
				if key == payload.Old {
					remoteKeys[index] = payload.New
					replaced = true
					break
				}
			}
			if !replaced {
				remoteKeys = append(remoteKeys, payload.New)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": remoteKeys})
		case http.MethodGet:
			getCalls++
			http.Error(w, "GET should not be needed to create the first API key", http.StatusInternalServerError)
		case http.MethodPut:
			putCalls++
			http.Error(w, "PUT should not be needed to create the first API key", http.StatusInternalServerError)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer cpa.Close()

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     cpa.URL,
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)

	created := apiKeyCreateResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/api-keys", map[string]any{
		"description": "VSCode",
	}, cookies, &created)

	if created.APIKey == "" || created.APIKeyHash == "" {
		t.Fatalf("created API key response is missing key fields: %#v", created)
	}
	if len(remoteKeys) != 1 || remoteKeys[0] != created.APIKey {
		t.Fatalf("remote keys = %#v, want the created key %#v", remoteKeys, created.APIKey)
	}
	if badPatchPayload != "" {
		t.Fatal(badPatchPayload)
	}
	if patchCalls != 1 || getCalls != 0 || putCalls != 0 {
		t.Fatalf("remote call counts patch/get/put = %d/%d/%d, want 1/0/0", patchCalls, getCalls, putCalls)
	}
}
