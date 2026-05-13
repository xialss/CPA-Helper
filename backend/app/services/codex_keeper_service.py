import json
import logging
import re
import threading
import time
from collections import deque
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict, dataclass, field
from datetime import date, datetime, timedelta
from pathlib import Path
from typing import Any, Protocol

import httpx
from croniter import CroniterBadCronError, CroniterBadDateError, croniter
from sqlmodel import Session, select

from app.core.config import (
    DEFAULT_CODEX_KEEPER_PRIORITY_RULES,
    AppConfig,
    load_config,
    save_config,
)
from app.core.errors import AppError, ConflictError, NotFoundError, ValidationAppError
from app.core.paths import get_data_dir
from app.db.session import get_engine
from app.models import CodexKeeperAuthState, CodexKeeperRun, CodexKeeperRunAccount
from app.schemas.codex_keeper import (
    CodexKeeperAccount,
    CodexKeeperAccountsResponse,
    CodexKeeperBulkDeleteFailure,
    CodexKeeperBulkDeleteResponse,
    CodexKeeperCronPreviewResponse,
    CodexKeeperPriorityRule,
    CodexKeeperSettingsResponse,
    CodexKeeperSettingsUpdateRequest,
    CodexKeeperStatsResponse,
    CodexKeeperStatusResponse,
)

logger = logging.getLogger(__name__)

KEEPER_USAGE_URL = "https://chatgpt.com/backend-api/wham/usage"
KEEPER_ACTION_NONE = "none"
KEEPER_DISABLED_ACTION = "禁用凭证"
KEEPER_DISABLED_ACTION_PREFIX = f"{KEEPER_DISABLED_ACTION}："
LEGACY_KEEPER_DISABLED_ACTION = "已禁用凭证"
LEGACY_KEEPER_DISABLED_ACTION_PREFIX = f"{LEGACY_KEEPER_DISABLED_ACTION}："
RECENT_LOG_LIMIT = 300
KEEPER_RUN_ACCOUNT_HISTORY_LIMIT = 50
KEEPER_LOG_RETENTION_DAYS = 3
KEEPER_LOG_PREFIX = "codex-keeper-"


@dataclass(slots=True)
class KeeperRuntimeSettings:
    cliaproxy_url: str
    management_key: str
    schedule_cron: str
    quota_threshold: int
    usage_timeout_seconds: int
    cpa_timeout_seconds: int
    max_retries: int
    worker_threads: int
    dry_run: bool
    auto_start_daemon: bool


@dataclass(slots=True)
class KeeperHttpResult:
    status_code: int | None
    json_data: dict[str, Any] | None = None
    brief: str = ""
    error: str | None = None


@dataclass(slots=True)
class KeeperUsageInfo:
    plan_type: str = "unknown"
    primary_used_percent: int = 0
    secondary_used_percent: int | None = None
    primary_reset_at: datetime | None = None
    secondary_reset_at: datetime | None = None
    primary_window_seconds: int | None = None
    secondary_window_seconds: int | None = None


@dataclass(slots=True)
class KeeperStateUpdate:
    kind: str
    name: str
    checked_at: datetime
    email: str | None = None
    account_type: str | None = None
    priority: int | None = None
    disabled: bool | None = None
    usage: KeeperUsageInfo | None = None
    status_code: int | None = None
    last_error: str | None = None
    latest_action: str | None = None
    restore_priority: int | None = None


@dataclass(slots=True)
class KeeperStats:
    total: int = 0
    healthy: int = 0
    status_disabled: int = 0
    status_enabled: int = 0
    priority_degraded: int = 0
    priority_restored: int = 0
    skipped: int = 0
    network_error: int = 0

    def response(self) -> CodexKeeperStatsResponse:
        return CodexKeeperStatsResponse(**asdict(self))


@dataclass(slots=True)
class KeeperAccountProcessResult:
    name: str
    result: str
    stats: KeeperStats = field(default_factory=KeeperStats)
    state_update: KeeperStateUpdate | None = None
    checked_at: datetime | None = None
    email: str | None = None
    account_type: str | None = None
    priority: int | None = None
    disabled: bool | None = None
    usage: KeeperUsageInfo | None = None
    status_code: int | None = None
    last_error: str | None = None
    latest_action: str | None = None


class KeeperCPAClientProtocol(Protocol):
    def list_auth_files(self) -> list[dict[str, Any]]: ...

    def get_auth_file(self, name: str) -> dict[str, Any] | None: ...

    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult: ...

    def set_auth_disabled(self, name: str, disabled: bool) -> None: ...

    def set_auth_priority(self, name: str, priority: int) -> None: ...

    def delete_auth_file(self, name: str) -> None: ...


class KeeperUsageClientProtocol(Protocol):
    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult: ...


class KeeperCPAClient:
    def __init__(self, settings: KeeperRuntimeSettings):
        self.base_url = settings.cliaproxy_url.rstrip("/")
        self.timeout = settings.cpa_timeout_seconds
        self.usage_timeout = settings.usage_timeout_seconds
        self.max_retries = settings.max_retries
        self.headers = {
            "Authorization": f"Bearer {settings.management_key}",
            "X-Management-Key": settings.management_key,
            "Accept": "application/json",
        }

    def list_auth_files(self) -> list[dict[str, Any]]:
        response = self._request("GET", "/v0/management/auth-files")
        payload = _json_payload(response, "读取 auth files 失败")
        items = _extract_list(payload, ("files", "items", "data", "value"))
        return [item for item in items if isinstance(item, dict)]

    def get_auth_file(self, name: str) -> dict[str, Any] | None:
        response = self._request(
            "GET",
            "/v0/management/auth-files/download",
            params={"name": name},
        )
        if response.status_code == 404:
            return None
        payload = _json_payload(response, "读取 auth file 详情失败")
        return payload if isinstance(payload, dict) else None

    def set_auth_disabled(self, name: str, disabled: bool) -> None:
        response = self._request(
            "PATCH",
            "/v0/management/auth-files/status",
            json={"name": name, "disabled": disabled},
        )
        _ensure_success(response, "写入 auth file 状态失败")

    def set_auth_priority(self, name: str, priority: int) -> None:
        response = self._request(
            "PATCH",
            "/v0/management/auth-files/fields",
            json={"name": name, "priority": priority},
        )
        _ensure_success(response, "写入 auth file priority 失败")

    def delete_auth_file(self, name: str) -> None:
        response = self._request(
            "DELETE",
            "/v0/management/auth-files",
            params={"name": name},
        )
        _ensure_success(response, "删除 auth file 失败")

    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult:
        headers = {
            "Authorization": "Bearer $TOKEN$",
            "Content-Type": "application/json",
            "User-Agent": "codex_cli_rs/0.76.0",
        }
        if account_id:
            headers["Chatgpt-Account-Id"] = account_id
        result = KeeperHttpResult(status_code=None, error="api-call 请求失败")
        for attempt in range(self.max_retries + 1):
            result = self._call_usage_once(auth_index, headers)
            if (
                result.status_code is not None
                and result.status_code >= 500
                and attempt < self.max_retries
            ):
                time.sleep(1)
                continue
            return result
        return result

    def _call_usage_once(self, auth_index: str, headers: dict[str, str]) -> KeeperHttpResult:
        try:
            response = self._request(
                "POST",
                "/v0/management/api-call",
                json={
                    "auth_index": auth_index,
                    "method": "GET",
                    "url": KEEPER_USAGE_URL,
                    "header": headers,
                    "data": "",
                },
                timeout=self.usage_timeout,
            )
        except ValidationAppError as exc:
            return KeeperHttpResult(status_code=None, error=exc.message)
        if not 200 <= response.status_code < 300:
            return KeeperHttpResult(
                status_code=None,
                json_data=_safe_response_json(response),
                error=f"api-call 管理请求失败：HTTP {response.status_code}",
                brief=_brief_response(response),
            )
        payload = _safe_response_json(response)
        if payload is None:
            return KeeperHttpResult(status_code=None, error="api-call 响应不是有效 JSON")
        status_code = _int_value(payload.get("status_code"), payload.get("statusCode"))
        if status_code is None:
            return KeeperHttpResult(status_code=None, error="api-call 响应缺少 status_code")
        body = payload.get("body")
        if status_code <= 0:
            return KeeperHttpResult(
                status_code=None,
                error=_brief_body(body) or "api-call 网络请求失败",
            )
        return KeeperHttpResult(
            status_code=status_code,
            json_data=_safe_body_json(body),
            brief=_brief_body(body),
        )

    def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        last_error: httpx.HTTPError | None = None
        for attempt in range(self.max_retries + 1):
            try:
                with httpx.Client(
                    base_url=self.base_url,
                    timeout=self.timeout,
                    headers=self.headers,
                ) as client:
                    response = client.request(method, path, **kwargs)
                if response.status_code >= 500 and attempt < self.max_retries:
                    time.sleep(1)
                    continue
                return response
            except httpx.HTTPError as exc:
                last_error = exc
                if attempt < self.max_retries:
                    time.sleep(1)
                    continue
        assert last_error is not None
        raise ValidationAppError(f"CLIProxyAPI 管理请求失败：{last_error.__class__.__name__}")


