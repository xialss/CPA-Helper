from app.core.security import hash_api_key
from app.services.usage_parser import normalize_usage, redacted_raw_json


def test_normalize_usage_extracts_core_fields() -> None:
    normalized = normalize_usage(
        {
            "timestamp": "2026-05-04T08:30:00+08:00",
            "api_key": "sk-test-secret-value",
            "provider": "openai",
            "model": "gpt-4.1-mini",
            "endpoint": "/v1/chat/completions",
            "request_id": "req_123",
            "auth_type": "oauth",
            "usage": {
                "prompt_tokens": 12,
                "completion_tokens": 7,
                "cached_tokens": 3,
                "reasoning_tokens": 2,
            },
            "status_code": 200,
        }
    )

    assert normalized["api_key_hash"] == hash_api_key("sk-test-secret-value")
    assert "api_key" not in normalized
    assert normalized["provider"] == "openai"
    assert normalized["model"] == "gpt-4.1-mini"
    assert normalized["auth"] == "oauth"
    assert normalized["total_tokens"] == 19
    assert normalized["failed"] is False
    assert str(normalized["dedupe_key"]).startswith("raw:")


def test_normalize_usage_distinguishes_same_request_id_with_different_usage() -> None:
    first = {
        "timestamp": "2026-05-05T20:54:25.727730431+08:00",
        "api_key": "sk-test-usage-key",
        "provider": "codex",
        "model": "gpt-5.5",
        "endpoint": "GET /v1/responses",
        "request_id": "ebec718b",
        "tokens": {
            "input_tokens": 59167,
            "output_tokens": 2598,
            "cached_tokens": 50048,
            "reasoning_tokens": 488,
            "total_tokens": 61765,
        },
    }
    second = {
        **first,
        "timestamp": "2026-05-05T20:55:17.198345891+08:00",
        "tokens": {
            "input_tokens": 62155,
            "output_tokens": 2336,
            "cached_tokens": 58240,
            "reasoning_tokens": 236,
            "total_tokens": 64491,
        },
    }

    first_normalized = normalize_usage(first)
    second_normalized = normalize_usage(second)

    assert first_normalized["request_id"] == second_normalized["request_id"] == "ebec718b"
    assert first_normalized["dedupe_key"] != second_normalized["dedupe_key"]
    assert first_normalized["total_tokens"] == 61765
    assert second_normalized["total_tokens"] == 64491


def test_normalize_usage_keeps_identical_raw_message_idempotent() -> None:
    raw = {
        "timestamp": "2026-05-05T20:54:25.727730431+08:00",
        "api_key": "sk-test-usage-key",
        "request_id": "ebec718b",
        "tokens": {"total_tokens": 61765},
    }

    first = normalize_usage(raw)
    second = normalize_usage(raw)

    assert first["dedupe_key"] == second["dedupe_key"]


def test_normalize_usage_handles_unknown_key_and_failures() -> None:
    normalized = normalize_usage({"error": {"message": "failed"}, "total_tokens": 42})

    assert normalized["api_key_hash"] == hash_api_key("unknown")
    assert normalized["failed"] is True
    assert normalized["total_tokens"] == 42


def test_normalize_usage_keeps_auth_value_unmasked() -> None:
    normalized = normalize_usage({"api_key": "sk-test-usage-key", "auth": "oauth"})

    assert normalized["auth"] == "oauth"


def test_redacted_raw_json_masks_sensitive_keys() -> None:
    raw = '{"api_key":"sk-secret-value","nested":{"authorization":"Bearer abcdef"}}'

    redacted = redacted_raw_json(raw)

    assert isinstance(redacted, dict)
    assert redacted["api_key"] == "sk-sec...alue"
    assert redacted["nested"] == {"authorization": "Bearer...cdef"}
