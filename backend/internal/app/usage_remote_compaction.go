package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
)

const remoteCompactionMetadataField = "cpa_helper_request_metadata"
const remoteCompactionLogSource = "cpa-request-log"

// Two workers leave capacity for explicit audits in the shared four-download
// log reader. One upstream batch bounds the in-memory backlog; it holds IDs,
// timestamps and configuration only, never request bodies or queue payloads.
const remoteCompactionWorkers = 2

var errCompactionLogIncomplete = errors.New("CPA request log is not complete")
var errCompactionLogMismatch = errors.New("CPA request log does not match the collected request")

type remoteCompactionMarker struct {
	Version          int    `json:"version"`
	Source           string `json:"source"`
	RequestID        string `json:"request_id"`
	RemoteCompaction bool   `json:"remote_compaction"`
}

type remoteCompactionCandidate struct {
	RecordID   int
	DedupeKey  string
	RequestID  string
	QueuedAt   time.Time
	ReceivedAt time.Time
	Collector  CollectorConfig
}

func remoteCompactionTraceTime(value string) (time.Time, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '7' || !strings.ContainsRune("89ab", rune(value[19])) || value != strings.ToLower(value) {
		return time.Time{}, false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(raw) != 16 {
		return time.Time{}, false
	}
	var milliseconds int64
	for _, b := range raw[:6] {
		milliseconds = milliseconds<<8 | int64(b)
	}
	return time.UnixMilli(milliseconds), true
}

func usageRemoteCompaction(record UsageRecord) bool {
	var object map[string]json.RawMessage
	if record.RequestID == nil || json.Unmarshal([]byte(record.RawJSON), &object) != nil {
		return false
	}
	// Exact keys avoid accepting differently cased queue fields as provenance.
	var marker map[string]json.RawMessage
	if json.Unmarshal(object[remoteCompactionMetadataField], &marker) != nil {
		return false
	}
	var version int
	var source, requestID string
	var positive bool
	if json.Unmarshal(marker["version"], &version) != nil || json.Unmarshal(marker["source"], &source) != nil || json.Unmarshal(marker["request_id"], &requestID) != nil || json.Unmarshal(marker["remote_compaction"], &positive) != nil {
		return false
	}
	_, valid := remoteCompactionTraceTime(requestID)
	return version == 1 && source == remoteCompactionLogSource && positive && valid && requestID == *record.RequestID
}

type remoteCompactionReader struct {
	app    *App
	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan remoteCompactionCandidate
	wg     sync.WaitGroup
}

func newRemoteCompactionReader(ctx context.Context, a *App) *remoteCompactionReader {
	ctx, cancel := context.WithCancel(ctx)
	reader := &remoteCompactionReader{app: a, ctx: ctx, cancel: cancel, jobs: make(chan remoteCompactionCandidate, collectorUsageBatchSize)}
	for range remoteCompactionWorkers {
		reader.wg.Add(1)
		go func() {
			defer reader.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case candidate := <-reader.jobs:
					if ctx.Err() != nil {
						return
					}
					if err := a.readRemoteCompaction(ctx, candidate); err != nil && ctx.Err() == nil {
						log.Printf("remote compaction label unavailable for record %d: %v", candidate.RecordID, err)
					}
				}
			}
		}()
	}
	return reader
}

func (r *remoteCompactionReader) stop() {
	r.cancel()
	r.wg.Wait()
}

func isCompactionResponsesEndpoint(endpoint string) bool {
	parts := strings.Fields(endpoint)
	if len(parts) != 2 || parts[0] != "POST" {
		return false
	}
	u, err := url.Parse(parts[1])
	if err != nil {
		return false
	}
	switch u.Path {
	case "/v1/responses", "/openai/v1/responses", "/backend-api/codex/responses":
		return true
	}
	return false
}

