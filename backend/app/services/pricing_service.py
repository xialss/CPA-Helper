from datetime import datetime
from typing import Any
from urllib.parse import urlparse

import httpx
from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select

from app.core.errors import ConflictError, NotFoundError, ValidationAppError
from app.models import ModelPrice
from app.schemas.pricing import (
    ModelPriceCreate,
    ModelPriceResponse,
    ModelPriceSyncResponse,
    ModelPriceUpdate,
)

DEFAULT_LITELLM_PRICING_URL = (
    "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
)
MANUAL_PRICE_SOURCE = "manual"
LITELLM_PRICE_SOURCE = "litellm"
USD_PER_TOKEN_TO_MILLION = 1_000_000
MAX_PROVIDER_LENGTH = 120
MAX_MODEL_LENGTH = 180
QUERY_PROVIDER_ALIASES: dict[str, tuple[str, ...]] = {
    "codex": ("openai",),
    "claude": ("anthropic",),
}


def list_prices(session: Session) -> list[ModelPriceResponse]:
    prices = session.exec(select(ModelPrice).order_by(ModelPrice.provider, ModelPrice.model)).all()
    return [ModelPriceResponse.model_validate(price, from_attributes=True) for price in prices]


def get_price_map(session: Session) -> dict[tuple[str, str], ModelPrice]:
    prices = session.exec(select(ModelPrice)).all()
    return {_price_key(price.provider, price.model): price for price in prices}


def find_matching_price(
    prices: dict[tuple[str, str], ModelPrice],
    provider: str | None,
    model: str | None,
) -> ModelPrice | None:
    provider_key, model_key = _price_key(provider, model)
    if not provider_key or not model_key:
        return None

    for candidate_provider in _candidate_provider_keys(provider_key):
        price = prices.get((candidate_provider, model_key))
        if price is not None:
            return price
    return None


def create_price(session: Session, payload: ModelPriceCreate) -> ModelPriceResponse:
    item = ModelPrice(
        **payload.model_dump(),
        source=MANUAL_PRICE_SOURCE,
        source_model=None,
        auto_synced=False,
        last_synced_at=None,
    )
    session.add(item)
    try:
        session.commit()
    except IntegrityError as exc:
        session.rollback()
        raise ConflictError("该 provider/model 价格已存在") from exc
    session.refresh(item)
    return ModelPriceResponse.model_validate(item, from_attributes=True)


def update_price(session: Session, price_id: int, payload: ModelPriceUpdate) -> ModelPriceResponse:
    item = session.get(ModelPrice, price_id)
    if item is None:
        raise NotFoundError("模型价格不存在")
    for key, value in payload.model_dump().items():
        setattr(item, key, value)
    item.source = MANUAL_PRICE_SOURCE
    item.source_model = None
    item.auto_synced = False
    item.last_synced_at = None
    item.updated_at = datetime.now()
    try:
        session.commit()
    except IntegrityError as exc:
        session.rollback()
        raise ConflictError("该 provider/model 价格已存在") from exc
    session.refresh(item)
    return ModelPriceResponse.model_validate(item, from_attributes=True)


def delete_price(session: Session, price_id: int) -> None:
    item = session.get(ModelPrice, price_id)
    if item is None:
        raise NotFoundError("模型价格不存在")
    session.delete(item)
    session.commit()


def _validate_source_url(source_url: str) -> str:
    parsed = urlparse(source_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValidationAppError("价格数据源 URL 必须是有效的 HTTP/HTTPS 地址")
    return source_url


def _fetch_litellm_pricing_json(source_url: str) -> dict[str, Any]:
    source_url = _validate_source_url(source_url)
    try:
        response = httpx.get(source_url, timeout=30.0, follow_redirects=True)
        response.raise_for_status()
    except httpx.HTTPError as exc:
        raise ValidationAppError("下载 LiteLLM 价格数据失败") from exc

    try:
        data = response.json()
    except ValueError as exc:
        raise ValidationAppError("LiteLLM 价格数据不是有效 JSON") from exc

    if not isinstance(data, dict):
        raise ValidationAppError("LiteLLM 价格数据格式不正确")
    return data


def _number(value: Any) -> float | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int | float):
        if value < 0:
            return None
        return float(value)
    if isinstance(value, str):
        try:
            parsed = float(value)
        except ValueError:
            return None
        return parsed if parsed >= 0 else None
    return None


def _usd_per_million(value: Any) -> float:
    number = _number(value)
    if number is None:
        return 0.0
    return round(number * USD_PER_TOKEN_TO_MILLION, 12)


