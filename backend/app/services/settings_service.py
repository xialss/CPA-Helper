from app.core.config import AppConfig, load_config, save_config
from app.schemas.settings import SettingsResponse, SettingsUpdateRequest


def settings_to_response(config: AppConfig | None = None) -> SettingsResponse:
    current = config or load_config()
    collector = current.collector
    return SettingsResponse(
        cliaproxy_url=collector.cliaproxy_url,
        management_key=collector.management_key,
        management_key_set=bool(collector.management_key),
        collector_enabled=collector.enabled,
        queue_name=collector.queue_name,
        batch_size=collector.batch_size,
        poll_interval_seconds=collector.poll_interval_seconds,
        retry_interval_seconds=collector.retry_interval_seconds,
        theme_preference=current.theme_preference,
    )


def update_settings(payload: SettingsUpdateRequest) -> SettingsResponse:
    config = load_config()
    collector = config.collector
    if payload.cliaproxy_url is not None:
        collector.cliaproxy_url = payload.cliaproxy_url.strip().rstrip("/")
    if payload.management_key is not None:
        collector.management_key = payload.management_key.strip()
    if payload.collector_enabled is not None:
        collector.enabled = payload.collector_enabled
    if payload.queue_name is not None:
        collector.queue_name = payload.queue_name.strip()
    if payload.batch_size is not None:
        collector.batch_size = payload.batch_size
    if payload.poll_interval_seconds is not None:
        collector.poll_interval_seconds = payload.poll_interval_seconds
    if payload.retry_interval_seconds is not None:
        collector.retry_interval_seconds = payload.retry_interval_seconds
    if payload.theme_preference is not None:
        config.theme_preference = payload.theme_preference
    save_config(config)
    return settings_to_response(config)
