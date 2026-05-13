from datetime import datetime

from fastapi.testclient import TestClient
from sqlmodel import Session

from app.api.routes import codex_keeper as codex_keeper_route
from app.db.session import get_engine
from app.models import CodexKeeperAuthState
from app.schemas.codex_keeper import CodexKeeperAccountsResponse, CodexKeeperStatusResponse
from app.services import codex_keeper_service


def _setup_admin(client: TestClient) -> None:
    response = client.post(
        "/api/auth/setup",
        json={"username": "admin", "password": "new-password", "nickname": "管理员"},
    )
    assert response.status_code == 200


def test_codex_keeper_rejects_non_admin(client: TestClient) -> None:
    _setup_admin(client)
    created = client.post(
        "/api/users",
        json={
            "username": "normal-user",
            "password": "password",
            "is_admin": False,
            "nickname": "普通用户",
        },
    )
    assert created.status_code == 200
    logout = client.post("/api/auth/logout")
    assert logout.status_code == 200
    login = client.post(
        "/api/auth/login",
        json={"username": "normal-user", "password": "password"},
    )
    assert login.status_code == 200

    response = client.get("/api/codex-keeper/settings")
    bulk_delete = client.post(
        "/api/codex-keeper/accounts/bulk-delete",
        json={"auth_names": ["disabled.json"]},
    )

    assert response.status_code == 403
    assert bulk_delete.status_code == 403


def test_codex_keeper_settings_have_no_refresh_configuration(client: TestClient) -> None:
    _setup_admin(client)

    saved = client.put(
        "/api/codex-keeper/settings",
        json={
            "schedule_cron": "*/15 * * * *",
            "quota_threshold": 90,
            "usage_timeout_seconds": 5,
            "cpa_timeout_seconds": 6,
            "max_retries": 1,
            "worker_threads": 2,
            "dry_run": True,
            "auto_start_daemon": False,
            "priority_rules": [{"account_type": "plus", "priority": 4}],
        },
    )

    assert saved.status_code == 200
    body = saved.json()
    assert body["schedule_cron"] == "*/15 * * * *"
    assert len(body["next_run_times"]) == 5
    assert body["quota_threshold"] == 90
    assert "interval_seconds" not in body
    assert "enable_refresh" not in body
    assert "expiry_threshold_days" not in body


def test_codex_keeper_schedule_preview_returns_next_five_runs(client: TestClient) -> None:
    _setup_admin(client)

    response = client.post(
        "/api/codex-keeper/schedule/preview",
        json={"schedule_cron": "0 * * * *"},
    )

    assert response.status_code == 200
    body = response.json()
    assert body["schedule_cron"] == "0 * * * *"
    assert len(body["next_run_times"]) == 5


def test_codex_keeper_schedule_preview_rejects_invalid_cron(client: TestClient) -> None:
    _setup_admin(client)

    response = client.post(
        "/api/codex-keeper/schedule/preview",
        json={"schedule_cron": "* * *"},
    )

    assert response.status_code == 422


def test_codex_keeper_accounts_are_empty_before_first_inspection(client: TestClient) -> None:
    _setup_admin(client)

    response = client.get("/api/codex-keeper/accounts")

    assert response.status_code == 200
    assert response.json() == {"items": []}


def test_codex_keeper_accounts_reads_local_state_without_management_key(
    client: TestClient,
) -> None:
    _setup_admin(client)
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                email="a@example.com",
                account_type="plus",
                priority=4,
                latest_action="应用类型优先级：plus -> priority 4",
                primary_reset_at=datetime(2026, 5, 11, 22, 18),
                secondary_reset_at=datetime(2026, 5, 18, 18, 14),
            )
        )
        session.commit()

    response = client.get("/api/codex-keeper/accounts")

    assert response.status_code == 200
    assert response.json()["items"][0]["name"] == "codex-a.json"
    assert response.json()["items"][0]["email"] == "a@example.com"
    assert response.json()["items"][0]["priority"] == 4
    assert response.json()["items"][0]["latest_action"] == "应用类型优先级：plus -> priority 4"
    assert "reason" not in response.json()["items"][0]
    assert response.json()["items"][0]["primary_reset_at"] == "2026-05-11T22:18:00"
    assert response.json()["items"][0]["secondary_reset_at"] == "2026-05-18T18:14:00"
    assert "keeper_action" not in response.json()["items"][0]
    assert "status_disabled_by_keeper" not in response.json()["items"][0]
    assert "priority_degraded_by_keeper" not in response.json()["items"][0]
    assert "original_priority" not in response.json()["items"][0]


