package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMigrateDoesNotRepairHistoricalUsageTokens(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":2,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":102}}`)

	if _, err := Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102)
}

func TestAuditUsageTokensIsReadOnlyAndRedacted(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	const secret = "secret-api-key-must-not-appear"
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"api_key":"`+secret+`","token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":2,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":102}}`)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatalf("audit usage tokens: %v", err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("read-only audit changed the SQLite database file")
	}
	if audit.Candidates != 1 || audit.InputCandidates != 1 || audit.InputTokenDelta != 100 || audit.OutputCandidates != 0 {
		t.Fatalf("audit = %#v", audit)
	}
	encoded, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "raw_json") {
		t.Fatalf("audit exposed raw payload data: %s", encoded)
	}
	if strings.Contains(string(encoded), dbPath) || strings.Contains(string(encoded), "database_path") {
		t.Fatalf("audit exposed the live database path: %s", encoded)
	}
}

func TestAuditUsageTokensIncludesExactRedactedCandidateEvidence(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	const secret = "candidate-secret-must-not-appear"
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"api_key":"`+secret+`","token_breakdown":{},"tokens":{"input_tokens":100,"output_tokens":2}}`)
	insertUsageTokenRepairRecord(t, dbPath, 2, 1, 0, 3, 4, 5, 6, 21, `{"model":"secret-model","token_breakdown":{},"tokens":{"input_tokens":1,"output_tokens":20}}`)
	insertUsageTokenRepairRecord(t, dbPath, 3, 0, 0, 3, 4, 5, 6, 70, `{"request":{"authorization":"secret-auth"},"token_breakdown":{},"tokens":{"input_tokens":30,"output_tokens":40}}`)

	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []UsageTokenRepairEvidence{
		{RecordID: 1, Timestamp: "2026-07-25T12:00:00Z", Input: &UsageTokenRepairFieldEvidence{Stored: 0, Raw: 100, Path: usageTokenInputPath}},
		{RecordID: 2, Timestamp: "2026-07-25T12:00:00Z", Output: &UsageTokenRepairFieldEvidence{Stored: 0, Raw: 20, Path: usageTokenOutputPath}},
		{RecordID: 3, Timestamp: "2026-07-25T12:00:00Z", Input: &UsageTokenRepairFieldEvidence{Stored: 0, Raw: 30, Path: usageTokenInputPath}, Output: &UsageTokenRepairFieldEvidence{Stored: 0, Raw: 40, Path: usageTokenOutputPath}},
	}
	if !reflect.DeepEqual(audit.Evidence, want) {
		t.Fatalf("evidence = %#v, want %#v", audit.Evidence, want)
	}
	encoded, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "secret-model", "secret-auth", "raw_json", "api_key", "authorization", "model"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("audit evidence exposed %q: %s", forbidden, encoded)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded UsageTokenRepairAudit
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("strict audit decode: %v", err)
	}
	if !reflect.DeepEqual(decoded.Evidence, want) {
		t.Fatalf("round-tripped evidence = %#v, want %#v", decoded.Evidence, want)
	}
}

func TestAuditAndApplyUsageTokensPreserveLargeJSONIntegers(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	const largeInput = 9007199254740993
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 2, `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":9007199254740993,"output_tokens":2}}`)

	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audit.Candidates != 1 || audit.InputTokenDelta != largeInput {
		t.Fatalf("large integer audit = %#v", audit)
	}
	backupPath := newUsageTokenRepairBackupPath(t)
	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err != nil {
		t.Fatal(err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, largeInput, 2, 3, 4, 5, 6, 2)
	assertUsageTokenRepairValues(t, backupPath, 1, 0, 2, 3, 4, 5, 6, 2)
}

func TestAuditUsageTokensRejectsOutOfRangeJSONIntegers(t *testing.T) {
	prepareUsageTokenRepairRuntime(t)
	dbPath := filepath.Join(os.Getenv("CPA_HELPER_DATA_DIR"), "db", "cpa_helper.sqlite3")
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 2, `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":9223372036854775808,"output_tokens":2}}`)

	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audit.Candidates != 0 {
		t.Fatalf("out-of-range integer produced repair candidate: %#v", audit)
	}
}