// Only the batch just admitted by the runner is eligible. Completed duplicates
// retain their earlier received_at; historical duplicate receipts have no quota
// completion. Neither startup inbox recovery nor UI reads call this method.
func (r *remoteCompactionReader) scheduleBatch(ctx context.Context, collector CollectorConfig, messages []string, receivedAt time.Time) error {
	seen := map[string]bool{}
	dropped := 0
	for _, message := range messages {
		normalized, err := normalizeUsage([]byte(message))
		if err != nil {
			return err
		}
		if normalized.RequestID == nil || normalized.Endpoint == nil || !isCompactionResponsesEndpoint(*normalized.Endpoint) {
			continue
		}
		if _, valid := remoteCompactionTraceTime(*normalized.RequestID); !valid || seen[normalized.DedupeKey] {
			continue
		}
		seen[normalized.DedupeKey] = true
		candidate := remoteCompactionCandidate{DedupeKey: normalized.DedupeKey, RequestID: *normalized.RequestID, QueuedAt: normalized.Timestamp, ReceivedAt: receivedAt, Collector: collector}
		err = r.app.db.QueryRowContext(ctx, `SELECT r.id FROM usage_records AS r
			JOIN usage_collector_inbox AS i ON i.usage_record_id = r.id
			WHERE r.dedupe_key = ? AND r.collector_origin = ? AND r.request_id = ?
			AND i.collector_origin = r.collector_origin AND i.dedupe_key = r.dedupe_key
			AND i.received_at = ? AND i.state = 'complete' AND i.quota_handled = 1
			AND r.created_at >= i.received_at AND r.raw_json <> ''`, candidate.DedupeKey, usageCollectorOrigin(collector), candidate.RequestID, dbTime(receivedAt)).Scan(&candidate.RecordID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case r.jobs <- candidate:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		return fmt.Errorf("native log reader backlog is full; %d new candidates remain unclassified", dropped)
	}
	return nil
}

func (a *App) readRemoteCompaction(ctx context.Context, candidate remoteCompactionCandidate) error {
	// CPA publishes usage before finalizing its log file. Four reads at 0, 1,
	// 3 and 7 seconds cover that observed race without creating a history job.
	// A ten-second lifetime also bounds network IO and shutdown cancellation.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Second << (attempt - 1))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		positive, err := a.inspectRemoteCompactionLog(ctx, candidate)
		if err == nil {
			if positive {
				return a.saveRemoteCompactionMarker(ctx, candidate)
			}
			return nil
		}
		last = err
		var upstream *AppError
		if !errors.Is(err, errCompactionLogIncomplete) && !(errors.As(err, &upstream) && (upstream.Code == "audit_log_unavailable" || upstream.Code == "audit_busy")) {
			return err
		}
	}
	return fmt.Errorf("new request log did not become available after four attempts: %w", last)
}

func (a *App) inspectRemoteCompactionLog(ctx context.Context, candidate remoteCompactionCandidate) (bool, error) {
	file, _, err := a.fetchUsageLog(ctx, AppConfig{Collector: candidate.Collector}, candidate.RequestID)
	if err != nil {
		return false, err
	}
	defer removeUsageLog(file)
	return parseRemoteCompactionLog(compactionContextReader{ctx, file}, candidate.RequestID, candidate.QueuedAt)
}

type compactionContextReader struct {
	ctx context.Context
	io.Reader
}

func (r compactionContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (a *App) saveRemoteCompactionMarker(ctx context.Context, candidate remoteCompactionCandidate) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT r.raw_json FROM usage_records AS r
		JOIN usage_collector_inbox AS i ON i.usage_record_id = r.id
		WHERE r.id = ? AND r.dedupe_key = ? AND r.collector_origin = ? AND r.request_id = ?
		AND i.collector_origin = r.collector_origin AND i.dedupe_key = r.dedupe_key
		AND i.received_at = ? AND i.state = 'complete' AND i.quota_handled = 1
		AND r.raw_json <> '' AND r.timestamp >= ?`, candidate.RecordID, candidate.DedupeKey, usageCollectorOrigin(candidate.Collector), candidate.RequestID, dbTime(candidate.ReceivedAt), dbTime(usageRawRetentionCutoff(time.Now()))).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // A concurrently expired/removed record must not regain details.
	}
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
		return errors.New("invalid saved usage JSON for compaction marker")
	}
	marker, err := json.Marshal(remoteCompactionMarker{1, remoteCompactionLogSource, candidate.RequestID, true})
	if err != nil {
		return err
	}
	object[remoteCompactionMetadataField] = marker
	updated, err := json.Marshal(object)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_records SET raw_json = ? WHERE id = ?`, string(updated), candidate.RecordID); err != nil {
		return err
	}
	return tx.Commit()
}

