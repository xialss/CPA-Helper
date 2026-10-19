package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

func parseAPIKeyExpiration(raw json.RawMessage) (*time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, validationError("密钥有效期必须是 RFC3339 日期时间或 null")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, validationError("密钥有效期必须是 RFC3339 日期时间或 null")
	}
	expiresAt = expiresAt.Truncate(time.Microsecond)
	if !expiresAt.After(time.Now()) {
		return nil, validationError("密钥过期时间必须晚于当前时间")
	}
	return &expiresAt, nil
}

func storedAPIKeyExpiration(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	expiresAt, ok := parseDBTime(value.String)
	if !ok {
		return nil, errors.New("API KEY 有效期存储格式无效")
	}
	return &expiresAt, nil
}

func apiKeyExpirationString(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.In(appTimeLocation).Format(time.RFC3339Nano)
	return &formatted
}

func apiKeyExpirationPassed(value *string, now time.Time) bool {
	if value == nil {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, *value)
	return err == nil && !expiresAt.After(now)
}

func (key UserAPIKey) expired(now time.Time) bool {
	return key.ExpiresAt != nil && !key.ExpiresAt.After(now)
}

// Every remote addition, including compensation and administrative binding,
// must respect a saved deadline. Creation has no saved binding yet.
func (a *App) requireAPIKeyNotExpired(ctx context.Context, hash string) error {
	var value sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT CAST(expires_at AS TEXT) FROM user_api_keys WHERE api_key_hash = ?`, hash).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expiresAt, err := storedAPIKeyExpiration(value)
	if err != nil {
		return err
	}
	if (UserAPIKey{ExpiresAt: expiresAt}).expired(time.Now()) {
		return conflictError("API KEY 已过期，请先修改密钥有效期")
	}
	return nil
}

func (a *App) insertGeneratedAPIKey(ctx context.Context, userID int, hash, key, description string, expiresAt *time.Time) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := dbTime(time.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_api_keys (api_key_hash, user_id, api_key, description, created_at, updated_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, hash, userID, key, description, now, now, dbTimePtr(expiresAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ?`, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) updateAPIKeyExpirationLocked(ctx context.Context, key UserAPIKey, description string, expiresAt *time.Time) error {
	user, err := a.getUser(ctx, key.UserID)
	if err != nil {
		return err
	}
	// Paused or disabled accounts remain paused when their owner edits a
	// deadline. Their existing lifecycle restoration handles activation.
	activate := key.expired(time.Now()) && user.DisabledAt == nil && user.QuotaPausedAt == nil && quotaHasAvailable(user)
	if activate && (key.APIKey == nil || strings.TrimSpace(*key.APIKey) == "") {
		return conflictError("当前 API KEY 缺少完整密钥，无法恢复")
	}
	var enforcedAt, syncError sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT CAST(expiration_enforced_at AS TEXT), expiration_sync_error FROM user_api_keys WHERE api_key_hash = ?`, key.APIKeyHash).Scan(&enforcedAt, &syncError); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE user_api_keys SET description = ?, expires_at = ?, expiration_enforced_at = NULL, expiration_sync_error = NULL, updated_at = ? WHERE api_key_hash = ? AND user_id = ?`, description, dbTimePtr(expiresAt), dbTime(time.Now()), key.APIKeyHash, key.UserID)
	if err != nil || !activate {
		return err
	}
	changed, err := a.addRemoteAPIKeyWithChange(ctx, *key.APIKey)
	if err == nil {
		return nil
	}
	persistCtx := context.WithoutCancel(ctx)
	_, restoreErr := a.db.ExecContext(persistCtx, `UPDATE user_api_keys SET description = ?, expires_at = ?, expiration_enforced_at = ?, expiration_sync_error = ?, updated_at = ? WHERE api_key_hash = ? AND user_id = ?`, key.Description, dbTimePtr(key.ExpiresAt), enforcedAt, syncError, dbTimePtr(key.UpdatedAt), key.APIKeyHash, key.UserID)
	err = combineUserMutationErrors(err, restoreErr)
	if changed || key.expired(time.Now()) {
		err = combineUserMutationErrors(err, a.removeRemoteAPIKeyHash(persistCtx, key.APIKeyHash))
	}
	return err
}

type apiKeyExpirationRunner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (a *App) startAPIKeyExpiration() {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &apiKeyExpirationRunner{cancel: cancel, done: make(chan struct{})}
	a.apiKeyExpiration = runner
	go func() {
		defer close(runner.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := a.enforceExpiredAPIKeys(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("API key expiration synchronization failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// A failed CPA write stays pending and is retried by the background runner;
// the error is persisted on the key so the UI never reports successful revoke.
func (a *App) enforceExpiredAPIKeys(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx, `SELECT api_key_hash, user_id FROM user_api_keys WHERE expires_at IS NOT NULL AND expiration_enforced_at IS NULL AND (julianday(expires_at) <= julianday(?) OR julianday(expires_at) IS NULL)`, dbTime(time.Now()))
	if err != nil {
		return err
	}
	type pendingKey struct {
		hash   string
		userID int
	}
	var pending []pendingKey
	for rows.Next() {
		var key pendingKey
		if err := rows.Scan(&key.hash, &key.userID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, key)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	var combined error
	for _, pending := range pending {
		unlock := a.lockUserMutation(pending.userID)
		var value sql.NullString
		err := a.db.QueryRowContext(ctx, `SELECT CAST(expires_at AS TEXT) FROM user_api_keys WHERE api_key_hash = ? AND user_id = ? AND expiration_enforced_at IS NULL`, pending.hash, pending.userID).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			unlock()
			continue
		}
		if err == nil && value.Valid {
			expiresAt, parseErr := storedAPIKeyExpiration(value)
			if parseErr != nil || (UserAPIKey{ExpiresAt: expiresAt}).expired(time.Now()) {
				err = a.removeRemoteAPIKeyHash(ctx, pending.hash)
				persistCtx := context.WithoutCancel(ctx)
				if err == nil {
					_, err = a.db.ExecContext(persistCtx, `UPDATE user_api_keys SET expiration_enforced_at = ?, expiration_sync_error = NULL WHERE api_key_hash = ? AND user_id = ?`, dbTime(time.Now()), pending.hash, pending.userID)
				} else {
					_, persistErr := a.db.ExecContext(persistCtx, `UPDATE user_api_keys SET expiration_sync_error = ? WHERE api_key_hash = ? AND user_id = ?`, err.Error(), pending.hash, pending.userID)
					err = combineUserMutationErrors(err, persistErr)
				}
			}
		}
		unlock()
		if err != nil {
			combined = errors.Join(combined, fmt.Errorf("expire API key for user %d: %w", pending.userID, err))
		}
	}
	return combined
}
