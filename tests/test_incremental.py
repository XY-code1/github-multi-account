"""增量同步测试：ETag 条件请求，304 命中时不重复拉取。"""
from __future__ import annotations

import asyncio

import httpx

from app import service


def make_transport():
    """模拟 GitHub：每个端点一个 ETag，带 If-None-Match 且匹配时返回 304。"""
    etags = {
        "/user": 'W/"user-etag"',
        "/search/issues": 'W/"search-etag"',
        "/users/u/received_events": 'W/"events-etag"',
    }
    counts = {"requests": 0, "conditional": 0}

    def handler(request: httpx.Request) -> httpx.Response:
        counts["requests"] += 1
        path = request.url.path
        etag = etags.get(path)
        if etag and request.headers.get("if-none-match") == etag:
            counts["conditional"] += 1
            return httpx.Response(304, headers={"etag": etag})
        if path == "/user":
            return httpx.Response(
                200, headers={"etag": etags["/user"]},
                json={"login": "u", "followers": 1, "following": 1, "public_repos": 1},
            )
        if path == "/search/issues":
            return httpx.Response(
                200, headers={"etag": etags["/search/issues"]},
                json={"total_count": 3, "items": [{"title": "x"}]},
            )
        if path == "/users/u/received_events":
            return httpx.Response(
                200, headers={"etag": etags["/users/u/received_events"]},
                json=[{"type": "PushEvent"}],
            )
        if path == "/rate_limit":
            return httpx.Response(200, json={"resources": {"core": {"remaining": 100, "limit": 5000}}})
        return httpx.Response(404, json={"message": "not found"})

    return httpx.MockTransport(handler), counts, etags


def test_conditional_request_skips_unchanged(monkeypatch):
    transport, counts, etags = make_transport()
    real_cls = service.GitHubClient
    monkeypatch.setattr(service, "GitHubClient", lambda token: real_cls(token, transport=transport))

    account = {"id": 1, "username": "u", "token": "t", "type": "personal", "group": "", "tags": []}

    # 首次：没有 ETag，全部端点真实请求（user + 3 search + events + rate_limit）
    first = asyncio.run(
        service.fetch_account_summary(account, include_events=True, etags={}, previous={})
    )
    assert counts["requests"] == 6
    assert counts["conditional"] == 0
    assert first["review_prs_count"] == 3
    assert first["events"] == [{"type": "PushEvent"}]

    # 模拟同步引擎写回 ETag
    stored = {
        "review_prs": etags["/search/issues"],
        "my_prs": etags["/search/issues"],
        "issues": etags["/search/issues"],
        "events": etags["/users/u/received_events"],
    }

    # 第二次：带 ETag，4 个端点命中 304，只有 /user 真正拉取
    second = asyncio.run(
        service.fetch_account_summary(account, include_events=True, etags=stored, previous=first)
    )
    assert counts["conditional"] == 4
    assert counts["requests"] == 12  # 只多了 1 次 /user + 1 次 rate_limit
    # 304 时保留上一次的结果，不能清空
    assert second["review_prs_count"] == 3
    assert second["review_prs"] == first["review_prs"]
    assert second["my_prs"] == first["my_prs"]
    assert second["assigned_issues"] == first["assigned_issues"]
    assert second["events"] == first["events"]


def test_etag_written_back(monkeypatch):
    """服务层要把新 ETag 写回传入的 etags 字典。"""
    transport, counts, etags = make_transport()
    real_cls = service.GitHubClient
    monkeypatch.setattr(service, "GitHubClient", lambda token: real_cls(token, transport=transport))

    account = {"id": 2, "username": "u", "token": "t", "type": "personal", "group": "", "tags": []}
    stored: dict[str, str] = {}
    asyncio.run(service.fetch_account_summary(account, include_events=True, etags=stored, previous={}))

    assert stored["review_prs"] == etags["/search/issues"]
    assert stored["my_prs"] == etags["/search/issues"]
    assert stored["issues"] == etags["/search/issues"]
    assert stored["events"] == etags["/users/u/received_events"]
