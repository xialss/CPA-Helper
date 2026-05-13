import asyncio
import json
import logging
from datetime import datetime, timedelta
from typing import Any
from urllib.parse import urlparse

import httpx
from sqlmodel import Session

from app.core.config import CollectorConfig, load_config
from app.db.session import get_engine
from app.models import CollectorState
from app.schemas.collector import CollectorStatusResponse
from app.services.usage_service import save_usage_message

logger = logging.getLogger(__name__)
_REMOTE_ENABLED_UNSET = object()


class RespError(Exception):
    pass


async def _read_line(reader: asyncio.StreamReader) -> bytes:
    line = await reader.readline()
    if not line:
        raise RespError("RESP 连接已关闭")
    return line.rstrip(b"\r\n")


async def _read_resp(reader: asyncio.StreamReader) -> Any:
    prefix = await reader.readexactly(1)
    if prefix == b"+":
        return (await _read_line(reader)).decode()
    if prefix == b"-":
        raise RespError((await _read_line(reader)).decode(errors="replace"))
    if prefix == b":":
        return int(await _read_line(reader))
    if prefix == b"$":
        length = int(await _read_line(reader))
        if length == -1:
            return None
        data = await reader.readexactly(length)
        await reader.readexactly(2)
        return data
    if prefix == b"*":
        length = int(await _read_line(reader))
        if length == -1:
            return None
        return [await _read_resp(reader) for _ in range(length)]
    raise RespError("未知 RESP 响应")


async def _send_resp_command(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    *parts: str,
) -> Any:
    encoded = [part.encode() for part in parts]
    payload = f"*{len(encoded)}\r\n".encode()
    for part in encoded:
        payload += f"${len(part)}\r\n".encode() + part + b"\r\n"
    writer.write(payload)
    await writer.drain()
    return await _read_resp(reader)


def _decode_queue_item(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False)


