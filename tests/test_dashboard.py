"""全局聚合测试：账号总览、统一待办优先级、失败隔离。"""
from __future__ import annotations

import pytest

from app import config, dashboard, db, store, sync


@pytest.fixture
def populated(tmp_path, monkeypatch):
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

    config.add_account("坏号", "bad", "ghp_bad", group="工作", type_="personal")
    config.add_account("好号A", "good1", "ghp_1", group="工作", type_="personal")
    config.add_account("好号B", "good2", "ghp_2", group="个人", type_="bot")
    accounts = config.load_accounts()
    db.rebuild_meta(accounts)

    # 坏号：凭据失效 + 连续失败
    db.upsert_summary(1, "bad", {"status": "invalid", "label": "坏号", "error": "Token 无效",
                                  "review_prs_count": 0, "assigned_issues_count": 0, "events": []})
    db.upsert_sync_state(1, "bad", last_sync_at="2026-01-01T00:00:00+00:00",
                         last_status="invalid", last_error="Token 无效", consecutive_failures=4)
    # 好号A：有 PR 和 Issue
    db.upsert_summary(2, "good1", {"status": "active", "label": "好号A", "review_prs_count": 5,
                                    "my_prs_count": 2, "assigned_issues_count": 3,
                                    "followers_count": 100, "total_stars": 500, "events": [
                                        {"type": "PushEvent", "actor": {"login": "u"},
                                         "repo": {"name": "o/r"}, "payload": {"action": "pushed"},
                                         "created_at": "2026-01-02T00:00:00Z"}]})
    db.upsert_sync_state(2, "good1", last_sync_at="2026-01-02T00:00:00+00:00", last_status="active")
    db.upsert_rate_limit(2, "good1", 10, 5000, None)
    # 好号B：正常
    db.upsert_summary(3, "good2", {"status": "active", "label": "好号B", "review_prs_count": 0,
                                    "assigned_issues_count": 0, "events": []})
    db.upsert_sync_state(3, "good2", last_sync_at="2026-01-02T00:00:00+00:00", last_status="active")
    return accounts


def test_summary_counts(populated):
    s = dashboard.build_summary()
    assert s["total"] == 3
    assert s["healthy"] == 2
    assert s["attention"] == 1
    assert s["credential_risk"] == 1
    assert s["quota_low"] == 1
    assert s["review_prs"] == 5
    assert s["issues"] == 3


def test_task_priority_order(populated):
    tasks = dashboard.build_tasks()
    assert len(tasks) >= 4
    # 高优先级排在最前
    assert tasks[0]["priority"] == "high"
    # 同优先级内稳定
    priorities = [t["priority"] for t in tasks]
    order = {"high": 0, "medium": 1, "low": 2}
    assert priorities == sorted(priorities, key=lambda p: order[p])
    # 坏号的凭据异常必须出现
    assert any(t["type"] == "credential" and t["account_label"] == "坏号" for t in tasks)
    # 好号A 的 PR / Issue 必须出现
    assert any(t["type"] == "review_pr" and t["account_label"] == "好号A" for t in tasks)
    assert any(t["type"] == "issue" and t["account_label"] == "好号A" for t in tasks)


def test_activities_aggregate(populated):
    acts = dashboard.build_activities(limit=10)
    assert len(acts) == 1
    assert acts[0]["repo"] == "o/r"
    assert acts[0]["account_label"] == "好号A"


def test_quota_capped_summary_and_task(populated):
    """剩余 <= 60 算异常：计入总览风险，并生成高优先级待办。"""
    db.upsert_rate_limit(2, "good1", 30, 5000, None)

    s = dashboard.build_summary()
    assert s["quota_capped"] == 1
    assert s["quota_low"] == 1

    tasks = dashboard.build_tasks()
    capped = [t for t in tasks if t["type"] == "quota_capped"]
    assert len(capped) == 1
    assert capped[0]["priority"] == "high"
    assert capped[0]["account_username"] == "good1"
    # 封顶的账号不再重复生成「配额不足」的中优先级待办
    assert not [t for t in tasks if t["type"] == "quota" and t["account_id"] == 2]


