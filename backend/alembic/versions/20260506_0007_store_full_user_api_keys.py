"""Store full user API keys locally.

Revision ID: 20260506_0007
Revises: 20260506_0006
Create Date: 2026-05-06
"""

import hashlib
import json
from collections.abc import Mapping, Sequence
from typing import Any

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0007"
down_revision: str | None = "20260506_0006"
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


def _hash_api_key(api_key: str) -> str:
    return hashlib.sha256((api_key.strip() or "unknown").encode()).hexdigest()


def _mask_secret(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = value.strip()
    if not normalized:
        return None
    if normalized == "unknown":
        return "unknown"
    if len(normalized) <= 8:
        return f"{normalized[:2]}...{normalized[-2:]}"
    return f"{normalized[:6]}...{normalized[-4:]}"


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


def _raw_api_key_from_usage(raw_json: str, api_key_hash: str) -> str | None:
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return None
    candidate = _find_first(parsed, API_KEY_KEYS)
    if not isinstance(candidate, str):
        return None
    normalized = candidate.strip()
    if not normalized or _hash_api_key(normalized) != api_key_hash:
        return None
    return normalized


def upgrade() -> None:
    if not _table_exists("user_api_keys"):
        return

    if not _column_exists("user_api_keys", "api_key"):
        with op.batch_alter_table("user_api_keys") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "api_key",
                    sqlmodel.sql.sqltypes.AutoString(length=400),
                    nullable=True,
                )
            )

    _backfill_api_keys()

    if _column_exists("user_api_keys", "api_key_masked"):
        with op.batch_alter_table("user_api_keys") as batch_op:
            batch_op.drop_column("api_key_masked")


def _backfill_api_keys() -> None:
    connection = op.get_bind()
    rows = connection.execute(
        sa.text("SELECT api_key_hash FROM user_api_keys WHERE api_key IS NULL OR api_key = ''")
    ).mappings()

    for row in rows:
        api_key_hash = str(row["api_key_hash"])
        api_key = _usage_api_key(connection, api_key_hash)
        if api_key is None:
            continue
        connection.execute(
            sa.text(
                """
                UPDATE user_api_keys
                SET api_key = :api_key
                WHERE api_key_hash = :api_key_hash
                """
            ),
            {"api_key": api_key, "api_key_hash": api_key_hash},
        )


def _usage_api_key(connection, api_key_hash: str) -> str | None:
    if not _table_exists("usage_records"):
        return None
    rows = connection.execute(
        sa.text(
            """
            SELECT raw_json
            FROM usage_records
            WHERE api_key_hash = :api_key_hash
            ORDER BY timestamp DESC
            """
        ),
        {"api_key_hash": api_key_hash},
    ).mappings()
    for row in rows:
        raw_json = row.get("raw_json")
        if not isinstance(raw_json, str):
            continue
        api_key = _raw_api_key_from_usage(raw_json, api_key_hash)
        if api_key is not None:
            return api_key
    return None


def downgrade() -> None:
    if not _table_exists("user_api_keys"):
        return

    if not _column_exists("user_api_keys", "api_key_masked"):
        with op.batch_alter_table("user_api_keys") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "api_key_masked",
                    sqlmodel.sql.sqltypes.AutoString(length=80),
                    nullable=True,
                )
            )
        _backfill_masked_keys()

    if _column_exists("user_api_keys", "api_key"):
        with op.batch_alter_table("user_api_keys") as batch_op:
            batch_op.drop_column("api_key")


def _backfill_masked_keys() -> None:
    connection = op.get_bind()
    rows = connection.execute(
        sa.text("SELECT api_key_hash, api_key FROM user_api_keys")
    ).mappings()
    for row in rows:
        connection.execute(
            sa.text(
                """
                UPDATE user_api_keys
                SET api_key_masked = :api_key_masked
                WHERE api_key_hash = :api_key_hash
                """
            ),
            {
                "api_key_hash": row["api_key_hash"],
                "api_key_masked": _mask_secret(row.get("api_key")),
            },
        )
