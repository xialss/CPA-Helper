import json
import re
import threading
from collections.abc import Generator
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import httpx
import pytest
from sqlmodel import Session, select

from app.core.errors import ValidationAppError
from app.db.session import get_engine, init_db, reset_engine_for_tests
from app.models import CodexKeeperAuthState, CodexKeeperRun, CodexKeeperRunAccount
from app.services import codex_keeper_service
from app.services.codex_keeper_service import (
    CodexKeeperService,
    KeeperAccountProcessResult,
    KeeperCPAClient,
    KeeperHttpResult,
    KeeperRuntimeSettings,
    KeeperStats,
    next_keeper_run_times,
)

PRIMARY_RESET_TS = 1_778_509_125
SECONDARY_RESET_TS = 1_778_569_664


class FakeCPAClient:
    def __init__(self, detail: dict):
        self.detail = detail
        self.priority_calls: list[tuple[str, int]] = []
        self.disabled_calls: list[tuple[str, bool]] = []

    def list_auth_files(self) -> list[dict]:
        return [{"name": "codex-a.json", "type": "codex"}]

    def get_auth_file(self, name: str) -> dict:
        return dict(self.detail)

    def set_auth_disabled(self, name: str, disabled: bool) -> None:
        self.disabled_calls.append((name, disabled))

    def set_auth_priority(self, name: str, priority: int) -> None:
        self.priority_calls.append((name, priority))

    def delete_auth_file(self, name: str) -> None:
        raise AssertionError("delete should not be called during inspection")

    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult:
        return KeeperHttpResult(status_code=200, json_data={})


class FakeUsageClient:
    def __init__(self, result: KeeperHttpResult):
        self.result = result
        self.calls: list[tuple[str, str | None]] = []

    def check_usage(self, auth_index: str, account_id: str | None = None) -> KeeperHttpResult:
        self.calls.append((auth_index, account_id))
        return self.result


@pytest.fixture(autouse=True)
def isolated_db(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Generator[None, None, None]:
    monkeypatch.setenv("CPA_HELPER_DATA_DIR", str(tmp_path))
    reset_engine_for_tests()
    init_db()
    yield
    reset_engine_for_tests()


def _settings() -> KeeperRuntimeSettings:
    return KeeperRuntimeSettings(
        cliaproxy_url="http://cpa.test",
        management_key="secret",
        schedule_cron="*/30 * * * *",
        quota_threshold=100,
        usage_timeout_seconds=1,
        cpa_timeout_seconds=1,
        max_retries=0,
        worker_threads=1,
        dry_run=False,
        auto_start_daemon=False,
    )


def _detail(priority: int | None = 7) -> dict:
    detail = {
        "name": "codex-a.json",
        "type": "codex",
        "email": "a@example.com",
        "access_token": "access-token",
        "disabled": False,
    }
    if priority is not None:
        detail["priority"] = priority
    return detail


def _usage(percent: int, plan_type: str = "plus") -> KeeperHttpResult:
    return KeeperHttpResult(
        status_code=200,
        json_data={
            "plan_type": plan_type,
            "rate_limit": {
                "primary_window": {
                    "used_percent": percent,
                    "limit_window_seconds": 18000,
                    "reset_at": PRIMARY_RESET_TS,
                }
            },
        },
    )


def _local_from_timestamp(value: int) -> datetime:
    return datetime.fromtimestamp(value).astimezone().replace(tzinfo=None)


def test_parse_keeper_usage_info_reads_quota_reset_times() -> None:
    usage = codex_keeper_service.parse_keeper_usage_info(
        {
            "plan_type": "plus",
            "rate_limit": {
                "primary_window": {
                    "used_percent": 34,
                    "limit_window_seconds": 18000,
                    "reset_at": PRIMARY_RESET_TS,
                },
                "secondary_window": {
                    "used_percent": 95,
                    "limit_window_seconds": 604800,
                    "reset_at": SECONDARY_RESET_TS,
                },
            },
        }
    )

    assert usage.primary_used_percent == 34
    assert usage.secondary_used_percent == 95
    assert usage.primary_reset_at == _local_from_timestamp(PRIMARY_RESET_TS)
    assert usage.secondary_reset_at == _local_from_timestamp(SECONDARY_RESET_TS)


def test_bulk_delete_keeper_accounts_deletes_disabled_accounts(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    delete_calls: list[str] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: KeeperRuntimeSettings):
            self.settings = settings

        def delete_auth_file(self, name: str) -> None:
            delete_calls.append(name)

    monkeypatch.setattr(codex_keeper_service, "build_runtime_settings", lambda: (_settings(), {}))
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="disabled-a.json", disabled=True),
                CodexKeeperAuthState(auth_name="disabled-b.json", disabled=True),
            ]
        )
        session.commit()

    result = codex_keeper_service.bulk_delete_keeper_accounts(
        ["disabled-a.json", "disabled-a.json", "disabled-b.json"]
    )

    assert result.status == "completed"
    assert result.deleted == ["disabled-a.json", "disabled-b.json"]
    assert result.failed == []
    assert delete_calls == ["disabled-a.json", "disabled-b.json"]
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "disabled-a.json") is None
        assert session.get(CodexKeeperAuthState, "disabled-b.json") is None


