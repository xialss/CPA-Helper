import os
from pathlib import Path


def get_repo_root() -> Path:
    return Path(__file__).resolve().parents[3]


def get_data_dir() -> Path:
    configured = os.environ.get("CPA_HELPER_DATA_DIR")
    data_dir = Path(configured) if configured else get_repo_root() / "data"
    data_dir.mkdir(parents=True, exist_ok=True)
    return data_dir


def get_db_dir() -> Path:
    db_dir = get_data_dir() / "db"
    db_dir.mkdir(parents=True, exist_ok=True)
    return db_dir


def get_sqlite_path() -> Path:
    return get_db_dir() / "cpa_helper.sqlite3"


def get_frontend_dist_dir() -> Path:
    return get_repo_root() / "frontend" / "dist"
