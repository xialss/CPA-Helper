package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const usageAuditTimeout = 60 * time.Second
const usageLogPreviewBytes = 1 << 20

type usageAuditFlight struct {
	done   chan struct{}
	origin string
	result *usageModelAuditResult
	err    error
}

type usageAuditEnvelope struct {
	Audit                *usageModelAuditResult `json:"audit"`
	SourceStatus         string                 `json:"source_status"`
	AcknowledgementToken *string                `json:"acknowledgement_token"`
}

func usageAuditError(code, message string, status int) error {
	return &AppError{Code: code, Message: message, Status: status}
}

func usageAuditAcknowledgement(cfg AppConfig, id int) string {
	mac := hmac.New(sha256.New, []byte(cfg.SessionSecret))
	fmt.Fprintf(mac, "usage-log-source:%d:%s", id, usageCollectorOrigin(cfg.Collector))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) usageAuditContext(ctx context.Context, id int) (UsageRecord, AppConfig, string, error) {
	record, err := a.getUsageRecord(ctx, id)
	if err != nil {
		return record, AppConfig{}, "", err
	}
	if usageRecordIsExpired(record, time.Now()) {
		return record, AppConfig{}, "", notFoundError("请求明细已超过保留期")
	}
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return record, cfg, "", err
	}
	current := usageCollectorOrigin(cfg.Collector)
	if current == "" {
		return record, cfg, "", usageAuditError("audit_source_invalid", "CPA 管理地址无效", 400)
	}
	var origin sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT collector_origin FROM usage_records WHERE id = ?`, id).Scan(&origin); err != nil {
		return record, cfg, "", err
	}
	if origin.Valid && origin.String != "" && origin.String != current {
		return record, cfg, "", usageAuditError("audit_source_mismatch", "记录来源与当前 CPA 不一致", 409)
	}
	status := "matched"
	if !origin.Valid || origin.String == "" {
		status = "unknown"
	}
	return record, cfg, status, nil
}

func (a *App) recheckUsageAudit(r *http.Request, id int, cfg AppConfig) error {
	if _, err := a.superAdminUser(r.Context(), r); err != nil {
		return err
	}
	return a.recheckUsageAuditContext(r.Context(), id, cfg, 0)
}

func (a *App) recheckUsageAuditContext(ctx context.Context, id int, cfg AppConfig, actorID int) error {
	if actorID > 0 {
		var isAdmin, isSuperAdmin bool
		var disabledAt sql.NullString
		if err := a.db.QueryRowContext(ctx, `SELECT is_admin, is_super_admin, disabled_at FROM users WHERE id = ?`, actorID).Scan(&isAdmin, &isSuperAdmin, &disabledAt); err != nil {
			return err
		}
		if !isAdmin || !isSuperAdmin || disabledAt.Valid {
			return forbiddenError("需要超级管理员权限")
		}
	}
	_, current, _, err := a.usageAuditContext(ctx, id)
	if err != nil {
		return err
	}
	if usageCollectorOrigin(current.Collector) != usageCollectorOrigin(cfg.Collector) || current.Collector.ManagementKey != cfg.Collector.ManagementKey {
		return usageAuditError("audit_source_mismatch", "核查期间 CPA 配置已改变，请重新操作", 409)
	}
	return nil
}

func (a *App) loadUsageModelAudit(ctx context.Context, id int, origin string) (*usageModelAuditResult, error) {
	var savedOrigin, raw string
	err := a.db.QueryRowContext(ctx, `SELECT collector_origin, result_json FROM usage_model_audits WHERE usage_record_id = ?`, id).Scan(&savedOrigin, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if savedOrigin != origin {
		return nil, nil
	}
	var result usageModelAuditResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	if result.ParserVersion != usageModelAuditParserVersion {
		return nil, nil
	}
	return &result, nil
}

func (a *App) handleUsageModelAudit(w http.ResponseWriter, r *http.Request, rawID, action string) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	user, err := a.superAdminUser(r.Context(), r)
	if err != nil {
		return err
	}
	id, err := parseIntPath(rawID)
	if err != nil {
		return err
	}
	if action == "cpa-log" {
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return requireMethod(r, http.MethodGet)
	}
	record, cfg, sourceStatus, err := a.usageAuditContext(r.Context(), id)
	if err != nil {
		return err
	}
	origin := usageCollectorOrigin(cfg.Collector)
	envelope := usageAuditEnvelope{SourceStatus: sourceStatus}
	if sourceStatus == "unknown" {
		token := usageAuditAcknowledgement(cfg, id)
		envelope.AcknowledgementToken = &token
	}
	var payload struct {
		Refresh           bool   `json:"refresh"`
		AcknowledgeSource string `json:"acknowledge_source"`
	}
	if action == "model-audit" && r.Method == http.MethodGet {
		envelope.Audit, err = a.loadUsageModelAudit(r.Context(), id, origin)
		if err != nil {
			return err
		}
		if err := a.recheckUsageAudit(r, id, cfg); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, envelope)
		return nil
	}
	if r.Method == http.MethodPost {
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
	} else {
		payload.AcknowledgeSource = r.URL.Query().Get("acknowledge_source")
	}
	if sourceStatus == "unknown" && !hmac.Equal([]byte(payload.AcknowledgeSource), []byte(usageAuditAcknowledgement(cfg, id))) {
		return usageAuditError("audit_source_acknowledgement_required", "历史记录来源未知，请明确确认使用当前 CPA 核查", 409)
	}
	if record.RequestID == nil || strings.TrimSpace(*record.RequestID) == "" {
		return usageAuditError("audit_request_id_missing", "记录没有 request_id，无法获取日志", 400)
	}
	if action == "cpa-log" {
		file, _, err := a.fetchUsageLog(r.Context(), cfg, *record.RequestID)
		if err != nil {
			return err
		}
		defer removeUsageLog(file)
		if err := a.recheckUsageAudit(r, id, cfg); err != nil {
			return err
		}
		stat, err := file.Stat()
		if err != nil {
			return err
		}
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cpa-request-%d.log"`, id))
			w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
			_, err = io.Copy(w, file)
			return err
		}
		preview, err := io.ReadAll(io.LimitReader(file, usageLogPreviewBytes))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"text": string(preview), "truncated": stat.Size() > usageLogPreviewBytes, "total_bytes": stat.Size()})
		return nil
	}
	if !payload.Refresh {
		envelope.Audit, err = a.loadUsageModelAudit(r.Context(), id, origin)
		if err != nil {
			return err
		}
		if envelope.Audit != nil && envelope.Audit.Status == "complete" {
			if err := a.recheckUsageAudit(r, id, cfg); err != nil {
				return err
			}
			writeJSON(w, 200, envelope)
			return nil
		}
	}
	a.usageAuditMu.Lock()
	if a.usageAuditFlights == nil {
		a.usageAuditFlights = make(map[int]*usageAuditFlight)
	}
	flight, exists := a.usageAuditFlights[id]
	if !exists {
		flight = &usageAuditFlight{done: make(chan struct{}), origin: origin}
		a.usageAuditFlights[id] = flight
	}
	a.usageAuditMu.Unlock()
	if exists {
		select {
		case <-r.Context().Done():
			return usageAuditError("audit_cancelled", "核查已取消", 408)
		case <-flight.done:
		}
		if flight.origin != origin {
			return usageAuditError("audit_source_mismatch", "核查来源已改变，请重试", 409)
		}
		if err := a.recheckUsageAudit(r, id, cfg); err != nil {
			return err
		}
		if flight.err != nil {
			return flight.err
		}
		envelope.Audit = flight.result
		writeJSON(w, 200, envelope)
		return nil
	}
	defer func() {
		a.usageAuditMu.Lock()
		if recovered := recover(); recovered != nil {
			flight.err = usageAuditError("audit_failed", "核查过程中发生内部错误", 500)
			log.Printf("usage model audit panic: %v", recovered)
		}
		delete(a.usageAuditFlights, id)
		close(flight.done)
		a.usageAuditMu.Unlock()
	}()
	// A previous flight may have finished between the initial cache read and
	// registration of this flight. Recheck after winning ownership.
	if !payload.Refresh {
		cached, cacheErr := a.loadUsageModelAudit(r.Context(), id, origin)
		if cacheErr != nil {
			flight.err = cacheErr
			return cacheErr
		}
		if cached != nil && cached.Status == "complete" {
			if err := a.recheckUsageAudit(r, id, cfg); err != nil {
				flight.err = err
				return err
			}
			flight.result = cached
			envelope.Audit = cached
			writeJSON(w, 200, envelope)
			return nil
		}
	}
	result, auditErr := a.runUsageModelAudit(r, record, cfg, sourceStatus, user.ID)
	flight.result, flight.err = result, auditErr
	if auditErr != nil {
		return auditErr
	}
	envelope.Audit = result
	writeJSON(w, 200, envelope)
	return nil
}

