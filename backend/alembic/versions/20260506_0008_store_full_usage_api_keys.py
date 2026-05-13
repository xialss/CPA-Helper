"""Store full API keys on usage records.

Revision ID: 20260506_0008
Revises: 20260506_0007
Create Date: 2026-05-06
"""

import hashlib
import json
from collections.abc import Mapping, Sequence
from typing import Any

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0008"
down_revision: str | None = "20260506_0007"
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


def _mask_secret(value: str | None) -> str:
    if value is None:
        return "unknown"
    normalized = value.strip()
    if not normalized:
        return "unknown"
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
    if not _table_exists("usage_records"):
        return

    if not _column_exists("usage_records", "api_key"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "api_key",
                    sqlmodel.sql.sqltypes.AutoString(length=400),
                    nullable=True,
                )
            )

    _backfill_api_keys()

    if _column_exists("usage_records", "api_key_masked"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.drop_column("api_key_masked")


def _backfill_api_keys() -> None:
    connection = op.get_bind()
    bound_by_hash = _bound_api_keys(connection)
    has_masked = _column_exists("usage_records", "api_key_masked")
    masked_select = ", api_key_masked" if has_masked else ""
    rows = connection.execute(
        sa.text(
            f"""
            SELECT id, api_key_hash, raw_json{masked_select}
            FROM usage_records
            WHERE api_key IS NULL OR api_key = ''
            """
        )
    ).mappings()
    for row in rows:
        api_key_hash = str(row["api_key_hash"])
        api_key = (
            bound_by_hash.get(api_key_hash)
            or _raw_api_key_from_usage(str(row["raw_json"]), api_key_hash)
            or (str(row["api_key_masked"]) if has_masked and row.get("api_key_masked") else None)
            or "unknown"
        )
        connection.execute(
            sa.text("UPDATE usage_records SET api_key = :api_key WHERE id = :id"),
            {"api_key": api_key, "id": row["id"]},
        )


def _bound_api_keys(connection) -> dict[str, str]:
    if not _table_exists("user_api_keys") or not _column_exists("user_api_keys", "api_key"):
        return {}
    rows = connection.execute(
        sa.text(
            """
            SELECT api_key_hash, api_key
            FROM user_api_keys
            WHERE api_key IS NOT NULL AND api_key != ''
            """
        )
    ).mappings()
    return {str(row["api_key_hash"]): str(row["api_key"]) for row in rows}


def downgrade() -> None:
    if not _table_exists("usage_records"):
        return

    if not _column_exists("usage_records", "api_key_masked"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.add_column(
                sa.Column(
                    "api_key_masked",
                    sqlmodel.sql.sqltypes.AutoString(length=80),
                    nullable=True,
                )
            )
        _backfill_masked_keys()

    if _column_exists("usage_records", "api_key"):
        with op.batch_alter_table("usage_records") as batch_op:
            batch_op.drop_column("api_key")


def _backfill_masked_keys() -> None:
    connection = op.get_bind()
    rows = connection.execute(
        sa.text("SELECT id, api_key FROM usage_records")
    ).mappings()
    for row in rows:
        connection.execute(
            sa.text(
                """
                UPDATE usage_records
                SET api_key_masked = :api_key_masked
                WHERE id = :id
                """
            ),
            {"id": row["id"], "api_key_masked": _mask_secret(row.get("api_key"))},
        )
