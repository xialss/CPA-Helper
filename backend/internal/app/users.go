package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const generatedAPIKeyPrefix = "sk-"
const generatedAPIKeyLength = 52
const generatedAPIKeyAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

type userPayload struct {
	Username         string   `json:"username"`
	Password         *string  `json:"password"`
	IsAdmin          bool     `json:"is_admin"`
	IsSuperAdmin     *bool    `json:"is_super_admin"`
	Nickname         string   `json:"nickname"`
	LifetimeQuotaUSD *float64 `json:"lifetime_quota_usd,omitempty"`
	MonthlyQuotaUSD  *float64 `json:"monthly_quota_usd,omitempty"`
}

type userAPIKeyBindPayload struct {
	APIKey      *string `json:"api_key"`
	APIKeyHash  *string `json:"api_key_hash"`
	Description string  `json:"description"`
}

type apiKeyPayload struct {
	Description string `json:"description"`
}

type UserRecord struct {
	ID                   int
	Username             string
	IsAdmin              bool
	IsSuperAdmin         bool
	Nickname             string
	DisabledAt           *time.Time
	PasswordHash         *string
	PasswordSalt         *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	QuotaLifetimeUSD     *float64
	QuotaMonthlyUSD      *float64
	QuotaStartedAt       *time.Time
	QuotaMonth           string
	QuotaMonthUsedUSD    float64
	QuotaPausedAt        *time.Time
	QuotaPauseReason     *string
	QuotaSyncError       *string
	QuotaUnpricedRecords int
	MustChangePassword   bool
}

type UserAPIKey struct {
	APIKeyHash  string
	UserID      int
	APIKey      *string
	Description string
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

type UserApiKeySummary struct {
	APIKeyHash            string     `json:"api_key_hash"`
	APIKey                *string    `json:"api_key"`
	Description           string     `json:"description"`
	UserID                *int       `json:"user_id"`
	UserName              *string    `json:"user_name"`
	CreatedAt             *time.Time `json:"created_at"`
	UpdatedAt             *time.Time `json:"updated_at"`
	Records               int        `json:"records"`
	SuccessRecords        int        `json:"success_records"`
	FailedRecords         int        `json:"failed_records"`
	TotalTokens           int        `json:"total_tokens"`
	TodayRecords          int        `json:"today_records"`
	TodaySuccessRecords   int        `json:"today_success_records"`
	TodayFailedRecords    int        `json:"today_failed_records"`
	TodayInputTokens      int        `json:"today_input_tokens"`
	TodayOutputTokens     int        `json:"today_output_tokens"`
	TodayCachedTokens     int        `json:"today_cached_tokens"`
	TodayReasoningTokens  int        `json:"today_reasoning_tokens"`
	TodayTotalTokens      int        `json:"today_total_tokens"`
	TodayEstimatedCostUSD float64    `json:"today_estimated_cost_usd"`
	TodayUnpricedRecords  int        `json:"today_unpriced_records"`
	FirstSeenAt           *time.Time `json:"first_seen_at"`
	LastSeenAt            *time.Time `json:"last_seen_at"`
	LastProvider          *string    `json:"last_provider"`
	LastModel             *string    `json:"last_model"`
	Providers             []string   `json:"providers"`
	Models                []string   `json:"models"`
}

type UserSummaryResponse struct {
	ID                    int                     `json:"id"`
	Username              string                  `json:"username"`
	IsAdmin               bool                    `json:"is_admin"`
	IsSuperAdmin          bool                    `json:"is_super_admin"`
	Nickname              string                  `json:"nickname"`
	DisabledAt            *time.Time              `json:"disabled_at"`
	PasswordSet           bool                    `json:"password_set"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	APIKeys               []UserApiKeySummary     `json:"api_keys"`
	KeyCount              int                     `json:"key_count"`
	Records               int                     `json:"records"`
	SuccessRecords        int                     `json:"success_records"`
	FailedRecords         int                     `json:"failed_records"`
	TotalTokens           int                     `json:"total_tokens"`
	TodayRecords          int                     `json:"today_records"`
	TodaySuccessRecords   int                     `json:"today_success_records"`
	TodayFailedRecords    int                     `json:"today_failed_records"`
	TodayInputTokens      int                     `json:"today_input_tokens"`
	TodayOutputTokens     int                     `json:"today_output_tokens"`
	TodayCachedTokens     int                     `json:"today_cached_tokens"`
	TodayReasoningTokens  int                     `json:"today_reasoning_tokens"`
	TodayTotalTokens      int                     `json:"today_total_tokens"`
	TodayEstimatedCostUSD float64                 `json:"today_estimated_cost_usd"`
	TodayUnpricedRecords  int                     `json:"today_unpriced_records"`
	TotalEstimatedCostUSD float64                 `json:"total_estimated_cost_usd"`
	TotalUnpricedRecords  int                     `json:"total_unpriced_records"`
	FirstSeenAt           *time.Time              `json:"first_seen_at"`
	LastSeenAt            *time.Time              `json:"last_seen_at"`
	LastProvider          *string                 `json:"last_provider"`
	LastModel             *string                 `json:"last_model"`
	Providers             []string                `json:"providers"`
	Models                []string                `json:"models"`
	Quota                 UserQuotaStatusResponse `json:"quota"`
}

type userListPageResponse struct {
	Items    []UserSummaryResponse `json:"items"`
	Total    int                   `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) error {
	actor, err := a.adminUser(r.Context(), r)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("page") != "" || r.URL.Query().Get("page_size") != "" {
			page, pageSize, err := parseUserListPagination(r)
			if err != nil {
				return err
			}
			users, total, err := a.listUsersPage(r.Context(), actor, page, pageSize)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, userListPageResponse{Items: users, Total: total, Page: page, PageSize: pageSize})
			return nil
		}
		users, err := a.listUsers(r.Context(), actor)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, users)
		return nil
	case http.MethodPost:
		var payload userPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		user, err := a.createUser(r.Context(), actor, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, user)
		return nil
	default:
		return methodNotAllowed()
	}
}

func parseUserListPagination(r *http.Request) (int, int, error) {
	page, pageSize := 1, 20
	var err error
	if value := r.URL.Query().Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 {
			return 0, 0, validationError("page 必须是正整数")
		}
	}
	if value := r.URL.Query().Get("page_size"); value != "" {
		pageSize, err = strconv.Atoi(value)
		if err != nil || pageSize < 1 || pageSize > 100 {
			return 0, 0, validationError("page_size 必须是 1 到 100 之间的整数")
		}
	}
	return page, pageSize, nil
}

func (a *App) listUsersPage(ctx context.Context, actor *AuthUser, page, pageSize int) ([]UserSummaryResponse, int, error) {
	if err := a.ensureUsersInitialized(ctx); err != nil {
		return nil, 0, err
	}
	var total int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}
	users, err := a.usersPage(ctx, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	if len(users) == 0 {
		return []UserSummaryResponse{}, total, nil
	}
	keys, err := a.keySummaries(ctx)
	if err != nil {
		return nil, 0, err
	}
	keys = visibleAdminKeySummaries(actor, users, keys)
	usage, err := a.userUsageSummaries(ctx, users)
	if err != nil {
		return nil, 0, err
	}
	keysByUser := map[int][]UserApiKeySummary{}
	for _, key := range keys {
		if key.UserID != nil {
			keysByUser[*key.UserID] = append(keysByUser[*key.UserID], key)
		}
	}
	responses := make([]UserSummaryResponse, 0, len(users))
	for _, user := range users {
		var quotaUser UserRecord
		func() {
			unlock := a.lockUserMutation(user.ID)
			defer unlock()
			quotaUser, err = a.ensureQuotaMonth(ctx, user)
		}()
		if err != nil {
			return nil, 0, err
		}
		quota := quotaStatusFromUser(quotaUser)
		responses = append(responses, userSummaryResponse(quotaUser, keysByUser[user.ID], usage[quotaUser.Username], quota))
	}
	return responses, total, nil
}

