import logging
from typing import Any

import httpx
from sqlmodel import Session, select

from app.core.config import load_config
from app.core.errors import ValidationAppError
from app.core.security import mask_secret
from app.models import ModelPrice, UserApiKey
from app.schemas.available_models import (
    AvailableModelItem,
    AvailableModelKeyError,
    AvailableModelPrice,
    AvailableModelSource,
    AvailableModelsResponse,
    is_json_scalar,
)
from app.services.pricing_service import find_matching_price, get_price_map

MODEL_LIST_TIMEOUT_SECONDS = 8
MODEL_CONTAINER_KEYS = ("data", "models", "items", "value")
MODEL_ID_KEYS = ("id", "model", "name")
OWNER_KEYS = ("owner", "owned_by", "organization")
RESERVED_METADATA_KEYS = {
    *MODEL_ID_KEYS,
    *OWNER_KEYS,
    "object",
    "created",
}

logger = logging.getLogger(__name__)


def list_current_user_available_models(session: Session, user_id: int) -> AvailableModelsResponse:
    bindings = session.exec(
        select(UserApiKey)
        .where(UserApiKey.user_id == user_id)
        .order_by(UserApiKey.created_at, UserApiKey.api_key_hash)
    ).all()
    queryable_bindings = [binding for binding in bindings if _normalized_api_key(binding.api_key)]
    if not bindings:
        return AvailableModelsResponse(
            has_api_keys=False,
            api_key_count=0,
            queryable_api_key_count=0,
        )
    if not queryable_bindings:
        return AvailableModelsResponse(
            has_api_keys=True,
            api_key_count=len(bindings),
            queryable_api_key_count=0,
        )

    prices = get_price_map(session)
    models_by_id: dict[str, AvailableModelItem] = {}
    errors: list[AvailableModelKeyError] = []
    for binding in queryable_bindings:
        source = _source_from_binding(binding)
        api_key = _normalized_api_key(binding.api_key)
        if api_key is None:
            continue
        try:
            remote_items = _fetch_model_items(api_key)
        except ValidationAppError as exc:
            logger.warning(
                "CPA model list request failed",
                extra={"api_key_hash": binding.api_key_hash, "user_id": binding.user_id},
            )
            errors.append(
                AvailableModelKeyError(
                    api_key_hash=source.api_key_hash,
                    api_key_preview=source.api_key_preview,
                    description=source.description,
                    message=exc.message,
                )
            )
            continue

        for raw_item in remote_items:
            model = _parse_model_item(raw_item, source)
            if model is None:
                continue
            existing = models_by_id.get(model.id)
            if existing is None:
                models_by_id[model.id] = model
            else:
                _merge_model(existing, model)

    if errors and not models_by_id:
        messages = "；".join(f"{error.description}: {error.message}" for error in errors[:3])
        raise ValidationAppError(f"查询 CPA 可用模型失败：{messages}")

    models = sorted(models_by_id.values(), key=lambda item: item.id.casefold())
    for model in models:
        model.price = _price_for_model(model, prices)

    return AvailableModelsResponse(
        has_api_keys=True,
        api_key_count=len(bindings),
        queryable_api_key_count=len(queryable_bindings),
        models=models,
        errors=errors,
    )


def _fetch_model_items(api_key: str) -> list[object]:
    try:
        with _models_client() as client:
            response = client.get("/v1/models", headers={"Authorization": f"Bearer {api_key}"})
    except httpx.HTTPError as exc:
        raise ValidationAppError(f"CPA 模型列表请求失败：{exc.__class__.__name__}") from exc
    _ensure_success(response)
    return _parse_models_response(response)


def _models_client() -> httpx.Client:
    config = load_config().collector
    return httpx.Client(base_url=config.cliaproxy_url, timeout=MODEL_LIST_TIMEOUT_SECONDS)


def _ensure_success(response: httpx.Response) -> None:
    if 200 <= response.status_code < 300:
        return
    raise ValidationAppError(f"CPA 模型列表请求失败：HTTP {response.status_code}")