func TestRepairTokenIntegerRequiresExactDecimalAndExponentIntegers(t *testing.T) {
	for _, value := range []json.Number{"1.0000000000000000001", "9007199254740991.9", "1.5e0"} {
		if token, ok := repairTokenInteger(value); ok {
			t.Fatalf("repairTokenInteger(%q) = %d, true; want invalid fractional value", value, token)
		}
	}
	for value, want := range map[json.Number]int{"1e3": 1000, "9007199254740992.0": 9007199254740992} {
		if token, ok := repairTokenInteger(value); !ok || token != want {
			t.Fatalf("repairTokenInteger(%q) = %d, %t; want %d, true", value, token, ok, want)
		}
	}
}

func TestApplyUsageTokenRepairRequiresGuardsAndRejectsStaleAudit(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	raw := `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":2,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":102}}`
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, raw)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backupPath := newUsageTokenRepairBackupPath(t)

	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, false); err == nil || !strings.Contains(err.Error(), "confirm-writers-paused") {
		t.Fatalf("missing acknowledgement error = %v", err)
	}
	if _, err := ApplyUsageTokenRepair(context.Background(), audit, "", true); err == nil || !strings.Contains(err.Error(), "requires --backup") {
		t.Fatalf("missing backup error = %v", err)
	}

	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	if _, err := db.Exec(`UPDATE usage_records SET input_tokens = 1 WHERE id = 1`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale audit error = %v", err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 1, 2, 3, 4, 5, 6, 102)
}

func TestApplyUsageTokenRepairRejectsTamperedEvidence(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{},"tokens":{"input_tokens":100,"output_tokens":2}}`)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	var tampered UsageTokenRepairAudit
	if err := json.Unmarshal(encoded, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Evidence[0].Input.Raw++
	backupPath := newUsageTokenRepairBackupPath(t)
	if _, err := ApplyUsageTokenRepair(context.Background(), tampered, backupPath, true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("tampered evidence error = %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("tampered report unexpectedly created backup: %v", err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102)
}

func TestApplyUsageTokenRepairDoesNotCreateMissingLiveDatabase(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":2}}`)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backupPath := newUsageTokenRepairBackupPath(t)
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err == nil || !strings.Contains(err.Error(), "inspect live usage database") {
		t.Fatalf("missing live database error = %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("apply recreated missing live database: %v", err)
	}
}

func TestApplyUsageTokenRepairRejectsQuotaChargesAndUnexpectedTotalMismatch(t *testing.T) {
	for name, testCase := range map[string]struct {
		prepare   func(*testing.T, string)
		wantError string
	}{
		"quota charge": {
			prepare: func(t *testing.T, dbPath string) {
				db := openUsageTokenRepairRuntimeDB(t, dbPath)
				defer db.Close()
				if _, err := db.Exec(`INSERT INTO user_quota_charges (usage_record_id, user_id, usage_username, amount_usd, monthly_deducted_usd, lifetime_deducted_usd, unpriced, quota_month, created_at) VALUES (1, 1, 'user', 0, 0, 0, 0, '2026-07', '2026-07-25 12:00:00')`); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "quota charge",
		},
		"unexpected total": {
			prepare: func(t *testing.T, dbPath string) {
				db := openUsageTokenRepairRuntimeDB(t, dbPath)
				defer db.Close()
				if _, err := db.Exec(`UPDATE usage_records SET total_tokens = 0 WHERE id = 1`); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "non-target token mismatches",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dbPath := prepareUsageTokenRepairRuntime(t)
			insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{"input":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":2,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":102}}`)
			testCase.prepare(t, dbPath)
			audit, err := AuditUsageTokens(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			backupPath := newUsageTokenRepairBackupPath(t)
			if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("apply error = %v, want %q", err, testCase.wantError)
			}
			assertUsageTokenRepairValues(t, dbPath, 1, 0, 2, 3, 4, 5, 6, map[string]int{"quota charge": 102, "unexpected total": 0}[name])
		})
	}
}

func TestApplyUsageTokenRepairAuditsNonTargetFieldsBeforeCandidateFiltering(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{},"tokens":{"input_tokens":100,"output_tokens":2,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":102}}`)
	insertUsageTokenRepairRecord(t, dbPath, 2, 10, 20, 3, 4, 5, 6, 0, `{"token_breakdown":{},"tokens":{"input_tokens":10,"output_tokens":20,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":30}}`)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audit.Candidates != 1 || audit.UnexpectedMismatches.TotalTokens != 1 {
		t.Fatalf("audit = %#v", audit)
	}
	backupPath := newUsageTokenRepairBackupPath(t)
	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err == nil || !strings.Contains(err.Error(), "non-target token mismatches") {
		t.Fatalf("apply error = %v", err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102)
	assertUsageTokenRepairValues(t, dbPath, 2, 10, 20, 3, 4, 5, 6, 0)
}

func TestApplyUsageTokenRepairCreatesConsistentBackupIncludingWALState(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{},"tokens":{"input_tokens":100,"output_tokens":2}}`)
	writer := openUsageTokenRepairRuntimeDB(t, dbPath)
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`UPDATE usage_records SET input_tokens = 7 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("expected live WAL before backup: %v", err)
	}
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backupPath := newUsageTokenRepairBackupPath(t)
	if _, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true); err != nil {
		t.Fatal(err)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 100, 2, 3, 4, 5, 6, 102)
	assertUsageTokenRepairValues(t, backupPath, 1, 7, 2, 3, 4, 5, 6, 102)
	backupDB := openUsageTokenRepairRuntimeDB(t, backupPath)
	defer backupDB.Close()
	var quickCheck string
	if err := backupDB.QueryRow(`PRAGMA quick_check`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("backup quick_check = %q, err = %v", quickCheck, err)
	}
	if _, err := checkDatabaseReady(context.Background(), backupDB, backupPath); err != nil {
		t.Fatalf("backup schema readiness: %v", err)
	}
}

