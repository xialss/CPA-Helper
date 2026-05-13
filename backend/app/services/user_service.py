import secrets
import string
from contextlib import suppress
from datetime import datetime, time, timedelta
from typing import NamedTuple

from sqlmodel import Session, select

from app.core.errors import ConflictError, NotFoundError, ValidationAppError
from app.core.security import create_salt, hash_api_key, hash_password
from app.models import ModelPrice, UsageRecord, User, UserApiKey
from app.schemas.api_keys import ApiKeyCreateRequest, ApiKeyUpdateRequest
from app.schemas.users import (
    UserApiKeyBindPayload,
    UserApiKeySummary,
    UserPayload,
    UserSummaryResponse,
)
from app.services import cpa_management_service
from app.services.pricing_service import find_matching_price, get_price_map

TOKENS_PER_MILLION = 1_000_000
GENERATED_API_KEY_PREFIX = "sk-"
GENERATED_API_KEY_RANDOM_LENGTH = 52
GENERATED_API_KEY_ALPHABET = string.ascii_letters + string.digits


class UserInfo(NamedTuple):
    id: int
    username: str
    name: str


class UserInfoLookup(NamedTuple):
    by_api_key_hash: dict[str, UserInfo]
    by_id: dict[int, UserInfo]
    by_username: dict[str, UserInfo]


def display_user_name(user: User) -> str:
    return user.nickname.strip() or user.username.strip() or "未知用户"


def historical_user_name(user: User) -> str:
    name = display_user_name(user)
    if user.disabled_at is not None:
        return f"{name} (已禁用)"
    return name


def list_users(session: Session) -> list[UserSummaryResponse]:
    ensure_users_initialized(session)
    users = list(session.exec(select(User).order_by(User.id)).all())
    summaries = _key_summaries(session)
    usage_by_user = _user_usage_summaries(session)
    keys_by_user: dict[int, list[UserApiKeySummary]] = {}
    for key_summary in summaries:
        if key_summary.user_id is not None:
            keys_by_user.setdefault(key_summary.user_id, []).append(key_summary)

    responses = [
        _user_summary_response(
            user,
            keys_by_user.get(user.id or 0, []),
            usage_by_user.get(user.id or 0),
        )
        for user in users
    ]
    return sorted(responses, key=lambda item: item.id)


def list_observed_api_keys(session: Session) -> list[UserApiKeySummary]:
    ensure_users_initialized(session)
    return _key_summaries(session)


def list_bound_api_keys(session: Session) -> list[UserApiKeySummary]:
    ensure_users_initialized(session)
    return [item for item in _key_summaries(session) if item.user_id is not None]


def list_current_user_api_keys(session: Session, username: str) -> list[UserApiKeySummary]:
    user = _get_current_user(session, username)
    return [
        _current_user_api_key_summary(item, user)
        for item in _key_summaries(session)
        if item.user_id == user.id
    ]


def create_user(session: Session, payload: UserPayload) -> UserSummaryResponse:
    ensure_users_initialized(session)
    username = payload.username.strip()
    _ensure_username_available(session, username)
    nickname = payload.nickname.strip()
    if payload.password is None:
        raise ValidationAppError("密码不能为空")
    password_salt = create_salt()
    user = User(
        username=username,
        password_salt=password_salt,
        password_hash=hash_password(payload.password, password_salt),
        is_admin=payload.is_admin,
        nickname=nickname,
    )
    session.add(user)
    session.commit()
    session.refresh(user)
    return _user_summary_response(user, [], None)


def update_user(session: Session, user_id: int, payload: UserPayload) -> UserSummaryResponse:
    ensure_users_initialized(session)
    user = _get_user(session, user_id)
    username = payload.username.strip()
    if username != user.username:
        raise ConflictError("账号不允许修改")
    first_user_id = _first_user_id(session)
    if user.id == first_user_id and not payload.is_admin:
        raise ConflictError("首个管理员账号不能取消管理员权限")
    user.username = username
    user.nickname = payload.nickname.strip()
    user.is_admin = payload.is_admin
    if payload.password is not None:
        user.password_salt = create_salt()
        user.password_hash = hash_password(payload.password, user.password_salt)
    user.updated_at = datetime.now()
    session.add(user)
    session.commit()
    session.refresh(user)
    key_summaries = [item for item in _key_summaries(session) if item.user_id == user_id]
    usage_by_user = _user_usage_summaries(session)
    return _user_summary_response(user, key_summaries, usage_by_user.get(user.id or 0))