func (a *App) handleUserByPath(w http.ResponseWriter, r *http.Request) error {
	actor, err := a.adminUser(r.Context(), r)
	if err != nil {
		return err
	}
	parts := splitPath(r.URL.Path, "/api/users/")
	if len(parts) == 1 && parts[0] == "observed-api-keys" {
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		keys, err := a.adminKeySummaries(r.Context(), actor)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, keys)
		return nil
	}
	if len(parts) < 1 {
		return notFoundError("Not Found")
	}
	userID, err := parseIntPath(parts[0])
	if err != nil {
		return err
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodPut:
			var payload userPayload
			if err := decodeJSON(r, &payload); err != nil {
				return err
			}
			if actor.ID == userID && payload.Password != nil && strings.TrimSpace(*payload.Password) != "" {
				return validationError("请使用账户设置修改当前密码")
			}
			user, err := a.updateUser(r.Context(), actor, userID, payload)
			if err != nil {
				return err
			}
			// An administrator may reset their own password from this endpoint.
			// Password changes revoke the previous session version, so issue the
			// replacement cookie in the same response before the client refreshes
			// /auth/me or follows the must-change redirect.
			if actor.ID == userID {
				var sessionVersion int
				if targetErr := a.db.QueryRowContext(r.Context(), `SELECT session_version FROM users WHERE id = ?`, userID).Scan(&sessionVersion); targetErr != nil {
					return targetErr
				}
				cfg, cfgErr := a.loadConfig(r.Context())
				if cfgErr != nil {
					return cfgErr
				}
				if cookieErr := setSessionCookieWithVersion(w, r, userID, cfg.SessionSecret, sessionVersion); cookieErr != nil {
					return cookieErr
				}
			}
			writeJSON(w, http.StatusOK, user)
			return nil
		case http.MethodDelete:
			if err := a.disableUser(r.Context(), actor, userID); err != nil {
				return err
			}
			writeNoContent(w)
			return nil
		default:
			return methodNotAllowed()
		}
	}
	if len(parts) == 2 && parts[1] == "disable" {
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		if err := a.disableUser(r.Context(), actor, userID); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	}
	if len(parts) == 2 && parts[1] == "enable" {
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		if err := a.enableUser(r.Context(), actor, userID); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	}
	if len(parts) == 2 && parts[1] == "quota" {
		if !actor.IsSuperAdmin {
			return forbiddenError("需要超级管理员权限")
		}
		if err := requireMethod(r, http.MethodPut); err != nil {
			return err
		}
		var payload userQuotaPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		status, err := a.updateUserQuota(r.Context(), userID, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, status)
		return nil
	}
	if len(parts) == 2 && parts[1] == "api-keys" {
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		var payload userAPIKeyBindPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		summary, err := a.bindUserAPIKey(r.Context(), actor, userID, payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, summary)
		return nil
	}
	if len(parts) == 3 && parts[1] == "api-keys" {
		if err := requireMethod(r, http.MethodDelete); err != nil {
			return err
		}
		if err := a.unbindUserAPIKey(r.Context(), actor, userID, parts[2]); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	}
	return notFoundError("Not Found")
}

func (a *App) handleCurrentUserAPIKeys(w http.ResponseWriter, r *http.Request) error {
	user, err := a.readyUser(r.Context(), r)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := a.currentUserAPIKeys(r.Context(), user)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, keys)
		return nil
	case http.MethodPost:
		var payload apiKeyPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		summary, err := a.createGeneratedAPIKeyForUser(r.Context(), user.ID, user.Username, payload.Description)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, summary)
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) handleCurrentUserAPIKeyByHash(w http.ResponseWriter, r *http.Request) error {
	user, err := a.readyUser(r.Context(), r)
	if err != nil {
		return err
	}
	apiKeyHash := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/api-keys/"), "/")
	if apiKeyHash == "" {
		return notFoundError("API KEY 不存在")
	}
	switch r.Method {
	case http.MethodPut:
		var payload apiKeyPayload
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		summary, err := a.updateCurrentUserAPIKey(r.Context(), user, apiKeyHash, payload.Description)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, summary)
		return nil
	case http.MethodDelete:
		if err := a.deleteCurrentUserAPIKey(r.Context(), user, apiKeyHash); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	default:
		return methodNotAllowed()
	}
}

func (a *App) listUsers(ctx context.Context, actor *AuthUser) ([]UserSummaryResponse, error) {
	return a.listUsersLocked(ctx, actor)
}

