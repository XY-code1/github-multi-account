"""账号状态聚合服务：为每个账号并发拉取登录态、PR、Issue、动态等。"""
from __future__ import annotations

import asyncio
from datetime import datetime, timezone
from typing import Any

from .config import mask_token
from .github import GitHubClient, GitHubError


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


async def fetch_account_status(account: dict[str, Any]) -> dict[str, Any]:
    """拉取单个账号的完整状态。

    先验证 token（get_user），成功后再并发拉取其余数据。
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
        review_prs, my_prs, issues, events, repos = await asyncio.gather(
            client.review_requested_prs(login),
            client.my_open_prs(login),
            client.assigned_issues(login),
            client.received_events(login),
            client.user_repos(login),
        )
        result["review_prs"] = review_prs
        result["my_prs"] = my_prs
        result["assigned_issues"] = issues
        result["events"] = events
        result["repos"] = repos

        # 3) 速率限制（失败不影响主流程）
        try:
            result["rate_limit"] = await client.rate_limit()
        except Exception:
            pass

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