def test_bulk_delete_keeper_accounts_keeps_failed_items(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    delete_calls: list[str] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: KeeperRuntimeSettings):
            self.settings = settings

        def delete_auth_file(self, name: str) -> None:
            delete_calls.append(name)
            if name == "remote-fail.json":
                raise ValidationAppError("远端删除失败")

    monkeypatch.setattr(codex_keeper_service, "build_runtime_settings", lambda: (_settings(), {}))
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="deleted.json", disabled=True),
                CodexKeeperAuthState(auth_name="remote-fail.json", disabled=True),
                CodexKeeperAuthState(auth_name="enabled.json", disabled=False),
            ]
        )
        session.commit()

    result = codex_keeper_service.bulk_delete_keeper_accounts(
        ["deleted.json", "remote-fail.json", "enabled.json"]
    )

    assert result.status == "completed"
    assert result.deleted == ["deleted.json"]
    assert [item.model_dump() for item in result.failed] == [
        {"name": "remote-fail.json", "message": "远端删除失败"},
        {"name": "enabled.json", "message": "只能删除已禁用账号"},
    ]
    assert delete_calls == ["deleted.json", "remote-fail.json"]
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "deleted.json") is None
        assert session.get(CodexKeeperAuthState, "remote-fail.json") is not None
        assert session.get(CodexKeeperAuthState, "enabled.json") is not None