class KeeperUsageClient:
    def __init__(self, cpa_client: KeeperCPAClientProtocol):
        self.cpa_client = cpa_client

    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult:
        return self.cpa_client.check_usage(auth_index, account_id)


class CodexKeeperService:
    def __init__(
        self,
        settings: KeeperRuntimeSettings,
        priority_rules: dict[str, int],
        *,
        cpa_client: KeeperCPAClientProtocol | None = None,
        usage_client: KeeperUsageClientProtocol | None = None,
        log_callback: Callable[[str], None] | None = None,
        stats_callback: Callable[[KeeperStats], None] | None = None,
        run_id: int | None = None,
    ):
        self.settings = settings
        self.priority_rules = _normalize_priority_rules(priority_rules)
        self.cpa_client = cpa_client or KeeperCPAClient(settings)
        self.usage_client = usage_client or KeeperUsageClient(self.cpa_client)
        self._log_callback = log_callback
        self._stats_callback = stats_callback
        self.run_id = run_id
        self._stop_event = threading.Event()

    def request_stop(self) -> None:
        self._stop_event.set()

    def stop_requested(self) -> bool:
        return self._stop_event.is_set()

    def run_once(self) -> KeeperStats:
        stats = KeeperStats()
        self._log("开始 Codex 账号巡检")
        auth_files = [item for item in self.cpa_client.list_auth_files() if _is_codex_auth(item)]
        auth_names = {
            name
            for item in auth_files
            if (name := _string_value(item.get("name"))) is not None
        }
        deleted_states = _delete_missing_keeper_states(auth_names)
        if deleted_states:
            self._log(f"已清理 {deleted_states} 个已从 CPA 消失的账号状态")
        stats.total = len(auth_files)
        self._emit_stats(stats)
        if not auth_files:
            self._log("未发现 Codex auth file")
            return stats

        restore_priorities = _state_restore_priorities(auth_names)
        with ThreadPoolExecutor(max_workers=self.settings.worker_threads) as executor:
            future_map = {
                executor.submit(
                    self.process_auth_file,
                    item,
                    restore_priorities.get(_string_value(item.get("name")) or "unknown"),
                ): item
                for item in auth_files
            }
            for future in as_completed(future_map):
                result = future.result()
                self._apply_state_update(result.state_update)
                if self.run_id is not None:
                    _record_keeper_run_account(self.run_id, result, self.settings.quota_threshold)
                _merge_stats(stats, result.stats)
                self._emit_stats(stats)

        self._log(
            "巡检完成："
            f"健康 {stats.healthy}，坏凭证禁用 {stats.status_disabled}，"
            f"优先级降级 {stats.priority_degraded}，网络错误 {stats.network_error}"
        )
        return stats

    def process_auth_file(
        self,
        auth_info: dict[str, Any],
        restore_priority: int | None = None,
    ) -> KeeperAccountProcessResult:
        name = _string_value(auth_info.get("name")) or "unknown"
        stats = KeeperStats()
        now = datetime.now()
        try:
            detail = self.cpa_client.get_auth_file(name)
            if not detail:
                message = "获取 auth file 详情失败"
                state_update = self._state_update_error(name, message, now)
                stats.skipped += 1
                self._log(f"{name}: 获取详情失败")
                return self._process_result(
                    name,
                    "skipped",
                    stats,
                    now,
                    last_error=message,
                    latest_action=message,
                    state_update=state_update,
                )
            if not _is_codex_auth({**auth_info, **detail}):
                stats.skipped += 1
                return self._process_result(name, "skipped", stats, now, detail=detail)

            disabled = _bool_value(detail.get("disabled"), auth_info.get("disabled"))
            priority = _int_value(detail.get("priority"), auth_info.get("priority"))
            account_type = _account_type_from_detail(detail)
            if disabled:
                state_update = self._state_update_disabled(
                    name,
                    detail,
                    priority,
                    account_type,
                    now,
                )
                stats.skipped += 1
                self._log(f"{name}: 当前已禁用，跳过 usage 检测")
                return self._process_result(
                    name,
                    "disabled",
                    stats,
                    now,
                    detail=detail,
                    account_type=account_type,
                    priority=priority,
                    disabled=True,
                    state_update=state_update,
                )

            access_token = _string_value(detail.get("access_token"))
            if not access_token:
                disable_cause = "缺少 access token"
                effective_disabled = self._disable_bad_auth(
                    name,
                    cause=disable_cause,
                )
                if effective_disabled:
                    stats.status_disabled += 1
                display_action = _keeper_disable_operation_action(
                    disable_cause,
                    effective_disabled,
                )
                state_update = self._state_update_bad_auth(
                    name,
                    detail,
                    priority=priority,
                    account_type=account_type,
                    disabled=effective_disabled,
                    latest_action=display_action,
                    now=now,
                )
                return self._process_result(
                    name,
                    "status_disabled",
                    stats,
                    now,
                    detail=detail,
                    account_type=account_type,
                    priority=priority,
                    disabled=effective_disabled,
                    latest_action=display_action,
                    state_update=state_update,
                )

            result = self.usage_client.check_usage(
                _auth_index_value(
                    detail.get("auth_index"),
                    auth_info.get("auth_index"),
                    detail.get("authIndex"),
                    auth_info.get("authIndex"),
                    detail.get("index"),
                    auth_info.get("index"),
                    name,
                ),
                _string_value(detail.get("account_id")),
            )
            if result.status_code is None:
                message = f"网络检测失败：{result.error}"
                state_update = self._state_update_error(
                    name,
                    message,
                    now,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    account_type=account_type,
                )
                stats.network_error += 1
                self._log(f"{name}: 网络检测失败")
                return self._process_result(
                    name,
                    "network_error",
                    stats,
                    now,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    last_error=message,
                    latest_action=message,
                    state_update=state_update,
                )
            if result.status_code >= 500:
                message = f"usage 服务异常：HTTP {result.status_code}"
                state_update = self._state_update_error(
                    name,
                    message,
                    now,
                    status_code=result.status_code,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    account_type=account_type,
                )
                stats.network_error += 1
                self._log(f"{name}: usage 服务异常 HTTP {result.status_code}")
                return self._process_result(
                    name,
                    "network_error",
                    stats,
                    now,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    status_code=result.status_code,
                    last_error=message,
                    latest_action=message,
                    state_update=state_update,
                )
            if _is_bad_credential_status(result):
                cause = _bad_credential_cause(result)
                effective_disabled = self._disable_bad_auth(
                    name,
                    cause=cause,
                )
                if effective_disabled:
                    stats.status_disabled += 1
                display_action = _keeper_disable_operation_action(cause, effective_disabled)
                state_update = self._state_update_bad_auth(
                    name,
                    detail,
                    priority=priority,
                    account_type=account_type,
                    disabled=effective_disabled,
                    latest_action=display_action,
                    now=now,
                    status_code=result.status_code,
                )
                return self._process_result(
                    name,
                    "status_disabled",
                    stats,
                    now,
                    detail=detail,
                    account_type=account_type,
                    priority=priority,
                    disabled=effective_disabled,
                    status_code=result.status_code,
                    latest_action=display_action,
                    state_update=state_update,
                )
            if result.status_code != 200:
                message = f"usage 检测返回 HTTP {result.status_code}"
                state_update = self._state_update_error(
                    name,
                    message,
                    now,
                    status_code=result.status_code,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    account_type=account_type,
                )
                stats.skipped += 1
                return self._process_result(
                    name,
                    "skipped",
                    stats,
                    now,
                    detail=detail,
                    priority=priority,
                    disabled=disabled,
                    status_code=result.status_code,
                    last_error=message,
                    latest_action=message,
                    state_update=state_update,
                )

            usage = parse_keeper_usage_info(result.json_data or {})
            account_type = classify_account_type(usage, detail)
            quota_reached = _quota_reached(usage, self.settings.quota_threshold)
            next_priority = priority
            next_restore_priority: int | None = None
            priority_action: str | None = None
            priority_changed = False

            if _is_user_managed_low_priority(priority):
                priority_action = "保持低优先级：priority 小于 -1"
            elif quota_reached:
                next_priority, next_restore_priority, priority_changed = (
                    self._quota_reached_priority_state(
                        name,
                        priority,
                        restore_priority,
                        account_type,
                    )
                )
                if priority_changed:
                    stats.priority_degraded += 1
                priority_action = (
                    "降为低优先级："
                    f"额度使用率达到阈值 {self.settings.quota_threshold}%"
                )
            else:
                next_priority, next_restore_priority, priority_changed, priority_action = (
                    self._quota_recovered_priority_state(
                        name,
                        priority,
                        restore_priority,
                        account_type,
                    )
                )
                if priority_changed:
                    stats.priority_restored += 1

            state_update = self._state_update_success(
                name,
                detail,
                next_priority,
                account_type,
                usage,
                now,
                restore_priority=next_restore_priority,
                latest_action=priority_action,
            )
            stats.healthy += 1
            self._log(f"{name}: 巡检正常，类型 {account_type or 'unknown'}")
            return self._process_result(
                name,
                "priority_degraded" if quota_reached and priority_changed else "healthy",
                stats,
                now,
                detail=detail,
                account_type=account_type,
                priority=next_priority,
                disabled=False,
                usage=usage,
                status_code=result.status_code,
                latest_action=priority_action,
                state_update=state_update,
            )
        except Exception as exc:
            logger.exception("Codex keeper account processing failed", extra={"auth_name": name})
            message = f"巡检异常：{exc.__class__.__name__}"
            state_update = self._state_update_error(name, message, now)
            stats.skipped += 1
            return self._process_result(
                name,
                "skipped",
                stats,
                now,
                last_error=message,
                latest_action=message,
                state_update=state_update,
            )

    def _quota_reached_priority_state(
        self,
        name: str,
        priority: int | None,
        restore_priority: int | None,
        account_type: str | None,
    ) -> tuple[int | None, int | None, bool]:
        priority, _ = self._priority_or_type_default(priority, account_type)
        if priority is None:
            return None, None, False
        if priority < -1:
            return priority, None, False
        if priority == -1:
            return priority, restore_priority, False
        next_restore_priority = priority if priority > 20 else None
        if self.settings.dry_run:
            return priority, None, False
        self.cpa_client.set_auth_priority(name, -1)
        return -1, next_restore_priority, True

    def _quota_recovered_priority_state(
        self,
        name: str,
        priority: int | None,
        restore_priority: int | None,
        account_type: str | None,
    ) -> tuple[int | None, int | None, bool, str | None]:
        priority, priority_from_default = self._priority_or_type_default(
            priority,
            account_type,
        )
        if priority is None:
            return None, None, False, None
        if priority < -1:
            return priority, None, False, "保持低优先级：priority 小于 -1"
        if priority > 20:
            return priority, None, False, None
        if priority == -1 and restore_priority is not None:
            if self.settings.dry_run:
                return priority, restore_priority, False, "模拟恢复高优先级：额度已恢复"
            self.cpa_client.set_auth_priority(name, restore_priority)
            return restore_priority, None, True, "恢复高优先级：额度已恢复"
        if account_type and account_type in self.priority_rules:
            next_priority = self.priority_rules[account_type]
            if priority_from_default or priority != next_priority:
                if self.settings.dry_run:
                    return priority, None, False, (
                        f"模拟应用类型优先级：{account_type} -> priority {next_priority}"
                    )
                self.cpa_client.set_auth_priority(name, next_priority)
                return (
                    next_priority,
                    None,
                    True,
                    f"应用类型优先级：{account_type} -> priority {next_priority}",
                )
        return priority, None, False, None

    def _priority_or_type_default(
        self,
        priority: int | None,
        account_type: str | None,
    ) -> tuple[int | None, bool]:
        if priority is not None:
            return priority, False
        if account_type and account_type in self.priority_rules:
            return self.priority_rules[account_type], True
        return None, False

    def _process_result(
        self,
        name: str,
        result: str,
        stats: KeeperStats,
        now: datetime,
        *,
        detail: dict[str, Any] | None = None,
        account_type: str | None = None,
        priority: int | None = None,
        disabled: bool | None = None,
        usage: KeeperUsageInfo | None = None,
        status_code: int | None = None,
        last_error: str | None = None,
        latest_action: str | None = None,
        state_update: KeeperStateUpdate | None = None,
    ) -> KeeperAccountProcessResult:
        return KeeperAccountProcessResult(
            name=name,
            result=result,
            stats=stats,
            state_update=state_update,
            checked_at=now,
            email=_string_value(detail.get("email")) if detail else None,
            account_type=account_type,
            priority=priority,
            disabled=disabled,
            usage=usage,
            status_code=status_code,
            last_error=last_error,
            latest_action=_normalize_keeper_latest_action(latest_action),
        )

    def _disable_bad_auth(
        self,
        name: str,
        *,
        cause: str,
    ) -> bool:
        effective_disabled = False
        if not self.settings.dry_run:
            self.cpa_client.set_auth_disabled(name, True)
            effective_disabled = True
        if effective_disabled:
            self._log(f"{name}: {cause}，已写入禁用状态")
        else:
            self._log(f"{name}: {cause}，dry-run 未禁用")
        return effective_disabled

    def _state_update_disabled(
        self,
        name: str,
        detail: dict[str, Any],
        priority: int | None,
        account_type: str | None,
        now: datetime,
    ) -> KeeperStateUpdate:
        return KeeperStateUpdate(
            kind="disabled",
            name=name,
            checked_at=now,
            email=_string_value(detail.get("email")),
            account_type=account_type,
            priority=priority,
        )

    def _state_update_success(
        self,
        name: str,
        detail: dict[str, Any],
        priority: int | None,
        account_type: str | None,
        usage: KeeperUsageInfo,
        now: datetime,
        *,
        restore_priority: int | None = None,
        latest_action: str | None = None,
    ) -> KeeperStateUpdate:
        return KeeperStateUpdate(
            kind="success",
            name=name,
            checked_at=now,
            email=_string_value(detail.get("email")),
            account_type=account_type,
            priority=priority,
            usage=usage,
            latest_action=latest_action,
            restore_priority=restore_priority,
        )

    def _state_update_error(
        self,
        name: str,
        message: str,
        now: datetime,
        *,
        status_code: int | None = None,
        detail: dict[str, Any] | None = None,
        priority: int | None = None,
        disabled: bool | None = None,
        account_type: str | None = None,
    ) -> KeeperStateUpdate:
        return KeeperStateUpdate(
            kind="error",
            name=name,
            checked_at=now,
            email=_string_value(detail.get("email")) if detail else None,
            account_type=account_type,
            priority=priority,
            disabled=disabled,
            status_code=status_code,
            last_error=message[:2000],
        )

    def _state_update_bad_auth(
        self,
        name: str,
        detail: dict[str, Any],
        *,
        priority: int | None,
        account_type: str | None,
        disabled: bool,
        latest_action: str,
        now: datetime,
        status_code: int | None = None,
    ) -> KeeperStateUpdate:
        return KeeperStateUpdate(
            kind="bad_auth",
            name=name,
            checked_at=now,
            email=_string_value(detail.get("email")),
            account_type=account_type,
            priority=priority,
            disabled=disabled,
            status_code=status_code,
            latest_action=latest_action,
        )

    def _apply_state_update(self, update: KeeperStateUpdate | None) -> None:
        if update is None:
            return
        with Session(get_engine()) as session:
            state = _state_for_update(session, update.name)
            if update.kind == "disabled":
                previous_action = state.latest_action
                state.email = update.email
                state.account_type = update.account_type
                state.disabled = True
                state.priority = update.priority
                state.restore_priority = None
                state.latest_action = (
                    _normalize_keeper_latest_action(previous_action)
                    if _is_keeper_disabled_action(previous_action)
                    else None
                )
                state.last_error = None
                state.last_status_code = None
                _clear_usage_state(state)
                state.last_checked_at = update.checked_at
                state.last_healthy_at = None
            elif update.kind == "success":
                state.email = update.email
                state.account_type = update.account_type
                state.disabled = False
                state.priority = update.priority
                state.restore_priority = update.restore_priority
                state.latest_action = _normalize_keeper_latest_action(update.latest_action)
                state.last_error = None
                state.last_status_code = 200
                state.last_healthy_at = update.checked_at
                if update.usage is not None:
                    _write_usage_state(
                        state,
                        update.usage,
                        self.settings.quota_threshold,
                        update.checked_at,
                    )
            elif update.kind == "bad_auth":
                state.email = update.email
                state.account_type = update.account_type
                state.disabled = bool(update.disabled)
                state.priority = update.priority
                state.restore_priority = None
                state.latest_action = _normalize_keeper_latest_action(update.latest_action)
                state.last_error = None
                state.last_status_code = update.status_code
                _clear_usage_state(state)
                state.last_checked_at = update.checked_at
            elif update.kind == "error":
                if update.email is not None:
                    state.email = update.email
                if update.account_type is not None:
                    state.account_type = update.account_type
                if update.disabled is not None:
                    state.disabled = update.disabled
                if update.priority is not None:
                    state.priority = update.priority
                state.last_error = update.last_error
                state.last_status_code = update.status_code
                state.last_checked_at = update.checked_at
            else:
                raise ValidationAppError(f"未知账号状态写入类型：{update.kind}")
            state.updated_at = update.checked_at
            session.add(state)
            session.commit()

    def _log(self, message: str) -> None:
        if self._log_callback is not None:
            self._log_callback(message)

    def _emit_stats(self, stats: KeeperStats) -> None:
        if self._stats_callback is not None:
            self._stats_callback(stats)


