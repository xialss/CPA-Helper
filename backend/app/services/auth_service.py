from datetime import datetime

from sqlmodel import Session, select

from app.core.config import read_legacy_account_config
from app.core.errors import AuthenticationError, ConflictError, ForbiddenError
from app.core.security import create_salt, hash_password, verify_password
from app.db.session import get_engine
from app.models import User


def authenticate(username: str, password: str) -> tuple[int, str, bool, bool]:
    with Session(get_engine()) as session:
        _migrate_legacy_account_if_needed(session)
        if _user_count(session) == 0:
            raise ConflictError("系统尚未初始化，请先创建首个管理员账号")
        user = _find_user_by_username(session, username.strip())
        if (
            user is None
            or user.disabled_at is not None
            or user.password_salt is None
            or user.password_hash is None
        ):
            raise AuthenticationError("用户名或密码不正确")
        if not verify_password(password, user.password_salt, user.password_hash):
            raise AuthenticationError("用户名或密码不正确")
        return user.id or 0, user.username, bool(user.is_admin), False


def setup_required() -> bool:
    with Session(get_engine()) as session:
        _migrate_legacy_account_if_needed(session)
        return _user_count(session) == 0


def create_first_admin(
    *,
    username: str,
    password: str,
    nickname: str,
) -> tuple[int, str, bool, bool]:
    with Session(get_engine()) as session:
        _migrate_legacy_account_if_needed(session)
        if _user_count(session) > 0:
            raise ConflictError("首个管理员账号已存在")
        salt = create_salt()
        user = User(
            username=username.strip(),
            password_salt=salt,
            password_hash=hash_password(password, salt),
            is_admin=True,
            nickname=nickname.strip(),
        )
        session.add(user)
        session.commit()
        session.refresh(user)
        return user.id or 0, user.username, bool(user.is_admin), False


def change_credentials(
    *,
    current_user_id: int,
    password: str,
    current_password: str | None,
) -> tuple[int, str, bool, bool]:
    with Session(get_engine()) as session:
        user = session.get(User, current_user_id)
        if user is None or user.disabled_at is not None:
            raise AuthenticationError("登录会话已失效")
        if current_password is None:
            raise ForbiddenError("需要提供当前密码")
        if user.password_salt is None or user.password_hash is None:
            raise AuthenticationError("当前密码不正确")
        if not verify_password(current_password, user.password_salt, user.password_hash):
            raise AuthenticationError("当前密码不正确")
        user.password_salt = create_salt()
        user.password_hash = hash_password(password, user.password_salt)
        user.updated_at = datetime.now()
        session.add(user)
        session.commit()
        session.refresh(user)
        return user.id or 0, user.username, bool(user.is_admin), False


def _migrate_legacy_account_if_needed(session: Session) -> None:
    if _user_count(session) > 0:
        return
    account = read_legacy_account_config()
    if account is None:
        return
    user = User(
        username=account.username,
        password_hash=account.password_hash,
        password_salt=account.password_salt,
        is_admin=True,
        nickname=account.username,
    )
    session.add(user)
    session.commit()


def _find_user_by_username(session: Session, username: str) -> User | None:
    return session.exec(select(User).where(User.username == username)).first()


def _user_count(session: Session) -> int:
    return len(session.exec(select(User.id)).all())
