"""后台同步引擎：定时同步所有账号，失败隔离 + 指数退避重试。

设计要点（面向几百个账号）：
- 并发限制（默认 3，可配置），避免瞬间打满带宽；
- 单账号失败不影响其他账号；
- 连续失败后按指数退避推迟重试（60s → 120s → 240s，上限 1 小时，带抖动）；
- 同步结果写入 summaries 表，仪表盘读缓存，不实时请求 GitHub；
- 记录 API 配额与同步状态，供"需要关注"判断使用。

配置（config/settings.yaml）：
    sync:
      enabled: true
      interval_seconds: 900
      concurrency: 3
      max_retries: 3
      base_backoff_seconds: 60
      max_backoff_seconds: 3600
"""
from __future__ import annotations

import asyncio
import logging
import random
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import yaml

from . import config, db, service

logger = logging.getLogger(__name__)

DEFAULTS: dict[str, Any] = {
    "enabled": True,
    "interval_seconds": 900,
    "concurrency": 5,
    "max_retries": 3,
    "base_backoff_seconds": 60,
    "max_backoff_seconds": 3600,
    # 单账号剩余配额低于该值时本轮跳过，避免把配额打满
    "quota_floor": 100,
}


def save_settings(cfg: dict[str, Any]) -> None:
    """持久化同步配置到 config/settings.yaml（保留通知渠道等其他分节）。"""
    data = config.read_settings()
    data["sync"] = cfg
    config.write_settings(data)


def _settings_file() -> Path:
    return Path(config.CONFIG_DIR) / "settings.yaml"


def load_settings() -> dict[str, Any]:
    cfg = dict(DEFAULTS)
    try:
        if _settings_file().exists():
            data = yaml.safe_load(_settings_file().read_text(encoding="utf-8")) or {}
        else:
            data = {}
    except Exception:  # noqa: BLE001
        data = {}
    for k, v in (data.get("sync") or {}).items():
        if k in DEFAULTS:
            cfg[k] = v
    return cfg


