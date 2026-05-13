"""Add users and API key bindings.

Revision ID: 20260506_0003
Revises: 20260506_0002
Create Date: 2026-05-06
"""

from collections.abc import Sequence
from datetime import datetime

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0003"
down_revision: str | None = "20260506_0002"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def upgrade() -> None:
    if not _table_exists("users"):
        op.create_table(
            "users",
            sa.Column("id", sa.Integer(), nullable=False),
            sa.Column("name", sqlmodel.sql.sqltypes.AutoString(length=120), nullable=False),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.PrimaryKeyConstraint("id"),
        )
        op.create_index("ix_users_name", "users", ["name"])

    if not _table_exists("user_api_keys"):
        op.create_table(
            "user_api_keys",
            sa.Column(
                "api_key_hash",
                sqlmodel.sql.sqltypes.AutoString(length=64),
                nullable=False,
            ),
            sa.Column("user_id", sa.Integer(), nullable=False),
            sa.Column("api_key_masked", sqlmodel.sql.sqltypes.AutoString(length=80), nullable=True),
            sa.Column("created_at", sa.DateTime(), nullable=False),
            sa.Column("updated_at", sa.DateTime(), nullable=False),
            sa.ForeignKeyConstraint(["user_id"], ["users.id"]),
            sa.PrimaryKeyConstraint("api_key_hash"),
        )
        op.create_index("ix_user_api_keys_user_id", "user_api_keys", ["user_id"])

    if _table_exists("api_key_aliases"):
        _migrate_aliases()


def _migrate_aliases() -> None:
    connection = op.get_bind()
    now = datetime.now()
    alias_rows = connection.execute(
        sa.text("SELECT api_key_hash, alias, updated_at FROM api_key_aliases")
    ).mappings()
    user_ids_by_name: dict[str, int] = {}

    for row in alias_rows:
        name = str(row["alias"]).strip()
        if not name:
            continue
        user_id = user_ids_by_name.get(name)
        if user_id is None:
            result = connection.execute(
                sa.text(
                    """
                    INSERT INTO users (name, created_at, updated_at)
                    VALUES (:name, :created_at, :updated_at)
                    """
                ),
                {"name": name, "created_at": now, "updated_at": now},
            )
            user_id = int(result.lastrowid)
            user_ids_by_name[name] = user_id

        connection.execute(
            sa.text(
                """
                INSERT OR IGNORE INTO user_api_keys
                    (api_key_hash, user_id, api_key_masked, created_at, updated_at)
                VALUES
                    (:api_key_hash, :user_id, NULL, :created_at, :updated_at)
                """
            ),
            {
                "api_key_hash": str(row["api_key_hash"]),
                "user_id": user_id,
                "created_at": now,
                "updated_at": row.get("updated_at") or now,
            },
        )


def downgrade() -> None:
    if _table_exists("user_api_keys"):
        op.drop_index("ix_user_api_keys_user_id", table_name="user_api_keys")
        op.drop_table("user_api_keys")
    if _table_exists("users"):
        op.drop_index("ix_users_name", table_name="users")
        op.drop_table("users")