class CodexKeeperRunner:
    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._thread: threading.Thread | None = None
        self._service: CodexKeeperService | None = None
        self._state = "stopped"
        self._detail = "未运行"
        self._mode: str | None = None
        self._stats = KeeperStats()
        self._logs: deque[str] = deque(maxlen=RECENT_LOG_LIMIT)
        self._last_started_at: datetime | None = None
        self._last_finished_at: datetime | None = None
        self._current_run_id: int | None = None
        self._loaded_persisted = False

    def load_persisted_state(self) -> None:
        with self._lock:
            self._load_persisted_state_locked()

    def start_once(self) -> None:
        self._start(daemon=False, mode="run_once")

    def start_daemon(self) -> None:
        self._start(daemon=True, mode="daemon")

    def start_auto_if_configured(self) -> None:
        config = load_config()
        if not config.codex_keeper.auto_start_daemon:
            return
        try:
            self.start_daemon()
        except ValidationAppError as exc:
            self._append_log(f"自动启动跳过：{exc.message}")
        except ConflictError:
            return

    def stop(self) -> None:
        with self._lock:
            if self._service is None:
                self._state = "stopped"
                self._detail = "未运行"
                return
            self._state = "stopping"
            self._detail = "正在等待当前巡检结束"
            self._service.request_stop()
            self._append_log("已请求停止")

    def clear_logs(self) -> None:
        with self._lock:
            self._logs.clear()
            _delete_keeper_log_files()

    def status(self) -> CodexKeeperStatusResponse:
        with self._lock:
            self._load_persisted_state_locked()
            return CodexKeeperStatusResponse(
                running=self._thread is not None and self._thread.is_alive(),
                state=self._state,
                detail=self._detail,
                mode=self._mode,
                last_started_at=self._last_started_at,
                last_finished_at=self._last_finished_at,
                stats=self._stats.response(),
                logs=list(self._logs),
            )

    def _start(self, *, daemon: bool, mode: str) -> None:
        with self._lock:
            self._load_persisted_state_locked()
            if self._thread is not None and self._thread.is_alive():
                raise ConflictError("Codex Keeper 正在运行")
            settings, priority_rules = build_runtime_settings()
            started_at = datetime.now()
            run_id = None
            if not daemon:
                run_id = _create_keeper_run(
                    mode=mode,
                    state="running",
                    detail="单轮巡检中",
                    started_at=started_at,
                    stats=KeeperStats(),
                )
            self._state = "running"
            self._detail = "守护运行中" if daemon else "单轮巡检中"
            self._mode = mode
            self._last_started_at = started_at
            self._last_finished_at = None
            self._stats = KeeperStats()
            self._current_run_id = run_id
            self._service = CodexKeeperService(
                settings,
                priority_rules,
                log_callback=self._append_log,
                stats_callback=self._set_stats,
                run_id=run_id,
            )
            target = self._run_daemon if daemon else self._run_once
            self._thread = threading.Thread(target=target, name="codex-keeper", daemon=True)
            self._thread.start()

    def _run_once(self) -> None:
        service = self._service
        if service is None:
            return
        try:
            stats = service.run_once()
            self._finish("stopped", "已停止", stats)
        except Exception as exc:
            logger.exception("Codex keeper run failed")
            self._append_log(f"运行异常：{exc.__class__.__name__}")
            self._finish("error", "运行异常", self._stats)

    def _run_daemon(self) -> None:
        service = self._service
        if service is None:
            return
        while not service.stop_requested():
            next_run_at = next_keeper_run_times(service.settings.schedule_cron, count=1)[0]
            self._append_log(f"下一轮计划：{next_run_at.strftime('%Y-%m-%d %H:%M:%S')}")
            _sleep_until_or_stop(service, next_run_at)
            if service.stop_requested():
                break
            self._begin_run_record("daemon", "守护轮次运行中")
            try:
                stats = service.run_once()
                self._set_stats(stats)
                self._finish_run_record("stopped", "本轮完成", stats)
            except Exception as exc:
                logger.exception("Codex keeper daemon round failed")
                self._append_log(f"守护轮次异常：{exc.__class__.__name__}")
                self._finish_run_record("error", "守护轮次异常", self._stats)
            finally:
                service.run_id = None
        self._finish("stopped", "已停止", self._stats)

    def _finish(self, state: str, detail: str, stats: KeeperStats) -> None:
        with self._lock:
            self._finish_run_record(state, detail, stats)
            self._state = state
            self._detail = detail
            self._stats = stats
            self._last_finished_at = datetime.now()
            self._service = None
            self._thread = None

    def _set_stats(self, stats: KeeperStats) -> None:
        with self._lock:
            self._stats = stats
            if self._current_run_id is not None:
                _update_keeper_run(
                    self._current_run_id,
                    state="running",
                    detail=self._detail,
                    stats=stats,
                )

    def _append_log(self, message: str) -> None:
        now = datetime.now()
        line = _format_keeper_log_line(message, now)
        with self._lock:
            self._logs.append(line)
            _append_keeper_log_line(line, now)

    def _begin_run_record(self, mode: str, detail: str) -> None:
        started_at = datetime.now()
        run_id = _create_keeper_run(
            mode=mode,
            state="running",
            detail=detail,
            started_at=started_at,
            stats=KeeperStats(),
        )
        with self._lock:
            self._mode = mode
            self._last_started_at = started_at
            self._last_finished_at = None
            self._stats = KeeperStats()
            self._current_run_id = run_id
            if self._service is not None:
                self._service.run_id = run_id

    def _finish_run_record(self, state: str, detail: str, stats: KeeperStats) -> None:
        with self._lock:
            run_id = self._current_run_id
            if run_id is None:
                return
            finished_at = datetime.now()
            self._current_run_id = None
            self._last_finished_at = finished_at
        _update_keeper_run(
            run_id,
            state=state,
            detail=detail,
            stats=stats,
            finished_at=finished_at,
        )
        _prune_keeper_run_account_history()

    def _load_persisted_state_locked(self) -> None:
        if self._loaded_persisted:
            return
        self._logs = deque(_load_keeper_log_lines(), maxlen=RECENT_LOG_LIMIT)
        latest_run = _latest_keeper_run()
        running = self._thread is not None and self._thread.is_alive()
        if latest_run is not None and not running:
            self._mode = latest_run.mode
            self._state = "stopped" if latest_run.state == "running" else latest_run.state
            self._detail = (
                "服务重启后加载最近一次巡检"
                if latest_run.state == "running"
                else latest_run.detail or "已加载最近一次巡检"
            )
            self._last_started_at = latest_run.started_at
            self._last_finished_at = latest_run.finished_at
            self._stats = _stats_from_keeper_run(latest_run)
        self._loaded_persisted = True


