import json
from datetime import datetime, timedelta

from fastapi.testclient import TestClient
from sqlmodel import Session, select

from app.core.security import hash_api_key, hash_password
from app.db.session import get_engine, reset_engine_for_tests
from app.main import app
from app.models import AppSetting, UsageRecord, User, UserApiKey
from app.schemas.pricing import ModelPriceCreate
from app.services import cpa_management_service
from app.services.pricing_service import create_price
from app.services.usage_service import save_usage_message


def _create_first_admin(
    client: TestClient,
    *,
    username: str = "admin",
    password: str = "new-password",
    nickname: str = "管理员",
) -> None:
    state = client.get("/api/auth/setup")
    assert state.status_code == 200
    if state.json()["setup_required"]:
        created = client.post(
            "/api/auth/setup",
            json={"username": username, "password": password, "nickname": nickname},
        )
        assert created.status_code == 200
        assert created.json()["username"] == username
        assert created.json()["is_admin"] is True
        assert created.json()["must_change_password"] is False
        return
    login = client.post("/api/auth/login", json={"username": username, "password": password})
    assert login.status_code == 200


def _login_and_change_default_password(client: TestClient) -> None:
    _create_first_admin(client)


def test_first_login_requires_creating_first_admin(client: TestClient) -> None:
    setup = client.get("/api/auth/setup")
    assert setup.status_code == 200
    assert setup.json()["setup_required"] is True

    login = client.post("/api/auth/login", json={"username": "admin", "password": "password"})
    assert login.status_code == 409

    blocked = client.get("/api/settings")
    assert blocked.status_code == 401

    _create_first_admin(client, username="primary-admin", password="current-admin-password")

    setup_after = client.get("/api/auth/setup")
    assert setup_after.status_code == 200
    assert setup_after.json()["setup_required"] is False

    settings = client.get("/api/settings")
    assert settings.status_code == 200
    assert settings.json()["management_key"] == ""
    assert settings.json()["management_key_set"] is False


def test_first_admin_cannot_be_demoted_and_multiple_admins_are_allowed(
    client: TestClient,
) -> None:
    _create_first_admin(client, username="primary-admin", password="current-admin-password")

    users = client.get("/api/users")
    assert users.status_code == 200
    current_users = [item for item in users.json() if item["username"] == "primary-admin"]
    assert len(current_users) == 1
    current_user = current_users[0]
    assert current_user["is_admin"] is True
    assert current_user["password_set"] is True
    assert current_user["nickname"] == "管理员"

    rejected_rename = client.put(
        f"/api/users/{current_user['id']}",
        json={
            "username": "renamed-primary-admin",
            "is_admin": True,
            "nickname": "管理员",
        },
    )
    assert rejected_rename.status_code == 409

    updated_first = client.put(
        f"/api/users/{current_user['id']}",
        json={
            "username": "primary-admin",
            "is_admin": True,
            "nickname": "管理员昵称",
        },
    )
    assert updated_first.status_code == 200
    assert updated_first.json()["username"] == "primary-admin"
    assert updated_first.json()["nickname"] == "管理员昵称"

    me_after_update = client.get("/api/auth/me")
    assert me_after_update.status_code == 200
    assert me_after_update.json()["id"] == current_user["id"]
    assert me_after_update.json()["username"] == "primary-admin"

    rejected_delete = client.delete(f"/api/users/{current_user['id']}")
    assert rejected_delete.status_code == 409

    demoted = client.put(
        f"/api/users/{current_user['id']}",
        json={
            "username": "primary-admin",
            "is_admin": False,
            "nickname": "管理员",
        },
    )
    assert demoted.status_code == 409

    second_admin = client.post(
        "/api/users",
        json={
            "username": "second-admin",
            "password": "password",
            "is_admin": True,
            "nickname": "管理员",
        },
    )
    assert second_admin.status_code == 200
    assert second_admin.json()["is_admin"] is True

    updated_second_admin = client.put(
        f"/api/users/{second_admin.json()['id']}",
        json={
            "username": "second-admin",
            "is_admin": False,
            "nickname": "管理员",
        },
    )
    assert updated_second_admin.status_code == 200
    assert updated_second_admin.json()["is_admin"] is False


