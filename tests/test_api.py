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


def test_dashboard_tasks_filter_params(client):
    from app import db

    bad = config.add_account("坏号", "bad", "ghp_bad")
    db.upsert_summary(bad["id"], "bad", {"status": "invalid", "label": "坏号", "error": "Token 无效"})
    db.upsert_sync_state(bad["id"], "bad", last_status="invalid")
    good = config.add_account("好号", "good", "ghp_good")
    db.upsert_summary(good["id"], "good", {"status": "active", "label": "好号", "review_prs_count": 3})
    db.upsert_sync_state(good["id"], "good", last_status="active")

    r = client.get("/api/dashboard/tasks?priority=high")
    assert r.status_code == 200
    assert r.json()["total"] == 1
    assert r.json()["tasks"][0]["type"] == "credential"

    r = client.get("/api/dashboard/tasks?type=review_pr")
    assert r.json()["total"] == 1
    assert r.json()["tasks"][0]["account_label"] == "好号"

    r = client.get(f"/api/dashboard/tasks?accountId={good['id']}")
    assert r.json()["total"] == 1
    assert r.json()["tasks"][0]["account_id"] == good["id"]

    assert client.get("/api/dashboard/tasks").json()["total"] == 2


def test_batch_operations(client):
    from app import db

    a1 = config.add_account("A", "batch-u1", "ghp_1", group="工作")
    a2 = config.add_account("B", "batch-u2", "ghp_2", group="个人")
    ids = [a1["id"], a2["id"]]

    # 刻意不预先创建 sync_state：暂停操作应自建状态行
    r = client.post("/api/accounts/batch-toggle-sync", json={"ids": ids, "enabled": False})
    assert r.status_code == 200 and r.json()["updated"] == 2
    for aid in ids:
        state = db.get_sync_state(aid)
        assert state is not None and state["sync_enabled"] == 0

    r = client.post("/api/accounts/batch-tag", json={"ids": ids, "tags": ["核心", "ci"]})
    assert r.status_code == 200 and r.json()["updated"] == 2
    for acc in config.load_accounts():
        assert set(acc["tags"]) == {"核心", "ci"}

    r = client.post("/api/accounts/batch-group", json={"ids": ids, "group": "测试"})
    assert r.status_code == 200 and r.json()["updated"] == 2
    assert all(acc["group"] == "测试" for acc in config.load_accounts())

    # 幂等：重复打标签不应重复
    client.post("/api/accounts/batch-tag", json={"ids": [a1["id"]], "tags": ["核心"]})
    assert set(config.load_accounts()[0]["tags"]) == {"核心", "ci"}

    assert client.post("/api/accounts/batch-tag", json={"ids": ids, "tags": []}).status_code == 400
    assert client.post("/api/accounts/batch-group", json={"ids": ids, "group": ""}).status_code == 400
    assert client.post("/api/accounts/batch-toggle-sync", json={"ids": []}).status_code == 400


def test_prune_audit():
    """审计日志只增不减，需要按条数与保留期清理。"""
    from app import db

    for i in range(10):
        db.audit("test.action", None, "", f"第 {i} 条")
    assert len(db.list_audit(100)) == 10

    assert db.prune_audit(limit=5, max_days=90) == 5
    assert len(db.list_audit(100)) == 5

    # 保留期外的一律清掉
    assert db.prune_audit(limit=100, max_days=-1) == 5
    assert db.list_audit(100) == []
