"""Rename user remark to nickname.

Revision ID: 20260508_0001
Revises: 20260507_0001
Create Date: 2026-05-08
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260508_0001"
down_revision: str | None = "20260507_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def upgrade() -> None:
    if not _table_exists("users"):
        return

    if _column_exists("users", "nickname"):
        return

    with op.batch_alter_table("users") as batch_op:
        batch_op.alter_column(
            "remark",
            new_column_name="nickname",
            existing_type=sa.String(length=240),
            existing_nullable=False,
        )


def downgrade() -> None:
    if not _table_exists("users"):
        return

    if _column_exists("users", "remark"):
        return

    with op.batch_alter_table("users") as batch_op:
        batch_op.alter_column(
            "nickname",
            new_column_name="remark",
            existing_type=sa.String(length=240),
            existing_nullable=False,
        )
