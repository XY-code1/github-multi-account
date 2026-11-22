"""后台同步引擎：定时同步所有账号，失败隔离 + 指数退避重试。

设计要点（面向几百个账号）：
- 并发限制（默认 5，可配置），避免瞬间打满带宽；
- 单账号失败不影响其他账号；
- 连续失败后按指数退避推迟重试（60s → 120s → 240s，上限 1 小时，带抖动）；
- 同步结果写入 summaries 表，仪表盘读缓存，不实时请求 GitHub；
- 记录 API 配额与同步状态，供"需要关注"判断使用。

反风控调度（不改变原有 ETag 增量同步与失败退避）：
- 错峰：同一轮内每个账号启动前随机延迟 0~stagger_seconds，避免瞬时并发尖峰；
- 自适应间隔：本轮因配额/冷却跳过的账号占比超过 quota_low_ratio 时，
  下一轮间隔自动拉长（上限 max_interval_seconds），配额充裕后回落；
- 单账号冷却：同一账号在 min_sync_interval_seconds 内不重复拉取，
  防止 MCP / 手动同步在短时间内重复触发相同请求。

配置（config/settings.yaml）：
    sync:
      enabled: true
      interval_seconds: 900
      concurrency: 5
      max_retries: 3
      base_backoff_seconds: 60
      max_backoff_seconds: 3600
      quota_floor: 100
      min_sync_interval_seconds: 60
      stagger_seconds: 15
      max_interval_seconds: 3600
      adaptive_interval: true
      quota_low_ratio: 0.5
"""
from __future__ import annotations

import asyncio
import logging
import random
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import yaml

from . import config, db, service, webhook

logger = logging.getLogger(__name__)

# 这些状态表示"本轮没有真正发起同步"（配额不足 / 退避 / 冷却），
# 用于自适应间隔判断是否需要拉长下一轮。
SKIP_STATUSES = frozenset({"skipped", "deferred", "throttled"})

