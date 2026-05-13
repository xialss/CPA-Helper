from fastapi import APIRouter, Response

from app.api.deps import CurrentUserDep
from app.core.config import load_config
from app.core.security import (
    SESSION_COOKIE_NAME,
    SESSION_MAX_AGE_SECONDS,
    create_session_token,
)
from app.schemas.auth import (
    AuthUserResponse,
    ChangeCredentialsRequest,
    FirstAdminSetupRequest,
    LoginRequest,
    LoginResponse,
    SetupStateResponse,
)
from app.services.auth_service import (
    authenticate,
    change_credentials,
    create_first_admin,
    setup_required,
)

router = APIRouter(prefix="/auth", tags=["auth"])


def _set_session_cookie(response: Response, user_id: int) -> None:
    config = load_config()
    response.set_cookie(
        SESSION_COOKIE_NAME,
        create_session_token(user_id, config.session_secret),
        httponly=True,
        samesite="lax",
        max_age=SESSION_MAX_AGE_SECONDS,
    )


@router.post("/login", response_model=LoginResponse)
def login(payload: LoginRequest, response: Response) -> LoginResponse:
    user_id, username, is_admin, must_change = authenticate(payload.username, payload.password)
    _set_session_cookie(response, user_id)
    return LoginResponse(
        id=user_id,
        username=username,
        is_admin=is_admin,
        must_change_password=must_change,
    )


@router.get("/setup", response_model=SetupStateResponse)
def setup_state() -> SetupStateResponse:
    return SetupStateResponse(setup_required=setup_required())


@router.post("/setup", response_model=AuthUserResponse)
def setup_first_admin(
    payload: FirstAdminSetupRequest,
    response: Response,
) -> AuthUserResponse:
    user_id, username, is_admin, must_change = create_first_admin(
        username=payload.username,
        password=payload.password,
        nickname=payload.nickname,
    )
    _set_session_cookie(response, user_id)
    return AuthUserResponse(
        id=user_id,
        username=username,
        is_admin=is_admin,
        must_change_password=must_change,
    )


@router.get("/me", response_model=AuthUserResponse)
def me(user: CurrentUserDep) -> AuthUserResponse:
    return user


@router.post("/change-credentials", response_model=AuthUserResponse)
def change_account(
    payload: ChangeCredentialsRequest,
    response: Response,
    user: CurrentUserDep,
) -> AuthUserResponse:
    user_id, username, is_admin, must_change = change_credentials(
        current_user_id=user.id,
        password=payload.password,
        current_password=payload.current_password,
    )
    _set_session_cookie(response, user_id)
    return AuthUserResponse(
        id=user_id,
        username=username,
        is_admin=is_admin,
        must_change_password=must_change,
    )


@router.post("/logout")
def logout(response: Response) -> dict[str, bool]:
    response.delete_cookie(SESSION_COOKIE_NAME)
    return {"ok": True}
