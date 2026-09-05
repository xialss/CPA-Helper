package app

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

const quotaPauseReasonExhausted = "quota_exhausted"

type userQuotaPayload struct {
	LifetimeQuotaUSD *float64 `json:"lifetime_quota_usd"`
	MonthlyQuotaUSD  *float64 `json:"monthly_quota_usd"`
}

type UserQuotaStatusResponse struct {
	Unlimited            bool       `json:"unlimited"`
	LifetimeQuotaUSD     *float64   `json:"lifetime_quota_usd"`
	LifetimeRemainingUSD *float64   `json:"lifetime_remaining_usd"`
	MonthlyQuotaUSD      *float64   `json:"monthly_quota_usd"`
	MonthlyUsedUSD       float64    `json:"monthly_used_usd"`
	MonthlyRemainingUSD  *float64   `json:"monthly_remaining_usd"`
	QuotaMonth           string     `json:"quota_month"`
	Paused               bool       `json:"paused"`
	PausedAt             *time.Time `json:"paused_at"`
	PauseReason          *string    `json:"pause_reason"`
	SyncError            *string    `json:"sync_error"`
	UnpricedRecords      int        `json:"unpriced_records"`
	CanCreateKeys        bool       `json:"can_create_keys"`
	StartedAt            *time.Time `json:"started_at"`
}

func (a *App) handleCurrentUserQuota(w http.ResponseWriter, r *http.Request) error {
	user, err := a.readyUser(r.Context(), r)
	if err != nil {
		return err
	}
	if err := requireMethod(r, http.MethodGet); err != nil {
		return err
	}
	status, err := a.userQuotaStatus(r.Context(), user.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, status)
	return nil
}

func (a *App) updateUserQuota(ctx context.Context, userID int, payload userQuotaPayload) (UserQuotaStatusResponse, error) {
	defer a.lockUserMutation(userID)()
	return a.updateUserQuotaLocked(ctx, userID, payload)
}

func (a *App) updateUserQuotaLocked(ctx context.Context, userID int, payload userQuotaPayload) (UserQuotaStatusResponse, error) {
	user, err := a.getUser(ctx, userID)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	lifetime, err := normalizedQuotaAmount(payload.LifetimeQuotaUSD)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	monthly, err := normalizedQuotaAmount(payload.MonthlyQuotaUSD)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}

	now := dbTime(time.Now())
	currentMonth := quotaMonth(time.Now())
	var startedAt any
	if lifetime != nil || monthly != nil {
		if user.QuotaStartedAt != nil {
			startedAt = dbTime(*user.QuotaStartedAt)
		} else {
			startedAt = now
		}
	}
	monthUsed := 0.0
	monthValue := ""
	if monthly != nil {
		monthValue = currentMonth
		if user.QuotaMonthlyUSD != nil && user.QuotaMonth == currentMonth {
			monthUsed = mathRound(user.QuotaMonthUsedUSD, 8)
		}
	}
	_, err = a.db.ExecContext(ctx, `
		UPDATE users
		SET quota_lifetime_usd = ?, quota_monthly_usd = ?, quota_started_at = ?,
		    quota_month = ?, quota_month_used_usd = ?, quota_sync_error = NULL,
		    updated_at = ?
		WHERE id = ?
	`, quotaAmountArg(lifetime), quotaAmountArg(monthly), startedAt, monthValue, monthUsed, now, userID)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	user, err = a.getUser(ctx, userID)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	var syncErr error
	if quotaHasAvailable(user) {
		syncErr = a.restoreQuotaPausedUserIfAvailableLocked(ctx, user.ID)
	} else {
		syncErr = a.pauseUserKeysForQuotaLocked(ctx, user.ID, quotaPauseReasonExhausted)
	}
	status, statusErr := a.userQuotaStatusLocked(ctx, userID)
	if statusErr != nil {
		return UserQuotaStatusResponse{}, statusErr
	}
	if syncErr != nil {
		return status, syncErr
	}
	return status, nil
}

func (a *App) userQuotaStatus(ctx context.Context, userID int) (UserQuotaStatusResponse, error) {
	defer a.lockUserMutation(userID)()
	return a.userQuotaStatusLocked(ctx, userID)
}

func (a *App) userQuotaStatusLocked(ctx context.Context, userID int) (UserQuotaStatusResponse, error) {
	user, err := a.getUser(ctx, userID)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	return a.userQuotaStatusForRecordLocked(ctx, user)
}

