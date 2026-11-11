"""GitHub REST API 客户端（异步）。

所有请求都通过后端发起，token 不暴露给浏览器。
"""
from __future__ import annotations

import asyncio
import logging
from typing import Any

import httpx

logger = logging.getLogger(__name__)

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

    async def search(self, query: str, per_page: int = 30) -> tuple[int, list[dict]]:
        """搜索 issue/PR，返回 (真实总数, 当前页条目)。

        总数取自 total_count，避免用 len(items) 把计数截断成 per_page。
        """
        data = await self._get("/search/issues", params={"q": query, "per_page": per_page})
        return data.get("total_count", 0), data.get("items", [])

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

    async def _repo_page(self, login: str, page: int) -> list[dict]:
        return await self._get(
            f"/users/{login}/repos",
            params={"sort": "updated", "direction": "desc", "per_page": 100, "page": page},
        )

    async def user_repos_all(self, login: str, max_pages: int = 10, concurrency: int = 5) -> list[dict]:
        """并发分页拉取该用户的公开仓库（用于统计 Star）。

        一次并发拉 concurrency 页；遇到不足 100 条的页或请求失败即停止。
        """
        repos: list[dict] = []
        page = 1
        while page <= max_pages:
            pages = list(range(page, min(page + concurrency, max_pages + 1)))
            batches = await asyncio.gather(
                *(self._repo_page(login, p) for p in pages),
                return_exceptions=True,
            )
            stop = False
            for batch in batches:
                if isinstance(batch, BaseException):
                    logger.warning("仓库分页请求失败（login=%s）: %s", login, batch)
                    stop = True
                    continue
                repos.extend(batch)
                if len(batch) < 100:
                    stop = True
            if stop:
                break
            page += concurrency
        return repos

    async def get_followers(self, login: str, per_page: int = 100) -> list[dict]:
        """该用户的粉丝列表。"""
        return await self._get(f"/users/{login}/followers", params={"per_page": per_page})

    async def get_following(self, login: str, per_page: int = 100) -> list[dict]:
        """该用户关注的人。"""
        return await self._get(f"/users/{login}/following", params={"per_page": per_page})

    async def rate_limit(self) -> dict:
        return await self._get("/rate_limit")
