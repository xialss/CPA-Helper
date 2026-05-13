"""Add model price sync metadata.

Revision ID: 20260506_0001
Revises: 20260504_0001
Create Date: 2026-05-06
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0001"
down_revision: str | None = "20260504_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _column_exists(table_name: str, column_name: str) -> bool:
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def upgrade() -> None:
    if not _column_exists("model_prices", "source"):
        op.add_column(
            "model_prices",
            sa.Column(
                "source",
                sqlmodel.sql.sqltypes.AutoString(length=40),
                nullable=False,
                server_default="manual",
            ),
        )
    if not _column_exists("model_prices", "source_model"):
        op.add_column(
            "model_prices",
            sa.Column(
                "source_model",
                sqlmodel.sql.sqltypes.AutoString(length=180),
                nullable=True,
            ),
        )
    if not _column_exists("model_prices", "auto_synced"):
        op.add_column(
            "model_prices",
            sa.Column("auto_synced", sa.Boolean(), nullable=False, server_default=sa.false()),
        )
    if not _column_exists("model_prices", "last_synced_at"):
        op.add_column("model_prices", sa.Column("last_synced_at", sa.DateTime(), nullable=True))


def downgrade() -> None:
    op.drop_column("model_prices", "last_synced_at")
    op.drop_column("model_prices", "auto_synced")
    op.drop_column("model_prices", "source_model")
    op.drop_column("model_prices", "source")
