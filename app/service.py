"""账号状态聚合服务：为每个账号并发拉取登录态、PR、Issue、动态等。"""
from __future__ import annotations

import asyncio
from datetime import datetime, timezone
from typing import Any

from . import store
from .config import mask_token
from .github import GitHubClient, GitHubError


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


async def fetch_account_status(account: dict[str, Any], force_record: bool = False) -> dict[str, Any]:
    """拉取单个账号的完整状态。

    先验证 token（get_user），成功后再并发拉取其余数据。
    force_record=True 时忽略快照节流，强制记录一条历史。
    """
    token = account.get("token", "")
    result: dict[str, Any] = {
        "id": account["id"],
        "label": account.get("label") or account.get("username"),
        "username": account.get("username", ""),
        "token_masked": mask_token(token),
        "status": "unknown",
        "user": None,
        "review_prs": [],
        "my_prs": [],
        "assigned_issues": [],
        "events": [],
        "repos": [],
        "rate_limit": None,
        "error": None,
        "last_checked": datetime.now(timezone.utc).isoformat(),
        "followers_count": 0,
        "following_count": 0,
        "public_repos": 0,
        "total_stars": 0,
        "top_repos": [],
        "star_distribution": [],
        "follower_list": [],
        "following_list": [],
    }
    if not token:
        result["status"] = "invalid"
        result["error"] = "未配置 Token"
        return result

    client = GitHubClient(token)
    try:
        # 1) 验证登录
        user = await client.get_user()
        result["user"] = user
        result["status"] = "active"
        login = user.get("login", account.get("username", ""))

        # 2) 并发拉取其余数据
        review_prs, my_prs, issues, events, repos, followers, following = await asyncio.gather(
            client.review_requested_prs(login),
            client.my_open_prs(login),
            client.assigned_issues(login),
            client.received_events(login),
            client.user_repos_all(login),
            client.get_followers(login),
            client.get_following(login),
        )
        result["review_prs"] = review_prs
        result["my_prs"] = my_prs
        result["assigned_issues"] = issues
        result["events"] = events
        result["repos"] = repos[:30]

        # 3) 粉丝 / 关注 / Star 统计
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

        # 3) 速率限制（失败不影响主流程）
        try:
            result["rate_limit"] = await client.rate_limit()
        except Exception:
            pass

        # 4) 记录历史快照（默认 10 分钟节流，force_record 时忽略）
        store.save_snapshot(
            account.get("username", ""),
            {
                "followers": result["followers_count"],
                "following": result["following_count"],
                "total_stars": result["total_stars"],
                "public_repos": result["public_repos"],
                "review_prs": len(result["review_prs"]),
                "my_prs": len(result["my_prs"]),
                "assigned_issues": len(result["assigned_issues"]),
                "events": len(result["events"]),
            },
            force=force_record,
        )

    except GitHubError as e:
        result["status"] = "invalid" if e.status == 401 else "error"
        result["error"] = e.message
    except Exception as e:  # noqa: BLE001 - 兜底任何网络/解析异常
        result["status"] = "error"
        result["error"] = f"请求失败：{e}"
    finally:
        await client.aclose()
    return result


async def fetch_all_status(accounts: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """并发拉取全部账号状态。"""
    return await asyncio.gather(*(fetch_account_status(a) for a in accounts))
