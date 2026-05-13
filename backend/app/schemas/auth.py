from pydantic import BaseModel, Field, field_validator


class LoginRequest(BaseModel):
    username: str = Field(min_length=1, max_length=80)
    password: str = Field(min_length=1, max_length=256)

    @field_validator("username")
    @classmethod
    def username_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("账号不能为空")
        return normalized


class ChangeCredentialsRequest(BaseModel):
    password: str = Field(min_length=8, max_length=256)
    current_password: str | None = Field(default=None, max_length=256)


class AuthUserResponse(BaseModel):
    id: int
    username: str
    is_admin: bool
    must_change_password: bool


class LoginResponse(BaseModel):
    id: int
    username: str
    is_admin: bool
    must_change_password: bool


class SetupStateResponse(BaseModel):
    setup_required: bool


class FirstAdminSetupRequest(BaseModel):
    username: str = Field(min_length=1, max_length=120)
    password: str = Field(min_length=8, max_length=256)
    nickname: str = Field(min_length=1, max_length=240)

    @field_validator("username")
    @classmethod
    def username_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("账号不能为空")
        return normalized

    @field_validator("nickname")
    @classmethod
    def nickname_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("用户昵称不能为空")
        return normalized
