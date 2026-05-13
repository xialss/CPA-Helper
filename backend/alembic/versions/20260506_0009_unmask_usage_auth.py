"""Store unmasked usage auth values.

Revision ID: 20260506_0009
Revises: 20260506_0008
Create Date: 2026-05-06
"""

import json
from collections.abc import Mapping, Sequence
from typing import Any

import sqlalchemy as sa

from alembic import op

revision: str = "20260506_0009"
down_revision: str | None = "20260506_0008"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None

AUTH_KEYS = ("auth_type", "auth", "authentication")


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    if not _table_exists(table_name):
        return False
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def _find_first(data: Any, keys: tuple[str, ...]) -> Any:
    if isinstance(data, Mapping):
        for key in keys:
            if key in data:
                return data[key]
        for value in data.values():
            found = _find_first(value, keys)
            if found is not None:
                return found
    elif isinstance(data, list):
        for value in data:
            found = _find_first(value, keys)
            if found is not None:
                return found
    return None


def _to_str(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        normalized = value.strip()
        return normalized or None
    if isinstance(value, int | float | bool):
        return str(value)
    return None


def _auth_from_raw_json(raw_json: str) -> str | None:
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return None
    return _to_str(_find_first(parsed, AUTH_KEYS))


def upgrade() -> None:
    if not _table_exists("usage_records") or not _column_exists("usage_records", "auth"):
        return

    connection = op.get_bind()
    rows = connection.execute(
        sa.text("SELECT id, raw_json FROM usage_records WHERE raw_json IS NOT NULL")
    ).mappings()
    for row in rows:
        auth = _auth_from_raw_json(str(row["raw_json"]))
        if auth is None:
            continue
        connection.execute(
            sa.text("UPDATE usage_records SET auth = :auth WHERE id = :id"),
            {"id": row["id"], "auth": auth},
        )


def downgrade() -> None:
    pass
