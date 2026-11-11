"""GitHub REST API 客户端（异步）。

所有请求都通过后端发起，token 不暴露给浏览器。
"""
from __future__ import annotations

from typing import Any

import httpx

API_BASE = "https://api.github.com"


class GitHubError(Exception):
    def __init__(self, status: int, message: str):
        self.status = status
        self.message = message
        super().__init__(message)


class GitHubClient:
    def __init__(self, token: str, timeout: float = 12.0):
        if not token:
            raise GitHubError(0, "未配置 Token")
        self.token = token
        self.client = httpx.AsyncClient(
            base_url=API_BASE,
            headers={
                "Authorization": f"Bearer {token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "github-account-dashboard",
            },
            timeout=timeout,
        )

    async def aclose(self) -> None:
        await self.client.aclose()

    async def _get(self, path: str, params: dict | None = None) -> Any:
        resp = await self.client.get(path, params=params)
        if resp.status_code == 401:
            raise GitHubError(401, "Token 无效或已过期")
        if resp.status_code == 403:
            msg = "API 速率限制或权限不足"
            try:
                detail = resp.json().get("message")
                if detail:
                    msg = detail
            except Exception:
                pass
            raise GitHubError(403, msg)
        if resp.status_code == 404:
            raise GitHubError(404, "资源不存在或无访问权限")
        resp.raise_for_status()
        return resp.json()

    # ---------- 用户 ----------

    async def get_user(self) -> dict:
        """验证 token 并返回登录用户信息。"""
        return await self._get("/user")

    # ---------- 搜索 ----------

    async def search(self, query: str, per_page: int = 30) -> list[dict]:
        data = await self._get("/search/issues", params={"q": query, "per_page": per_page})
        return data.get("items", [])

    async def review_requested_prs(self, login: str) -> list[dict]:
        """等待我 review 的开放 PR。"""
        return await self.search(f"is:open is:pr review-requested:{login}")

    async def my_open_prs(self, login: str) -> list[dict]:
        """我发起的开放 PR。"""
        return await self.search(f"is:open is:pr author:{login}")

    async def assigned_issues(self, login: str) -> list[dict]:
        """分配给我的开放 Issue。"""
        return await self.search(f"is:open is:issue assignee:{login}")

    # ---------- 动态与仓库 ----------

    async def received_events(self, login: str, per_page: int = 30) -> list[dict]:
        """该用户收到的事件（被 @、仓库被 star、关注的更新等）。"""
        return await self._get(f"/users/{login}/received_events", params={"per_page": per_page})

    async def user_repos(self, login: str, per_page: int = 10) -> list[dict]:
        """该用户最近更新的公开仓库。"""
        return await self._get(
            f"/users/{login}/repos",
            params={"sort": "updated", "direction": "desc", "per_page": per_page},
        )

    async def rate_limit(self) -> dict:
        return await self._get("/rate_limit")
