from datetime import datetime
from typing import Annotated

from fastapi import APIRouter, Depends, Query

from app.api.deps import ReadyUserDep, SessionDep
from app.schemas.usage import (
    TrendPoint,
    UsageDistributionsResponse,
    UsageFilterParams,
    UsageOptionsResponse,
    UsageOverviewResponse,
    UsageRankingsResponse,
    UsageRecordDetailResponse,
    UsageRecordsResponse,
    UsageSummaryResponse,
)
from app.services.usage_service import (
    get_distributions,
    get_options,
    get_overview,
    get_rankings,
    get_record_detail,
    get_summary,
    get_trends,
    list_records,
)

router = APIRouter(prefix="/usage", tags=["usage"])


def usage_filters(
    scope: str | None = Query(default=None, pattern="^(admin|account)$"),
    start: datetime | None = None,
    end: datetime | None = None,
    user_id: int | None = None,
    api_key_description: str | None = None,
    provider: str | None = None,
    model: str | None = None,
    endpoint: str | None = None,
    failed: bool | None = None,
    request_id: str | None = None,
) -> UsageFilterParams:
    return UsageFilterParams(
        scope=scope,
        start=start,
        end=end,
        user_id=user_id,
        api_key_description=api_key_description,
        provider=provider,
        model=model,
        endpoint=endpoint,
        failed=failed,
        request_id=request_id,
    )


UsageFiltersDep = Annotated[UsageFilterParams, Depends(usage_filters)]


@router.get("/summary", response_model=UsageSummaryResponse)
def summary(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
) -> UsageSummaryResponse:
    return get_summary(session, filters, user)


@router.get("/trends", response_model=list[TrendPoint])
def trends(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
) -> list[TrendPoint]:
    return get_trends(session, filters, user)


@router.get("/rankings", response_model=UsageRankingsResponse)
def rankings(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
    group_by: str = Query(
        default="api_key_description",
        pattern="^(api_key|api_key_description|model|user)$",
    ),
) -> UsageRankingsResponse:
    return get_rankings(session, filters, user, group_by)


@router.get("/distributions", response_model=UsageDistributionsResponse)
def distributions(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
) -> UsageDistributionsResponse:
    return get_distributions(session, filters, user)


@router.get("/overview", response_model=UsageOverviewResponse)
def overview(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
) -> UsageOverviewResponse:
    return get_overview(session, filters, user)


@router.get("/records", response_model=UsageRecordsResponse)
def records(
    filters: UsageFiltersDep,
    session: SessionDep,
    user: ReadyUserDep,
    page: int = Query(default=1, ge=1),
    page_size: int = Query(default=50, ge=1, le=200),
) -> UsageRecordsResponse:
    return list_records(session, filters, user, page, page_size)


@router.get("/records/{record_id}", response_model=UsageRecordDetailResponse)
def record_detail(
    record_id: int,
    session: SessionDep,
    user: ReadyUserDep,
    scope: str | None = Query(default=None, pattern="^(admin|account)$"),
) -> UsageRecordDetailResponse:
    return get_record_detail(session, record_id, user, scope)


@router.get("/options", response_model=UsageOptionsResponse)
def options(
    session: SessionDep,
    user: ReadyUserDep,
    scope: str | None = Query(default=None, pattern="^(admin|account)$"),
) -> UsageOptionsResponse:
    return get_options(session, user, scope)
