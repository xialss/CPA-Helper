package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUsageTTLMetadataStrictPartitionAndEvidence(t *testing.T) {
	a, _, _ := newCollectorTTLApp(t)
	f, cfg := newCollectorTTLFixture(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
	id := collectorTTLID(40)
	for _, test := range []struct {
		name, reason string
		mutate       func(*usageTTLMetadataResponse)
	}{
		{"version", "unsupported_metadata_version", func(r *usageTTLMetadataResponse) { r.MetadataVersion = "v2" }},
		{"retention", "invalid_metadata_response", func(r *usageTTLMetadataResponse) { r.RetentionHours = 12 }},
		{"null records", "invalid_metadata_response", func(r *usageTTLMetadataResponse) { r.Records = nil }},
		{"duplicate record", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records = append(r.Records, r.Records[0]) }},
		{"duplicate missing", "invalid_metadata_partition", func(r *usageTTLMetadataResponse) { r.Missing = []string{id} }},
		{"foreign record", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].ExecutionID = collectorTTLID(41) }},
		{"incomplete partition", "invalid_metadata_partition", func(r *usageTTLMetadataResponse) { r.Records = []usageTTLExecutionMetadata{} }},
		{"foreign missing", "invalid_metadata_partition", func(r *usageTTLMetadataResponse) { r.Missing = []string{collectorTTLID(42)} }},
		{"unknown source", "unsupported_metadata_source", func(r *usageTTLMetadataResponse) { r.Records[0].Source = "upstream" }},
		{"mixed not a policy", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].TTL = "mixed" }},
		{"invalid scope", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].CallerScope = "raw-api-key" }},
		{"zero revision", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].PolicyRevision = 0 }},
		{"unsafe revision", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].PolicyRevision = pluginModelCatalogMaxRev + 1 }},
		{"zero observed time", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].ObservedAt = time.Time{} }},
		{"past retention", "invalid_metadata_record", func(r *usageTTLMetadataResponse) { r.Records[0].ObservedAt = now.Add(-24 * time.Hour) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := usageTTLMetadataResponse{MetadataVersion: "v1", RetentionHours: 24, Records: []usageTTLExecutionMetadata{collectorTTLFact(id, "1h", now)}, Missing: []string{}}
			test.mutate(&response)
			f.mu.Lock()
			f.response = &response
			f.mu.Unlock()
			_, err := a.lookupUsageTTLMetadata(context.Background(), cfg, []string{id}, now)
			reason, terminal := usageTTLMetadataErrorReason(err)
			if err == nil || !terminal || reason != test.reason {
				t.Fatalf("strict metadata check = %s/%t/%v, want %s", reason, terminal, err, test.reason)
			}
		})
	}
	f.mu.Lock()
	f.response = &usageTTLMetadataResponse{MetadataVersion: "v1", RetentionHours: 24, Records: []usageTTLExecutionMetadata{collectorTTLFact(id, "1h", now.Add(time.Second))}, Missing: []string{}}
	f.mu.Unlock()
	if facts, err := a.lookupUsageTTLMetadata(context.Background(), cfg, []string{id}, now); err != nil || len(facts) != 1 {
		t.Fatalf("observer completed after lookup began: %v/%v", facts, err)
	}
	for _, ids := range [][]string{nil, {id, id}, {"request-id"}, {strings.ToUpper(id)}} {
		if _, err := a.lookupUsageTTLMetadata(context.Background(), cfg, ids, now); err == nil {
			t.Fatalf("invalid lookup accepted: %v", ids)
		}
	}
}

func TestUsageTTLMetadataRepairAndRawRetention(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	f, cfg := newCollectorTTLFixture(t)
	now, ctx := time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation), context.Background()
	id := collectorTTLID(50)
	raw := collectorTTLMessage(t, id, now, nil)
	f.facts[id] = collectorTTLFact(id, "1h", now)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil {
		t.Fatal(err)
	}
	record := collectorTTLRecord(t, a, raw)
	var factMetadata string
	if err := a.db.QueryRow(`SELECT cache_creation_metadata FROM usage_analytics_facts WHERE usage_record_id = ?`, record.ID).Scan(&factMetadata); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeUsageCacheCreationMetadata(factMetadata)
	if err != nil || decoded.Source == nil || *decoded.Source != "enforced_request" {
		t.Fatalf("compact fact lost provenance: %#v/%v", decoded, err)
	}
	metadata := record.CacheCreationMetadata
	metadata.Reason = ttlText("audit_verified")
	encoded, err := encodeUsageCacheCreationMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE usage_records SET raw_json = '{}', cache_creation_metadata = ? WHERE id = ?`, encoded, record.ID); err != nil {
		t.Fatal(err)
	}
	if count := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, record.ID); count != 1 {
		t.Fatal("metadata-only update did not schedule fact repair")
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT cache_creation_metadata FROM usage_analytics_facts WHERE usage_record_id = ?`, record.ID).Scan(&factMetadata); err != nil || factMetadata != encoded {
		t.Fatalf("metadata repair did not update compact evidence: %s/%v", factMetadata, err)
	}
	if collectorTTLCount(t, a, `SELECT hourly_needs_rebuild FROM usage_analytics_state WHERE id = 1`) != 1 {
		t.Fatal("metadata-only repair retained stale hourly generation")
	}
	stored, err := a.getUsageRecord(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := listItemFromRecordVersioned(stored, nil, pricing, usageRedactionOptions{})
	public, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"execution_id":"` + id + `"`, `"cache_creation_ttl_source":"enforced_request"`, `"cache_creation_policy_revision":7`, `"cache_creation_observed_at":"2026-10-28T10:00:00+08:00"`} {
		if !strings.Contains(string(public), required) {
			t.Fatalf("API lost frozen evidence after raw cleanup, missing %s: %s", required, public)
		}
	}
	if strings.Contains(string(public), pluginCallerScope(collectorTTLTestKey)) {
		t.Fatal("caller scope leaked into public usage DTO")
	}
	assertCollectorTTLCharge(t, a, record.ID, 8, false)
	for _, invalid := range []string{`{"version":"v2"}`, `{"version":"v1"}`, `{"version":"v1","ttl_source":"invented"}`, `{"version":"v1","ttl_source":"enforced_request","status":"matched"}`, `{"version":"v1","status":"matched","extra":"ignored"}`} {
		if _, err := a.db.Exec(`UPDATE usage_records SET cache_creation_metadata = ? WHERE id = ?`, invalid, record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.getUsageRecord(ctx, record.ID); err == nil {
			t.Fatalf("corrupt persisted provenance silently accepted: %s", invalid)
		}
	}
}
