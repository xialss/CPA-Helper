"""Add Codex keeper run history tables.

Revision ID: 20260511_0002
Revises: 20260511_0001
Create Date: 2026-05-11
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260511_0002"
down_revision: str | None = "20260511_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _index_exists(table_name: str, index_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    indexes = sa.inspect(op.get_bind()).get_indexes(table_name)
    return any(index["name"] == index_name for index in indexes)


def _create_index_once(index_name: str, table_name: str, columns: list[str]) -> None:
    if not _index_exists(table_name, index_name):
        op.create_index(index_name, table_name, columns)


def _drop_index_once(index_name: str, table_name: str) -> None:
    if _index_exists(table_name, index_name):
        op.drop_index(index_name, table_name=table_name)


def upgrade() -> None:
    if not _table_exists("codex_keeper_runs"):
        op.create_table(
            "codex_keeper_runs",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("mode", sa.String(length=20), nullable=False),
            sa.Column("state", sa.String(length=20), nullable=False),
            sa.Column("detail", sa.Text(), nullable=True),
            sa.Column("started_at", sa.DateTime(), nullable=False),
            sa.Column("finished_at", sa.DateTime(), nullable=True),
            sa.Column("total", sa.Integer(), nullable=False),
            sa.Column("healthy", sa.Integer(), nullable=False),
            sa.Column("status_disabled", sa.Integer(), nullable=False),
            sa.Column("status_enabled", sa.Integer(), nullable=False),
            sa.Column("priority_degraded", sa.Integer(), nullable=False),
            sa.Column("priority_restored", sa.Integer(), nullable=False),
            sa.Column("skipped", sa.Integer(), nullable=False),
            sa.Column("network_error", sa.Integer(), nullable=False),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
        )
    _create_index_once("ix_codex_keeper_runs_finished_at", "codex_keeper_runs", ["finished_at"])
    _create_index_once("ix_codex_keeper_runs_mode", "codex_keeper_runs", ["mode"])
    _create_index_once("ix_codex_keeper_runs_started_at", "codex_keeper_runs", ["started_at"])
    _create_index_once("ix_codex_keeper_runs_state", "codex_keeper_runs", ["state"])

    if not _table_exists("codex_keeper_run_accounts"):
        op.create_table(
            "codex_keeper_run_accounts",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("run_id", sa.Integer(), nullable=False),
            sa.Column("auth_name", sa.String(length=500), nullable=False),
            sa.Column("email", sa.String(length=320), nullable=True),
            sa.Column("result", sa.String(length=40), nullable=False),
            sa.Column("account_type", sa.String(length=80), nullable=True),
            sa.Column("priority", sa.Integer(), nullable=True),
            sa.Column("disabled", sa.Boolean(), nullable=True),
            sa.Column("keeper_action", sa.String(length=40), nullable=False),
            sa.Column("primary_used_percent", sa.Integer(), nullable=True),
            sa.Column("secondary_used_percent", sa.Integer(), nullable=True),
            sa.Column("quota_threshold", sa.Integer(), nullable=True),
            sa.Column("last_status_code", sa.Integer(), nullable=True),
            sa.Column("last_error", sa.Text(), nullable=True),
            sa.Column("reason", sa.Text(), nullable=True),
            sa.Column("checked_at", sa.DateTime(), nullable=False),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.ForeignKeyConstraint(["run_id"], ["codex_keeper_runs.id"]),
            sa.PrimaryKeyConstraint("id"),
        )
    _create_index_once(
        "ix_codex_keeper_run_accounts_auth_name",
        "codex_keeper_run_accounts",
        ["auth_name"],
    )
    _create_index_once(
        "ix_codex_keeper_run_accounts_checked_at",
        "codex_keeper_run_accounts",
        ["checked_at"],
    )
    _create_index_once(
        "ix_codex_keeper_run_accounts_result",
        "codex_keeper_run_accounts",
        ["result"],
    )
    _create_index_once(
        "ix_codex_keeper_run_accounts_run_id",
        "codex_keeper_run_accounts",
        ["run_id"],
    )


def downgrade() -> None:
    _drop_index_once("ix_codex_keeper_run_accounts_run_id", "codex_keeper_run_accounts")
    _drop_index_once("ix_codex_keeper_run_accounts_result", "codex_keeper_run_accounts")
    _drop_index_once("ix_codex_keeper_run_accounts_checked_at", "codex_keeper_run_accounts")
    _drop_index_once("ix_codex_keeper_run_accounts_auth_name", "codex_keeper_run_accounts")
    if _table_exists("codex_keeper_run_accounts"):
        op.drop_table("codex_keeper_run_accounts")

    _drop_index_once("ix_codex_keeper_runs_state", "codex_keeper_runs")
    _drop_index_once("ix_codex_keeper_runs_started_at", "codex_keeper_runs")
    _drop_index_once("ix_codex_keeper_runs_mode", "codex_keeper_runs")
    _drop_index_once("ix_codex_keeper_runs_finished_at", "codex_keeper_runs")
    if _table_exists("codex_keeper_runs"):
        op.drop_table("codex_keeper_runs")
