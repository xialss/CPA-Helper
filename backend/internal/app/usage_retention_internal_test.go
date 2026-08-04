package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageRecordRetentionRejectsLegacyAllAndUsesSecondPrecision(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 34, 56, 987_654_321, appTimeLocation)
	start := usageRawRetentionCutoff(now)
	end := start.Add(time.Hour)
	filters := UsageFilters{Start: &start, End: &end}

	legacy := httptest.NewRequest(http.MethodGet, "/api/usage/records?range=all", nil)
	assertUsageRetentionValidationError(t, validateUsageRecordRetention(legacy, filters, now))

	normal := httptest.NewRequest(http.MethodGet, "/api/usage/records", nil)
	if err := validateUsageRecordRetention(normal, filters, now); err != nil {
		t.Fatalf("exact second-precision 7 day range rejected: %v", err)
	}
	tooOld := start.Add(-time.Second)
	assertUsageRetentionValidationError(t, validateUsageRecordRetention(normal, UsageFilters{Start: &tooOld, End: &end}, now))
}

func TestUsageRecordsRejectsMissingExplicitRangeBeforeDefaults(t *testing.T) {
	user := &AuthUser{ID: 1, Username: "admin", IsAdmin: true}
	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "missing both", path: "/api/usage/records"},
		{name: "missing start", path: "/api/usage/records?end=2026-07-31T12:00:00Z"},
		{name: "missing end", path: "/api/usage/records?start=2026-07-31T11:00:00Z"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			filters, err := parseUsageFilters(request)
			if err != nil {
				t.Fatalf("parseUsageFilters: %v", err)
			}
			err = (&App{}).usageRecords(httptest.NewRecorder(), request, filters, user)
			assertUsageRetentionValidationError(t, err)
		})
	}
}

func TestRawUsageMetadataAliasesMatchNormalizedRecordProjection(t *testing.T) {
	const rawJSON = `{"event":{"Origin":"nested-origin-source","AccountEmail":"Origin-Owner@Example.com","Authentication":"api_key","AccountId":"nested-auth-index"}}`
	normalized, err := normalizeUsage([]byte(rawJSON))
	if err != nil {
		t.Fatalf("normalizeUsage: %v", err)
	}
	if normalized.Source == nil || *normalized.Source != "nested-origin-source" {
		t.Fatalf("normalized source = %#v", normalized.Source)
	}
	if normalized.SourceAccount == nil || *normalized.SourceAccount != "origin-owner@example.com" {
		t.Fatalf("normalized source account = %#v", normalized.SourceAccount)
	}
	if normalized.Auth == nil || *normalized.Auth != "api_key" {
		t.Fatalf("normalized auth = %#v", normalized.Auth)
	}
	if normalized.AuthIndex == nil || *normalized.AuthIndex != "nested-auth-index" {
		t.Fatalf("normalized auth index = %#v", normalized.AuthIndex)
	}

	if source := sourceFromUsageRawJSON(rawJSON); source == nil || *source != *normalized.Source {
		t.Fatalf("raw source = %#v, want %#v", source, normalized.Source)
	}
	if account := sourceAccountFromUsageRawJSON(rawJSON); account == nil || *account != *normalized.SourceAccount {
		t.Fatalf("raw source account = %#v, want %#v", account, normalized.SourceAccount)
	}
	if auth := authFromUsageRawJSON(rawJSON); auth == nil || *auth != *normalized.Auth {
		t.Fatalf("raw auth = %#v, want %#v", auth, normalized.Auth)
	}
	if index := usageAnalyticsRawAuthIndex(rawJSON); index == nil || *index != *normalized.AuthIndex {
		t.Fatalf("raw auth index = %#v, want %#v", index, normalized.AuthIndex)
	}

	item := listItemFromRecord(
		UsageRecord{RawJSON: rawJSON},
		map[string]userInfo{},
		map[[2]string]ModelPrice{},
		usageRedactionOptions{},
	)
	expectedMaskedSource := maskSecret(normalized.Source)
	if source, ok := item["source"].(*string); !ok || source == nil || *source != expectedMaskedSource {
		t.Fatalf("record projection source = %#v, want masked %q", item["source"], expectedMaskedSource)
	}
	if auth, ok := item["auth"].(*string); !ok || auth == nil || *auth != *normalized.Auth {
		t.Fatalf("record projection auth = %#v, want %#v", item["auth"], normalized.Auth)
	}
	if index, ok := item["auth_index"].(*string); !ok || index == nil || *index != *normalized.AuthIndex {
		t.Fatalf("record projection auth index = %#v, want %#v", item["auth_index"], normalized.AuthIndex)
	}
}