def test_keeper_cpa_client_checks_usage_through_api_call(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[dict[str, Any]] = []

    class FakeHTTPClient:
        def __init__(self, **kwargs: Any):
            self.kwargs = kwargs

        def __enter__(self) -> "FakeHTTPClient":
            return self

        def __exit__(self, *args: object) -> None:
            return None

        def request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
            calls.append(
                {
                    "client": self.kwargs,
                    "method": method,
                    "path": path,
                    "kwargs": kwargs,
                }
            )
            return httpx.Response(
                200,
                json={
                    "status_code": 200,
                    "body": json.dumps(
                        {
                            "plan_type": "plus",
                            "rate_limit": {
                                "primary_window": {
                                    "used_percent": 42,
                                    "limit_window_seconds": 18000,
                                }
                            },
                        }
                    ),
                },
            )

    monkeypatch.setattr(codex_keeper_service.httpx, "Client", FakeHTTPClient)

    settings = _settings()
    settings.usage_timeout_seconds = 7
    result = KeeperCPAClient(settings).check_usage("codex-a.json", "account-1")

    assert result.status_code == 200
    assert result.json_data is not None
    assert result.json_data["plan_type"] == "plus"
    assert calls == [
        {
            "client": {
                "base_url": "http://cpa.test",
                "timeout": 1,
                "headers": {
                    "Authorization": "Bearer secret",
                    "X-Management-Key": "secret",
                    "Accept": "application/json",
                },
            },
            "method": "POST",
            "path": "/v0/management/api-call",
            "kwargs": {
                "json": {
                    "auth_index": "codex-a.json",
                    "method": "GET",
                    "url": "https://chatgpt.com/backend-api/wham/usage",
                    "header": {
                        "Authorization": "Bearer $TOKEN$",
                        "Content-Type": "application/json",
                        "User-Agent": "codex_cli_rs/0.76.0",
                        "Chatgpt-Account-Id": "account-1",
                    },
                    "data": "",
                },
                "timeout": 7,
            },
        }
    ]


def test_keeper_cpa_client_management_error_is_network_result(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class FakeHTTPClient:
        def __init__(self, **kwargs: Any):
            self.kwargs = kwargs

        def __enter__(self) -> "FakeHTTPClient":
            return self

        def __exit__(self, *args: object) -> None:
            return None

        def request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
            return httpx.Response(401, json={"detail": "bad management key"})

    monkeypatch.setattr(codex_keeper_service.httpx, "Client", FakeHTTPClient)

    result = KeeperCPAClient(_settings()).check_usage("codex-a.json")

    assert result.status_code is None
    assert result.error == "api-call 管理请求失败：HTTP 401"
    assert "bad management key" in result.brief


def test_next_keeper_run_times_uses_cron_expression() -> None:
    next_times = next_keeper_run_times(
        "*/15 * * * *",
        base=datetime(2026, 5, 11, 12, 7),
    )

    assert next_times[:5] == [
        datetime(2026, 5, 11, 12, 15),
        datetime(2026, 5, 11, 12, 30),
        datetime(2026, 5, 11, 12, 45),
        datetime(2026, 5, 11, 13, 0),
        datetime(2026, 5, 11, 13, 15),
    ]


def test_list_keeper_accounts_reads_persisted_states_only(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def fail_build_runtime_settings() -> tuple[KeeperRuntimeSettings, dict[str, int]]:
        raise AssertionError("account status list should not call CPA runtime settings")

    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        fail_build_runtime_settings,
    )
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(
                    auth_name="codex-a.json",
                    email="state-a@example.com",
                    account_type="plus",
                    priority=4,
                    primary_reset_at=_local_from_timestamp(PRIMARY_RESET_TS),
                    secondary_reset_at=_local_from_timestamp(SECONDARY_RESET_TS),
                ),
                CodexKeeperAuthState(
                    auth_name="codex-b.json",
                    email="state-b@example.com",
                    account_type="free",
                    disabled=True,
                    priority=-1,
                ),
            ]
        )
        session.commit()

    response = codex_keeper_service.list_keeper_accounts()
    items = {item.name: item for item in response.items}

    assert list(items) == ["codex-a.json", "codex-b.json"]
    assert items["codex-a.json"].email == "state-a@example.com"
    assert items["codex-a.json"].priority == 4
    assert items["codex-a.json"].disabled is False
    assert items["codex-a.json"].primary_reset_at == _local_from_timestamp(PRIMARY_RESET_TS)
    assert items["codex-a.json"].secondary_reset_at == _local_from_timestamp(SECONDARY_RESET_TS)
    assert items["codex-b.json"].email == "state-b@example.com"
    assert items["codex-b.json"].priority == -1
    assert items["codex-b.json"].disabled is True


def test_keeper_run_deletes_states_missing_from_current_auth_list() -> None:
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="codex-a.json", email="a@example.com"),
                CodexKeeperAuthState(auth_name="missing.json", email="missing@example.com"),
            ]
        )
        session.commit()
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=FakeCPAClient(_detail(priority=7)),
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.total == 1
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "codex-a.json") is not None
        assert session.get(CodexKeeperAuthState, "missing.json") is None


def test_keeper_run_deletes_all_states_when_current_auth_list_is_empty() -> None:
    class EmptyCPAClient(FakeCPAClient):
        def list_auth_files(self) -> list[dict]:
            return []

    with Session(get_engine()) as session:
        session.add(CodexKeeperAuthState(auth_name="missing.json"))
        session.commit()
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=EmptyCPAClient(_detail(priority=7)),
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.total == 0
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "missing.json") is None


