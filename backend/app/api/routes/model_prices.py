from fastapi import APIRouter, status

from app.api.deps import ReadyAdminDep, SessionDep
from app.schemas.pricing import (
    ModelPriceCreate,
    ModelPriceResponse,
    ModelPriceSyncRequest,
    ModelPriceSyncResponse,
    ModelPriceUpdate,
)
from app.services.pricing_service import (
    create_price,
    delete_price,
    list_prices,
    sync_litellm_prices,
    update_price,
)

router = APIRouter(prefix="/model-prices", tags=["model-prices"])


@router.get("", response_model=list[ModelPriceResponse])
def get_prices(
    session: SessionDep,
    user: ReadyAdminDep,
) -> list[ModelPriceResponse]:
    return list_prices(session)


@router.post("", response_model=ModelPriceResponse, status_code=status.HTTP_201_CREATED)
def post_price(
    payload: ModelPriceCreate,
    session: SessionDep,
    user: ReadyAdminDep,
) -> ModelPriceResponse:
    return create_price(session, payload)


@router.post("/sync/litellm", response_model=ModelPriceSyncResponse)
def sync_prices_from_litellm(
    session: SessionDep,
    user: ReadyAdminDep,
    payload: ModelPriceSyncRequest | None = None,
) -> ModelPriceSyncResponse:
    return sync_litellm_prices(session, payload.source_url if payload else None)


@router.put("/{price_id}", response_model=ModelPriceResponse)
def put_price(
    price_id: int,
    payload: ModelPriceUpdate,
    session: SessionDep,
    user: ReadyAdminDep,
) -> ModelPriceResponse:
    return update_price(session, price_id, payload)


@router.delete("/{price_id}", status_code=status.HTTP_204_NO_CONTENT)
def remove_price(
    price_id: int,
    session: SessionDep,
    user: ReadyAdminDep,
) -> None:
    delete_price(session, price_id)