func TestUsageRetentionPrunesRawButKeepsFactsAndAuthorizesBeforeGone(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	result, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, provider, model, endpoint, source, auth, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'owner', 'codex', 'gpt-retention', '/v1/responses', 'owner@example.com', 'api_key', 0,
			9, 3, 0, 0, 0, 0, 12, 'retention-old', '{"source":"owner@example.com"}')
	`, dbTime(now), dbTime(timestamp))
	if err != nil {
		t.Fatalf("insert old usage record: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read inserted usage id: %v", err)
	}
	if err := app.ensureUsageAnalyticsFacts(context.Background()); err != nil {
		t.Fatalf("ensureUsageAnalyticsFacts: %v", err)
	}
	pruned, err := app.pruneExpiredUsageRawJSON(context.Background(), now)
	if err != nil {
		t.Fatalf("pruneExpiredUsageRawJSON: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned records = %d, want 1", pruned)
	}

	var rawJSON string
	var factTokens int
	if err := app.db.QueryRow(`SELECT raw_json FROM usage_records WHERE id = ?`, id).Scan(&rawJSON); err != nil {
		t.Fatalf("read pruned raw json: %v", err)
	}
	if err := app.db.QueryRow(`SELECT total_tokens FROM usage_analytics_facts WHERE usage_record_id = ?`, id).Scan(&factTokens); err != nil {
		t.Fatalf("read retained fact: %v", err)
	}
	if rawJSON != "" || factTokens != 12 {
		t.Fatalf("pruned raw/fact tokens = %q/%d, want empty/12", rawJSON, factTokens)
	}

	audit, _, err := auditUsageTokenCandidates(context.Background(), app.db, "usage-retention.db")
	if err != nil {
		t.Fatalf("auditUsageTokenCandidates: %v", err)
	}
	if audit.PrunedRecords != 1 {
		t.Fatalf("audit pruned records = %d, want 1", audit.PrunedRecords)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/usage/records/1", nil)
	if err := app.usageRecordDetail(httptest.NewRecorder(), request, int(id), &AuthUser{Username: "intruder"}); !isUsageAppError(err, "not_found", http.StatusNotFound) {
		t.Fatalf("unauthorized expired record error = %#v, want not_found", err)
	}
	if err := app.usageRecordDetail(httptest.NewRecorder(), request, int(id), &AuthUser{Username: "owner"}); !isUsageAppError(err, "usage_record_expired", http.StatusGone) {
		t.Fatalf("owner expired record error = %#v, want usage_record_expired", err)
	}
}

func TestUsageAnalyticsReconcilesOriginOnlyRawMetadataBeforePrune(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	const source = "origin-only-source"
	const sourceAccount = "origin-alias@example.com"
	const auth = "api_key"
	const authIndex = "origin-auth-index"
	rawJSON := `{"event":{"Origin":"` + source + `","AccountEmail":"Origin-Alias@Example.com","Auth_Type":"` + auth + `","AuthIndex":"` + authIndex + `"}}`
	result, err := app.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-origin', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'origin-only-direct-import', ?)
	`, dbTime(now), dbTime(timestamp), rawJSON)
	if err != nil {
		t.Fatalf("insert origin-only direct import: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read origin-only direct import id: %v", err)
	}

	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("reconcile origin-only direct import: %v", err)
	}
	sourceKey := usageSourceKey(stringPtr(source))
	if sourceKey == nil {
		t.Fatal("origin-only source key is nil")
	}

	var recordSource, recordAccount, recordAuth, recordAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_records WHERE id = ?
	`, recordID).Scan(&recordSource, &recordAccount, &recordAuth, &recordAuthIndex); err != nil {
		t.Fatalf("query reconciled origin record metadata: %v", err)
	}
	if recordSource != source || recordAccount != sourceAccount || recordAuth != auth || recordAuthIndex != authIndex {
		t.Fatalf("reconciled origin record metadata = %q/%q/%q/%q", recordSource, recordAccount, recordAuth, recordAuthIndex)
	}

	var factKey, factAccount, factAuth, factAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query reconciled origin fact metadata: %v", err)
	}
	if factKey != *sourceKey || factAccount != sourceAccount || factAuth != auth || factAuthIndex != authIndex {
		t.Fatalf("reconciled origin fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}

	var catalogSource, catalogAccount, catalogAuth, catalogAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_source_catalog WHERE source_key = ?
	`, *sourceKey).Scan(&catalogSource, &catalogAccount, &catalogAuth, &catalogAuthIndex); err != nil {
		t.Fatalf("query reconciled origin source catalog: %v", err)
	}
	if catalogSource != source || catalogAccount != sourceAccount || catalogAuth != auth || catalogAuthIndex != authIndex {
		t.Fatalf("reconciled origin source catalog = %q/%q/%q/%q", catalogSource, catalogAccount, catalogAuth, catalogAuthIndex)
	}

	assertOriginUsageRetainedForKeeper(t, app, timestamp, recordID, source, sourceAccount, authIndex)
	pruned, err := app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune origin-only raw payload: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned origin-only payloads = %d, want 1", pruned)
	}
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("verify origin-only fact after prune: %v", err)
	}
	var prunedRawJSON string
	if err := app.db.QueryRowContext(ctx, `SELECT raw_json FROM usage_records WHERE id = ?`, recordID).Scan(&prunedRawJSON); err != nil {
		t.Fatalf("query pruned origin-only raw payload: %v", err)
	}
	if prunedRawJSON != "" {
		t.Fatalf("pruned origin-only raw payload = %q, want empty", prunedRawJSON)
	}
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query retained origin fact metadata: %v", err)
	}
	if factKey != *sourceKey || factAccount != sourceAccount || factAuth != auth || factAuthIndex != authIndex {
		t.Fatalf("retained origin fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}
	assertOriginUsageRetainedForKeeper(t, app, timestamp, recordID, source, sourceAccount, authIndex)
}

func TestUsageRetentionPruneSkipsPendingFactInsertedAfterPreflight(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	ctx := context.Background()
	now := time.Now().In(appTimeLocation)
	if err := app.requireUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("preflight analytics facts: %v", err)
	}

	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	const source = "preflight-origin-source"
	const sourceAccount = "preflight-origin@example.com"
	const auth = "api_key"
	const authIndex = "preflight-origin-auth-index"
	rawJSON := `{"event":{"Origin":"` + source + `","AccountEmail":"Preflight-Origin@Example.com","Auth_Type":"` + auth + `","AuthIndex":"` + authIndex + `"}}`
	result, err := app.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-preflight-origin', '/v1/responses', 0,
			1, 1, 0, 0, 0, 0, 2, 'preflight-origin-direct-import', ?)
	`, dbTime(now), dbTime(timestamp), rawJSON)
	if err != nil {
		t.Fatalf("insert backdated direct import after preflight: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read inserted usage record id: %v", err)
	}

	var pending int
	if err := app.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM usage_analytics_pending_facts
		WHERE usage_record_id = ?
	`, recordID).Scan(&pending); err != nil {
		t.Fatalf("query direct-import pending fact: %v", err)
	}
	if pending != 1 {
		t.Fatalf("direct-import pending facts = %d, want 1", pending)
	}

	pruned, err := app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune after direct import: %v", err)
	}
	if pruned != 0 {
		t.Fatalf("pruned pending direct imports = %d, want 0", pruned)
	}
	var retainedRawJSON string
	if err := app.db.QueryRowContext(ctx, `SELECT raw_json FROM usage_records WHERE id = ?`, recordID).Scan(&retainedRawJSON); err != nil {
		t.Fatalf("query skipped pending raw payload: %v", err)
	}
	if retainedRawJSON != rawJSON {
		t.Fatalf("pending raw payload = %q, want retained payload", retainedRawJSON)
	}

	runner := newUsageMaintenanceRunner(app)
	if err := runner.reconcileFactsAfterRawPrune(ctx, pruned); err != nil {
		t.Fatalf("reconcile pending direct import after zero-row prune: %v", err)
	}
	pruned, err = app.pruneExpiredUsageRawJSON(ctx, now)
	if err != nil {
		t.Fatalf("prune reconciled direct import: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned reconciled direct imports = %d, want 1", pruned)
	}

	sourceKey := usageSourceKey(stringPtr(source))
	if sourceKey == nil {
		t.Fatal("direct-import source key is nil")
	}
	var recordSource, recordAccount, recordAuth, recordAuthIndex, prunedRawJSON string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index, raw_json
		FROM usage_records
		WHERE id = ?
	`, recordID).Scan(&recordSource, &recordAccount, &recordAuth, &recordAuthIndex, &prunedRawJSON); err != nil {
		t.Fatalf("query reconciled direct-import record metadata: %v", err)
	}
	if recordSource != source || recordAccount != sourceAccount || recordAuth != auth || recordAuthIndex != authIndex || prunedRawJSON != "" {
		t.Fatalf("reconciled direct-import record = %q/%q/%q/%q/%q", recordSource, recordAccount, recordAuth, recordAuthIndex, prunedRawJSON)
	}

	var factKey, factAccount, factAuth, factAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source_key, source_account, auth, auth_index
		FROM usage_analytics_facts
		WHERE usage_record_id = ?
	`, recordID).Scan(&factKey, &factAccount, &factAuth, &factAuthIndex); err != nil {
		t.Fatalf("query retained direct-import fact metadata: %v", err)
	}
	if factKey != *sourceKey || factAccount != sourceAccount || factAuth != auth || factAuthIndex != authIndex {
		t.Fatalf("retained direct-import fact metadata = %q/%q/%q/%q", factKey, factAccount, factAuth, factAuthIndex)
	}

	var catalogSource, catalogAccount, catalogAuth, catalogAuthIndex string
	if err := app.db.QueryRowContext(ctx, `
		SELECT source, source_account, auth, auth_index
		FROM usage_source_catalog
		WHERE source_key = ?
	`, *sourceKey).Scan(&catalogSource, &catalogAccount, &catalogAuth, &catalogAuthIndex); err != nil {
		t.Fatalf("query retained direct-import source catalog metadata: %v", err)
	}
	if catalogSource != source || catalogAccount != sourceAccount || catalogAuth != auth || catalogAuthIndex != authIndex {
		t.Fatalf("retained direct-import source catalog metadata = %q/%q/%q/%q", catalogSource, catalogAccount, catalogAuth, catalogAuthIndex)
	}
}

