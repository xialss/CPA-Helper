package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	usageTokenRepairAuditVersion = 3
	usageTokenInputPath          = "$.tokens.input_tokens"
	usageTokenOutputPath         = "$.tokens.output_tokens"
)

type UsageTokenRepairMismatches struct {
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

func (m UsageTokenRepairMismatches) Total() int64 {
	return m.CachedTokens + m.CacheReadTokens + m.CacheCreationTokens + m.ReasoningTokens + m.TotalTokens
}

type UsageTokenRepairAudit struct {
	Version              int                        `json:"version"`
	DatabasePath         string                     `json:"-"`
	DatabaseFingerprint  string                     `json:"database_fingerprint"`
	GeneratedAt          string                     `json:"generated_at"`
	Candidates           int64                      `json:"candidates"`
	InputCandidates      int64                      `json:"input_candidates"`
	OutputCandidates     int64                      `json:"output_candidates"`
	InputTokenDelta      int64                      `json:"input_token_delta"`
	OutputTokenDelta     int64                      `json:"output_token_delta"`
	PrunedRecords        int64                      `json:"pruned_records"`
	EarliestTimestamp    string                     `json:"earliest_timestamp,omitempty"`
	LatestTimestamp      string                     `json:"latest_timestamp,omitempty"`
	QuotaChargeRows      int64                      `json:"quota_charge_rows"`
	UnexpectedMismatches UsageTokenRepairMismatches `json:"unexpected_mismatches"`
	Evidence             []UsageTokenRepairEvidence `json:"evidence"`
	Fingerprint          string                     `json:"fingerprint"`
}

type UsageTokenRepairFieldEvidence struct {
	Stored int    `json:"stored"`
	Raw    int    `json:"raw"`
	Path   string `json:"path"`
}

type UsageTokenRepairEvidence struct {
	RecordID  int64                          `json:"record_id"`
	Timestamp string                         `json:"timestamp"`
	Input     *UsageTokenRepairFieldEvidence `json:"input,omitempty"`
	Output    *UsageTokenRepairFieldEvidence `json:"output,omitempty"`
}

type UsageTokenRepairResult struct {
	Audit               UsageTokenRepairAudit `json:"audit"`
	Updated             int64                 `json:"updated"`
	Skipped             int64                 `json:"skipped"`
	RemainingCandidates int64                 `json:"remaining_candidates"`
	PrunedRecords       int64                 `json:"pruned_records"`
}

type usageTokenRepairCandidate struct {
	id                  int64
	timestamp           string
	inputTokens         int
	outputTokens        int
	cachedTokens        int
	cacheReadTokens     int
	cacheCreationTokens int
	reasoningTokens     int
	totalTokens         int
	rawInputTokens      int
	rawOutputTokens     int
	hasRawInput         bool
	hasRawOutput        bool
	inputMismatch       bool
	outputMismatch      bool
	rawNonTarget        [5]int
	hasRawNonTarget     [5]bool
	hasQuotaCharge      bool
}

type usageTokenRepairQuerier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func AuditUsageTokens(ctx context.Context) (UsageTokenRepairAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return UsageTokenRepairAudit{}, err
	}
	db, err := openRuntimeDB(paths, true)
	if err != nil {
		return UsageTokenRepairAudit{}, err
	}
	defer db.Close()
	if _, err := checkDatabaseReady(ctx, db, paths.DBPath); err != nil {
		return UsageTokenRepairAudit{}, err
	}
	return auditUsageTokens(ctx, db, paths.DBPath)
}