func (a *App) runUsageModelAudit(r *http.Request, record UsageRecord, cfg AppConfig, sourceStatus string, actor int) (*usageModelAuditResult, error) {
	// A shared audit flight must outlive the request that won it. Keep request
	// values for authorization checks, but detach the upstream fetch from client
	// disconnects so waiting super-admin requests are not cancelled together.
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), usageAuditTimeout)
	defer cancel()
	file, hash, err := a.fetchUsageLog(auditContext, cfg, *record.RequestID)
	if err != nil {
		return nil, err
	}
	defer removeUsageLog(file)
	result, err := parseUsageModelAudit(file, record)
	if err != nil {
		return nil, usageAuditError("audit_parse_failed", "日志解析失败", 422)
	}
	result.RecordID, result.CheckedBy = record.ID, actor
	result.CheckedAt = time.Now().In(appTimeLocation)
	result.ParserVersion = usageModelAuditParserVersion
	result.SourceStatus, result.LogSHA256 = sourceStatus, hash
	result.QueueResponseModel = record.ResponseModel
	if err := a.recheckUsageAuditContext(auditContext, record.ID, cfg, actor); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = a.db.ExecContext(auditContext, `INSERT INTO usage_model_audits(usage_record_id, collector_origin, result_json) VALUES(?,?,?) ON CONFLICT(usage_record_id) DO UPDATE SET collector_origin=excluded.collector_origin, result_json=excluded.result_json`, record.ID, usageCollectorOrigin(cfg.Collector), string(raw))
	if err != nil {
		return nil, err
	}
	if err := a.recheckUsageAuditContext(auditContext, record.ID, cfg, actor); err != nil {
		return nil, err
	}
	return &result, nil
}