def _create_keeper_run(
    *,
    mode: str,
    state: str,
    detail: str,
    started_at: datetime,
    stats: KeeperStats,
) -> int:
    now = datetime.now()
    with Session(get_engine()) as session:
        run = CodexKeeperRun(
            mode=mode,
            state=state,
            detail=detail,
            started_at=started_at,
            total=stats.total,
            healthy=stats.healthy,
            status_disabled=stats.status_disabled,
            status_enabled=stats.status_enabled,
            priority_degraded=stats.priority_degraded,
            priority_restored=stats.priority_restored,
            skipped=stats.skipped,
            network_error=stats.network_error,
            created_at=now,
            updated_at=now,
        )
        session.add(run)
        session.commit()
        session.refresh(run)
        if run.id is None:
            raise ValidationAppError("创建巡检历史失败")
        return run.id


def _update_keeper_run(
    run_id: int,
    *,
    state: str,
    detail: str,
    stats: KeeperStats,
    finished_at: datetime | None = None,
) -> None:
    with Session(get_engine()) as session:
        run = session.get(CodexKeeperRun, run_id)
        if run is None:
            return
        run.state = state
        run.detail = detail
        run.finished_at = finished_at
        run.total = stats.total
        run.healthy = stats.healthy
        run.status_disabled = stats.status_disabled
        run.status_enabled = stats.status_enabled
        run.priority_degraded = stats.priority_degraded
        run.priority_restored = stats.priority_restored
        run.skipped = stats.skipped
        run.network_error = stats.network_error
        run.updated_at = datetime.now()
        session.add(run)
        session.commit()


