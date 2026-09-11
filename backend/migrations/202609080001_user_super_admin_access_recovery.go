package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"
)

// This migration deliberately follows the already-published
// 202609020001 migration instead of changing it. It initializes super-admin
// access only when no saved super-admin identity exists; a disabled account
// retains its existing role and disabled state.
func init() {
	goose.AddMigrationNoTxContext(upUserSuperAdminAccessRecovery, nil)
}

func upUserSuperAdminAccessRecovery(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var superAdminCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_super_admin = 1`).Scan(&superAdminCount); err != nil {
		return fmt.Errorf("count super administrators: %w", err)
	}
	if superAdminCount == 0 {
		var activeUserID int64
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM users
			WHERE disabled_at IS NULL
			ORDER BY CASE WHEN is_admin = 1 THEN 0 ELSE 1 END, id ASC
			LIMIT 1
		`).Scan(&activeUserID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `UPDATE users SET is_admin = 1, is_super_admin = 1 WHERE id = ?`, activeUserID); err != nil {
				return fmt.Errorf("restore active super administrator: %w", err)
			}
		case errors.Is(err, sql.ErrNoRows):
			// Every user is disabled. Do not silently re-enable an account.
		default:
			return fmt.Errorf("find active user for super-admin recovery: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user super-admin recovery migration: %w", err)
	}
	return nil
}