def _parse_models_response(response: httpx.Response) -> list[object]:
    try:
        payload = response.json()
    except ValueError as exc:
        raise ValidationAppError("CPA 模型列表响应不是有效 JSON") from exc
    return _extract_model_items(payload)


def _extract_model_items(payload: object) -> list[object]:
    if isinstance(payload, list):
        return list(payload)
    if not isinstance(payload, dict):
        raise ValidationAppError("CPA 模型列表响应格式不支持")
    for key in MODEL_CONTAINER_KEYS:
        if key not in payload:
            continue
        value = payload[key]
        if isinstance(value, list):
            return list(value)
        raise ValidationAppError(f"CPA 模型列表响应字段 {key} 不是列表")
    if any(key in payload for key in MODEL_ID_KEYS):
        return [payload]
    raise ValidationAppError("CPA 模型列表响应缺少模型列表")


def _parse_model_item(raw_item: object, source: AvailableModelSource) -> AvailableModelItem | None:
    if isinstance(raw_item, str):
        model_id = raw_item.strip()
        if not model_id:
            return None
        return AvailableModelItem(id=model_id, name=model_id, sources=[source])

    if not isinstance(raw_item, dict):
        return None

    model_id = _first_string(raw_item, MODEL_ID_KEYS)
    if model_id is None:
        return None
    name = _string_value(raw_item.get("name")) or model_id
    return AvailableModelItem(
        id=model_id,
        name=name,
        object=_string_value(raw_item.get("object")),
        owner=_first_string(raw_item, OWNER_KEYS),
        created=_int_value(raw_item.get("created")),
        metadata=_metadata_from_raw_item(raw_item),
        sources=[source],
    )


def _merge_model(target: AvailableModelItem, incoming: AvailableModelItem) -> None:
    if target.name == target.id and incoming.name != incoming.id:
        target.name = incoming.name
    if target.object is None:
        target.object = incoming.object
    if target.owner is None:
        target.owner = incoming.owner
    if target.created is None:
        target.created = incoming.created
    for key, value in incoming.metadata.items():
        target.metadata.setdefault(key, value)
    known_source_hashes = {source.api_key_hash for source in target.sources}
    target.sources.extend(
        source for source in incoming.sources if source.api_key_hash not in known_source_hashes
    )
    target.sources.sort(key=lambda source: (source.description.casefold(), source.api_key_hash))


def _price_for_model(
    model: AvailableModelItem,
    prices: dict[tuple[str, str], ModelPrice],
) -> AvailableModelPrice | None:
    price = find_matching_price(prices, model.owner, model.id)
    if price is None:
        return None
    return AvailableModelPrice(
        provider=price.provider,
        model=price.model,
        input_usd_per_million=price.input_usd_per_million,
        output_usd_per_million=price.output_usd_per_million,
        cached_usd_per_million=price.cached_usd_per_million,
        reasoning_usd_per_million=price.reasoning_usd_per_million,
    )


def _metadata_from_raw_item(raw_item: dict[Any, Any]) -> dict[str, str | int | float | bool | None]:
    metadata: dict[str, str | int | float | bool | None] = {}
    for key, value in raw_item.items():
        key_text = str(key)
        if key_text in RESERVED_METADATA_KEYS or not is_json_scalar(value):
            continue
        metadata[key_text] = value
    return metadata


def _source_from_binding(binding: UserApiKey) -> AvailableModelSource:
    description = binding.description.strip() or "未命名 Key"
    return AvailableModelSource(
        api_key_hash=binding.api_key_hash,
        api_key_preview=mask_secret(binding.api_key),
        description=description,
    )


def _normalized_api_key(api_key: str | None) -> str | None:
    if api_key is None:
        return None
    normalized = api_key.strip()
    return normalized or None


def _first_string(raw_item: dict[Any, Any], keys: tuple[str, ...]) -> str | None:
    for key in keys:
        value = _string_value(raw_item.get(key))
        if value is not None:
            return value
    return None


def _string_value(value: object) -> str | None:
    if not isinstance(value, str):
        return None
    normalized = value.strip()
    return normalized or None


def _int_value(value: object) -> int | None:
    if isinstance(value, bool) or not isinstance(value, int):
        return None
    return value
