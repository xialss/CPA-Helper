from fastapi import APIRouter

from app.api.deps import ReadyUserDep, SessionDep
from app.schemas.available_models import AvailableModelsResponse
from app.services.available_models_service import list_current_user_available_models

router = APIRouter(prefix="/account/models", tags=["account-models"])


@router.get("", response_model=AvailableModelsResponse)
def get_available_models(
    session: SessionDep,
    user: ReadyUserDep,
) -> AvailableModelsResponse:
    return list_current_user_available_models(session, user.id)
