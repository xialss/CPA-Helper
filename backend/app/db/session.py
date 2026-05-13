from collections.abc import Generator

from sqlalchemy.engine import Engine
from sqlmodel import Session, SQLModel, create_engine

from app.core.paths import get_sqlite_path

_engine: Engine | None = None


def get_engine() -> Engine:
    global _engine
    if _engine is None:
        sqlite_path = get_sqlite_path()
        _engine = create_engine(
            f"sqlite:///{sqlite_path}",
            connect_args={"check_same_thread": False},
        )
    return _engine


def init_db() -> None:
    from app import models  # noqa: F401

    SQLModel.metadata.create_all(get_engine())


def get_session() -> Generator[Session, None, None]:
    with Session(get_engine()) as session:
        yield session


def reset_engine_for_tests() -> None:
    global _engine
    if _engine is not None:
        _engine.dispose()
    _engine = None

