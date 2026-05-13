from fastapi import APIRouter

from app.api.deps import ReadyAdminDep
from app.schemas.codex_keeper import (
    CodexKeeperAccountsResponse,
    CodexKeeperActionResponse,
    CodexKeeperBulkDeleteRequest,
    CodexKeeperBulkDeleteResponse,
    CodexKeeperCronPreviewRequest,
    CodexKeeperCronPreviewResponse,
    CodexKeeperPriorityUpdateRequest,
    CodexKeeperSettingsResponse,
    CodexKeeperSettingsUpdateRequest,
    CodexKeeperStatusResponse,
)
from app.services.codex_keeper_service import (
    bulk_delete_keeper_accounts,
    codex_keeper_runner,
    delete_keeper_account,
    disable_keeper_account,
    enable_keeper_account,
    get_keeper_settings,
    list_keeper_accounts,
    preview_keeper_cron,
    update_keeper_account_priority,
    update_keeper_settings,
)

router = APIRouter(prefix="/codex-keeper", tags=["codex-keeper"])


@router.get("/settings", response_model=CodexKeeperSettingsResponse)
def get_settings(user: ReadyAdminDep) -> CodexKeeperSettingsResponse:
    return get_keeper_settings()


@router.put("/settings", response_model=CodexKeeperSettingsResponse)
def put_settings(
    payload: CodexKeeperSettingsUpdateRequest,
    user: ReadyAdminDep,
) -> CodexKeeperSettingsResponse:
    return update_keeper_settings(payload)


@router.post("/schedule/preview", response_model=CodexKeeperCronPreviewResponse)
def preview_schedule(
    payload: CodexKeeperCronPreviewRequest,
    user: ReadyAdminDep,
) -> CodexKeeperCronPreviewResponse:
    return preview_keeper_cron(payload.schedule_cron)


@router.get("/status", response_model=CodexKeeperStatusResponse)
def get_status(user: ReadyAdminDep) -> CodexKeeperStatusResponse:
    return codex_keeper_runner.status()


@router.get("/accounts", response_model=CodexKeeperAccountsResponse)
def get_accounts(user: ReadyAdminDep) -> CodexKeeperAccountsResponse:
    return list_keeper_accounts()


@router.post("/run-once", response_model=CodexKeeperActionResponse)
def run_once(user: ReadyAdminDep) -> CodexKeeperActionResponse:
    codex_keeper_runner.start_once()
    return CodexKeeperActionResponse(status="started")


@router.post("/start", response_model=CodexKeeperActionResponse)
def start(user: ReadyAdminDep) -> CodexKeeperActionResponse:
    codex_keeper_runner.start_daemon()
    return CodexKeeperActionResponse(status="started")


@router.post("/stop", response_model=CodexKeeperActionResponse)
def stop(user: ReadyAdminDep) -> CodexKeeperActionResponse:
    codex_keeper_runner.stop()
    return CodexKeeperActionResponse(status="stopping")


@router.post("/logs/clear", response_model=CodexKeeperActionResponse)
def clear_logs(user: ReadyAdminDep) -> CodexKeeperActionResponse:
    codex_keeper_runner.clear_logs()
    return CodexKeeperActionResponse(status="cleared")


@router.post("/accounts/bulk-delete", response_model=CodexKeeperBulkDeleteResponse)
def bulk_delete_accounts(
    payload: CodexKeeperBulkDeleteRequest,
    user: ReadyAdminDep,
) -> CodexKeeperBulkDeleteResponse:
    return bulk_delete_keeper_accounts(payload.auth_names)


@router.post("/accounts/{auth_name}/enable", response_model=CodexKeeperActionResponse)
def enable_account(auth_name: str, user: ReadyAdminDep) -> CodexKeeperActionResponse:
    enable_keeper_account(auth_name)
    return CodexKeeperActionResponse(status="enabled")


@router.post("/accounts/{auth_name}/disable", response_model=CodexKeeperActionResponse)
def disable_account(auth_name: str, user: ReadyAdminDep) -> CodexKeeperActionResponse:
    disable_keeper_account(auth_name)
    return CodexKeeperActionResponse(status="disabled")


@router.delete("/accounts/{auth_name}", response_model=CodexKeeperActionResponse)
def delete_account(auth_name: str, user: ReadyAdminDep) -> CodexKeeperActionResponse:
    delete_keeper_account(auth_name)
    return CodexKeeperActionResponse(status="deleted")


@router.patch("/accounts/{auth_name}/priority", response_model=CodexKeeperActionResponse)
def update_priority(
    auth_name: str,
    payload: CodexKeeperPriorityUpdateRequest,
    user: ReadyAdminDep,
) -> CodexKeeperActionResponse:
    update_keeper_account_priority(auth_name, payload.priority)
    return CodexKeeperActionResponse(status="updated")
