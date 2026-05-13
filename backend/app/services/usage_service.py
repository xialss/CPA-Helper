import json
from collections import defaultdict
from datetime import datetime, time, timedelta
from typing import NamedTuple

from sqlalchemy import func
from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select

from app.core.errors import NotFoundError
from app.models import ModelPrice, UsageRecord, User, UserApiKey
from app.schemas.auth import AuthUserResponse
from app.schemas.usage import (
    DistributionItem,
    RankingItem,
    TrendPoint,
    UsageDistributionsResponse,
    UsageFilterParams,
    UsageOptionsResponse,
    UsageOverviewResponse,
    UsageRankingsResponse,
    UsageRecordDetailResponse,
    UsageRecordListItem,
    UsageRecordsResponse,
    UsageSummaryResponse,
)
from app.services.pricing_service import find_matching_price, get_price_map
from app.services.usage_parser import normalize_usage, redacted_raw_json
from app.services.user_service import (
    UserInfo,
    UserInfoLookup,
    display_user_name,
    get_user_info_lookup,
)

TOKENS_PER_MILLION = 1_000_000


class CostResult(NamedTuple):
    amount: float
    unpriced: bool


type CostMap = dict[int, CostResult]


class RecordUserSnapshot(NamedTuple):
    id: int | None
    name: str


class UsageAccessScope(NamedTuple):
    user_id: int
    username: str
    is_admin: bool


def default_today_range() -> tuple[datetime, datetime]:
    today = datetime.now().date()
    start = datetime.combine(today, time.min)
    return start, start + timedelta(days=1)


def normalized_filters(filters: UsageFilterParams) -> UsageFilterParams:
    if filters.start is not None and filters.end is not None:
        return filters
    start, end = default_today_range()
    return filters.model_copy(
        update={
            "start": filters.start or start,
            "end": filters.end or end,
        }
    )


def access_scope(
    current_user: AuthUserResponse,
    requested_scope: str | None = None,
) -> UsageAccessScope:
    account_scoped = requested_scope == "account" or not current_user.is_admin
    return UsageAccessScope(
        user_id=current_user.id,
        username=current_user.username,
        is_admin=not account_scoped,
    )


def scoped_filters(filters: UsageFilterParams, scope: UsageAccessScope) -> UsageFilterParams:
    if scope.is_admin:
        return filters
    return filters.model_copy(update={"usage_username": scope.username, "user_id": scope.user_id})


def effective_scoped_filters(
    session: Session,
    filters: UsageFilterParams,
    scope: UsageAccessScope,
) -> UsageFilterParams:
    scoped = scoped_filters(filters, scope)
    if scoped.usage_username is not None or scoped.user_id is None:
        return scoped
    username = session.exec(
        select(User.username).where(User.id == scoped.user_id)
    ).first()
    return scoped.model_copy(update={"usage_username": username or "__missing_user__"})


def save_usage_message(
    session: Session,
    raw: str | bytes | dict[str, object],
) -> tuple[UsageRecord, bool]:
    normalized = normalize_usage(raw)
    api_key_hash = str(normalized.pop("api_key_hash"))
    normalized.update(_usage_owner_snapshot(session, api_key_hash))
    record = UsageRecord(**normalized)
    session.add(record)
    try:
        session.commit()
    except IntegrityError:
        session.rollback()
        existing = session.exec(
            select(UsageRecord).where(UsageRecord.dedupe_key == normalized["dedupe_key"])
        ).first()
        if existing is None:
            raise
        return existing, False
    session.refresh(record)
    return record, True


def _apply_user_scope(
    statement,
    username: str,
):
    return statement.where(UsageRecord.usage_username == username)


def _apply_filters(statement, filters: UsageFilterParams):
    effective = normalized_filters(filters)
    if effective.start is not None:
        statement = statement.where(UsageRecord.timestamp >= effective.start)
    if effective.end is not None:
        statement = statement.where(UsageRecord.timestamp < effective.end)
    if effective.usage_username is not None:
        statement = _apply_user_scope(
            statement,
            effective.usage_username,
        )
    if effective.api_key_description:
        statement = statement.where(
            UsageRecord.api_key_description == effective.api_key_description
        )
    if effective.provider:
        statement = statement.where(UsageRecord.provider == effective.provider)
    if effective.model:
        statement = statement.where(UsageRecord.model == effective.model)
    if effective.endpoint:
        statement = statement.where(UsageRecord.endpoint == effective.endpoint)
    if effective.failed is not None:
        statement = statement.where(UsageRecord.failed == effective.failed)
    if effective.request_id:
        statement = statement.where(UsageRecord.request_id.contains(effective.request_id))
    return statement


