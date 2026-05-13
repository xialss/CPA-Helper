from datetime import datetime

from pydantic import BaseModel


class CollectorStatusResponse(BaseModel):
    enabled: bool
    running: bool
    queue_name: str
    batch_size: int
    poll_interval_seconds: float
    retry_interval_seconds: float
    last_poll_at: datetime | None
    last_success_at: datetime | None
    last_error: str | None
    remote_enabled: bool | None
    records_collected: int

