"""账号状态聚合服务。

- fetch_account_summary：轻量汇总（侧栏用，4 个请求）
- fetch_account_detail ：完整详情（详情页用，含仓库 / 粉丝 / 动态）
- fetch_all_summaries  ：并发拉取全部账号汇总（带并发限流）

容错原则：并发请求使用 return_exceptions=True，单个数据源失败
只影响该字段（降级为空），不拖垮整个账号。
"""
from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timezone
from typing import Any

from . import store
from .config import mask_token
from .github import GitHubClient, GitHubError

logger = logging.getLogger(__name__)

# 同时刷新的最大账号数，避免瞬间打满带宽 / 触发限流
MAX_CONCURRENT_ACCOUNTS = 3

_semaphore: asyncio.Semaphore | None = None
_semaphore_loop: asyncio.AbstractEventLoop | None = None


def _get_semaphore() -> asyncio.Semaphore:
    """按需创建信号量，并在事件循环变化时重建（兼容测试）。"""
    global _semaphore, _semaphore_loop
    loop = asyncio.get_running_loop()
    if _semaphore is None or _semaphore_loop is not loop:
        _semaphore = asyncio.Semaphore(MAX_CONCURRENT_ACCOUNTS)
        _semaphore_loop = loop
    return _semaphore


def _star_distribution(repos: list[dict]) -> list[dict[str, Any]]:
    """按 Star 数分段统计仓库数量。"""
    ranges = [("0 星", 0, 0), ("1-10", 1, 10), ("11-50", 11, 50), ("51-100", 51, 100), ("100+", 101, None)]
    dist = {label: 0 for label, _, _ in ranges}
    for r in repos:
        stars = r.get("stargazers_count", 0)
        for label, low, high in ranges:
            if stars >= low and (high is None or stars <= high):
                dist[label] += 1
                break
    return [{"label": label, "count": count} for label, count in dist.items()]


def _empty_result(account: dict[str, Any]) -> dict[str, Any]:
    """所有字段都给默认值，前端不会因缺字段报错。"""
    return {
        "id": account["id"],
        "label": account.get("label") or account.get("username"),
        "username": account.get("username", ""),
        "token_masked": mask_token(account.get("token", "")),
        "status": "unknown",
        "user": None,
        "error": None,
        "warnings": [],
        "last_checked": datetime.now(timezone.utc).isoformat(),
        "review_prs": [],
        "my_prs": [],
        "assigned_issues": [],
        "events": [],
        "repos": [],
        "rate_limit": None,
        "review_prs_count": 0,
        "my_prs_count": 0,
        "assigned_issues_count": 0,
        "followers_count": 0,
        "following_count": 0,
        "public_repos": 0,
        "total_stars": 0,
        "top_repos": [],
        "star_distribution": [],
        "follower_list": [],
        "following_list": [],
    }


async def _resolve_login(client: GitHubClient, account: dict[str, Any], result: dict[str, Any]) -> str | None:
    """验证登录；成功返回 login，失败把错误写入 result 并返回 None。"""
    try:
        user = await client.get_user()
    except GitHubError as e:
        result["status"] = "invalid" if e.status == 401 else "error"
        result["error"] = e.message
        return None
    except Exception as e:  # noqa: BLE001
        result["status"] = "error"
        result["error"] = f"请求失败：{e}"
        return None
    result["user"] = user
    result["status"] = "active"
    return user.get("login", account.get("username", ""))


def _collect(results: list[Any], names: tuple[str, ...]) -> tuple[list[Any], list[str]]:
    """把 gather 的结果拆成 (值列表, 失败名称列表)。"""
    values: list[Any] = []
    failed: list[str] = []
    for name, value in zip(names, results):
        if isinstance(value, BaseException):
            values.append([])
            failed.append(name)
        else:
            values.append(value)
    return values, failed


def _split(result: Any) -> tuple[int, list]:
    """把搜索返回拆成 (真实总数, 条目)；失败时降级为 (0, [])。"""
    if isinstance(result, tuple) and len(result) == 2:
        return result[0], result[1]
    return len(result or []), result or []


async def verify_token(username: str, token: str) -> dict[str, Any]:
    """仅验证 token 有效性，不保存、不拉取其他数据。用于「测试连接」。"""
    if not token:
        return {"status": "invalid", "user": None, "error": "未配置 Token"}
    client = GitHubClient(token)
    try:
        user = await client.get_user()
        return {"status": "active", "user": user, "error": None}
    except GitHubError as e:
        return {
            "status": "invalid" if e.status == 401 else "error",
            "user": None,
            "error": e.message,
        }
    except Exception as e:  # noqa: BLE001
        return {"status": "error", "user": None, "error": f"请求失败：{e}"}
    finally:
        await client.aclose()


