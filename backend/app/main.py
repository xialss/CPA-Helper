from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

from app.api.routes import (
    account_models,
    api_keys,
    auth,
    codex_keeper,
    collector,
    model_prices,
    settings,
    usage,
    users,
)
from app.core.config import load_config
from app.core.errors import AppError
from app.core.exception_handlers import app_error_handler
from app.core.logging import configure_logging
from app.core.paths import get_frontend_dist_dir
from app.db.session import init_db
from app.services.codex_keeper_service import codex_keeper_runner
from app.services.collector_service import collector_runner


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncIterator[None]:
    configure_logging()
    init_db()
    load_config()
    collector_runner.start()
    codex_keeper_runner.load_persisted_state()
    codex_keeper_runner.start_auto_if_configured()
    try:
        yield
    finally:
        codex_keeper_runner.stop()
        await collector_runner.stop()


app = FastAPI(title="CPA Helper", lifespan=lifespan)
app.add_exception_handler(AppError, app_error_handler)
app.add_middleware(
    CORSMiddleware,
    allow_origins=[
        "http://127.0.0.1:5173",
        "http://localhost:5173",
    ],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.include_router(auth.router, prefix="/api")
app.include_router(settings.router, prefix="/api")
app.include_router(collector.router, prefix="/api")
app.include_router(codex_keeper.router, prefix="/api")
app.include_router(usage.router, prefix="/api")
app.include_router(model_prices.router, prefix="/api")
app.include_router(api_keys.router, prefix="/api")
app.include_router(account_models.router, prefix="/api")
app.include_router(users.router, prefix="/api")


@app.get("/api/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


frontend_dist = get_frontend_dist_dir()
assets_dir = frontend_dist / "assets"
if assets_dir.exists():
    app.mount("/assets", StaticFiles(directory=assets_dir), name="assets")


def _frontend_static_file(path: str) -> Path | None:
    try:
        static_path = (frontend_dist / path).resolve()
        static_path.relative_to(frontend_dist.resolve())
    except ValueError:
        return None
    if static_path.is_file():
        return static_path
    return None


@app.get("/{path:path}", include_in_schema=False, response_model=None)
def spa(path: str) -> FileResponse | dict[str, str]:
    if path.startswith("api/"):
        raise HTTPException(status_code=404, detail="Not Found")
    static_path = _frontend_static_file(path)
    if static_path is not None:
        return FileResponse(static_path)
    index_path = frontend_dist / "index.html"
    if index_path.exists():
        return FileResponse(index_path)
    return {"status": "frontend_not_built"}