// The native log endpoint uses an eight-character filename suffix. This parser
// rejects conflicting full traces and impossible request-time ordering; it does
// not claim those checks give the upstream endpoint full-UUID uniqueness.
func parseRemoteCompactionLog(input io.Reader, requestID string, queuedAt time.Time) (bool, error) {
	startedAt, valid := remoteCompactionTraceTime(requestID)
	if !valid {
		return false, errCompactionLogMismatch
	}
	reader := bufio.NewReader(input)
	line, err := compactionLogLine(reader)
	if err != nil || strings.TrimPrefix(line, "\ufeff") != "=== REQUEST INFO ===" {
		return false, errCompactionLogIncomplete
	}
	var method, target, transport string
	var loggedAt time.Time
	for {
		line, err = compactionLogLine(reader)
		if err != nil {
			return false, errCompactionLogIncomplete
		}
		if line == "=== HEADERS ===" {
			break
		}
		key, value, _ := strings.Cut(line, ": ")
		switch key {
		case "URL":
			target = value
		case "Method":
			method = value
		case "Downstream Transport":
			transport = value
		case "Timestamp":
			loggedAt, err = time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return false, errCompactionLogMismatch
			}
		}
	}
	if !isCompactionResponsesEndpoint(method+" "+target) || transport != "http" || loggedAt.IsZero() || loggedAt.Before(startedAt) || queuedAt.IsZero() || loggedAt.After(queuedAt) {
		return false, errCompactionLogMismatch
	}
	for {
		line, err = compactionLogLine(reader)
		if err != nil {
			return false, errCompactionLogIncomplete
		}
		if line == "=== REQUEST BODY ===" {
			break
		}
		key, value, _ := strings.Cut(line, ":")
		if strings.EqualFold(key, "Upgrade") && strings.EqualFold(strings.TrimSpace(value), "websocket") {
			return false, errCompactionLogMismatch
		}
		if strings.HasPrefix(line, "=== ") {
			return false, errCompactionLogMismatch
		}
	}
	decoder := json.NewDecoder(reader)
	positive, err := readCompactionRequest(decoder)
	if err != nil {
		return false, errCompactionLogIncomplete
	}
	// Decode exactly one JSON request, then require an actual native section
	// boundary. A partial body or appended JSON must never produce a true tag.
	reader = bufio.NewReader(io.MultiReader(decoder.Buffered(), reader))
	for {
		line, err = compactionLogLine(reader)
		if err != nil {
			return false, errCompactionLogIncomplete
		}
		if line != "" {
			break
		}
	}
	// Native CPA can publish failed usage before it sends an upstream request;
	// those complete logs start with API ERROR RESPONSE instead of API REQUEST.
	if line != "=== RESPONSE ===" && line != "=== API ERROR RESPONSE ===" && !auditSectionPattern.MatchString(line) {
		return false, errCompactionLogMismatch
	}
	// Read only the downstream header block for an optional authoritative full
	// trace. Do not use input headers or inspect response data for compaction.
	for line != "=== RESPONSE ===" {
		line, err = compactionLogLine(reader)
		if err != nil {
			return false, errCompactionLogIncomplete
		}
	}
	for {
		line, err = compactionLogLine(reader)
		if err != nil {
			return false, errCompactionLogIncomplete
		}
		if line == "" {
			return positive, nil
		}
		key, value, _ := strings.Cut(line, ":")
		if strings.EqualFold(key, "X-CPA-TRACE-ID") && !strings.HasSuffix(strings.TrimSpace(value), "-"+requestID) {
			return false, errCompactionLogMismatch
		}
	}
}

func compactionLogLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err
}

// Token-wise traversal discards unrelated values instead of retaining a full
// multi-megabyte prompt, and only reads an item's direct, exactly named type.
func readCompactionRequest(decoder *json.Decoder) (bool, error) {
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false, errCompactionLogIncomplete
	}
	positive, inputSeen := false, false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false, err
		}
		value, err := decoder.Token()
		if err != nil {
			return false, err
		}
		if key == "input" {
			if inputSeen {
				return false, errCompactionLogMismatch
			}
			inputSeen = true
			if value == json.Delim('[') {
				for decoder.More() {
					item, err := decoder.Token()
					if err != nil {
						return false, err
					}
					if item == json.Delim('{') {
						typeSeen := false
						for decoder.More() {
							field, err := decoder.Token()
							if err != nil {
								return false, err
							}
							child, err := decoder.Token()
							if err != nil {
								return false, err
							}
							if field == "type" {
								if typeSeen {
									return false, errCompactionLogMismatch
								}
								typeSeen = true
								positive = positive || child == "compaction_trigger"
							}
							if err := skipCompactionJSONValue(decoder, child); err != nil {
								return false, err
							}
						}
						if _, err := decoder.Token(); err != nil {
							return false, err
						}
					} else if err := skipCompactionJSONValue(decoder, item); err != nil {
						return false, err
					}
				}
				if _, err := decoder.Token(); err != nil {
					return false, err
				}
				continue
			}
		}
		if err := skipCompactionJSONValue(decoder, value); err != nil {
			return false, err
		}
	}
	_, err = decoder.Token()
	return positive, err
}

func skipCompactionJSONValue(decoder *json.Decoder, value json.Token) error {
	if value != json.Delim('{') && value != json.Delim('[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		value, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}
