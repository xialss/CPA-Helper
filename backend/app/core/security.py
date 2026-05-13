import base64
import hashlib
import hmac
import json
import secrets
from datetime import UTC, datetime, timedelta
from typing import Any, NamedTuple

SESSION_COOKIE_NAME = "cpa_helper_session"
SESSION_MAX_AGE_SECONDS = 60 * 60 * 24 * 7

SENSITIVE_KEY_MARKERS = (
    "api_key",
    "apikey",
    "authorization",
    "bearer",
    "cookie",
    "key",
    "password",
    "secret",
    "token",
)


class SessionIdentity(NamedTuple):
    user_id: int | None
    username: str | None


def create_salt() -> str:
    return secrets.token_hex(16)


def create_secret() -> str:
    return secrets.token_urlsafe(48)


def hash_password(password: str, salt: str) -> str:
    digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), 260_000)
    return base64.urlsafe_b64encode(digest).decode()


def verify_password(password: str, salt: str, expected_hash: str) -> bool:
    return hmac.compare_digest(hash_password(password, salt), expected_hash)


def hash_api_key(api_key: str) -> str:
    value = api_key.strip() or "unknown"
    return hashlib.sha256(value.encode()).hexdigest()


def mask_secret(value: str | None, *, unknown_label: str = "unknown") -> str:
    if value is None:
        return unknown_label
    normalized = value.strip()
    if not normalized:
        return unknown_label
    if normalized == "unknown":
        return "unknown"
    if len(normalized) <= 4:
        return "****"
    if len(normalized) <= 8:
        return f"{normalized[:1]}...{normalized[-1:]}"
    return f"{normalized[:6]}...{normalized[-4:]}"


def _b64_encode(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).decode().rstrip("=")


def _b64_decode(value: str) -> bytes:
    padding = "=" * (-len(value) % 4)
    return base64.urlsafe_b64decode(value + padding)


def create_session_token(user_id: int, secret: str) -> str:
    expires = datetime.now(UTC) + timedelta(seconds=SESSION_MAX_AGE_SECONDS)
    payload = {"sub": str(user_id), "typ": "user_id", "exp": int(expires.timestamp())}
    payload_bytes = json.dumps(payload, separators=(",", ":"), sort_keys=True).encode()
    payload_part = _b64_encode(payload_bytes)
    signature = hmac.new(secret.encode(), payload_part.encode(), hashlib.sha256).digest()
    return f"{payload_part}.{_b64_encode(signature)}"


def read_session_token(token: str, secret: str) -> SessionIdentity | None:
    try:
        payload_part, signature_part = token.split(".", 1)
        expected = hmac.new(secret.encode(), payload_part.encode(), hashlib.sha256).digest()
        if not hmac.compare_digest(_b64_decode(signature_part), expected):
            return None
        payload = json.loads(_b64_decode(payload_part))
        if not isinstance(payload, dict):
            return None
        subject = payload.get("sub")
        subject_type = payload.get("typ")
        expires = payload.get("exp")
        if not isinstance(expires, int):
            return None
        if datetime.now(UTC).timestamp() > expires:
            return None
        if subject_type == "user_id":
            try:
                user_id = int(subject)
            except (TypeError, ValueError):
                return None
            return SessionIdentity(user_id=user_id, username=None)
        if isinstance(subject, str):
            return SessionIdentity(user_id=None, username=subject)
        return None
    except (ValueError, json.JSONDecodeError, TypeError):
        return None


def redact_json(value: Any) -> Any:
    if isinstance(value, dict):
        redacted: dict[str, Any] = {}
        for key, child in value.items():
            key_lower = str(key).lower()
            if any(marker in key_lower for marker in SENSITIVE_KEY_MARKERS):
                redacted[str(key)] = mask_secret(str(child)) if child else None
            else:
                redacted[str(key)] = redact_json(child)
        return redacted
    if isinstance(value, list):
        return [redact_json(item) for item in value]
    return value
