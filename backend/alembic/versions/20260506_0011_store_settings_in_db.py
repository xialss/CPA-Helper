"""Store application settings in SQLite.

Revision ID: 20260506_0011
Revises: 20260506_0010
Create Date: 2026-05-06
"""

import base64
import hashlib
import json
import os
import secrets
from collections.abc import Sequence
from datetime import datetime
from pathlib import Path
from typing import Any

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0011"
down_revision: str | None = "20260506_0010"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _legacy_settings_path() -> Path:
    configured = os.environ.get("CPA_HELPER_DATA_DIR")
    data_dir = Path(configured) if configured else Path(__file__).resolve().parents[3] / "data"
    return data_dir / "config" / "settings.json"


def _hash_password(password: str, salt: str) -> str:
    digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), 260_000)
    return base64.urlsafe_b64encode(digest).decode()


def _default_values() -> dict[str, Any]:
    salt = secrets.token_hex(16)
    return {
        "account_username": "admin",
        "account_password_hash": _hash_password("password", salt),
        "account_password_salt": salt,
        "account_must_change_password": True,
        "collector_enabled": False,
        "cliaproxy_url": "http://127.0.0.1:8317",
        "management_key": "",
        "queue_name": "usage",
        "batch_size": 100,
        "poll_interval_seconds": 2.0,
        "retry_interval_seconds": 10.0,
        "theme_preference": "system",
        "session_secret": secrets.token_urlsafe(48),
    }


def _legacy_values() -> dict[str, Any] | None:
    path = _legacy_settings_path()
    if not path.exists():
        return None
    raw = json.loads(path.read_text(encoding="utf-8"))
    defaults = _default_values()
    account = raw.get("account") if isinstance(raw.get("account"), dict) else {}
    collector = raw.get("collector") if isinstance(raw.get("collector"), dict) else {}
    return {
        "account_username": account.get("username") or defaults["account_username"],
        "account_password_hash": account.get("password_hash")
        or defaults["account_password_hash"],
        "account_password_salt": account.get("password_salt")
        or defaults["account_password_salt"],
        "account_must_change_password": bool(
            account.get("must_change_password", defaults["account_must_change_password"])
        ),
        "collector_enabled": bool(collector.get("enabled", defaults["collector_enabled"])),
        "cliaproxy_url": collector.get("cliaproxy_url") or defaults["cliaproxy_url"],
        "management_key": collector.get("management_key") or defaults["management_key"],
        "queue_name": collector.get("queue_name") or defaults["queue_name"],
        "batch_size": int(collector.get("batch_size", defaults["batch_size"])),
        "poll_interval_seconds": float(
            collector.get("poll_interval_seconds", defaults["poll_interval_seconds"])
        ),
        "retry_interval_seconds": float(
            collector.get("retry_interval_seconds", defaults["retry_interval_seconds"])
        ),
        "theme_preference": raw.get("theme_preference") or defaults["theme_preference"],
        "session_secret": raw.get("session_secret") or defaults["session_secret"],
    }


def _settings_row_exists() -> bool:
    if not _table_exists("app_settings"):
        return False
    row = op.get_bind().execute(sa.text("SELECT id FROM app_settings LIMIT 1")).first()
    return row is not None


def _insert_legacy_settings() -> None:
    if _settings_row_exists():
        return
    values = _legacy_values()
    if values is None:
        return
    now = datetime.now()
    op.get_bind().execute(
        sa.text(
            """
            INSERT INTO app_settings (
                id,
                account_username,
                account_password_hash,
                account_password_salt,
                account_must_change_password,
                collector_enabled,
                cliaproxy_url,
                management_key,
                queue_name,
                batch_size,
                poll_interval_seconds,
                retry_interval_seconds,
                theme_preference,
                session_secret,
                created_at,
                updated_at
            )
            VALUES (
                1,
                :account_username,
                :account_password_hash,
                :account_password_salt,
                :account_must_change_password,
                :collector_enabled,
                :cliaproxy_url,
                :management_key,
                :queue_name,
                :batch_size,
                :poll_interval_seconds,
                :retry_interval_seconds,
                :theme_preference,
                :session_secret,
                :created_at,
                :updated_at
            )
            """
        ),
        {**values, "created_at": now, "updated_at": now},
    )


def upgrade() -> None:
    if not _table_exists("app_settings"):
        op.create_table(
            "app_settings",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column(
                "account_username",
                sqlmodel.sql.sqltypes.AutoString(length=120),
                nullable=False,
            ),
            sa.Column(
                "account_password_hash",
                sqlmodel.sql.sqltypes.AutoString(length=200),
                nullable=False,
            ),
            sa.Column(
                "account_password_salt",
                sqlmodel.sql.sqltypes.AutoString(length=64),
                nullable=False,
            ),
            sa.Column("account_must_change_password", sa.Boolean(), nullable=False),
            sa.Column("collector_enabled", sa.Boolean(), nullable=False),
            sa.Column(
                "cliaproxy_url",
                sqlmodel.sql.sqltypes.AutoString(length=500),
                nullable=False,
            ),
            sa.Column(
                "management_key",
                sqlmodel.sql.sqltypes.AutoString(length=1000),
                nullable=False,
            ),
            sa.Column(
                "queue_name",
                sqlmodel.sql.sqltypes.AutoString(length=120),
                nullable=False,
            ),
            sa.Column("batch_size", sa.Integer(), nullable=False),
            sa.Column("poll_interval_seconds", sa.Float(), nullable=False),
            sa.Column("retry_interval_seconds", sa.Float(), nullable=False),
            sa.Column(
                "theme_preference",
                sqlmodel.sql.sqltypes.AutoString(length=16),
                nullable=False,
            ),
            sa.Column(
                "session_secret",
                sqlmodel.sql.sqltypes.AutoString(length=200),
                nullable=False,
            ),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
        )
    _insert_legacy_settings()


def downgrade() -> None:
    if _table_exists("app_settings"):
        op.drop_table("app_settings")
