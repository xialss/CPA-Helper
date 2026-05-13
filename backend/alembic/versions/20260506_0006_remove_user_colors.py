"""Remove user color fields.

Revision ID: 20260506_0006
Revises: 20260506_0005
Create Date: 2026-05-06
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0006"
down_revision: str | None = "20260506_0005"
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
    if _column_exists("users", "color"):
        with op.batch_alter_table("users") as batch_op:
            batch_op.drop_column("color")

    if _column_exists("api_key_aliases", "color"):
        with op.batch_alter_table("api_key_aliases") as batch_op:
            batch_op.drop_column("color")


def downgrade() -> None:
    if _table_exists("api_key_aliases") and not _column_exists("api_key_aliases", "color"):
        with op.batch_alter_table("api_key_aliases") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "color",
                    sqlmodel.sql.sqltypes.AutoString(length=16),
                    nullable=False,
                    server_default="#0891a3",
                )
            )

    if _table_exists("users") and not _column_exists("users", "color"):
        with op.batch_alter_table("users") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "color",
                    sqlmodel.sql.sqltypes.AutoString(length=16),
                    nullable=False,
                    server_default="#0891a3",
                )
            )