def _latest_keeper_run() -> CodexKeeperRun | None:
    with Session(get_engine()) as session:
        return session.exec(
            select(CodexKeeperRun).order_by(CodexKeeperRun.id.desc()).limit(1)
        ).first()


def _stats_from_keeper_run(run: CodexKeeperRun) -> KeeperStats:
    return KeeperStats(
        total=run.total,
        healthy=run.healthy,
        status_disabled=run.status_disabled,
        status_enabled=run.status_enabled,
        priority_degraded=run.priority_degraded,
        priority_restored=run.priority_restored,
        skipped=run.skipped,
        network_error=run.network_error,
    )


def _record_keeper_run_account(
    run_id: int,
    result: KeeperAccountProcessResult,
    quota_threshold: int,
) -> None:
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, result.name)
        usage = result.usage
        row = CodexKeeperRunAccount(
            run_id=run_id,
            auth_name=result.name,
            email=result.email or (state.email if state else None),
            result=result.result,
            account_type=result.account_type or (state.account_type if state else None),
            priority=result.priority if result.priority is not None else _state_priority(state),
            disabled=(
                result.disabled
                if result.disabled is not None
                else (state.disabled if state else None)
            ),
            keeper_action=KEEPER_ACTION_NONE,
            primary_used_percent=(
                usage.primary_used_percent
                if usage is not None
                else (state.primary_used_percent if state else None)
            ),
            secondary_used_percent=(
                usage.secondary_used_percent
                if usage is not None
                else (state.secondary_used_percent if state else None)
            ),
            quota_threshold=(
                quota_threshold if usage is not None else (state.quota_threshold if state else None)
            ),
            last_status_code=(
                result.status_code
                if result.status_code is not None
                else (state.last_status_code if state else None)
            ),
            last_error=result.last_error or (state.last_error if state else None),
            latest_action=_normalize_keeper_latest_action(
                result.latest_action or (state.latest_action if state else None)
            ),
            checked_at=result.checked_at or datetime.now(),
            created_at=datetime.now(),
        )
        session.add(row)
        session.commit()


def _state_priority(state: CodexKeeperAuthState | None) -> int | None:
    return state.priority if state else None


def _prune_keeper_run_account_history(limit: int = KEEPER_RUN_ACCOUNT_HISTORY_LIMIT) -> None:
    with Session(get_engine()) as session:
        recent_run_ids = {
            run_id
            for run_id in session.exec(
                select(CodexKeeperRun.id).order_by(CodexKeeperRun.id.desc()).limit(limit)
            ).all()
            if run_id is not None
        }
        if not recent_run_ids:
            return
        stale_accounts = [
            account
            for account in session.exec(select(CodexKeeperRunAccount)).all()
            if account.run_id not in recent_run_ids
        ]
        for account in stale_accounts:
            session.delete(account)
        session.commit()


def _keeper_log_dir() -> Path:
    path = get_data_dir() / "logs"
    path.mkdir(parents=True, exist_ok=True)
    return path


def _keeper_log_path(now: datetime) -> Path:
    return _keeper_log_dir() / f"{KEEPER_LOG_PREFIX}{now.strftime('%Y-%m-%d')}.log"


def _format_keeper_log_line(
    message: str,
    now: datetime,
    *,
    level: str = "INFO",
    logger_name: str = logger.name,
) -> str:
    timestamp = now.strftime("%Y-%m-%d %H:%M:%S,%f")[:-3]
    return f"{timestamp} - {logger_name} - {level} - {message}"


def _append_keeper_log_line(line: str, now: datetime) -> None:
    _cleanup_keeper_log_files(now)
    try:
        with _keeper_log_path(now).open("a", encoding="utf-8") as file:
            file.write(f"{line}\n")
    except OSError:
        logger.exception("Failed to write Codex keeper log file")


