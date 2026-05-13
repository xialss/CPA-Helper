"""Rename Codex keeper reason columns to latest action.

Revision ID: 20260511_0005
Revises: 20260511_0004
Create Date: 2026-05-11
"""

import re
from collections.abc import Sequence

import sqlalchemy as sa

from alembic import op

revision: str = "20260511_0005"
down_revision: str | None = "20260511_0004"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


TABLES = (
    ("codex_keeper_auth_states", "auth_name"),
    ("codex_keeper_run_accounts", "id"),
)


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    inspector = sa.inspect(op.get_bind())
    return any(column["name"] == column_name for column in inspector.get_columns(table_name))


def _latest_action(value: str | None) -> str | None:
    if value is None:
        return None
    text = value.strip()
    if not text:
        return None
    if text == "已禁用凭证":
        return "禁用凭证"
    if text.startswith("已禁用凭证："):
        return f"禁用凭证：{text.removeprefix('已禁用凭证：')}"
    if text.endswith("，dry-run 未禁用") and not text.startswith("模拟禁用："):
        return f"模拟禁用：{text.removesuffix('，dry-run 未禁用')}"
    quota_match = re.match(r"^额度达到阈值\s+(.+)$", text)
    if quota_match:
        return f"降为低优先级：额度使用率达到阈值 {quota_match.group(1)}"
    type_priority_match = re.match(
        r"^按账号类型\s+(\S+)\s+应用 priority\s+(-?\d+)(.*)$",
        text,
    )
    if type_priority_match:
        account_type, priority, tail = type_priority_match.groups()
        prefix = "模拟应用类型优先级" if "dry-run" in tail else "应用类型优先级"
        return f"{prefix}：{account_type} -> priority {priority}"
    if text == "priority 小于 -1，保持用户低优先级":
        return "保持低优先级：priority 小于 -1"
    if text == "额度恢复，已恢复用户高优先级":
        return "恢复高优先级：额度已恢复"
    if text == "额度恢复，dry-run 未恢复用户高优先级":
        return "模拟恢复高优先级：额度已恢复"
    return text


def _legacy_reason(value: str | None) -> str | None:
    if value is None:
        return None
    text = value.strip()
    if not text:
        return None
    if text == "禁用凭证":
        return "已禁用凭证"
    if text.startswith("禁用凭证："):
        return f"已禁用凭证：{text.removeprefix('禁用凭证：')}"
    if text.startswith("模拟禁用："):
        return f"{text.removeprefix('模拟禁用：')}，dry-run 未禁用"
    quota_match = re.match(r"^降为低优先级：额度使用率达到阈值\s+(.+)$", text)
    if quota_match:
        return f"额度达到阈值 {quota_match.group(1)}"
    type_priority_match = re.match(
        r"^(模拟应用类型优先级|应用类型优先级)：(\S+) -> priority (-?\d+)$",
        text,
    )
    if type_priority_match:
        prefix, account_type, priority = type_priority_match.groups()
        suffix = "，dry-run 未写入" if prefix.startswith("模拟") else ""
        return f"按账号类型 {account_type} 应用 priority {priority}{suffix}"
    if text == "保持低优先级：priority 小于 -1":
        return "priority 小于 -1，保持用户低优先级"
    if text == "恢复高优先级：额度已恢复":
        return "额度恢复，已恢复用户高优先级"
    if text == "模拟恢复高优先级：额度已恢复":
        return "额度恢复，dry-run 未恢复用户高优先级"
    return text


def _ensure_column(table_name: str, column_name: str) -> None:
    if _column_exists(table_name, column_name):
        return
    with op.batch_alter_table(table_name) as batch_op:
        batch_op.add_column(sa.Column(column_name, sa.Text(), nullable=True))


def _drop_column_if_exists(table_name: str, column_name: str) -> None:
    if not _column_exists(table_name, column_name):
        return
    with op.batch_alter_table(table_name) as batch_op:
        batch_op.drop_column(column_name)


def _copy_column(
    table_name: str,
    id_column: str,
    source_column: str,
    target_column: str,
    *,
    to_latest_action: bool,
) -> None:
    if not _column_exists(table_name, source_column):
        return
    bind = op.get_bind()
    rows = list(
        bind.execute(
            sa.text(
                f"SELECT {id_column}, {source_column} "
                f"FROM {table_name} WHERE {source_column} IS NOT NULL"
            )
        ).mappings()
    )
    converter = _latest_action if to_latest_action else _legacy_reason
    for row in rows:
        target_value = converter(row[source_column])
        bind.execute(
            sa.text(
                f"UPDATE {table_name} SET {target_column} = :target_value "
                f"WHERE {id_column} = :row_id"
            ),
            {"target_value": target_value, "row_id": row[id_column]},
        )


def upgrade() -> None:
    for table_name, id_column in TABLES:
        if not _table_exists(table_name):
            continue
        _ensure_column(table_name, "latest_action")
        if _column_exists(table_name, "reason"):
            _copy_column(
                table_name,
                id_column,
                "reason",
                "latest_action",
                to_latest_action=True,
            )
            _drop_column_if_exists(table_name, "reason")
        else:
            _copy_column(
                table_name,
                id_column,
                "latest_action",
                "latest_action",
                to_latest_action=True,
            )


def downgrade() -> None:
    for table_name, id_column in reversed(TABLES):
        if not _table_exists(table_name):
            continue
        _ensure_column(table_name, "reason")
        if _column_exists(table_name, "latest_action"):
            _copy_column(
                table_name,
                id_column,
                "latest_action",
                "reason",
                to_latest_action=False,
            )
            _drop_column_if_exists(table_name, "latest_action")