def _filtered_records(
    session: Session,
    filters: UsageFilterParams,
) -> list[UsageRecord]:
    statement = _apply_filters(
        select(UsageRecord),
        filters,
    ).order_by(UsageRecord.timestamp)
    return list(session.exec(statement).all())


def _cost_key(record: UsageRecord) -> int:
    return id(record)


def _cost_map(
    records: list[UsageRecord],
    prices: dict[tuple[str, str], ModelPrice],
) -> CostMap:
    return {_cost_key(record): _calculate_cost(record, prices) for record in records}


def _record_cost(record: UsageRecord, costs: CostMap) -> CostResult:
    return costs[_cost_key(record)]


def _calculate_cost(record: UsageRecord, prices: dict[tuple[str, str], ModelPrice]) -> CostResult:
    price = find_matching_price(prices, record.provider, record.model)
    if price is None:
        return CostResult(0.0, record.total_tokens > 0)
    input_tokens, cached_tokens = _split_priced_subset(
        record.input_tokens,
        record.cached_tokens,
        price.cached_usd_per_million,
    )
    output_tokens, reasoning_tokens = _split_priced_subset(
        record.output_tokens,
        record.reasoning_tokens,
        price.reasoning_usd_per_million,
    )
    amount = (
        _million_token_cost(input_tokens, price.input_usd_per_million)
        + _million_token_cost(output_tokens, price.output_usd_per_million)
        + _million_token_cost(cached_tokens, price.cached_usd_per_million)
        + _million_token_cost(reasoning_tokens, price.reasoning_usd_per_million)
    )
    return CostResult(round(amount, 8), False)


def _split_priced_subset(
    total_tokens: int,
    subset_tokens: int,
    subset_price: float,
) -> tuple[int, int]:
    normalized_total = max(total_tokens, 0)
    normalized_subset = min(max(subset_tokens, 0), normalized_total)
    if subset_price <= 0:
        return normalized_total, 0
    return normalized_total - normalized_subset, normalized_subset


def _million_token_cost(tokens: int, usd_per_million: float) -> float:
    return tokens / TOKENS_PER_MILLION * usd_per_million


def _user_label(record: UsageRecord, users: UserInfoLookup) -> str:
    user_info = _record_user_info(record, users)
    if user_info is None:
        return "未绑定"
    return user_info.name


def _user_id(record: UsageRecord, users: UserInfoLookup) -> int | None:
    user_info = _record_user_info(record, users)
    return user_info.id if user_info else None


def _record_user_info(
    record: UsageRecord,
    users: UserInfoLookup,
) -> RecordUserSnapshot | None:
    if record.usage_username is None:
        return None
    current = users.by_username.get(record.usage_username)
    if current is None:
        return RecordUserSnapshot(
            id=None,
            name=record.usage_username,
        )
    return RecordUserSnapshot(
        id=current.id,
        name=current.name,
    )


def _user_lookup_for_scope(session: Session, scope: UsageAccessScope) -> UserInfoLookup:
    if scope.is_admin:
        return get_user_info_lookup(session)
    user_info = UserInfoLookup(
        by_api_key_hash={},
        by_id={
            scope.user_id: UserInfo(
                id=scope.user_id,
                username=scope.username,
                name=scope.username,
            )
        },
        by_username={
            scope.username: UserInfo(
                id=scope.user_id,
                username=scope.username,
                name=scope.username,
            )
        },
    )
    return user_info


def _record_is_visible_to_scope(
    record: UsageRecord,
    scope: UsageAccessScope,
) -> bool:
    if scope.is_admin:
        return True
    return record.usage_username == scope.username


def _usage_owner_snapshot(session: Session, api_key_hash: str) -> dict[str, object | None]:
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None:
        return {
            "usage_username": None,
            "api_key_description": None,
        }
    user = session.get(User, binding.user_id)
    if user is None:
        return {
            "usage_username": None,
            "api_key_description": binding.description or None,
        }
    return {
        "usage_username": user.username,
        "api_key_description": binding.description or None,
    }


def _raw_json_string_field(raw_json: str, field_name: str) -> str | None:
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return None
    if not isinstance(parsed, dict):
        return None
    value = parsed.get(field_name)
    if value is None:
        return None
    if isinstance(value, str):
        normalized = value.strip()
        return normalized or None
    if isinstance(value, int | float | bool):
        return str(value)
    return None


