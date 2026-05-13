"""Add Codex keeper settings and auth states.

Revision ID: 20260511_0001
Revises: 20260509_0001
Create Date: 2026-05-11
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260511_0001"
down_revision: str | None = "20260509_0001"
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
    if _table_exists("app_settings"):
        if not _column_exists("app_settings", "codex_keeper_settings"):
            op.add_column(
                "app_settings",
                sa.Column(
                    "codex_keeper_settings",
                    sa.Text(),
                    nullable=False,
                    server_default="{}",
                ),
            )
        if not _column_exists("app_settings", "codex_keeper_priority_rules"):
            op.add_column(
                "app_settings",
                sa.Column(
                    "codex_keeper_priority_rules",
                    sa.Text(),
                    nullable=False,
                    server_default="{}",
                ),
            )

    if not _table_exists("codex_keeper_auth_states"):
        op.create_table(
            "codex_keeper_auth_states",
            sa.Column("auth_name", sa.String(length=500), nullable=False),
            sa.Column("email", sa.String(length=320), nullable=True),
            sa.Column("account_type", sa.String(length=80), nullable=True),
            sa.Column("original_priority", sa.Integer(), nullable=True),
            sa.Column("last_priority", sa.Integer(), nullable=True),
            sa.Column("keeper_action", sa.String(length=40), nullable=False),
            sa.Column("status_disabled_by_keeper", sa.Boolean(), nullable=False),
            sa.Column("priority_degraded_by_keeper", sa.Boolean(), nullable=False),
            sa.Column("reason", sa.Text(), nullable=True),
            sa.Column("last_error", sa.Text(), nullable=True),
            sa.Column("last_status_code", sa.Integer(), nullable=True),
            sa.Column("primary_used_percent", sa.Integer(), nullable=True),
            sa.Column("secondary_used_percent", sa.Integer(), nullable=True),
            sa.Column("quota_threshold", sa.Integer(), nullable=True),
            sa.Column("last_checked_at", sa.DateTime(), nullable=True),
            sa.Column("last_healthy_at", sa.DateTime(), nullable=True),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("auth_name"),
        )
        op.create_index(
            "ix_codex_keeper_auth_states_keeper_action",
            "codex_keeper_auth_states",
            ["keeper_action"],
        )
        op.create_index(
            "ix_codex_keeper_auth_states_last_checked_at",
            "codex_keeper_auth_states",
            ["last_checked_at"],
        )


def downgrade() -> None:
    if _table_exists("codex_keeper_auth_states"):
        op.drop_index(
            "ix_codex_keeper_auth_states_last_checked_at",
            table_name="codex_keeper_auth_states",
        )
        op.drop_index(
            "ix_codex_keeper_auth_states_keeper_action",
            table_name="codex_keeper_auth_states",
        )
        op.drop_table("codex_keeper_auth_states")

    if _table_exists("app_settings"):
        if _column_exists("app_settings", "codex_keeper_priority_rules"):
            op.drop_column("app_settings", "codex_keeper_priority_rules")
        if _column_exists("app_settings", "codex_keeper_settings"):
            op.drop_column("app_settings", "codex_keeper_settings")
