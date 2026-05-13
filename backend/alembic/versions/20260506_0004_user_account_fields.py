"""Add user account fields.

Revision ID: 20260506_0004
Revises: 20260506_0003
Create Date: 2026-05-06
"""

from collections.abc import Sequence
from datetime import datetime

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0004"
down_revision: str | None = "20260506_0003"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def upgrade() -> None:
    if not _table_exists("users"):
        return

    if not _column_exists("users", "username"):
        op.add_column(
            "users",
            sa.Column(
                "username",
                sqlmodel.sql.sqltypes.AutoString(length=120),
                nullable=False,
                server_default="",
            ),
        )
        op.create_index("ix_users_username", "users", ["username"])

    if not _column_exists("users", "password_hash"):
        op.add_column(
            "users",
            sa.Column(
                "password_hash",
                sqlmodel.sql.sqltypes.AutoString(length=200),
                nullable=True,
            ),
        )

    if not _column_exists("users", "password_salt"):
        op.add_column(
            "users",
            sa.Column(
                "password_salt",
                sqlmodel.sql.sqltypes.AutoString(length=64),
                nullable=True,
            ),
        )

    if not _column_exists("users", "is_admin"):
        op.add_column(
            "users",
            sa.Column("is_admin", sa.Boolean(), nullable=False, server_default=sa.false()),
        )
        op.create_index("ix_users_is_admin", "users", ["is_admin"])

    if not _column_exists("users", "remark"):
        op.add_column(
            "users",
            sa.Column(
                "remark",
                sqlmodel.sql.sqltypes.AutoString(length=240),
                nullable=False,
                server_default="",
            ),
        )

    connection = op.get_bind()
    rows = connection.execute(sa.text("SELECT id, name, username, remark FROM users")).mappings()
    for row in rows:
        name = str(row["name"] or "").strip()
        username = str(row["username"] or "").strip() or name
        remark = str(row["remark"] or "").strip() or name
        connection.execute(
            sa.text(
                """
                UPDATE users
                SET username = :username, remark = :remark, updated_at = :updated_at
                WHERE id = :id
                """
            ),
            {
                "id": row["id"],
                "username": username,
                "remark": remark,
                "updated_at": datetime.now(),
            },
        )


def downgrade() -> None:
    if not _table_exists("users"):
        return
    if _column_exists("users", "remark"):
        op.drop_column("users", "remark")
    if _column_exists("users", "is_admin"):
        op.drop_index("ix_users_is_admin", table_name="users")
        op.drop_column("users", "is_admin")
    if _column_exists("users", "password_salt"):
        op.drop_column("users", "password_salt")
    if _column_exists("users", "password_hash"):
        op.drop_column("users", "password_hash")
    if _column_exists("users", "username"):
        op.drop_index("ix_users_username", table_name="users")
        op.drop_column("users", "username")
