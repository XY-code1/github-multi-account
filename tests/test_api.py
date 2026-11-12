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


def test_create_account_keeps_metadata(client):
    resp = client.post(
        "/api/accounts",
        json={
            "label": "工作号",
            "username": "meta-u",
            "token": "ghp_meta",
            "group": "工作",
            "type": "bot",
            "tags": ["核心", "ci"],
            "note": "元数据测试",
            "alias": "M1",
        },
    )
    assert resp.status_code == 200

    listed = client.get("/api/accounts?query=meta-u").json()
    assert listed["total"] == 1
    item = listed["items"][0]
    assert item["type"] == "bot"
    assert item["group"] == "工作"
    assert item["tags"] == ["核心", "ci"]
    assert item["note"] == "元数据测试"
    assert item["alias"] == "M1"
    assert "ghp_meta" not in resp.text


def test_account_info_export_excludes_credentials(client):
    config.add_account("A", "export-u", "ghp_secret", group="g", type_="org")
    resp = client.get("/api/accounts/export")
    assert resp.status_code == 200
    data = resp.json()
    assert len(data["accounts"]) == 1
    assert "token" not in data["accounts"][0]
    assert data["accounts"][0]["username"] == "export-u"
    assert data["accounts"][0]["type"] == "org"


def test_credential_export_is_encrypted_with_password(client):
    config.add_account("A", "cred-u", "ghp_secret2")
    resp = client.post("/api/accounts/export", json={"password": "pw"})
    assert resp.status_code == 200
    body = resp.text
    assert "encrypted" in resp.json()
    assert "ghp_secret2" not in body
