package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upUserSuperAdmin, nil)
}

func upUserSuperAdmin(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var exists bool
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "is_super_admin" {
			exists = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE users ADD COLUMN is_super_admin BOOLEAN NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	var userCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		return err
	}
	if userCount == 0 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit user super admin migration: %w", err)
		}
		return nil
	}

	var existingSuperAdmins int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_super_admin = 1`).Scan(&existingSuperAdmins); err != nil {
		return err
	}
	if existingSuperAdmins > 0 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit user super admin migration: %w", err)
		}
		return nil
	}

	var superAdminID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE is_admin = 1 AND disabled_at IS NULL ORDER BY id ASC LIMIT 1`).Scan(&superAdminID); err != nil {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE disabled_at IS NULL ORDER BY id ASC LIMIT 1`).Scan(&superAdminID); err != nil {
			if err := tx.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id ASC LIMIT 1`).Scan(&superAdminID); err != nil {
				return fmt.Errorf("select initial user for super administrator: %w", err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE users
		SET is_super_admin = CASE
			WHEN id = ? THEN 1
			ELSE 0
		END,
		is_admin = CASE
			WHEN id = ? THEN 1
			ELSE is_admin
		END
	`, superAdminID, superAdminID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user super admin migration: %w", err)
	}
	return nil
}
