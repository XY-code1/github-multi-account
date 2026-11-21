"""GitHub REST API 客户端（异步）。

所有请求都通过后端发起，token 不暴露给浏览器。
"""
from __future__ import annotations

import asyncio
import logging
import time
from typing import Any
from urllib.parse import quote

import httpx

logger = logging.getLogger(__name__)

API_BASE = "https://api.github.com"

# 执行写操作（star / follow / 建 PR）前要求 core 配额至少剩余的次数。
# 低于该值时直接拒绝，避免写操作本身把配额打穿、影响后台同步。
QUOTA_RESERVE = 50


class GitHubError(Exception):
    def __init__(self, status: int, message: str, retry_after: float | None = None):
        self.status = status
        self.message = message
        self.retry_after = retry_after
        super().__init__(message)


class GitHubClient:
    def __init__(self, token: str, timeout: float = 12.0, transport: httpx.AsyncBaseTransport | None = None):
        if not token:
            raise GitHubError(0, "未配置 Token")
        self.token = token
        kwargs: dict[str, Any] = {}
        if transport is not None:
            kwargs["transport"] = transport
        self.client = httpx.AsyncClient(
            base_url=API_BASE,
            headers={
                "Authorization": f"Bearer {token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "github-account-dashboard",
            },
            timeout=timeout,
            **kwargs,
        )

    async def aclose(self) -> None:
        await self.client.aclose()

    async def _request(
        self, path: str, params: dict | None = None, etag: str | None = None
    ) -> tuple[Any, str | None, bool]:
        """发起 GET，返回 (数据, 新 ETag, 是否有变化)。

        带上 If-None-Match 后，未变化时 GitHub 返回 304 且**不消耗 API 配额**，
        这是几百个账号场景下省配额的关键。
        """
        headers = {"If-None-Match": etag} if etag else None
        resp = await self.client.get(path, params=params, headers=headers)
        new_etag = resp.headers.get("etag")
        if resp.status_code == 304:
            return None, new_etag or etag, False
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
        return resp.json(), new_etag, True

    async def _get(self, path: str, params: dict | None = None) -> Any:
        data, _, _ = await self._request(path, params)
        return data

    # ---------- 条件请求版本（供增量同步使用） ----------

    async def get_user_cond(self, etag: str | None = None) -> tuple[Any, str | None, bool]:
        return await self._request("/user", etag=etag)

    async def search_cond(
        self, query: str, per_page: int = 30, etag: str | None = None
    ) -> tuple[int, list[dict], str | None, bool]:
        data, new_etag, modified = await self._request(
            "/search/issues", params={"q": query, "per_page": per_page}, etag=etag
        )
        if not modified:
            return 0, [], new_etag, False
        return data.get("total_count", 0), data.get("items", []), new_etag, True

    async def received_events_cond(
        self, login: str, per_page: int = 30, etag: str | None = None
    ) -> tuple[list[dict], str | None, bool]:
        return await self._request(
            f"/users/{login}/received_events", params={"per_page": per_page}, etag=etag
        )

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

    # ---------- 写操作 ----------

    async def _status(
        self,
        method: str,
        path: str,
        json_body: dict | None = None,
        params: dict | None = None,
    ) -> tuple[int, Any]:
        """发起任意方法的请求，返回 (状态码, 数据)。

        只对 401 / 403 / 429 抛错（403/429 带配额与等待提示），其余状态码
        原样返回，让调用方自己决定 404 之类是否算异常。
        """
        resp = await self.client.request(method, path, json=json_body, params=params)
        if resp.status_code == 401:
            raise GitHubError(401, "Token 无效或已过期")
        if resp.status_code in (403, 429):
            detail = _rate_limit_hint(resp)
            msg = "API 速率限制或权限不足"
            if detail:
                msg = f"{msg}（{detail}）"
            raise GitHubError(resp.status_code, msg, retry_after=_retry_after(resp))
        if resp.status_code == 204 or not resp.content:
            return resp.status_code, None
        try:
            return resp.status_code, resp.json()
        except ValueError:
            return resp.status_code, None

    async def _send(
        self,
        method: str,
        path: str,
        json_body: dict | None = None,
        ok: tuple[int, ...] = (200, 201, 204),
        params: dict | None = None,
    ) -> Any:
        """发起写请求并按允许的状态码校验，返回解析后的数据。"""
        status, data = await self._status(method, path, json_body, params)
        if status not in ok:
            raise GitHubError(status, f"GitHub API 错误 (HTTP {status})")
        return data

    async def check_quota(self, min_remaining: int = QUOTA_RESERVE) -> dict:
        """写操作前的配额预检：core 剩余低于阈值时直接拒绝。

        后台同步、批量操作都依赖同一个 core 配额池，写操作前拦一道，
        避免一次误操作把配额打穿、让整个面板拿不到数据。
        """
        data = await self._get("/rate_limit")
        core = (data.get("resources") or {}).get("core") or {}
        try:
            remaining = int(core.get("remaining", 0))
        except (TypeError, ValueError):
            remaining = 0
        reset = core.get("reset")
        if remaining < min_remaining:
            hint = ""
            if reset:
                try:
                    hint = f"，约 {max(0, int(reset) - int(time.time()))} 秒后重置"
                except (TypeError, ValueError):
                    pass
            raise GitHubError(
                403,
                f"API 配额不足：core 剩余 {remaining} 次，写操作要求 ≥ {min_remaining}{hint}",
            )
        return {"remaining": remaining, "limit": core.get("limit", 0), "reset": reset}

    # ---------- Star ----------

    async def is_starred(self, owner: str, repo: str) -> bool:
        status, _ = await self._status("GET", f"/user/starred/{owner}/{repo}")
        if status == 204:
            return True
        if status == 404:
            return False
        raise GitHubError(status, f"检查 Star 状态失败 (HTTP {status})")

    async def star_repo(self, owner: str, repo: str) -> dict:
        """Star 仓库（幂等）。"""
        await self.check_quota()
        if await self.is_starred(owner, repo):
            return {"starred": True, "already": True}
        await self._send("PUT", f"/user/starred/{owner}/{repo}", ok=(204,))
        return {"starred": True, "already": False}

    async def unstar_repo(self, owner: str, repo: str) -> dict:
        """取消 Star（幂等）。"""
        await self.check_quota()
        if not await self.is_starred(owner, repo):
            return {"starred": False, "already": True}
        await self._send("DELETE", f"/user/starred/{owner}/{repo}", ok=(204,))
        return {"starred": False, "already": False}

    # ---------- Follow ----------

    async def is_following(self, username: str) -> bool:
        status, _ = await self._status("GET", f"/user/following/{username}")
        if status == 204:
            return True
        if status == 404:
            return False
        raise GitHubError(status, f"检查关注状态失败 (HTTP {status})")

    async def follow_user(self, username: str) -> dict:
        """关注用户（幂等）。"""
        await self.check_quota()
        if await self.is_following(username):
            return {"following": True, "already": True}
        await self._send("PUT", f"/user/following/{username}", ok=(204,))
        return {"following": True, "already": False}

    async def unfollow_user(self, username: str) -> dict:
        """取消关注（幂等）。"""
        await self.check_quota()
        if not await self.is_following(username):
            return {"following": False, "already": True}
        await self._send("DELETE", f"/user/following/{username}", ok=(204,))
        return {"following": False, "already": False}

    # ---------- 仓库 / 分支 / 文件 ----------

    async def get_repo(self, owner: str, repo: str) -> dict:
        return await self._send("GET", f"/repos/{owner}/{repo}")

    async def ensure_fork(self, owner: str, repo: str, login: str, timeout: float = 60.0) -> dict:
        """确保 login 名下存在 owner/repo 的 fork；没有就创建并等待就绪。

        GitHub 的 fork 是异步操作，创建后立即可读性不保证，因此轮询等待。
        """
        status, data = await self._status("GET", f"/repos/{login}/{repo}")
        if status == 200 and isinstance(data, dict) and data.get("fork"):
            return {"forked": False, "repo": f"{login}/{repo}"}
        await self._send("POST", f"/repos/{owner}/{repo}/forks", ok=(200, 202))
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            await asyncio.sleep(2.0)
            status, data = await self._status("GET", f"/repos/{login}/{repo}")
            if status == 200 and isinstance(data, dict) and data.get("fork"):
                return {"forked": True, "repo": f"{login}/{repo}"}
        raise GitHubError(0, f"fork {owner}/{repo} 等待超时，请稍后重试")

    async def create_branch(self, owner: str, repo: str, branch: str, from_branch: str) -> dict:
        """基于 from_branch 创建分支；已存在时视为成功。"""
        ref = await self._send("GET", f"/repos/{owner}/{repo}/git/ref/heads/{from_branch}")
        sha = (ref or {}).get("object", {}).get("sha")
        if not sha:
            raise GitHubError(0, f"无法获取 {owner}/{repo} 分支 {from_branch} 的 SHA")
        status, _ = await self._status(
            "POST",
            f"/repos/{owner}/{repo}/git/refs",
            {"ref": f"refs/heads/{branch}", "sha": sha},
        )
        if status in (200, 201):
            return {"created": True, "branch": branch}
        # 422 通常表示分支已存在，幂等处理
        return {"created": False, "branch": branch}

    async def put_file(
        self, owner: str, repo: str, path: str, content: str, branch: str, message: str
    ) -> dict:
        """在指定分支创建/更新文本文件（自动处理已存在文件的 sha）。"""
        import base64

        clean = (path or "").strip().lstrip("/")
        if not clean:
            raise GitHubError(0, "文件路径不能为空")
        escaped = "/".join(quote(seg) for seg in clean.split("/"))
        status, existing = await self._status(
            "GET", f"/repos/{owner}/{repo}/contents/{escaped}", params={"ref": branch}
        )
        body = {
            "message": message,
            "content": base64.b64encode(content.encode("utf-8")).decode("ascii"),
            "branch": branch,
        }
        if status == 200 and isinstance(existing, dict):
            body["sha"] = existing.get("sha")
        elif status != 404:
            raise GitHubError(status, f"读取文件 {path} 失败 (HTTP {status})")
        await self._send(
            "PUT", f"/repos/{owner}/{repo}/contents/{escaped}", body, ok=(200, 201)
        )
        return {"committed": True, "path": clean}

    # ---------- Pull Request ----------

    async def find_open_pr(self, owner: str, repo: str, head: str, base: str | None = None) -> dict | None:
        """查找同源同目标的开 PR（用于幂等创建）。"""
        params = {"state": "open", "head": head, "per_page": 1}
        if base:
            params["base"] = base
        data = await self._send("GET", f"/repos/{owner}/{repo}/pulls", params=params)
        items = data if isinstance(data, list) else []
        return items[0] if items else None

    async def create_pull_request(
        self,
        owner: str,
        repo: str,
        title: str,
        head: str,
        base: str,
        body: str | None = None,
        draft: bool = False,
    ) -> dict:
        payload: dict[str, Any] = {
            "title": title,
            "head": head,
            "base": base,
            "draft": bool(draft),
        }
        if body:
            payload["body"] = body
        return await self._send("POST", f"/repos/{owner}/{repo}/pulls", payload, ok=(200, 201))


def _retry_after(resp: httpx.Response) -> float | None:
    """从响应头推算需要等待的秒数（Retry-After 优先，其次配额重置时间）。"""
    raw = resp.headers.get("retry-after")
    if raw:
        try:
            return float(raw)
        except ValueError:
            return None
    reset = resp.headers.get("x-ratelimit-reset")
    if reset:
        try:
            return max(0.0, float(reset) - time.time())
        except ValueError:
            return None
    return None


def _rate_limit_hint(resp: httpx.Response) -> str:
    parts: list[str] = []
    remaining = resp.headers.get("x-ratelimit-remaining")
    if remaining:
        parts.append(f"剩余 {remaining} 次")
    wait = _retry_after(resp)
    if wait is not None:
        parts.append(f"约 {int(wait)} 秒后可重试")
    return "，".join(parts)
