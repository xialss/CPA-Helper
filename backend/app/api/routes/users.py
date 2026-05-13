from fastapi import APIRouter, status

from app.api.deps import ReadyAdminDep, SessionDep
from app.schemas.users import (
    UserApiKeyBindPayload,
    UserApiKeySummary,
    UserPayload,
    UserSummaryResponse,
)
from app.services.user_service import (
    bind_user_api_key,
    create_user,
    disable_user,
    enable_user,
    list_observed_api_keys,
    list_users,
    unbind_user_api_key,
    update_user,
)

router = APIRouter(prefix="/users", tags=["users"])


@router.get("", response_model=list[UserSummaryResponse])
def get_users(
    session: SessionDep,
    user: ReadyAdminDep,
) -> list[UserSummaryResponse]:
    return list_users(session)


@router.post("", response_model=UserSummaryResponse)
def post_user(
    payload: UserPayload,
    session: SessionDep,
    user: ReadyAdminDep,
) -> UserSummaryResponse:
    return create_user(session, payload)


@router.get("/observed-api-keys", response_model=list[UserApiKeySummary])
def get_observed_api_keys(
    session: SessionDep,
    user: ReadyAdminDep,
) -> list[UserApiKeySummary]:
    return list_observed_api_keys(session)


@router.put("/{user_id}", response_model=UserSummaryResponse)
def put_user(
    user_id: int,
    payload: UserPayload,
    session: SessionDep,
    user: ReadyAdminDep,
) -> UserSummaryResponse:
    return update_user(session, user_id, payload)


@router.delete("/{user_id}", status_code=status.HTTP_204_NO_CONTENT)
def remove_user(
    user_id: int,
    session: SessionDep,
    user: ReadyAdminDep,
) -> None:
    disable_user(session, user_id)


@router.post("/{user_id}/disable", status_code=status.HTTP_204_NO_CONTENT)
def disable_user_account(
    user_id: int,
    session: SessionDep,
    user: ReadyAdminDep,
) -> None:
    disable_user(session, user_id)


@router.post("/{user_id}/enable", status_code=status.HTTP_204_NO_CONTENT)
def enable_user_account(
    user_id: int,
    session: SessionDep,
    user: ReadyAdminDep,
) -> None:
    enable_user(session, user_id)


@router.post("/{user_id}/api-keys", response_model=UserApiKeySummary)
def post_user_api_key(
    user_id: int,
    payload: UserApiKeyBindPayload,
    session: SessionDep,
    user: ReadyAdminDep,
) -> UserApiKeySummary:
    return bind_user_api_key(session, user_id, payload)


@router.delete("/{user_id}/api-keys/{api_key_hash}", status_code=status.HTTP_204_NO_CONTENT)
def remove_user_api_key(
    user_id: int,
    api_key_hash: str,
    session: SessionDep,
    user: ReadyAdminDep,
) -> None:
    unbind_user_api_key(session, user_id, api_key_hash)