func (a *App) userQuotaStatusForRecordLocked(ctx context.Context, user UserRecord) (UserQuotaStatusResponse, error) {
	var err error
	user, err = a.ensureQuotaMonth(ctx, user)
	if err != nil {
		return UserQuotaStatusResponse{}, err
	}
	if user.QuotaPausedAt != nil {
		var syncErr error
		syncAction := "restore"
		if quotaHasAvailable(user) {
			syncErr = a.restoreQuotaPausedUserIfAvailableLocked(ctx, user.ID)
		} else {
			syncAction = "pause"
			// A previous pause can leave a persisted sync error while the account
			// remains exhausted. Retry the remote removal on reads so a transient
			// CPA failure can converge without requiring a second quota mutation.
			syncErr = a.pauseUserKeysForQuotaLocked(ctx, user.ID, quotaPauseReasonExhausted)
		}
		user, err = a.getUser(ctx, user.ID)
		if err != nil {
			return UserQuotaStatusResponse{}, err
		}
		if syncErr != nil {
			// Remote sync failures are persisted on the user and returned as
			// part of the status, so a GET never silently discards them.
			log.Printf("%s quota-paused user %d during status read failed: %v", syncAction, user.ID, syncErr)
			return quotaStatusFromUser(user), nil
		}
	} else if !quotaIsUnlimited(user) && !quotaHasAvailable(user) {
		syncErr := a.pauseUserKeysForQuotaLocked(ctx, user.ID, quotaPauseReasonExhausted)
		user, err = a.getUser(ctx, user.ID)
		if err != nil {
			return UserQuotaStatusResponse{}, err
		}
		if syncErr != nil {
			log.Printf("pause quota-exhausted user %d during status read failed: %v", user.ID, syncErr)
			return quotaStatusFromUser(user), nil
		}
	}
	return quotaStatusFromUser(user), nil
}

func (a *App) ensureUserQuotaReadyForKeys(ctx context.Context, userID int) error {
	status, err := a.userQuotaStatusLocked(ctx, userID)
	if err != nil {
		return err
	}
	if !status.CanCreateKeys {
		return conflictError("用户额度已用尽，API KEY 已暂停，请联系管理员补充额度")
	}
	return nil
}