def test_change_credentials_can_update_password_and_keep_session_valid(
    client: TestClient,
) -> None:
    _create_first_admin(client, username="primary-admin", password="current-admin-password")

    changed = client.post(
        "/api/auth/change-credentials",
        json={
            "password": "new-admin-password",
            "current_password": "current-admin-password",
        },
    )
    assert changed.status_code == 200
    assert changed.json()["id"] == 1
    assert changed.json()["username"] == "primary-admin"
    assert changed.json()["is_admin"] is True

    me = client.get("/api/auth/me")
    assert me.status_code == 200
    assert me.json()["id"] == 1
    assert me.json()["username"] == "primary-admin"
    assert me.json()["is_admin"] is True

    relogin = client.post(
        "/api/auth/login",
        json={"username": "primary-admin", "password": "new-admin-password"},
    )
    assert relogin.status_code == 200
    assert relogin.json()["is_admin"] is True


def test_regular_user_is_scoped_away_from_admin_apis_and_other_usage(
    client: TestClient,
) -> None:
    _create_first_admin(
        client,
        username="admin",
        password="current-admin-password",
        nickname="管理员昵称",
    )
    admin_key = "sk-admin-user"
    admin_bound = client.post(
        "/api/users/1/api-keys",
        json={"api_key": admin_key, "description": "Admin"},
    )
    assert admin_bound.status_code == 200
    normal_user = client.post(
        "/api/users",
        json={
            "username": "normal-user",
            "password": "normal-password",
            "is_admin": False,
            "nickname": "普通用户昵称",
        },
    )
    assert normal_user.status_code == 200
    normal_user_id = normal_user.json()["id"]
    other_user = client.post(
        "/api/users",
        json={
            "username": "other-user",
            "password": "other-password",
            "is_admin": False,
            "nickname": "其他用户昵称",
        },
    )
    assert other_user.status_code == 200
    other_user_id = other_user.json()["id"]

    normal_key = "sk-normal-user"
    other_key = "sk-other-user"
    normal_bound = client.post(
        f"/api/users/{normal_user_id}/api-keys",
        json={"api_key": normal_key, "description": "VSCode"},
    )
    assert normal_bound.status_code == 200
    other_bound = client.post(
        f"/api/users/{other_user_id}/api-keys",
        json={"api_key": other_key, "description": "Cursor"},
    )
    assert other_bound.status_code == 200

    now = datetime.now()
    with Session(get_engine()) as session:
        admin_record, created = save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": admin_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 5,
            },
        )
        assert created is True
        admin_record_id = admin_record.id
        own_record, created = save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": normal_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 10,
            },
        )
        assert created is True
        own_record_id = own_record.id
        other_record, created = save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": other_key,
                "provider": "anthropic",
                "model": "claude-sonnet",
                "input_tokens": 20,
            },
        )
        assert created is True
        other_record_id = other_record.id

    admin_account_params = {
        "scope": "account",
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    admin_account_overview = client.get("/api/usage/overview", params=admin_account_params)
    assert admin_account_overview.status_code == 200
    admin_account_body = admin_account_overview.json()
    assert admin_account_body["summary"]["total_records"] == 1
    assert admin_account_body["user_ranking"]["items"] == []
    assert admin_account_body["options"]["users"] == []
    assert "管理员昵称" not in json.dumps(admin_account_body, ensure_ascii=False)

    admin_account_detail = client.get(
        f"/api/usage/records/{admin_record_id}",
        params={"scope": "account"},
    )
    assert admin_account_detail.status_code == 200
    assert admin_account_detail.json()["user_label"] == "admin"

    transferred_key = client.post(
        f"/api/users/{normal_user_id}/api-keys",
        json={"api_key": other_key, "description": "转绑密钥"},
    )
    assert transferred_key.status_code == 200

    logout = client.post("/api/auth/logout")
    assert logout.status_code == 200
    login = client.post(
        "/api/auth/login",
        json={"username": "normal-user", "password": "normal-password"},
    )
    assert login.status_code == 200
    assert login.json()["is_admin"] is False
    me = client.get("/api/auth/me")
    assert me.status_code == 200
    assert me.json()["username"] == "normal-user"
    assert me.json()["is_admin"] is False

    self_keys = client.get("/api/api-keys")
    assert self_keys.status_code == 200
    self_keys_body = self_keys.json()
    assert {item["api_key_hash"] for item in self_keys_body} == {
        hash_api_key(normal_key),
        hash_api_key(other_key),
    }
    assert {item["user_name"] for item in self_keys_body} == {"normal-user"}
    assert "普通用户昵称" not in json.dumps(self_keys_body, ensure_ascii=False)
    assert "其他用户昵称" not in json.dumps(self_keys_body, ensure_ascii=False)

    for path in ("/api/users", "/api/settings", "/api/collector/status", "/api/model-prices"):
        blocked = client.get(path)
        assert blocked.status_code == 403

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
        "user_id": other_user_id,
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["total_records"] == 1

    overview = client.get("/api/usage/overview", params=params)
    assert overview.status_code == 200
    overview_body = overview.json()
    assert overview_body["summary"]["total_records"] == 1
    assert overview_body["user_ranking"]["items"] == []
    assert overview_body["options"]["users"] == []
    assert "普通用户昵称" not in json.dumps(overview_body, ensure_ascii=False)
    assert "其他用户昵称" not in json.dumps(overview_body, ensure_ascii=False)

    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert records.json()["total"] == 1
    assert records.json()["items"][0]["id"] == own_record_id
    assert records.json()["items"][0]["user_label"] == "normal-user"

    own_detail = client.get(f"/api/usage/records/{own_record_id}")
    assert own_detail.status_code == 200
    assert own_detail.json()["user_label"] == "normal-user"

    other_detail = client.get(f"/api/usage/records/{other_record_id}")
    assert other_detail.status_code == 404


def test_settings_returns_full_management_key_for_admin(client: TestClient) -> None:
    _login_and_change_default_password(client)

    saved = client.put("/api/settings", json={"management_key": "  management-secret  "})
    assert saved.status_code == 200
    assert saved.json()["management_key"] == "management-secret"
    assert saved.json()["management_key_set"] is True

    loaded = client.get("/api/settings")
    assert loaded.status_code == 200
    assert loaded.json()["management_key"] == "management-secret"

    cleared = client.put("/api/settings", json={"management_key": "   "})
    assert cleared.status_code == 200
    assert cleared.json()["management_key"] == ""
    assert cleared.json()["management_key_set"] is False


def test_settings_are_stored_in_sqlite_without_json_config(
    client: TestClient,
    tmp_path,
) -> None:
    _login_and_change_default_password(client)

    saved = client.put(
        "/api/settings",
        json={
            "management_key": "management-secret",
            "collector_enabled": True,
            "queue_name": "usage",
        },
    )
    assert saved.status_code == 200
    assert not (tmp_path / "config").exists()

    with Session(get_engine()) as session:
        setting = session.get(AppSetting, 1)
        assert setting is not None
        assert setting.management_key == "management-secret"
        assert setting.collector_enabled is True
        assert setting.queue_name == "usage"


def test_legacy_json_config_is_migrated_to_sqlite(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("CPA_HELPER_DATA_DIR", str(tmp_path))
    reset_engine_for_tests()
    salt = "legacy-salt"
    config_dir = tmp_path / "config"
    config_dir.mkdir()
    (config_dir / "settings.json").write_text(
        json.dumps(
            {
                "account": {
                    "username": "legacy-admin",
                    "password_hash": hash_password("legacy-password", salt),
                    "password_salt": salt,
                    "must_change_password": False,
                },
                "collector": {
                    "enabled": True,
                    "cliaproxy_url": "http://127.0.0.1:9999",
                    "management_key": "legacy-management-key",
                    "queue_name": "legacy-usage",
                    "batch_size": 25,
                    "poll_interval_seconds": 3,
                    "retry_interval_seconds": 7,
                },
                "theme_preference": "dark",
                "session_secret": "legacy-session-secret",
            }
        ),
        encoding="utf-8",
    )

    try:
        with TestClient(app) as legacy_client:
            login = legacy_client.post(
                "/api/auth/login",
                json={"username": "legacy-admin", "password": "legacy-password"},
            )
            assert login.status_code == 200
            assert login.json()["must_change_password"] is False

            settings = legacy_client.get("/api/settings")
            assert settings.status_code == 200
            assert settings.json()["management_key"] == "legacy-management-key"
            assert settings.json()["queue_name"] == "legacy-usage"
            assert settings.json()["theme_preference"] == "dark"

            with Session(get_engine()) as session:
                setting = session.get(AppSetting, 1)
                assert setting is not None
                assert setting.management_key == "legacy-management-key"
                user = session.exec(select(User).where(User.username == "legacy-admin")).one()
                assert user.is_admin is True
                assert user.password_hash == hash_password("legacy-password", salt)
    finally:
        reset_engine_for_tests()


def test_trimmed_blank_inputs_are_rejected(client: TestClient) -> None:
    _login_and_change_default_password(client)

    blank_password = client.post(
        "/api/auth/change-credentials",
        json={"password": "   ", "current_password": "new-password"},
    )
    assert blank_password.status_code == 422

    blank_queue = client.put("/api/settings", json={"queue_name": "   "})
    assert blank_queue.status_code == 422

    blank_price = client.post(
        "/api/model-prices",
        json={
            "provider": "   ",
            "model": "gpt-4.1-mini",
            "input_usd_per_million": 1,
            "output_usd_per_million": 1,
            "cached_usd_per_million": 0,
            "reasoning_usd_per_million": 0,
        },
    )
    assert blank_price.status_code == 422

    user = client.post(
        "/api/users",
        json={
            "username": "test-user",
            "password": "password",
            "is_admin": False,
            "nickname": "测试用户",
        },
    )
    assert user.status_code == 200
    updated_user = client.put(
        f"/api/users/{user.json()['id']}",
        json={
            "username": "test-user",
            "is_admin": False,
            "nickname": "测试用户",
        },
    )
    assert updated_user.status_code == 200
    assert updated_user.json()["username"] == "test-user"
    duplicate_user = client.post(
        "/api/users",
        json={
            "username": "duplicate-user",
            "password": "password",
            "is_admin": False,
            "nickname": "重复用户",
        },
    )
    assert duplicate_user.status_code == 200
    users = client.get("/api/users")
    assert users.status_code == 200
    user_ids = [item["id"] for item in users.json()]
    assert user_ids == sorted(user_ids)
    blank_nickname_user = client.post(
        "/api/users",
        json={
            "username": "blank-nickname-user",
            "password": "password",
            "is_admin": False,
            "nickname": "   ",
        },
    )
    assert blank_nickname_user.status_code == 422
    blank_password_user = client.post(
        "/api/users",
        json={
            "username": "blank-password-user",
            "password": "   ",
            "is_admin": False,
            "nickname": "空密码用户",
        },
    )
    assert blank_password_user.status_code == 422
    rejected_account_rename = client.put(
        f"/api/users/{duplicate_user.json()['id']}",
        json={
            "username": "test-user",
            "is_admin": False,
            "nickname": "重复用户",
        },
    )
    assert rejected_account_rename.status_code == 409
    blank_api_key_bind = client.post(
        f"/api/users/{user.json()['id']}/api-keys",
        json={"api_key": "   ", "description": "VSCode"},
    )
    assert blank_api_key_bind.status_code == 422


def test_usage_summary_records_and_detail_are_protected_and_redacted(client: TestClient) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-4.1-mini",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0.25,
                reasoning_usd_per_million=3,
            ),
        )
        record, created = save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-test-secret-value",
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "endpoint": "/v1/chat/completions",
                "request_id": "req_456",
                "auth_index": "8f70719a121606f2",
                "auth_type": "oauth",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
                "cached_tokens": 100_000,
                "reasoning_tokens": 10_000,
            },
        )
        assert created is True
        record_id = record.id

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["total_records"] == 1
    assert summary.json()["estimated_cost_usd"] == 1.935

    overview = client.get("/api/usage/overview", params=params)
    assert overview.status_code == 200
    overview_body = overview.json()
    assert overview_body["summary"]["total_records"] == 1
    assert overview_body["summary"]["estimated_cost_usd"] == 1.935
    assert overview_body["trends"][0]["records"] == 1
    assert overview_body["user_ranking"]["group_by"] == "user"
    assert overview_body["user_ranking"]["items"] == []
    assert overview_body["api_key_description_ranking"]["group_by"] == "api_key_description"
    assert overview_body["api_key_description_ranking"]["items"][0]["records"] == 1
    assert overview_body["api_key_ranking"]["group_by"] == "api_key_description"
    assert overview_body["api_key_ranking"]["items"][0]["records"] == 1
    assert overview_body["model_ranking"]["group_by"] == "model"
    assert overview_body["model_ranking"]["items"][0]["label"] == "openai / gpt-4.1-mini"
    assert overview_body["distributions"]["providers"][0]["label"] == "openai"
    assert "openai" in overview_body["options"]["providers"]

    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert "api_key" not in records.json()["items"][0]
    assert "api_key_hash" not in records.json()["items"][0]
    assert records.json()["items"][0]["user_label"] == "未绑定"
    assert records.json()["items"][0]["user_id"] is None
    assert records.json()["items"][0]["auth_index"] == "8f70719a121606f2"
    assert records.json()["items"][0]["auth"] == "oauth"
    assert records.json()["start"] == params["start"]
    assert records.json()["end"] == params["end"]

    detail = client.get(f"/api/usage/records/{record_id}")
    assert detail.status_code == 200
    assert detail.json()["user_label"] == "未绑定"
    assert detail.json()["raw_json"]["api_key"] == "sk-tes...alue"