func ApplyUsageTokenRepair(ctx context.Context, approved UsageTokenRepairAudit, backupPath string, writersPaused bool) (result UsageTokenRepairResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !writersPaused {
		return result, errors.New("usage token repair requires --confirm-writers-paused")
	}
	if approved.Version != usageTokenRepairAuditVersion || approved.DatabaseFingerprint == "" || approved.Fingerprint == "" {
		return result, errors.New("usage token repair requires a valid audit report")
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return result, err
	}
	absoluteBackupPath, err := prepareUsageTokenRepairBackupPath(paths.DBPath, backupPath)
	if err != nil {
		return result, err
	}
	db, err := openRuntimeDB(paths, false)
	if err != nil {
		return result, err
	}
	defer db.Close()
	if _, err := checkDatabaseReady(ctx, db, paths.DBPath); err != nil {
		return result, err
	}
	analytics := &App{db: db}
	if err := analytics.requireUsageAnalyticsFacts(ctx); err != nil {
		return result, fmt.Errorf("usage token repair requires reconciled analytics facts: %w", err)
	}
	preBackup, err := auditUsageTokens(ctx, db, paths.DBPath)
	if err != nil {
		return result, err
	}
	if !sameUsageTokenRepairAudit(approved, preBackup) {
		return result, errors.New("usage token repair audit is stale; run audit again")
	}
	if err := validateUsageTokenRepairSafety(preBackup); err != nil {
		return result, err
	}
	backupAudit, err := createAndVerifyUsageTokenRepairBackup(ctx, db, absoluteBackupPath, preBackup)
	if err != nil {
		return result, err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return result, fmt.Errorf("start usage token repair transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	current, candidates, err := auditUsageTokenCandidates(ctx, conn, paths.DBPath)
	if err != nil {
		return result, err
	}
	if !sameUsageTokenRepairAudit(approved, current) {
		return result, errors.New("usage token repair audit is stale; run audit again")
	}
	if !sameUsageTokenRepairLogicalAudit(backupAudit, current) {
		return result, errors.New("usage token repair backup no longer matches the current live audit")
	}
	if err := validateUsageTokenRepairSafety(current); err != nil {
		return result, err
	}

	var updated int64
	for _, candidate := range candidates {
		var execResult sql.Result
		switch {
		case candidate.inputMismatch && candidate.outputMismatch:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_records SET input_tokens = ?, output_tokens = ? WHERE id = ? AND input_tokens = ? AND output_tokens = ?`, candidate.rawInputTokens, candidate.rawOutputTokens, candidate.id, candidate.inputTokens, candidate.outputTokens)
		case candidate.inputMismatch:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_records SET input_tokens = ? WHERE id = ? AND input_tokens = ?`, candidate.rawInputTokens, candidate.id, candidate.inputTokens)
		case candidate.outputMismatch:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_records SET output_tokens = ? WHERE id = ? AND output_tokens = ?`, candidate.rawOutputTokens, candidate.id, candidate.outputTokens)
		default:
			continue
		}
		if err != nil {
			return result, fmt.Errorf("update usage record %d: %w", candidate.id, err)
		}
		rows, err := execResult.RowsAffected()
		if err != nil {
			return result, err
		}
		if rows != 1 {
			return result, fmt.Errorf("usage token repair candidate %d changed during apply", candidate.id)
		}
		updated += rows
		switch {
		case candidate.inputMismatch && candidate.outputMismatch:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_analytics_facts SET input_tokens = ?, output_tokens = ? WHERE usage_record_id = ?`, candidate.rawInputTokens, candidate.rawOutputTokens, candidate.id)
		case candidate.inputMismatch:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_analytics_facts SET input_tokens = ? WHERE usage_record_id = ?`, candidate.rawInputTokens, candidate.id)
		default:
			execResult, err = conn.ExecContext(ctx, `UPDATE usage_analytics_facts SET output_tokens = ? WHERE usage_record_id = ?`, candidate.rawOutputTokens, candidate.id)
		}
		if err != nil {
			return result, fmt.Errorf("update usage analytics fact %d: %w", candidate.id, err)
		}
		factRows, err := execResult.RowsAffected()
		if err != nil {
			return result, err
		}
		if factRows != 1 {
			return result, fmt.Errorf("usage analytics fact %d changed during repair", candidate.id)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM usage_analytics_pending_facts WHERE usage_record_id = ?`, candidate.id); err != nil {
			return result, fmt.Errorf("clear usage analytics pending fact %d: %w", candidate.id, err)
		}
	}
	if updated > 0 {
		if err := markUsageAnalyticsFactsChanged(ctx, conn); err != nil {
			return result, err
		}
	}

	after, _, err := auditUsageTokenCandidates(ctx, conn, paths.DBPath)
	if err != nil {
		return result, err
	}
	if after.Candidates != 0 {
		return result, fmt.Errorf("usage token repair verification failed: %d candidates remain", after.Candidates)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return result, err
	}
	committed = true
	return UsageTokenRepairResult{
		Audit:               current,
		Updated:             updated,
		Skipped:             current.Candidates - updated,
		RemainingCandidates: after.Candidates,
		PrunedRecords:       current.PrunedRecords,
	}, nil
}

