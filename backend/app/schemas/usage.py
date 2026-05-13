from datetime import datetime
from typing import Literal

from pydantic import BaseModel, Field


class UsageFilterParams(BaseModel):
    scope: Literal["admin", "account"] | None = None
    start: datetime | None = None
    end: datetime | None = None
    user_id: int | None = None
    usage_username: str | None = None
    api_key_description: str | None = None
    provider: str | None = None
    model: str | None = None
    endpoint: str | None = None
    failed: bool | None = None
    request_id: str | None = None


class UsageSummaryResponse(BaseModel):
    start: datetime
    end: datetime
    total_records: int
    failed_records: int
    success_records: int
    input_tokens: int
    output_tokens: int
    cached_tokens: int
    reasoning_tokens: int
    total_tokens: int
    estimated_cost_usd: float
    unpriced_records: int


class TrendPoint(BaseModel):
    bucket: str
    records: int
    failed_records: int
    total_tokens: int
    estimated_cost_usd: float


class RankingItem(BaseModel):
    key: str
    label: str
    records: int
    failed_records: int
    total_tokens: int
    estimated_cost_usd: float
    user_id: int | None = None
    api_key_description: str | None = None


class UsageRankingsResponse(BaseModel):
    group_by: Literal["api_key_description", "model", "user"]
    items: list[RankingItem]


class DistributionItem(BaseModel):
    key: str
    label: str
    records: int
    total_tokens: int
    estimated_cost_usd: float


class UsageDistributionsResponse(BaseModel):
    providers: list[DistributionItem]
    endpoints: list[DistributionItem]


class UsageRecordListItem(BaseModel):
    id: int
    timestamp: datetime
    api_key_description: str | None = None
    user_id: int | None = None
    user_label: str
    provider: str | None
    model: str | None
    endpoint: str | None
    source: str | None
    request_id: str | None
    auth_index: str | None
    auth: str | None
    latency_ms: float | None
    failed: bool
    input_tokens: int
    output_tokens: int
    cached_tokens: int
    reasoning_tokens: int
    total_tokens: int
    estimated_cost_usd: float
    unpriced: bool


class UsageRecordsResponse(BaseModel):
    items: list[UsageRecordListItem]
    total: int
    page: int
    page_size: int
    start: datetime
    end: datetime


class UsageRecordDetailResponse(UsageRecordListItem):
    raw_json: dict[str, object] | list[object] | str


class UsageOptionsResponse(BaseModel):
    users: list[RankingItem] = Field(default_factory=list)
    api_key_descriptions: list[RankingItem] = Field(default_factory=list)
    providers: list[str] = Field(default_factory=list)
    models: list[str] = Field(default_factory=list)
    endpoints: list[str] = Field(default_factory=list)


class UsageOverviewResponse(BaseModel):
    summary: UsageSummaryResponse
    trends: list[TrendPoint]
    user_ranking: UsageRankingsResponse
    api_key_description_ranking: UsageRankingsResponse
    api_key_ranking: UsageRankingsResponse | None = None
    model_ranking: UsageRankingsResponse
    distributions: UsageDistributionsResponse
    options: UsageOptionsResponse