def _load_keeper_log_lines(limit: int = RECENT_LOG_LIMIT) -> list[str]:
    _cleanup_keeper_log_files()
    lines: list[str] = []
    for path in sorted(_keeper_log_dir().glob(f"{KEEPER_LOG_PREFIX}*.log")):
        if _keeper_log_file_date(path) is None:
            continue
        try:
            lines.extend(path.read_text(encoding="utf-8").splitlines())
        except OSError:
            logger.exception("Failed to read Codex keeper log file", extra={"path": str(path)})
    return lines[-limit:]


def _delete_keeper_log_files() -> None:
    for path in _keeper_log_dir().glob(f"{KEEPER_LOG_PREFIX}*.log"):
        try:
            path.unlink(missing_ok=True)
        except OSError:
            logger.exception("Failed to delete Codex keeper log file", extra={"path": str(path)})


def _cleanup_keeper_log_files(now: datetime | None = None) -> None:
    current = now or datetime.now()
    cutoff = current.date() - timedelta(days=KEEPER_LOG_RETENTION_DAYS - 1)
    for path in _keeper_log_dir().glob(f"{KEEPER_LOG_PREFIX}*.log"):
        file_date = _keeper_log_file_date(path)
        if file_date is not None and file_date < cutoff:
            try:
                path.unlink(missing_ok=True)
            except OSError:
                logger.exception(
                    "Failed to cleanup Codex keeper log file",
                    extra={"path": str(path)},
                )


def _keeper_log_file_date(path: Path) -> date | None:
    if not path.name.startswith(KEEPER_LOG_PREFIX) or path.suffix != ".log":
        return None
    raw_date = path.name[len(KEEPER_LOG_PREFIX) : -len(path.suffix)]
    try:
        return datetime.strptime(raw_date, "%Y-%m-%d").date()
    except ValueError:
        return None


def get_keeper_settings() -> CodexKeeperSettingsResponse:
    return _settings_response(load_config())


def update_keeper_settings(
    payload: CodexKeeperSettingsUpdateRequest,
) -> CodexKeeperSettingsResponse:
    config = load_config()
    keeper = config.codex_keeper.model_copy()
    update_data = payload.model_dump(exclude_unset=True, exclude={"priority_rules"})
    if "schedule_cron" in update_data:
        update_data["schedule_cron"] = normalize_keeper_cron(update_data["schedule_cron"])
    keeper = keeper.model_copy(update=update_data)
    config.codex_keeper = keeper
    if payload.priority_rules is not None:
        config.codex_keeper_priority_rules = {
            item.account_type: item.priority for item in payload.priority_rules
        }
    saved = save_config(config)
    return _settings_response(saved)


def preview_keeper_cron(schedule_cron: str) -> CodexKeeperCronPreviewResponse:
    normalized = normalize_keeper_cron(schedule_cron)
    return CodexKeeperCronPreviewResponse(
        schedule_cron=normalized,
        next_run_times=next_keeper_run_times(normalized),
    )


def list_keeper_accounts() -> CodexKeeperAccountsResponse:
    states = _load_keeper_states()
    items = [_state_account_response(state) for state in states.values()]
    items.sort(key=lambda item: (item.email or "", item.name))
    return CodexKeeperAccountsResponse(items=items)


def enable_keeper_account(auth_name: str) -> None:
    settings, _ = build_runtime_settings()
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, auth_name)
        if state is None:
            raise NotFoundError("账号状态不存在")
        KeeperCPAClient(settings).set_auth_disabled(auth_name, False)
        state.disabled = False
        state.latest_action = None
        state.last_error = None
        state.last_status_code = None
        state.updated_at = datetime.now()
        session.add(state)
        session.commit()


def disable_keeper_account(auth_name: str) -> None:
    settings, _ = build_runtime_settings()
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, auth_name)
        if state is None:
            raise NotFoundError("账号状态不存在")
        KeeperCPAClient(settings).set_auth_disabled(auth_name, True)
        state.disabled = True
        state.restore_priority = None
        state.latest_action = None
        state.last_error = None
        state.last_status_code = None
        _clear_usage_state(state)
        state.last_checked_at = datetime.now()
        state.last_healthy_at = None
        state.updated_at = datetime.now()
        session.add(state)
        session.commit()


def delete_keeper_account(auth_name: str) -> None:
    settings, _ = build_runtime_settings()
    with Session(get_engine()) as session:
        _delete_keeper_account_state(session, KeeperCPAClient(settings), auth_name)


def bulk_delete_keeper_accounts(auth_names: list[str]) -> CodexKeeperBulkDeleteResponse:
    settings, _ = build_runtime_settings()
    cpa_client = KeeperCPAClient(settings)
    deleted: list[str] = []
    failed: list[CodexKeeperBulkDeleteFailure] = []

    for auth_name in _normalize_bulk_delete_auth_names(auth_names):
        with Session(get_engine()) as session:
            try:
                _delete_keeper_account_state(session, cpa_client, auth_name)
            except AppError as exc:
                session.rollback()
                failed.append(
                    CodexKeeperBulkDeleteFailure(name=auth_name, message=exc.message)
                )
                continue
        deleted.append(auth_name)

    return CodexKeeperBulkDeleteResponse(
        status="completed",
        deleted=deleted,
        failed=failed,
    )


def update_keeper_account_priority(auth_name: str, priority: int) -> None:
    settings, priority_rules = build_runtime_settings()
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, auth_name)
        if state is None:
            raise NotFoundError("账号状态不存在")
        _validate_manual_priority(priority, state.account_type, priority_rules)
        KeeperCPAClient(settings).set_auth_priority(auth_name, priority)
        state.priority = priority
        state.restore_priority = None
        state.latest_action = None
        state.last_error = None
        state.updated_at = datetime.now()
        session.add(state)
        session.commit()


def _delete_keeper_account_state(
    session: Session,
    cpa_client: KeeperCPAClientProtocol,
    auth_name: str,
) -> None:
    state = session.get(CodexKeeperAuthState, auth_name)
    if state is None:
        raise NotFoundError("账号状态不存在")
    if not state.disabled:
        raise ValidationAppError("只能删除已禁用账号")
    cpa_client.delete_auth_file(auth_name)
    session.delete(state)
    session.commit()


def _normalize_bulk_delete_auth_names(auth_names: list[str]) -> list[str]:
    seen: set[str] = set()
    normalized: list[str] = []
    for raw_name in auth_names:
        name = raw_name.strip()
        if not name:
            raise ValidationAppError("账号名称不能为空")
        if name in seen:
            continue
        seen.add(name)
        normalized.append(name)
    if not normalized:
        raise ValidationAppError("账号名称不能为空")
    return normalized


def build_runtime_settings() -> tuple[KeeperRuntimeSettings, dict[str, int]]:
    config = load_config()
    collector = config.collector
    if not collector.management_key:
        raise ValidationAppError("管理密钥未设置，无法运行 Codex Keeper")
    return (
        KeeperRuntimeSettings(
            cliaproxy_url=collector.cliaproxy_url.rstrip("/"),
            management_key=collector.management_key,
            schedule_cron=normalize_keeper_cron(config.codex_keeper.schedule_cron),
            quota_threshold=config.codex_keeper.quota_threshold,
            usage_timeout_seconds=config.codex_keeper.usage_timeout_seconds,
            cpa_timeout_seconds=config.codex_keeper.cpa_timeout_seconds,
            max_retries=config.codex_keeper.max_retries,
            worker_threads=config.codex_keeper.worker_threads,
            dry_run=config.codex_keeper.dry_run,
            auto_start_daemon=config.codex_keeper.auto_start_daemon,
        ),
        _normalize_priority_rules(config.codex_keeper_priority_rules),
    )


