from datetime import datetime

from pydantic import BaseModel, Field, field_validator


class CodexKeeperPriorityRule(BaseModel):
    account_type: str = Field(min_length=1, max_length=80)
    priority: int = Field(ge=0, le=20)

    @field_validator("account_type")
    @classmethod
    def normalize_account_type(cls, value: str) -> str:
        normalized = value.strip().lower()
        if not normalized:
            raise ValueError("账号类型不能为空")
        return normalized


class CodexKeeperSettingsResponse(BaseModel):
    cliaproxy_url: str
    management_key_set: bool
    schedule_cron: str
    next_run_times: list[datetime]
    quota_threshold: int
    usage_timeout_seconds: int
    cpa_timeout_seconds: int
    max_retries: int
    worker_threads: int
    dry_run: bool
    auto_start_daemon: bool
    priority_rules: list[CodexKeeperPriorityRule]


class CodexKeeperSettingsUpdateRequest(BaseModel):
    schedule_cron: str | None = Field(default=None, min_length=1, max_length=120)
    quota_threshold: int | None = Field(default=None, ge=0, le=100)
    usage_timeout_seconds: int | None = Field(default=None, ge=1)
    cpa_timeout_seconds: int | None = Field(default=None, ge=1)
    max_retries: int | None = Field(default=None, ge=0, le=5)
    worker_threads: int | None = Field(default=None, ge=1, le=64)
    dry_run: bool | None = None
    auto_start_daemon: bool | None = None
    priority_rules: list[CodexKeeperPriorityRule] | None = None


class CodexKeeperCronPreviewRequest(BaseModel):
    schedule_cron: str = Field(min_length=1, max_length=120)


class CodexKeeperCronPreviewResponse(BaseModel):
    schedule_cron: str
    next_run_times: list[datetime]


class CodexKeeperStatsResponse(BaseModel):
    total: int = 0
    healthy: int = 0
    status_disabled: int = 0
    status_enabled: int = 0
    priority_degraded: int = 0
    priority_restored: int = 0
    skipped: int = 0
    network_error: int = 0


class CodexKeeperStatusResponse(BaseModel):
    running: bool
    state: str
    detail: str
    mode: str | None
    last_started_at: datetime | None
    last_finished_at: datetime | None
    stats: CodexKeeperStatsResponse
    logs: list[str]


class CodexKeeperActionResponse(BaseModel):
    status: str


class CodexKeeperBulkDeleteRequest(BaseModel):
    auth_names: list[str] = Field(min_length=1)

    @field_validator("auth_names")
    @classmethod
    def normalize_auth_names(cls, value: list[str]) -> list[str]:
        seen: set[str] = set()
        auth_names: list[str] = []
        for raw_name in value:
            name = raw_name.strip()
            if not name:
                raise ValueError("账号名称不能为空")
            if name in seen:
                continue
            seen.add(name)
            auth_names.append(name)
        return auth_names


class CodexKeeperBulkDeleteFailure(BaseModel):
    name: str
    message: str


class CodexKeeperBulkDeleteResponse(BaseModel):
    status: str
    deleted: list[str]
    failed: list[CodexKeeperBulkDeleteFailure]


class CodexKeeperPriorityUpdateRequest(BaseModel):
    priority: int


class CodexKeeperAccount(BaseModel):
    name: str
    email: str | None
    account_type: str | None
    disabled: bool
    priority: int | None
    primary_used_percent: int | None
    secondary_used_percent: int | None
    primary_reset_at: datetime | None
    secondary_reset_at: datetime | None
    quota_threshold: int | None
    last_status_code: int | None
    last_error: str | None
    latest_action: str | None
    last_checked_at: datetime | None
    last_healthy_at: datetime | None


class CodexKeeperAccountsResponse(BaseModel):
    items: list[CodexKeeperAccount]
