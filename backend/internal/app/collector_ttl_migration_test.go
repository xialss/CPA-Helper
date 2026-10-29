package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
)

func TestCollectorTTLMigrationBackfillsWithoutReplayingLedger(t *testing.T) {
	ctx := context.Background()
	path := prepareMigrationTestDatabase(t, t.TempDir(), 202610270001)
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for index, test := range []struct{ ttl, five, hour string }{{"'unknown'", "NULL", "NULL"}, {"'1h'", "NULL", "NULL"}, {"'mixed'", "NULL", "NULL"}} {
		_, err := db.Exec(`INSERT INTO usage_records (id, created_at, timestamp, failed, input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json, cache_creation_ttl, cache_creation_5m_tokens, cache_creation_1h_tokens)
			VALUES (?, '2026-10-27T10:00:00+08:00', '2026-10-27T10:00:00+08:00', 0, 0, 0, 0, 0, 100, 0, 100, ?, ?, `+test.ttl+`, `+test.five+`, `+test.hour+`)`, index+1, "old-ttl-"+test.ttl, `{"execution_id":"`+collectorTTLID(index+60)+`","request_id":"not-an-execution"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id                     int
		source, status, reason string
	}{
		{1, "unknown", "legacy", "legacy_no_metadata"}, {2, "upstream", "legacy", "historical_upstream"}, {3, "upstream", "conflict", "invalid_upstream_buckets"},
	} {
		var raw string
		if err := db.QueryRow(`SELECT cache_creation_metadata FROM usage_records WHERE id = ?`, test.id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			ExecutionID *string `json:"execution_id"`
			Source      string  `json:"ttl_source"`
			Status      string  `json:"status"`
			Reason      string  `json:"reason"`
		}
		err := json.Unmarshal([]byte(raw), &metadata)
		if err != nil || metadata.ExecutionID == nil || metadata.Source != test.source || metadata.Status != test.status || metadata.Reason != test.reason {
			t.Fatalf("migration provenance %d = %#v/%v", test.id, metadata, err)
		}
	}
	var charges, inbox int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_quota_charges`).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_collector_inbox`).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if charges != 0 || inbox != 0 {
		t.Fatal("migration replayed old usage into a new ledger/producer")
	}
	if err := goose.DownToContext(ctx, db, ".", 202610270001); err != nil {
		t.Fatalf("safe unused down: %v", err)
	}
	if err := goose.UpToContext(ctx, db, ".", backendMigrations.LatestVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO usage_collector_inbox (collector_origin, dedupe_key, payload_json, received_at, next_attempt_at, deadline_at) VALUES ('origin', 'pending', '{}', '2026', '2026', '2026')`); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 202610270001); err == nil || !strings.Contains(err.Error(), "frozen accounting evidence") {
		t.Fatalf("unsafe down did not protect pending receipts: %v", err)
	}
	if !testTableExists(t, db, "usage_collector_inbox") {
		t.Fatal("unsafe rollback partially removed inbox")
	}
	if _, err := db.Exec(`DELETE FROM usage_collector_inbox; UPDATE usage_records SET source_key = ? WHERE id = 1`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 202610270001); err == nil || !strings.Contains(err.Error(), "frozen accounting evidence") {
		t.Fatalf("rollback lost a protected source's only retained identity: %v", err)
	}
}