def _litellm_entry_to_price(model_name: str, raw_entry: Any) -> ModelPriceCreate | None:
    if not isinstance(raw_entry, dict):
        return None

    provider = str(raw_entry.get("litellm_provider") or "").strip().lower()
    model = model_name.strip()
    if not provider or not model:
        return None
    if len(provider) > MAX_PROVIDER_LENGTH or len(model) > MAX_MODEL_LENGTH:
        return None

    payload = ModelPriceCreate(
        provider=provider,
        model=model,
        input_usd_per_million=_usd_per_million(raw_entry.get("input_cost_per_token")),
        output_usd_per_million=_usd_per_million(raw_entry.get("output_cost_per_token")),
        cached_usd_per_million=_usd_per_million(raw_entry.get("cache_read_input_token_cost")),
        reasoning_usd_per_million=0.0,
    )

    if (
        payload.input_usd_per_million == 0
        and payload.output_usd_per_million == 0
        and payload.cached_usd_per_million == 0
        and payload.reasoning_usd_per_million == 0
    ):
        return None
    return payload


def _prices_equal(item: ModelPrice, payload: ModelPriceCreate) -> bool:
    return (
        item.provider == payload.provider
        and item.model == payload.model
        and item.input_usd_per_million == payload.input_usd_per_million
        and item.output_usd_per_million == payload.output_usd_per_million
        and item.cached_usd_per_million == payload.cached_usd_per_million
        and item.reasoning_usd_per_million == payload.reasoning_usd_per_million
        and item.source == LITELLM_PRICE_SOURCE
        and item.source_model == payload.model
        and item.auto_synced is True
    )


def sync_litellm_prices(
    session: Session,
    source_url: str | None = None,
) -> ModelPriceSyncResponse:
    effective_url = source_url or DEFAULT_LITELLM_PRICING_URL
    raw_data = _fetch_litellm_pricing_json(effective_url)
    existing = {
        (price.provider.lower(), price.model.lower()): price
        for price in session.exec(select(ModelPrice)).all()
    }

    now = datetime.now()
    created = 0
    updated = 0
    unchanged = 0
    skipped_manual = 0
    skipped_invalid = 0

    for model_name, raw_entry in raw_data.items():
        if not isinstance(model_name, str) or model_name == "sample_spec":
            skipped_invalid += 1
            continue
        payload = _litellm_entry_to_price(model_name, raw_entry)
        if payload is None:
            skipped_invalid += 1
            continue

        key = (payload.provider.lower(), payload.model.lower())
        item = existing.get(key)
        if item is None:
            item = ModelPrice(
                **payload.model_dump(),
                source=LITELLM_PRICE_SOURCE,
                source_model=model_name,
                auto_synced=True,
                last_synced_at=now,
                updated_at=now,
            )
            session.add(item)
            existing[key] = item
            created += 1
            continue

        if not item.auto_synced or item.source != LITELLM_PRICE_SOURCE:
            skipped_manual += 1
            continue

        if _prices_equal(item, payload):
            unchanged += 1
            continue

        item.provider = payload.provider
        item.model = payload.model
        item.input_usd_per_million = payload.input_usd_per_million
        item.output_usd_per_million = payload.output_usd_per_million
        item.cached_usd_per_million = payload.cached_usd_per_million
        item.reasoning_usd_per_million = payload.reasoning_usd_per_million
        item.source = LITELLM_PRICE_SOURCE
        item.source_model = model_name
        item.auto_synced = True
        item.last_synced_at = now
        item.updated_at = now
        updated += 1

    try:
        session.commit()
    except IntegrityError as exc:
        session.rollback()
        raise ConflictError("同步模型价格时出现重复 provider/model") from exc

    return ModelPriceSyncResponse(
        source_url=effective_url,
        total_entries=len(raw_data),
        imported=created + updated,
        created=created,
        updated=updated,
        unchanged=unchanged,
        skipped_manual=skipped_manual,
        skipped_invalid=skipped_invalid,
    )


def _price_key(provider: str | None, model: str | None) -> tuple[str, str]:
    return _normalize_price_part(provider), _normalize_price_part(model)


def _normalize_price_part(value: str | None) -> str:
    return (value or "").strip().casefold()


def _candidate_provider_keys(provider_key: str) -> tuple[str, ...]:
    return (provider_key, *QUERY_PROVIDER_ALIASES.get(provider_key, ()))