def test_disabled_account_syncs_absolute_state_and_skips_usage() -> None:
    detail = _detail(priority=4)
    detail["disabled"] = True
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                disabled=False,
                priority=-1,
                restore_priority=30,
                latest_action="旧操作",
                last_error="旧错误",
                last_status_code=500,
                primary_used_percent=100,
                secondary_used_percent=100,
                primary_reset_at=_local_from_timestamp(PRIMARY_RESET_TS),
                secondary_reset_at=_local_from_timestamp(SECONDARY_RESET_TS),
                quota_threshold=90,
                last_healthy_at=datetime.now(),
            )
        )
        session.commit()
    usage_client = FakeUsageClient(_usage(20))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=FakeCPAClient(detail),
        usage_client=usage_client,
    )

    stats = service.run_once()

    assert stats.skipped == 1
    assert usage_client.calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is True
        assert state.priority == 4
        assert state.restore_priority is None
        assert state.latest_action is None
        assert state.last_error is None
        assert state.last_status_code is None
        assert state.primary_used_percent is None
        assert state.secondary_used_percent is None
        assert state.primary_reset_at is None
        assert state.secondary_reset_at is None
        assert state.quota_threshold is None
        assert state.last_healthy_at is None
        assert state.last_checked_at is not None


def test_disabled_account_preserves_keeper_disabled_latest_action() -> None:
    detail = _detail(priority=4)
    detail["disabled"] = True
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                disabled=True,
                priority=4,
                latest_action="已禁用凭证：凭证不可用：HTTP 401",
                last_error="旧错误",
                primary_used_percent=100,
                primary_reset_at=_local_from_timestamp(PRIMARY_RESET_TS),
                quota_threshold=90,
            )
        )
        session.commit()
    usage_client = FakeUsageClient(_usage(20))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=FakeCPAClient(detail),
        usage_client=usage_client,
    )

    stats = service.run_once()

    assert stats.skipped == 1
    assert usage_client.calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is True
        assert state.latest_action == "禁用凭证：凭证不可用：HTTP 401"
        assert state.last_error is None
        assert state.primary_used_percent is None
        assert state.primary_reset_at is None
        assert state.quota_threshold is None


def test_missing_token_disables_bad_credential_without_usage_check() -> None:
    detail = _detail(priority=4)
    del detail["access_token"]
    cpa = FakeCPAClient(detail)
    usage_client = FakeUsageClient(_usage(20))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=usage_client,
    )

    stats = service.run_once()

    assert stats.status_disabled == 1
    assert cpa.disabled_calls == [("codex-a.json", True)]
    assert usage_client.calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is True
        assert state.priority == 4
        assert state.latest_action == "禁用凭证：缺少 access token"


def test_quota_exhaustion_only_sets_priority_to_minus_one() -> None:
    run_id = codex_keeper_service._create_keeper_run(
        mode="run_once",
        state="running",
        detail="单轮巡检中",
        started_at=datetime.now(),
        stats=KeeperStats(),
    )
    detail = _detail(priority=7)
    detail["auth_index"] = "auth-index-1"
    cpa = FakeCPAClient(detail)
    usage_client = FakeUsageClient(_usage(100))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=usage_client,
        run_id=run_id,
    )

    stats = service.run_once()

    assert stats.priority_degraded == 1
    assert stats.status_disabled == 0
    assert usage_client.calls == [("auth-index-1", None)]
    assert cpa.priority_calls == [("codex-a.json", -1)]
    assert cpa.disabled_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == -1
        assert state.restore_priority is None
        assert state.disabled is False
        account = session.exec(
            select(CodexKeeperRunAccount).where(CodexKeeperRunAccount.run_id == run_id)
        ).one()
        assert account.result == "priority_degraded"
        assert account.latest_action == "降为低优先级：额度使用率达到阈值 100%"


def test_missing_priority_uses_type_default_when_quota_is_healthy() -> None:
    cpa = FakeCPAClient(_detail(priority=None))
    service = CodexKeeperService(
        _settings(),
        {"free": 0},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20, plan_type="free")),
    )

    stats = service.run_once()

    assert stats.healthy == 1
    assert stats.priority_restored == 1
    assert cpa.priority_calls == [("codex-a.json", 0)]
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.account_type == "free"
        assert state.priority == 0
        assert state.restore_priority is None
        assert state.latest_action == "应用类型优先级：free -> priority 0"
        assert state.primary_reset_at == _local_from_timestamp(PRIMARY_RESET_TS)
        assert state.secondary_reset_at is None