func TestCreateUsageTokenRepairBackupRemovesIncompleteDestination(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	defer db.Close()
	backupPath := newUsageTokenRepairBackupPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := createAndVerifyUsageTokenRepairBackup(ctx, db, backupPath, UsageTokenRepairAudit{}); err == nil {
		t.Fatal("create backup with canceled context unexpectedly succeeded")
	}
	if _, err := os.Lstat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete backup destination remains: %v", err)
	}

	expected, err := auditUsageTokens(context.Background(), db, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	expected.Candidates++
	backupPath = newUsageTokenRepairBackupPath(t)
	if _, err := createAndVerifyUsageTokenRepairBackup(context.Background(), db, backupPath, expected); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("logical mismatch error = %v", err)
	}
	if _, err := os.Lstat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unaccepted backup destination remains: %v", err)
	}
}

func TestApplyUsageTokenRepairRejectsExistingLiveAndSidecarBackupDestinations(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102, `{"token_breakdown":{},"tokens":{"input_tokens":100,"output_tokens":2}}`)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	existingValidDB := newUsageTokenRepairBackupPath(t)
	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	if _, err := db.Exec(`VACUUM INTO ?`, existingValidDB); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	unrelatedDB := openUsageTokenRepairRuntimeDB(t, existingValidDB)
	if _, err := unrelatedDB.Exec(`UPDATE usage_records SET input_tokens = 77 WHERE id = 1`); err != nil {
		_ = unrelatedDB.Close()
		t.Fatal(err)
	}
	var quickCheck string
	if err := unrelatedDB.QueryRow(`PRAGMA quick_check`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		_ = unrelatedDB.Close()
		t.Fatalf("unrelated database quick_check = %q, err = %v", quickCheck, err)
	}
	if _, err := checkDatabaseReady(context.Background(), unrelatedDB, existingValidDB); err != nil {
		_ = unrelatedDB.Close()
		t.Fatalf("unrelated database schema readiness: %v", err)
	}
	_ = unrelatedDB.Close()

	tests := []struct {
		name      string
		path      string
		wantError string
	}{
		{name: "existing valid database", path: existingValidDB, wantError: "already exists"},
		{name: "live database", path: dbPath, wantError: "live database or a SQLite sidecar"},
		{name: "wal sidecar", path: dbPath + "-wal", wantError: "live database or a SQLite sidecar"},
		{name: "shm sidecar", path: dbPath + "-shm", wantError: "live database or a SQLite sidecar"},
		{name: "journal sidecar", path: dbPath + "-journal", wantError: "live database or a SQLite sidecar"},
		{name: "directory", path: t.TempDir(), wantError: "new file, not a directory"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ApplyUsageTokenRepair(context.Background(), audit, testCase.path, true); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("apply error = %v, want %q", err, testCase.wantError)
			}
			assertUsageTokenRepairValues(t, dbPath, 1, 0, 2, 3, 4, 5, 6, 102)
		})
	}
}

