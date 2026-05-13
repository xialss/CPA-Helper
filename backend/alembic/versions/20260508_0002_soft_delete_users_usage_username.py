"""Soft delete users and snapshot usage username.

Revision ID: 20260508_0002
Revises: 20260508_0001
Create Date: 2026-05-08
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260508_0002"
down_revision: str | None = "20260508_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def _index_exists(table_name: str, index_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    indexes = sa.inspect(op.get_bind()).get_indexes(table_name)
    return any(index["name"] == index_name for index in indexes)


def upgrade() -> None:
    if _table_exists("users") and not _column_exists("users", "deleted_at"):
        op.add_column("users", sa.Column("deleted_at", sa.DateTime(), nullable=True))
        op.create_index("ix_users_deleted_at", "users", ["deleted_at"])

    if not _table_exists("usage_records"):
        return

    if not _column_exists("usage_records", "usage_username"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.add_column(sa.Column("usage_username", sa.String(length=120), nullable=True))
        op.create_index("ix_usage_records_usage_username", "usage_records", ["usage_username"])

    if _column_exists("usage_records", "usage_user_id"):
        _backfill_usage_username()
        if _index_exists("usage_records", "ix_usage_records_usage_user_id"):
            op.drop_index("ix_usage_records_usage_user_id", table_name="usage_records")
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.drop_column("usage_user_id")


def downgrade() -> None:
    if _table_exists("usage_records") and not _column_exists("usage_records", "usage_user_id"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.add_column(sa.Column("usage_user_id", sa.Integer(), nullable=True))
        _backfill_usage_user_id()
        op.create_index("ix_usage_records_usage_user_id", "usage_records", ["usage_user_id"])

    if _table_exists("usage_records") and _column_exists("usage_records", "usage_username"):
        if _index_exists("usage_records", "ix_usage_records_usage_username"):
            op.drop_index("ix_usage_records_usage_username", table_name="usage_records")
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.drop_column("usage_username")

    if _table_exists("users") and _column_exists("users", "deleted_at"):
        if _index_exists("users", "ix_users_deleted_at"):
            op.drop_index("ix_users_deleted_at", table_name="users")
        with op.batch_alter_table("users") as batch_op:
            batch_op.drop_column("deleted_at")


def _backfill_usage_username() -> None:
    if not _table_exists("users"):
        return
    connection = op.get_bind()
    connection.execute(
        sa.text(
            """
            UPDATE usage_records
            SET usage_username = (
                SELECT users.username
                FROM users
                WHERE users.id = usage_records.usage_user_id
            )
            WHERE usage_user_id IS NOT NULL
              AND (usage_username IS NULL OR usage_username = '')
            """
        )
    )


def _backfill_usage_user_id() -> None:
    if not _table_exists("users") or not _column_exists("usage_records", "usage_username"):
        return
    connection = op.get_bind()
    connection.execute(
        sa.text(
            """
            UPDATE usage_records
            SET usage_user_id = (
                SELECT users.id
                FROM users
                WHERE users.username = usage_records.usage_username
            )
            WHERE usage_username IS NOT NULL
              AND usage_username != ''
            """
        )
    )