class SyncEngine:
    """后台同步引擎。"""

    def __init__(self) -> None:
        self.cfg = load_settings()
        self._task: asyncio.Task | None = None
        self._stop = asyncio.Event()
        self.progress: dict[str, Any] = {
            "status": "idle",
            "total": 0,
            "done": 0,
            "failed": 0,
            "started_at": None,
            "finished_at": None,
            "current": None,
            "duration_seconds": 0,
        }

    def configure(self, settings: dict[str, Any]) -> None:
        for k, v in settings.items():
            if k in DEFAULTS:
                self.cfg[k] = v

    def is_running(self) -> bool:
        return self._task is not None

    def start(self) -> None:
        if self._task is None:
            self._stop.clear()
            self._task = asyncio.create_task(self._run())

    async def stop(self) -> None:
        self._stop.set()
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None

    async def _run(self) -> None:
        import time

        base = int(self.cfg["interval_seconds"])
        interval = base
        while not self._stop.is_set():
            try:
                await asyncio.wait_for(self._stop.wait(), timeout=interval)
                break
            except asyncio.TimeoutError:
                pass
            if not self.cfg["enabled"]:
                continue
            started = time.monotonic()
            try:
                await self.sync_all()
            except Exception as e:  # noqa: BLE001
                logger.warning("同步周期失败: %s", e)
            elapsed = time.monotonic() - started
            self.progress["duration_seconds"] = round(elapsed, 1)
            # 自适应间隔：本轮耗时超过间隔时延长下一轮，避免积压
            if base > 0 and elapsed > interval:
                interval = min(int(elapsed * 1.2) + 60, max(base * 3, 3600))
                logger.info("同步耗时 %.0fs 超过间隔，下一轮间隔调整为 %ds", elapsed, interval)
            else:
                interval = base

    async def sync_all(self, only_ids: list[int] | None = None) -> dict[str, Any]:
        accounts = config.load_accounts()
        if only_ids:
            accounts = [a for a in accounts if a["id"] in only_ids]
        if not accounts:
            return {"total": 0, "done": 0, "failed": 0}
        sem = asyncio.Semaphore(max(1, int(self.cfg["concurrency"])))
        self.progress = {
            "status": "running",
            "total": len(accounts),
            "done": 0,
            "failed": 0,
            "started_at": datetime.now(timezone.utc).isoformat(),
            "finished_at": None,
            "current": None,
            "duration_seconds": 0,
        }
        results = await asyncio.gather(
            *(self._guarded(sem, a) for a in accounts),
            return_exceptions=True,
        )
        done = sum(1 for r in results if isinstance(r, dict) and r.get("ok"))
        self.progress.update(
            {
                "status": "idle",
                "done": done,
                "failed": len(results) - done,
                "finished_at": datetime.now(timezone.utc).isoformat(),
                "current": None,
            }
        )
        return {"total": len(accounts), "done": done, "failed": len(results) - done}

    async def _guarded(self, sem: asyncio.Semaphore, acc: dict[str, Any]) -> dict[str, Any]:
        async with sem:
            self.progress["current"] = acc["username"]
            r = await self.sync_account(acc)
            self.progress["done"] += 1
            return r

    async def sync_account(self, acc: dict[str, Any]) -> dict[str, Any]:
        """同步单个账号：失败隔离 + 指数退避。"""
        aid, username = acc["id"], acc["username"]
        state = db.get_sync_state(aid) or {}
        if state.get("sync_enabled") is False:
            return {"id": aid, "ok": False, "status": "skipped", "reason": "sync_disabled"}
        # 配额保护：剩余额度太低时本轮跳过
        rl = db.all_rate_limits().get(aid)
        floor = int(self.cfg.get("quota_floor", 100))
        if rl and int(rl.get("remaining") or 0) < floor:
            return {"id": aid, "ok": False, "status": "skipped", "reason": "quota_low"}
        nxt = state.get("next_retry_at")
        if nxt:
            try:
                if datetime.now(timezone.utc) < datetime.fromisoformat(nxt):
                    return {"id": aid, "ok": False, "status": "deferred", "retry_at": nxt}
            except ValueError:
                pass

        # 增量同步：带上各端点的 ETag，未变化（304）不消耗 API 配额
        etags = {
            key: value
            for key, value in (
                ("review_prs", db.get_etag(aid, "review_prs")),
                ("my_prs", db.get_etag(aid, "my_prs")),
                ("issues", db.get_etag(aid, "issues")),
                ("events", db.get_etag(aid, "events")),
            )
            if value
        }
        summary = await service.fetch_account_summary(
            acc,
            include_events=True,
            etags=etags,
            previous=db.get_summary_dict(aid),
        )
        for key, value in etags.items():
            db.put_etag(aid, key, value)
        db.upsert_sync_state(
            aid,
            username,
            last_sync_at=datetime.now(timezone.utc).isoformat(),
            last_status=summary["status"],
            last_error=summary.get("error"),
            type=summary.get("type", "personal"),
            group_name=summary.get("group_name", ""),
            tags=summary.get("tags", []),
        )
        if summary["status"] == "active":
            db.clear_sync_failure(aid)
        else:
            failures = int(state.get("consecutive_failures") or 0) + 1
            backoff = min(
                int(self.cfg["base_backoff_seconds"]) * (2 ** (failures - 1)),
                int(self.cfg["max_backoff_seconds"]),
            )
            backoff = int(backoff * (0.5 + random.random()))
            next_retry = (datetime.now(timezone.utc) + timedelta(seconds=backoff)).isoformat()
            db.upsert_sync_state(
                aid, username, consecutive_failures=failures, next_retry_at=next_retry
            )

        core = (summary.get("rate_limit") or {}).get("resources", {}).get("core", {})
        if core:
            db.upsert_rate_limit(
                aid,
                username,
                int(core.get("remaining", 0)),
                int(core.get("limit", 0)),
                None,
            )
        db.upsert_summary(aid, username, summary)
        return {"id": aid, "ok": summary["status"] == "active", "status": summary["status"]}
