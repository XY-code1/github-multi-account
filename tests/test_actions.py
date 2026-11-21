"""Star / Follow / 创建 PR 的客户端与用例测试。

全部使用 httpx.MockTransport，不访问真实 GitHub。
"""
from __future__ import annotations

import asyncio

import httpx
import pytest

from app import actions
from app.github import QUOTA_RESERVE, GitHubClient, GitHubError, _rate_limit_hint


def make_client(handler, calls: list | None = None) -> GitHubClient:
    """构造一个走 MockTransport 的客户端，可选记录请求。"""

    def dispatch(request: httpx.Request) -> httpx.Response:
        if calls is not None:
            calls.append((request.method, request.url.path))
        return handler(request)

    return GitHubClient("ghp_test", transport=httpx.MockTransport(dispatch))


def rate_limit(remaining: int):
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/rate_limit"
        return httpx.Response(
            200,
            json={"resources": {"core": {"limit": 5000, "remaining": remaining, "reset": 0}}},
        )

    return handler


def json_response(status: int, payload) -> httpx.Response:
    return httpx.Response(status, json=payload)


def test_check_quota_rejects_low_remaining():
    calls: list = []
    client = make_client(rate_limit(10), calls)
    with pytest.raises(GitHubError) as exc:
        asyncio.run(client.check_quota())
    assert exc.value.status == 403
    assert "配额不足" in exc.value.message
    # 配额不足时不应继续发起写请求
    assert calls == [("GET", "/rate_limit")]


def test_star_is_idempotent():
    calls: list = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.method == "GET" and request.url.path == "/user/starred/o/r":
            return httpx.Response(204)
        calls.append((request.method, request.url.path))
        return httpx.Response(500)

    client = make_client(handler, calls)
    out = asyncio.run(client.star_repo("o", "r"))
    assert out == {"starred": True, "already": True}
    assert ("PUT", "/user/starred/o/r") not in calls


def test_star_creates_when_missing():
    calls: list = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.method == "GET" and request.url.path == "/user/starred/o/r":
            return httpx.Response(404)
        if request.method == "PUT" and request.url.path == "/user/starred/o/r":
            calls.append((request.method, request.url.path))
            return httpx.Response(204)
        return httpx.Response(500)

    client = make_client(handler)
    out = asyncio.run(client.star_repo("o", "r"))
    assert out == {"starred": True, "already": False}
    assert ("PUT", "/user/starred/o/r") in calls


def test_follow_is_idempotent():
    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.method == "GET" and request.url.path == "/user/following/alice":
            return httpx.Response(204)
        return httpx.Response(500)

    client = make_client(handler)
    assert asyncio.run(client.follow_user("alice")) == {"following": True, "already": True}


def test_rate_limit_hint_uses_retry_after():
    resp = httpx.Response(403, headers={"Retry-After": "30", "X-RateLimit-Remaining": "3"})
    assert "30" in _rate_limit_hint(resp)


def test_action_star_repo_uses_account_token(monkeypatch):
    calls: list = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.method == "GET" and request.url.path == "/user/starred/o/r":
            return httpx.Response(404)
        calls.append((request.method, request.url.path))
        return httpx.Response(204)

    monkeypatch.setattr(actions, "GitHubClient", lambda token, **kw: make_client(handler, calls))
    account = {"id": 1, "username": "u1", "token": "ghp_fake"}
    out = asyncio.run(actions.star_repo(account, "o", "r"))
    assert out["ok"] and out["account"] == "u1" and out["already"] is False
    assert ("PUT", "/user/starred/o/r") in calls


def test_action_create_pr_existing(monkeypatch):
    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/user":
            return json_response(200, {"login": "u1"})
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.url.path == "/repos/o/r":
            return json_response(200, {"default_branch": "main"})
        if request.method == "GET" and request.url.path == "/repos/o/r/pulls":
            return json_response(200, [{"number": 3, "html_url": "u3", "title": "old"}])
        return httpx.Response(500)

    monkeypatch.setattr(actions, "GitHubClient", lambda token, **kw: make_client(handler))
    account = {"id": 1, "username": "u1", "token": "ghp_fake"}
    out = asyncio.run(actions.create_pr(account, "o/r", "标题", head="feature"))
    assert out["action"] == "existing"
    assert out["url"] == "u3"


def test_action_create_pr_created(monkeypatch):
    posted: list = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/user":
            return json_response(200, {"login": "u1"})
        if request.url.path == "/rate_limit":
            return rate_limit(5000)(request)
        if request.url.path == "/repos/o/r":
            return json_response(200, {"default_branch": "main"})
        if request.method == "GET" and request.url.path == "/repos/o/r/pulls":
            return json_response(200, [])
        if request.method == "POST" and request.url.path == "/repos/o/r/pulls":
            posted.append(request)
            return json_response(201, {"number": 7, "html_url": "u7", "title": "t"})
        return httpx.Response(500)

    monkeypatch.setattr(actions, "GitHubClient", lambda token, **kw: make_client(handler))
    account = {"id": 1, "username": "u1", "token": "ghp_fake"}
    out = asyncio.run(actions.create_pr(account, "o/r", "标题", head="feature"))
    assert out["action"] == "created"
    assert out["url"] == "u7"
    assert len(posted) == 1


def test_put_file_keeps_path_segments():
    import asyncio

    paths: list = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/repos/o/r/contents/docs/a.md":
            paths.append(request.url.path)
            if request.method == "GET":
                return httpx.Response(404)
            return httpx.Response(201, json={})
        return httpx.Response(500)

    client = make_client(handler)
    out = asyncio.run(client.put_file("o", "r", "docs/a.md", "hi", "main", "msg"))
    assert out["committed"] is True
    assert paths == ["/repos/o/r/contents/docs/a.md"] * 2


def test_action_create_pr_requires_file_pair(monkeypatch):
    monkeypatch.setattr(actions, "GitHubClient", lambda token, **kw: make_client(rate_limit(5000)))
    account = {"id": 1, "username": "u1", "token": "ghp_fake"}
    with pytest.raises(actions.ActionError):
        asyncio.run(actions.create_pr(account, "o/r", "标题", head="f", file_path="a.md"))


def test_split_repo_rejects_bad_format():
    with pytest.raises(actions.ActionError):
        actions.split_repo("bad")


def test_quota_reserve_matches_go():
    """双版本阈值应保持一致（Go: github.QuotaReserve）。"""
    assert QUOTA_RESERVE == 50
