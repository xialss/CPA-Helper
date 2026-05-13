from pydantic import BaseModel, Field, field_validator


class SettingsResponse(BaseModel):
    cliaproxy_url: str
    management_key: str
    management_key_set: bool
    collector_enabled: bool
    queue_name: str
    batch_size: int
    poll_interval_seconds: float
    retry_interval_seconds: float
    theme_preference: str


class SettingsUpdateRequest(BaseModel):
    cliaproxy_url: str | None = Field(default=None, max_length=500)
    management_key: str | None = Field(default=None, max_length=1000)
    collector_enabled: bool | None = None
    queue_name: str | None = Field(default=None, min_length=1, max_length=120)
    batch_size: int | None = Field(default=None, ge=1, le=1000)
    poll_interval_seconds: float | None = Field(default=None, ge=0.2, le=3600)
    retry_interval_seconds: float | None = Field(default=None, ge=1, le=3600)
    theme_preference: str | None = Field(default=None, pattern="^(system|light|dark)$")

    @field_validator("cliaproxy_url", "queue_name")
    @classmethod
    def optional_text_must_not_be_blank(cls, value: str | None) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        if not normalized:
            raise ValueError("不能为空")
        return normalized
