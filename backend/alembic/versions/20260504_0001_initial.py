"""Initial CPA Helper schema.

Revision ID: 20260504_0001
Revises:
Create Date: 2026-05-04
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260504_0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def upgrade() -> None:
    if not _table_exists("usage_records"):
        op.create_table(
            "usage_records",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("timestamp", sa.DateTime(), nullable=False),
            sa.Column(
                "api_key_hash",
                sqlmodel.sql.sqltypes.AutoString(length=64),
                nullable=False,
            ),
            sa.Column(
                "api_key_masked",
                sqlmodel.sql.sqltypes.AutoString(length=80),
                nullable=False,
            ),
            sa.Column("provider", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=True),
            sa.Column("model", sqlmodel.sql.sqltypes.AutoString(length=180), nullable=True),
            sa.Column("endpoint", sqlmodel.sql.sqltypes.AutoString(length=240), nullable=True),
            sa.Column("source", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=True),
            sa.Column("request_id", sqlmodel.sql.sqltypes.AutoString(length=240), nullable=True),
            sa.Column("auth", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=True),
            sa.Column("latency_ms", sa.Float(), nullable=True),
            sa.Column("failed", sa.Boolean(), nullable=False),
            sa.Column("input_tokens", sa.Integer(), nullable=False),
            sa.Column("output_tokens", sa.Integer(), nullable=False),
            sa.Column("cached_tokens", sa.Integer(), nullable=False),
            sa.Column("reasoning_tokens", sa.Integer(), nullable=False),
            sa.Column("total_tokens", sa.Integer(), nullable=False),
            sa.Column("dedupe_key", sqlmodel.sql.sqltypes.AutoString(length=80), nullable=False),
            sa.Column("raw_json", sa.Text(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
        )
        op.create_index("ix_usage_records_api_key_hash", "usage_records", ["api_key_hash"])
        op.create_index(
            "ix_usage_records_dedupe_key",
            "usage_records",
            ["dedupe_key"],
            unique=True,
        )
        op.create_index("ix_usage_records_endpoint", "usage_records", ["endpoint"])
        op.create_index("ix_usage_records_failed", "usage_records", ["failed"])
        op.create_index("ix_usage_records_model", "usage_records", ["model"])
        op.create_index("ix_usage_records_provider", "usage_records", ["provider"])
        op.create_index("ix_usage_records_request_id", "usage_records", ["request_id"])
        op.create_index("ix_usage_records_timestamp", "usage_records", ["timestamp"])

    if not _table_exists("model_prices"):
        op.create_table(
            "model_prices",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("provider", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=False),
            sa.Column("model", sqlmodel.sql.sqltypes.AutoString(length=180), nullable=False),
            sa.Column("input_usd_per_million", sa.Float(), nullable=False),
            sa.Column("output_usd_per_million", sa.Float(), nullable=False),
            sa.Column("cached_usd_per_million", sa.Float(), nullable=False),
            sa.Column("reasoning_usd_per_million", sa.Float(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
            sa.UniqueConstraint("provider", "model", name="uq_model_prices_provider_model"),
        )

    if not _table_exists("api_key_aliases"):
        op.create_table(
            "api_key_aliases",
            sa.Column("api_key_hash", sqlmodel.sql.sqltypes.AutoString(length=64), nullable=False),
            sa.Column("alias", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("api_key_hash"),
        )

    if not _table_exists("collector_state"):
        op.create_table(
            "collector_state",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("running", sa.Boolean(), nullable=False),
            sa.Column("last_poll_at", sa.DateTime(), nullable=True),
            sa.Column("last_success_at", sa.DateTime(), nullable=True),
            sa.Column("last_error", sa.Text(), nullable=True),
            sa.Column("remote_enabled", sa.Boolean(), nullable=True),
            sa.Column("records_collected", sa.Integer(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
        )


def downgrade() -> None:
    op.drop_table("collector_state")
    op.drop_table("api_key_aliases")
    op.drop_table("model_prices")
    op.drop_index("ix_usage_records_timestamp", table_name="usage_records")
    op.drop_index("ix_usage_records_request_id", table_name="usage_records")
    op.drop_index("ix_usage_records_provider", table_name="usage_records")
    op.drop_index("ix_usage_records_model", table_name="usage_records")
    op.drop_index("ix_usage_records_failed", table_name="usage_records")
    op.drop_index("ix_usage_records_endpoint", table_name="usage_records")
    op.drop_index("ix_usage_records_dedupe_key", table_name="usage_records")
    op.drop_index("ix_usage_records_api_key_hash", table_name="usage_records")
    op.drop_table("usage_records")