def _to_list_item(
    record: UsageRecord,
    users: UserInfoLookup,
    prices: dict[tuple[str, str], ModelPrice],
) -> UsageRecordListItem:
    cost = _calculate_cost(record, prices)
    return UsageRecordListItem(
        id=record.id or 0,
        timestamp=record.timestamp,
        api_key_description=record.api_key_description,
        user_id=_user_id(record, users),
        user_label=_user_label(record, users),
        provider=record.provider,
        model=record.model,
        endpoint=record.endpoint,
        source=record.source,
        request_id=record.request_id,
        auth_index=_raw_json_string_field(record.raw_json, "auth_index"),
        auth=_raw_json_string_field(record.raw_json, "auth_type") or record.auth,
        latency_ms=record.latency_ms,
        failed=record.failed,
        input_tokens=record.input_tokens,
        output_tokens=record.output_tokens,
        cached_tokens=record.cached_tokens,
        reasoning_tokens=record.reasoning_tokens,
        total_tokens=record.total_tokens,
        estimated_cost_usd=cost.amount,
        unpriced=cost.unpriced,
    )


def _summary_from_records(
    effective: UsageFilterParams,
    records: list[UsageRecord],
    costs: CostMap,
) -> UsageSummaryResponse:
    failed_records = sum(1 for record in records if record.failed)
    return UsageSummaryResponse(
        start=effective.start or default_today_range()[0],
        end=effective.end or default_today_range()[1],
        total_records=len(records),
        failed_records=failed_records,
        success_records=len(records) - failed_records,
        input_tokens=sum(record.input_tokens for record in records),
        output_tokens=sum(record.output_tokens for record in records),
        cached_tokens=sum(record.cached_tokens for record in records),
        reasoning_tokens=sum(record.reasoning_tokens for record in records),
        total_tokens=sum(record.total_tokens for record in records),
        estimated_cost_usd=round(sum(cost.amount for cost in costs.values()), 8),
        unpriced_records=sum(1 for cost in costs.values() if cost.unpriced),
    )


def _trend_points_from_records(
    effective: UsageFilterParams,
    records: list[UsageRecord],
    costs: CostMap,
) -> list[TrendPoint]:
    buckets: dict[str, list[UsageRecord]] = defaultdict(list)
    duration = (effective.end or datetime.now()) - (effective.start or datetime.now())
    for record in records:
        bucket = (
            record.timestamp.strftime("%Y-%m-%d %H:00")
            if duration <= timedelta(days=2)
            else record.timestamp.strftime("%Y-%m-%d")
        )
        buckets[bucket].append(record)
    points: list[TrendPoint] = []
    for bucket, bucket_records in sorted(buckets.items()):
        points.append(
            TrendPoint(
                bucket=bucket,
                records=len(bucket_records),
                failed_records=sum(1 for record in bucket_records if record.failed),
                total_tokens=sum(record.total_tokens for record in bucket_records),
                estimated_cost_usd=round(
                    sum(_record_cost(record, costs).amount for record in bucket_records),
                    8,
                ),
            )
        )
    return points


def _ranking_from_records(
    records: list[UsageRecord],
    costs: CostMap,
    group_by: str,
    users: UserInfoLookup,
) -> UsageRankingsResponse:
    grouped: dict[str, list[UsageRecord]] = defaultdict(list)
    labels: dict[str, str] = {}
    user_ids: dict[str, int | None] = {}
    api_key_descriptions: dict[str, str | None] = {}
    for record in records:
        if group_by == "model":
            key = f"{record.provider or 'unknown'}::{record.model or 'unknown'}"
            label = f"{record.provider or 'unknown'} / {record.model or 'unknown'}"
            user_ids[key] = None
            api_key_descriptions[key] = None
        elif group_by == "user":
            user_info = _record_user_info(record, users)
            if user_info is None:
                continue
            key = str(user_info.id) if user_info.id is not None else user_info.name
            label = user_info.name
            user_ids[key] = user_info.id
            api_key_descriptions[key] = None
        else:
            description = record.api_key_description.strip() if record.api_key_description else ""
            key = description or "unlabeled"
            label = description or "未设置 KEY 描述"
            user_ids[key] = None
            api_key_descriptions[key] = description or None
        grouped[key].append(record)
        labels[key] = label
    items: list[RankingItem] = []
    for key, group_records in grouped.items():
        items.append(
            RankingItem(
                key=key,
                label=labels[key],
                records=len(group_records),
                failed_records=sum(1 for record in group_records if record.failed),
                total_tokens=sum(record.total_tokens for record in group_records),
                estimated_cost_usd=round(
                    sum(_record_cost(record, costs).amount for record in group_records),
                    8,
                ),
                user_id=user_ids.get(key),
                api_key_description=api_key_descriptions.get(key),
            )
        )
    items.sort(key=lambda item: (item.total_tokens, item.records), reverse=True)
    safe_group = (
        group_by
        if group_by in {"api_key_description", "model", "user"}
        else "api_key_description"
    )
    return UsageRankingsResponse(group_by=safe_group, items=items[:20])