def parse_keeper_usage_info(payload: dict[str, Any]) -> KeeperUsageInfo:
    rate_limit = payload.get("rate_limit") if isinstance(payload.get("rate_limit"), dict) else {}
    parsed_at = datetime.now()
    primary = (
        rate_limit.get("primary_window")
        if isinstance(rate_limit.get("primary_window"), dict)
        else {}
    )
    secondary = (
        rate_limit.get("secondary_window")
        if isinstance(rate_limit.get("secondary_window"), dict)
        else None
    )
    return KeeperUsageInfo(
        plan_type=_string_value(payload.get("plan_type")) or "unknown",
        primary_used_percent=_int_value(primary.get("used_percent")) or 0,
        secondary_used_percent=(
            _int_value(secondary.get("used_percent")) if isinstance(secondary, dict) else None
        ),
        primary_reset_at=_quota_window_reset_at(primary, parsed_at),
        secondary_reset_at=(
            _quota_window_reset_at(secondary, parsed_at) if isinstance(secondary, dict) else None
        ),
        primary_window_seconds=_int_value(primary.get("limit_window_seconds")),
        secondary_window_seconds=(
            _int_value(secondary.get("limit_window_seconds"))
            if isinstance(secondary, dict)
            else None
        ),
    )


def _quota_window_reset_at(window: dict[str, Any], parsed_at: datetime) -> datetime | None:
    reset_at = _datetime_from_unix_timestamp(
        window.get("reset_at"),
        window.get("resetAt"),
        window.get("reset_at_seconds"),
        window.get("resetAtSeconds"),
    )
    if reset_at is not None:
        return reset_at
    reset_after_seconds = _int_value(
        window.get("reset_after_seconds"),
        window.get("resetAfterSeconds"),
    )
    if reset_after_seconds is None or reset_after_seconds < 0:
        return None
    return parsed_at + timedelta(seconds=reset_after_seconds)


def _datetime_from_unix_timestamp(*values: object) -> datetime | None:
    timestamp = _int_value(*values)
    if timestamp is None:
        return None
    seconds = timestamp / 1000 if timestamp > 10_000_000_000 else timestamp
    try:
        return datetime.fromtimestamp(seconds).astimezone().replace(tzinfo=None)
    except (OSError, OverflowError, ValueError):
        return None


def classify_account_type(usage: KeeperUsageInfo, detail: dict[str, Any]) -> str | None:
    values = [
        usage.plan_type,
        detail.get("plan_type"),
        detail.get("plan"),
        detail.get("tier"),
        detail.get("account_plan"),
        detail.get("subscription_plan"),
        detail.get("sku"),
    ]
    text = " ".join(str(value).lower() for value in values if value not in (None, ""))
    compact = text.replace("-", "_").replace(" ", "_")
    if "20x" in compact or "pro_20" in compact:
        return "pro_20x"
    if "5x" in compact or "pro_5" in compact:
        return "pro_5x"
    if "team" in compact or "business" in compact:
        return "team"
    if "plus" in compact:
        return "plus"
    if "free" in compact:
        return "free"
    return None


def _settings_response(config: AppConfig) -> CodexKeeperSettingsResponse:
    keeper = config.codex_keeper
    schedule_cron = normalize_keeper_cron(keeper.schedule_cron)
    return CodexKeeperSettingsResponse(
        cliaproxy_url=config.collector.cliaproxy_url,
        management_key_set=bool(config.collector.management_key),
        schedule_cron=schedule_cron,
        next_run_times=next_keeper_run_times(schedule_cron),
        quota_threshold=keeper.quota_threshold,
        usage_timeout_seconds=keeper.usage_timeout_seconds,
        cpa_timeout_seconds=keeper.cpa_timeout_seconds,
        max_retries=keeper.max_retries,
        worker_threads=keeper.worker_threads,
        dry_run=keeper.dry_run,
        auto_start_daemon=keeper.auto_start_daemon,
        priority_rules=[
            CodexKeeperPriorityRule(account_type=key, priority=value)
            for key, value in sorted(
                _normalize_priority_rules(config.codex_keeper_priority_rules).items()
            )
        ],
    )


def _load_keeper_states() -> dict[str, CodexKeeperAuthState]:
    with Session(get_engine()) as session:
        return {
            state.auth_name: state
            for state in session.exec(select(CodexKeeperAuthState)).all()
        }


def _state_account_response(state: CodexKeeperAuthState) -> CodexKeeperAccount:
    return CodexKeeperAccount(
        name=state.auth_name,
        email=state.email,
        account_type=state.account_type,
        disabled=state.disabled,
        priority=state.priority,
        primary_used_percent=state.primary_used_percent,
        secondary_used_percent=state.secondary_used_percent,
        primary_reset_at=state.primary_reset_at,
        secondary_reset_at=state.secondary_reset_at,
        quota_threshold=state.quota_threshold,
        last_status_code=state.last_status_code,
        last_error=state.last_error,
        latest_action=_normalize_keeper_latest_action(state.latest_action),
        last_checked_at=state.last_checked_at,
        last_healthy_at=state.last_healthy_at,
    )


def _delete_missing_keeper_states(current_auth_names: set[str]) -> int:
    with Session(get_engine()) as session:
        states = session.exec(select(CodexKeeperAuthState)).all()
        stale_states = [
            state for state in states if state.auth_name not in current_auth_names
        ]
        for state in stale_states:
            session.delete(state)
        if stale_states:
            session.commit()
        return len(stale_states)


def _state_for_update(session: Session, name: str) -> CodexKeeperAuthState:
    state = session.get(CodexKeeperAuthState, name)
    if state is None:
        state = CodexKeeperAuthState(auth_name=name)
    return state


def _write_usage_state(
    state: CodexKeeperAuthState,
    usage: KeeperUsageInfo,
    quota_threshold: int,
    now: datetime,
) -> None:
    state.primary_used_percent = usage.primary_used_percent
    state.secondary_used_percent = usage.secondary_used_percent
    state.primary_reset_at = usage.primary_reset_at
    state.secondary_reset_at = usage.secondary_reset_at
    state.quota_threshold = quota_threshold
    state.last_checked_at = now
    state.updated_at = now


def _clear_usage_state(state: CodexKeeperAuthState) -> None:
    state.primary_used_percent = None
    state.secondary_used_percent = None
    state.primary_reset_at = None
    state.secondary_reset_at = None
    state.quota_threshold = None


def _state_restore_priorities(names: set[str]) -> dict[str, int | None]:
    if not names:
        return {}
    with Session(get_engine()) as session:
        values: dict[str, int | None] = {}
        for name in names:
            state = session.get(CodexKeeperAuthState, name)
            if state is not None:
                values[name] = state.restore_priority
        return values


def _account_type_from_detail(detail: dict[str, Any]) -> str | None:
    usage = KeeperUsageInfo(plan_type=_string_value(detail.get("plan_type")) or "unknown")
    return classify_account_type(usage, detail)


def _validate_manual_priority(
    priority: int,
    account_type: str | None,
    priority_rules: dict[str, int],
) -> None:
    if priority < -1 or priority > 20:
        return
    if account_type and priority_rules.get(account_type) == priority:
        return
    if not account_type or account_type not in priority_rules:
        raise ValidationAppError("该账号类型没有可设置的系统 priority")
    expected = priority_rules[account_type]
    raise ValidationAppError(
        f"只能设置小于 -1、大于 20，或当前账号类型 {account_type} 对应的 priority {expected}"
    )


def _normalize_priority_rules(value: dict[str, int]) -> dict[str, int]:
    rules = dict(DEFAULT_CODEX_KEEPER_PRIORITY_RULES)
    for key, raw_priority in value.items():
        normalized_key = str(key).strip().lower()
        try:
            priority = int(raw_priority)
        except (TypeError, ValueError):
            continue
        if normalized_key and 0 <= priority <= 20:
            rules[normalized_key] = priority
    return rules


