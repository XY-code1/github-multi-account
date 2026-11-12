"""账号列表服务端查询测试：分页 / 搜索 / 筛选 / 排序。"""
from __future__ import annotations

import pytest

from app import config, db, store
from app.query import query_accounts


@pytest.fixture
def many_accounts(tmp_path, monkeypatch):
    cfg = tmp_path / "config"
    data = tmp_path / "data"
    cfg.mkdir(exist_ok=True)
    data.mkdir(exist_ok=True)
    monkeypatch.setattr(config, "CONFIG_DIR", cfg)
    monkeypatch.setattr(config, "ACCOUNTS_FILE", cfg / "accounts.yaml")
    monkeypatch.setattr(config, "_cache", {"mtime": None, "accounts": None})
    monkeypatch.setattr(db, "DATA_DIR", data)
    monkeypatch.setattr(db, "DB_FILE", data / "dashboard.db")
    monkeypatch.setattr(db, "_lock", __import__("threading").RLock())
    monkeypatch.setattr(store, "DATA_DIR", data)
    monkeypatch.setattr(store, "DB_FILE", data / "dashboard.db")
    store.init_db()

    accounts = []
    for i in range(100):
        accounts.append({
            "id": i + 1,
            "label": f"账号{i + 1}",
            "username": f"user{i + 1}",
            "token": f"ghp_fake{i:04d}",
            "group": ["工作", "个人", "测试"][i % 3],
            "type": ["personal", "bot", "org"][i % 3],
            "alias": f"U{i + 1}",
            "note": f"第{i + 1}个测试账号",
            "tags": [["核心"], ["自动化", "ci"], ["实验"]][i % 3],
        })
    config.save_accounts(accounts)
    loaded = config.load_accounts()
    db.rebuild_meta(loaded)
    for a in loaded:
        db.upsert_summary(
            a["id"], a["username"],
            {
                "status": "invalid" if a["id"] % 10 == 0 else "active",
                "label": a["label"], "type": a["type"],
                "group_name": a["group"], "tags": a["tags"],
                "followers_count": a["id"] * 10, "following_count": a["id"],
                "public_repos": a["id"] % 20, "review_prs_count": a["id"] % 30,
                "my_prs_count": a["id"] % 10, "assigned_issues_count": a["id"] % 20,
                "total_stars": a["id"] * 100, "events": [], "error": None,
            },
        )
        db.upsert_sync_state(a["id"], a["username"], last_sync_at="2026-01-01T00:00:00+00:00")
        db.upsert_rate_limit(a["id"], a["username"], 5000 - a["id"], 5000, None)
    return loaded


def test_pagination(many_accounts):
    r = query_accounts(page=1, page_size=50)
    assert r["total"] == 100
    assert len(r["items"]) == 50
    assert r["total_pages"] == 2
    r2 = query_accounts(page=2, page_size=50)
    assert len(r2["items"]) == 50
    assert r2["items"][0]["id"] != r["items"][0]["id"]


def test_page_size_options(many_accounts):
    for size in (25, 50, 100):
        r = query_accounts(page=1, page_size=size)
        assert len(r["items"]) == size


def test_search_username(many_accounts):
    assert query_accounts(query="user1", page_size=5)["total"] == 12  # user1, user10..user19, user100
    assert query_accounts(query="user100", page_size=5)["total"] == 1


def test_search_alias_and_note(many_accounts):
    assert query_accounts(query="U100", page_size=5)["total"] == 1
    assert query_accounts(query="测试账号", page_size=5)["total"] == 100


def test_filter_status_and_type(many_accounts):
    assert query_accounts(status="invalid", page_size=5)["total"] == 10
    assert query_accounts(status="invalid", type_="bot", page_size=5)["total"] == 3


def test_filter_group_and_tags(many_accounts):
    assert query_accounts(group="工作", page_size=5)["total"] == 34
    assert query_accounts(tags="核心", page_size=5)["total"] == 34
    assert query_accounts(tags="ci", page_size=5)["total"] == 33


def test_sort(many_accounts):
    by_todos = query_accounts(sort="todos", order="desc", page_size=3)["items"]
    assert by_todos[0]["todos"] >= by_todos[1]["todos"] >= by_todos[2]["todos"]
    by_quota = query_accounts(sort="quota", order="asc", page_size=3)["items"]
    assert by_quota[0]["remaining"] <= by_quota[1]["remaining"] <= by_quota[2]["remaining"]
    by_name = query_accounts(sort="username", order="asc", page_size=3)["items"]
    names = [a["username"] for a in by_name]
    assert names == sorted(names)


def test_groups_and_tags_exposed(many_accounts):
    r = query_accounts(page=1, page_size=1)
    assert set(r["groups"]) == {"工作", "个人", "测试"}
    assert set(r["tags"]) == {"核心", "自动化", "ci", "实验"}


def test_empty_result(many_accounts):
    r = query_accounts(query="不存在的用户名")
    assert r["total"] == 0
    assert r["items"] == []


def test_sync_failed_flagging(many_accounts):
    """活跃账号连续失败 3 次以上应标记为同步失败。"""
    db.upsert_sync_state(
        1, "user1", last_sync_at="2026-01-01T00:00:00+00:00",
        last_status="active", consecutive_failures=3,
    )
    r = query_accounts(status="sync_failed", page_size=5)
    assert r["total"] == 1
    assert r["items"][0]["id"] == 1


def test_invalid_status_not_overridden_by_failures(many_accounts):
    """凭据失效的账号即使连续失败也应保持 invalid，而不是 sync_failed。"""
    db.upsert_sync_state(
        10, "user10", last_status="invalid", consecutive_failures=5,
    )
    r = query_accounts(status="invalid", page_size=50)
    assert 10 in [i["id"] for i in r["items"]]
    assert query_accounts(status="sync_failed", page_size=5)["total"] == 0
