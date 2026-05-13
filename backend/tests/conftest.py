from collections.abc import Generator

import pytest
from fastapi.testclient import TestClient

from app.db.session import reset_engine_for_tests
from app.main import app


@pytest.fixture()
def client(tmp_path, monkeypatch: pytest.MonkeyPatch) -> Generator[TestClient, None, None]:
    monkeypatch.setenv("CPA_HELPER_DATA_DIR", str(tmp_path))
    reset_engine_for_tests()
    with TestClient(app) as test_client:
        yield test_client
    reset_engine_for_tests()