func assertOriginUsageRetainedForKeeper(t *testing.T, app *App, timestamp time.Time, recordID int64, source, sourceAccount, authIndex string) {
	t.Helper()
	records, err := app.keeperUsageRecordsInRange(context.Background(), timestamp.Add(-time.Minute), timestamp.Add(time.Minute))
	if err != nil {
		t.Fatalf("load origin-only Keeper usage records: %v", err)
	}
	if len(records) != 1 || records[0].ID != int(recordID) {
		t.Fatalf("origin-only Keeper records = %#v, want record %d", records, recordID)
	}
	record := records[0]
	if record.Source == nil || *record.Source != source || record.SourceAccount == nil || *record.SourceAccount != sourceAccount || record.AuthIndex == nil || *record.AuthIndex != authIndex {
		t.Fatalf("origin-only Keeper record metadata = %#v", record)
	}
}

func TestUsageMaintenancePrunesRawWithoutInvalidatingCurrentHourlyBaseline(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	now := time.Now().In(appTimeLocation)
	timestamp := usageRawRetentionCutoff(now).Add(-time.Hour)
	result, err := app.db.Exec(`
		INSERT INTO usage_records (
			created_at, timestamp, usage_username, provider, model, endpoint, failed,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'owner', 'codex', 'gpt-maintenance', '/v1/responses', 0,
			5, 2, 0, 0, 0, 0, 7, 'maintenance-prune-queue',
			'{"source":"maintenance@example.com","auth_type":"api_key"}')
	`, dbTime(now), dbTime(timestamp))
	if err != nil {
		t.Fatalf("insert expired raw usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read expired raw usage record id: %v", err)
	}

	ctx := context.Background()
	if err := app.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatalf("materialize expired raw usage fact: %v", err)
	}
	pricing, err := app.billingPriceIndex(ctx)
	if err != nil {
		t.Fatalf("build usage pricing index: %v", err)
	}
	if err := app.rebuildUsageAnalyticsHourly(ctx, pricing); err != nil {
		t.Fatalf("build hourly baseline before raw prune: %v", err)
	}
	before, err := loadUsageAnalyticsState(ctx, app.db)
	if err != nil {
		t.Fatalf("load hourly state before raw prune: %v", err)
	}
	if before.hourlyNeedsRebuild || before.hourlyFactsVersion != before.factsVersion || !before.lastHourlyRebuildAt.Valid {
		t.Fatalf("hourly state before raw prune = %#v, want current baseline", before)
	}

	const rebuildMarker = "2000-01-01T00:00:00Z"
	if _, err := app.db.ExecContext(ctx, `
		UPDATE usage_analytics_state
		SET last_hourly_rebuild_at = ?
		WHERE id = 1
	`, rebuildMarker); err != nil {
		t.Fatalf("mark hourly baseline before raw prune: %v", err)
	}

	runner := newUsageMaintenanceRunner(app)
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("usage maintenance RunOnce: %v", err)
	}
	pending, err := app.usageAnalyticsFactsPending(ctx)
	if err != nil {
		t.Fatalf("query pending facts after maintenance: %v", err)
	}
	if pending {
		t.Fatal("maintenance queued a raw-prune fact for request-time reconciliation")
	}

	var rawJSON string
	if err := app.db.QueryRow(`SELECT raw_json FROM usage_records WHERE id = ?`, recordID).Scan(&rawJSON); err != nil {
		t.Fatalf("query maintained raw payload: %v", err)
	}
	if rawJSON != "" {
		t.Fatalf("maintained raw payload = %q, want empty", rawJSON)
	}
	after, err := loadUsageAnalyticsState(ctx, app.db)
	if err != nil {
		t.Fatalf("load maintenance analytics state: %v", err)
	}
	if !after.lastHourlyRebuildAt.Valid || after.lastHourlyRebuildAt.String != rebuildMarker {
		t.Fatalf("raw-prune maintenance rebuilt hourly baseline at %#v, want %q preserved", after.lastHourlyRebuildAt, rebuildMarker)
	}
	if after.hourlyNeedsRebuild || after.hourlyFactsVersion != after.factsVersion || after.hourlyMaxRecordID != before.hourlyMaxRecordID {
		t.Fatalf("raw-prune maintenance state = %#v, want unchanged hourly baseline", after)
	}
}