async def _consume_resp_queue(config: CollectorConfig) -> list[str]:
    parsed = urlparse(config.cliaproxy_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or 8317
    reader, writer = await asyncio.wait_for(asyncio.open_connection(host, port), timeout=8)
    try:
        if config.management_key:
            await _send_resp_command(reader, writer, "AUTH", config.management_key)
        try:
            result = await _send_resp_command(
                reader,
                writer,
                "LPOP",
                config.queue_name,
                str(config.batch_size),
            )
        except RespError:
            items: list[str] = []
            for _ in range(config.batch_size):
                item = await _send_resp_command(reader, writer, "LPOP", config.queue_name)
                decoded = _decode_queue_item(item)
                if decoded is None:
                    break
                items.append(decoded)
            return items
        if result is None:
            return []
        if isinstance(result, list):
            return [decoded for item in result if (decoded := _decode_queue_item(item)) is not None]
        decoded = _decode_queue_item(result)
        return [decoded] if decoded is not None else []
    finally:
        writer.close()
        await writer.wait_closed()


def _state(session: Session) -> CollectorState:
    state = session.get(CollectorState, 1)
    if state is None:
        state = CollectorState(id=1)
        session.add(state)
        session.commit()
        session.refresh(state)
    return state


def update_state(
    *,
    running: bool | None = None,
    last_poll_at: datetime | None = None,
    last_success_at: datetime | None = None,
    last_error: str | None = None,
    remote_enabled: bool | None | object = _REMOTE_ENABLED_UNSET,
    records_delta: int = 0,
) -> None:
    with Session(get_engine()) as session:
        state = _state(session)
        if running is not None:
            state.running = running
        if last_poll_at is not None:
            state.last_poll_at = last_poll_at
        if last_success_at is not None:
            state.last_success_at = last_success_at
        if last_error is not None:
            state.last_error = last_error[:2000]
        if remote_enabled is not _REMOTE_ENABLED_UNSET:
            state.remote_enabled = remote_enabled if isinstance(remote_enabled, bool) else None
        if records_delta:
            state.records_collected += records_delta
        state.updated_at = datetime.now()
        session.add(state)
        session.commit()


async def _sync_remote_usage_enabled(config: CollectorConfig) -> tuple[bool | None, str | None]:
    if not config.management_key:
        return None, "管理密钥未设置，无法同步远端 usage 开关"
    headers = {
        "Authorization": f"Bearer {config.management_key}",
        "X-Management-Key": config.management_key,
    }
    async with httpx.AsyncClient(base_url=config.cliaproxy_url, timeout=8) as client:
        try:
            current = await client.get(
                "/v0/management/usage-statistics-enabled",
                headers=headers,
            )
        except httpx.HTTPError as exc:
            return None, f"远端 usage 开关查询失败：{exc.__class__.__name__}"

        current_enabled = _parse_remote_usage_enabled(current)
        if current_enabled is True:
            return True, None

        try:
            response = await client.put(
                "/v0/management/usage-statistics-enabled",
                json={"value": True},
                headers=headers,
            )
        except httpx.HTTPError as exc:
            if current_enabled is False:
                return False, f"远端 usage 开关开启失败：{exc.__class__.__name__}"
            return None, f"远端 usage 开关开启失败：{exc.__class__.__name__}"

        if 200 <= response.status_code < 300:
            return True, None
        if current_enabled is False:
            return False, f"远端 usage 开关开启失败：HTTP {response.status_code}"
        return None, f"远端 usage 开关同步失败：HTTP {response.status_code}"


def _parse_remote_usage_enabled(response: httpx.Response) -> bool | None:
    if not 200 <= response.status_code < 300:
        return None
    try:
        payload = response.json()
    except ValueError:
        return None
    if not isinstance(payload, dict):
        return None
    value = payload.get("usage-statistics-enabled")
    return value if isinstance(value, bool) else None


class CollectorRunner:
    def __init__(self) -> None:
        self._task: asyncio.Task[None] | None = None
        self._stop_event = asyncio.Event()
        self._last_remote_sync_at: datetime | None = None

    def start(self) -> None:
        if self._task is None or self._task.done():
            self._stop_event = asyncio.Event()
            self._task = asyncio.create_task(self._run(), name="cpa-helper-collector")

    async def stop(self) -> None:
        if self._task is None:
            return
        self._stop_event.set()
        await self._task

    async def _run(self) -> None:
        logger.info("Collector runner started")
        try:
            while not self._stop_event.is_set():
                config = load_config().collector
                try:
                    if config.management_key:
                        await self._sync_remote_if_needed(config)
                    if not config.enabled:
                        update_state(running=False)
                        await self._sleep_or_stop(min(config.poll_interval_seconds, 5))
                        continue
                    update_state(running=True, last_poll_at=datetime.now())
                    messages = await _consume_resp_queue(config)
                    inserted = 0
                    if messages:
                        with Session(get_engine()) as session:
                            for message in messages:
                                _, created = save_usage_message(session, message)
                                inserted += int(created)
                    update_state(
                        running=True,
                        last_success_at=datetime.now(),
                        last_error="",
                        records_delta=inserted,
                    )
                    await self._sleep_or_stop(config.poll_interval_seconds)
                except Exception as exc:
                    logger.warning(
                        "Collector poll failed",
                        extra={"error_type": exc.__class__.__name__},
                    )
                    update_state(
                        running=True,
                        last_error=f"{exc.__class__.__name__}: {str(exc)[:500]}",
                    )
                    await self._sleep_or_stop(config.retry_interval_seconds)
        finally:
            update_state(running=False)
            logger.info("Collector runner stopped")

    async def _sleep_or_stop(self, delay: float) -> None:
        try:
            await asyncio.wait_for(self._stop_event.wait(), timeout=delay)
        except TimeoutError:
            return

    async def _sync_remote_if_needed(self, config: CollectorConfig) -> None:
        now = datetime.now()
        if self._last_remote_sync_at and now - self._last_remote_sync_at < timedelta(seconds=60):
            return
        remote_enabled, error = await _sync_remote_usage_enabled(config)
        self._last_remote_sync_at = now
        update_state(remote_enabled=remote_enabled, last_error=error if error else None)


def get_collector_status(session: Session) -> CollectorStatusResponse:
    state = _state(session)
    config = load_config().collector
    return CollectorStatusResponse(
        enabled=config.enabled,
        running=state.running,
        queue_name=config.queue_name,
        batch_size=config.batch_size,
        poll_interval_seconds=config.poll_interval_seconds,
        retry_interval_seconds=config.retry_interval_seconds,
        last_poll_at=state.last_poll_at,
        last_success_at=state.last_success_at,
        last_error=state.last_error or None,
        remote_enabled=state.remote_enabled,
        records_collected=state.records_collected,
    )


collector_runner = CollectorRunner()