def test_codex_keeper_account_actions_sync_local_state(
    client: TestClient,
    monkeypatch,
) -> None:
    _setup_admin(client)
    disabled_calls: list[tuple[str, bool]] = []
    priority_calls: list[tuple[str, int]] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: codex_keeper_service.KeeperRuntimeSettings):
            self.settings = settings

        def set_auth_disabled(self, name: str, disabled: bool) -> None:
            disabled_calls.append((name, disabled))

        def set_auth_priority(self, name: str, priority: int) -> None:
            priority_calls.append((name, priority))

        def delete_auth_file(self, name: str) -> None:
            raise AssertionError("delete should not be called in this test")

    runtime_settings = codex_keeper_service.KeeperRuntimeSettings(
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
    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        lambda: (runtime_settings, {"plus": 4}),
    )
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                email="a@example.com",
                account_type="plus",
                disabled=False,
                priority=4,
                restore_priority=30,
                last_error="旧错误",
                primary_used_percent=100,
                primary_reset_at=datetime(2026, 5, 11, 22, 18),
                quota_threshold=90,
            )
        )
        session.commit()

    disabled = client.post("/api/codex-keeper/accounts/codex-a.json/disable")
    enabled = client.post("/api/codex-keeper/accounts/codex-a.json/enable")
    updated = client.patch(
        "/api/codex-keeper/accounts/codex-a.json/priority",
        json={"priority": 21},
    )

    assert disabled.status_code == 200
    assert disabled.json() == {"status": "disabled"}
    assert enabled.status_code == 200
    assert updated.status_code == 200
    assert disabled_calls == [("codex-a.json", True), ("codex-a.json", False)]
    assert priority_calls == [("codex-a.json", 21)]
    with Session(get_engine()) as session:
        state = session.get(CodexKeeperAuthState, "codex-a.json")
        assert state is not None
        assert state.disabled is False
        assert state.priority == 21
        assert state.restore_priority is None
        assert state.last_error is None
        assert state.primary_used_percent is None
        assert state.primary_reset_at is None
        assert state.quota_threshold is None


def test_codex_keeper_priority_endpoint_restricts_manual_values(
    client: TestClient,
    monkeypatch,
) -> None:
    _setup_admin(client)
    priority_calls: list[tuple[str, int]] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: codex_keeper_service.KeeperRuntimeSettings):
            self.settings = settings

        def set_auth_priority(self, name: str, priority: int) -> None:
            priority_calls.append((name, priority))

        def delete_auth_file(self, name: str) -> None:
            raise AssertionError("delete should not be called in this test")

    runtime_settings = codex_keeper_service.KeeperRuntimeSettings(
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
    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        lambda: (runtime_settings, {"plus": 4}),
    )
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add(
            CodexKeeperAuthState(
                auth_name="codex-a.json",
                account_type="plus",
                priority=4,
            )
        )
        session.commit()

    rejected_reserved = client.patch(
        "/api/codex-keeper/accounts/codex-a.json/priority",
        json={"priority": 10},
    )
    rejected_minus_one = client.patch(
        "/api/codex-keeper/accounts/codex-a.json/priority",
        json={"priority": -1},
    )
    allowed_default = client.patch(
        "/api/codex-keeper/accounts/codex-a.json/priority",
        json={"priority": 4},
    )
    allowed_low = client.patch(
        "/api/codex-keeper/accounts/codex-a.json/priority",
        json={"priority": -2},
    )

    assert rejected_reserved.status_code == 422
    assert rejected_minus_one.status_code == 422
    assert allowed_default.status_code == 200
    assert allowed_low.status_code == 200
    assert priority_calls == [("codex-a.json", 4), ("codex-a.json", -2)]


def test_codex_keeper_delete_requires_disabled_and_removes_state(
    client: TestClient,
    monkeypatch,
) -> None:
    _setup_admin(client)
    delete_calls: list[str] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: codex_keeper_service.KeeperRuntimeSettings):
            self.settings = settings

        def delete_auth_file(self, name: str) -> None:
            delete_calls.append(name)

    runtime_settings = codex_keeper_service.KeeperRuntimeSettings(
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
    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        lambda: (runtime_settings, {"plus": 4}),
    )
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="enabled.json", disabled=False),
                CodexKeeperAuthState(auth_name="disabled.json", disabled=True),
            ]
        )
        session.commit()

    rejected = client.delete("/api/codex-keeper/accounts/enabled.json")
    deleted = client.delete("/api/codex-keeper/accounts/disabled.json")

    assert rejected.status_code == 422
    assert deleted.status_code == 200
    assert deleted.json() == {"status": "deleted"}
    assert delete_calls == ["disabled.json"]
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "enabled.json") is not None
        assert session.get(CodexKeeperAuthState, "disabled.json") is None


