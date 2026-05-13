from datetime import datetime

from sqlalchemy import Column, Text
from sqlmodel import Field, SQLModel


class AppSetting(SQLModel, table=True):
    __tablename__ = "app_settings"

    id: int = Field(default=1, primary_key=True)
    collector_enabled: bool = Field(default=False)
    cliaproxy_url: str = Field(default="http://127.0.0.1:8317", max_length=500)
    management_key: str = Field(default="", max_length=1000)
    queue_name: str = Field(default="usage", max_length=120)
    batch_size: int = Field(default=100)
    poll_interval_seconds: float = Field(default=2.0)
    retry_interval_seconds: float = Field(default=10.0)
    theme_preference: str = Field(default="system", max_length=16)
    codex_keeper_settings: str = Field(
        default="{}",
        sa_column=Column(Text, nullable=False, default="{}"),
    )
    codex_keeper_priority_rules: str = Field(
        default="{}",
        sa_column=Column(Text, nullable=False, default="{}"),
    )
    session_secret: str = Field(max_length=200)
    created_at: datetime = Field(default_factory=datetime.now)
    updated_at: datetime = Field(default_factory=datetime.now)