def test_missing_priority_uses_type_default_when_quota_is_exhausted() -> None:
    cpa = FakeCPAClient(_detail(priority=None))
    service = CodexKeeperService(
        _settings(),
        {"free": 0},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(100, plan_type="free")),
    )

    stats = service.run_once()

    assert stats.healthy == 1
    assert stats.priority_degraded == 1
    assert cpa.priority_calls == [("codex-a.json", -1)]
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.account_type == "free"
        assert state.priority == -1
        assert state.restore_priority is None
        assert state.latest_action == "降为低优先级：额度使用率达到阈值 100%"


def test_quota_exhaustion_temporarily_degrades_manual_high_priority() -> None:
    cpa = FakeCPAClient(_detail(priority=30))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(100)),
    )

    stats = service.run_once()

    assert stats.priority_degraded == 1
    assert cpa.priority_calls == [("codex-a.json", -1)]
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == -1
        assert state.restore_priority == 30


def test_quota_recovery_restores_manual_high_priority() -> None:
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                priority=-1,
                restore_priority=30,
            )
        )
        session.commit()
    cpa = FakeCPAClient(_detail(priority=-1))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.priority_restored == 1
    assert cpa.priority_calls == [("codex-a.json", 30)]
    assert cpa.disabled_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == 30
        assert state.restore_priority is None


def test_quota_recovery_applies_type_rule_for_system_priority() -> None:
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                priority=-1,
            )
        )
        session.commit()
    cpa = FakeCPAClient(_detail(priority=-1))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.priority_restored == 1
    assert cpa.priority_calls == [("codex-a.json", 4)]
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == 4
        assert state.restore_priority is None


def test_manual_high_priority_is_not_overwritten_by_type_rule() -> None:
    cpa = FakeCPAClient(_detail(priority=30))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.healthy == 1
    assert stats.priority_restored == 0
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == 30
        assert state.restore_priority is None


def test_manual_low_priority_is_not_overwritten_by_type_rule() -> None:
    cpa = FakeCPAClient(_detail(priority=-2))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.healthy == 1
    assert stats.priority_restored == 0
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == -2
        assert state.restore_priority is None
        assert state.latest_action == "保持低优先级：priority 小于 -1"


def test_quota_exhaustion_preserves_manual_low_priority() -> None:
    run_id = codex_keeper_service._create_keeper_run(
        mode="run_once",
        state="running",
        detail="单轮巡检中",
        started_at=datetime.now(),
        stats=KeeperStats(),
    )
    cpa = FakeCPAClient(_detail(priority=-2))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(100)),
        run_id=run_id,
    )

    stats = service.run_once()

    assert stats.healthy == 1
    assert stats.priority_degraded == 0
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == -2
        assert state.restore_priority is None
        account = session.exec(
            select(CodexKeeperRunAccount).where(CodexKeeperRunAccount.run_id == run_id)
        ).one()
        assert account.result == "healthy"
        assert account.keeper_action == "none"


def test_manual_low_priority_clears_stale_keeper_degrade_state() -> None:
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                priority=-1,
                restore_priority=30,
            )
        )
        session.commit()
    cpa = FakeCPAClient(_detail(priority=-2))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(_usage(20)),
    )

    stats = service.run_once()

    assert stats.priority_restored == 0
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.priority == -2
        assert state.restore_priority is None


def test_bad_credential_only_uses_status_disable() -> None:
    cpa = FakeCPAClient(_detail(priority=4))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(KeeperHttpResult(status_code=401, brief="unauthorized")),
    )

    stats = service.run_once()

    assert stats.status_disabled == 1
    assert cpa.disabled_calls == [("codex-a.json", True)]
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is True
        assert state.priority == 4
        assert state.restore_priority is None
        assert state.latest_action == "禁用凭证：凭证不可用：HTTP 401，unauthorized"