def disable_user(session: Session, user_id: int) -> None:
    user = _get_user(session, user_id)
    if user.id == 1:
        raise ConflictError("首个用户不能禁用")
    now = datetime.now()
    for binding in session.exec(select(UserApiKey).where(UserApiKey.user_id == user_id)).all():
        cpa_management_service.remove_remote_api_key_hash(binding.api_key_hash)
    user.disabled_at = now
    user.updated_at = now
    session.add(user)
    session.commit()


def enable_user(session: Session, user_id: int) -> None:
    user = _get_user(session, user_id)
    if user.disabled_at is None:
        return
    bindings = list(session.exec(select(UserApiKey).where(UserApiKey.user_id == user_id)).all())
    missing_full_keys = [binding.api_key_hash for binding in bindings if not binding.api_key]
    if missing_full_keys:
        raise ConflictError("存在无法恢复的 API KEY，请重新绑定后再启用")
    restored_hashes: list[str] = []
    try:
        for binding in bindings:
            if binding.api_key is None:
                continue
            cpa_management_service.add_remote_api_key(binding.api_key)
            restored_hashes.append(binding.api_key_hash)
    except Exception:
        for api_key_hash in restored_hashes:
            with suppress(Exception):
                cpa_management_service.remove_remote_api_key_hash(api_key_hash)
        raise
    user.disabled_at = None
    user.updated_at = datetime.now()
    session.add(user)
    session.commit()


def bind_user_api_key(
    session: Session,
    user_id: int,
    payload: UserApiKeyBindPayload,
) -> UserApiKeySummary:
    user = _get_active_user(session, user_id)
    api_key_hash = payload.api_key_hash or hash_api_key(payload.api_key or "")
    if (
        payload.api_key is not None
        and payload.api_key_hash is not None
        and hash_api_key(payload.api_key) != payload.api_key_hash
    ):
        raise ConflictError("API KEY 与 API KEY 标识不匹配")
    api_key = payload.api_key or _observed_api_key(session, api_key_hash)
    if api_key is None:
        raise NotFoundError("未找到完整 API KEY，请粘贴原始 API KEY")
    _upsert_user_api_key_binding(
        session,
        user,
        api_key_hash=api_key_hash,
        api_key=api_key,
        description=payload.description,
    )
    return _key_summary_by_hash(session, api_key_hash)


def create_generated_api_key(
    session: Session,
    payload: ApiKeyCreateRequest,
) -> UserApiKeySummary:
    ensure_users_initialized(session)
    user = session.exec(
        select(User).where(User.disabled_at.is_(None)).order_by(User.id)
    ).first()
    if user is None:
        raise ConflictError("请先创建首个管理员账号")
    return _create_generated_api_key_for_user(session, user, payload.description)


def _create_generated_api_key_for_user(
    session: Session,
    user: User,
    description: str,
) -> UserApiKeySummary:
    api_key = _generate_unique_api_key(session)
    cpa_management_service.add_remote_api_key(api_key)
    api_key_hash = hash_api_key(api_key)
    try:
        _upsert_user_api_key_binding(
            session,
            user,
            api_key_hash=api_key_hash,
            api_key=api_key,
            description=description,
        )
    except Exception:
        with suppress(Exception):
            cpa_management_service.remove_remote_api_key_hash(api_key_hash)
        raise
    summary = _key_summary_by_hash(session, api_key_hash)
    summary.api_key = api_key
    return summary


def create_generated_api_key_for_current_user(
    session: Session,
    username: str,
    payload: ApiKeyCreateRequest,
) -> UserApiKeySummary:
    user = _get_current_user(session, username)
    return _current_user_api_key_summary(
        _create_generated_api_key_for_user(session, user, payload.description),
        user,
    )


def update_bound_api_key(
    session: Session,
    api_key_hash: str,
    payload: ApiKeyUpdateRequest,
) -> UserApiKeySummary:
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None:
        raise NotFoundError("API KEY 不存在")
    binding.description = payload.description.strip()
    binding.updated_at = datetime.now()
    session.add(binding)
    user = session.get(User, binding.user_id)
    if user is not None:
        user.updated_at = datetime.now()
        session.add(user)
    session.commit()
    return _key_summary_by_hash(session, api_key_hash)