def _distribution_items(
    records: list[UsageRecord],
    costs: CostMap,
    field_name: str,
) -> list[DistributionItem]:
    grouped: dict[str, list[UsageRecord]] = defaultdict(list)
    for record in records:
        value = getattr(record, field_name) or "unknown"
        grouped[value].append(record)
    items: list[DistributionItem] = []
    for key, group_records in grouped.items():
        items.append(
            DistributionItem(
                key=key,
                label=key,
                records=len(group_records),
                total_tokens=sum(record.total_tokens for record in group_records),
                estimated_cost_usd=round(
                    sum(_record_cost(record, costs).amount for record in group_records),
                    8,
                ),
            )
        )
    items.sort(key=lambda item: item.records, reverse=True)
    return items[:20]


def _distributions_from_records(
    records: list[UsageRecord],
    costs: CostMap,
) -> UsageDistributionsResponse:
    return UsageDistributionsResponse(
        providers=_distribution_items(records, costs, "provider"),
        endpoints=_distribution_items(records, costs, "endpoint"),
    )


def get_summary(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
) -> UsageSummaryResponse:
    scope = access_scope(current_user, filters.scope)
    effective = normalized_filters(effective_scoped_filters(session, filters, scope))
    records = _filtered_records(session, effective)
    prices = get_price_map(session)
    costs = _cost_map(records, prices)
    return _summary_from_records(effective, records, costs)


def get_trends(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
) -> list[TrendPoint]:
    scope = access_scope(current_user, filters.scope)
    effective = normalized_filters(effective_scoped_filters(session, filters, scope))
    records = _filtered_records(session, effective)
    prices = get_price_map(session)
    costs = _cost_map(records, prices)
    return _trend_points_from_records(effective, records, costs)


def get_rankings(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
    group_by: str,
) -> UsageRankingsResponse:
    scope = access_scope(current_user, filters.scope)
    if not scope.is_admin and group_by == "user":
        return UsageRankingsResponse(group_by="user", items=[])
    records = _filtered_records(session, effective_scoped_filters(session, filters, scope))
    users = _user_lookup_for_scope(session, scope)
    prices = get_price_map(session)
    costs = _cost_map(records, prices)
    return _ranking_from_records(records, costs, group_by, users)


def get_distributions(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
) -> UsageDistributionsResponse:
    scope = access_scope(current_user, filters.scope)
    records = _filtered_records(session, effective_scoped_filters(session, filters, scope))
    prices = get_price_map(session)
    costs = _cost_map(records, prices)
    return _distributions_from_records(records, costs)


def get_overview(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
) -> UsageOverviewResponse:
    scope = access_scope(current_user, filters.scope)
    effective = normalized_filters(effective_scoped_filters(session, filters, scope))
    records = _filtered_records(session, effective)
    prices = get_price_map(session)
    users = _user_lookup_for_scope(session, scope)
    costs = _cost_map(records, prices)
    api_key_description_ranking = _ranking_from_records(
        records,
        costs,
        "api_key_description",
        users,
    )
    return UsageOverviewResponse(
        summary=_summary_from_records(effective, records, costs),
        trends=_trend_points_from_records(effective, records, costs),
        user_ranking=(
            _ranking_from_records(records, costs, "user", users)
            if scope.is_admin
            else UsageRankingsResponse(group_by="user", items=[])
        ),
        api_key_description_ranking=api_key_description_ranking,
        api_key_ranking=api_key_description_ranking,
        model_ranking=_ranking_from_records(records, costs, "model", users),
        distributions=_distributions_from_records(records, costs),
        options=get_options(session, current_user, filters.scope),
    )