func auditUsageTokens(ctx context.Context, db usageTokenRepairQuerier, dbPath string) (UsageTokenRepairAudit, error) {
	audit, _, err := auditUsageTokenCandidates(ctx, db, dbPath)
	return audit, err
}

func auditUsageTokenCandidates(ctx context.Context, db usageTokenRepairQuerier, dbPath string) (UsageTokenRepairAudit, []usageTokenRepairCandidate, error) {
	return auditUsageTokenCandidatesAt(ctx, db, dbPath, time.Now().In(appTimeLocation))
}

func auditUsageTokenCandidatesAt(ctx context.Context, db usageTokenRepairQuerier, dbPath string, now time.Time) (UsageTokenRepairAudit, []usageTokenRepairCandidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, timestamp, input_tokens, output_tokens, cached_tokens,
		       cache_read_tokens, cache_creation_tokens, reasoning_tokens,
		       total_tokens, raw_json
		FROM usage_records
		ORDER BY id
	`)
	if err != nil {
		return UsageTokenRepairAudit{}, nil, err
	}
	defer rows.Close()

	var auditedRecords []usageTokenRepairCandidate
	var candidates []usageTokenRepairCandidate
	var prunedRecords int64
	cutoff := usageRawRetentionCutoff(now)
	for rows.Next() {
		var candidate usageTokenRepairCandidate
		var rawJSON string
		if err := rows.Scan(&candidate.id, &candidate.timestamp, &candidate.inputTokens, &candidate.outputTokens, &candidate.cachedTokens, &candidate.cacheReadTokens, &candidate.cacheCreationTokens, &candidate.reasoningTokens, &candidate.totalTokens, &rawJSON); err != nil {
			return UsageTokenRepairAudit{}, nil, err
		}
		if strings.TrimSpace(rawJSON) == "" {
			prunedRecords++
			continue
		}
		timestamp, ok := parseDBTime(candidate.timestamp)
		if !ok {
			return UsageTokenRepairAudit{}, nil, fmt.Errorf("parse usage token repair timestamp for record %d: %q", candidate.id, candidate.timestamp)
		}
		if timestamp.Before(cutoff) {
			prunedRecords++
			continue
		}
		var payload map[string]any
		decoder := json.NewDecoder(strings.NewReader(rawJSON))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			continue
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			continue
		}
		if _, ok := payload["token_breakdown"].(map[string]any); !ok {
			continue
		}
		tokens, ok := payload["tokens"].(map[string]any)
		if !ok {
			continue
		}
		candidate.rawInputTokens, candidate.hasRawInput = repairTokenInteger(tokens["input_tokens"])
		candidate.rawOutputTokens, candidate.hasRawOutput = repairTokenInteger(tokens["output_tokens"])
		for index, key := range []string{"cached_tokens", "cache_read_tokens", "cache_creation_tokens", "reasoning_tokens", "total_tokens"} {
			candidate.rawNonTarget[index], candidate.hasRawNonTarget[index] = repairTokenInteger(tokens[key])
		}
		candidate.inputMismatch = candidate.hasRawInput && candidate.inputTokens != candidate.rawInputTokens
		candidate.outputMismatch = candidate.hasRawOutput && candidate.outputTokens != candidate.rawOutputTokens
		auditedRecords = append(auditedRecords, candidate)
		if !candidate.inputMismatch && !candidate.outputMismatch {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return UsageTokenRepairAudit{}, nil, err
	}

	charged := make(map[int64]bool)
	chargeRows, err := db.QueryContext(ctx, `SELECT usage_record_id FROM user_quota_charges`)
	if err != nil {
		return UsageTokenRepairAudit{}, nil, err
	}
	for chargeRows.Next() {
		var id int64
		if err := chargeRows.Scan(&id); err != nil {
			_ = chargeRows.Close()
			return UsageTokenRepairAudit{}, nil, err
		}
		charged[id] = true
	}
	if err := chargeRows.Close(); err != nil {
		return UsageTokenRepairAudit{}, nil, err
	}

	absoluteDBPath, err := filepath.Abs(dbPath)
	if err != nil {
		return UsageTokenRepairAudit{}, nil, err
	}
	audit := UsageTokenRepairAudit{
		Version:             usageTokenRepairAuditVersion,
		DatabasePath:        filepath.Clean(absoluteDBPath),
		DatabaseFingerprint: usageTokenRepairDatabaseFingerprint(absoluteDBPath),
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		PrunedRecords:       prunedRecords,
		Candidates:          int64(len(candidates)),
		Evidence:            make([]UsageTokenRepairEvidence, 0, len(candidates)),
	}
	for index := range auditedRecords {
		candidate := &auditedRecords[index]
		candidate.hasQuotaCharge = charged[candidate.id]
		stored := [...]int{candidate.cachedTokens, candidate.cacheReadTokens, candidate.cacheCreationTokens, candidate.reasoningTokens, candidate.totalTokens}
		counts := [...]*int64{&audit.UnexpectedMismatches.CachedTokens, &audit.UnexpectedMismatches.CacheReadTokens, &audit.UnexpectedMismatches.CacheCreationTokens, &audit.UnexpectedMismatches.ReasoningTokens, &audit.UnexpectedMismatches.TotalTokens}
		for field := range stored {
			if candidate.hasRawNonTarget[field] && stored[field] != candidate.rawNonTarget[field] {
				(*counts[field])++
			}
		}
	}
	for index := range candidates {
		candidate := &candidates[index]
		candidate.hasQuotaCharge = charged[candidate.id]
		if candidate.hasQuotaCharge {
			audit.QuotaChargeRows++
		}
		if candidate.inputMismatch {
			audit.InputCandidates++
			audit.InputTokenDelta += int64(candidate.rawInputTokens) - int64(candidate.inputTokens)
		}
		if candidate.outputMismatch {
			audit.OutputCandidates++
			audit.OutputTokenDelta += int64(candidate.rawOutputTokens) - int64(candidate.outputTokens)
		}
		if audit.EarliestTimestamp == "" || candidate.timestamp < audit.EarliestTimestamp {
			audit.EarliestTimestamp = candidate.timestamp
		}
		if audit.LatestTimestamp == "" || candidate.timestamp > audit.LatestTimestamp {
			audit.LatestTimestamp = candidate.timestamp
		}
		evidence := UsageTokenRepairEvidence{RecordID: candidate.id, Timestamp: candidate.timestamp}
		if candidate.inputMismatch {
			evidence.Input = &UsageTokenRepairFieldEvidence{Stored: candidate.inputTokens, Raw: candidate.rawInputTokens, Path: usageTokenInputPath}
		}
		if candidate.outputMismatch {
			evidence.Output = &UsageTokenRepairFieldEvidence{Stored: candidate.outputTokens, Raw: candidate.rawOutputTokens, Path: usageTokenOutputPath}
		}
		audit.Evidence = append(audit.Evidence, evidence)
	}
	audit.Fingerprint = usageTokenRepairFingerprint(auditedRecords)
	return audit, candidates, nil
}

func repairTokenInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	if integer, err := strconv.ParseInt(number.String(), 10, strconv.IntSize); err == nil {
		if integer < 0 {
			return 0, false
		}
		return int(integer), true
	}

	// Decimal and exponent forms are accepted only when their exact value is an
	// integer that float64 can represent without losing integer precision.
	// Larger plain integers are handled by ParseInt above.
	exact, ok := new(big.Rat).SetString(number.String())
	if !ok || exact.Sign() < 0 || !exact.IsInt() {
		return 0, false
	}
	integer := exact.Num()
	if integer.Cmp(big.NewInt(1<<53)) > 0 ||
		(strconv.IntSize == 32 && integer.Cmp(big.NewInt(math.MaxInt32)) > 0) {
		return 0, false
	}
	return int(integer.Int64()), true
}

func usageTokenRepairFingerprint(candidates []usageTokenRepairCandidate) string {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	digest := sha256.New()
	for _, candidate := range candidates {
		writeUsageTokenRepairFingerprint(digest, candidate)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func usageTokenRepairDatabaseFingerprint(databasePath string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(databasePath)))
	return hex.EncodeToString(digest[:])
}

func writeUsageTokenRepairFingerprint(digest hash.Hash, candidate usageTokenRepairCandidate) {
	fmt.Fprintf(digest, "%d\x00%s\x00%d\x00%d\x00%d\x00%d\x00%t\x00%t\x00%t\x00",
		candidate.id, candidate.timestamp, candidate.inputTokens, candidate.outputTokens,
		candidate.rawInputTokens, candidate.rawOutputTokens, candidate.hasRawInput,
		candidate.hasRawOutput, candidate.hasQuotaCharge)
	for _, stored := range []int{candidate.cachedTokens, candidate.cacheReadTokens, candidate.cacheCreationTokens, candidate.reasoningTokens, candidate.totalTokens} {
		fmt.Fprintf(digest, "%d\x00", stored)
	}
	for index := range candidate.rawNonTarget {
		fmt.Fprintf(digest, "%d\x00%t\x00", candidate.rawNonTarget[index], candidate.hasRawNonTarget[index])
	}
}

func sameUsageTokenRepairAudit(approved, current UsageTokenRepairAudit) bool {
	return approved.Version == current.Version &&
		approved.DatabaseFingerprint == current.DatabaseFingerprint &&
		approved.Candidates == current.Candidates &&
		approved.InputCandidates == current.InputCandidates &&
		approved.OutputCandidates == current.OutputCandidates &&
		approved.InputTokenDelta == current.InputTokenDelta &&
		approved.OutputTokenDelta == current.OutputTokenDelta &&
		approved.PrunedRecords == current.PrunedRecords &&
		approved.EarliestTimestamp == current.EarliestTimestamp &&
		approved.LatestTimestamp == current.LatestTimestamp &&
		approved.QuotaChargeRows == current.QuotaChargeRows &&
		approved.UnexpectedMismatches == current.UnexpectedMismatches &&
		reflect.DeepEqual(approved.Evidence, current.Evidence) &&
		approved.Fingerprint == current.Fingerprint
}

func sameUsageTokenRepairLogicalAudit(left, right UsageTokenRepairAudit) bool {
	left.DatabaseFingerprint = ""
	right.DatabaseFingerprint = ""
	return sameUsageTokenRepairAudit(left, right)
}

func validateUsageTokenRepairSafety(audit UsageTokenRepairAudit) error {
	if audit.QuotaChargeRows > 0 {
		return fmt.Errorf("usage token repair stopped: %d candidate quota charge rows require manual audit", audit.QuotaChargeRows)
	}
	if unexpected := audit.UnexpectedMismatches.Total(); unexpected > 0 {
		return fmt.Errorf("usage token repair stopped: %d unexpected non-target token mismatches require manual audit", unexpected)
	}
	return nil
}

func prepareUsageTokenRepairBackupPath(targetPath, backupPath string) (string, error) {
	if backupPath == "" {
		return "", errors.New("usage token repair requires --backup")
	}
	targetInfo, err := os.Stat(targetPath)
	if err != nil {
		return "", fmt.Errorf("inspect live usage database: %w", err)
	}
	if !targetInfo.Mode().IsRegular() {
		return "", errors.New("live usage database must be a regular SQLite file")
	}
	absoluteTargetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	absoluteBackupPath, err := filepath.Abs(backupPath)
	if err != nil {
		return "", err
	}
	for _, protectedPath := range []string{absoluteTargetPath, absoluteTargetPath + "-wal", absoluteTargetPath + "-shm", absoluteTargetPath + "-journal"} {
		if strings.EqualFold(filepath.Clean(absoluteBackupPath), filepath.Clean(protectedPath)) {
			return "", errors.New("usage token repair backup destination must not be the live database or a SQLite sidecar")
		}
	}
	if info, err := os.Lstat(absoluteBackupPath); err == nil {
		if info.IsDir() {
			return "", errors.New("usage token repair backup destination must be a new file, not a directory")
		}
		return "", errors.New("usage token repair backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect usage token repair backup destination: %w", err)
	}
	parentInfo, err := os.Stat(filepath.Dir(absoluteBackupPath))
	if err != nil {
		return "", fmt.Errorf("inspect usage token repair backup directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return "", errors.New("usage token repair backup parent must be a directory")
	}
	resolvedTargetPath, err := filepath.EvalSymlinks(absoluteTargetPath)
	if err != nil {
		return "", fmt.Errorf("resolve live usage database path: %w", err)
	}
	resolvedBackupParent, err := filepath.EvalSymlinks(filepath.Dir(absoluteBackupPath))
	if err != nil {
		return "", fmt.Errorf("resolve usage token repair backup directory: %w", err)
	}
	resolvedBackupPath := filepath.Join(resolvedBackupParent, filepath.Base(absoluteBackupPath))
	for _, protectedPath := range []string{resolvedTargetPath, resolvedTargetPath + "-wal", resolvedTargetPath + "-shm", resolvedTargetPath + "-journal"} {
		if strings.EqualFold(filepath.Clean(resolvedBackupPath), filepath.Clean(protectedPath)) {
			return "", errors.New("usage token repair backup destination must not be the live database or a SQLite sidecar")
		}
	}
	return filepath.Clean(absoluteBackupPath), nil
}

func createAndVerifyUsageTokenRepairBackup(ctx context.Context, db *sql.DB, absoluteBackupPath string, expected UsageTokenRepairAudit) (audit UsageTokenRepairAudit, err error) {
	backupFile, err := os.OpenFile(absoluteBackupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return audit, fmt.Errorf("reserve usage token repair backup destination: %w", err)
	}
	verified := false
	defer func() {
		if verified {
			return
		}
		if removeErr := os.Remove(absoluteBackupPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove incomplete usage token repair backup: %w", removeErr))
		}
	}()
	if err := backupFile.Close(); err != nil {
		return audit, fmt.Errorf("reserve usage token repair backup destination: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, absoluteBackupPath); err != nil {
		return audit, fmt.Errorf("create usage token repair backup: %w", err)
	}
	audit, err = verifyCreatedUsageTokenRepairBackup(ctx, absoluteBackupPath)
	if err != nil {
		return UsageTokenRepairAudit{}, err
	}
	if !sameUsageTokenRepairLogicalAudit(expected, audit) {
		return UsageTokenRepairAudit{}, errors.New("usage token repair backup does not match the approved live audit")
	}
	verified = true
	return audit, nil
}

func verifyCreatedUsageTokenRepairBackup(ctx context.Context, absoluteBackupPath string) (UsageTokenRepairAudit, error) {
	backupDB, err := sql.Open("sqlite", sqliteDSN(absoluteBackupPath, true))
	if err != nil {
		return UsageTokenRepairAudit{}, fmt.Errorf("open usage token repair backup: %w", err)
	}
	defer backupDB.Close()
	backupDB.SetMaxOpenConns(1)
	var quickCheck string
	if err := backupDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return UsageTokenRepairAudit{}, fmt.Errorf("verify usage token repair backup: %w", err)
	}
	if quickCheck != "ok" {
		return UsageTokenRepairAudit{}, fmt.Errorf("verify usage token repair backup: quick_check returned %q", quickCheck)
	}
	if _, err := checkDatabaseReady(ctx, backupDB, absoluteBackupPath); err != nil {
		return UsageTokenRepairAudit{}, fmt.Errorf("verify usage token repair backup schema: %w", err)
	}
	audit, err := auditUsageTokens(ctx, backupDB, absoluteBackupPath)
	if err != nil {
		return UsageTokenRepairAudit{}, fmt.Errorf("audit usage token repair backup: %w", err)
	}
	return audit, nil
}
