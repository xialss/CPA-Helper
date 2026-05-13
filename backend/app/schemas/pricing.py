from datetime import datetime

from pydantic import BaseModel, Field, field_validator


class ModelPriceBase(BaseModel):
    provider: str = Field(min_length=1, max_length=120)
    model: str = Field(min_length=1, max_length=180)
    input_usd_per_million: float = Field(default=0.0, ge=0)
    output_usd_per_million: float = Field(default=0.0, ge=0)
    cached_usd_per_million: float = Field(default=0.0, ge=0)
    reasoning_usd_per_million: float = Field(default=0.0, ge=0)

    @field_validator("provider", "model")
    @classmethod
    def text_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("不能为空")
        return normalized


class ModelPriceCreate(ModelPriceBase):
    pass


class ModelPriceUpdate(ModelPriceBase):
    pass


class ModelPriceResponse(ModelPriceBase):
    id: int
    source: str = "manual"
    source_model: str | None = None
    auto_synced: bool = False
    last_synced_at: datetime | None = None
    updated_at: datetime


class ModelPriceSyncRequest(BaseModel):
    source_url: str | None = Field(default=None, max_length=1000)

    @field_validator("source_url")
    @classmethod
    def source_url_must_not_be_blank(cls, value: str | None) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        return normalized or None


class ModelPriceSyncResponse(BaseModel):
    source_url: str
    total_entries: int
    imported: int
    created: int
    updated: int
    unchanged: int
    skipped_manual: int
    skipped_invalid: int
