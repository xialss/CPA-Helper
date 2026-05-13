from fastapi import APIRouter

from app.api.deps import ReadyAdminDep
from app.schemas.settings import SettingsResponse, SettingsUpdateRequest
from app.services.settings_service import settings_to_response, update_settings

router = APIRouter(prefix="/settings", tags=["settings"])


@router.get("", response_model=SettingsResponse)
def get_settings(user: ReadyAdminDep) -> SettingsResponse:
    return settings_to_response()


@router.put("", response_model=SettingsResponse)
def put_settings(
    payload: SettingsUpdateRequest,
    user: ReadyAdminDep,
) -> SettingsResponse:
    return update_settings(payload)