func TestUsageMaintenanceSkipsCurrentHourlyBaseline(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	insertMaterializedUsageAnalyticsFact(t, app, "maintenance-current-baseline", `{}`)
	runner := newUsageMaintenanceRunner(app)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("establish hourly baseline: %v", err)
	}
	state, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load established hourly state: %v", err)
	}
	if state.hourlyNeedsRebuild || state.hourlyFactsVersion != state.factsVersion || !state.lastHourlyRebuildAt.Valid {
		t.Fatalf("established hourly state = %#v, want current baseline", state)
	}

	const rebuildMarker = "2000-01-01T00:00:00Z"
	if _, err := app.db.ExecContext(context.Background(), `
		UPDATE usage_analytics_state
		SET last_hourly_rebuild_at = ?
		WHERE id = 1
	`, rebuildMarker); err != nil {
		t.Fatalf("mark established hourly baseline: %v", err)
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("run maintenance against current baseline: %v", err)
	}
	after, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load hourly state after current maintenance: %v", err)
	}
	if !after.lastHourlyRebuildAt.Valid || after.lastHourlyRebuildAt.String != rebuildMarker {
		t.Fatalf("current maintenance rebuilt hourly baseline at %#v, want %q preserved", after.lastHourlyRebuildAt, rebuildMarker)
	}
	if after.hourlyNeedsRebuild || after.hourlyFactsVersion != after.factsVersion || after.hourlyMaxRecordID != state.hourlyMaxRecordID {
		t.Fatalf("current maintenance state = %#v, want unchanged hourly baseline", after)
	}
}

