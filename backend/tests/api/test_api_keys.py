import json

from fastapi.testclient import TestClient

from app.core.errors import ValidationAppError
from app.core.security import hash_api_key
from app.services import cpa_management_service


def _login_and_change_default_password(client: TestClient) -> None:
    setup = client.post(
        "/api/auth/setup",
        json={"username": "admin", "password": "new-password", "nickname": "管理员昵称"},
    )
    assert setup.status_code == 200


def _create_regular_user(client: TestClient) -> int:
    user = client.post(
        "/api/users",
        json={
            "username": "api-key-user",
            "password": "password",
            "is_admin": False,
            "nickname": "密钥用户",
        },
    )
    assert user.status_code == 200
    return int(user.json()["id"])


def test_self_service_api_key_syncs_remote_and_only_lists_own_keys(
    client: TestClient,
    monkeypatch,
) -> None:
    _login_and_change_default_password(client)
    other_user_id = _create_regular_user(client)
    added_keys: list[str] = []
    removed_hashes: list[str] = []

    monkeypatch.setattr(
        cpa_management_service,
        "add_remote_api_key",
        lambda api_key: added_keys.append(api_key),
    )
    monkeypatch.setattr(
        cpa_management_service,
        "remove_remote_api_key_hash",
        lambda api_key_hash: removed_hashes.append(api_key_hash),
    )

    created = client.post(
        "/api/api-keys",
        json={"description": "VSCode"},
    )
    assert created.status_code == 200
    body = created.json()
    api_key = body["api_key"]
    api_key_hash = hash_api_key(api_key)
    assert api_key.startswith("sk-")
    assert len(api_key) == len("sk-") + 52
    assert api_key.removeprefix("sk-").isalnum()
    assert body["api_key_hash"] == api_key_hash
    assert body["api_key"] == api_key
    assert body["description"] == "VSCode"
    assert body["user_name"] == "admin"
    assert body["created_at"] is not None
    assert "管理员昵称" not in json.dumps(body, ensure_ascii=False)
    assert added_keys == [api_key]

    bound_other = client.post(
        f"/api/users/{other_user_id}/api-keys",
        json={"api_key": "other-user-key", "description": "Other IDE"},
    )
    assert bound_other.status_code == 200
    other_api_key_hash = hash_api_key("other-user-key")

    listed = client.get("/api/api-keys")
    assert listed.status_code == 200
    listed_body = listed.json()
    listed_hashes = [item["api_key_hash"] for item in listed_body]
    assert api_key_hash in listed_hashes
    assert other_api_key_hash not in listed_hashes
    assert listed_body[0]["created_at"] is not None
    assert listed_body[0]["api_key"] == api_key
    assert listed_body[0]["user_name"] == "admin"
    assert "管理员昵称" not in json.dumps(listed_body, ensure_ascii=False)

    updated = client.put(f"/api/api-keys/{api_key_hash}", json={"description": "Cursor"})
    assert updated.status_code == 200
    updated_body = updated.json()
    assert updated_body["description"] == "Cursor"
    assert updated_body["user_name"] == "admin"
    assert "管理员昵称" not in json.dumps(updated_body, ensure_ascii=False)

    forbidden_update = client.put(
        f"/api/api-keys/{other_api_key_hash}",
        json={"description": "Forbidden"},
    )
    assert forbidden_update.status_code == 404

    forbidden_delete = client.delete(f"/api/api-keys/{other_api_key_hash}")
    assert forbidden_delete.status_code == 404

    removed = client.delete(f"/api/api-keys/{api_key_hash}")
    assert removed.status_code == 204
    assert removed_hashes == [api_key_hash]

    listed_after_delete = client.get("/api/api-keys")
    assert listed_after_delete.status_code == 200
    assert [item["api_key_hash"] for item in listed_after_delete.json()] == []


def test_generated_api_key_remote_failure_does_not_create_local_binding(
    client: TestClient,
    monkeypatch,
) -> None:
    _login_and_change_default_password(client)
    _create_regular_user(client)

    def fail_remote_sync(api_key: str) -> None:
        raise ValidationAppError("远端不可用")

    monkeypatch.setattr(
        cpa_management_service,
        "add_remote_api_key",
        fail_remote_sync,
    )

    created = client.post(
        "/api/api-keys",
        json={"description": "VSCode"},
    )
    assert created.status_code == 422

    listed = client.get("/api/api-keys")
    assert listed.status_code == 200
    assert listed.json() == []
