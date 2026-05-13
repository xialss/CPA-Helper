"""Reserved no-op alias metadata migration.

Revision ID: 20260506_0002
Revises: 20260506_0001
Create Date: 2026-05-06
"""

from collections.abc import Sequence

revision: str = "20260506_0002"
down_revision: str | None = "20260506_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
