"""Remove legacy user name and API key aliases.

Revision ID: 20260506_0010
Revises: 20260506_0009
Create Date: 2026-05-06
"""

from collections.abc import Sequence
from datetime import datetime

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0010"
down_revision: str | None = "20260506_0009"
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
    if _table_exists("api_key_aliases"):
        op.drop_table("api_key_aliases")

    if _column_exists("users", "name"):
        if _index_exists("users", "ix_users_name"):
            op.drop_index("ix_users_name", table_name="users")
        with op.batch_alter_table("users") as batch_op:
            batch_op.drop_column("name")


def downgrade() -> None:
    if _table_exists("users") and not _column_exists("users", "name"):
        with op.batch_alter_table("users") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "name",
                    sqlmodel.sql.sqltypes.AutoString(length=120),
                    nullable=False,
                    server_default="",
                )
            )
        op.get_bind().execute(
            sa.text(
                """
                UPDATE users
                SET name = COALESCE(NULLIF(remark, ''), NULLIF(username, ''), '未知用户')
                """
            )
        )
        if not _index_exists("users", "ix_users_name"):
            op.create_index("ix_users_name", "users", ["name"])

    if not _table_exists("api_key_aliases"):
        op.create_table(
            "api_key_aliases",
            sa.Column(
                "api_key_hash",
                sqlmodel.sql.sqltypes.AutoString(length=64),
                nullable=False,
            ),
            sa.Column("alias", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False, default=datetime.now),
            sa.PrimaryKeyConstraint("api_key_hash"),
        )
