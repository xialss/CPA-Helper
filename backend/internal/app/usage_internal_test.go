package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestSaveUsageMessageStoresReasoningEffortAndTTFT(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-ttft","provider":"openai","model":"gpt-5.5","request_id":"usage-ttft","reasoning_effort":"xhigh","ttft_ms":710,"input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw))
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.ReasoningEffort == nil || *record.ReasoningEffort != "xhigh" {
		t.Fatalf("record reasoning_effort = %#v, want xhigh", record.ReasoningEffort)
	}
	if record.TTFTMS == nil || *record.TTFTMS != 710 {
		t.Fatalf("record ttft_ms = %#v, want 710", record.TTFTMS)
	}

	var reasoningEffort sql.NullString
	var ttftMS sql.NullFloat64
	if err := app.db.QueryRow(`SELECT reasoning_effort, ttft_ms FROM usage_records WHERE id = ?`, record.ID).Scan(&reasoningEffort, &ttftMS); err != nil {
		t.Fatal(err)
	}
	if !reasoningEffort.Valid || reasoningEffort.String != "xhigh" || !ttftMS.Valid || ttftMS.Float64 != 710 {
		t.Fatalf("stored reasoning/ttft = %#v/%#v, want xhigh/710", reasoningEffort, ttftMS)
	}
}

func TestSaveUsageMessageIgnoresZeroTTFT(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-ttft-zero","provider":"openai","model":"gpt-5.5","request_id":"usage-ttft-zero","ttft_ms":0,"input_tokens":10,"output_tokens":2}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw))
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.TTFTMS != nil {
		t.Fatalf("record ttft_ms = %#v, want nil", record.TTFTMS)
	}

	var ttftMS sql.NullFloat64
	if err := app.db.QueryRow(`SELECT ttft_ms FROM usage_records WHERE id = ?`, record.ID).Scan(&ttftMS); err != nil {
		t.Fatal(err)
	}
	if ttftMS.Valid {
		t.Fatalf("stored ttft_ms = %v, want NULL", ttftMS.Float64)
	}
}

func TestSaveUsageMessageExposesCodexCacheCostBreakdown(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	raw := `{"api_key":"sk-usage-cache","provider":"codex","model":"gpt-test","request_id":"usage-cache","tokens":{"input_tokens":100,"output_tokens":20,"cached_tokens":30,"cache_read_tokens":0,"cache_creation_tokens":40,"reasoning_tokens":5,"total_tokens":120}}`
	record, created, err := app.saveUsageMessage(context.Background(), []byte(raw))
	if err != nil || !created {
		t.Fatalf("saveUsageMessage created=%v err=%v", created, err)
	}
	if record.InputTokens != 100 || record.OutputTokens != 20 || record.CachedTokens != 30 || record.CacheReadTokens != 0 || record.CacheCreationTokens != 40 {
		t.Fatalf("stored tokens = input %d output %d cached %d read %d creation %d, want 100/20/30/0/40", record.InputTokens, record.OutputTokens, record.CachedTokens, record.CacheReadTokens, record.CacheCreationTokens)
	}

	prices := map[[2]string]ModelPrice{
		priceKey("openai", "gpt-test"): {
			Provider:               "openai",
			Model:                  "gpt-test",
			InputUSDPerMillion:     10,
			OutputUSDPerMillion:    20,
			CacheReadUSDPerMillion: 1,
		},
	}
	item := listItemFromRecord(record, map[string]userInfo{}, prices, usageRedactionOptions{})
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal usage item: %v", err)
	}
	var response struct {
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		Unpriced         bool    `json:"unpriced"`
		CostBreakdown    struct {
			BillingUnit         string `json:"billing_unit"`
			NormalInputTokens   int    `json:"normal_input_tokens"`
			CacheReadTokens     int    `json:"cache_read_tokens"`
			CacheCreationTokens int    `json:"cache_creation_tokens"`
			OutputTokens        int    `json:"output_tokens"`
			Items               []struct {
				Kind          string  `json:"kind"`
				Tokens        int     `json:"tokens"`
				USDPerMillion float64 `json:"usd_per_million"`
				SubtotalUSD   float64 `json:"subtotal_usd"`
			} `json:"items"`
			TotalUSD float64 `json:"total_usd"`
			Unpriced bool    `json:"unpriced"`
		} `json:"cost_breakdown"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("unmarshal usage item: %v", err)
	}
	if response.Unpriced || response.CostBreakdown.Unpriced || response.CostBreakdown.BillingUnit != modelBillingUnitToken {
		t.Fatalf("pricing state = top unpriced %v breakdown %#v", response.Unpriced, response.CostBreakdown)
	}
	if response.CostBreakdown.NormalInputTokens != 30 || response.CostBreakdown.CacheReadTokens != 30 || response.CostBreakdown.CacheCreationTokens != 40 || response.CostBreakdown.OutputTokens != 20 {
		t.Fatalf("API token breakdown = input %d read %d creation %d output %d, want 30/30/40/20", response.CostBreakdown.NormalInputTokens, response.CostBreakdown.CacheReadTokens, response.CostBreakdown.CacheCreationTokens, response.CostBreakdown.OutputTokens)
	}
	if len(response.CostBreakdown.Items) != 3 || response.CostBreakdown.Items[0].Kind != usageCostKindInput || response.CostBreakdown.Items[0].Tokens != 70 || response.CostBreakdown.TotalUSD != 0.00113 || response.EstimatedCostUSD != response.CostBreakdown.TotalUSD {
		t.Fatalf("API cost breakdown = items %#v total %v estimated %v, want three items with 70 input tokens and total 0.00113", response.CostBreakdown.Items, response.CostBreakdown.TotalUSD, response.EstimatedCostUSD)
	}
}