DEFAULTS: dict[str, Any] = {
    "enabled": True,
    "interval_seconds": 900,
    "concurrency": 5,
    "max_retries": 3,
    "base_backoff_seconds": 60,
    "max_backoff_seconds": 3600,
    # 单账号剩余配额低于该值时本轮跳过，避免把配额打满
    "quota_floor": 100,
    # 单账号同步冷却：该秒数内重复同步直接返回 throttled（防止 MCP / 手动重复触发）
    "min_sync_interval_seconds": 60,
    # 错峰：同一轮内每个账号启动前随机延迟 0~该秒数
    "stagger_seconds": 15,
    # 自适应间隔上限（配额紧张时拉长到该值为止）
    "max_interval_seconds": 3600,
    # 是否启用按配额自适应间隔
    "adaptive_interval": True,
    # 本轮被跳过（配额不足/冷却/退避）的账号占比达到该值时拉长下一轮间隔
    "quota_low_ratio": 0.5,
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


def next_interval(
    *,
    base: int,
    elapsed: float,
    total: int,
    skipped: int,
    current: int,
    cfg: dict[str, Any],
) -> int:
    """计算下一轮同步间隔。

    优先级：配额紧张（跳过占比达标）> 本轮耗时过长 > 回到基础间隔。
    仅影响本地调度，不回写用户配置。
    """
    max_interval = int(cfg.get("max_interval_seconds") or 0) or max(base * 4, 3600)
    ratio = float(cfg.get("quota_low_ratio", 0.5) or 0)
    if (
        cfg.get("adaptive_interval", True)
        and total > 0
        and ratio > 0
        and skipped / total >= ratio
    ):
        return min(max(current * 2, base * 2), max_interval)
    if base > 0 and elapsed > current:
        return min(int(elapsed * 1.2) + 60, max(base * 3, 3600))
    return base


class SyncEngine:
    """后台同步引擎。"""

    def __init__(self) -> None:
        self.cfg = load_settings()
        self._task: asyncio.Task | None = None
        self._stop = asyncio.Event()
        # 账号 ID -> 上次实际发起同步的 monotonic 时间（用于单账号冷却）
        self._last_sync: dict[int, float] = {}
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
                result = await self.sync_all()
            except Exception as e:  # noqa: BLE001
                logger.warning("同步周期失败: %s", e)
            elapsed = time.monotonic() - started
            self.progress["duration_seconds"] = round(elapsed, 1)
            # 自适应间隔：本轮多数账号因配额/冷却被跳过时拉长下一轮，
            # 避免在配额紧张期继续按固定频率空转；否则沿用原有的耗时保护逻辑。
            total = result.get("total", 0)
            skipped = result.get("skipped", 0)
            new_interval = next_interval(
                base=base,
                elapsed=elapsed,
                total=total,
                skipped=skipped,
                current=interval,
                cfg=self.cfg,
            )
            if new_interval != interval:
                logger.info(
                    "下一轮同步间隔 %ds → %ds（本轮 %d/%d 个账号因配额/冷却跳过，耗时 %.0fs）",
                    interval, new_interval, skipped, total, elapsed,
                )
            interval = new_interval

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
        skipped = sum(1 for r in results if isinstance(r, dict) and r.get("status") in SKIP_STATUSES)
        self.progress.update(
            {
                "status": "idle",
                "done": done,
                "failed": len(results) - done,
                "finished_at": datetime.now(timezone.utc).isoformat(),
                "current": None,
            }
        )
        return {
            "total": len(accounts),
            "done": done,
            "failed": len(results) - done,
            "skipped": skipped,
        }

    async def _guarded(self, sem: asyncio.Semaphore, acc: dict[str, Any]) -> dict[str, Any]:
        async with sem:
            # 错峰：随机延迟后再发请求，避免同一时刻并发尖峰
            stagger = float(self.cfg.get("stagger_seconds") or 0)
            if stagger > 0:
                await asyncio.sleep(random.uniform(0, stagger))
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
        # 单账号冷却：刚同步过的账号不重复拉取（防止 MCP / 手动在短时间内重复触发）
        cooldown = float(self.cfg.get("min_sync_interval_seconds") or 0)
        if cooldown > 0:
            last = self._last_sync.get(aid)
            if last is not None:
                remaining = cooldown - (time.monotonic() - last)
                if remaining > 0:
                    return {
                        "id": aid,
                        "ok": False,
                        "status": "throttled",
                        "reason": "cooldown",
                        "retry_after": int(remaining) + 1,
                    }
        self._last_sync[aid] = time.monotonic()

        # 同步前的旧状态（用于事件检测与 304 兜底）
        previous = db.get_summary_dict(aid)
        old_rl = db.all_rate_limits().get(aid)
        was_capped = bool(
            old_rl
            and int(old_rl.get("quota_limit") or 0) > 0
            and int(old_rl.get("remaining") or 0) <= db.QUOTA_CAP
        )

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
            previous=previous,
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

        # 出站 Webhook：把本次同步检测到的变化推给外部系统
        new_rl = db.all_rate_limits().get(aid)
        now_capped = bool(
            new_rl
            and int(new_rl.get("quota_limit") or 0) > 0
            and int(new_rl.get("remaining") or 0) <= db.QUOTA_CAP
        )
        for event_type, detail in _detect_events(previous, summary, was_capped, now_capped):
            await webhook.dispatch(
                event_type,
                {
                    "account": {
                        "id": aid,
                        "username": username,
                        "label": summary.get("label") or acc.get("label"),
                    },
                    "detail": detail,
                    "ts": datetime.now(timezone.utc).isoformat(),
                },
            )
        return {"id": aid, "ok": summary["status"] == "active", "status": summary["status"]}


def _detect_events(
    previous: dict[str, Any],
    summary: dict[str, Any],
    was_capped: bool,
    now_capped: bool,
) -> list[tuple[str, str]]:
    """对比新旧汇总，返回 [(事件类型, 详情)]。"""
    events: list[tuple[str, str]] = []
    pairs = (
        ("review_pr.added", "review_prs_count", "待 Review PR"),
        ("my_pr.added", "my_prs_count", "我发起的 PR"),
        ("issue.added", "assigned_issues_count", "分配 Issue"),
    )
    for event_type, key, label in pairs:
        old_n = int(previous.get(key) or 0)
        new_n = int(summary.get(key) or 0)
        if new_n > old_n:
            events.append((event_type, f"{label}从 {old_n} 增加到 {new_n}"))

    status = summary.get("status")
    if status in ("invalid", "error"):
        events.append(("account.invalid", summary.get("error") or "凭据无效或已过期"))
    elif status != "active":
        events.append(("sync.failed", summary.get("error") or f"同步状态 {status}"))
    if now_capped and not was_capped:
        events.append(("account.capped", "API 已封顶（剩余 ≤ 60/小时）"))
    return events
