from fastapi.testclient import TestClient
from sqlmodel import Session, select

from app.db.session import get_engine
from app.models import ModelPrice
from app.schemas.pricing import ModelPriceCreate
from app.services import pricing_service
from app.services.pricing_service import create_price, find_matching_price, get_price_map


def _login_and_change_default_password(client: TestClient) -> None:
    setup = client.post(
        "/api/auth/setup",
        json={"username": "admin", "password": "new-password", "nickname": "管理员"},
    )
    assert setup.status_code == 200


def test_price_map_uses_exact_keys_and_query_side_provider_aliases(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="OpenAI",
                model="GPT-5.5",
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

        prices = get_price_map(session)

    assert set(prices) == {("openai", "gpt-5.5"), ("anthropic", "claude-sonnet")}

    codex_price = find_matching_price(prices, " codex ", " gpt-5.5 ")
    assert codex_price is not None
    assert codex_price.provider == "OpenAI"
    assert codex_price.model == "GPT-5.5"

    claude_price = find_matching_price(prices, "claude", "CLAUDE-sonnet")
    assert claude_price is not None
    assert claude_price.provider == "anthropic"
    assert find_matching_price(prices, None, "gpt-5.5") is None
    assert find_matching_price(prices, "codex", None) is None
    assert find_matching_price(prices, "TokenRouter", "anthropic/claude-sonnet") is None


def test_price_matching_does_not_apply_reverse_model_only_or_alias_fallbacks(
    client: TestClient,
) -> None:
    _login_and_change_default_password(client)
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
        create_price(
            session,
            ModelPriceCreate(
                provider="azure",
                model="shared-model",
                input_usd_per_million=5,
                output_usd_per_million=6,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )
        create_price(
            session,
            ModelPriceCreate(
                provider="anthropic",
                model="claude-opus-4-6",
                input_usd_per_million=7,
                output_usd_per_million=8,
                cached_usd_per_million=0,
                reasoning_usd_per_million=0,
            ),
        )

        prices = get_price_map(session)

    assert find_matching_price(prices, "openai", "gpt-5.5") is None
    assert find_matching_price(prices, "anthropic", "claude-sonnet") is None
    assert find_matching_price(prices, "openai", "shared-model") is None
    assert find_matching_price(prices, "anthropic", "claude-opus-4.6") is None
    assert find_matching_price(prices, "TokenRouter", "anthropic/claude-opus-4.6") is None


def test_litellm_price_sync_creates_prices_and_preserves_manual(
    client: TestClient,
    monkeypatch,
) -> None:
    _login_and_change_default_password(client)

    with Session(get_engine()) as session:
        create_price(
            session,
            ModelPriceCreate(
                provider="openai",
                model="gpt-4o-mini",
                input_usd_per_million=99,
                output_usd_per_million=99,
                cached_usd_per_million=99,
                reasoning_usd_per_million=99,
            ),
        )

    def fake_fetch(source_url: str):
        assert source_url == pricing_service.DEFAULT_LITELLM_PRICING_URL
        return {
            "gpt-4o-mini": {
                "litellm_provider": "openai",
                "input_cost_per_token": 1.5e-7,
                "output_cost_per_token": 6e-7,
                "cache_read_input_token_cost": 7.5e-8,
            },
            "claude-3-5-haiku": {
                "litellm_provider": "anthropic",
                "input_cost_per_token": 8e-7,
                "output_cost_per_token": 4e-6,
            },
            "sample_spec": {"input_cost_per_token": 1},
        }

    monkeypatch.setattr(pricing_service, "_fetch_litellm_pricing_json", fake_fetch)

    response = client.post("/api/model-prices/sync/litellm")
    assert response.status_code == 200
    body = response.json()
    assert body["total_entries"] == 3
    assert body["created"] == 1
    assert body["updated"] == 0
    assert body["unchanged"] == 0
    assert body["skipped_manual"] == 1
    assert body["skipped_invalid"] == 1
    assert body["imported"] == 1

    with Session(get_engine()) as session:
        manual = session.exec(
            select(ModelPrice).where(
                ModelPrice.provider == "openai",
                ModelPrice.model == "gpt-4o-mini",
            )
        ).one()
        assert manual.input_usd_per_million == 99
        assert manual.source == "manual"
        assert manual.auto_synced is False

        synced = session.exec(
            select(ModelPrice).where(
                ModelPrice.provider == "anthropic",
                ModelPrice.model == "claude-3-5-haiku",
            )
        ).one()
        assert synced.input_usd_per_million == 0.8
        assert synced.output_usd_per_million == 4
        assert synced.cached_usd_per_million == 0
        assert synced.reasoning_usd_per_million == 0
        assert synced.source == "litellm"
        assert synced.source_model == "claude-3-5-haiku"
        assert synced.auto_synced is True
        assert synced.last_synced_at is not None


def test_litellm_price_sync_updates_previous_auto_synced_prices(
    client: TestClient,
    monkeypatch,
) -> None:
    _login_and_change_default_password(client)

    monkeypatch.setattr(
        pricing_service,
        "_fetch_litellm_pricing_json",
        lambda _source_url: {
            "claude-3-5-haiku": {
                "litellm_provider": "anthropic",
                "input_cost_per_token": 8e-7,
                "output_cost_per_token": 4e-6,
            }
        },
    )
    first = client.post("/api/model-prices/sync/litellm")
    assert first.status_code == 200
    assert first.json()["created"] == 1

    monkeypatch.setattr(
        pricing_service,
        "_fetch_litellm_pricing_json",
        lambda _source_url: {
            "claude-3-5-haiku": {
                "litellm_provider": "anthropic",
                "input_cost_per_token": 1e-6,
                "output_cost_per_token": 5e-6,
                "cache_read_input_token_cost": 1e-7,
            }
        },
    )
    second = client.post("/api/model-prices/sync/litellm")
    assert second.status_code == 200
    assert second.json()["updated"] == 1

    with Session(get_engine()) as session:
        synced = session.exec(select(ModelPrice)).one()
        assert synced.input_usd_per_million == 1
        assert synced.output_usd_per_million == 5
        assert synced.cached_usd_per_million == 0.1
