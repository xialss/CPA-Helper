from datetime import datetime

from sqlalchemy import Column, Text
from sqlmodel import Field, SQLModel


class UsageRecord(SQLModel, table=True):
    __tablename__ = "usage_records"

    id: int | None = Field(default=None, primary_key=True)
    created_at: datetime = Field(default_factory=datetime.now)
    timestamp: datetime = Field(index=True)
    usage_username: str | None = Field(default=None, index=True, max_length=120)
    api_key_description: str | None = Field(default=None, max_length=240)
    provider: str | None = Field(default=None, index=True, max_length=120)
    model: str | None = Field(default=None, index=True, max_length=180)
    endpoint: str | None = Field(default=None, index=True, max_length=240)
    source: str | None = Field(default=None, max_length=120)
    request_id: str | None = Field(default=None, index=True, max_length=240)
    auth: str | None = Field(default=None, max_length=120)
    latency_ms: float | None = None
    failed: bool = Field(default=False, index=True)
    input_tokens: int = 0
    output_tokens: int = 0
    cached_tokens: int = 0
    reasoning_tokens: int = 0
    total_tokens: int = 0
    dedupe_key: str = Field(index=True, unique=True, max_length=80)
    raw_json: str = Field(sa_column=Column(Text, nullable=False))
