package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAvailableModelsWithoutManagementKey(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	const apiKey = "sk-model-catalog-regression-key"
	var mu sync.Mutex
	queries, managementRequests := 0, 0
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/v1/models" {
			managementRequests++
			http.Error(w, "management access is not configured", http.StatusUnauthorized)
			return
		}
		queries++
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "invalid model query key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "available-model"}}})
	}))
	defer cpa.Close()
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collector.CLIProxyURL, cfg.Collector.ManagementKey = cpa.URL, ""
	if err := a.saveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	now := dbTime(time.Now())
	result, err := a.db.ExecContext(ctx, `INSERT INTO users (username, nickname, created_at, updated_at) VALUES (?, ?, ?, ?)`, "member", "Member", now, now)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.upsertUserAPIKey(ctx, int(userID), hashAPIKey(apiKey), apiKey, "Model key"); err != nil {
		t.Fatal(err)
	}

	response, err := a.availableModelsForUser(ctx, int(userID))
	if err != nil {
		t.Fatal(err)
	}
	if response.ModelListFilterStatus != "not_configured" || len(response.Models) != 1 || response.Models[0].ID != "available-model" || len(response.Errors) != 0 {
		t.Fatalf("model catalog without management key = %#v", response)
	}
	mu.Lock()
	defer mu.Unlock()
	if !response.HasAPIKeys || response.APIKeyCount != 1 || response.QueryableAPIKeyCount != 1 || queries != 1 || managementRequests != 0 {
		t.Fatalf("query behavior: response=%#v, models=%d, management=%d", response, queries, managementRequests)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("model response exposed the user API key")
	}
}