func (a *App) listUsersLocked(ctx context.Context, actor *AuthUser) ([]UserSummaryResponse, error) {
	if err := a.ensureUsersInitialized(ctx); err != nil {
		return nil, err
	}
	users, err := a.allUsers(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := a.keySummaries(ctx)
	if err != nil {
		return nil, err
	}
	keys = visibleAdminKeySummaries(actor, users, keys)
	usage, err := a.userUsageSummaries(ctx, users)
	if err != nil {
		return nil, err
	}
	keysByUser := map[int][]UserApiKeySummary{}
	for _, key := range keys {
		if key.UserID != nil {
			keysByUser[*key.UserID] = append(keysByUser[*key.UserID], key)
		}
	}
	responses := make([]UserSummaryResponse, 0, len(users))
	for _, user := range users {
		// Listing is a read path: month rollover is the only local normalization
		// needed here. Remote CPA synchronization is performed by quota writes,
		// usage ingestion, and the dedicated quota endpoint, avoiding one network
		// round trip per account while rendering the table.
		var quotaUser UserRecord
		func() {
			unlock := a.lockUserMutation(user.ID)
			defer unlock()
			quotaUser, err = a.ensureQuotaMonth(ctx, user)
		}()
		if err != nil {
			return nil, err
		}
		quota := quotaStatusFromUser(quotaUser)
		responses = append(responses, userSummaryResponse(quotaUser, keysByUser[user.ID], usage[quotaUser.Username], quota))
	}
	sort.Slice(responses, func(i, j int) bool { return responses[i].ID < responses[j].ID })
	return responses, nil
}

func (a *App) userSummaryForUser(ctx context.Context, actor *AuthUser, user UserRecord) (UserSummaryResponse, error) {
	keys, err := a.keySummariesForUser(ctx, user.ID)
	if err != nil {
		return UserSummaryResponse{}, err
	}
	keys = visibleAdminKeySummaries(actor, []UserRecord{user}, keys)
	usage, err := a.userUsageSummaries(ctx, []UserRecord{user})
	if err != nil {
		return UserSummaryResponse{}, err
	}
	quota, err := a.userQuotaStatusLocked(ctx, user.ID)
	if err != nil {
		return UserSummaryResponse{}, err
	}
	return userSummaryResponse(user, keys, usage[user.Username], quota), nil
}

func (a *App) adminKeySummaries(ctx context.Context, actor *AuthUser) ([]UserApiKeySummary, error) {
	users, err := a.allUsers(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := a.keySummaries(ctx)
	if err != nil {
		return nil, err
	}
	return visibleAdminKeySummaries(actor, users, keys), nil
}

func visibleAdminKeySummaries(actor *AuthUser, users []UserRecord, keys []UserApiKeySummary) []UserApiKeySummary {
	nonSuperAdminUserIDs := make(map[int]bool, len(users))
	for _, user := range users {
		nonSuperAdminUserIDs[user.ID] = !user.IsSuperAdmin
	}
	visible := make([]UserApiKeySummary, 0, len(keys))
	for _, key := range keys {
		if key.UserID == nil || (key.UserID != nil && !nonSuperAdminUserIDs[*key.UserID] && !isSuperAdminActor(actor)) {
			continue
		}
		// Administrative list endpoints never expose plaintext credentials.
		key.APIKey = nil
		visible = append(visible, key)
	}
	return visible
}

func (a *App) createUser(ctx context.Context, actor *AuthUser, payload userPayload) (UserSummaryResponse, error) {
	a.userMutationMu.Lock()
	defer a.userMutationMu.Unlock()

	if err := a.ensureUsersInitialized(ctx); err != nil {
		return UserSummaryResponse{}, err
	}
	username := strings.TrimSpace(payload.Username)
	nickname := strings.TrimSpace(payload.Nickname)
	if username == "" || nickname == "" {
		return UserSummaryResponse{}, validationError("账号和昵称不能为空")
	}
	if payload.Password == nil || strings.TrimSpace(*payload.Password) == "" {
		return UserSummaryResponse{}, validationError("密码不能为空")
	}
	if err := validatePasswordBaseline(*payload.Password); err != nil {
		return UserSummaryResponse{}, err
	}
	if err := a.ensureUsernameAvailable(ctx, username, nil); err != nil {
		return UserSummaryResponse{}, err
	}
	isSuperAdmin := payload.IsSuperAdmin != nil && *payload.IsSuperAdmin
	if isSuperAdmin && !isSuperAdminActor(actor) {
		return UserSummaryResponse{}, forbiddenError("需要超级管理员权限才能设置超级管理员")
	}
	isAdmin := payload.IsAdmin || isSuperAdmin
	if !isSuperAdminActor(actor) && (payload.LifetimeQuotaUSD != nil || payload.MonthlyQuotaUSD != nil) {
		return UserSummaryResponse{}, forbiddenError("需要超级管理员权限才能设置用户额度")
	}
	if isAdmin && !isSuperAdminActor(actor) {
		return UserSummaryResponse{}, forbiddenError("需要超级管理员权限才能创建管理员")
	}
	salt, err := createSalt()
	if err != nil {
		return UserSummaryResponse{}, err
	}
	now := dbTime(time.Now())

	var result sql.Result
	if !isSuperAdminActor(actor) {
		// Ordinary administrators may manage account identities, but cannot mint
		// an account with an implicit unlimited budget. New accounts they create
		// start exhausted and remain paused until a super administrator assigns a
		// quota through the dedicated quota endpoint.
		currentMonth := quotaMonth(time.Now())
		result, err = a.db.ExecContext(ctx, `
			INSERT INTO users (username, password_hash, password_salt, is_admin, is_super_admin, nickname,
			                   quota_lifetime_usd, quota_monthly_usd, quota_started_at, quota_month, quota_month_used_usd,
			                   quota_paused_at, quota_pause_reason, must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, ?, 0, ?, ?, 1, ?, ?)
		`, username, hashPassword(*payload.Password, salt), salt, isAdmin, isSuperAdmin, nickname, now, currentMonth, now, quotaPauseReasonExhausted, now, now)
	} else if payload.LifetimeQuotaUSD != nil || payload.MonthlyQuotaUSD != nil {
		currentMonth := quotaMonth(time.Now())
		lifetime, normErr := normalizedQuotaAmount(payload.LifetimeQuotaUSD)
		if normErr != nil {
			return UserSummaryResponse{}, normErr
		}
		monthly, normErr := normalizedQuotaAmount(payload.MonthlyQuotaUSD)
		if normErr != nil {
			return UserSummaryResponse{}, normErr
		}
		result, err = a.db.ExecContext(ctx, `
			INSERT INTO users (username, password_hash, password_salt, is_admin, is_super_admin, nickname,
			                   quota_lifetime_usd, quota_monthly_usd, quota_started_at, quota_month, quota_month_used_usd,
			                   must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, ?, ?)
		`, username, hashPassword(*payload.Password, salt), salt, isAdmin, isSuperAdmin, nickname, quotaAmountArg(lifetime), quotaAmountArg(monthly), now, currentMonth, now, now)
	} else {
		result, err = a.db.ExecContext(ctx, `
			INSERT INTO users (username, password_hash, password_salt, is_admin, is_super_admin, nickname, must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, username, hashPassword(*payload.Password, salt), salt, isAdmin, isSuperAdmin, nickname, now, now)
	}
	if err != nil {
		return UserSummaryResponse{}, err
	}
	id, _ := result.LastInsertId()
	user, err := a.getUser(ctx, int(id))
	if err != nil {
		return UserSummaryResponse{}, err
	}
	return userSummaryResponse(user, nil, emptyUserUsageSummary(), quotaStatusFromUser(user)), nil
}

func isSuperAdminActor(actor *AuthUser) bool {
	return actor != nil && actor.IsSuperAdmin
}

func requireSuperAdminTargetAccess(actor *AuthUser, target UserRecord) error {
	if (target.IsSuperAdmin || target.IsAdmin) && !isSuperAdminActor(actor) {
		if actor == nil || actor.ID != target.ID {
			return forbiddenError("普通管理员不能操作管理员账号")
		}
	}
	return nil
}

func (a *App) userMutationGuardError(ctx context.Context, actor *AuthUser, id int, desiredSuperAdmin bool) error {
	current, err := a.getUser(ctx, id)
	if err != nil {
		return err
	}
	if err := requireSuperAdminTargetAccess(actor, current); err != nil {
		return err
	}
	if current.IsSuperAdmin && !desiredSuperAdmin && current.DisabledAt == nil {
		return conflictError("至少需要保留一个超级管理员")
	}
	return conflictError("用户状态已变化，请刷新后重试")
}

func (a *App) updateUser(ctx context.Context, actor *AuthUser, id int, payload userPayload) (UserSummaryResponse, error) {
	defer a.lockUserMutation(id)()
	if payload.LifetimeQuotaUSD != nil || payload.MonthlyQuotaUSD != nil {
		return UserSummaryResponse{}, validationError("用户额度请使用额度接口修改")
	}

	user, err := a.getUser(ctx, id)
	if err != nil {
		return UserSummaryResponse{}, err
	}
	if err := requireSuperAdminTargetAccess(actor, user); err != nil {
		return UserSummaryResponse{}, err
	}
	username := strings.TrimSpace(payload.Username)
	if username != user.Username {
		return UserSummaryResponse{}, conflictError("账号不允许修改")
	}
	desiredSuperAdmin := user.IsSuperAdmin
	if payload.IsSuperAdmin != nil {
		desiredSuperAdmin = *payload.IsSuperAdmin
	}
	if desiredSuperAdmin != user.IsSuperAdmin && (actor == nil || !actor.IsSuperAdmin) {
		return UserSummaryResponse{}, forbiddenError("需要超级管理员权限才能修改超级管理员角色")
	}
	desiredAdmin := payload.IsAdmin || desiredSuperAdmin
	if desiredAdmin != user.IsAdmin && (actor == nil || !actor.IsSuperAdmin) {
		return UserSummaryResponse{}, forbiddenError("需要超级管理员权限才能修改管理员角色")
	}
	firstID, err := a.firstActiveUserID(ctx)
	if err != nil {
		return UserSummaryResponse{}, err
	}
	if firstID != nil && user.ID == *firstID && !desiredAdmin {
		return UserSummaryResponse{}, conflictError("第一个管理员账号不能取消管理员权限")
	}
	nickname := strings.TrimSpace(payload.Nickname)
	if nickname == "" {
		return UserSummaryResponse{}, validationError("昵称不能为空")
	}
	if payload.Password != nil && strings.TrimSpace(*payload.Password) != "" {
		if (user.IsAdmin || user.IsSuperAdmin) && !isSuperAdminActor(actor) {
			return UserSummaryResponse{}, forbiddenError("普通管理员不能重置管理员密码")
		}
		if err := validatePasswordBaseline(*payload.Password); err != nil {
			return UserSummaryResponse{}, err
		}
	}
	var result sql.Result
	actorID := 0
	if actor != nil {
		actorID = actor.ID
	}
	if payload.Password != nil && strings.TrimSpace(*payload.Password) != "" {
		salt, err := createSalt()
		if err != nil {
			return UserSummaryResponse{}, err
		}
		result, err = a.db.ExecContext(ctx, `
			UPDATE users
			SET nickname = ?, is_admin = ?, is_super_admin = ?, password_hash = ?, password_salt = ?, must_change_password = CASE WHEN ? = id THEN 0 ELSE 1 END, session_version = session_version + 1, updated_at = ?
			WHERE id = ?
			  AND (? = 1 OR is_super_admin = 0)
			  AND (
				is_super_admin = 0
				OR disabled_at IS NOT NULL
				OR ? = 1
				OR EXISTS (
					SELECT 1 FROM users AS other
					WHERE other.is_super_admin = 1 AND other.disabled_at IS NULL AND other.id <> ?
				)
			  )
		`, nickname, desiredAdmin, desiredSuperAdmin, hashPassword(*payload.Password, salt), salt, actorID, dbTime(time.Now()), id, isSuperAdminActor(actor), desiredSuperAdmin, id)
		if err != nil {
			return UserSummaryResponse{}, err
		}
	} else {
		result, err = a.db.ExecContext(ctx, `
			UPDATE users
			SET nickname = ?, is_admin = ?, is_super_admin = ?, session_version = CASE WHEN is_admin <> ? OR is_super_admin <> ? THEN session_version + 1 ELSE session_version END, updated_at = ?
			WHERE id = ?
			  AND (? = 1 OR is_super_admin = 0)
			  AND (
				is_super_admin = 0
				OR disabled_at IS NOT NULL
				OR ? = 1
				OR EXISTS (
					SELECT 1 FROM users AS other
					WHERE other.is_super_admin = 1 AND other.disabled_at IS NULL AND other.id <> ?
				)
			  )
		`, nickname, desiredAdmin, desiredSuperAdmin, desiredAdmin, desiredSuperAdmin, dbTime(time.Now()), id, isSuperAdminActor(actor), desiredSuperAdmin, id)
		if err != nil {
			return UserSummaryResponse{}, err
		}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return UserSummaryResponse{}, err
	}
	if affected != 1 {
		return UserSummaryResponse{}, a.userMutationGuardError(ctx, actor, id, desiredSuperAdmin)
	}
	updated, err := a.getUser(ctx, id)
	if err != nil {
		return UserSummaryResponse{}, err
	}
	return a.userSummaryForUser(ctx, actor, updated)
}

func (a *App) disableUser(ctx context.Context, actor *AuthUser, id int) error {
	defer a.lockUserMutation(id)()

	user, err := a.getUser(ctx, id)
	if err != nil {
		return err
	}
	if err := requireSuperAdminTargetAccess(actor, user); err != nil {
		return err
	}
	firstID, err := a.firstActiveUserID(ctx)
	if err != nil {
		return err
	}
	if firstID != nil && user.ID == *firstID {
		return conflictError("第一个管理员账号不能禁用")
	}
	if actor != nil && actor.ID == user.ID {
		return conflictError("不能禁用当前登录账号")
	}
	if user.DisabledAt != nil {
		return nil
	}
	keys, err := a.userAPIKeys(ctx, id)
	if err != nil {
		return err
	}
	disabledAt := dbTime(time.Now())
	result, err := a.db.ExecContext(ctx, `
		UPDATE users
		SET disabled_at = ?, session_version = session_version + 1, updated_at = ?
		WHERE id = ? AND disabled_at IS NULL
		  AND (? = 1 OR is_super_admin = 0)
		  AND (
			is_super_admin = 0
			OR EXISTS (
				SELECT 1 FROM users AS other
				WHERE other.is_super_admin = 1 AND other.disabled_at IS NULL AND other.id <> ?
			)
		  )
	`, disabledAt, disabledAt, id, isSuperAdminActor(actor), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		current, err := a.getUser(ctx, id)
		if err != nil {
			return err
		}
		if current.DisabledAt != nil {
			return nil
		}
		return a.userMutationGuardError(ctx, actor, id, false)
	}
	removed := make([]UserAPIKey, 0, len(keys))
	for _, key := range keys {
		changed, syncErr := a.removeRemoteAPIKeyHashWithChange(ctx, key.APIKeyHash)
		if changed {
			removed = append(removed, key)
		}
		if syncErr != nil {
			compensationCtx := context.WithoutCancel(ctx)
			compensationErr := a.restoreRemovedUserAPIKeys(compensationCtx, removed)
			rollback, rollbackErr := a.db.ExecContext(compensationCtx, `UPDATE users SET disabled_at = NULL, updated_at = ? WHERE id = ? AND disabled_at = ?`, dbTime(time.Now()), id, disabledAt)
			if rollbackErr == nil {
				var rolledBack int64
				rolledBack, rollbackErr = rollback.RowsAffected()
				if rollbackErr == nil && rolledBack != 1 {
					rollbackErr = conflictError("禁用用户时用户状态已变化")
				}
			}
			combinedErr := combineUserMutationErrors(syncErr, compensationErr)
			combinedErr = combineUserMutationErrors(combinedErr, rollbackErr)
			if compensationErr != nil || rollbackErr != nil {
				if persistErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); persistErr != nil {
					combinedErr = combineUserMutationErrors(combinedErr, persistErr)
				}
			}
			return combinedErr
		}
	}
	return nil
}

func (a *App) enableUser(ctx context.Context, actor *AuthUser, id int) error {
	defer a.lockUserMutation(id)()

	user, err := a.getUser(ctx, id)
	if err != nil {
		return err
	}
	if err := requireSuperAdminTargetAccess(actor, user); err != nil {
		return err
	}
	if user.DisabledAt == nil {
		return nil
	}
	user, err = a.ensureQuotaMonth(ctx, user)
	if err != nil {
		return err
	}
	if !quotaHasAvailable(user) {
		return conflictError("用户额度已用尽，请补充额度后再恢复 API KEY")
	}
	keys, err := a.userAPIKeys(ctx, id)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.APIKey == nil {
			return conflictError("存在无法恢复的 API KEY，请重新绑定后再启用")
		}
	}
	restored := []string{}
	for _, key := range keys {
		if key.APIKey == nil {
			continue
		}
		changed, syncErr := a.addRemoteAPIKeyWithChange(ctx, *key.APIKey)
		if changed {
			restored = append(restored, key.APIKeyHash)
		}
		if syncErr != nil {
			compensationCtx := context.WithoutCancel(ctx)
			compensationErr := a.removeRestoredUserAPIKeys(compensationCtx, restored)
			combinedErr := combineUserMutationErrors(syncErr, compensationErr)
			if persistErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); persistErr != nil {
				combinedErr = combineUserMutationErrors(combinedErr, persistErr)
			}
			return combinedErr
		}
	}
	result, err := a.db.ExecContext(ctx, `
		UPDATE users
		SET disabled_at = NULL, session_version = session_version + 1, quota_paused_at = NULL, quota_pause_reason = NULL, quota_sync_error = NULL, updated_at = ?
		WHERE id = ? AND disabled_at IS NOT NULL AND (? = 1 OR is_super_admin = 0)
	`, dbTime(time.Now()), id, isSuperAdminActor(actor))
	if err != nil {
		compensationCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(compensationCtx, restored)
		combinedErr := combineUserMutationErrors(err, compensationErr)
		if persistErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	affected, err := result.RowsAffected()
	if err != nil {
		compensationCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(compensationCtx, restored)
		combinedErr := combineUserMutationErrors(err, compensationErr)
		if persistErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	if affected == 1 {
		return nil
	}
	current, err := a.getUser(ctx, id)
	if err != nil {
		compensationCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(compensationCtx, restored)
		combinedErr := combineUserMutationErrors(err, compensationErr)
		if persistErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	if current.DisabledAt == nil {
		return nil
	}
	compensationCtx := context.WithoutCancel(ctx)
	compensationErr := a.removeRestoredUserAPIKeys(compensationCtx, restored)
	guardErr := a.userMutationGuardError(ctx, actor, id, true)
	combinedErr := combineUserMutationErrors(guardErr, compensationErr)
	if syncErr := a.setQuotaSyncError(compensationCtx, id, combinedErr); syncErr != nil {
		combinedErr = combineUserMutationErrors(combinedErr, syncErr)
	}
	return combinedErr
}

func combineUserMutationErrors(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w (补偿同步失败：%v)", primary, secondary)
}

func (a *App) restoreRemovedUserAPIKeys(ctx context.Context, keys []UserAPIKey) error {
	ctx = context.WithoutCancel(ctx)
	var combinedErr error
	for index := len(keys) - 1; index >= 0; index-- {
		key := keys[index]
		if key.APIKey == nil {
			combinedErr = combineUserMutationErrors(combinedErr, conflictError("缺少 API KEY 明文，无法恢复远端绑定"))
			continue
		}
		if err := a.addRemoteAPIKey(ctx, *key.APIKey); err != nil {
			combinedErr = combineUserMutationErrors(combinedErr, err)
		}
	}
	return combinedErr
}

func (a *App) removeRestoredUserAPIKeys(ctx context.Context, hashes []string) error {
	ctx = context.WithoutCancel(ctx)
	var combinedErr error
	for index := len(hashes) - 1; index >= 0; index-- {
		if err := a.removeRemoteAPIKeyHash(ctx, hashes[index]); err != nil {
			combinedErr = combineUserMutationErrors(combinedErr, err)
		}
	}
	return combinedErr
}

// lockAPIKeyBindingUsers acquires a stable lock set for a binding operation.
// Ownership is read again after locking; if another binding changed it in the
// meantime, return a conflict instead of making an authorization decision from
// a stale owner snapshot. Locking IDs in ascending order avoids cross-account
// deadlocks when two administrators concurrently swap the same credentials.
func (a *App) lockAPIKeyBindingUsers(ctx context.Context, userID int, apiKeyHash string) (func(), error) {
	var ownerID int
	err := a.db.QueryRowContext(ctx, `SELECT user_id FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash).Scan(&ownerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		ownerID = 0
	}
	ids := []int{userID}
	if ownerID > 0 && ownerID != userID {
		ids = append(ids, ownerID)
	}
	sort.Ints(ids)
	unlocks := make([]func(), 0, len(ids))
	for _, id := range ids {
		unlocks = append(unlocks, a.lockUserMutation(id))
	}
	unlock := func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
	}
	var currentOwnerID int
	err = a.db.QueryRowContext(ctx, `SELECT user_id FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash).Scan(&currentOwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		currentOwnerID = 0
		err = nil
	}
	if err != nil {
		unlock()
		return nil, err
	}
	if currentOwnerID != ownerID {
		unlock()
		return nil, conflictError("API KEY 所属用户状态已变化，请刷新后重试")
	}
	return unlock, nil
}

func (a *App) bindUserAPIKey(ctx context.Context, actor *AuthUser, userID int, payload userAPIKeyBindPayload) (UserApiKeySummary, error) {
	description := strings.TrimSpace(payload.Description)
	if description == "" {
		return UserApiKeySummary{}, validationError("API KEY 描述不能为空")
	}
	var apiKeyHash string
	if payload.APIKeyHash != nil {
		apiKeyHash = strings.TrimSpace(*payload.APIKeyHash)
	}
	var apiKey *string
	if payload.APIKey != nil {
		normalized := strings.TrimSpace(*payload.APIKey)
		if normalized == "" {
			return UserApiKeySummary{}, validationError("API KEY 不能为空")
		}
		apiKey = &normalized
		calculated := hashAPIKey(normalized)
		if apiKeyHash != "" && calculated != apiKeyHash {
			return UserApiKeySummary{}, conflictError("API KEY 与 API KEY 标识不匹配")
		}
		apiKeyHash = calculated
	}
	if apiKeyHash == "" {
		return UserApiKeySummary{}, validationError("API KEY 或 API KEY 标识不能为空")
	}

	unlock, err := a.lockAPIKeyBindingUsers(ctx, userID, apiKeyHash)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	defer unlock()

	target, err := a.getUser(ctx, userID)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	if err := requireSuperAdminTargetAccess(actor, target); err != nil {
		return UserApiKeySummary{}, err
	}
	user, err := a.getActiveUser(ctx, userID)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	if err := a.ensureUserQuotaReadyForKeys(ctx, user.ID); err != nil {
		return UserApiKeySummary{}, err
	}

	if err := a.requireExistingAPIKeyOwnerAccess(ctx, actor, apiKeyHash, user.ID, apiKey != nil); err != nil {
		return UserApiKeySummary{}, err
	}
	if apiKey == nil {
		existing, err := a.getAPIKey(ctx, apiKeyHash)
		if err != nil {
			return UserApiKeySummary{}, notFoundError("未找到完整 API KEY，请粘贴原始 API KEY")
		}
		apiKey = existing.APIKey
	}
	if apiKey == nil {
		return UserApiKeySummary{}, notFoundError("未找到完整 API KEY，请粘贴原始 API KEY")
	}
	changed, err := a.addRemoteAPIKeyWithChange(ctx, *apiKey)
	if err != nil {
		if changed {
			compensationErr := a.removeRemoteAPIKeyHash(context.WithoutCancel(ctx), apiKeyHash)
			if compensationErr != nil {
				err = combineUserMutationErrors(err, compensationErr)
			}
		}
		return UserApiKeySummary{}, err
	}
	if err := a.upsertUserAPIKey(ctx, user.ID, apiKeyHash, *apiKey, description); err != nil {
		var compensationErr error
		if changed {
			compensationErr = a.removeRemoteAPIKeyHash(context.WithoutCancel(ctx), apiKeyHash)
		}
		return UserApiKeySummary{}, combineUserMutationErrors(err, compensationErr)
	}
	summary, err := a.keySummaryByHash(ctx, apiKeyHash, nil)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	if payload.APIKey == nil || !isSuperAdminActor(actor) {
		// A regular administrator may bind a key to an ordinary account, but the
		// mutation response must not become an alternate plaintext-key read path.
		summary.APIKey = nil
	}
	return summary, nil
}

func (a *App) requireExistingAPIKeyOwnerAccess(ctx context.Context, actor *AuthUser, apiKeyHash string, targetUserID int, providedPlaintext bool) error {
	var ownerID int
	err := a.db.QueryRowContext(ctx, `SELECT user_id FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if ownerID == targetUserID {
		return nil
	}
	owner, err := a.getUser(ctx, ownerID)
	if err != nil {
		return err
	}
	if err := requireSuperAdminTargetAccess(actor, owner); err != nil {
		return err
	}
	if !isSuperAdminActor(actor) && !providedPlaintext {
		return forbiddenError("跨账户改绑 API KEY 需要提供完整密钥或超级管理员权限")
	}
	return nil
}

func (a *App) unbindUserAPIKey(ctx context.Context, actor *AuthUser, userID int, apiKeyHash string) error {
	defer a.lockUserMutation(userID)()

	target, err := a.getUser(ctx, userID)
	if err != nil {
		return err
	}
	if err := requireSuperAdminTargetAccess(actor, target); err != nil {
		return err
	}

	key, err := a.getAPIKey(ctx, apiKeyHash)
	if err != nil {
		return err
	}
	if key.UserID != userID {
		return notFoundError("API KEY 绑定不存在")
	}

	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return err
	}

	var changed bool
	if strings.TrimSpace(cfg.Collector.ManagementKey) != "" {
		if key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "" {
			return conflictError("当前 API KEY 缺少完整密钥，无法安全解绑")
		}
		changed, err = a.removeRemoteAPIKeyHashWithChange(ctx, apiKeyHash)
		if err != nil {
			var compensationErr error
			if changed && key.APIKey != nil && strings.TrimSpace(*key.APIKey) != "" {
				compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
			}
			return combineUserMutationErrors(err, compensationErr)
		}
	}

	result, err := a.db.ExecContext(ctx, `DELETE FROM user_api_keys WHERE user_id = ? AND api_key_hash = ?`, userID, apiKeyHash)
	if err != nil {
		var compensationErr error
		if changed && key.APIKey != nil && strings.TrimSpace(*key.APIKey) != "" {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(err, compensationErr)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		var compensationErr error
		if changed && key.APIKey != nil && strings.TrimSpace(*key.APIKey) != "" {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(err, compensationErr)
	}
	if affected == 0 {
		var compensationErr error
		if changed && key.APIKey != nil && strings.TrimSpace(*key.APIKey) != "" {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(notFoundError("API KEY 绑定不存在"), compensationErr)
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ?`, dbTime(time.Now()), userID)
	return nil
}

func (a *App) currentUserAPIKeys(ctx context.Context, user *AuthUser) ([]UserApiKeySummary, error) {
	keys, err := a.keySummaries(ctx)
	if err != nil {
		return nil, err
	}
	var result []UserApiKeySummary
	for _, key := range keys {
		if key.UserID != nil && *key.UserID == user.ID {
			name := user.Username
			key.UserName = &name
			result = append(result, key)
		}
	}
	return result, nil
}

func (a *App) createGeneratedAPIKeyForUser(ctx context.Context, userID int, username, description string) (UserApiKeySummary, error) {
	defer a.lockUserMutation(userID)()

	description = strings.TrimSpace(description)
	if description == "" {
		return UserApiKeySummary{}, validationError("API KEY 描述不能为空")
	}
	user, err := a.getActiveUser(ctx, userID)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	if err := a.ensureUserQuotaReadyForKeys(ctx, user.ID); err != nil {
		return UserApiKeySummary{}, err
	}
	apiKey, err := a.generateUniqueAPIKey(ctx)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	if err := a.addRemoteAPIKey(ctx, apiKey); err != nil {
		return UserApiKeySummary{}, err
	}
	apiKeyHash := hashAPIKey(apiKey)
	if err := a.upsertUserAPIKey(ctx, user.ID, apiKeyHash, apiKey, description); err != nil {
		compensationErr := a.removeRemoteAPIKeyHash(context.WithoutCancel(ctx), apiKeyHash)
		return UserApiKeySummary{}, combineUserMutationErrors(err, compensationErr)
	}
	summary, err := a.keySummaryByHash(ctx, apiKeyHash, &apiKey)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	summary.UserName = &username
	return summary, nil
}

func (a *App) updateCurrentUserAPIKey(ctx context.Context, user *AuthUser, apiKeyHash, description string) (UserApiKeySummary, error) {
	defer a.lockUserMutation(user.ID)()

	description = strings.TrimSpace(description)
	if description == "" {
		return UserApiKeySummary{}, validationError("API KEY 描述不能为空")
	}
	result, err := a.db.ExecContext(ctx, `UPDATE user_api_keys SET description = ?, updated_at = ? WHERE user_id = ? AND api_key_hash = ?`, description, dbTime(time.Now()), user.ID, apiKeyHash)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return UserApiKeySummary{}, notFoundError("API KEY 不存在")
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ?`, dbTime(time.Now()), user.ID)
	summary, err := a.keySummaryByHash(ctx, apiKeyHash, nil)
	if err != nil {
		return UserApiKeySummary{}, err
	}
	name := user.Username
	summary.UserName = &name
	return summary, nil
}

func (a *App) deleteCurrentUserAPIKey(ctx context.Context, user *AuthUser, apiKeyHash string) error {
	defer a.lockUserMutation(user.ID)()

	key, err := a.getAPIKey(ctx, apiKeyHash)
	if err != nil {
		return err
	}
	if key.UserID != user.ID {
		// Keep the endpoint non-enumerable across accounts.
		return notFoundError("API KEY 不存在")
	}
	if key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "" {
		return conflictError("当前 API KEY 缺少完整密钥，无法安全删除")
	}

	changed, err := a.removeRemoteAPIKeyHashWithChange(ctx, apiKeyHash)
	if err != nil {
		// A failed PUT can still have been applied remotely. Restore the
		// original credential before returning so the local binding remains a
		// truthful representation of CPA state.
		var compensationErr error
		if changed {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(err, compensationErr)
	}

	result, err := a.db.ExecContext(ctx, `DELETE FROM user_api_keys WHERE user_id = ? AND api_key_hash = ?`, user.ID, apiKeyHash)
	if err != nil {
		var compensationErr error
		if changed {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(err, compensationErr)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		var compensationErr error
		if changed {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(err, compensationErr)
	}
	if affected != 1 {
		var compensationErr error
		if changed {
			compensationErr = a.addRemoteAPIKey(context.WithoutCancel(ctx), *key.APIKey)
		}
		return combineUserMutationErrors(notFoundError("API KEY 不存在"), compensationErr)
	}
	return nil
}

func (a *App) upsertUserAPIKey(ctx context.Context, userID int, apiKeyHash, apiKey, description string) error {
	now := dbTime(time.Now())
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(api_key_hash) DO UPDATE SET user_id = excluded.user_id,
			api_key = excluded.api_key, description = excluded.description, updated_at = excluded.updated_at
	`, apiKeyHash, userID, apiKey, description, now, now)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ?`, now, userID)
	return err
}

func (a *App) generateUniqueAPIKey(ctx context.Context) (string, error) {
	for i := 0; i < 10; i++ {
		var builder strings.Builder
		builder.WriteString(generatedAPIKeyPrefix)
		for j := 0; j < generatedAPIKeyLength; j++ {
			index, err := rand.Int(rand.Reader, big.NewInt(int64(len(generatedAPIKeyAlphabet))))
			if err != nil {
				return "", err
			}
			builder.WriteByte(generatedAPIKeyAlphabet[index.Int64()])
		}
		apiKey := builder.String()
		var count int
		if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_api_keys WHERE api_key_hash = ?`, hashAPIKey(apiKey)).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return apiKey, nil
		}
	}
	return "", conflictError("生成 API KEY 失败，请重试")
}

const userSelectColumns = `id, username, is_admin, is_super_admin, nickname, CAST(disabled_at AS TEXT), password_hash, password_salt,
	CAST(created_at AS TEXT), CAST(updated_at AS TEXT), quota_lifetime_usd, quota_monthly_usd,
	CAST(quota_started_at AS TEXT), quota_month, quota_month_used_usd, CAST(quota_paused_at AS TEXT),
	quota_pause_reason, quota_sync_error, quota_unpriced_records, must_change_password`

func (a *App) allUsers(ctx context.Context) ([]UserRecord, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+userSelectColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []UserRecord
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (a *App) usersPage(ctx context.Context, page, pageSize int) ([]UserRecord, error) {
	offset := (page - 1) * pageSize
	rows, err := a.db.QueryContext(ctx, `SELECT `+userSelectColumns+` FROM users ORDER BY id LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]UserRecord, 0, pageSize)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(scanner userScanner) (UserRecord, error) {
	var user UserRecord
	var disabledAt, passwordHash, passwordSalt, createdAt, updatedAt, quotaStartedAt, quotaPausedAt, quotaPauseReason, quotaSyncError sql.NullString
	var quotaLifetime, quotaMonthly, quotaMonthUsed sql.NullFloat64
	var quotaUnpriced sql.NullInt64
	err := scanner.Scan(
		&user.ID, &user.Username, &user.IsAdmin, &user.IsSuperAdmin, &user.Nickname, &disabledAt,
		&passwordHash, &passwordSalt, &createdAt, &updatedAt, &quotaLifetime,
		&quotaMonthly, &quotaStartedAt, &user.QuotaMonth, &quotaMonthUsed,
		&quotaPausedAt, &quotaPauseReason, &quotaSyncError, &quotaUnpriced,
		&user.MustChangePassword,
	)
	if err != nil {
		return UserRecord{}, err
	}
	user.DisabledAt = timePtr(disabledAt)
	user.PasswordHash = nullableString(passwordHash)
	user.PasswordSalt = nullableString(passwordSalt)
	user.QuotaLifetimeUSD = nullableFloat(quotaLifetime)
	user.QuotaMonthlyUSD = nullableFloat(quotaMonthly)
	user.QuotaStartedAt = timePtr(quotaStartedAt)
	if quotaMonthUsed.Valid {
		user.QuotaMonthUsedUSD = quotaMonthUsed.Float64
	}
	user.QuotaPausedAt = timePtr(quotaPausedAt)
	user.QuotaPauseReason = nullableString(quotaPauseReason)
	user.QuotaSyncError = nullableString(quotaSyncError)
	if quotaUnpriced.Valid {
		user.QuotaUnpricedRecords = int(quotaUnpriced.Int64)
	}
	if parsed, ok := parseDBTime(createdAt.String); ok {
		user.CreatedAt = parsed
	}
	if parsed, ok := parseDBTime(updatedAt.String); ok {
		user.UpdatedAt = parsed
	}
	return user, nil
}

func (a *App) getUser(ctx context.Context, id int) (UserRecord, error) {
	row := a.db.QueryRowContext(ctx, `SELECT `+userSelectColumns+` FROM users WHERE id = ?`, id)
	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserRecord{}, notFoundError("用户不存在")
		}
		return UserRecord{}, err
	}
	return user, nil
}

func (a *App) userByUsername(ctx context.Context, username string) (UserRecord, error) {
	row := a.db.QueryRowContext(ctx, `SELECT `+userSelectColumns+` FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (a *App) getActiveUser(ctx context.Context, id int) (UserRecord, error) {
	user, err := a.getUser(ctx, id)
	if err != nil {
		return UserRecord{}, err
	}
	if user.DisabledAt != nil {
		return UserRecord{}, conflictError("用户已禁用")
	}
	return user, nil
}

func (a *App) ensureUsernameAvailable(ctx context.Context, username string, exceptID *int) error {
	var id int
	err := a.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if exceptID == nil || id != *exceptID {
		return conflictError("账号已存在")
	}
	return nil
}

func (a *App) userAPIKeys(ctx context.Context, userID int) ([]UserAPIKey, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT api_key_hash, user_id, api_key, description, CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM user_api_keys WHERE user_id = ? ORDER BY created_at, api_key_hash`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAPIKeys(rows)
}

func (a *App) getAPIKey(ctx context.Context, apiKeyHash string) (UserAPIKey, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT api_key_hash, user_id, api_key, description, CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM user_api_keys WHERE api_key_hash = ?`, apiKeyHash)
	if err != nil {
		return UserAPIKey{}, err
	}
	defer rows.Close()
	keys, err := scanAPIKeys(rows)
	if err != nil {
		return UserAPIKey{}, err
	}
	if len(keys) == 0 {
		return UserAPIKey{}, notFoundError("API KEY 不存在")
	}
	return keys[0], nil
}

func scanAPIKeys(rows *sql.Rows) ([]UserAPIKey, error) {
	var keys []UserAPIKey
	for rows.Next() {
		var key UserAPIKey
		var apiKey, createdAt, updatedAt sql.NullString
		if err := rows.Scan(&key.APIKeyHash, &key.UserID, &apiKey, &key.Description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		key.APIKey = nullableString(apiKey)
		key.CreatedAt = timePtr(createdAt)
		key.UpdatedAt = timePtr(updatedAt)
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (a *App) keySummaries(ctx context.Context) ([]UserApiKeySummary, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT k.api_key_hash, k.user_id, k.api_key, k.description, CAST(k.created_at AS TEXT), CAST(k.updated_at AS TEXT),
		       u.nickname, u.username
		FROM user_api_keys k
		LEFT JOIN users u ON u.id = k.user_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []UserApiKeySummary
	for rows.Next() {
		var summary UserApiKeySummary
		var apiKey, createdAt, updatedAt, nickname, username sql.NullString
		var userID int
		if err := rows.Scan(&summary.APIKeyHash, &userID, &apiKey, &summary.Description, &createdAt, &updatedAt, &nickname, &username); err != nil {
			return nil, err
		}
		summary.APIKey = nullableString(apiKey)
		summary.CreatedAt = timePtr(createdAt)
		summary.UpdatedAt = timePtr(updatedAt)
		summary.UserID = &userID
		if username.Valid {
			label := strings.TrimSpace(nickname.String)
			if label == "" {
				label = username.String
			}
			summary.UserName = &label
		}
		summary.Providers = []string{}
		summary.Models = []string{}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		left, right := time.Time{}, time.Time{}
		if summaries[i].UpdatedAt != nil {
			left = *summaries[i].UpdatedAt
		}
		if summaries[j].UpdatedAt != nil {
			right = *summaries[j].UpdatedAt
		}
		return left.After(right)
	})
	return summaries, rows.Err()
}

func (a *App) keySummariesForUser(ctx context.Context, userID int) ([]UserApiKeySummary, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT k.api_key_hash, k.user_id, k.api_key, k.description, CAST(k.created_at AS TEXT), CAST(k.updated_at AS TEXT),
		       u.nickname, u.username
		FROM user_api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		WHERE k.user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []UserApiKeySummary
	for rows.Next() {
		var summary UserApiKeySummary
		var apiKey, createdAt, updatedAt, nickname, username sql.NullString
		var uid int
		if err := rows.Scan(&summary.APIKeyHash, &uid, &apiKey, &summary.Description, &createdAt, &updatedAt, &nickname, &username); err != nil {
			return nil, err
		}
		summary.APIKey = nullableString(apiKey)
		summary.CreatedAt = timePtr(createdAt)
		summary.UpdatedAt = timePtr(updatedAt)
		summary.UserID = &uid
		if username.Valid {
			label := strings.TrimSpace(nickname.String)
			if label == "" {
				label = username.String
			}
			summary.UserName = &label
		}
		summary.Providers = []string{}
		summary.Models = []string{}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		left, right := time.Time{}, time.Time{}
		if summaries[i].UpdatedAt != nil {
			left = *summaries[i].UpdatedAt
		}
		if summaries[j].UpdatedAt != nil {
			right = *summaries[j].UpdatedAt
		}
		return left.After(right)
	})
	return summaries, rows.Err()
}

func (a *App) keySummaryByHash(ctx context.Context, apiKeyHash string, fullKey *string) (UserApiKeySummary, error) {
	var summary UserApiKeySummary
	var apiKey, createdAt, updatedAt, nickname, username sql.NullString
	var userID int
	err := a.db.QueryRowContext(ctx, `
		SELECT k.api_key_hash, k.user_id, k.api_key, k.description,
		       CAST(k.created_at AS TEXT), CAST(k.updated_at AS TEXT),
		       u.nickname, u.username
		FROM user_api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		WHERE k.api_key_hash = ?
	`, apiKeyHash).Scan(
		&summary.APIKeyHash, &userID, &apiKey, &summary.Description,
		&createdAt, &updatedAt, &nickname, &username,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return UserApiKeySummary{}, notFoundError("API KEY 不存在")
	}
	if err != nil {
		return UserApiKeySummary{}, err
	}
	summary.APIKey = nullableString(apiKey)
	if fullKey != nil {
		summary.APIKey = fullKey
	}
	summary.CreatedAt = timePtr(createdAt)
	summary.UpdatedAt = timePtr(updatedAt)
	summary.UserID = &userID
	if username.Valid {
		label := strings.TrimSpace(nickname.String)
		if label == "" {
			label = username.String
		}
		summary.UserName = &label
	}
	summary.Providers = []string{}
	summary.Models = []string{}
	return summary, nil
}

type userUsageSummary struct {
	Records               int
	SuccessRecords        int
	FailedRecords         int
	TotalTokens           int
	TodayRecords          int
	TodaySuccessRecords   int
	TodayFailedRecords    int
	TodayInputTokens      int
	TodayOutputTokens     int
	TodayCachedTokens     int
	TodayReasoningTokens  int
	TodayTotalTokens      int
	TodayEstimatedCostUSD float64
	TodayUnpricedRecords  int
	TotalEstimatedCostUSD float64
	TotalUnpricedRecords  int
	FirstSeenAt           *time.Time
	LastSeenAt            *time.Time
	LastProvider          *string
	LastModel             *string
	Providers             []string
	Models                []string
}

func emptyUserUsageSummary() userUsageSummary {
	return userUsageSummary{Providers: []string{}, Models: []string{}}
}

type userUsageSummaryAccumulator struct {
	knownUsers   map[string]struct{}
	createdAt    map[string]time.Time
	values       map[string]userUsageSummary
	providerSeen map[string]map[string]bool
	modelSeen    map[string]map[string]bool
	todayStart   time.Time
	todayEnd     time.Time
}

func newUserUsageSummaryAccumulator(users []UserRecord) *userUsageSummaryAccumulator {
	knownUsers := make(map[string]struct{}, len(users))
	createdAt := make(map[string]time.Time, len(users))
	for _, user := range users {
		knownUsers[user.Username] = struct{}{}
		createdAt[user.Username] = user.CreatedAt
	}
	todayStart, todayEnd := defaultTodayRange()
	return &userUsageSummaryAccumulator{
		knownUsers:   knownUsers,
		createdAt:    createdAt,
		values:       make(map[string]userUsageSummary, len(users)),
		providerSeen: make(map[string]map[string]bool, len(users)),
		modelSeen:    make(map[string]map[string]bool, len(users)),
		todayStart:   todayStart,
		todayEnd:     todayEnd,
	}
}

func (a *userUsageSummaryAccumulator) summaryFor(username string) (userUsageSummary, bool) {
	if _, ok := a.knownUsers[username]; !ok {
		return userUsageSummary{}, false
	}
	summary := a.values[username]
	if summary.Providers == nil {
		summary = emptyUserUsageSummary()
		a.providerSeen[username] = map[string]bool{}
		a.modelSeen[username] = map[string]bool{}
	}
	return summary, true
}

func (a *userUsageSummaryAccumulator) add(value usageAnalyticsRecord) {
	if value.record.UsageUsername == nil {
		return
	}
	username := strings.TrimSpace(*value.record.UsageUsername)
	if createdAt, ok := a.createdAt[username]; ok && value.record.Timestamp.Before(createdAt) {
		return
	}
	summary, ok := a.summaryFor(username)
	if !ok {
		return
	}
	summary.Records++
	if value.record.Failed {
		summary.FailedRecords++
	} else {
		summary.SuccessRecords++
	}
	summary.TotalTokens += value.aggregateTotalTokens
	if summary.FirstSeenAt == nil || value.record.Timestamp.Before(*summary.FirstSeenAt) {
		timestamp := value.record.Timestamp
		summary.FirstSeenAt = &timestamp
	}
	if summary.LastSeenAt == nil || value.record.Timestamp.After(*summary.LastSeenAt) {
		timestamp := value.record.Timestamp
		summary.LastSeenAt = &timestamp
		summary.LastProvider = cloneUsageString(value.record.Provider)
		summary.LastModel = cloneUsageString(value.record.Model)
	}
	appendUniqueString(&summary.Providers, a.providerSeen[username], value.record.Provider)
	appendUniqueString(&summary.Models, a.modelSeen[username], value.record.Model)
	if !value.record.Timestamp.Before(a.todayStart) && value.record.Timestamp.Before(a.todayEnd) {
		summary.TodayRecords++
		if value.record.Failed {
			summary.TodayFailedRecords++
		} else {
			summary.TodaySuccessRecords++
		}
		summary.TodayInputTokens += value.aggregateInputTokens
		summary.TodayOutputTokens += value.record.OutputTokens
		summary.TodayCachedTokens += value.record.CachedTokens
		summary.TodayReasoningTokens += value.record.ReasoningTokens
		summary.TodayTotalTokens += value.aggregateTotalTokens
		summary.TodayEstimatedCostUSD = mathRound(summary.TodayEstimatedCostUSD+value.estimatedCostUSD, 8)
		if value.unpriced {
			summary.TodayUnpricedRecords++
		}
	}
	summary.TotalEstimatedCostUSD = mathRound(summary.TotalEstimatedCostUSD+value.estimatedCostUSD, 8)
	if value.unpriced {
		summary.TotalUnpricedRecords++
	}
	a.values[username] = summary
}

func (a *userUsageSummaryAccumulator) addHourly(value usageAnalyticsHourlyContribution) {
	if value.usageUsername == nil {
		return
	}
	username := strings.TrimSpace(*value.usageUsername)
	if createdAt, ok := a.createdAt[username]; ok && value.hourStart.Before(createdAt) {
		return
	}
	summary, ok := a.summaryFor(username)
	if !ok {
		return
	}
	records := int(value.recordCount)
	failed := int(value.failedRecords)
	summary.Records += records
	summary.FailedRecords += failed
	summary.SuccessRecords += records - failed
	summary.TotalTokens += int(value.aggregateTotalTokens)
	if summary.FirstSeenAt == nil || value.hourStart.Before(*summary.FirstSeenAt) {
		timestamp := value.hourStart
		summary.FirstSeenAt = &timestamp
	}
	if summary.LastSeenAt == nil || value.hourStart.After(*summary.LastSeenAt) {
		timestamp := value.hourStart
		summary.LastSeenAt = &timestamp
		summary.LastProvider = cloneUsageString(value.provider)
		summary.LastModel = cloneUsageString(value.model)
	}
	appendUniqueString(&summary.Providers, a.providerSeen[username], value.provider)
	appendUniqueString(&summary.Models, a.modelSeen[username], value.model)
	summary.TotalEstimatedCostUSD = mathRound(summary.TotalEstimatedCostUSD+value.estimatedCostUSD, 8)
	summary.TotalUnpricedRecords += int(value.unpricedRecords)
	if !value.hourStart.Before(a.todayStart) && value.hourStart.Before(a.todayEnd) {
		summary.TodayRecords += records
		summary.TodayFailedRecords += failed
		summary.TodaySuccessRecords += records - failed
		summary.TodayInputTokens += int(value.aggregateInputTokens)
		summary.TodayOutputTokens += int(value.outputTokens)
		summary.TodayCachedTokens += int(value.cachedTokens)
		summary.TodayReasoningTokens += int(value.reasoningTokens)
		summary.TodayTotalTokens += int(value.aggregateTotalTokens)
		summary.TodayEstimatedCostUSD = mathRound(summary.TodayEstimatedCostUSD+value.estimatedCostUSD, 8)
		summary.TodayUnpricedRecords += int(value.unpricedRecords)
	}
	a.values[username] = summary
}

func cloneUsageString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type userUsageMetadata struct {
	firstSeenAt  *time.Time
	lastSeenAt   *time.Time
	lastProvider *string
	lastModel    *string
}

func (a *App) userUsageMetadata(ctx context.Context, users []UserRecord) (map[string]userUsageMetadata, error) {
	if len(users) == 0 {
		return map[string]userUsageMetadata{}, nil
	}
	metadata := map[string]userUsageMetadata{}
	// SQLite has a finite host-parameter limit. Query bounded batches while
	// still doing one grouped facts scan per batch instead of two round trips per
	// user (the previous N+1 pattern).
	const batchSize = 400
	for start := 0; start < len(users); start += batchSize {
		end := start + batchSize
		if end > len(users) {
			end = len(users)
		}
		placeholders := make([]string, end-start)
		args := make([]any, end-start)
		for index, user := range users[start:end] {
			placeholders[index] = "?"
			args[index] = user.Username
		}
		query := fmt.Sprintf(`
			WITH bounds AS (
				SELECT f.usage_username, MIN(f.timestamp) AS first_seen_at, MAX(f.timestamp) AS last_seen_at
				FROM usage_analytics_facts AS f
				JOIN users AS u ON u.username = f.usage_username
				WHERE f.usage_username IN (%s) AND f.timestamp >= u.created_at
				GROUP BY f.usage_username
			), latest AS (
				SELECT f.usage_username, f.provider, f.model
				FROM usage_analytics_facts AS f
				JOIN bounds AS b ON b.usage_username = f.usage_username AND b.last_seen_at = f.timestamp
				JOIN users AS u ON u.username = f.usage_username AND f.timestamp >= u.created_at
				WHERE NOT EXISTS (
					SELECT 1 FROM usage_analytics_facts AS newer
					WHERE newer.usage_username = f.usage_username
					  AND newer.timestamp = f.timestamp
					  AND newer.usage_record_id > f.usage_record_id
				)
			)
			SELECT b.usage_username, CAST(b.first_seen_at AS TEXT), CAST(b.last_seen_at AS TEXT), latest.provider, latest.model
			FROM bounds AS b
			LEFT JOIN latest ON latest.usage_username = b.usage_username
		`, strings.Join(placeholders, ","))
		rows, err := a.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var username, firstSeen, lastSeen, provider, model sql.NullString
			if err := rows.Scan(&username, &firstSeen, &lastSeen, &provider, &model); err != nil {
				_ = rows.Close()
				return nil, err
			}
			first, firstOK := parseDBTime(firstSeen.String)
			last, lastOK := parseDBTime(lastSeen.String)
			if !username.Valid || !firstOK || !lastOK {
				continue
			}
			metadata[username.String] = userUsageMetadata{
				firstSeenAt:  &first,
				lastSeenAt:   &last,
				lastProvider: nullableString(provider),
				lastModel:    nullableString(model),
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func (a *App) userUsageSummaries(ctx context.Context, users []UserRecord) (map[string]userUsageSummary, error) {
	pricing, err := a.billingPriceIndex(ctx)
	if err != nil {
		return nil, err
	}
	filters := UsageFilters{}
	if len(users) == 1 {
		filters.UsageUsername = &users[0].Username
	} else {
		filters.UsageUsernames = make([]string, 0, len(users))
		for _, user := range users {
			filters.UsageUsernames = append(filters.UsageUsernames, user.Username)
		}
	}
	collector := newUsageAnalyticsCollector(filters, pricing.Prices, pricing.MatchContext, nil, usageAnalyticsCollectorOptions{})
	collector.setBillingPriceIndex(pricing)
	accumulator := newUserUsageSummaryAccumulator(users)
	collector.userSummaries = accumulator
	// Account creation is an exact record-level boundary. An hourly aggregate
	// cannot distinguish facts before and after creation within the same hour.
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		return nil, err
	}
	if err := a.visitFilteredUsageAnalyticsRecords(ctx, filters, "", collector.Add); err != nil {
		return nil, err
	}
	metadata, err := a.userUsageMetadata(ctx, users)
	if err != nil {
		return nil, err
	}
	result := make(map[string]userUsageSummary, len(users))
	for _, user := range users {
		summary := accumulator.values[user.Username]
		if summary.Providers == nil {
			summary = emptyUserUsageSummary()
		}
		if details, ok := metadata[user.Username]; ok {
			summary.FirstSeenAt = details.firstSeenAt
			summary.LastSeenAt = details.lastSeenAt
			summary.LastProvider = details.lastProvider
			summary.LastModel = details.lastModel
		}
		result[user.Username] = summary
	}
	return result, nil
}

func appendUniqueString(items *[]string, seen map[string]bool, value *string) {
	if value == nil {
		return
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" || seen[normalized] {
		return
	}
	seen[normalized] = true
	*items = append(*items, normalized)
}

func displayUserName(user UserRecord) string {
	if strings.TrimSpace(user.Nickname) != "" {
		return strings.TrimSpace(user.Nickname)
	}
	if strings.TrimSpace(user.Username) != "" {
		return strings.TrimSpace(user.Username)
	}
	return "未知用户"
}

func userSummaryResponse(user UserRecord, keys []UserApiKeySummary, usage userUsageSummary, quota UserQuotaStatusResponse) UserSummaryResponse {
	if usage.Providers == nil {
		usage = emptyUserUsageSummary()
	}
	return UserSummaryResponse{
		ID:                    user.ID,
		Username:              user.Username,
		IsAdmin:               user.IsAdmin,
		IsSuperAdmin:          user.IsSuperAdmin,
		Nickname:              user.Nickname,
		DisabledAt:            user.DisabledAt,
		PasswordSet:           user.PasswordHash != nil && user.PasswordSalt != nil,
		CreatedAt:             user.CreatedAt,
		UpdatedAt:             user.UpdatedAt,
		APIKeys:               keys,
		KeyCount:              len(keys),
		Records:               usage.Records,
		SuccessRecords:        usage.SuccessRecords,
		FailedRecords:         usage.FailedRecords,
		TotalTokens:           usage.TotalTokens,
		TodayRecords:          usage.TodayRecords,
		TodaySuccessRecords:   usage.TodaySuccessRecords,
		TodayFailedRecords:    usage.TodayFailedRecords,
		TodayInputTokens:      usage.TodayInputTokens,
		TodayOutputTokens:     usage.TodayOutputTokens,
		TodayCachedTokens:     usage.TodayCachedTokens,
		TodayReasoningTokens:  usage.TodayReasoningTokens,
		TodayTotalTokens:      usage.TodayTotalTokens,
		TodayEstimatedCostUSD: usage.TodayEstimatedCostUSD,
		TodayUnpricedRecords:  usage.TodayUnpricedRecords,
		TotalEstimatedCostUSD: usage.TotalEstimatedCostUSD,
		TotalUnpricedRecords:  usage.TotalUnpricedRecords,
		FirstSeenAt:           usage.FirstSeenAt,
		LastSeenAt:            usage.LastSeenAt,
		LastProvider:          usage.LastProvider,
		LastModel:             usage.LastModel,
		Providers:             usage.Providers,
		Models:                usage.Models,
		Quota:                 quota,
	}
}