def update_current_user_api_key(
    session: Session,
    username: str,
    api_key_hash: str,
    payload: ApiKeyUpdateRequest,
) -> UserApiKeySummary:
    user = _get_current_user(session, username)
    binding = _get_owned_api_key_binding(session, user.id or 0, api_key_hash)
    binding.description = payload.description.strip()
    binding.updated_at = datetime.now()
    session.add(binding)
    user.updated_at = datetime.now()
    session.add(user)
    session.commit()
    return _current_user_api_key_summary(_key_summary_by_hash(session, api_key_hash), user)


def delete_bound_api_key(session: Session, api_key_hash: str) -> None:
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None:
        raise NotFoundError("API KEY 不存在")
    cpa_management_service.remove_remote_api_key_hash(api_key_hash)
    session.delete(binding)
    session.commit()


def delete_current_user_api_key(session: Session, username: str, api_key_hash: str) -> None:
    user = _get_current_user(session, username)
    binding = _get_owned_api_key_binding(session, user.id or 0, api_key_hash)
    cpa_management_service.remove_remote_api_key_hash(api_key_hash)
    session.delete(binding)
    session.commit()


def unbind_user_api_key(session: Session, user_id: int, api_key_hash: str) -> None:
    _get_user(session, user_id)
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None or binding.user_id != user_id:
        raise NotFoundError("API KEY 绑定不存在")
    session.delete(binding)
    session.commit()


def get_user_info_map(session: Session) -> dict[str, UserInfo]:
    return get_user_info_lookup(session).by_api_key_hash


def get_user_info_lookup(session: Session) -> UserInfoLookup:
    ensure_users_initialized(session)
    users = [_to_user_info(user) for user in session.exec(select(User)).all()]
    by_id = {user.id: user for user in users if user.id}
    by_username = {user.username: user for user in users if user.username}
    by_api_key_hash: dict[str, UserInfo] = {}
    for binding in session.exec(select(UserApiKey)).all():
        user_info = by_id.get(binding.user_id)
        if user_info is not None:
            by_api_key_hash[binding.api_key_hash] = user_info
    return UserInfoLookup(
        by_api_key_hash=by_api_key_hash,
        by_id=by_id,
        by_username=by_username,
    )


def _to_user_info(user: User) -> UserInfo:
    return UserInfo(
        id=user.id or 0,
        username=user.username,
        name=historical_user_name(user),
    )


def get_user_api_key_hashes(session: Session, user_id: int) -> list[str]:
    ensure_users_initialized(session)
    return list(
        session.exec(
            select(UserApiKey.api_key_hash).where(UserApiKey.user_id == user_id)
        ).all()
    )


def _get_user(session: Session, user_id: int) -> User:
    user = session.get(User, user_id)
    if user is None:
        raise NotFoundError("用户不存在")
    return user


def _get_active_user(session: Session, user_id: int) -> User:
    user = _get_user(session, user_id)
    if user.disabled_at is not None:
        raise ConflictError("用户已禁用")
    return user


def _get_current_user(session: Session, username: str) -> User:
    ensure_users_initialized(session)
    normalized = username.strip()
    user = _find_user_by_username(session, normalized)
    if user is None or user.disabled_at is not None:
        raise NotFoundError("用户不存在")
    return user


def _get_owned_api_key_binding(
    session: Session,
    user_id: int,
    api_key_hash: str,
) -> UserApiKey:
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None or binding.user_id != user_id:
        raise NotFoundError("API KEY 不存在")
    return binding


def _upsert_user_api_key_binding(
    session: Session,
    user: User,
    *,
    api_key_hash: str,
    api_key: str,
    description: str,
) -> None:
    if user.disabled_at is not None:
        raise ConflictError("用户已禁用")
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None:
        binding = UserApiKey(
            api_key_hash=api_key_hash,
            user_id=user.id or 0,
            api_key=api_key,
            description=description.strip(),
        )
        session.add(binding)
    else:
        binding.user_id = user.id or binding.user_id
        binding.api_key = api_key
        binding.description = description.strip()
        binding.updated_at = datetime.now()
    user.updated_at = datetime.now()
    session.add(user)
    session.commit()


def _generate_unique_api_key(session: Session) -> str:
    for _ in range(10):
        random_part = "".join(
            secrets.choice(GENERATED_API_KEY_ALPHABET)
            for _ in range(GENERATED_API_KEY_RANDOM_LENGTH)
        )
        api_key = f"{GENERATED_API_KEY_PREFIX}{random_part}"
        if session.get(UserApiKey, hash_api_key(api_key)) is None:
            return api_key
    raise ConflictError("生成 API KEY 失败，请重试")


