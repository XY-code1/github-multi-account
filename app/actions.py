"""GitHub 写操作用例：Star 仓库、关注用户、创建 Pull Request。

约定：
- 所有写操作先做参数校验与配额预检（core 剩余 ≥ quota_reserve），避免误操作打穿配额；
- 操作幂等：star / follow 重复执行不产生副作用，create_pr 会先查已有同源同目标的开 PR；
- 统一返回结构化结果，供 HTTP API 与 MCP 工具直接透传。
"""
from __future__ import annotations

import logging
from typing import Any

from .github import QUOTA_RESERVE, GitHubClient, GitHubError

logger = logging.getLogger(__name__)


class ActionError(Exception):
    """参数或前置校验失败（映射为 HTTP 400）。"""

    def __init__(self, message: str, status: int = 400):
        self.status = status
        self.message = message
        super().__init__(message)


def split_repo(repo: str) -> tuple[str, str]:
    """解析 "owner/repo" 形式的目标仓库。"""
    parts = (repo or "").strip().strip("/").split("/")
    if len(parts) != 2 or not parts[0] or not parts[1]:
        raise ActionError("repo 格式不正确，应为 owner/repo")
    return parts[0], parts[1]


def _token(account: dict[str, Any]) -> str:
    token = account.get("token", "")
    if not token:
        raise ActionError("账号未配置 Token", status=400)
    return token


async def star_repo(
    account: dict[str, Any], owner: str, repo: str, quota_reserve: int = QUOTA_RESERVE
) -> dict[str, Any]:
    """以指定账号 Star 仓库（幂等）。"""
    if not owner or not repo:
        raise ActionError("owner 与 repo 不能为空")
    client = GitHubClient(_token(account))
    try:
        result = await client.star_repo(owner, repo)
    finally:
        await client.aclose()
    return {
        "ok": True,
        "account": account.get("username", ""),
        "owner": owner,
        "repo": repo,
        **result,
    }


async def follow_user(
    account: dict[str, Any], username: str, quota_reserve: int = QUOTA_RESERVE
) -> dict[str, Any]:
    """以指定账号关注用户（幂等）。"""
    if not username:
        raise ActionError("username 不能为空")
    client = GitHubClient(_token(account))
    try:
        result = await client.follow_user(username)
    finally:
        await client.aclose()
    return {
        "ok": True,
        "account": account.get("username", ""),
        "username": username,
        **result,
    }


async def create_pr(
    account: dict[str, Any],
    repo: str,
    title: str,
    *,
    base: str | None = None,
    head: str | None = None,
    body: str | None = None,
    draft: bool = False,
    fork: bool = False,
    branch: str | None = None,
    file_path: str | None = None,
    file_content: str | None = None,
    quota_reserve: int = QUOTA_RESERVE,
) -> dict[str, Any]:
    """以指定账号创建 Pull Request。

    两种模式：
    - fork=False（默认）：head 为同仓库分支名，需要账号对目标仓库有写权限；
    - fork=True：自动 fork（如尚未 fork），head 为 `{login}:{branch}`。

    可选提交文件：同时提供 file_path 与 file_content 时，会在工作仓库的 branch
    分支上创建/更新该文件（branch 必填，不存在则基于 base 自动创建）。
    """
    owner, name = split_repo(repo)
    if not (title or "").strip():
        raise ActionError("title 不能为空")
    if (file_path is None) != (file_content is None):
        raise ActionError("file_path 与 file_content 必须同时提供")

    client = GitHubClient(_token(account))
    try:
        me = await client.get_user()
        login = me.get("login") or account.get("username", "")
        await client.check_quota(quota_reserve)

        info = await client.get_repo(owner, name)
        base_branch = base or info.get("default_branch") or "main"
        branch_name = branch or head
        if not branch_name:
            raise ActionError("必须提供 head（分支名）或 branch")

        work_owner = owner
        if fork:
            await client.ensure_fork(owner, name, login)
            work_owner = login
            head_ref = f"{login}:{branch_name}"
        else:
            head_ref = branch_name

        if file_path:
            if not branch:
                raise ActionError("提供文件时 branch 为必填")
            await client.create_branch(work_owner, name, branch, base_branch)
            await client.put_file(
                work_owner, name, file_path, file_content, branch, f"{title}（自动提交）"
            )

        existing = await client.find_open_pr(owner, name, head_ref, base_branch)
        if existing:
            return {
                "ok": True,
                "action": "existing",
                "account": login,
                "repo": f"{owner}/{name}",
                "pr": existing,
                "url": existing.get("html_url"),
            }

        pr = await client.create_pull_request(
            owner, name, title, head_ref, base_branch, body, draft
        )
        return {
            "ok": True,
            "action": "created",
            "account": login,
            "repo": f"{owner}/{name}",
            "pr": pr,
            "url": pr.get("html_url"),
        }
    finally:
        await client.aclose()


__all__ = ["ActionError", "create_pr", "follow_user", "split_repo", "star_repo", "GitHubError"]
