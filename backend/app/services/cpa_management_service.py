from collections.abc import Iterable

import httpx

from app.core.config import load_config
from app.core.errors import ValidationAppError
from app.core.security import hash_api_key

MANAGEMENT_TIMEOUT_SECONDS = 8


def add_remote_api_key(api_key: str) -> None:
    keys = _remote_api_keys()
    if api_key not in keys:
        keys.append(api_key)
        _put_remote_api_keys(keys)


def remove_remote_api_key_hash(api_key_hash: str) -> None:
    keys = _remote_api_keys()
    next_keys = [api_key for api_key in keys if hash_api_key(api_key) != api_key_hash]
    if len(next_keys) != len(keys):
        _put_remote_api_keys(next_keys)


def _remote_api_keys() -> list[str]:
    try:
        with _management_client() as client:
            response = client.get("/v0/management/api-keys", headers=_management_headers())
    except httpx.HTTPError as exc:
        raise ValidationAppError(f"读取 CPA API KEY 失败：{exc.__class__.__name__}") from exc
    _ensure_success(response, "读取 CPA API KEY 失败")
    return _parse_string_list(response)


def _put_remote_api_keys(api_keys: list[str]) -> None:
    try:
        with _management_client() as client:
            response = client.put(
                "/v0/management/api-keys",
                json=api_keys,
                headers=_management_headers(),
            )
    except httpx.HTTPError as exc:
        raise ValidationAppError(f"写入 CPA API KEY 失败：{exc.__class__.__name__}") from exc
    _ensure_success(response, "写入 CPA API KEY 失败")


def _management_client() -> httpx.Client:
    config = load_config().collector
    if not config.management_key:
        raise ValidationAppError("管理密钥未设置，无法同步 CPA API KEY")
    return httpx.Client(base_url=config.cliaproxy_url, timeout=MANAGEMENT_TIMEOUT_SECONDS)


def _management_headers() -> dict[str, str]:
    management_key = load_config().collector.management_key
    return {
        "Authorization": f"Bearer {management_key}",
        "X-Management-Key": management_key,
    }


def _ensure_success(response: httpx.Response, message: str) -> None:
    if 200 <= response.status_code < 300:
        return
    raise ValidationAppError(f"{message}：HTTP {response.status_code}")


def _parse_string_list(response: httpx.Response) -> list[str]:
    try:
        payload = response.json()
    except ValueError as exc:
        raise ValidationAppError("CPA API KEY 响应不是有效 JSON") from exc
    return list(_iter_string_items(payload))


def _iter_string_items(payload: object) -> Iterable[str]:
    if isinstance(payload, list):
        for item in payload:
            if isinstance(item, str) and item.strip():
                yield item.strip()
        return
    if not isinstance(payload, dict):
        return
    for key in ("api-keys", "api_keys", "items", "value", "data"):
        value = payload.get(key)
        if isinstance(value, list):
            yield from _iter_string_items(value)
            return
