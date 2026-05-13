from datetime import datetime

from pydantic import BaseModel, Field, field_validator, model_validator


class UserPayload(BaseModel):
    username: str = Field(min_length=1, max_length=120)
    password: str | None = Field(default=None, max_length=400)
    is_admin: bool = False
    nickname: str = Field(min_length=1, max_length=240)

    @field_validator("username")
    @classmethod
    def username_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("账号不能为空")
        return normalized

    @field_validator("password")
    @classmethod
    def password_must_not_be_blank(cls, value: str | None) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        if not normalized:
            return None
        return normalized

    @field_validator("nickname")
    @classmethod
    def nickname_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("用户昵称不能为空")
        return normalized


class UserApiKeyBindPayload(BaseModel):
    api_key: str | None = Field(default=None, max_length=400)
    api_key_hash: str | None = Field(default=None, min_length=64, max_length=64)
    description: str = Field(min_length=1, max_length=240)

    @field_validator("api_key")
    @classmethod
    def api_key_must_not_be_blank(cls, value: str | None) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        if not normalized:
            raise ValueError("API KEY 不能为空")
        return normalized

    @field_validator("api_key_hash")
    @classmethod
    def api_key_hash_must_not_be_blank(cls, value: str | None) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        if not normalized:
            raise ValueError("API KEY 标识不能为空")
        return normalized

    @field_validator("description")
    @classmethod
    def description_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("API KEY 描述不能为空")
        return normalized

    @model_validator(mode="after")
    def must_provide_key_or_hash(self) -> "UserApiKeyBindPayload":
        if self.api_key is None and self.api_key_hash is None:
            raise ValueError("API KEY 或 API KEY 标识不能为空")
        return self


class UserApiKeySummary(BaseModel):
    api_key_hash: str
    api_key: str | None = None
    description: str = ""
    user_id: int | None = None
    user_name: str | None = None
    created_at: datetime | None = None
    updated_at: datetime | None = None
    records: int = 0
    success_records: int = 0
    failed_records: int = 0
    total_tokens: int = 0
    today_records: int = 0
    today_success_records: int = 0
    today_failed_records: int = 0
    today_input_tokens: int = 0
    today_output_tokens: int = 0
    today_cached_tokens: int = 0
    today_reasoning_tokens: int = 0
    today_total_tokens: int = 0
    today_estimated_cost_usd: float = 0.0
    today_unpriced_records: int = 0
    first_seen_at: datetime | None = None
    last_seen_at: datetime | None = None
    last_provider: str | None = None
    last_model: str | None = None
    providers: list[str] = Field(default_factory=list)
    models: list[str] = Field(default_factory=list)


class UserSummaryResponse(BaseModel):
    id: int
    username: str
    is_admin: bool
    nickname: str
    disabled_at: datetime | None = None
    password_set: bool
    created_at: datetime
    updated_at: datetime
    api_keys: list[UserApiKeySummary] = Field(default_factory=list)
    key_count: int = 0
    records: int = 0
    success_records: int = 0
    failed_records: int = 0
    total_tokens: int = 0
    today_records: int = 0
    today_success_records: int = 0
    today_failed_records: int = 0
    today_input_tokens: int = 0
    today_output_tokens: int = 0
    today_cached_tokens: int = 0
    today_reasoning_tokens: int = 0
    today_total_tokens: int = 0
    today_estimated_cost_usd: float = 0.0
    today_unpriced_records: int = 0
    first_seen_at: datetime | None = None
    last_seen_at: datetime | None = None
    last_provider: str | None = None
    last_model: str | None = None
    providers: list[str] = Field(default_factory=list)
    models: list[str] = Field(default_factory=list)