def test_network_error_does_not_modify_remote_auth() -> None:
    cpa = FakeCPAClient(_detail(priority=4))
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=cpa,
        usage_client=FakeUsageClient(KeeperHttpResult(status_code=None, error="timeout")),
    )

    stats = service.run_once()

    assert stats.network_error == 1
    assert cpa.disabled_calls == []
    assert cpa.priority_calls == []
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is False
        assert state.priority == 4
        assert state.last_error == "网络检测失败：timeout"


def test_process_auth_file_defers_account_state_write_until_serial_apply() -> None:
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=FakeCPAClient(_detail(priority=4)),
        usage_client=FakeUsageClient(_usage(20)),
    )

    result = service.process_auth_file({"name": "codex-a.json", "type": "codex"})

    assert result.result == "healthy"
    assert result.state_update is not None
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "codex-a.json") is None

    service._apply_state_update(result.state_update)

    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.email == "a@example.com"
        assert state.disabled is False
        assert state.priority == 4
        assert state.primary_used_percent == 20


def test_keeper_run_keeps_workers_and_applies_state_updates_on_main_thread() -> None:
    class MultiCPAClient(FakeCPAClient):
        def __init__(self) -> None:
            self.priority_calls: list[tuple[str, int]] = []
            self.disabled_calls: list[tuple[str, bool]] = []

        def list_auth_files(self) -> list[dict]:
            return [{"name": f"codex-{index}.json", "type": "codex"} for index in range(4)]

        def get_auth_file(self, name: str) -> dict:
            index = name.removeprefix("codex-").removesuffix(".json")
            detail = _detail(priority=4)
            detail["name"] = name
            detail["email"] = f"{index}@example.com"
            return detail

    class HealthyUsageClient:
        def check_usage(
            self,
            auth_index: str,
            account_id: str | None = None,
        ) -> KeeperHttpResult:
            return _usage(20)

    settings = _settings()
    settings.worker_threads = 8
    service = CodexKeeperService(
        settings,
        {"plus": 4},
        cpa_client=MultiCPAClient(),
        usage_client=HealthyUsageClient(),
    )
    main_thread = threading.current_thread().name
    apply_threads: list[str] = []
    original_apply_state_update = service._apply_state_update

    def record_apply_thread(update: codex_keeper_service.KeeperStateUpdate | None) -> None:
        apply_threads.append(threading.current_thread().name)
        original_apply_state_update(update)

    service._apply_state_update = record_apply_thread

    stats = service.run_once()

    assert settings.worker_threads == 8
    assert stats.total == 4
    assert stats.healthy == 4
    assert apply_threads == [main_thread] * 4
    with Session(get_engine()) as session:
        states = session.exec(select(CodexKeeperAuthState)).all()
        assert {state.auth_name for state in states} == {
            "codex-0.json",
            "codex-1.json",
            "codex-2.json",
            "codex-3.json",
        }


def test_keeper_run_persists_stats_and_account_snapshot() -> None:
    run_id = codex_keeper_service._create_keeper_run(
        mode="run_once",
        state="running",
        detail="单轮巡检中",
        started_at=datetime.now(),
        stats=KeeperStats(),
    )
    detail = _detail(priority=7)
    detail["auth_index"] = "auth-index-1"
    service = CodexKeeperService(
        _settings(),
        {"plus": 4},
        cpa_client=FakeCPAClient(detail),
        usage_client=FakeUsageClient(_usage(20)),
        run_id=run_id,
    )

    stats = service.run_once()
    codex_keeper_service._update_keeper_run(
        run_id,
        state="stopped",
        detail="已停止",
        stats=stats,
        finished_at=datetime.now(),
    )

    with Session(get_engine()) as session:
        run = session.get(CodexKeeperRun, run_id)
        accounts = session.exec(select(CodexKeeperRunAccount)).all()

    assert run is not None
    assert run.total == 1
    assert run.healthy == 1
    assert len(accounts) == 1
    account = accounts[0]
    assert account.run_id == run_id
    assert account.auth_name == "codex-a.json"
    assert account.email == "a@example.com"
    assert account.result == "healthy"
    assert account.primary_used_percent == 20
    assert account.quota_threshold == 100
    assert account.last_status_code == 200


