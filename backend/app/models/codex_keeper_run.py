from datetime import datetime

from sqlalchemy import Column, Text
from sqlmodel import Field, SQLModel


class CodexKeeperRun(SQLModel, table=True):
    __tablename__ = "codex_keeper_runs"

    id: int | None = Field(default=None, primary_key=True)
    mode: str = Field(max_length=20, index=True)
    state: str = Field(max_length=20, index=True)
    detail: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    started_at: datetime = Field(index=True)
    finished_at: datetime | None = Field(default=None, index=True)

    total: int = 0
    healthy: int = 0
    status_disabled: int = 0
    status_enabled: int = 0
    priority_degraded: int = 0
    priority_restored: int = 0
    skipped: int = 0
    network_error: int = 0

    created_at: datetime = Field(default_factory=datetime.now)
    updated_at: datetime = Field(default_factory=datetime.now)


class CodexKeeperRunAccount(SQLModel, table=True):
    __tablename__ = "codex_keeper_run_accounts"

    id: int | None = Field(default=None, primary_key=True)
    run_id: int = Field(foreign_key="codex_keeper_runs.id", index=True)
    auth_name: str = Field(max_length=500, index=True)
    email: str | None = Field(default=None, max_length=320)
    result: str = Field(max_length=40, index=True)
    account_type: str | None = Field(default=None, max_length=80)
    priority: int | None = None
    disabled: bool | None = None
    keeper_action: str = Field(default="none", max_length=40)
    primary_used_percent: int | None = None
    secondary_used_percent: int | None = None
    quota_threshold: int | None = None
    last_status_code: int | None = None
    last_error: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    latest_action: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    checked_at: datetime = Field(index=True)
    created_at: datetime = Field(default_factory=datetime.now)
