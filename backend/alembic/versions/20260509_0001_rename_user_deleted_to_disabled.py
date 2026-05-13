"""Rename user deleted timestamp to disabled timestamp.

Revision ID: 20260509_0001
Revises: 20260508_0002
Create Date: 2026-05-09
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260509_0001"
down_revision: str | None = "20260508_0002"
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
    if not _table_exists("users"):
        return
    if _index_exists("users", "ix_users_deleted_at"):
        op.drop_index("ix_users_deleted_at", table_name="users")
    if _column_exists("users", "deleted_at") and not _column_exists("users", "disabled_at"):
        with op.batch_alter_table("users") as batch_op:
            batch_op.alter_column("deleted_at", new_column_name="disabled_at")
    elif not _column_exists("users", "disabled_at"):
        op.add_column("users", sa.Column("disabled_at", sa.DateTime(), nullable=True))
    if not _index_exists("users", "ix_users_disabled_at"):
        op.create_index("ix_users_disabled_at", "users", ["disabled_at"])


def downgrade() -> None:
    if not _table_exists("users"):
        return
    if _index_exists("users", "ix_users_disabled_at"):
        op.drop_index("ix_users_disabled_at", table_name="users")
    if _column_exists("users", "disabled_at") and not _column_exists("users", "deleted_at"):
        with op.batch_alter_table("users") as batch_op:
            batch_op.alter_column("disabled_at", new_column_name="deleted_at")
    elif not _column_exists("users", "deleted_at"):
        op.add_column("users", sa.Column("deleted_at", sa.DateTime(), nullable=True))
    if not _index_exists("users", "ix_users_deleted_at"):
        op.create_index("ix_users_deleted_at", "users", ["deleted_at"])
