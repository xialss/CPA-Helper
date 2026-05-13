"""Remove usage user account snapshot.

Revision ID: 20260506_0013
Revises: 20260506_0012
Create Date: 2026-05-06
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0013"
down_revision: str | None = "20260506_0012"
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


def _index_exists(table_name: str, index_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    return any(
        index["name"] == index_name
        for index in sa.inspect(op.get_bind()).get_indexes(table_name)
    )


def upgrade() -> None:
    if not _column_exists("usage_records", "usage_user_account"):
        return
    if _index_exists("usage_records", "ix_usage_records_usage_user_account"):
        op.drop_index("ix_usage_records_usage_user_account", table_name="usage_records")
    with op.batch_alter_table("usage_records") as batch_op:
        batch_op.drop_column("usage_user_account")


def downgrade() -> None:
    if _column_exists("usage_records", "usage_user_account"):
        return
    with op.batch_alter_table("usage_records") as batch_op:
        batch_op.add_column(
            sa.Column(
                "usage_user_account",
                sqlmodel.sql.sqltypes.AutoString(length=120),
                nullable=True,
            )
        )
    if not _index_exists("usage_records", "ix_usage_records_usage_user_account"):
        op.create_index(
            "ix_usage_records_usage_user_account",
            "usage_records",
            ["usage_user_account"],
        )
