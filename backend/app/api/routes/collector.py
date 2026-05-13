from fastapi import APIRouter

from app.api.deps import ReadyAdminDep, SessionDep
from app.schemas.collector import CollectorStatusResponse
from app.services.collector_service import get_collector_status

router = APIRouter(prefix="/collector", tags=["collector"])


@router.get("/status", response_model=CollectorStatusResponse)
def status(
    session: SessionDep,
    user: ReadyAdminDep,
) -> CollectorStatusResponse:
    return get_collector_status(session)
