from typing import Annotated

from fastapi import Depends, Request
from sqlmodel import Session, select

from app.core.config import load_config
from app.core.errors import AuthenticationError, ForbiddenError
from app.core.security import SESSION_COOKIE_NAME, read_session_token
from app.db.session import get_engine, get_session
from app.models import User
from app.schemas.auth import AuthUserResponse


def get_current_user(request: Request) -> AuthUserResponse:
    config = load_config()
    token = request.cookies.get(SESSION_COOKIE_NAME)
    if not token:
        raise AuthenticationError("请先登录")
    identity = read_session_token(token, config.session_secret)
    if identity is None:
        raise AuthenticationError("登录会话已失效")
    with Session(get_engine()) as session:
        if identity.user_id is not None:
            user = session.get(User, identity.user_id)
        elif identity.username is not None:
            user = session.exec(
                select(User).where(
                    User.username == identity.username,
                    User.disabled_at.is_(None),
                )
            ).first()
        else:
            user = None
        if user is None or user.disabled_at is not None:
            raise AuthenticationError("登录会话已失效")
    return AuthUserResponse(
        id=user.id or 0,
        username=user.username,
        is_admin=bool(user.is_admin),
        must_change_password=False,
    )


CurrentUserDep = Annotated[AuthUserResponse, Depends(get_current_user)]


def require_ready_user(user: CurrentUserDep) -> AuthUserResponse:
    if user.must_change_password:
        raise ForbiddenError("首次登录后必须先修改账号密码")
    return user


SessionDep = Annotated[Session, Depends(get_session)]
ReadyUserDep = Annotated[AuthUserResponse, Depends(require_ready_user)]


def require_admin_user(user: ReadyUserDep) -> AuthUserResponse:
    if not user.is_admin:
        raise ForbiddenError("需要管理员权限")
    return user


ReadyAdminDep = Annotated[AuthUserResponse, Depends(require_admin_user)]
