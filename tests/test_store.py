"""历史快照测试：节流、查询、清理。"""
from datetime import datetime, timedelta, timezone

from app import store


def _save(username, followers, force=True):
    return store.save_snapshot(username, {"followers": followers}, force=force)


def test_throttle_limits_frequency():
    assert _save("u1", 1) is True
    assert _save("u1", 2, force=False) is False
    assert _save("u1", 3, force=True) is True
    history = store.get_history("u1")
    assert len(history) == 2
    assert [h["followers"] for h in history] == [1, 3]


def test_history_is_chronological():
    for i in range(4):
        _save("u1", i)
    history = store.get_history("u1")
    assert [h["followers"] for h in history] == [0, 1, 2, 3]


def test_history_per_user_isolated():
    _save("u1", 1)
    _save("u2", 9)
    assert len(store.get_history("u1")) == 1
    assert len(store.get_history("u2")) == 1
    assert store.get_history("unknown") == []


def test_prune_keeps_newest_n():
    for i in range(5):
        _save("u1", i)
    result = store.prune_snapshots(max_per_account=2, max_days=90)
    assert result["deleted"] == 3
    history = store.get_history("u1")
    assert len(history) == 2
    assert [h["followers"] for h in history] == [3, 4]


def test_prune_removes_expired():
    _save("u1", 1)
    # 手动插入一条 100 天前的记录
    old = (datetime.now(timezone.utc) - timedelta(days=100)).isoformat()
    with store._lock, store._connect() as conn:
        conn.execute(
            """INSERT INTO snapshots (username, created_at, followers)
               VALUES (?, ?, ?)""",
            ("u1", old, 0),
        )
    result = store.prune_snapshots(max_per_account=1000, max_days=90)
    assert result["deleted"] >= 1
    history = store.get_history("u1")
    assert all(h["followers"] != 0 or h["created_at"] != old for h in history)


def test_stats():
    _save("u1", 1)
    _save("u1", 2)
    stats = store.get_stats("u1")
    assert stats["count"] == 2
    assert stats["first"] and stats["last"]


def test_delete_history():
    _save("u1", 1)
    _save("u1", 2)
    _save("u2", 9)
    assert store.delete_history("u1") == 2
    assert store.get_history("u1") == []
    assert len(store.get_history("u2")) == 1  # 其他账号不受影响


def test_prune_without_window_function_support():
    # 模拟老版本 SQLite：超量清理应跳过而不是误删
    import sqlite3 as _sqlite3
    original = _sqlite3.sqlite_version_info
    store.sqlite3.sqlite_version_info = (3, 20)
    try:
        for i in range(5):
            _save("u1", i)
        result = store.prune_snapshots(max_per_account=2, max_days=90)
        assert result["deleted"] == 0
        assert len(store.get_history("u1")) == 5
    finally:
        store.sqlite3.sqlite_version_info = original