def test_usage_cost_does_not_double_count_cached_or_reasoning_token_details(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-5.5",
                input_usd_per_million=5,
                output_usd_per_million=30,
                cached_usd_per_million=0.5,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-cost-regression",
                "provider": "openai",
                "model": "gpt-5.5",
                "input_tokens": 64_436,
                "output_tokens": 566,
                "cached_tokens": 60_288,
                "reasoning_tokens": 414,
                "total_tokens": 65_002,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 0.067864

    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert records.json()["items"][0]["estimated_cost_usd"] == 0.067864


def test_usage_cost_matches_query_side_provider_aliases(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-5.5",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        create_price(
            session,
            ModelPriceCreate(
                provider="anthropic",
                model="claude-sonnet",
                input_usd_per_million=3,
                output_usd_per_million=4,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-provider-alias",
                "provider": "codex",
                "model": "gpt-5.5",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-claude-provider-alias",
                "provider": "claude",
                "model": "claude-sonnet",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 7
    assert summary.json()["unpriced_records"] == 0

    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert {item["estimated_cost_usd"] for item in records.json()["items"]} == {2, 5}
    assert all(item["unpriced"] is False for item in records.json()["items"])


def test_usage_cost_does_not_apply_provider_aliases_in_reverse(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="codex",
                model="gpt-5.5",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        create_price(
            session,
            ModelPriceCreate(
                provider="claude",
                model="claude-sonnet",
                input_usd_per_million=3,
                output_usd_per_million=4,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-openai-reverse-alias",
                "provider": "openai",
                "model": "gpt-5.5",
                "input_tokens": 1_000_000,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-anthropic-reverse-alias",
                "provider": "anthropic",
                "model": "claude-sonnet",
                "input_tokens": 1_000_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 0
    assert summary.json()["unpriced_records"] == 2


def test_usage_cost_does_not_match_provider_prefixed_or_digit_version_alias(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="anthropic",
                model="claude-opus-4-6",
                input_usd_per_million=15,
                output_usd_per_million=75,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-provider-prefixed-model",
                "provider": "TokenRouter",
                "model": "anthropic/claude-opus-4.6",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-digit-version-model",
                "provider": "anthropic",
                "model": "claude-opus-4.6",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 0
    assert summary.json()["unpriced_records"] == 2

    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert {item["estimated_cost_usd"] for item in records.json()["items"]} == {0}
    assert all(item["unpriced"] is True for item in records.json()["items"])


def test_usage_cost_matches_exact_provider_model_only(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="anthropic",
                model="claude-opus-4-6",
                input_usd_per_million=15,
                output_usd_per_million=75,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        create_price(
            session,
            ModelPriceCreate(
                provider="TokenRouter",
                model="anthropic/claude-opus-4.6",
                input_usd_per_million=2,
                output_usd_per_million=4,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-exact-provider-model-price",
                "provider": "TokenRouter",
                "model": "anthropic/claude-opus-4.6",
                "input_tokens": 1_000_000,
                "output_tokens": 500_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 4
    assert summary.json()["unpriced_records"] == 0


def test_usage_cost_does_not_match_same_model_for_unrelated_provider(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="azure",
                model="gpt-5.5",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-ambiguous-provider",
                "provider": "openai",
                "model": "gpt-5.5",
                "input_tokens": 1_000_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 0
    assert summary.json()["unpriced_records"] == 1


def test_usage_cost_matches_query_provider_alias_with_other_same_model_prices(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    with Session(get_engine()) as session:
        for provider, input_price in (
            ("openai", 1),
            ("azure", 99),
        ):
            create_price(
                session,
                ModelPriceCreate(
                    provider=provider,
                    model="gpt-5.5",
                    input_usd_per_million=input_price,
                    output_usd_per_million=0,
                    cached_usd_per_million=0,
                    reasoning_usd_per_million=0,
                ),
            )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": "sk-provider-alias-with-other-model-price",
                "provider": "codex",
                "model": "gpt-5.5",
                "input_tokens": 1_000_000,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    summary = client.get("/api/usage/summary", params=params)
    assert summary.status_code == 200
    assert summary.json()["estimated_cost_usd"] == 1
    assert summary.json()["unpriced_records"] == 0


def test_observed_api_keys_return_bound_keys_without_usage_key_backfill(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    visible_key = "sk-visible-secret-value"
    mismatched_key = "sk-hidden-secret-value"
    manual_hash = hash_api_key("manual-only")

    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-4.1-mini",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0.25,
                reasoning_usd_per_million=3,
            ),
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": visible_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 10,
                "output_tokens": 5,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": (now - timedelta(days=1)).isoformat(),
                "api_key": visible_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 30,
                "failed": True,
            },
        )
        mismatched_record, created = save_usage_message(
            session,
            {
                "timestamp": (now - timedelta(minutes=1)).isoformat(),
                "api_key": mismatched_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 20,
            },
        )
        assert created is True
        mismatched_record.raw_json = '{"api_key":"sk-not-the-same"}'
        session.add(mismatched_record)
        session.commit()

    user = client.post(
        "/api/users",
        json={
            "username": "dev-user",
            "password": "password",
            "is_admin": False,
            "nickname": "研发用户",
        },
    )
    assert user.status_code == 200
    user_id = user.json()["id"]
    visible_hash = hash_api_key(visible_key)
    bound = client.post(
        f"/api/users/{user_id}/api-keys",
        json={"api_key": visible_key, "description": "VSCode"},
    )
    assert bound.status_code == 200
    assert bound.json()["user_name"] == "研发用户"

    mismatched_bound = client.post(
        f"/api/users/{user_id}/api-keys",
        json={
            "api_key": "another-key",
            "api_key_hash": visible_hash,
            "description": "错误绑定",
        },
    )
    assert mismatched_bound.status_code == 409

    manual_user = client.post(
        "/api/users",
        json={
            "username": "manual-user",
            "password": "password",
            "is_admin": False,
            "nickname": "手动用户",
        },
    )
    assert manual_user.status_code == 200
    manual_bound = client.post(
        f"/api/users/{manual_user.json()['id']}/api-keys",
        json={"api_key": "manual-only", "description": "手动导入"},
    )
    assert manual_bound.status_code == 200

    observed = client.get("/api/users/observed-api-keys")
    assert observed.status_code == 200
    items = {item["api_key_hash"]: item for item in observed.json()}

    assert items[visible_hash]["api_key"] == visible_key
    assert items[visible_hash]["user_id"] == user_id
    assert items[visible_hash]["user_name"] == "研发用户"
    assert items[visible_hash]["records"] == 0
    assert items[visible_hash]["success_records"] == 0
    assert items[visible_hash]["failed_records"] == 0
    assert items[visible_hash]["total_tokens"] == 0
    assert items[visible_hash]["today_records"] == 0
    assert items[visible_hash]["today_success_records"] == 0
    assert items[visible_hash]["today_failed_records"] == 0
    assert items[visible_hash]["today_input_tokens"] == 0
    assert items[visible_hash]["today_output_tokens"] == 0
    assert items[visible_hash]["today_total_tokens"] == 0
    assert items[visible_hash]["today_estimated_cost_usd"] == 0
    assert items[visible_hash]["today_unpriced_records"] == 0
    assert items[visible_hash]["first_seen_at"] is None
    assert items[visible_hash]["last_seen_at"] is None
    assert items[visible_hash]["last_provider"] is None
    assert items[visible_hash]["last_model"] is None
    assert items[visible_hash]["providers"] == []
    assert items[visible_hash]["models"] == []

    mismatched_hash = hash_api_key(mismatched_key)
    assert mismatched_hash not in items

    assert items[manual_hash]["api_key"] == "manual-only"
    assert items[manual_hash]["user_name"] == "手动用户"
    assert items[manual_hash]["records"] == 0
    assert items[manual_hash]["total_tokens"] == 0
    assert items[manual_hash]["today_records"] == 0
    assert items[manual_hash]["today_total_tokens"] == 0
    assert items[manual_hash]["today_estimated_cost_usd"] == 0
    assert items[manual_hash]["providers"] == []
    assert items[manual_hash]["models"] == []
    assert items[manual_hash]["last_seen_at"] is None

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    records = client.get("/api/usage/records", params=params)
    assert records.status_code == 200
    assert records.json()["total"] == 2
    assert {item["user_label"] for item in records.json()["items"]} == {"未绑定"}
    assert {item["api_key_description"] for item in records.json()["items"]} == {None}
    assert {item["user_id"] for item in records.json()["items"]} == {None}

    detail = client.get(f"/api/usage/records/{records.json()['items'][0]['id']}")
    assert detail.status_code == 200
    assert "user_color" not in detail.json()


def test_user_usage_groups_multiple_api_keys_by_user_snapshot(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    key_a = "sk-user-a"
    key_b = "sk-user-b"
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-4.1-mini",
                input_usd_per_million=1,
                output_usd_per_million=2,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )

    user = client.post(
        "/api/users",
        json={
            "username": "group-user",
            "password": "password",
            "is_admin": False,
            "nickname": "聚合用户",
        },
    )
    assert user.status_code == 200
    user_id = user.json()["id"]
    for api_key in (key_a, key_b):
        bound = client.post(
            f"/api/users/{user_id}/api-keys",
            json={"api_key": api_key, "description": "VSCode"},
        )
        assert bound.status_code == 200

    with Session(get_engine()) as session:
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": key_a,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 100,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": key_b,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 200,
            },
        )

    users = client.get("/api/users")
    assert users.status_code == 200
    grouped_user = next(item for item in users.json() if item["id"] == user_id)
    assert grouped_user["id"] == user_id
    assert grouped_user["key_count"] == 2
    assert grouped_user["records"] == 2
    assert grouped_user["today_total_tokens"] == 300

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    overview = client.get("/api/usage/overview", params=params)
    assert overview.status_code == 200
    top_user = overview.json()["user_ranking"]["items"][0]
    assert top_user["user_id"] == user_id
    assert top_user["records"] == 2
    assert top_user["total_tokens"] == 300
    assert any(item["label"] == "聚合用户" for item in overview.json()["options"]["users"])

    user_records = client.get("/api/usage/records", params={**params, "user_id": user_id})
    assert user_records.status_code == 200
    assert user_records.json()["total"] == 2
    assert {item["user_label"] for item in user_records.json()["items"]} == {"聚合用户"}

    unbound = client.delete(f"/api/users/{user_id}/api-keys/{hash_api_key(key_b)}")
    assert unbound.status_code == 204
    user_records_after_unbind = client.get(
        "/api/usage/records",
        params={**params, "user_id": user_id},
    )
    assert user_records_after_unbind.status_code == 200
    assert user_records_after_unbind.json()["total"] == 2
    assert {item["api_key_description"] for item in user_records_after_unbind.json()["items"]} == {
        "VSCode"
    }


def test_usage_records_can_filter_by_api_key_description_snapshot(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    key_a = "sk-description-a"
    key_b = "sk-description-b"
    user = client.post(
        "/api/users",
        json={
            "username": "description-user",
            "password": "password",
            "is_admin": False,
            "nickname": "描述聚合",
        },
    )
    assert user.status_code == 200
    user_id = user.json()["id"]
    for api_key in (key_a, key_b):
        bound = client.post(
            f"/api/users/{user_id}/api-keys",
            json={"api_key": api_key, "description": "VSCode"},
        )
        assert bound.status_code == 200

    with Session(get_engine()) as session:
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": key_a,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 100,
            },
        )
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": key_b,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 200,
            },
        )

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    description_records = client.get(
        "/api/usage/records",
        params={**params, "api_key_description": "VSCode"},
    )
    assert description_records.status_code == 200
    assert description_records.json()["total"] == 2
    assert {item["api_key_description"] for item in description_records.json()["items"]} == {
        "VSCode"
    }
    assert all("api_key_hash" not in item for item in description_records.json()["items"])

    overview = client.get("/api/usage/overview", params=params)
    assert overview.status_code == 200
    options = overview.json()["options"]["api_key_descriptions"]
    assert [item["key"] for item in options] == ["VSCode"]
    assert [item["label"] for item in options] == ["VSCode"]
    description_ranking = overview.json()["api_key_description_ranking"]["items"]
    assert description_ranking[0]["key"] == "VSCode"
    assert description_ranking[0]["api_key_description"] == "VSCode"
    assert description_ranking[0]["records"] == 2


def test_usage_record_keeps_account_and_api_key_description_snapshot(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    now = datetime.now()
    api_key = "snapshot-key"
    user = client.post(
        "/api/users",
        json={
            "username": "snapshot-user",
            "password": "password",
            "is_admin": False,
            "nickname": "可改昵称",
        },
    )
    assert user.status_code == 200
    user_id = user.json()["id"]
    bound = client.post(
        f"/api/users/{user_id}/api-keys",
        json={"api_key": api_key, "description": "VSCode"},
    )
    assert bound.status_code == 200

    with Session(get_engine()) as session:
        save_usage_message(
            session,
            {
                "timestamp": now.isoformat(),
                "api_key": api_key,
                "provider": "openai",
                "model": "gpt-4.1-mini",
                "input_tokens": 100,
            },
        )
        usage_record = session.exec(select(UsageRecord)).one()
        assert usage_record.usage_username == "snapshot-user"

    rebound = client.post(
        f"/api/users/{user_id}/api-keys",
        json={"api_key": api_key, "description": "Cursor"},
    )
    assert rebound.status_code == 200
    updated_user = client.put(
        f"/api/users/{user_id}",
        json={
            "username": "snapshot-user",
            "is_admin": False,
            "nickname": "当前昵称",
        },
    )
    assert updated_user.status_code == 200
    unbound = client.delete(f"/api/users/{user_id}/api-keys/{hash_api_key(api_key)}")
    assert unbound.status_code == 204

    params = {
        "start": (now - timedelta(hours=1)).isoformat(),
        "end": (now + timedelta(hours=1)).isoformat(),
    }
    records = client.get("/api/usage/records", params={**params, "user_id": user_id})
    assert records.status_code == 200
    assert records.json()["total"] == 1
    item = records.json()["items"][0]
    assert item["user_id"] == user_id
    assert item["user_label"] == "当前昵称"
    assert item["api_key_description"] == "VSCode"

    disabled_user = client.post(f"/api/users/{user_id}/disable")
    assert disabled_user.status_code == 204

    disabled_login = client.post(
        "/api/auth/login",
        json={"username": "snapshot-user", "password": "password"},
    )
    assert disabled_login.status_code == 401

    with Session(get_engine()) as session:
        disabled_row = session.get(User, user_id)
        assert disabled_row is not None
        assert disabled_row.disabled_at is not None

    visible_users = client.get("/api/users")
    assert visible_users.status_code == 200
    disabled_user_item = next(item for item in visible_users.json() if item["id"] == user_id)
    assert disabled_user_item["disabled_at"] is not None

    duplicate_disabled_username = client.post(
        "/api/users",
        json={
            "username": "snapshot-user",
            "password": "password",
            "is_admin": False,
            "nickname": "复用账号",
        },
    )
    assert duplicate_disabled_username.status_code == 409

    disabled_user_records = client.get("/api/usage/records", params={**params, "user_id": user_id})
    assert disabled_user_records.status_code == 200
    disabled_record_item = disabled_user_records.json()["items"][0]
    assert disabled_record_item["user_id"] == user_id
    assert disabled_record_item["user_label"] == "当前昵称 (已禁用)"

    disabled_user_detail = client.get(f"/api/usage/records/{disabled_record_item['id']}")
    assert disabled_user_detail.status_code == 200
    assert disabled_user_detail.json()["user_label"] == "当前昵称 (已禁用)"

    enabled_user = client.post(f"/api/users/{user_id}/enable")
    assert enabled_user.status_code == 204
    enabled_login = client.post(
        "/api/auth/login",
        json={"username": "snapshot-user", "password": "password"},
    )
    assert enabled_login.status_code == 200


def test_disabling_user_removes_remote_keys_and_enabling_restores_them(
    client: TestClient,
    monkeypatch,
) -> None:
    _login_and_change_default_password(client)
    removed_hashes: list[str] = []
    restored_keys: list[str] = []
    monkeypatch.setattr(
        cpa_management_service,
        "remove_remote_api_key_hash",
        lambda api_key_hash: removed_hashes.append(api_key_hash),
    )
    monkeypatch.setattr(
        cpa_management_service,
        "add_remote_api_key",
        lambda api_key: restored_keys.append(api_key),
    )
    api_key = "disable-bound-key"
    api_key_hash = hash_api_key(api_key)
    created = client.post(
        "/api/users",
        json={
            "username": "disabled-user",
            "password": "password",
            "is_admin": False,
            "nickname": "禁用用户",
        },
    )
    assert created.status_code == 200
    user_id = created.json()["id"]
    bound = client.post(
        f"/api/users/{user_id}/api-keys",
        json={"api_key": api_key, "description": "VSCode"},
    )
    assert bound.status_code == 200

    disabled = client.post(f"/api/users/{user_id}/disable")
    assert disabled.status_code == 204
    assert removed_hashes == [api_key_hash]

    with Session(get_engine()) as session:
        disabled_row = session.get(User, user_id)
        assert disabled_row is not None
        assert disabled_row.disabled_at is not None
        assert session.get(UserApiKey, api_key_hash) is not None

    enabled = client.post(f"/api/users/{user_id}/enable")
    assert enabled.status_code == 204
    assert restored_keys == [api_key]

    with Session(get_engine()) as session:
        enabled_row = session.get(User, user_id)
        assert enabled_row is not None
        assert enabled_row.disabled_at is None
