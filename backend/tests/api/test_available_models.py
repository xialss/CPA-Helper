import json

import httpx
from fastapi.testclient import TestClient
from sqlmodel import Session

from app.core.security import hash_api_key
from app.db.session import get_engine
from app.models import UserApiKey
from app.schemas.pricing import ModelPriceCreate
from app.services import available_models_service
from app.services.pricing_service import create_price


def _setup_admin(client: TestClient) -> int:
    response = client.post(
        "/api/auth/setup",
        json={"username": "admin", "password": "new-password", "nickname": "管理员"},
    )
    assert response.status_code == 200
    return int(response.json()["id"])


def _create_regular_user(client: TestClient) -> int:
    response = client.post(
        "/api/users",
        json={
            "username": "model-user",
            "password": "password",
            "is_admin": False,
            "nickname": "模型用户",
        },
    )
    assert response.status_code == 200
    return int(response.json()["id"])


def _bind_api_key(client: TestClient, user_id: int, api_key: str, description: str) -> None:
    response = client.post(
        f"/api/users/{user_id}/api-keys",
        json={"api_key": api_key, "description": description},
    )
    assert response.status_code == 200


def test_available_models_returns_no_key_state(client: TestClient) -> None:
    _setup_admin(client)

    response = client.get("/api/account/models")

    assert response.status_code == 200
    assert response.json() == {
        "has_api_keys": False,
        "api_key_count": 0,
        "queryable_api_key_count": 0,
        "models": [],
        "errors": [],
    }


def test_available_models_reports_bound_key_without_stored_secret(
    client: TestClient,
    monkeypatch,
) -> None:
    admin_id = _setup_admin(client)
    with Session(get_engine()) as session:
        session.add(
            UserApiKey(
                api_key_hash=hash_api_key("legacy-key"),
                user_id=admin_id,
                api_key=None,
                description="Legacy",
            )
        )
        session.commit()

    did_call_cpa = False

    def models_client() -> httpx.Client:
        nonlocal did_call_cpa
        did_call_cpa = True
        return httpx.Client(base_url="http://cpa.test")

    monkeypatch.setattr(available_models_service, "_models_client", models_client)

    response = client.get("/api/account/models")

    assert response.status_code == 200
    assert response.json() == {
        "has_api_keys": True,
        "api_key_count": 1,
        "queryable_api_key_count": 0,
        "models": [],
        "errors": [],
    }
    assert did_call_cpa is False


def test_available_models_aggregates_current_user_keys_without_leaking_api_keys(
    client: TestClient,
    monkeypatch,
) -> None:
    admin_id = _setup_admin(client)
    regular_user_id = _create_regular_user(client)
    _bind_api_key(client, admin_id, "sk-admin-secret", "Admin IDE")
    _bind_api_key(client, regular_user_id, "sk-user-one", "VSCode")
    _bind_api_key(client, regular_user_id, "sk-user-two", "Cursor")
    login = client.post(
        "/api/auth/login",
        json={"username": "model-user", "password": "password"},
    )
    assert login.status_code == 200
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-4o",
                input_usd_per_million=2.5,
                output_usd_per_million=10,
                cached_usd_per_million=1.25,
                reasoning_usd_per_million=0,
            ),
        )
    seen_authorizations: list[str] = []

    def handler(request: httpx.Request) -> httpx.Response:
        authorization = request.headers.get("authorization", "")
        seen_authorizations.append(authorization)
        if authorization == "Bearer sk-user-one":
            return httpx.Response(
                200,
                json={
                    "data": [
                        {
                            "id": "gpt-4o",
                            "object": "model",
                            "owned_by": "openai",
                            "context_window": 128000,
                        },
                        {
                            "id": "gemini-pro",
                            "name": "Gemini Pro",
                        },
                    ]
                },
            )
        if authorization == "Bearer sk-user-two":
            return httpx.Response(
                200,
                json=[{"id": "gpt-4o", "name": "GPT-4o", "tier": "paid"}],
            )
        return httpx.Response(401, json={"error": "unexpected key"})

    transport = httpx.MockTransport(handler)
    monkeypatch.setattr(
        available_models_service,
        "_models_client",
        lambda: httpx.Client(base_url="http://cpa.test", transport=transport),
    )

    response = client.get("/api/account/models")

    assert response.status_code == 200
    body = response.json()
    serialized_body = json.dumps(body, ensure_ascii=False)
    assert body["has_api_keys"] is True
    assert body["api_key_count"] == 2
    assert body["queryable_api_key_count"] == 2
    assert {model["id"] for model in body["models"]} == {"gpt-4o", "gemini-pro"}
    assert "sk-admin-secret" not in seen_authorizations
    assert seen_authorizations == ["Bearer sk-user-one", "Bearer sk-user-two"]
    assert "sk-user-one" not in serialized_body
    assert "sk-user-two" not in serialized_body

    gpt_model = next(model for model in body["models"] if model["id"] == "gpt-4o")
    assert gpt_model["name"] == "GPT-4o"
    assert "provider" not in gpt_model
    assert gpt_model["owner"] == "openai"
    assert gpt_model["metadata"] == {"context_window": 128000, "tier": "paid"}
    assert gpt_model["price"] == {
        "provider": "openai",
        "model": "gpt-4o",
        "input_usd_per_million": 2.5,
        "output_usd_per_million": 10.0,
        "cached_usd_per_million": 1.25,
        "reasoning_usd_per_million": 0.0,
    }
    assert [source["description"] for source in gpt_model["sources"]] == ["Cursor", "VSCode"]
    assert all(source["api_key_preview"] for source in gpt_model["sources"])

    gemini_model = next(model for model in body["models"] if model["id"] == "gemini-pro")
    assert gemini_model["price"] is None