def ensure_users_initialized(session: Session) -> None:
    if _first_user_id(session) is None:
        raise ConflictError("请先创建首个管理员账号")


def _find_user_by_username(session: Session, username: str) -> User | None:
    return session.exec(select(User).where(User.username == username)).first()


def _first_user_id(session: Session) -> int | None:
    return session.exec(
        select(User.id).where(User.disabled_at.is_(None)).order_by(User.id)
    ).first()


def _ensure_username_available(
    session: Session,
    username: str,
    *,
    user_id: int | None = None,
) -> None:
    existing = _find_user_by_username(session, username)
    if existing is not None and existing.id != user_id:
        raise ConflictError("账号已存在")


def _key_summary_by_hash(session: Session, api_key_hash: str) -> UserApiKeySummary:
    for summary in _key_summaries(session):
        if summary.api_key_hash == api_key_hash:
            return summary
    binding = session.get(UserApiKey, api_key_hash)
    if binding is None:
        raise NotFoundError("API KEY 不存在")
    user = session.get(User, binding.user_id)
    return UserApiKeySummary(
        api_key_hash=api_key_hash,
        api_key=binding.api_key,
        description=binding.description,
        user_id=binding.user_id,
        user_name=display_user_name(user) if user else None,
        created_at=binding.created_at,
        updated_at=binding.updated_at,
    )


def _observed_api_key(session: Session, api_key_hash: str) -> str | None:
    binding = session.get(UserApiKey, api_key_hash)
    if binding and binding.api_key:
        return binding.api_key
    return None


def _user_summary_response(
    user: User,
    api_keys: list[UserApiKeySummary],
    usage: dict | None,
) -> UserSummaryResponse:
    usage = usage or _empty_user_usage_summary()
    return UserSummaryResponse(
        id=user.id or 0,
        username=user.username,
        is_admin=user.is_admin,
        nickname=user.nickname,
        disabled_at=user.disabled_at,
        password_set=bool(user.password_hash and user.password_salt),
        created_at=user.created_at,
        updated_at=user.updated_at,
        api_keys=api_keys,
        key_count=len(api_keys),
        records=int(usage["records"]),
        success_records=int(usage["success_records"]),
        failed_records=int(usage["failed_records"]),
        total_tokens=int(usage["total_tokens"]),
        today_records=int(usage["today_records"]),
        today_success_records=int(usage["today_success_records"]),
        today_failed_records=int(usage["today_failed_records"]),
        today_input_tokens=int(usage["today_input_tokens"]),
        today_output_tokens=int(usage["today_output_tokens"]),
        today_cached_tokens=int(usage["today_cached_tokens"]),
        today_reasoning_tokens=int(usage["today_reasoning_tokens"]),
        today_total_tokens=int(usage["today_total_tokens"]),
        today_estimated_cost_usd=float(usage["today_estimated_cost_usd"]),
        today_unpriced_records=int(usage["today_unpriced_records"]),
        first_seen_at=usage["first_seen_at"],
        last_seen_at=usage["last_seen_at"],
        last_provider=usage["last_provider"],
        last_model=usage["last_model"],
        providers=list(usage["providers"]),
        models=list(usage["models"]),
    )


def _current_user_api_key_summary(
    summary: UserApiKeySummary,
    user: User,
) -> UserApiKeySummary:
    return summary.model_copy(update={"user_name": user.username})


def _key_summaries(session: Session) -> list[UserApiKeySummary]:
    bindings = {
        binding.api_key_hash: binding
        for binding in session.exec(select(UserApiKey)).all()
    }
    users = {
        user.id: user
        for user in session.exec(select(User)).all()
    }
    summaries = [
        _empty_key_summary(
            api_key_hash,
            binding,
            users.get(binding.user_id),
            api_key=binding.api_key,
        )
        for api_key_hash, binding in bindings.items()
    ]

    items = [
        UserApiKeySummary.model_validate(
            {key: value for key, value in item.items() if not key.startswith("_")}
        )
        for item in summaries
    ]
    return sorted(
        items,
        key=lambda item: (
            item.updated_at or item.created_at or datetime.min,
            item.user_name or "",
            item.api_key_hash,
        ),
        reverse=True,
    )


