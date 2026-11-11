"""本地数据持久化：用 SQLite 记录账号状态快照，用于历史趋势。

快照按「配置的用户名」归档，按 10 分钟节流自动记录，也可手动记录。
数据库文件：data/dashboard.db（已在 .gitignore 中）。
"""
from __future__ import annotations

import os
import sqlite3
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from threading import Lock
from typing import Any, Iterator

BASE_DIR = Path(__file__).resolve().parent.parent
DATA_DIR = Path(os.getenv("GAD_DATA_DIR", BASE_DIR / "data"))
DB_FILE = DATA_DIR / "dashboard.db"

_lock = Lock()

SCHEMA = """
CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL,
    created_at TEXT NOT NULL,
    followers INTEGER DEFAULT 0,
    following INTEGER DEFAULT 0,
    total_stars INTEGER DEFAULT 0,
    public_repos INTEGER DEFAULT 0,
    review_prs INTEGER DEFAULT 0,
    my_prs INTEGER DEFAULT 0,
    assigned_issues INTEGER DEFAULT 0,
    events INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_snapshots_user_time ON snapshots(username, created_at);
"""

SNAPSHOT_INTERVAL_SECONDS = 600  # 10 分钟


@contextmanager
def _connect() -> Iterator[sqlite3.Connection]:
    conn = sqlite3.connect(DB_FILE)
    try:
        yield conn
        conn.commit()
    finally:
        conn.close()


def init_db() -> None:
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    with _lock, _connect() as conn:
        conn.executescript(SCHEMA)


def save_snapshot(username: str, metrics: dict[str, Any], force: bool = False) -> bool:
    """保存一条快照。默认 10 分钟内只保存一次；force=True 时忽略间隔。"""
    if not username:
        return False
    now = datetime.now(timezone.utc)
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            if not force:
                row = conn.execute(
                    "SELECT created_at FROM snapshots WHERE username=? ORDER BY id DESC LIMIT 1",
                    (username,),
                ).fetchone()
                if row:
                    last = datetime.fromisoformat(row[0])
                    if (now - last).total_seconds() < SNAPSHOT_INTERVAL_SECONDS:
                        return False
            conn.execute(
                """INSERT INTO snapshots
                   (username, created_at, followers, following, total_stars, public_repos,
                    review_prs, my_prs, assigned_issues, events)
                   VALUES (?,?,?,?,?,?,?,?,?,?)""",
                (
                    username,
                    now.isoformat(),
                    int(metrics.get("followers", 0)),
                    int(metrics.get("following", 0)),
                    int(metrics.get("total_stars", 0)),
                    int(metrics.get("public_repos", 0)),
                    int(metrics.get("review_prs", 0)),
                    int(metrics.get("my_prs", 0)),
                    int(metrics.get("assigned_issues", 0)),
                    int(metrics.get("events", 0)),
                ),
            )
        return True


def get_history(username: str, limit: int = 200) -> list[dict[str, Any]]:
    """返回时间升序的历史快照。"""
    if not username or not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute(
            """SELECT created_at, followers, following, total_stars, public_repos,
                      review_prs, my_prs, assigned_issues, events
               FROM snapshots WHERE username=? ORDER BY created_at DESC LIMIT ?""",
            (username, limit),
        ).fetchall()
    result = [dict(r) for r in rows]
    result.reverse()
    return result


def get_stats(username: str) -> dict[str, Any]:
    """快照总数与时间范围。"""
    if not username or not DB_FILE.exists():
        return {"count": 0, "first": None, "last": None}
    with _lock, _connect() as conn:
        row = conn.execute(
            "SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM snapshots WHERE username=?",
            (username,),
        ).fetchone()
    return {"count": row[0], "first": row[1], "last": row[2]}
