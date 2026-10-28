package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	collectorTTLMetadataWait = 5 * time.Minute
	collectorTTLRetention    = 24 * time.Hour
	collectorTTLBatchSize    = 1000
)

// This is attribution evidence, never a second token or monetary ledger.
// It is stored independently of raw_json so raw retention cannot erase it.
type usageCacheCreationMetadata struct {
	Version        string     `json:"version"`
	ExecutionID    *string    `json:"execution_id,omitempty"`
	Source         *string    `json:"ttl_source,omitempty"`
	Status         *string    `json:"status,omitempty"`
	Reason         *string    `json:"reason,omitempty"`
	PolicyRevision *uint64    `json:"policy_revision,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
}

type usageTTLExecutionMetadata struct {
	ExecutionID    string    `json:"execution_id"`
	CallerScope    string    `json:"caller_scope"`
	TTL            string    `json:"ttl"`
	Source         string    `json:"ttl_source"`
	PolicyRevision uint64    `json:"policy_revision"`
	ObservedAt     time.Time `json:"observed_at"`
}

type usageTTLMetadataResponse struct {
	MetadataVersion string                      `json:"metadata_version"`
	RetentionHours  int                         `json:"retention_hours"`
	Records         []usageTTLExecutionMetadata `json:"records"`
	Missing         []string                    `json:"missing_execution_ids"`
}

type usageTTLMetadataProtocolError struct{ reason string }

func (e *usageTTLMetadataProtocolError) Error() string {
	return "CPA Helper Plugin TTL metadata contract is invalid: " + e.reason
}

func ttlText(value string) *string { return &value }

func validUsageExecutionID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || strings.ToLower(id) != id {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

func validUsageCallerScope(scope string) bool {
	if len(scope) != 64 || strings.ToLower(scope) != scope {
		return false
	}
	_, err := hex.DecodeString(scope)
	return err == nil
}

func decodeUsageCacheCreationMetadata(raw string) (usageCacheCreationMetadata, error) {
	if strings.TrimSpace(raw) == "" {
		return usageCacheCreationMetadata{}, nil
	}
	var metadata usageCacheCreationMetadata
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return metadata, fmt.Errorf("stored cache creation metadata is malformed")
	}
	if metadata.Version != "v1" || (metadata.ExecutionID != nil && !validUsageExecutionID(*metadata.ExecutionID)) {
		return metadata, fmt.Errorf("stored cache creation metadata version or execution identity is unsupported")
	}
	if metadata.Status == nil || metadata.Reason == nil || strings.TrimSpace(*metadata.Reason) == "" {
		return metadata, fmt.Errorf("stored cache creation metadata lacks attribution status")
	}
	if metadata.Source != nil && *metadata.Source != "upstream" && *metadata.Source != "enforced_request" && *metadata.Source != "unknown" {
		return metadata, fmt.Errorf("stored cache creation metadata source is unsupported")
	}
	if metadata.Status != nil {
		switch *metadata.Status {
		case "pending", "matched", "missing", "expired", "unsupported", "error", "conflict", "not_applicable", "legacy":
		default:
			return metadata, fmt.Errorf("stored cache creation metadata status is unsupported")
		}
	}
	if metadata.PolicyRevision != nil && (*metadata.PolicyRevision == 0 || *metadata.PolicyRevision > pluginModelCatalogMaxRev) {
		return metadata, fmt.Errorf("stored cache creation metadata revision is invalid")
	}
	if metadata.ObservedAt != nil && metadata.ObservedAt.IsZero() {
		return metadata, fmt.Errorf("stored cache creation metadata timestamp is invalid")
	}
	if metadata.Source != nil && *metadata.Source == "enforced_request" &&
		(metadata.ExecutionID == nil || metadata.PolicyRevision == nil || metadata.ObservedAt == nil || metadata.Status == nil || *metadata.Status != "matched") {
		return metadata, fmt.Errorf("stored cache creation metadata lacks execution evidence")
	}
	return metadata, nil
}

func encodeUsageCacheCreationMetadata(metadata usageCacheCreationMetadata) (string, error) {
	if metadata.Version == "" {
		return "", nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	if _, err := decodeUsageCacheCreationMetadata(string(encoded)); err != nil {
		return "", err
	}
	return string(encoded), nil
}

func initialUsageCacheCreationMetadata(usage normalizedUsage) usageCacheCreationMetadata {
	metadata := usageCacheCreationMetadata{Version: "v1", ExecutionID: usage.ExecutionID}
	if usage.CacheCreationTokens <= 0 {
		metadata.Status, metadata.Reason = ttlText("not_applicable"), ttlText("no_cache_creation")
		return metadata
	}
	metadata.Source, metadata.Status, metadata.Reason = ttlText("unknown"), ttlText("legacy"), ttlText("legacy_no_metadata")
	if usage.CacheCreationTTL != nil {
		switch *usage.CacheCreationTTL {
		case "5m", "1h", "mixed":
			metadata.Source, metadata.Status, metadata.Reason = ttlText("upstream"), ttlText("matched"), ttlText("upstream_reported")
			if *usage.CacheCreationTTL == "mixed" && !validUsageMixedBuckets(usage.CacheCreationTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens) {
				metadata.Status, metadata.Reason = ttlText("conflict"), ttlText("invalid_upstream_buckets")
			}
		}
	}
	return metadata
}

func validUsageMixedBuckets(total int, five, hour *int) bool {
	return five != nil && hour != nil && *five >= 0 && *hour >= 0 && *five <= total && *hour == total-*five
}

func usageCacheCreationMetadataForRecord(record UsageRecord) usageCacheCreationMetadata {
	if record.CacheCreationMetadata.Version != "" {
		return record.CacheCreationMetadata
	}
	return initialUsageCacheCreationMetadata(normalizedUsage{
		CacheCreationTokens: record.CacheCreationTokens, CacheCreationTTL: record.CacheCreationTTL,
		CacheCreation5mTokens: record.CacheCreation5mTokens, CacheCreation1hTokens: record.CacheCreation1hTokens,
	})
}

func (a *App) lookupUsageTTLMetadata(ctx context.Context, cfg AppConfig, ids []string, now time.Time) (map[string]usageTTLExecutionMetadata, error) {
	if len(ids) == 0 || len(ids) > collectorTTLBatchSize {
		return nil, &usageTTLMetadataProtocolError{reason: "invalid_lookup_batch"}
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validUsageExecutionID(id) || requested[id] {
			return nil, &usageTTLMetadataProtocolError{reason: "invalid_lookup_identity"}
		}
		requested[id] = true
	}
	body, err := json.Marshal(map[string]any{"execution_ids": ids})
	if err != nil {
		return nil, err
	}
	if len(body) > 64<<10 {
		return nil, &usageTTLMetadataProtocolError{reason: "invalid_lookup_batch"}
	}
	status, _, raw, err := a.cpaHelperPluginRequest(ctx, cfg, http.MethodPost, cpaHelperPluginManagementPrefix+"/claude-ttl/executions", body, nil)
	if err != nil {
		return nil, appErrorFromPlugin(err, status)
	}
	var response usageTTLMetadataResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &usageTTLMetadataProtocolError{reason: "invalid_metadata_response"}
	}
	if response.MetadataVersion != "v1" {
		return nil, &usageTTLMetadataProtocolError{reason: "unsupported_metadata_version"}
	}
	if response.RetentionHours != 24 || response.Records == nil || response.Missing == nil {
		return nil, &usageTTLMetadataProtocolError{reason: "invalid_metadata_response"}
	}
	seen := make(map[string]bool, len(ids))
	result := make(map[string]usageTTLExecutionMetadata, len(response.Records))
	for _, record := range response.Records {
		if !requested[record.ExecutionID] || seen[record.ExecutionID] || !validUsageCallerScope(record.CallerScope) ||
			(record.TTL != "5m" && record.TTL != "1h") || record.PolicyRevision == 0 || record.PolicyRevision > pluginModelCatalogMaxRev ||
			record.ObservedAt.IsZero() || !record.ObservedAt.Add(collectorTTLRetention).After(now) {
			return nil, &usageTTLMetadataProtocolError{reason: "invalid_metadata_record"}
		}
		if record.Source != "enforced_request" {
			return nil, &usageTTLMetadataProtocolError{reason: "unsupported_metadata_source"}
		}
		seen[record.ExecutionID], result[record.ExecutionID] = true, record
	}
	for _, id := range response.Missing {
		if !requested[id] || seen[id] {
			return nil, &usageTTLMetadataProtocolError{reason: "invalid_metadata_partition"}
		}
		seen[id] = true
	}
	if len(seen) != len(requested) {
		return nil, &usageTTLMetadataProtocolError{reason: "invalid_metadata_partition"}
	}
	return result, nil
}

func usageTTLMetadataErrorReason(err error) (string, bool) {
	var protocol *usageTTLMetadataProtocolError
	if errors.As(err, &protocol) {
		return protocol.reason, true
	}
	var application *AppError
	if errors.As(err, &application) {
		switch application.Code {
		case "plugin_incompatible":
			return "metadata_feature_incompatible", true
		case "plugin_not_loaded", "plugin_not_configured":
			return "metadata_unsupported", true
		}
	}
	return "metadata_unavailable", false
}

func usageAnalyticsTableColumns(ctx context.Context, executor usageAnalyticsExecutor, table string) (map[string]bool, error) {
	queryer, ok := executor.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, fmt.Errorf("usage metadata executor cannot inspect its schema")
	}
	if table != "usage_records" && table != "usage_analytics_facts" {
		return nil, fmt.Errorf("invalid usage metadata table")
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