def _user_usage_summaries(session: Session) -> dict[int, dict]:
    prices = get_price_map(session)
    today_start, today_end = _today_range()
    summaries: dict[int, dict] = {}
    users_by_username = {
        user.username: user
        for user in session.exec(select(User)).all()
    }
    records = session.exec(
        select(UsageRecord)
        .where(UsageRecord.usage_username.is_not(None))
        .order_by(UsageRecord.timestamp.desc())
    ).all()
    for record in records:
        user = users_by_username.get(record.usage_username or "")
        if user is None or user.id is None:
            continue
        user_id = user.id
        existing = summaries.setdefault(user_id, _empty_user_usage_summary())
        existing["records"] += 1
        existing["failed_records"] += int(record.failed)
        existing["success_records"] += int(not record.failed)
        existing["total_tokens"] += record.total_tokens
        existing["first_seen_at"] = _earlier(existing["first_seen_at"], record.timestamp)
        if existing["last_seen_at"] is None or record.timestamp > existing["last_seen_at"]:
            existing["last_seen_at"] = record.timestamp
            existing["last_provider"] = record.provider
            existing["last_model"] = record.model
        _append_unique(existing["providers"], existing["_provider_seen"], record.provider)
        _append_unique(existing["models"], existing["_model_seen"], record.model)
        if today_start <= record.timestamp < today_end:
            amount, unpriced = _calculate_usage_cost(record, prices)
            existing["today_records"] += 1
            existing["today_failed_records"] += int(record.failed)
            existing["today_success_records"] += int(not record.failed)
            existing["today_input_tokens"] += record.input_tokens
            existing["today_output_tokens"] += record.output_tokens
            existing["today_cached_tokens"] += record.cached_tokens
            existing["today_reasoning_tokens"] += record.reasoning_tokens
            existing["today_total_tokens"] += record.total_tokens
            existing["today_estimated_cost_usd"] = round(
                existing["today_estimated_cost_usd"] + amount,
                8,
            )
            existing["today_unpriced_records"] += int(unpriced)
    return summaries


def _empty_user_usage_summary() -> dict:
    return {
        "records": 0,
        "success_records": 0,
        "failed_records": 0,
        "total_tokens": 0,
        "today_records": 0,
        "today_success_records": 0,
        "today_failed_records": 0,
        "today_input_tokens": 0,
        "today_output_tokens": 0,
        "today_cached_tokens": 0,
        "today_reasoning_tokens": 0,
        "today_total_tokens": 0,
        "today_estimated_cost_usd": 0.0,
        "today_unpriced_records": 0,
        "first_seen_at": None,
        "last_seen_at": None,
        "last_provider": None,
        "last_model": None,
        "providers": [],
        "models": [],
        "_provider_seen": set(),
        "_model_seen": set(),
    }


def _empty_key_summary(
    api_key_hash: str,
    binding: UserApiKey | None,
    user: User | None,
    *,
    api_key: str | None = None,
) -> dict:
    return {
        "api_key_hash": api_key_hash,
        "api_key": api_key,
        "description": binding.description if binding else "",
        "user_id": binding.user_id if binding else None,
        "user_name": display_user_name(user) if user else None,
        "created_at": binding.created_at if binding else None,
        "updated_at": binding.updated_at if binding else None,
        "records": 0,
        "success_records": 0,
        "failed_records": 0,
        "total_tokens": 0,
        "today_records": 0,
        "today_success_records": 0,
        "today_failed_records": 0,
        "today_input_tokens": 0,
        "today_output_tokens": 0,
        "today_cached_tokens": 0,
        "today_reasoning_tokens": 0,
        "today_total_tokens": 0,
        "today_estimated_cost_usd": 0.0,
        "today_unpriced_records": 0,
        "first_seen_at": None,
        "last_seen_at": None,
        "last_provider": None,
        "last_model": None,
        "providers": [],
        "models": [],
        "_provider_seen": set(),
        "_model_seen": set(),
    }


def _append_unique(items: list[str], seen: set[str], value: str | None) -> None:
    normalized = (value or "").strip()
    if not normalized or normalized in seen:
        return
    seen.add(normalized)
    items.append(normalized)


def _today_range() -> tuple[datetime, datetime]:
    today = datetime.now().date()
    start = datetime.combine(today, time.min)
    return start, start + timedelta(days=1)


def _earlier(current: datetime | None, value: datetime) -> datetime:
    if current is None:
        return value
    return min(current, value)


def _calculate_usage_cost(
    record: UsageRecord,
    prices: dict[tuple[str, str], ModelPrice],
) -> tuple[float, bool]:
    price = find_matching_price(prices, record.provider, record.model)
    if price is None:
        return 0.0, record.total_tokens > 0
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
    return round(amount, 8), False


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
