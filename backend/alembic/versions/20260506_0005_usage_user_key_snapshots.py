"""Add API key descriptions and usage ownership snapshots.

Revision ID: 20260506_0005
Revises: 20260506_0004
Create Date: 2026-05-06
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel

from alembic import op

revision: str = "20260506_0005"
down_revision: str | None = "20260506_0004"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def _table_exists(table_name: str) -> bool:
    return sa.inspect(op.get_bind()).has_table(table_name)


def _column_exists(table_name: str, column_name: str) -> bool:
    columns = sa.inspect(op.get_bind()).get_columns(table_name)
    return any(column["name"] == column_name for column in columns)


def _index_exists(table_name: str, index_name: str) -> bool:
    indexes = sa.inspect(op.get_bind()).get_indexes(table_name)
    return any(index["name"] == index_name for index in indexes)


def upgrade() -> None:
    connection = op.get_bind()

    if _table_exists("user_api_keys") and not _column_exists("user_api_keys", "description"):
        op.add_column(
            "user_api_keys",
            sa.Column(
                "description",
                sqlmodel.sql.sqltypes.AutoString(length=240),
                nullable=False,
                server_default="",
            ),
        )

    if _table_exists("usage_records"):
        if not _column_exists("usage_records", "usage_user_id"):
            op.add_column("usage_records", sa.Column("usage_user_id", sa.Integer(), nullable=True))
            op.create_index("ix_usage_records_usage_user_id", "usage_records", ["usage_user_id"])
        if not _column_exists("usage_records", "usage_user_account"):
            op.add_column(
                "usage_records",
                sa.Column(
                    "usage_user_account",
                    sqlmodel.sql.sqltypes.AutoString(length=120),
                    nullable=True,
                ),
            )
            op.create_index(
                "ix_usage_records_usage_user_account",
                "usage_records",
                ["usage_user_account"],
            )
        if not _column_exists("usage_records", "api_key_description"):
            op.add_column(
                "usage_records",
                sa.Column(
                    "api_key_description",
                    sqlmodel.sql.sqltypes.AutoString(length=240),
                    nullable=True,
                ),
            )

    if _table_exists("users"):
        _dedupe_usernames(connection)
        if not _index_exists("users", "uq_users_username"):
            op.create_index("uq_users_username", "users", ["username"], unique=True)

    if _table_exists("usage_records") and _table_exists("users") and _table_exists("user_api_keys"):
        _backfill_usage_snapshots(connection)


def _merge_duplicate_username(connection, target_user_id: int, username: str) -> None:
    duplicate_rows = connection.execute(
        sa.text("SELECT id FROM users WHERE username = :username AND id != :id"),
        {"username": username, "id": target_user_id},
    ).all()
    for duplicate in duplicate_rows:
        duplicate_id = int(duplicate[0])
        connection.execute(
            sa.text("UPDATE OR IGNORE user_api_keys SET user_id = :target WHERE user_id = :source"),
            {"target": target_user_id, "source": duplicate_id},
        )
        connection.execute(
            sa.text("DELETE FROM user_api_keys WHERE user_id = :source"),
            {"source": duplicate_id},
        )
        connection.execute(sa.text("DELETE FROM users WHERE id = :id"), {"id": duplicate_id})


def _dedupe_usernames(connection) -> None:
    rows = connection.execute(
        sa.text("SELECT id, username FROM users ORDER BY id")
    ).mappings().all()
    seen: dict[str, int] = {}
    for row in rows:
        username = str(row["username"] or "").strip() or f"user-{row['id']}"
        user_id = int(row["id"])
        if username not in seen:
            seen[username] = user_id
            if username != row["username"]:
                connection.execute(
                    sa.text("UPDATE users SET username = :username WHERE id = :id"),
                    {"username": username, "id": user_id},
                )
            continue
        _merge_duplicate_username(connection, seen[username], username)


def _backfill_usage_snapshots(connection) -> None:
    rows = connection.execute(
        sa.text(
            """
            SELECT
                uak.api_key_hash AS api_key_hash,
                uak.user_id AS user_id,
                uak.description AS description,
                users.username AS username
            FROM user_api_keys AS uak
            JOIN users ON users.id = uak.user_id
            """
        )
    ).mappings().all()
    for row in rows:
        username = str(row["username"] or "").strip()
        description = str(row["description"] or "").strip() or None
        connection.execute(
            sa.text(
                """
                UPDATE usage_records
                SET usage_user_id = :user_id,
                    usage_user_account = :username,
                    api_key_description = :description
                WHERE api_key_hash = :api_key_hash
                  AND (usage_user_account IS NULL OR usage_user_account = '')
                """
            ),
            {
                "user_id": row["user_id"],
                "username": username,
                "description": description,
                "api_key_hash": row["api_key_hash"],
            },
        )


def downgrade() -> None:
    if _table_exists("users") and _index_exists("users", "uq_users_username"):
        op.drop_index("uq_users_username", table_name="users")

    if _table_exists("usage_records"):
        if _column_exists("usage_records", "api_key_description"):
            op.drop_column("usage_records", "api_key_description")
        if _column_exists("usage_records", "usage_user_account"):
            op.drop_index("ix_usage_records_usage_user_account", table_name="usage_records")
            op.drop_column("usage_records", "usage_user_account")
        if _column_exists("usage_records", "usage_user_id"):
            op.drop_index("ix_usage_records_usage_user_id", table_name="usage_records")
            op.drop_column("usage_records", "usage_user_id")

    if _table_exists("user_api_keys") and _column_exists("user_api_keys", "description"):
        op.drop_column("user_api_keys", "description")
