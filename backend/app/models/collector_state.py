from datetime import datetime

from sqlalchemy import Column, Text
from sqlmodel import Field, SQLModel


class CollectorState(SQLModel, table=True):
    __tablename__ = "collector_state"

    id: int = Field(default=1, primary_key=True)
    running: bool = False
    last_poll_at: datetime | None = None
    last_success_at: datetime | None = None
    last_error: str | None = Field(default=None, sa_column=Column(Text, nullable=True))
    remote_enabled: bool | None = None
    records_collected: int = 0
    updated_at: datetime = Field(default_factory=datetime.now)

