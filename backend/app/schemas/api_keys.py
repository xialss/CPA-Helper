from pydantic import BaseModel, Field, field_validator


class ApiKeyCreateRequest(BaseModel):
    description: str = Field(min_length=1, max_length=240)

    @field_validator("description")
    @classmethod
    def description_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("API KEY 描述不能为空")
        return normalized


class ApiKeyUpdateRequest(BaseModel):
    description: str = Field(min_length=1, max_length=240)

    @field_validator("description")
    @classmethod
    def description_must_not_be_blank(cls, value: str) -> str:
        normalized = value.strip()
        if not normalized:
            raise ValueError("API KEY 描述不能为空")
        return normalized
