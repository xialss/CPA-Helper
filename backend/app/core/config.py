import json
from datetime import datetime

from pydantic import BaseModel, Field
from sqlmodel import Session

from app.core.paths import get_data_dir
from app.core.security import create_secret
from app.db.session import get_engine
from app.models.app_setting import AppSetting


class LegacyAccountConfig(BaseModel):
    username: str
    password_hash: str
    password_salt: str


class CollectorConfig(BaseModel):
    enabled: bool = False
    cliaproxy_url: str = "http://127.0.0.1:8317"
    management_key: str = ""
    queue_name: str = "usage"
    batch_size: int = Field(default=100, ge=1, le=1000)
    poll_interval_seconds: float = Field(default=2.0, ge=0.2, le=3600)
    retry_interval_seconds: float = Field(default=10.0, ge=1, le=3600)


class CodexKeeperConfig(BaseModel):
    schedule_cron: str = Field(default="*/30 * * * *", min_length=1, max_length=120)
    quota_threshold: int = Field(default=100, ge=0, le=100)
    usage_timeout_seconds: int = Field(default=15, ge=1)
    cpa_timeout_seconds: int = Field(default=30, ge=1)
    max_retries: int = Field(default=2, ge=0, le=5)
    worker_threads: int = Field(default=8, ge=1, le=64)
    dry_run: bool = True
    auto_start_daemon: bool = False


DEFAULT_CODEX_KEEPER_PRIORITY_RULES: dict[str, int] = {
    "pro_20x": 20,
    "pro_5x": 5,
    "plus": 4,
    "team": 3,
    "free": 0,
}


class AppConfig(BaseModel):
    collector: CollectorConfig = Field(default_factory=CollectorConfig)
    codex_keeper: CodexKeeperConfig = Field(default_factory=CodexKeeperConfig)
    codex_keeper_priority_rules: dict[str, int] = Field(
        default_factory=lambda: dict(DEFAULT_CODEX_KEEPER_PRIORITY_RULES)
    )
    theme_preference: str = "system"
    session_secret: str = Field(default_factory=create_secret)


def create_default_config() -> AppConfig:
    return AppConfig()


def _legacy_settings_path():
    return get_data_dir() / "config" / "settings.json"


def _read_legacy_config() -> AppConfig | None:
    path = _legacy_settings_path()
    if not path.exists():
        return None
    raw = json.loads(path.read_text(encoding="utf-8"))
    return AppConfig.model_validate(
        {
            "collector": raw.get("collector", {}),
            "codex_keeper": _normalize_keeper_payload(raw.get("codex_keeper", {})),
            "codex_keeper_priority_rules": raw.get(
                "codex_keeper_priority_rules",
                DEFAULT_CODEX_KEEPER_PRIORITY_RULES,
            ),
            "theme_preference": raw.get("theme_preference", "system"),
            "session_secret": raw.get("session_secret") or create_secret(),
        }
    )


def read_legacy_account_config() -> LegacyAccountConfig | None:
    path = _legacy_settings_path()
    if not path.exists():
        return None
    raw = json.loads(path.read_text(encoding="utf-8"))
    account = raw.get("account")
    if not isinstance(account, dict):
        return None
    username = str(account.get("username") or "").strip()
    password_hash = str(account.get("password_hash") or "").strip()
    password_salt = str(account.get("password_salt") or "").strip()
    if not username or not password_hash or not password_salt:
        return None
    return LegacyAccountConfig(
        username=username,
        password_hash=password_hash,
        password_salt=password_salt,
    )


def _ensure_settings_table() -> None:
    AppSetting.__table__.create(get_engine(), checkfirst=True)


