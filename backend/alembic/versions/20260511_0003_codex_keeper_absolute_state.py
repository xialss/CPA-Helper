"""Use absolute Codex keeper account state.

Revision ID: 20260511_0003
Revises: 20260511_0002
Create Date: 2026-05-11
"""

from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260511_0003"
down_revision: str | None = "20260511_0002"
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
    table_name = "codex_keeper_auth_states"
    if not _table_exists(table_name):
        return

    if not _column_exists(table_name, "disabled"):
        op.add_column(
            table_name,
            sa.Column("disabled", sa.Boolean(), nullable=False, server_default=sa.false()),
        )
    if not _column_exists(table_name, "priority"):
        op.add_column(table_name, sa.Column("priority", sa.Integer(), nullable=True))
    if not _column_exists(table_name, "restore_priority"):
        op.add_column(table_name, sa.Column("restore_priority", sa.Integer(), nullable=True))

    if _column_exists(table_name, "last_priority"):
        op.execute(
            sa.text(
                "UPDATE codex_keeper_auth_states "
                "SET priority = COALESCE(priority, last_priority)"
            )
        )
    if _column_exists(table_name, "original_priority"):
        op.execute(
            sa.text(
                "UPDATE codex_keeper_auth_states "
                "SET priority = COALESCE(priority, original_priority), "
                "restore_priority = CASE "
                "WHEN restore_priority IS NULL AND original_priority > 20 "
                "THEN original_priority ELSE restore_priority END"
            )
        )
    if _column_exists(table_name, "status_disabled_by_keeper"):
        op.execute(
            sa.text(
                "UPDATE codex_keeper_auth_states "
                "SET disabled = CASE "
                "WHEN status_disabled_by_keeper = 1 THEN 1 ELSE disabled END"
            )
        )

    if _index_exists(table_name, "ix_codex_keeper_auth_states_keeper_action"):
        op.drop_index("ix_codex_keeper_auth_states_keeper_action", table_name=table_name)

    old_columns = [
        column_name
        for column_name in [
            "original_priority",
            "last_priority",
            "keeper_action",
            "status_disabled_by_keeper",
            "priority_degraded_by_keeper",
        ]
        if _column_exists(table_name, column_name)
    ]
    if old_columns:
        with op.batch_alter_table(table_name) as batch_op:
            for column_name in old_columns:
                batch_op.drop_column(column_name)


def downgrade() -> None:
    table_name = "codex_keeper_auth_states"
    if not _table_exists(table_name):
        return

    old_column_defs = [
        ("original_priority", sa.Column("original_priority", sa.Integer(), nullable=True)),
        ("last_priority", sa.Column("last_priority", sa.Integer(), nullable=True)),
        (
            "keeper_action",
            sa.Column("keeper_action", sa.String(length=40), nullable=False, server_default="none"),
        ),
        (
            "status_disabled_by_keeper",
            sa.Column(
                "status_disabled_by_keeper",
                sa.Boolean(),
                nullable=False,
                server_default=sa.false(),
            ),
        ),
        (
            "priority_degraded_by_keeper",
            sa.Column(
                "priority_degraded_by_keeper",
                sa.Boolean(),
                nullable=False,
                server_default=sa.false(),
            ),
        ),
    ]
    with op.batch_alter_table(table_name) as batch_op:
        for column_name, column in old_column_defs:
            if not _column_exists(table_name, column_name):
                batch_op.add_column(column)

    if _column_exists(table_name, "priority"):
        op.execute(
            sa.text("UPDATE codex_keeper_auth_states SET last_priority = priority")
        )
    if _column_exists(table_name, "restore_priority"):
        op.execute(
            sa.text("UPDATE codex_keeper_auth_states SET original_priority = restore_priority")
        )
    if _column_exists(table_name, "disabled"):
        op.execute(
            sa.text(
                "UPDATE codex_keeper_auth_states "
                "SET status_disabled_by_keeper = disabled, "
                "keeper_action = CASE WHEN disabled = 1 THEN 'status_disabled' ELSE 'none' END"
            )
        )

    if not _index_exists(table_name, "ix_codex_keeper_auth_states_keeper_action"):
        op.create_index(
            "ix_codex_keeper_auth_states_keeper_action",
            table_name,
            ["keeper_action"],
        )

    new_columns = [
        column_name
        for column_name in ["disabled", "priority", "restore_priority"]
        if _column_exists(table_name, column_name)
    ]
    if new_columns:
        with op.batch_alter_table(table_name) as batch_op:
            for column_name in new_columns:
                batch_op.drop_column(column_name)