def test_task_includes_account_username(populated):
    """待办要带上具体账号身份，便于区分同名的不同账号。"""
    tasks = dashboard.build_tasks()
    cred = next(t for t in tasks if t["type"] == "credential")
    assert cred["account_username"] == "bad"
    review = next(t for t in tasks if t["type"] == "review_pr")
    assert review["account_username"] == "good1"


def test_activities_aggregate_duplicates(populated):
    """同一账号 + 同一仓库 + 同类型 + 同动作的重复活动应聚合并计数。"""
    events = [
        {
            "type": "PushEvent", "actor": {"login": "u", "avatar_url": ""},
            "repo": {"name": "o/r"}, "payload": {"action": "pushed"},
            "created_at": "2026-01-02T00:00:00Z",
        }
        for _ in range(3)
    ]
    db.upsert_summary(2, "good1", {"status": "active", "label": "好号A", "events": events})
    acts = dashboard.build_activities(limit=10)
    assert len(acts) == 1
    assert acts[0]["count"] == 3
    assert acts[0]["account_username"] == "good1"


def test_activities_keep_different_actions(populated):
    """动作不同不算重复，应各自保留。"""
    events = [
        {"type": "IssuesEvent", "actor": {"login": "u", "avatar_url": ""},
         "repo": {"name": "o/r"}, "payload": {"action": a},
         "created_at": "2026-01-02T00:00:00Z"}
        for a in ("opened", "closed")
    ]
    db.upsert_summary(2, "good1", {"status": "active", "label": "好号A", "events": events})
    acts = dashboard.build_activities(limit=10)
    assert len(acts) == 2
    assert all(a["count"] == 1 for a in acts)


def test_partial_failure_isolation(populated, monkeypatch):
    """单个账号失败不影响其他账号。"""
    import asyncio

    calls = {"n": 0}

    class FakeClient:
        def __init__(self, token, timeout=12.0):
            self.token = token
        async def aclose(self):
            pass
        async def get_user(self):
            calls["n"] += 1
            if self.token == "ghp_bad":
                raise RuntimeError("模拟：坏号失败")
            return {"login": "ok", "followers": 1, "following": 1, "public_repos": 1}
        async def review_requested_prs(self, login):
            return 0, []
        async def my_open_prs(self, login):
            return 0, []
        async def assigned_issues(self, login):
            return 0, []
        async def received_events(self, login):
            return []
        async def rate_limit(self):
            return {"resources": {"core": {"remaining": 100, "limit": 5000}}}

    monkeypatch.setattr(sync.service, "GitHubClient", FakeClient)
    accounts = populated
    engine = sync.SyncEngine()
    engine.configure({"concurrency": 3, "quota_floor": 0})
    result = asyncio.run(engine.sync_all())
    assert result["total"] == 3
    assert result["failed"] == 1
    assert result["done"] == 2


def test_backoff_scheduling(populated, monkeypatch):
    """连续失败后应写入退避时间。"""
    import asyncio
    from datetime import datetime

    class AlwaysFail:
        def __init__(self, token, timeout=12.0):
            pass
        async def aclose(self):
            pass
        async def get_user(self):
            raise RuntimeError("boom")
        async def review_requested_prs(self, login):
            return 0, []
        async def my_open_prs(self, login):
            return 0, []
        async def assigned_issues(self, login):
            return 0, []
        async def received_events(self, login):
            return []
        async def rate_limit(self):
            return {}

    monkeypatch.setattr(sync.service, "GitHubClient", AlwaysFail)
    engine = sync.SyncEngine()
    engine.configure({"concurrency": 1, "quota_floor": 0})
    asyncio.run(engine.sync_all())
    st = db.get_sync_state(1)
    assert st["consecutive_failures"] >= 1
    assert st["next_retry_at"] is not None


def test_token_never_in_api_payload(populated):
    """接口返回中不得出现明文 Token。"""
    import json

    s = dashboard.build_summary()
    tasks = dashboard.build_tasks()
    assert "ghp_" not in json.dumps(s)
    assert "ghp_" not in json.dumps(tasks)