func TestUsageMaintenanceFoldsAppendedFactTailIntoHourlyBaseline(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	insertMaterializedUsageAnalyticsFact(t, app, "maintenance-tail-base", `{}`)
	runner := newUsageMaintenanceRunner(app)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("establish hourly baseline: %v", err)
	}
	const rebuildMarker = "2000-01-01T00:00:00Z"
	if _, err := app.db.ExecContext(context.Background(), `
		UPDATE usage_analytics_state
		SET last_hourly_rebuild_at = ?
		WHERE id = 1
	`, rebuildMarker); err != nil {
		t.Fatalf("mark established hourly baseline: %v", err)
	}
	appendedRecordID := insertMaterializedUsageAnalyticsFact(t, app, "maintenance-tail-appended", `{}`)
	tailState, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load appended tail state: %v", err)
	}
	if tailState.hourlyNeedsRebuild || tailState.factsVersion <= tailState.hourlyFactsVersion {
		t.Fatalf("appended maintenance state = %#v, want append-only fact tail", tailState)
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("fold appended fact tail: %v", err)
	}
	state, err := loadUsageAnalyticsState(context.Background(), app.db)
	if err != nil {
		t.Fatalf("load folded hourly state: %v", err)
	}
	if state.hourlyNeedsRebuild || state.hourlyFactsVersion != state.factsVersion || state.hourlyMaxRecordID != appendedRecordID {
		t.Fatalf("folded maintenance state = %#v, want complete baseline through record %d", state, appendedRecordID)
	}
	if !state.lastHourlyRebuildAt.Valid || state.lastHourlyRebuildAt.String == rebuildMarker {
		t.Fatalf("appended tail did not rebuild hourly baseline: %#v", state.lastHourlyRebuildAt)
	}
	var hourlyRecords int
	if err := app.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(SUM(record_count), 0)
		FROM usage_analytics_hourly
		WHERE facts_version = ? AND pricing_version = ? AND selector_generation = ?
	`, state.hourlyFactsVersion, state.pricingVersion, state.selectorGeneration).Scan(&hourlyRecords); err != nil {
		t.Fatalf("count folded hourly records: %v", err)
	}
	if hourlyRecords != 2 {
		t.Fatalf("folded hourly records = %d, want 2", hourlyRecords)
	}
}

func TestUsageMaintenanceStopCancelsInProgressScheduledRun(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	tx, err := app.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin SQLite connection hold: %v", err)
	}
	runner := newUsageMaintenanceRunner(app)
	runner.interval = time.Millisecond
	defer runner.Stop()
	defer func() { _ = tx.Rollback() }()

	waitCount := app.db.Stats().WaitCount
	runner.Start()
	waitForUsageMaintenanceConnectionWait(t, app.db, waitCount)

	stopped := make(chan struct{})
	go func() {
		runner.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return after canceling the scheduled maintenance run")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release SQLite connection hold: %v", err)
	}

	var lastMaintenanceAt, lastMaintenanceError sql.NullString
	if err := app.db.QueryRowContext(context.Background(), `
		SELECT last_maintenance_at, last_maintenance_error
		FROM usage_analytics_state
		WHERE id = 1
	`).Scan(&lastMaintenanceAt, &lastMaintenanceError); err != nil {
		t.Fatalf("read maintenance state after cancellation: %v", err)
	}
	if lastMaintenanceAt.Valid {
		t.Fatalf("canceled scheduled maintenance recorded success at %q", lastMaintenanceAt.String)
	}
	if lastMaintenanceError.Valid && lastMaintenanceError.String != "" {
		t.Fatalf("canceled scheduled maintenance recorded failure %q", lastMaintenanceError.String)
	}
}

func TestCompactUsageDatabaseRequiresConfirmationAndCreatesBackup(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	if _, err := Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "usage-compact-backup.sqlite3")
	if _, err := CompactUsageDatabase(context.Background(), backupPath, false); err == nil {
		t.Fatal("CompactUsageDatabase without writer confirmation unexpectedly succeeded")
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed compaction created backup: %v", err)
	}

	report, err := CompactUsageDatabase(context.Background(), backupPath, true)
	if err != nil {
		t.Fatalf("CompactUsageDatabase: %v", err)
	}
	absBackupPath, err := filepath.Abs(backupPath)
	if err != nil {
		t.Fatalf("resolve backup path: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat compaction backup: %v", err)
	}
	if report.BackupPath != filepath.Clean(absBackupPath) || report.BackupBytes != info.Size() || report.BeforeBytes <= 0 || report.AfterBytes <= 0 {
		t.Fatalf("compaction report = %#v, backup bytes = %d", report, info.Size())
	}
}

func TestCompactUsageDatabaseRejectsPendingRawPayloadCorrectionBeforeBackup(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	recordID := insertMaterializedUsageAnalyticsFact(t, app, "compact-pending", `{"source":"before@example.com"}`)
	if _, err := app.db.ExecContext(context.Background(), `
		UPDATE usage_records
		SET raw_json = ?
		WHERE id = ?
	`, `{"source":"after@example.com"}`, recordID); err != nil {
		t.Fatalf("correct raw payload: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "compact-pending-backup.sqlite3")
	if _, err := CompactUsageDatabase(context.Background(), backupPath, true); err == nil || !strings.Contains(err.Error(), "pending repair") {
		t.Fatalf("CompactUsageDatabase pending-fact error = %v, want clear pending-repair refusal", err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending-fact compaction created backup: %v", err)
	}
	pending, err := app.usageAnalyticsFactsPending(context.Background())
	if err != nil {
		t.Fatalf("query pending facts after refused compaction: %v", err)
	}
	if !pending {
		t.Fatal("refused compaction reconciled the pending fact")
	}
}

func TestCompactUsageDatabaseRejectsOrphanedFactsBeforeBackup(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatalf("NewWithOptions() failed: %v", err)
	}
	defer app.Close()

	recordID := insertMaterializedUsageAnalyticsFact(t, app, "compact-orphan", `{"source":"orphan@example.com"}`)
	conn, err := app.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire SQLite connection: %v", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
			t.Errorf("restore SQLite foreign-key enforcement: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close SQLite connection: %v", err)
		}
	}()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable SQLite foreign-key enforcement for orphan fixture: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `DELETE FROM usage_records WHERE id = ?`, recordID); err != nil {
		t.Fatalf("create orphaned fact fixture: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("restore SQLite foreign-key enforcement: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "compact-orphan-backup.sqlite3")
	if _, err := CompactUsageDatabase(context.Background(), backupPath, true); err == nil || !strings.Contains(err.Error(), "orphaned facts") {
		t.Fatalf("CompactUsageDatabase orphaned-fact error = %v, want clear orphaned-fact refusal", err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned-fact compaction created backup: %v", err)
	}
}

func insertMaterializedUsageAnalyticsFact(t *testing.T, app *App, dedupeKey, rawJSON string) int64 {
	t.Helper()
	result, err := app.db.ExecContext(context.Background(), `
		INSERT INTO usage_records (
			created_at, timestamp, provider, model, endpoint,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, dedupe_key, raw_json
		) VALUES (?, ?, 'codex', 'gpt-compact', '/v1/responses', 1, 0, 0, 0, 0, 0, 1, ?, ?)
	`, dbTime(time.Now()), dbTime(time.Now()), dedupeKey, rawJSON)
	if err != nil {
		t.Fatalf("insert usage record: %v", err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read inserted usage record id: %v", err)
	}
	if err := app.ensureUsageAnalyticsFacts(context.Background()); err != nil {
		t.Fatalf("materialize usage analytics fact: %v", err)
	}
	return recordID
}

func assertUsageRetentionValidationError(t *testing.T, err error) {
	t.Helper()
	if !isUsageAppError(err, "validation_error", http.StatusUnprocessableEntity) {
		t.Fatalf("retention error = %#v, want validation_error", err)
	}
}

func isUsageAppError(err error, code string, status int) bool {
	var appErr *AppError
	return errors.As(err, &appErr) && appErr.Code == code && appErr.Status == status
}

func waitForUsageMaintenanceConnectionWait(t *testing.T, db *sql.DB, previousWaitCount int64) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if db.Stats().WaitCount > previousWaitCount {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("scheduled maintenance did not begin waiting for the SQLite connection")
		case <-poll.C:
		}
	}
}