func (a *App) applyQuotaCharge(ctx context.Context, record UsageRecord, pricing modelPriceBillingIndex) error {
	if record.UsageUsername == nil || strings.TrimSpace(*record.UsageUsername) == "" {
		return nil
	}
	user, err := a.userByUsername(ctx, *record.UsageUsername)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	userID := user.ID
	unlock := a.lockUserMutation(userID)
	defer unlock()
	// The username lookup above only identifies the lock key. Re-read inside
	// the critical section so concurrent charges always build on the latest
	// quota snapshot instead of overwriting a sibling update.
	user, err = a.getUser(ctx, userID)
	if err != nil {
		return err
	}
	user, err = a.ensureQuotaMonth(ctx, user)
	if err != nil {
		return err
	}
	if user.QuotaPausedAt != nil && user.DisabledAt == nil && quotaHasAvailable(user) {
		if user.QuotaPauseReason != nil && *user.QuotaPauseReason == quotaPauseReasonExhausted {
			if syncErr := a.restoreQuotaPausedUserIfAvailableLocked(ctx, user.ID); syncErr != nil {
				log.Printf("restore quota-paused user %d during usage charge failed: %v", user.ID, syncErr)
			}
			reloaded, reloadErr := a.getUser(ctx, user.ID)
			if reloadErr != nil {
				return reloadErr
			}
			user = reloaded
		}
	}
	if quotaIsUnlimited(user) {
		return nil
	}

	var existing int
	err = a.db.QueryRowContext(ctx, `SELECT id FROM user_quota_charges WHERE usage_record_id = ?`, record.ID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	cacheUsageRecordAuth(&record)
	amount, unpriced := recordCost(record, pricing.Prices, pricing.MatchContext)
	amount = mathRound(amount, 8)
	monthlyDeducted, lifetimeDeducted := 0.0, 0.0
	remaining := amount
	if !unpriced && remaining > 0 {
		if monthlyRemaining := quotaMonthlyRemaining(user); monthlyRemaining != nil && *monthlyRemaining > 0 {
			monthlyDeducted = minQuotaAmount(remaining, *monthlyRemaining)
			user.QuotaMonthUsedUSD = mathRound(user.QuotaMonthUsedUSD+monthlyDeducted, 8)
			remaining = mathRound(remaining-monthlyDeducted, 8)
		}
		if remaining > 0 && user.QuotaLifetimeUSD != nil && *user.QuotaLifetimeUSD > 0 {
			lifetimeDeducted = minQuotaAmount(remaining, *user.QuotaLifetimeUSD)
			nextLifetime := mathRound(*user.QuotaLifetimeUSD-lifetimeDeducted, 8)
			user.QuotaLifetimeUSD = &nextLifetime
			remaining = mathRound(remaining-lifetimeDeducted, 8)
		}
		if remaining > 0 && (user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD <= 0) {
			user.QuotaMonthUsedUSD = mathRound(user.QuotaMonthUsedUSD+remaining, 8)
			monthlyDeducted = mathRound(monthlyDeducted+remaining, 8)
			remaining = 0
		}
	}
	if unpriced {
		user.QuotaUnpricedRecords++
	}

	now := dbTime(time.Now())
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_quota_charges (
			usage_record_id, user_id, usage_username, amount_usd,
			monthly_deducted_usd, lifetime_deducted_usd, unpriced,
			quota_month, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.ID, user.ID, user.Username, amount, monthlyDeducted, lifetimeDeducted, unpriced, nonBlank(user.QuotaMonth, quotaMonth(record.Timestamp)), now)
	if err != nil {
		if isUniqueConstraintError(err) {
			return nil
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE users
		SET quota_month_used_usd = ?, quota_lifetime_usd = ?,
		    quota_unpriced_records = ?, updated_at = ?
		WHERE id = ?
	`, mathRound(user.QuotaMonthUsedUSD, 8), quotaAmountArg(user.QuotaLifetimeUSD), user.QuotaUnpricedRecords, now, user.ID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true

	if remaining > 0 || !quotaHasAvailable(user) {
		// Keep the usage record/ledger commit independent from CPA availability.
		// pauseUserKeysForQuota persists any synchronization failure on the
		// account; status endpoints expose that error instead of dropping it.
		if syncErr := a.pauseUserKeysForQuotaLocked(ctx, user.ID, quotaPauseReasonExhausted); syncErr != nil {
			log.Printf("pause quota-exhausted user %d during usage charge failed: %v", user.ID, syncErr)
		}
	}
	return nil
}

func (a *App) ensureQuotaMonth(ctx context.Context, user UserRecord) (UserRecord, error) {
	if user.QuotaMonthlyUSD == nil {
		return user, nil
	}
	current := quotaMonth(time.Now())
	if user.QuotaMonth == current {
		return user, nil
	}
	_, err := a.db.ExecContext(ctx, `
		UPDATE users
		SET quota_month = ?, quota_month_used_usd = 0, quota_sync_error = NULL, updated_at = ?
		WHERE id = ?
	`, current, dbTime(time.Now()), user.ID)
	if err != nil {
		return UserRecord{}, err
	}
	updated, err := a.getUser(ctx, user.ID)
	if err != nil {
		return UserRecord{}, err
	}
	if user.QuotaPausedAt != nil && user.DisabledAt == nil && quotaHasAvailable(updated) {
		if user.QuotaPauseReason != nil && *user.QuotaPauseReason == quotaPauseReasonExhausted {
			if syncErr := a.restoreQuotaPausedUserIfAvailableLocked(ctx, user.ID); syncErr != nil {
				log.Printf("auto restore quota-exhausted user %d on month rollover failed: %v", user.ID, syncErr)
			}
			reloaded, reloadErr := a.getUser(ctx, user.ID)
			if reloadErr != nil {
				return UserRecord{}, reloadErr
			}
			return reloaded, nil
		}
	}
	return updated, nil
}

func (a *App) pauseUserKeysForQuota(ctx context.Context, userID int, reason string) error {
	defer a.lockUserMutation(userID)()
	return a.pauseUserKeysForQuotaLocked(ctx, userID, reason)
}

func (a *App) pauseUserKeysForQuotaLocked(ctx context.Context, userID int, reason string) error {
	user, err := a.getUser(ctx, userID)
	if err != nil {
		return err
	}
	// Disabled users have already had their keys removed by disableUser. Keep
	// the local pause marker in sync, but do not mutate the remote list.
	if user.DisabledAt != nil {
		now := dbTime(time.Now())
		_, err := a.db.ExecContext(ctx, `UPDATE users SET quota_paused_at = COALESCE(quota_paused_at, ?), quota_pause_reason = ?, updated_at = ? WHERE id = ?`, now, reason, now, userID)
		return err
	}
	if user.QuotaPausedAt != nil && user.QuotaSyncError == nil {
		return nil
	}
	now := dbTime(time.Now())
	result, err := a.db.ExecContext(ctx, `
		UPDATE users
		SET quota_paused_at = COALESCE(quota_paused_at, ?),
		    quota_pause_reason = ?, quota_sync_error = NULL, updated_at = ?
		WHERE id = ? AND disabled_at IS NULL
	`, now, reason, now, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		current, currentErr := a.getUser(ctx, userID)
		if currentErr != nil {
			return currentErr
		}
		if current.DisabledAt != nil {
			return nil
		}
		return conflictError("暂停用户额度时用户状态已变化")
	}
	keys, err := a.userAPIKeys(ctx, userID)
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, err); persistErr != nil {
			return combineUserMutationErrors(err, persistErr)
		}
		return err
	}
	var syncErr error
	for _, key := range keys {
		_, keySyncErr := a.removeRemoteAPIKeyHashWithChange(ctx, key.APIKeyHash)
		if keySyncErr != nil {
			syncErr = combineUserMutationErrors(syncErr, keySyncErr)
		}
	}
	if syncErr != nil {
		// Keep the local account paused and attempt every key so a transient
		// failure for one binding does not leave the remaining bindings active.
		persistCtx := context.WithoutCancel(ctx)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, syncErr); persistErr != nil {
			syncErr = combineUserMutationErrors(syncErr, persistErr)
		}
		return syncErr
	}
	return nil
}

func (a *App) restoreQuotaPausedUserIfAvailable(ctx context.Context, userID int) error {
	defer a.lockUserMutation(userID)()
	return a.restoreQuotaPausedUserIfAvailableLocked(ctx, userID)
}

func (a *App) restoreQuotaPausedUserIfAvailableLocked(ctx context.Context, userID int) error {
	user, err := a.getUser(ctx, userID)
	if err != nil {
		return err
	}
	user, err = a.ensureQuotaMonth(ctx, user)
	if err != nil {
		return err
	}
	if user.QuotaPausedAt == nil || user.DisabledAt != nil || !quotaHasAvailable(user) {
		return nil
	}
	keys, err := a.userAPIKeys(ctx, userID)
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, err); persistErr != nil {
			return combineUserMutationErrors(err, persistErr)
		}
		return err
	}
	for _, key := range keys {
		if key.APIKey == nil {
			missingKeyErr := conflictError("存在无法恢复的 API KEY，请重新绑定后再恢复")
			persistCtx := context.WithoutCancel(ctx)
			if persistErr := a.setQuotaSyncError(persistCtx, userID, missingKeyErr); persistErr != nil {
				return combineUserMutationErrors(missingKeyErr, persistErr)
			}
			return missingKeyErr
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
			persistCtx := context.WithoutCancel(ctx)
			compensationErr := a.removeRestoredUserAPIKeys(persistCtx, restored)
			combinedErr := combineUserMutationErrors(syncErr, compensationErr)
			if persistErr := a.setQuotaSyncError(persistCtx, userID, combinedErr); persistErr != nil {
				combinedErr = combineUserMutationErrors(combinedErr, persistErr)
			}
			return combinedErr
		}
	}
	result, err := a.db.ExecContext(ctx, `
		UPDATE users
		SET quota_paused_at = NULL, quota_pause_reason = NULL,
		    quota_sync_error = NULL, updated_at = ?
		WHERE id = ? AND disabled_at IS NULL AND quota_paused_at = ?
	`, dbTime(time.Now()), userID, dbTimePtr(user.QuotaPausedAt))
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(persistCtx, restored)
		combinedErr := combineUserMutationErrors(err, compensationErr)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	affected, err := result.RowsAffected()
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(persistCtx, restored)
		combinedErr := combineUserMutationErrors(err, compensationErr)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	if affected != 1 {
		current, currentErr := a.getUser(ctx, userID)
		if currentErr != nil {
			persistCtx := context.WithoutCancel(ctx)
			compensationErr := a.removeRestoredUserAPIKeys(persistCtx, restored)
			combinedErr := combineUserMutationErrors(currentErr, compensationErr)
			if persistErr := a.setQuotaSyncError(persistCtx, userID, combinedErr); persistErr != nil {
				combinedErr = combineUserMutationErrors(combinedErr, persistErr)
			}
			return combinedErr
		}
		if current.DisabledAt == nil && current.QuotaPausedAt == nil {
			return nil
		}
		persistCtx := context.WithoutCancel(ctx)
		compensationErr := a.removeRestoredUserAPIKeys(persistCtx, restored)
		guardErr := conflictError("恢复额度用户时用户状态已变化")
		combinedErr := combineUserMutationErrors(guardErr, compensationErr)
		if persistErr := a.setQuotaSyncError(persistCtx, userID, combinedErr); persistErr != nil {
			combinedErr = combineUserMutationErrors(combinedErr, persistErr)
		}
		return combinedErr
	}
	return nil
}

func (a *App) setQuotaSyncError(ctx context.Context, userID int, err error) error {
	return a.setQuotaSyncMessage(ctx, userID, err.Error())
}

func (a *App) setQuotaSyncMessage(ctx context.Context, userID int, message string) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := a.db.ExecContext(ctx, `UPDATE users SET quota_sync_error = ?, updated_at = ? WHERE id = ?`, message, dbTime(time.Now()), userID)
	return err
}

func quotaStatusFromUser(user UserRecord) UserQuotaStatusResponse {
	monthlyRemaining := quotaMonthlyRemaining(user)
	var lifetimeRemaining *float64
	if user.QuotaLifetimeUSD != nil {
		value := mathRound(maxQuotaAmount(*user.QuotaLifetimeUSD, 0), 8)
		lifetimeRemaining = &value
	}
	return UserQuotaStatusResponse{
		Unlimited:            quotaIsUnlimited(user),
		LifetimeQuotaUSD:     user.QuotaLifetimeUSD,
		LifetimeRemainingUSD: lifetimeRemaining,
		MonthlyQuotaUSD:      user.QuotaMonthlyUSD,
		MonthlyUsedUSD:       mathRound(user.QuotaMonthUsedUSD, 8),
		MonthlyRemainingUSD:  monthlyRemaining,
		QuotaMonth:           user.QuotaMonth,
		Paused:               user.QuotaPausedAt != nil,
		PausedAt:             user.QuotaPausedAt,
		PauseReason:          user.QuotaPauseReason,
		SyncError:            user.QuotaSyncError,
		UnpricedRecords:      user.QuotaUnpricedRecords,
		CanCreateKeys:        user.QuotaPausedAt == nil && quotaHasAvailable(user),
		StartedAt:            user.QuotaStartedAt,
	}
}

func quotaIsUnlimited(user UserRecord) bool {
	return user.QuotaLifetimeUSD == nil && user.QuotaMonthlyUSD == nil
}

func quotaHasAvailable(user UserRecord) bool {
	if quotaIsUnlimited(user) {
		return true
	}
	if monthlyRemaining := quotaMonthlyRemaining(user); monthlyRemaining != nil && *monthlyRemaining > 0 {
		return true
	}
	return user.QuotaLifetimeUSD != nil && *user.QuotaLifetimeUSD > 0
}

func quotaMonthlyRemaining(user UserRecord) *float64 {
	if user.QuotaMonthlyUSD == nil {
		return nil
	}
	remaining := mathRound(*user.QuotaMonthlyUSD-user.QuotaMonthUsedUSD, 8)
	remaining = maxQuotaAmount(remaining, 0)
	return &remaining
}

func normalizedQuotaAmount(value *float64) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return nil, validationError("额度金额不能小于 0")
	}
	normalized := mathRound(*value, 8)
	return &normalized, nil
}

func quotaAmountArg(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func quotaMonth(value time.Time) string {
	return value.In(appTimeLocation).Format("2006-01")
}

func minQuotaAmount(left, right float64) float64 {
	if left < right {
		return mathRound(left, 8)
	}
	return mathRound(right, 8)
}

func maxQuotaAmount(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func isUniqueConstraintError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique")
}
