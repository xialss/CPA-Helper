import json
from datetime import datetime
from hashlib import sha256
from typing import Any

from app.core.security import hash_api_key, mask_secret, redact_json


class NormalizedUsage(dict[str, Any]):
    pass


TIMESTAMP_KEYS = ("timestamp", "time", "created_at", "createdAt", "request_time")
API_KEY_KEYS = ("api_key", "apiKey", "apikey", "key")
REQUEST_ID_KEYS = ("request_id", "requestId", "id")
PROVIDER_KEYS = ("provider", "provider_name")
MODEL_KEYS = ("model", "model_name")
ENDPOINT_KEYS = ("endpoint", "path", "route")
SOURCE_KEYS = ("source", "origin")
LATENCY_KEYS = ("latency_ms", "latency", "duration_ms", "duration")


def parse_raw_usage(raw: str | bytes | dict[str, Any] | list[Any]) -> tuple[Any, str]:
    if isinstance(raw, bytes):
        raw = raw.decode("utf-8", errors="replace")
    if isinstance(raw, str):
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = {"message": raw}
    else:
        parsed = raw
    raw_json = json.dumps(parsed, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return parsed, raw_json


def _walk(value: Any) -> list[tuple[str, Any]]:
    pairs: list[tuple[str, Any]] = []
    if isinstance(value, dict):
        for key, child in value.items():
            pairs.append((str(key), child))
            pairs.extend(_walk(child))
    elif isinstance(value, list):
        for child in value:
            pairs.extend(_walk(child))
    return pairs


def _find_first(data: Any, keys: tuple[str, ...]) -> Any:
    normalized = {key.lower() for key in keys}
    if isinstance(data, dict):
        for key in keys:
            if key in data:
                return data[key]
    for key, value in _walk(data):
        if key.lower() in normalized:
            return value
    return None


def _to_int(value: Any) -> int:
    if value is None or value == "":
        return 0
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return max(value, 0)
    if isinstance(value, float):
        return max(int(value), 0)
    if isinstance(value, str):
        try:
            return max(int(float(value)), 0)
        except ValueError:
            return 0
    return 0


def _to_float(value: Any) -> float | None:
    if value is None or value == "":
        return None
    if isinstance(value, int | float):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value)
        except ValueError:
            return None
    return None


def _to_str(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        normalized = value.strip()
        return normalized or None
    if isinstance(value, int | float | bool):
        return str(value)
    return None


def _parse_timestamp(value: Any) -> datetime:
    if isinstance(value, int | float):
        seconds = float(value) / 1000 if value > 10_000_000_000 else float(value)
        return datetime.fromtimestamp(seconds).astimezone().replace(tzinfo=None)
    if isinstance(value, str) and value.strip():
        normalized = value.strip().replace("Z", "+00:00")
        try:
            parsed = datetime.fromisoformat(normalized)
            if parsed.tzinfo is not None:
                parsed = parsed.astimezone().replace(tzinfo=None)
            return parsed
        except ValueError:
            return datetime.now()
    return datetime.now()


def _token_from_paths(data: Any, keys: tuple[str, ...]) -> int:
    value = _find_first(data, keys)
    return _to_int(value)


def _is_failed(data: Any) -> bool:
    failed = _find_first(data, ("failed", "is_failed", "error"))
    if isinstance(failed, bool):
        return failed
    if isinstance(failed, str):
        return failed.strip().lower() in {"true", "1", "yes", "failed", "error"}
    if failed:
        return True
    success = _find_first(data, ("success", "ok"))
    if isinstance(success, bool):
        return not success
    status = _to_int(_find_first(data, ("status", "status_code", "statusCode")))
    return status >= 400 if status else False


def _auth_label(data: Any) -> str | None:
    auth_type = _to_str(_find_first(data, ("auth_type",)))
    if auth_type:
        return auth_type
    return _to_str(_find_first(data, ("auth", "authentication")))


def normalize_usage(raw: str | bytes | dict[str, Any] | list[Any]) -> NormalizedUsage:
    parsed, raw_json = parse_raw_usage(raw)
    request_id = _to_str(_find_first(parsed, REQUEST_ID_KEYS))
    timestamp = _parse_timestamp(_find_first(parsed, TIMESTAMP_KEYS))
    api_key = _to_str(_find_first(parsed, API_KEY_KEYS)) or "unknown"
    input_tokens = _token_from_paths(
        parsed,
        ("input_tokens", "prompt_tokens", "promptTokens", "input"),
    )
    output_tokens = _token_from_paths(
        parsed,
        ("output_tokens", "completion_tokens", "completionTokens", "output"),
    )
    cached_tokens = _token_from_paths(
        parsed,
        ("cached_tokens", "cached_input_tokens", "cache_read_input_tokens", "cached"),
    )
    reasoning_tokens = _token_from_paths(parsed, ("reasoning_tokens", "reasoning"))
    total_tokens = _token_from_paths(parsed, ("total_tokens", "totalTokens", "total"))
    if total_tokens == 0:
        total_tokens = input_tokens + output_tokens
        if total_tokens == 0:
            total_tokens = cached_tokens + reasoning_tokens
    dedupe_source = raw_json
    return NormalizedUsage(
        timestamp=timestamp,
        api_key_hash=hash_api_key(api_key),
        provider=_to_str(_find_first(parsed, PROVIDER_KEYS)),
        model=_to_str(_find_first(parsed, MODEL_KEYS)),
        endpoint=_to_str(_find_first(parsed, ENDPOINT_KEYS)),
        source=_to_str(_find_first(parsed, SOURCE_KEYS)),
        request_id=request_id,
        auth=_auth_label(parsed),
        latency_ms=_to_float(_find_first(parsed, LATENCY_KEYS)),
        failed=_is_failed(parsed),
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cached_tokens=cached_tokens,
        reasoning_tokens=reasoning_tokens,
        total_tokens=total_tokens,
        dedupe_key=f"raw:{sha256(dedupe_source.encode()).hexdigest()}",
        raw_json=raw_json,
    )


def redacted_raw_json(raw_json: str) -> dict[str, object] | list[object] | str:
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return mask_secret(raw_json)
    redacted = redact_json(parsed)
    if isinstance(redacted, dict | list | str):
        return redacted
    return str(redacted)
