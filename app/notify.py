"""外部通知渠道：Bark / Server酱 / 企业微信机器人 / 通用 Webhook。

配置写在 config/settings.yaml（已 gitignore）：

    channels:
      - type: bark          # bark | webhook | serverchan | wecom
        name: iPhone
        url: https://api.day.app/xxxx
        enabled: true
      - type: webhook
        name: 自建
        url: https://example.com/hook
        enabled: false

程序会后台轮询账号的 PR/Issue 数量变化，有增加时推送到所有启用的渠道。
"""
from __future__ import annotations

import asyncio
import logging
import os
from pathlib import Path
from typing import Any
from urllib.parse import quote

import httpx
import yaml

logger = logging.getLogger(__name__)

CHANNEL_TYPES = ("bark", "webhook", "serverchan", "wecom")

CONFIG_DIR = Path(os.getenv("GAD_CONFIG_DIR", Path(__file__).resolve().parent.parent / "config"))
SETTINGS_FILE = CONFIG_DIR / "settings.yaml"


def load_channels() -> list[dict[str, Any]]:
    if not SETTINGS_FILE.exists():
        return []
    try:
        data = yaml.safe_load(SETTINGS_FILE.read_text(encoding="utf-8")) or {}
    except Exception:
        return []
    channels = data.get("channels") or []
    return [c for c in channels if isinstance(c, dict)]


def save_channels(channels: list[dict[str, Any]]) -> None:
    SETTINGS_FILE.parent.mkdir(parents=True, exist_ok=True)
    payload = {"channels": channels}
    SETTINGS_FILE.write_text(
        yaml.safe_dump(payload, allow_unicode=True, sort_keys=False), encoding="utf-8"
    )


async def _post_json(url: str, body: dict[str, Any]) -> None:
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(url, json=body)
        resp.raise_for_status()


async def _get(url: str) -> None:
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.get(url)
        resp.raise_for_status()


async def send_channel(channel: dict[str, Any], title: str, body: str, link: str = "") -> bool:
    if not channel.get("enabled", True):
        return False
    url = (channel.get("url") or "").strip()
    if not url:
        return False
    ctype = channel.get("type")
    try:
        if ctype == "webhook":
            await _post_json(url, {"title": title, "body": body, "url": link})
        elif ctype == "bark":
            await _post_json(url, {"title": title, "body": body})
        elif ctype == "serverchan":
            await _get(f"{url}?title={quote(title)}&desp={quote(body)}")
        elif ctype == "wecom":
            await _post_json(url, {"msgtype": "text", "text": {"content": f"{title}\n{body}"}})
        else:
            logger.warning("未知通知渠道类型: %s", ctype)
            return False
        return True
    except Exception as e:  # noqa: BLE001
        logger.warning("通知发送失败 (%s): %s", channel.get("name"), e)
        return False


async def send_all(channels: list[dict[str, Any]], title: str, body: str, link: str = "") -> int:
    """推送到所有启用渠道，返回成功数。"""
    enabled = [c for c in channels if c.get("enabled", True)]
    if not enabled:
        return 0
    results = await asyncio.gather(
        *(send_channel(c, title, body, link) for c in enabled),
        return_exceptions=True,
    )
    return sum(1 for r in results if r is True)


class NotificationPoller:
    """后台轮询 PR/Issue 数量变化并推送（首次只建立基线，不推送）。"""

    def __init__(self, interval_seconds: int = 300) -> None:
        self.interval = interval_seconds
        self.previous: dict[int, dict[str, int]] = {}
        self._task: asyncio.Task | None = None
        self._first = True

    def start(self) -> None:
        if self._task is None:
            self._task = asyncio.create_task(self._run())

    def is_running(self) -> bool:
        return self._task is not None

    async def stop(self) -> None:
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None

    async def _run(self) -> None:
        while True:
            await asyncio.sleep(self.interval)
            try:
                await self.tick()
            except Exception as e:  # noqa: BLE001
                logger.warning("通知轮询失败: %s", e)

    async def tick(self) -> list[str]:
        from . import config, service

        accounts = config.load_accounts()
        if not accounts:
            return []
        summaries = await service.fetch_all_summaries(accounts)
        messages: list[str] = []
        for s in summaries:
            aid = s.get("id")
            prev = self.previous.get(aid, {"review": 0, "issues": 0})
            review = s.get("review_prs_count", 0)
            issues = s.get("assigned_issues_count", 0)
            if not self._first and (review != prev["review"] or issues != prev["issues"]):
                parts = []
                if review > prev["review"]:
                    parts.append(f"待处理 PR +{review - prev['review']}")
                if issues > prev["issues"]:
                    parts.append(f"分配 Issue +{issues - prev['issues']}")
                if parts:
                    messages.append(f"{s.get('label')}: " + "，".join(parts))
            self.previous[aid] = {"review": review, "issues": issues}
        self._first = False
        if messages:
            await send_all(load_channels(), "GitHub 账号变化", "；".join(messages))
        return messages