def _is_codex_auth(item: dict[str, Any]) -> bool:
    return _string_value(item.get("type")) == "codex"


def _is_user_managed_low_priority(priority: int | None) -> bool:
    return priority is not None and priority < -1


def _quota_reached(usage: KeeperUsageInfo, threshold: int) -> bool:
    secondary = usage.secondary_used_percent
    return usage.primary_used_percent >= threshold or (
        secondary is not None and secondary >= threshold
    )


def _is_bad_credential_status(result: KeeperHttpResult) -> bool:
    if result.status_code in (401, 402):
        return True
    text = " ".join(
        part.lower()
        for part in [
            result.brief,
            json.dumps(result.json_data, ensure_ascii=False) if result.json_data else "",
        ]
        if part
    )
    return "workspace" in text and any(word in text for word in ("disabled", "deactivated"))


def _bad_credential_cause(result: KeeperHttpResult) -> str:
    cause = (
        f"凭证不可用：HTTP {result.status_code}"
        if result.status_code is not None
        else "凭证不可用"
    )
    detail = result.brief or (
        _brief_body(result.json_data) if result.json_data is not None else ""
    )
    return f"{cause}，{detail}" if detail else cause


def _keeper_disabled_action(cause: str) -> str:
    return f"{KEEPER_DISABLED_ACTION_PREFIX}{cause}"


def _keeper_disable_operation_action(cause: str, effective_disabled: bool) -> str:
    if effective_disabled:
        return _keeper_disabled_action(cause)
    return f"模拟禁用：{cause}"


def _normalize_keeper_latest_action(value: str | None) -> str | None:
    if value is None:
        return None
    text = value.strip()
    if not text:
        return None
    if text == LEGACY_KEEPER_DISABLED_ACTION:
        return KEEPER_DISABLED_ACTION
    if text.startswith(LEGACY_KEEPER_DISABLED_ACTION_PREFIX):
        detail = text.removeprefix(LEGACY_KEEPER_DISABLED_ACTION_PREFIX)
        return f"{KEEPER_DISABLED_ACTION_PREFIX}{detail}"
    if text.endswith("，dry-run 未禁用") and not text.startswith("模拟禁用："):
        return f"模拟禁用：{text.removesuffix('，dry-run 未禁用')}"
    quota_match = re.match(r"^额度达到阈值\s+(.+)$", text)
    if quota_match:
        return f"降为低优先级：额度使用率达到阈值 {quota_match.group(1)}"
    type_priority_match = re.match(
        r"^按账号类型\s+(\S+)\s+应用 priority\s+(-?\d+)(.*)$",
        text,
    )
    if type_priority_match:
        account_type, priority, tail = type_priority_match.groups()
        prefix = "模拟应用类型优先级" if "dry-run" in tail else "应用类型优先级"
        return f"{prefix}：{account_type} -> priority {priority}"
    if text == "priority 小于 -1，保持用户低优先级":
        return "保持低优先级：priority 小于 -1"
    if text == "额度恢复，已恢复用户高优先级":
        return "恢复高优先级：额度已恢复"
    if text == "额度恢复，dry-run 未恢复用户高优先级":
        return "模拟恢复高优先级：额度已恢复"
    return text


def _is_keeper_disabled_action(value: str | None) -> bool:
    if value is None:
        return False
    return value in (KEEPER_DISABLED_ACTION, LEGACY_KEEPER_DISABLED_ACTION) or value.startswith(
        (KEEPER_DISABLED_ACTION_PREFIX, LEGACY_KEEPER_DISABLED_ACTION_PREFIX)
    )


def _merge_stats(target: KeeperStats, incoming: KeeperStats) -> None:
    target.healthy += incoming.healthy
    target.status_disabled += incoming.status_disabled
    target.status_enabled += incoming.status_enabled
    target.priority_degraded += incoming.priority_degraded
    target.priority_restored += incoming.priority_restored
    target.skipped += incoming.skipped
    target.network_error += incoming.network_error


def normalize_keeper_cron(value: str) -> str:
    expression = " ".join(value.strip().split())
    if len(expression.split()) != 5 or not croniter.is_valid(expression):
        raise ValidationAppError("Cron 表达式无效，请使用 5 段格式：分 时 日 月 周")
    return expression


def next_keeper_run_times(
    schedule_cron: str,
    *,
    count: int = 5,
    base: datetime | None = None,
) -> list[datetime]:
    expression = normalize_keeper_cron(schedule_cron)
    try:
        iterator = croniter(expression, base or datetime.now())
        return [iterator.get_next(datetime) for _ in range(count)]
    except (CroniterBadCronError, CroniterBadDateError) as exc:
        raise ValidationAppError("Cron 表达式无效，请使用 5 段格式：分 时 日 月 周") from exc


def _sleep_until_or_stop(service: CodexKeeperService, next_run_at: datetime) -> None:
    while not service.stop_requested():
        remaining = (next_run_at - datetime.now()).total_seconds()
        if remaining <= 0:
            return
        time.sleep(min(1.0, remaining))


def _json_payload(response: httpx.Response, message: str) -> object:
    _ensure_success(response, message)
    try:
        return response.json()
    except ValueError as exc:
        raise ValidationAppError(f"{message}：响应不是有效 JSON") from exc


def _ensure_success(response: httpx.Response, message: str) -> None:
    if 200 <= response.status_code < 300:
        return
    raise ValidationAppError(f"{message}：HTTP {response.status_code}")


def _safe_response_json(response: httpx.Response) -> dict[str, Any] | None:
    try:
        payload = response.json()
    except ValueError:
        return None
    return payload if isinstance(payload, dict) else None


def _safe_body_json(body: object) -> dict[str, Any] | None:
    if isinstance(body, dict):
        return body
    if not isinstance(body, str):
        return None
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return None
    return payload if isinstance(payload, dict) else None


def _extract_list(payload: object, keys: tuple[str, ...]) -> list[object]:
    if isinstance(payload, list):
        return list(payload)
    if not isinstance(payload, dict):
        return []
    for key in keys:
        value = payload.get(key)
        if isinstance(value, list):
            return list(value)
    return []


def _brief_response(response: httpx.Response, limit: int = 160) -> str:
    text = response.text.strip().replace("\n", " ")
    return text[:limit] + ("..." if len(text) > limit else "")


def _brief_body(body: object, limit: int = 160) -> str:
    if isinstance(body, str):
        text = body
    elif body is None:
        text = ""
    else:
        text = json.dumps(body, ensure_ascii=False)
    text = text.strip().replace("\n", " ")
    return text[:limit] + ("..." if len(text) > limit else "")


def _auth_index_value(*values: object) -> str:
    for value in values:
        if isinstance(value, bool) or value in (None, ""):
            continue
        if isinstance(value, str):
            normalized = value.strip()
            if normalized:
                return normalized
            continue
        if isinstance(value, int):
            return str(value)
    return "unknown"


def _string_value(value: object) -> str | None:
    if not isinstance(value, str):
        return None
    normalized = value.strip()
    return normalized or None


def _int_value(*values: object) -> int | None:
    for value in values:
        if isinstance(value, bool) or value in (None, ""):
            continue
        try:
            return int(value)
        except (TypeError, ValueError):
            continue
    return None


def _bool_value(*values: object) -> bool:
    for value in values:
        if isinstance(value, bool):
            return value
        if isinstance(value, str):
            normalized = value.strip().lower()
            if normalized in {"1", "true", "yes", "on"}:
                return True
            if normalized in {"0", "false", "no", "off"}:
                return False
    return False


codex_keeper_runner = CodexKeeperRunner()