def _setting_to_config(setting: AppSetting) -> AppConfig:
    keeper_payload = _normalize_keeper_payload(_read_json_object(setting.codex_keeper_settings))
    priority_rules = _normalize_priority_rules(
        _read_json_object(setting.codex_keeper_priority_rules)
    )
    return AppConfig(
        collector=CollectorConfig(
            enabled=setting.collector_enabled,
            cliaproxy_url=setting.cliaproxy_url,
            management_key=setting.management_key,
            queue_name=setting.queue_name,
            batch_size=setting.batch_size,
            poll_interval_seconds=setting.poll_interval_seconds,
            retry_interval_seconds=setting.retry_interval_seconds,
        ),
        codex_keeper=CodexKeeperConfig.model_validate(keeper_payload),
        codex_keeper_priority_rules=priority_rules,
        theme_preference=setting.theme_preference,
        session_secret=setting.session_secret,
    )


def _setting_from_config(config: AppConfig, setting: AppSetting | None = None) -> AppSetting:
    target = setting or AppSetting(session_secret=config.session_secret)
    target.id = 1
    target.collector_enabled = config.collector.enabled
    target.cliaproxy_url = config.collector.cliaproxy_url
    target.management_key = config.collector.management_key
    target.queue_name = config.collector.queue_name
    target.batch_size = config.collector.batch_size
    target.poll_interval_seconds = config.collector.poll_interval_seconds
    target.retry_interval_seconds = config.collector.retry_interval_seconds
    target.theme_preference = config.theme_preference
    target.codex_keeper_settings = json.dumps(
        config.codex_keeper.model_dump(),
        ensure_ascii=False,
        sort_keys=True,
    )
    target.codex_keeper_priority_rules = json.dumps(
        _normalize_priority_rules(config.codex_keeper_priority_rules),
        ensure_ascii=False,
        sort_keys=True,
    )
    target.session_secret = config.session_secret
    target.updated_at = datetime.now()
    return target


def _read_json_object(value: str | None) -> dict:
    if not value:
        return {}
    try:
        payload = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return payload if isinstance(payload, dict) else {}


def _normalize_keeper_payload(value: dict) -> dict:
    payload = dict(value) if isinstance(value, dict) else {}
    if "schedule_cron" not in payload:
        payload["schedule_cron"] = _interval_seconds_to_cron(payload.get("interval_seconds"))
    payload.pop("interval_seconds", None)
    return payload


def _interval_seconds_to_cron(value: object) -> str:
    try:
        seconds = int(value)
    except (TypeError, ValueError):
        return "*/30 * * * *"
    if seconds < 60 or seconds % 60 != 0:
        return "*/30 * * * *"
    minutes = seconds // 60
    if minutes == 1:
        return "* * * * *"
    if minutes < 60 and 60 % minutes == 0:
        return f"*/{minutes} * * * *"
    if minutes == 60:
        return "0 * * * *"
    if minutes % 60 == 0:
        hours = minutes // 60
        if hours == 24:
            return "0 0 * * *"
        if 1 < hours < 24 and 24 % hours == 0:
            return f"0 */{hours} * * *"
    return "*/30 * * * *"


def _normalize_priority_rules(value: dict) -> dict[str, int]:
    rules = dict(DEFAULT_CODEX_KEEPER_PRIORITY_RULES)
    for raw_key, raw_value in value.items():
        key = str(raw_key).strip().lower()
        if not key:
            continue
        try:
            priority = int(raw_value)
        except (TypeError, ValueError):
            continue
        if 0 <= priority <= 20:
            rules[key] = priority
    return rules


def _ensure_app_setting(session: Session) -> AppSetting:
    setting = session.get(AppSetting, 1)
    if setting is not None:
        return setting

    config = _read_legacy_config() or create_default_config()
    setting = _setting_from_config(config)
    session.add(setting)
    session.commit()
    session.refresh(setting)
    return setting


def load_config() -> AppConfig:
    _ensure_settings_table()
    with Session(get_engine()) as session:
        return _setting_to_config(_ensure_app_setting(session))


def save_config(config: AppConfig) -> AppConfig:
    _ensure_settings_table()
    with Session(get_engine()) as session:
        setting = _ensure_app_setting(session)
        _setting_from_config(config, setting)
        session.add(setting)
        session.commit()
        session.refresh(setting)
        return _setting_to_config(setting)