async def fetch_account_summary(
    account: dict[str, Any], include_events: bool = False
) -> dict[str, Any]:
    """轻量汇总：登录态 + PR/Issue 计数。

    include_events=True 时额外拉取最近动态与配额（后台同步用）。
    """
    token = account.get("token", "")
    result = _empty_result(account)
    result["type"] = account.get("type", "personal")
    result["group_name"] = account.get("group", "")
    result["tags"] = account.get("tags", [])
    if not token:
        result["status"] = "invalid"
        result["error"] = "未配置 Token"
        return result

    client = GitHubClient(token)
    try:
        login = await _resolve_login(client, account, result)
        if not login:
            return result

        calls: list[Any] = [
            client.review_requested_prs(login),
            client.my_open_prs(login),
            client.assigned_issues(login),
        ]
        if include_events:
            calls.append(client.received_events(login))
        results = await asyncio.gather(*calls, return_exceptions=True)
        (review_res, mypr_res, issue_res), failed = _collect(
            results[:3], ("待处理 PR", "我的 PR", "分配 Issue")
        )
        if failed:
            result["warnings"] = failed
            logger.warning("账号 %s 汇总部分失败: %s", account.get("username"), ", ".join(failed))

        review_total, review_prs = _split(review_res)
        my_total, my_prs = _split(mypr_res)
        issue_total, issues = _split(issue_res)
        result["review_prs"] = review_prs
        result["my_prs"] = my_prs
        result["assigned_issues"] = issues
        result["review_prs_count"] = review_total
        result["my_prs_count"] = my_total
        result["assigned_issues_count"] = issue_total
        # 账号对比用的轻量指标（都来自 /user，无需额外请求）
        user = result["user"] or {}
        result["followers_count"] = user.get("followers", 0)
        result["following_count"] = user.get("following", 0)
        result["public_repos"] = user.get("public_repos", 0)

        if include_events:
            events_res = results[3]
            if not isinstance(events_res, BaseException):
                result["events"] = events_res
            try:
                result["rate_limit"] = await client.rate_limit()
            except Exception:  # noqa: BLE001
                pass
    finally:
        await client.aclose()
    return result


async def fetch_account_detail(account: dict[str, Any], force_record: bool = False) -> dict[str, Any]:
    """完整详情：概览 / PR / Issue / 动态 / 仓库 / 粉丝。"""
    token = account.get("token", "")
    result = _empty_result(account)
    if not token:
        result["status"] = "invalid"
        result["error"] = "未配置 Token"
        return result

    client = GitHubClient(token)
    try:
        login = await _resolve_login(client, account, result)
        if not login:
            return result

        results = await asyncio.gather(
            client.review_requested_prs(login),
            client.my_open_prs(login),
            client.assigned_issues(login),
            client.received_events(login),
            client.user_repos_all(login),
            client.get_followers(login),
            client.get_following(login),
            return_exceptions=True,
        )
        (review_res, mypr_res, issue_res, events, repos, followers, following), failed = _collect(
            results,
            ("待处理 PR", "我的 PR", "分配 Issue", "仓库动态", "仓库列表", "粉丝", "关注"),
        )
        if failed:
            result["warnings"] = failed
            logger.warning("账号 %s 详情部分失败: %s", account.get("username"), ", ".join(failed))

        review_total, review_prs = _split(review_res)
        my_total, my_prs = _split(mypr_res)
        issue_total, issues = _split(issue_res)
        result["review_prs"] = review_prs
        result["my_prs"] = my_prs
        result["assigned_issues"] = issues
        result["events"] = events
        result["repos"] = repos[:30]
        result["review_prs_count"] = review_total
        result["my_prs_count"] = my_total
        result["assigned_issues_count"] = issue_total

        # 粉丝 / 关注 / Star 统计
        user = result["user"] or {}
        result["followers_count"] = user.get("followers", 0)
        result["following_count"] = user.get("following", 0)
        result["public_repos"] = user.get("public_repos", 0)
        result["total_stars"] = sum(r.get("stargazers_count", 0) for r in repos)
        result["top_repos"] = [
            {
                "name": r.get("name", ""),
                "stars": r.get("stargazers_count", 0),
                "html_url": r.get("html_url", ""),
                "language": r.get("language"),
            }
            for r in sorted(repos, key=lambda x: x.get("stargazers_count", 0), reverse=True)[:10]
            if r.get("stargazers_count", 0) > 0
        ]
        result["star_distribution"] = _star_distribution(repos)
        result["follower_list"] = [
            {
                "login": f.get("login", ""),
                "avatar_url": f.get("avatar_url", ""),
                "html_url": f.get("html_url", ""),
            }
            for f in followers[:50]
        ]
        result["following_list"] = [
            {
                "login": f.get("login", ""),
                "avatar_url": f.get("avatar_url", ""),
                "html_url": f.get("html_url", ""),
            }
            for f in following[:50]
        ]

        # 速率限制（失败不影响主流程）
        try:
            result["rate_limit"] = await client.rate_limit()
        except Exception:  # noqa: BLE001
            pass

        # 记录历史快照（默认 10 分钟节流，force_record 时忽略）
        store.save_snapshot(
            account.get("username", ""),
            {
                "followers": result["followers_count"],
                "following": result["following_count"],
                "total_stars": result["total_stars"],
                "public_repos": result["public_repos"],
                "review_prs": len(review_prs),
                "my_prs": len(my_prs),
                "assigned_issues": len(issues),
                "events": len(events),
            },
            force=force_record,
        )
    finally:
        await client.aclose()
    return result