func removeUsageLog(file *os.File) {
	name := file.Name()
	if err := file.Close(); err != nil {
		log.Printf("CPA request log temporary file close failed: %v", err)
	}
	if err := os.Remove(name); err != nil {
		log.Printf("CPA request log temporary file cleanup failed: %v", err)
	}
}

func (a *App) fetchUsageLog(ctx context.Context, cfg AppConfig, requestID string) (*os.File, string, error) {
	a.usageAuditMu.Lock()
	if a.usageAuditActive >= 4 {
		a.usageAuditMu.Unlock()
		return nil, "", usageAuditError("audit_busy", "日志读取繁忙，请稍后重试", 429)
	}
	a.usageAuditActive++
	a.usageAuditMu.Unlock()
	defer func() { a.usageAuditMu.Lock(); a.usageAuditActive--; a.usageAuditMu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, usageAuditTimeout)
	defer cancel()
	base, err := collectorManagementHTTPURL(cfg.Collector.CLIProxyURL)
	if err != nil {
		return nil, "", usageAuditError("audit_source_invalid", "CPA 管理地址无效", 400)
	}
	if strings.TrimSpace(cfg.Collector.ManagementKey) == "" {
		return nil, "", usageAuditError("audit_upstream_auth", "未配置 CPA 管理密钥", 400)
	}
	target := makeURL(base, "/v0/management/request-log-by-id/"+url.PathEscape(requestID), nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", usageAuditError("audit_source_invalid", "CPA 管理地址无效", 400)
	}
	req.Header = managementHeaders(cfg.Collector.ManagementKey)
	resp, err := httpClient(usageAuditTimeout).Do(req)
	if err != nil {
		return nil, "", usageLogNetworkError(ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Disabled logging does not invalidate files already retained by CPA.
		// Only inspect the switch after the requested file was unavailable.
		resp.Body.Close()
		switchResp, body, switchErr := doJSON(ctx, httpClient(usageAuditTimeout), http.MethodGet, makeURL(base, "/v0/management/request-log", nil), managementHeaders(cfg.Collector.ManagementKey), nil)
		if switchErr != nil {
			// The primary request already established that this log is absent.
			// The switch probe is diagnostic only and must not mask that 404.
			switchResp = nil
		}
		if switchResp != nil && switchResp.StatusCode == 200 {
			var enabled map[string]bool
			if json.Unmarshal(body, &enabled) == nil {
				if value, present := enabled["request-log"]; present && !value {
					return nil, "", usageAuditError("audit_log_disabled", "日志不可用，且 CPA 完整请求日志未开启", 409)
				}
			}
		}
	}
	if err := usageLogHTTPError(resp.StatusCode); err != nil {
		return nil, "", err
	}
	file, err := os.CreateTemp("", "cpa-helper-log-*.log")
	if err != nil {
		return nil, "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, hash), resp.Body); err != nil {
		removeUsageLog(file)
		return nil, "", usageLogNetworkError(ctx)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		removeUsageLog(file)
		return nil, "", err
	}
	return file, hex.EncodeToString(hash.Sum(nil)), nil
}

func usageLogNetworkError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return usageAuditError("audit_timeout", "CPA 日志读取超时", 504)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return usageAuditError("audit_cancelled", "日志读取已取消", 408)
	}
	return usageAuditError("audit_upstream_unavailable", "CPA 日志读取失败，请检查连接", 502)
}

func usageLogHTTPError(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if status == 401 || status == 403 {
		return usageAuditError("audit_upstream_auth", "CPA 管理鉴权失败", 502)
	}
	if status == 404 {
		return usageAuditError("audit_log_unavailable", "日志不可用，可能尚未落盘或已清理", 404)
	}
	return usageAuditError("audit_upstream_unavailable", fmt.Sprintf("CPA 日志读取失败：HTTP %d", status), 502)
}
