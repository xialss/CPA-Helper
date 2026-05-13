"""Move login account state from app settings to users.

Revision ID: 20260506_0012
Revises: 20260506_0011
Create Date: 2026-05-06
"""

from collections.abc import Sequence
from datetime import datetime

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0012"
down_revision: str | None = "20260506_0011"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    return any(
        column["name"] == column_name
        for column in sa.inspect(op.get_bind()).get_columns(table_name)
    )


def _user_count() -> int:
    if not _table_exists("users"):
        return 0
    return int(op.get_bind().execute(sa.text("SELECT COUNT(*) FROM users")).scalar() or 0)


def _seed_user_from_app_settings() -> None:
    if _user_count() > 0:
        return
    required_columns = (
        "account_username",
        "account_password_hash",
        "account_password_salt",
    )
    if not all(_column_exists("app_settings", column) for column in required_columns):
        return
    row = op.get_bind().execute(
        sa.text(
            """
            SELECT account_username, account_password_hash, account_password_salt
            FROM app_settings
            WHERE id = 1
            """
        )
    ).first()
    if row is None:
        return
    username = (row[0] or "").strip()
    password_hash = (row[1] or "").strip()
    password_salt = (row[2] or "").strip()
    if not username or not password_hash or not password_salt:
        return
    now = datetime.now()
    op.get_bind().execute(
        sa.text(
            """
            INSERT INTO users (
                username,
                password_hash,
                password_salt,
                is_admin,
                remark,
                created_at,
                updated_at
            )
            VALUES (
                :username,
                :password_hash,
                :password_salt,
                1,
                '',
                :created_at,
                :updated_at
            )
            """
        ),
        {
            "username": username,
            "password_hash": password_hash,
            "password_salt": password_salt,
            "created_at": now,
            "updated_at": now,
        },
    )


def upgrade() -> None:
    if not _table_exists("app_settings"):
        return
    _seed_user_from_app_settings()
    columns_to_drop = [
        "account_username",
        "account_password_hash",
        "account_password_salt",
        "account_must_change_password",
    ]
    existing = [column for column in columns_to_drop if _column_exists("app_settings", column)]
    if existing:
        with op.batch_alter_table("app_settings") as batch_op:
            for column in existing:
                batch_op.drop_column(column)


def downgrade() -> None:
    if not _table_exists("app_settings"):
        return
    columns_to_add = {
        "account_username": sa.Column(
            "account_username",
            sqlmodel.sql.sqltypes.AutoString(length=120),
            nullable=False,
            server_default="admin",
        ),
        "account_password_hash": sa.Column(
            "account_password_hash",
            sqlmodel.sql.sqltypes.AutoString(length=200),
            nullable=False,
            server_default="",
        ),
        "account_password_salt": sa.Column(
            "account_password_salt",
            sqlmodel.sql.sqltypes.AutoString(length=64),
            nullable=False,
            server_default="",
        ),
        "account_must_change_password": sa.Column(
            "account_must_change_password",
            sa.Boolean(),
            nullable=False,
            server_default=sa.false(),
        ),
    }
    missing = [
        (name, column)
        for name, column in columns_to_add.items()
        if not _column_exists("app_settings", name)
    ]
    if missing:
        with op.batch_alter_table("app_settings") as batch_op:
            for _, column in missing:
                batch_op.add_column(column)

    if _table_exists("users"):
        row = op.get_bind().execute(
            sa.text(
                """
                SELECT username, password_hash, password_salt
                FROM users
                ORDER BY id
                LIMIT 1
                """
            )
        ).first()
        if row is not None:
            op.get_bind().execute(
                sa.text(
                    """
                    UPDATE app_settings
                    SET account_username = :username,
                        account_password_hash = :password_hash,
                        account_password_salt = :password_salt,
                        account_must_change_password = 0
                    WHERE id = 1
                    """
                ),
                {
                    "username": row[0],
                    "password_hash": row[1] or "",
                    "password_salt": row[2] or "",
                },
            )