def test_keeper_run_account_history_keeps_latest_50_runs() -> None:
    run_ids: list[int] = []
    for index in range(52):
        run_id = codex_keeper_service._create_keeper_run(
            mode="run_once",
            state="stopped",
            detail="已停止",
            started_at=datetime.now(),
            stats=KeeperStats(total=1, healthy=1),
        )
        run_ids.append(run_id)
        codex_keeper_service._record_keeper_run_account(
            run_id,
            KeeperAccountProcessResult(
                name=f"codex-{index}.json",
                result="healthy",
                checked_at=datetime.now(),
            ),
            quota_threshold=100,
        )

    codex_keeper_service._prune_keeper_run_account_history()

    with Session(get_engine()) as session:
        persisted_runs = session.exec(select(CodexKeeperRun)).all()
        accounts = session.exec(select(CodexKeeperRunAccount)).all()

    assert len(persisted_runs) == 52
    assert {account.run_id for account in accounts} == set(run_ids[-50:])


def test_runner_loads_latest_run_and_recent_log_files(tmp_path: Path) -> None:
    stats = KeeperStats(total=3, healthy=2, network_error=1)
    started_at = datetime.now() - timedelta(minutes=2)
    finished_at = datetime.now() - timedelta(minutes=1)
    run_id = codex_keeper_service._create_keeper_run(
        mode="run_once",
        state="running",
        detail="单轮巡检中",
        started_at=started_at,
        stats=stats,
    )
    codex_keeper_service._update_keeper_run(
        run_id,
        state="stopped",
        detail="已停止",
        stats=stats,
        finished_at=finished_at,
    )
    today = datetime.now().date()
    logs_dir = tmp_path / "logs"
    logs_dir.mkdir()
    yesterday_line = (
        f"{today - timedelta(days=1)} 10:00:00,000 - "
        "app.services.codex_keeper_service - INFO - 前一天日志"
    )
    today_line = (
        f"{today} 11:00:00,000 - "
        "app.services.codex_keeper_service - INFO - 今天日志"
    )
    (logs_dir / f"codex-keeper-{today - timedelta(days=1)}.log").write_text(
        f"{yesterday_line}\n",
        encoding="utf-8",
    )
    (logs_dir / f"codex-keeper-{today}.log").write_text(
        f"{today_line}\n",
        encoding="utf-8",
    )

    status = codex_keeper_service.CodexKeeperRunner().status()

    assert status.state == "stopped"
    assert status.stats.total == 3
    assert status.stats.healthy == 2
    assert status.stats.network_error == 1
    assert status.logs[-2:] == [yesterday_line, today_line]


def test_runner_writes_standard_keeper_log_format(tmp_path: Path) -> None:
    runner = codex_keeper_service.CodexKeeperRunner()

    runner._append_log("格式测试日志")
    status = runner.status()

    assert len(status.logs) == 1
    line = status.logs[0]
    assert re.match(
        r"^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} - "
        r"app\.services\.codex_keeper_service - INFO - 格式测试日志$",
        line,
    )
    log_files = list((tmp_path / "logs").glob("codex-keeper-*.log"))
    assert len(log_files) == 1
    assert log_files[0].read_text(encoding="utf-8").strip() == line


def test_keeper_log_rotation_keeps_three_days(tmp_path: Path) -> None:
    now = datetime.now()
    logs_dir = tmp_path / "logs"
    logs_dir.mkdir()
    paths = []
    for offset in range(4):
        log_date = now.date() - timedelta(days=offset)
        path = logs_dir / f"codex-keeper-{log_date}.log"
        path.write_text(f"[00:00:0{offset}] log\n", encoding="utf-8")
        paths.append(path)

    codex_keeper_service._cleanup_keeper_log_files(now)

    assert paths[0].exists()
    assert paths[1].exists()
    assert paths[2].exists()
    assert not paths[3].exists()


def test_keeper_service_contains_no_refresh_or_upload_paths() -> None:
    source = Path(codex_keeper_service.__file__).read_text(encoding="utf-8")

    assert "https://auth.openai.com/oauth/token" not in source
    assert "refresh_token" not in source
    assert "client.get(KEEPER_USAGE_URL)" not in source
    assert "Bearer {access_token}" not in source
    assert 'POST", "/v0/management/auth-files' not in source
