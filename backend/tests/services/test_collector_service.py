import asyncio
from types import SimpleNamespace

import httpx

from app.core.config import CollectorConfig
from app.services import collector_service


def test_sync_remote_usage_enabled_uses_management_endpoint(monkeypatch) -> None:
    calls: list[tuple[str, str, dict | None, dict]] = []

    class StubAsyncClient:
        def __init__(self, *, base_url: str, timeout: float) -> None:
            self.base_url = base_url
            self.timeout = timeout

        async def __aenter__(self) -> "StubAsyncClient":
            return self

        async def __aexit__(self, exc_type: object, exc: object, traceback: object) -> None:
            return None

        async def get(self, path: str, *, headers: dict) -> httpx.Response:
            calls.append(("GET", path, None, headers))
            return httpx.Response(200, json={"usage-statistics-enabled": False})

        async def put(self, path: str, *, json: dict, headers: dict) -> httpx.Response:
            calls.append(("PUT", path, json, headers))
            return httpx.Response(200, json={"status": "ok"})

    monkeypatch.setattr(collector_service.httpx, "AsyncClient", StubAsyncClient)

    enabled, error = asyncio.run(
        collector_service._sync_remote_usage_enabled(
            CollectorConfig(
                cliaproxy_url="http://127.0.0.1:8317",
                management_key="management-secret",
            )
        )
    )

    assert enabled is True
    assert error is None
    assert calls == [
        (
            "GET",
            "/v0/management/usage-statistics-enabled",
            None,
            {
                "Authorization": "Bearer management-secret",
                "X-Management-Key": "management-secret",
            },
        ),
        (
            "PUT",
            "/v0/management/usage-statistics-enabled",
            {"value": True},
            {
                "Authorization": "Bearer management-secret",
                "X-Management-Key": "management-secret",
            },
        ),
    ]


def test_sync_remote_usage_enabled_does_not_write_when_already_enabled(monkeypatch) -> None:
    calls: list[tuple[str, str]] = []

    class StubAsyncClient:
        def __init__(self, *, base_url: str, timeout: float) -> None:
            self.base_url = base_url
            self.timeout = timeout

        async def __aenter__(self) -> "StubAsyncClient":
            return self

        async def __aexit__(self, exc_type: object, exc: object, traceback: object) -> None:
            return None

        async def get(self, path: str, *, headers: dict) -> httpx.Response:
            calls.append(("GET", path))
            return httpx.Response(200, json={"usage-statistics-enabled": True})

        async def put(self, path: str, *, json: dict, headers: dict) -> httpx.Response:
            calls.append(("PUT", path))
            return httpx.Response(200, json={"status": "ok"})

    monkeypatch.setattr(collector_service.httpx, "AsyncClient", StubAsyncClient)

    enabled, error = asyncio.run(
        collector_service._sync_remote_usage_enabled(
            CollectorConfig(
                cliaproxy_url="http://127.0.0.1:8317",
                management_key="management-secret",
            )
        )
    )

    assert enabled is True
    assert error is None
    assert calls == [("GET", "/v0/management/usage-statistics-enabled")]


def test_sync_remote_usage_enabled_reports_disabled_when_enable_request_fails(
    monkeypatch,
) -> None:
    class StubAsyncClient:
        def __init__(self, *, base_url: str, timeout: float) -> None:
            self.base_url = base_url
            self.timeout = timeout

        async def __aenter__(self) -> "StubAsyncClient":
            return self

        async def __aexit__(self, exc_type: object, exc: object, traceback: object) -> None:
            return None

        async def get(self, path: str, *, headers: dict) -> httpx.Response:
            return httpx.Response(200, json={"usage-statistics-enabled": False})

        async def put(self, path: str, *, json: dict, headers: dict) -> httpx.Response:
            return httpx.Response(401, json={"error": "invalid management key"})

    monkeypatch.setattr(collector_service.httpx, "AsyncClient", StubAsyncClient)

    enabled, error = asyncio.run(
        collector_service._sync_remote_usage_enabled(
            CollectorConfig(
                cliaproxy_url="http://127.0.0.1:8317",
                management_key="bad-secret",
            )
        )
    )

    assert enabled is False
    assert error == "远端 usage 开关开启失败：HTTP 401"


def test_runner_syncs_remote_switch_even_when_local_collection_is_disabled(monkeypatch) -> None:
    runner = collector_service.CollectorRunner()
    synced: list[CollectorConfig] = []

    async def fake_sync_remote_if_needed(
        self: collector_service.CollectorRunner,
        config: CollectorConfig,
    ) -> None:
        synced.append(config)

    async def fake_sleep_or_stop(
        self: collector_service.CollectorRunner,
        delay: float,
    ) -> None:
        self._stop_event.set()

    monkeypatch.setattr(
        collector_service,
        "load_config",
        lambda: SimpleNamespace(
            collector=CollectorConfig(enabled=False, management_key="management-secret")
        ),
    )
    monkeypatch.setattr(collector_service, "update_state", lambda **kwargs: None)
    monkeypatch.setattr(
        collector_service.CollectorRunner,
        "_sync_remote_if_needed",
        fake_sync_remote_if_needed,
    )
    monkeypatch.setattr(
        collector_service.CollectorRunner,
        "_sleep_or_stop",
        fake_sleep_or_stop,
    )

    asyncio.run(runner._run())

    assert [config.management_key for config in synced] == ["management-secret"]
