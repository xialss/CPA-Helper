"""Store Codex keeper quota reset times.

Revision ID: 20260511_0004
Revises: 20260511_0003
Create Date: 2026-05-11
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260511_0004"
down_revision: str | None = "20260511_0003"
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
    table_name = "codex_keeper_auth_states"
    if not _table_exists(table_name):
        return
    if not _column_exists(table_name, "primary_reset_at"):
        op.add_column(table_name, sa.Column("primary_reset_at", sa.DateTime(), nullable=True))
    if not _column_exists(table_name, "secondary_reset_at"):
        op.add_column(table_name, sa.Column("secondary_reset_at", sa.DateTime(), nullable=True))


def downgrade() -> None:
    table_name = "codex_keeper_auth_states"
    if not _table_exists(table_name):
        return
    if _column_exists(table_name, "secondary_reset_at"):
        op.drop_column(table_name, "secondary_reset_at")
    if _column_exists(table_name, "primary_reset_at"):
        op.drop_column(table_name, "primary_reset_at")
