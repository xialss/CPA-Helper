from datetime import datetime

from sqlmodel import Field, SQLModel, UniqueConstraint


class ModelPrice(SQLModel, table=True):
    __tablename__ = "model_prices"
    __table_args__ = (UniqueConstraint("provider", "model", name="uq_model_prices_provider_model"),)

    id: int | None = Field(default=None, primary_key=True)
    provider: str = Field(max_length=120)
    model: str = Field(max_length=180)
    input_usd_per_million: float = 0.0
    output_usd_per_million: float = 0.0
    cached_usd_per_million: float = 0.0
    reasoning_usd_per_million: float = 0.0
    source: str = Field(default="manual", max_length=40)
    source_model: str | None = Field(default=None, max_length=180)
    auto_synced: bool = False
    last_synced_at: datetime | None = None
    updated_at: datetime = Field(default_factory=datetime.now)