def list_records(
    session: Session,
    filters: UsageFilterParams,
    current_user: AuthUserResponse,
    page: int,
    page_size: int,
) -> UsageRecordsResponse:
    scope = access_scope(current_user, filters.scope)
    effective_filters = normalized_filters(effective_scoped_filters(session, filters, scope))
    effective_page = max(page, 1)
    effective_size = min(max(page_size, 1), 200)
    count_statement = _apply_filters(
        select(func.count(UsageRecord.id)),
        effective_filters,
    )
    total = session.exec(count_statement).one()
    statement = (
        _apply_filters(
            select(UsageRecord),
            effective_filters,
        )
        .order_by(UsageRecord.timestamp.desc())
        .offset((effective_page - 1) * effective_size)
        .limit(effective_size)
    )
    records = session.exec(statement).all()
    users = _user_lookup_for_scope(session, scope)
    prices = get_price_map(session)
    return UsageRecordsResponse(
        items=[_to_list_item(record, users, prices) for record in records],
        total=total,
        page=effective_page,
        page_size=effective_size,
        start=effective_filters.start or default_today_range()[0],
        end=effective_filters.end or default_today_range()[1],
    )


def get_record_detail(
    session: Session,
    record_id: int,
    current_user: AuthUserResponse,
    requested_scope: str | None = None,
) -> UsageRecordDetailResponse:
    scope = access_scope(current_user, requested_scope)
    record = session.get(UsageRecord, record_id)
    if record is None or not _record_is_visible_to_scope(record, scope):
        raise NotFoundError("usage 记录不存在")
    users = _user_lookup_for_scope(session, scope)
    prices = get_price_map(session)
    item = _to_list_item(record, users, prices)
    return UsageRecordDetailResponse(
        **item.model_dump(),
        raw_json=redacted_raw_json(record.raw_json),
    )


def _api_key_description_options(descriptions: list[str | None]) -> list[RankingItem]:
    normalized = sorted({description.strip() for description in descriptions if description})
    return [
        RankingItem(
            key=description,
            label=description,
            records=0,
            failed_records=0,
            total_tokens=0,
            estimated_cost_usd=0.0,
            api_key_description=description,
        )
        for description in normalized
        if description
    ]


def get_options(
    session: Session,
    current_user: AuthUserResponse,
    requested_scope: str | None = None,
) -> UsageOptionsResponse:
    scope = access_scope(current_user, requested_scope)
    if not scope.is_admin:
        providers = session.exec(
            _apply_user_scope(
                select(UsageRecord.provider).where(UsageRecord.provider.is_not(None)).distinct(),
                scope.username,
            )
        ).all()
        models = session.exec(
            _apply_user_scope(
                select(UsageRecord.model).where(UsageRecord.model.is_not(None)).distinct(),
                scope.username,
            )
        ).all()
        endpoints = session.exec(
            _apply_user_scope(
                select(UsageRecord.endpoint).where(UsageRecord.endpoint.is_not(None)).distinct(),
                scope.username,
            )
        ).all()
        api_key_descriptions = session.exec(
            _apply_user_scope(
                select(UsageRecord.api_key_description)
                .where(UsageRecord.api_key_description.is_not(None))
                .distinct(),
                scope.username,
            )
        ).all()
        return UsageOptionsResponse(
            users=[],
            api_key_descriptions=_api_key_description_options(api_key_descriptions),
            providers=sorted(provider for provider in providers if provider),
            models=sorted(model for model in models if model),
            endpoints=sorted(endpoint for endpoint in endpoints if endpoint),
        )

    user_rows = session.exec(select(User).order_by(User.username)).all()
    providers = session.exec(
        select(UsageRecord.provider).where(UsageRecord.provider.is_not(None)).distinct()
    ).all()
    models = session.exec(
        select(UsageRecord.model).where(UsageRecord.model.is_not(None)).distinct()
    ).all()
    endpoints = session.exec(
        select(UsageRecord.endpoint).where(UsageRecord.endpoint.is_not(None)).distinct()
    ).all()
    api_key_descriptions = session.exec(
        select(UsageRecord.api_key_description)
        .where(UsageRecord.api_key_description.is_not(None))
        .distinct()
    ).all()
    return UsageOptionsResponse(
        users=[
            RankingItem(
                key=str(user.id or 0),
                label=display_user_name(user),
                records=0,
                failed_records=0,
                total_tokens=0,
                estimated_cost_usd=0.0,
                user_id=user.id,
            )
            for user in user_rows
        ],
        api_key_descriptions=_api_key_description_options(api_key_descriptions),
        providers=sorted(provider for provider in providers if provider),
        models=sorted(model for model in models if model),
        endpoints=sorted(endpoint for endpoint in endpoints if endpoint),
    )
