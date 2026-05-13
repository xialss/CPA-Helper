from fastapi import APIRouter, status

from app.api.deps import ReadyUserDep, SessionDep
from app.schemas.api_keys import ApiKeyCreateRequest, ApiKeyUpdateRequest
from app.schemas.users import UserApiKeySummary
from app.services.user_service import (
    create_generated_api_key_for_current_user,
    delete_current_user_api_key,
    list_current_user_api_keys,
    update_current_user_api_key,
)

router = APIRouter(prefix="/api-keys", tags=["api-keys"])


@router.get("", response_model=list[UserApiKeySummary])
def get_api_keys(
    session: SessionDep,
    user: ReadyUserDep,
) -> list[UserApiKeySummary]:
    return list_current_user_api_keys(session, user.username)


@router.post("", response_model=UserApiKeySummary)
def post_api_key(
    payload: ApiKeyCreateRequest,
    session: SessionDep,
    user: ReadyUserDep,
) -> UserApiKeySummary:
    return create_generated_api_key_for_current_user(session, user.username, payload)


@router.put("/{api_key_hash}", response_model=UserApiKeySummary)
def put_api_key(
    api_key_hash: str,
    payload: ApiKeyUpdateRequest,
    session: SessionDep,
    user: ReadyUserDep,
) -> UserApiKeySummary:
    return update_current_user_api_key(session, user.username, api_key_hash, payload)


@router.delete("/{api_key_hash}", status_code=status.HTTP_204_NO_CONTENT)
def remove_api_key(
    api_key_hash: str,
    session: SessionDep,
    user: ReadyUserDep,
) -> None:
    delete_current_user_api_key(session, user.username, api_key_hash)
