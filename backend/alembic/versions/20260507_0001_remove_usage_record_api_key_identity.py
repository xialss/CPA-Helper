"""Remove API key identity columns from usage records.

Revision ID: 20260507_0001
Revises: 20260506_0013
Create Date: 2026-05-07
"""

import hashlib
import json
from collections.abc import Mapping, Sequence
from typing import Any

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260507_0001"
down_revision: str | None = "20260506_0013"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None

API_KEY_KEYS = ("api_key", "apiKey", "apikey", "key")


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
    if not _table_exists("usage_records"):
        return

    if _index_exists("usage_records", "ix_usage_records_api_key_hash"):
        op.drop_index("ix_usage_records_api_key_hash", table_name="usage_records")

    with op.batch_alter_table("usage_records") as batch_op:
        if _column_exists("usage_records", "api_key"):
            batch_op.drop_column("api_key")
        if _column_exists("usage_records", "api_key_hash"):
            batch_op.drop_column("api_key_hash")


def downgrade() -> None:
    if not _table_exists("usage_records"):
        return

    with op.batch_alter_table("usage_records") as batch_op:
        if not _column_exists("usage_records", "api_key_hash"):
            batch_op.add_column(
                sa.Column(
                    "api_key_hash",
                    sqlmodel.sql.sqltypes.AutoString(length=64),
                    nullable=True,
                )
            )
        if not _column_exists("usage_records", "api_key"):
            batch_op.add_column(
                sa.Column(
                    "api_key",
                    sqlmodel.sql.sqltypes.AutoString(length=400),
                    nullable=True,
                )
            )

    _backfill_api_key_identity()

    if not _index_exists("usage_records", "ix_usage_records_api_key_hash"):
        op.create_index("ix_usage_records_api_key_hash", "usage_records", ["api_key_hash"])


def _backfill_api_key_identity() -> None:
    connection = op.get_bind()
    rows = connection.execute(sa.text("SELECT id, raw_json FROM usage_records")).mappings()
    for row in rows:
        api_key = _raw_api_key_from_usage(str(row["raw_json"])) or "unknown"
        connection.execute(
            sa.text(
                """
                UPDATE usage_records
                SET api_key = :api_key,
                    api_key_hash = :api_key_hash
                WHERE id = :id
                """
            ),
            {
                "id": row["id"],
                "api_key": api_key,
                "api_key_hash": _hash_api_key(api_key),
            },
        )


def _raw_api_key_from_usage(raw_json: str) -> str | None:
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return None
    candidate = _find_first(parsed, API_KEY_KEYS)
    if not isinstance(candidate, str):
        return None
    normalized = candidate.strip()
    return normalized or None


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


def _hash_api_key(api_key: str) -> str:
    return hashlib.sha256((api_key.strip() or "unknown").encode()).hexdigest()
