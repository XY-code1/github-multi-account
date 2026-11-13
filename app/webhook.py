"""出站 Webhook：把账号事件推送到外部系统（n8n / Zapier / 自建服务）。

配置写在 config/settings.yaml（已 gitignore）：

    webhooks:
      - name: 我的自动化
        url: https://example.com/hook
        events: [review_pr.added, issue.added]   # 省略表示订阅全部
        enabled: true

事件类型：
    review_pr.added / my_pr.added / issue.added
    account.invalid / account.capped / sync.failed
"""
from __future__ import annotations

import logging
from typing import Any

import httpx

from . import config, db

logger = logging.getLogger(__name__)

EVENT_TYPES = (
    "review_pr.added",
    "my_pr.added",
    "issue.added",
    "account.invalid",
    "account.capped",
    "sync.failed",
)


def load_webhooks() -> list[dict[str, Any]]:
    data = config.read_settings()
    raw = data.get("webhooks") or []
    return [w for w in raw if isinstance(w, dict)]


def save_webhooks(webhooks: list[dict[str, Any]]) -> None:
    data = config.read_settings()
    data["webhooks"] = webhooks
    config.write_settings(data)


async def dispatch(event_type: str, payload: dict[str, Any]) -> int:
    """把事件推送到所有订阅它的 Webhook，返回成功数并记录投递日志。"""
    if event_type not in EVENT_TYPES:
        return 0
    sent = 0
    for hook in load_webhooks():
        if not hook.get("enabled", True):
            continue
        events = hook.get("events") or []
        if events and event_type not in events:
            continue
        url = str(hook.get("url") or "").strip()
        if not url:
            continue
        body = {"event": event_type, **payload}
        detail = ""
        try:
            async with httpx.AsyncClient(timeout=8.0) as client:
                resp = await client.post(url, json=body)
                ok = resp.status_code < 400
                if not ok:
                    detail = f"HTTP {resp.status_code}"
        except Exception as e:  # noqa: BLE001
            ok = False
            detail = str(e)[:200]
            logger.warning("Webhook 推送失败 (%s): %s", hook.get("name"), e)
        db.log_webhook(url, event_type, ok, detail)
        if ok:
            sent += 1
    return sent