async def fetch_all_summaries(accounts: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """并发拉取全部账号的汇总（带并发限流，单账号异常不影响其他账号）。"""

    async def _limited(acc: dict[str, Any]) -> dict[str, Any]:
        async with _get_semaphore():
            try:
                return await fetch_account_summary(acc)
            except Exception as e:  # noqa: BLE001
                logger.exception("账号 %s 汇总失败", acc.get("username"))
                result = _empty_result(acc)
                result["status"] = "error"
                result["error"] = f"汇总失败：{e}"
                return result

    return await asyncio.gather(*(_limited(a) for a in accounts))


# ------------------------------ 全局搜索 ------------------------------

def repo_short(url: str) -> str:
    """从 repository_url 提取 owner/repo。"""
    parts = [p for p in (url or "").split("/") if p]
    return "/".join(parts[-2:]) if len(parts) >= 2 else (url or "")


async def search_account_items(account: dict[str, Any], query: str, kind: str) -> list[dict[str, Any]]:
    """在单个账号内按关键字搜索 PR / Issue。"""
    token = account.get("token", "")
    if not token or not query.strip():
        return []
    client = GitHubClient(token)
    try:
        user = await client.get_user()
        login = user.get("login", account.get("username", ""))
        queries: list[str] = []
        if kind in ("all", "pr"):
            queries.append(f"is:open is:pr author:{login} {query}")
            queries.append(f"is:open is:pr review-requested:{login} {query}")
        if kind in ("all", "issue"):
            queries.append(f"is:open is:issue assignee:{login} {query}")
        results = await asyncio.gather(
            *(client.search(q, 30) for q in queries),
            return_exceptions=True,
        )
        out: list[dict[str, Any]] = []
        seen: set[tuple] = set()
        for r in results:
            if isinstance(r, BaseException):
                continue
            for it in _split(r)[1]:
                key = (it.get("repository_url"), it.get("number"))
                if key in seen:
                    continue
                seen.add(key)
                out.append(
                    {
                        "account_id": account["id"],
                        "account_label": account.get("label") or account.get("username"),
                        "title": it.get("title", ""),
                        "html_url": it.get("html_url", ""),
                        "repository": repo_short(it.get("repository_url", "")),
                        "number": it.get("number"),
                        "created_at": it.get("created_at", ""),
                        "labels": [l.get("name", "") for l in it.get("labels", [])],
                    }
                )
        return out
    finally:
        await client.aclose()


async def global_search(accounts: list[dict[str, Any]], query: str, kind: str = "all") -> dict[str, Any]:
    """跨账号搜索：每个账号用自己的 token 独立查询，合并去重。"""
    if not query.strip():
        return {"results": [], "total": 0, "accounts": 0}
    results = await asyncio.gather(
        *(search_account_items(a, query, kind) for a in accounts),
        return_exceptions=True,
    )
    merged: list[dict[str, Any]] = []
    for r in results:
        if isinstance(r, list):
            merged.extend(r)
    merged.sort(key=lambda x: x.get("created_at") or "", reverse=True)
    return {"results": merged[:100], "total": len(merged), "accounts": len(accounts)}
