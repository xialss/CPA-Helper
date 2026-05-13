from typing import Any

from pydantic import BaseModel, Field

JsonScalar = str | int | float | bool | None


class AvailableModelSource(BaseModel):
    api_key_hash: str
    api_key_preview: str
    description: str


class AvailableModelPrice(BaseModel):
    provider: str
    model: str
    input_usd_per_million: float
    output_usd_per_million: float
    cached_usd_per_million: float
    reasoning_usd_per_million: float


class AvailableModelItem(BaseModel):
    id: str
    name: str
    object: str | None = None
    owner: str | None = None
    created: int | None = None
    metadata: dict[str, JsonScalar] = Field(default_factory=dict)
    price: AvailableModelPrice | None = None
    sources: list[AvailableModelSource] = Field(default_factory=list)


class AvailableModelKeyError(BaseModel):
    api_key_hash: str
    api_key_preview: str
    description: str
    message: str


class AvailableModelsResponse(BaseModel):
    has_api_keys: bool
    api_key_count: int
    queryable_api_key_count: int
    models: list[AvailableModelItem] = Field(default_factory=list)
    errors: list[AvailableModelKeyError] = Field(default_factory=list)


def is_json_scalar(value: Any) -> bool:
    return value is None or isinstance(value, str | int | float | bool)