func TestPrepareUsageTokenRepairBackupPathRejectsAliasedSidecars(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	aliasPath := filepath.Join(t.TempDir(), "db-alias")
	if err := os.Symlink(filepath.Dir(dbPath), aliasPath); err != nil {
		t.Skipf("create backup directory symlink: %v", err)
	}

	testedMissingSidecar := false
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + suffix); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		testedMissingSidecar = true
		backupPath := filepath.Join(aliasPath, filepath.Base(dbPath)+suffix)
		if _, err := prepareUsageTokenRepairBackupPath(dbPath, backupPath); err == nil || !strings.Contains(err.Error(), "live database or a SQLite sidecar") {
			t.Fatalf("aliased sidecar backup %q error = %v", backupPath, err)
		}
		if _, err := os.Lstat(dbPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected sidecar %q was created: %v", dbPath+suffix, err)
		}
	}
	if !testedMissingSidecar {
		t.Fatal("test database unexpectedly has every SQLite sidecar")
	}
}

func TestApplyUsageTokenRepairUpdatesOnlyInputOutputAndIsIdempotent(t *testing.T) {
	dbPath := prepareUsageTokenRepairRuntime(t)
	raw := `{"request":{"authorization":"secret"},"token_breakdown":{"input":{"total_tokens":0},"output":{"total_tokens":0}},"tokens":{"input_tokens":100,"output_tokens":20,"cached_tokens":3,"cache_read_tokens":4,"cache_creation_tokens":5,"reasoning_tokens":6,"total_tokens":120}}`
	insertUsageTokenRepairRecord(t, dbPath, 1, 0, 0, 3, 4, 5, 6, 120, raw)
	audit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backupPath := newUsageTokenRepairBackupPath(t)
	result, err := ApplyUsageTokenRepair(context.Background(), audit, backupPath, true)
	if err != nil {
		t.Fatalf("apply usage token repair: %v", err)
	}
	if result.Updated != 1 || result.RemainingCandidates != 0 || result.Audit.InputTokenDelta != 100 || result.Audit.OutputTokenDelta != 20 {
		t.Fatalf("apply result = %#v", result)
	}
	assertUsageTokenRepairValues(t, dbPath, 1, 100, 20, 3, 4, 5, 6, 120)
	var storedRaw string
	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	if err := db.QueryRow(`SELECT raw_json FROM usage_records WHERE id = 1`).Scan(&storedRaw); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	if storedRaw != raw {
		t.Fatalf("raw_json changed: %q", storedRaw)
	}

	secondAudit, err := AuditUsageTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyUsageTokenRepair(context.Background(), secondAudit, newUsageTokenRepairBackupPath(t), true)
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if second.Updated != 0 || second.RemainingCandidates != 0 {
		t.Fatalf("second apply = %#v", second)
	}
}

func prepareUsageTokenRepairRuntime(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	if _, err := Migrate(context.Background()); err != nil {
		t.Fatalf("migrate repair test database: %v", err)
	}
	return filepath.Join(dataDir, "db", "cpa_helper.sqlite3")
}

func insertUsageTokenRepairRecord(t *testing.T, dbPath string, id, input, output, cached, cacheRead, cacheCreation, reasoning, total int, raw string) {
	t.Helper()
	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO usage_records (
			id, created_at, timestamp, input_tokens, output_tokens, cached_tokens,
			cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens,
			dedupe_key, raw_json
		) VALUES (?, '2026-07-25 12:00:00', '2026-07-25 12:00:00', ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input, output, cached, cacheRead, cacheCreation, reasoning, total, "repair-test-"+string(rune('0'+id)), raw); err != nil {
		t.Fatal(err)
	}
}

func openUsageTokenRepairRuntimeDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func newUsageTokenRepairBackupPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "cpa_helper.backup.sqlite3")
}

func assertUsageTokenRepairValues(t *testing.T, dbPath string, id, input, output, cached, cacheRead, cacheCreation, reasoning, total int) {
	t.Helper()
	db := openUsageTokenRepairRuntimeDB(t, dbPath)
	defer db.Close()
	var gotInput, gotOutput, gotCached, gotCacheRead, gotCacheCreation, gotReasoning, gotTotal int
	if err := db.QueryRow(`SELECT input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens FROM usage_records WHERE id = ?`, id).Scan(&gotInput, &gotOutput, &gotCached, &gotCacheRead, &gotCacheCreation, &gotReasoning, &gotTotal); err != nil {
		t.Fatal(err)
	}
	if gotInput != input || gotOutput != output || gotCached != cached || gotCacheRead != cacheRead || gotCacheCreation != cacheCreation || gotReasoning != reasoning || gotTotal != total {
		t.Fatalf("usage tokens = %d/%d/%d/%d/%d/%d/%d, want %d/%d/%d/%d/%d/%d/%d", gotInput, gotOutput, gotCached, gotCacheRead, gotCacheCreation, gotReasoning, gotTotal, input, output, cached, cacheRead, cacheCreation, reasoning, total)
	}
}
