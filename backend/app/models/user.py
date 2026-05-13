from datetime import datetime

from sqlmodel import Field, SQLModel


class User(SQLModel, table=True):
    __tablename__ = "users"

    id: int | None = Field(default=None, primary_key=True)
    username: str = Field(default="", index=True, unique=True, max_length=120)
    password_hash: str | None = Field(default=None, max_length=200)
    password_salt: str | None = Field(default=None, max_length=64)
    is_admin: bool = Field(default=False, index=True)
    nickname: str = Field(default="", max_length=240)
    disabled_at: datetime | None = Field(default=None, index=True)
    created_at: datetime = Field(default_factory=datetime.now)
    updated_at: datetime = Field(default_factory=datetime.now)


class UserApiKey(SQLModel, table=True):
    __tablename__ = "user_api_keys"

    api_key_hash: str = Field(primary_key=True, max_length=64)
    user_id: int = Field(foreign_key="users.id", index=True)
    api_key: str | None = Field(default=None, max_length=400)
    description: str = Field(default="", max_length=240)
    created_at: datetime = Field(default_factory=datetime.now)
    updated_at: datetime = Field(default_factory=datetime.now)
