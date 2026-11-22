"""同步调度反风控策略测试：自适应间隔、错峰、单账号冷却。"""
from __future__ import annotations

import asyncio
import time

from app import service
from app.sync import DEFAULTS, SyncEngine, next_interval


def _cfg(**overrides) -> dict:
    cfg = dict(DEFAULTS)
    cfg.update(overrides)
    return cfg


def test_next_interval_adaptive():
    cfg = _cfg(quota_low_ratio=0.5, max_interval_seconds=3600)
    # 跳过占比达标 → 拉长
    assert next_interval(base=900, elapsed=60, total=4, skipped=2, current=900, cfg=cfg) == 1800
    # 低于阈值 → 基础间隔
    assert next_interval(base=900, elapsed=60, total=4, skipped=1, current=900, cfg=cfg) == 900
    # 关闭自适应
    assert next_interval(
        base=900, elapsed=60, total=4, skipped=4, current=900, cfg=_cfg(adaptive_interval=False)
    ) == 900
    # 不超过上限
    assert next_interval(
        base=900, elapsed=60, total=10, skipped=10, current=3600, cfg=cfg
    ) == 3600


def test_next_interval_elapsed_protection():
    cfg = _cfg(adaptive_interval=False)
    assert next_interval(base=900, elapsed=1000, total=5, skipped=0, current=900, cfg=cfg) > 900


def test_sync_account_cooldown(monkeypatch):
    engine = SyncEngine()
    engine.cfg = _cfg(min_sync_interval_seconds=60, stagger_seconds=0)
    engine._last_sync[1] = time.monotonic()

    async def fake_fetch(*args, **kwargs):  # noqa: ANN002, ANN003
        raise AssertionError("冷却期内不应发起同步请求")

    monkeypatch.setattr(service, "fetch_account_summary", fake_fetch)
    out = asyncio.run(engine.sync_account({"id": 1, "username": "u", "token": "t"}))
    assert out["status"] == "throttled"
    assert out["reason"] == "cooldown"
    assert out["retry_after"] > 0


def test_sync_account_not_throttled_after_cooldown(monkeypatch):
    engine = SyncEngine()
    engine.cfg = _cfg(min_sync_interval_seconds=60, stagger_seconds=0)
    engine._last_sync[1] = time.monotonic() - 61

    called = {"n": 0}

    async def fake_fetch(acc, **kwargs):  # noqa: ANN002
        called["n"] += 1
        return {
            "status": "active",
            "label": "u",
            "type": "personal",
            "group_name": "",
            "tags": [],
            "rate_limit": None,
        }

    monkeypatch.setattr(service, "fetch_account_summary", fake_fetch)
    out = asyncio.run(engine.sync_account({"id": 1, "username": "u", "token": "t"}))
    assert called["n"] == 1
    assert out["status"] == "active"


def test_stagger_config_is_honored():
    # 错峰参数应进入默认配置且可被配置覆盖
    assert DEFAULTS["stagger_seconds"] > 0
    assert SyncEngine().cfg["stagger_seconds"] == DEFAULTS["stagger_seconds"]