def test_codex_keeper_bulk_delete_rejects_empty_list(client: TestClient) -> None:
    _setup_admin(client)

    response = client.post(
        "/api/codex-keeper/accounts/bulk-delete",
        json={"auth_names": []},
    )

    assert response.status_code == 422


def test_codex_keeper_bulk_delete_deletes_disabled_accounts(
    client: TestClient,
    monkeypatch,
) -> None:
    _setup_admin(client)
    delete_calls: list[str] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: codex_keeper_service.KeeperRuntimeSettings):
            self.settings = settings

        def delete_auth_file(self, name: str) -> None:
            delete_calls.append(name)

    runtime_settings = codex_keeper_service.KeeperRuntimeSettings(
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
    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        lambda: (runtime_settings, {"plus": 4}),
    )
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="disabled-a.json", disabled=True),
                CodexKeeperAuthState(auth_name="disabled-b.json", disabled=True),
            ]
        )
        session.commit()

    response = client.post(
        "/api/codex-keeper/accounts/bulk-delete",
        json={"auth_names": ["disabled-a.json", " disabled-b.json ", "disabled-a.json"]},
    )

    assert response.status_code == 200
    assert response.json() == {
        "status": "completed",
        "deleted": ["disabled-a.json", "disabled-b.json"],
        "failed": [],
    }
    assert delete_calls == ["disabled-a.json", "disabled-b.json"]
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "disabled-a.json") is None
        assert session.get(CodexKeeperAuthState, "disabled-b.json") is None


def test_codex_keeper_bulk_delete_reports_partial_failures(
    client: TestClient,
    monkeypatch,
) -> None:
    _setup_admin(client)
    delete_calls: list[str] = []

    class FakeKeeperCPAClient:
        def __init__(self, settings: codex_keeper_service.KeeperRuntimeSettings):
            self.settings = settings

        def delete_auth_file(self, name: str) -> None:
            delete_calls.append(name)

    runtime_settings = codex_keeper_service.KeeperRuntimeSettings(
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
    monkeypatch.setattr(
        codex_keeper_service,
        "build_runtime_settings",
        lambda: (runtime_settings, {"plus": 4}),
    )
    monkeypatch.setattr(codex_keeper_service, "KeeperCPAClient", FakeKeeperCPAClient)
    with Session(get_engine()) as session:
        session.add_all(
            [
                CodexKeeperAuthState(auth_name="enabled.json", disabled=False),
                CodexKeeperAuthState(auth_name="disabled.json", disabled=True),
            ]
        )
        session.commit()

    response = client.post(
        "/api/codex-keeper/accounts/bulk-delete",
        json={"auth_names": ["enabled.json", "disabled.json", "missing.json"]},
    )

    assert response.status_code == 200
    assert response.json() == {
        "status": "completed",
        "deleted": ["disabled.json"],
        "failed": [
            {"name": "enabled.json", "message": "只能删除已禁用账号"},
            {"name": "missing.json", "message": "账号状态不存在"},
        ],
    }
    assert delete_calls == ["disabled.json"]
    with Session(get_engine()) as session:
        assert session.get(CodexKeeperAuthState, "enabled.json") is not None
        assert session.get(CodexKeeperAuthState, "disabled.json") is None


def test_codex_keeper_status_and_accounts_shapes(client: TestClient, monkeypatch) -> None:
    _setup_admin(client)
    monkeypatch.setattr(
        codex_keeper_route.codex_keeper_runner,
        "status",
        lambda: CodexKeeperStatusResponse(
            running=False,
            state="stopped",
            detail="未运行",
            mode=None,
            last_started_at=None,
            last_finished_at=None,
            stats={},
            logs=[],
        ),
    )
    monkeypatch.setattr(
        codex_keeper_route,
        "list_keeper_accounts",
        lambda: CodexKeeperAccountsResponse(items=[]),
    )

    status = client.get("/api/codex-keeper/status")
    accounts = client.get("/api/codex-keeper/accounts")

    assert status.status_code == 200
    assert status.json()["state"] == "stopped"
    assert accounts.status_code == 200
    assert accounts.json() == {"items": []}
