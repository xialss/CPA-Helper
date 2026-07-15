package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpModelPriceChannelsPreservesRowsAndEnforcesScopedUniqueness(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE model_prices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider VARCHAR(120) NOT NULL,
			model VARCHAR(180) NOT NULL,
			input_usd_per_million REAL NOT NULL DEFAULT 0,
			output_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_read_usd_per_million REAL NOT NULL DEFAULT 0,
			cache_creation_usd_per_million REAL NOT NULL DEFAULT 0,
			request_usd REAL,
			priority_multiplier REAL,
			long_context_threshold_tokens INTEGER,
			long_context_input_usd_per_million REAL,
			long_context_output_usd_per_million REAL,
			long_context_cache_read_usd_per_million REAL,
			long_context_cache_creation_usd_per_million REAL,
			source VARCHAR(40) NOT NULL DEFAULT 'manual',
			source_model VARCHAR(180),
			auto_synced BOOLEAN NOT NULL DEFAULT 0,
			last_synced_at DATETIME,
			updated_at DATETIME NOT NULL,
			CONSTRAINT uq_model_prices_provider_model UNIQUE (provider, model)
		);
		INSERT INTO model_prices (
			id, provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES (
			42, 'openai', 'shared-model', 1.25, 5, 0.125, 0.25, 0.04,
			2.5, 200000, 2.5, 10, 0.25, 0.5,
			'manual', 'source-model', 1, '2026-07-12T12:00:00Z', '2026-07-12T13:00:00Z'
		);
		INSERT INTO model_prices (
			id, provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, request_usd,
			priority_multiplier, long_context_threshold_tokens,
			long_context_input_usd_per_million, long_context_output_usd_per_million,
			long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
			source, source_model, auto_synced, last_synced_at, updated_at
		) VALUES
			(50, 'OpenAI', 'Case-Model', 10, 11, 1, 2, 0.5, 3,
			 300000, 20, 22, 2, 4, 'litellm', 'case-source-model', 1,
			 '2026-07-13T15:30:00Z', '2026-07-13T16:00:00Z'),
			(51, 'openai', 'case-model', 20, 0, 0, 0, NULL, NULL,
			 NULL, NULL, NULL, NULL, NULL, 'manual', NULL, 0, NULL, '2026-07-13T14:00:00Z'),
			(52, 'OPENAI', 'CASE-MODEL', 30, 0, 0, 0, NULL, NULL,
			 NULL, NULL, NULL, NULL, NULL, 'manual', NULL, 0, NULL, '2026-07-13T15:00:00Z'),
			(53, 'OpEnAi', 'CaSe-MoDeL', 40, 0, 0, 0, NULL, NULL,
			 NULL, NULL, NULL, NULL, NULL, 'manual', NULL, 0, NULL, '2026-07-13T15:00:00Z')
	`); err != nil {
		t.Fatalf("seed pre-channel schema: %v", err)
	}

	if err := upModelPriceChannels(context.Background(), db); err != nil {
		t.Fatalf("upModelPriceChannels failed: %v", err)
	}

	var (
		id                                                                   int64
		provider, model, scope, source, sourceModel, lastSyncedAt, updatedAt string
		channelBrand, channelKey                                             sql.NullString
		input, output, cacheRead, cacheCreation, requestUSD, priority        float64
		threshold                                                            int64
		longInput, longOutput, longCacheRead, longCacheCreation              float64
		autoSynced                                                           bool
	)
	if err := db.QueryRow(`
		SELECT id, provider, model, price_scope, channel_brand, channel_key,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million,
		       request_usd, priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, last_synced_at, updated_at
		FROM model_prices WHERE id = 42
	`).Scan(
		&id, &provider, &model, &scope, &channelBrand, &channelKey,
		&input, &output, &cacheRead, &cacheCreation, &requestUSD, &priority, &threshold,
		&longInput, &longOutput, &longCacheRead, &longCacheCreation,
		&source, &sourceModel, &autoSynced, &lastSyncedAt, &updatedAt,
	); err != nil {
		t.Fatalf("query migrated price: %v", err)
	}
	if id != 42 || provider != "openai" || model != "shared-model" || scope != "library" ||
		channelBrand.Valid || channelKey.Valid || input != 1.25 || output != 5 ||
		cacheRead != 0.125 || cacheCreation != 0.25 || requestUSD != 0.04 || priority != 2.5 ||
		threshold != 200000 || longInput != 2.5 || longOutput != 10 || longCacheRead != 0.25 ||
		longCacheCreation != 0.5 || source != "manual" || sourceModel != "source-model" ||
		!autoSynced || lastSyncedAt != "2026-07-12T12:00:00Z" || updatedAt != "2026-07-12T13:00:00Z" {
		t.Fatalf("migrated price was not preserved: %#v", []any{
			id, provider, model, scope, channelBrand, channelKey, input, output, cacheRead,
			cacheCreation, requestUSD, priority, threshold, longInput, longOutput,
			longCacheRead, longCacheCreation, source, sourceModel, autoSynced, lastSyncedAt, updatedAt,
		})
	}

	var duplicateCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM model_prices
		WHERE provider = 'openai' COLLATE NOCASE AND model = 'case-model' COLLATE NOCASE
	`).Scan(&duplicateCount); err != nil {
		t.Fatalf("query migrated case-fold duplicate count: %v", err)
	}
	if duplicateCount != 1 {
		t.Fatalf("migrated case-fold duplicate count = %d, want 1", duplicateCount)
	}
	var survivorID int64
	var survivorInput float64
	var survivorSource string
	if err := db.QueryRow(`
		SELECT id, input_usd_per_million, source
		FROM model_prices
		WHERE provider = 'openai' COLLATE NOCASE AND model = 'case-model' COLLATE NOCASE
	`).Scan(&survivorID, &survivorInput, &survivorSource); err != nil {
		t.Fatalf("query migrated case-fold duplicate survivor: %v", err)
	}
	if survivorID != 53 || survivorInput != 40 || survivorSource != "manual" {
		t.Fatalf("case-fold duplicate survivor = id %d input %v source %q, want latest manual row id 53", survivorID, survivorInput, survivorSource)
	}

	var archivedCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM model_price_library_conflicts
		WHERE provider = 'openai' COLLATE NOCASE AND model = 'case-model' COLLATE NOCASE
	`).Scan(&archivedCount); err != nil {
		t.Fatalf("query archived case-fold duplicate count: %v", err)
	}
	if archivedCount != 3 {
		t.Fatalf("archived case-fold duplicate count = %d, want 3", archivedCount)
	}

	var (
		archivedID, selectedID                                                  int64
		conflictReason, archivedProvider, archivedModel, archivedSource         string
		archivedSourceModel, archivedLastSyncedAt, archivedUpdatedAt            string
		archivedInput, archivedOutput, archivedCacheRead, archivedCacheCreation float64
		archivedRequest, archivedPriority                                       float64
		archivedThreshold                                                       int64
		archivedLongInput, archivedLongOutput                                   float64
		archivedLongCacheRead, archivedLongCacheCreation                        float64
		archivedAutoSynced                                                      bool
	)
	if err := db.QueryRow(`
		SELECT original_id, selected_price_id, conflict_reason, provider, model,
		       input_usd_per_million, output_usd_per_million,
		       cache_read_usd_per_million, cache_creation_usd_per_million,
		       request_usd, priority_multiplier, long_context_threshold_tokens,
		       long_context_input_usd_per_million, long_context_output_usd_per_million,
		       long_context_cache_read_usd_per_million, long_context_cache_creation_usd_per_million,
		       source, source_model, auto_synced, last_synced_at, updated_at
		FROM model_price_library_conflicts
		WHERE original_id = 50
	`).Scan(
		&archivedID, &selectedID, &conflictReason, &archivedProvider, &archivedModel,
		&archivedInput, &archivedOutput, &archivedCacheRead, &archivedCacheCreation,
		&archivedRequest, &archivedPriority, &archivedThreshold,
		&archivedLongInput, &archivedLongOutput, &archivedLongCacheRead, &archivedLongCacheCreation,
		&archivedSource, &archivedSourceModel, &archivedAutoSynced, &archivedLastSyncedAt, &archivedUpdatedAt,
	); err != nil {
		t.Fatalf("query archived case-fold duplicate: %v", err)
	}
	if archivedID != 50 || selectedID != 53 || conflictReason != "case_insensitive_library_identity" ||
		archivedProvider != "OpenAI" || archivedModel != "Case-Model" ||
		archivedInput != 10 || archivedOutput != 11 || archivedCacheRead != 1 || archivedCacheCreation != 2 ||
		archivedRequest != 0.5 || archivedPriority != 3 || archivedThreshold != 300000 ||
		archivedLongInput != 20 || archivedLongOutput != 22 || archivedLongCacheRead != 2 || archivedLongCacheCreation != 4 ||
		archivedSource != "litellm" || archivedSourceModel != "case-source-model" || !archivedAutoSynced ||
		archivedLastSyncedAt != "2026-07-13T15:30:00Z" || archivedUpdatedAt != "2026-07-13T16:00:00Z" {
		t.Fatalf("archived duplicate was not preserved: %#v", []any{
			archivedID, selectedID, conflictReason, archivedProvider, archivedModel,
			archivedInput, archivedOutput, archivedCacheRead, archivedCacheCreation,
			archivedRequest, archivedPriority, archivedThreshold, archivedLongInput, archivedLongOutput,
			archivedLongCacheRead, archivedLongCacheCreation, archivedSource, archivedSourceModel,
			archivedAutoSynced, archivedLastSyncedAt, archivedUpdatedAt,
		})
	}

	insertPrice := func(provider, model, scope string, brand, key any, source string) error {
		_, err := db.Exec(`
			INSERT INTO model_prices (
				provider, model, price_scope, channel_brand, channel_key, source, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, '2026-07-12T14:00:00Z')
		`, provider, model, scope, brand, key, source)
		return err
	}
	if err := insertPrice("OPENAI", "SHARED-MODEL", "library", nil, nil, "manual"); err == nil {
		t.Fatal("library provider/model uniqueness should be case-insensitive")
	}
	if err := insertPrice("Vendor Alpha", "shared-model", "channel", "openai_compatibility", "vendor alpha", "manual"); err != nil {
		t.Fatalf("insert first OpenAI-compatible channel: %v", err)
	}
	if err := insertPrice("Vendor Beta", "shared-model", "channel", "openai_compatibility", "vendor beta", "manual"); err != nil {
		t.Fatalf("insert second OpenAI-compatible channel: %v", err)
	}
	if err := insertPrice("Vendor Alpha", "SHARED-MODEL", "channel", "openai_compatibility", "VENDOR ALPHA", "manual"); err == nil {
		t.Fatal("OpenAI-compatible channel/model uniqueness should be case-insensitive")
	}
	if err := insertPrice("gemini", "shared-model", "channel", "gemini", "gemini-a.json", "manual"); err != nil {
		t.Fatalf("insert first native channel: %v", err)
	}
	if err := insertPrice("gemini", "SHARED-MODEL", "channel", "gemini", "gemini-a.json", "manual"); err == nil {
		t.Fatal("native channel/model uniqueness should normalize model case")
	}
	if err := insertPrice("gemini", "shared-model", "channel", "gemini", "GEMINI-A.JSON", "manual"); err != nil {
		t.Fatalf("native auth_index matching should remain exact: %v", err)
	}
	if err := insertPrice("gemini", "another-model", "channel", "gemini", "gemini-b.json", "litellm"); err == nil {
		t.Fatal("LiteLLM prices must remain library scoped")
	}
}