def test_available_models_returns_null_for_non_strict_provider_model_price(
    client: TestClient,
    monkeypatch,
) -> None:
    admin_id = _setup_admin(client)
    _bind_api_key(client, admin_id, "sk-anthropic-models", "VSCode")
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="anthropic",
                model="claude-opus-4-6",
                input_usd_per_million=15,
                output_usd_per_million=75,
                cached_usd_per_million=1.5,
                reasoning_usd_per_million=0,
            ),
        )
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

    transport = httpx.MockTransport(
        lambda request: httpx.Response(
            200,
            json={
                "data": [
                    {
                        "id": "anthropic/claude-opus-4.6",
                        "name": "anthropic/claude-opus-4.6",
                        "owned_by": "TokenRouter",
                    },
                    {
                        "id": "claude-opus-4.6",
                        "name": "claude-opus-4.6",
                        "owned_by": "anthropic",
                    },
                    {
                        "id": "gpt-5.5",
                        "name": "gpt-5.5",
                        "owned_by": "openai",
                    }
                ]
            },
        )
    )
    monkeypatch.setattr(
        available_models_service,
        "_models_client",
        lambda: httpx.Client(base_url="http://cpa.test", transport=transport),
    )

    response = client.get("/api/account/models")

    assert response.status_code == 200
    models_by_id = {model["id"]: model for model in response.json()["models"]}
    assert models_by_id["anthropic/claude-opus-4.6"]["price"] is None
    assert models_by_id["claude-opus-4.6"]["price"] is None
    assert models_by_id["gpt-5.5"]["price"] is None


def test_available_models_returns_sanitized_cpa_error(client: TestClient, monkeypatch) -> None:
    admin_id = _setup_admin(client)
    _bind_api_key(client, admin_id, "sk-failing-secret", "Broken")

    transport = httpx.MockTransport(lambda request: httpx.Response(503, json={"error": "down"}))
    monkeypatch.setattr(
        available_models_service,
        "_models_client",
        lambda: httpx.Client(base_url="http://cpa.test", transport=transport),
    )

    response = client.get("/api/account/models")

    assert response.status_code == 422
    body = response.json()
    message = body["detail"]["message"]
    assert "HTTP 503" in message
    assert "sk-failing-secret" not in json.dumps(body, ensure_ascii=False)


def test_available_models_does_not_echo_short_api_key(
    client: TestClient,
    monkeypatch,
) -> None:
    admin_id = _setup_admin(client)
    _bind_api_key(client, admin_id, "xy", "Short")

    transport = httpx.MockTransport(
        lambda request: httpx.Response(200, json={"data": [{"id": "tiny-model"}]})
    )
    monkeypatch.setattr(
        available_models_service,
        "_models_client",
        lambda: httpx.Client(base_url="http://cpa.test", transport=transport),
    )

    response = client.get("/api/account/models")

    assert response.status_code == 200
    serialized_body = json.dumps(response.json(), ensure_ascii=False)
    assert "xy" not in serialized_body
    assert "****" in serialized_body


def test_available_models_reports_unexpected_cpa_response_shape(
    client: TestClient,
    monkeypatch,
) -> None:
    admin_id = _setup_admin(client)
    _bind_api_key(client, admin_id, "sk-unexpected-shape", "Unexpected")

    transport = httpx.MockTransport(
        lambda request: httpx.Response(200, json={"unexpected": []})
    )
    monkeypatch.setattr(
        available_models_service,
        "_models_client",
        lambda: httpx.Client(base_url="http://cpa.test", transport=transport),
    )

    response = client.get("/api/account/models")

    assert response.status_code == 422
    body = response.json()
    message = body["detail"]["message"]
    assert "响应缺少模型列表" in message
    assert "sk-unexpected-shape" not in json.dumps(body, ensure_ascii=False)
