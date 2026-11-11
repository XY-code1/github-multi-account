"""API 级集成测试（FastAPI TestClient）。"""
import pytest
from fastapi.testclient import TestClient

from app import config, store
from app.main import app


@pytest.fixture
def client():
    with TestClient(app) as c:
        yield c


def test_summary_is_lightweight(client):
    config.add_account("A", "u1", "ghp_fake")
    resp = client.get("/api/status")
    assert resp.status_code == 200
    account = resp.json()["accounts"][0]
    assert account["review_prs_count"] == 0
    assert account["repos"] == []
    assert account["follower_list"] == []


def test_csrf_blocks_cross_site_writes(client):
    resp = client.post("/api/snapshot/1", headers={"Sec-Fetch-Site": "cross-site"})
    assert resp.status_code == 403


def test_import_bad_structure_returns_400(client):
    resp = client.post("/api/accounts/import", json={"accounts": "bad"})
    assert resp.status_code == 400
    assert "accounts" in resp.json()["detail"]


def test_history_limit_is_clamped(client):
    config.add_account("A", "limit-u", "ghp_fake")
    resp = client.get("/api/history/1?limit=-1")
    assert resp.status_code == 200
    assert resp.json()["history"] == []


def test_delete_account_cleans_history(client):
    config.add_account("A", "del-u", "ghp_x")
    store.save_snapshot("del-u", {"followers": 5}, force=True)
    assert store.get_stats("del-u")["count"] == 1

    resp = client.delete("/api/accounts/1")
    assert resp.status_code == 200
    assert store.get_stats("del-u")["count"] == 0


def test_missing_account_returns_404(client):
    assert client.get("/api/status/999").status_code == 404
    assert client.post("/api/snapshot/999").status_code == 404
    assert client.delete("/api/accounts/999").status_code == 404
