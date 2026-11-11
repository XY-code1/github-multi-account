"""服务层测试：容错、汇总/详情分离、并发限流。"""
import asyncio

import pytest

from app import service


class FakeClient:
    """记录调用；get_following 恒定失败，用于验证容错。"""

    calls: list[str] = []

    def __init__(self, token, timeout=12.0):
        pass

    async def aclose(self):
        pass

    async def get_user(self):
        FakeClient.calls.append("get_user")
        return {"login": "u", "followers": 10, "following": 5, "public_repos": 3}

    async def review_requested_prs(self, login):
        FakeClient.calls.append("review")
        return 42, [{"title": "PR1"}]

    async def my_open_prs(self, login):
        FakeClient.calls.append("mypr")
        return 0, []

    async def assigned_issues(self, login):
        FakeClient.calls.append("issues")
        return 0, []

    async def received_events(self, login):
        FakeClient.calls.append("events")
        return []

    async def user_repos_all(self, login):
        FakeClient.calls.append("repos")
        return []

    async def get_followers(self, login):
        FakeClient.calls.append("followers")
        return []

    async def get_following(self, login):
        FakeClient.calls.append("following")
        raise RuntimeError("模拟网络抖动")

    async def rate_limit(self):
        FakeClient.calls.append("rate_limit")
        return {}


class SlowClient:
    """用于验证并发上限。"""

    current = 0
    max_concurrent = 0

    def __init__(self, token, timeout=12.0):
        pass

    async def aclose(self):
        pass

    async def get_user(self):
        SlowClient.current += 1
        SlowClient.max_concurrent = max(SlowClient.max_concurrent, SlowClient.current)
        await asyncio.sleep(0.05)
        SlowClient.current -= 1
        return {"login": "u", "followers": 1, "following": 1, "public_repos": 1}

    async def review_requested_prs(self, login):
        return []

    async def my_open_prs(self, login):
        return []

    async def assigned_issues(self, login):
        return []


@pytest.fixture
def fake_client(monkeypatch):
    FakeClient.calls = []
    monkeypatch.setattr(service, "GitHubClient", FakeClient)
    return FakeClient


@pytest.fixture
def slow_client(monkeypatch):
    SlowClient.current = 0
    SlowClient.max_concurrent = 0
    monkeypatch.setattr(service, "GitHubClient", SlowClient)
    return SlowClient


def run(coro):
    return asyncio.run(coro)


ACCOUNT = {"id": 1, "label": "L", "username": "u", "token": "t"}


def test_detail_survives_partial_failure(fake_client):
    result = run(service.fetch_account_detail(ACCOUNT))
    assert result["status"] == "active"
    assert result["warnings"] == ["关注"]
    assert len(result["review_prs"]) == 1
    assert result["following_list"] == []


def test_summary_is_lightweight(fake_client):
    result = run(service.fetch_account_summary(ACCOUNT))
    assert result["status"] == "active"
    assert result["review_prs_count"] == 42  # 来自 total_count，不受分页截断
    assert len(result["review_prs"]) == 1  # 当前页条目数
    assert result["assigned_issues_count"] == 0
    heavy = {"repos", "followers", "following", "events", "rate_limit"}
    assert not heavy & set(fake_client.calls), fake_client.calls


def test_summary_also_survives_partial_failure(fake_client):
    result = run(service.fetch_account_summary(ACCOUNT))
    assert result["status"] == "active"
    assert result["review_prs_count"] == 42


def test_detail_calls_more_than_summary(fake_client):
    run(service.fetch_account_detail(ACCOUNT))
    detail_calls = len(fake_client.calls)
    fake_client.calls = []
    run(service.fetch_account_summary(ACCOUNT))
    summary_calls = len(fake_client.calls)
    assert summary_calls < detail_calls


def test_concurrency_is_limited(slow_client):
    accounts = [
        {"id": i, "label": f"L{i}", "username": f"u{i}", "token": "t"} for i in range(6)
    ]
    results = run(service.fetch_all_summaries(accounts))
    assert len(results) == 6
    assert slow_client.max_concurrent <= service.MAX_CONCURRENT_ACCOUNTS


def test_force_record_ignores_throttle():
    class OkClient(FakeClient):
        async def get_following(self, login):
            return []

    import app.service as svc

    original = svc.GitHubClient
    svc.GitHubClient = OkClient
    try:
        first = run(svc.fetch_account_detail({"id": 9, "label": "L", "username": "force-u", "token": "t"}))
        assert first["status"] == "active"
    finally:
        svc.GitHubClient = original
