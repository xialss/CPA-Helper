from pathlib import Path

from fastapi.testclient import TestClient

from app import main


def test_spa_serves_frontend_dist_root_static_files(
    client: TestClient,
    tmp_path: Path,
    monkeypatch,
) -> None:
    frontend_dist = tmp_path / "dist"
    frontend_dist.mkdir()
    logo_bytes = b"\x89PNG\r\n\x1a\nlogo"
    (frontend_dist / "logo.png").write_bytes(logo_bytes)
    (frontend_dist / "index.html").write_text("<!doctype html>", encoding="utf-8")
    monkeypatch.setattr(main, "frontend_dist", frontend_dist)

    response = client.get("/logo.png")

    assert response.status_code == 200
    assert response.headers["content-type"] == "image/png"
    assert response.content == logo_bytes


def test_spa_still_falls_back_to_index_for_routes(
    client: TestClient,
    tmp_path: Path,
    monkeypatch,
) -> None:
    frontend_dist = tmp_path / "dist"
    frontend_dist.mkdir()
    (frontend_dist / "index.html").write_text("<!doctype html>", encoding="utf-8")
    monkeypatch.setattr(main, "frontend_dist", frontend_dist)

    response = client.get("/admin/usage")

    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/html")
    assert response.text == "<!doctype html>"
