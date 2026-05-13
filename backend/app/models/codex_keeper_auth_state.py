from datetime import datetime

from sqlalchemy import Column, Text
from sqlmodel import Field, SQLModel


class CodexKeeperAuthState(SQLModel, table=True):
    __tablename__ = "codex_keeper_auth_states"

    auth_name: str = Field(primary_key=True, max_length=500)
    email: str | None = Field(default=None, max_length=320)
    account_type: str | None = Field(default=None, max_length=80)
    disabled: bool = False
    priority: int | None = None
    restore_priority: int | None = None
    latest_action: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    last_error: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    last_status_code: int | None = None
    primary_used_percent: int | None = None
    secondary_used_percent: int | None = None
    primary_reset_at: datetime | None = None
    secondary_reset_at: datetime | None = None
    quota_threshold: int | None = None
    last_checked_at: datetime | None = None
    last_healthy_at: datetime | None = None
    created_at: datetime = Field(default_factory=datetime.now)
    updated_at: datetime = Field(default_factory=datetime.now)
