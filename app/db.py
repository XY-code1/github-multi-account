"""运维中心数据库：同步状态、API 配额、审计日志、账号元数据。

只新增表，不修改既有 snapshots 表，保证可随时回滚：
删除本文件创建的四张表即回到旧版行为。

数据库文件：data/dashboard.db（已在 .gitignore 中）。
"""
from __future__ import annotations

import json
import logging
import os
import sqlite3
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from pathlib import Path
from threading import RLock
from typing import Any, Iterator

logger = logging.getLogger(__name__)

BASE_DIR = Path(__file__).resolve().parent.parent
DATA_DIR = Path(os.getenv("GAD_DATA_DIR", BASE_DIR / "data"))
DB_FILE = DATA_DIR / "dashboard.db"

_lock = RLock()

SCHEMA = """
CREATE TABLE IF NOT EXISTS sync_state (
    account_id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    last_sync_at TEXT,
    last_status TEXT DEFAULT 'unknown',
    last_error TEXT,
    consecutive_failures INTEGER DEFAULT 0,
    next_retry_at TEXT,
    sync_enabled INTEGER DEFAULT 1,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sync_status ON sync_state(last_status);
CREATE INDEX IF NOT EXISTS idx_sync_enabled ON sync_state(sync_enabled);

CREATE TABLE IF NOT EXISTS rate_limits (
    account_id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    remaining INTEGER DEFAULT 0,
    quota_limit INTEGER DEFAULT 0,
    reset_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,
    action TEXT NOT NULL,
    account_id INTEGER,
    username TEXT,
    detail TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_log(action);

CREATE TABLE IF NOT EXISTS account_meta (
    account_id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    type TEXT DEFAULT 'personal',
    tags TEXT DEFAULT '[]',
    note TEXT DEFAULT '',
    alias TEXT DEFAULT '',
    group_name TEXT DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_meta_username ON account_meta(username);
CREATE INDEX IF NOT EXISTS idx_meta_group ON account_meta(group_name);

CREATE TABLE IF NOT EXISTS summaries (
    account_id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    status TEXT DEFAULT 'unknown',
    label TEXT,
    type TEXT DEFAULT 'personal',
    group_name TEXT DEFAULT '',
    tags TEXT DEFAULT '[]',
    followers INTEGER DEFAULT 0,
    following INTEGER DEFAULT 0,
    public_repos INTEGER DEFAULT 0,
    review_prs INTEGER DEFAULT 0,
    my_prs INTEGER DEFAULT 0,
    assigned_issues INTEGER DEFAULT 0,
    total_stars INTEGER DEFAULT 0,
    payload TEXT,
    error TEXT,
    synced_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_summaries_status ON summaries(status);
CREATE INDEX IF NOT EXISTS idx_summaries_username ON summaries(username);
CREATE INDEX IF NOT EXISTS idx_summaries_group ON summaries(group_name);

-- 增量同步：按账号 + 端点缓存 ETag，命中 304 时不消耗 API 配额
CREATE TABLE IF NOT EXISTS api_cache (
    account_id INTEGER NOT NULL,
    endpoint TEXT NOT NULL,
    etag TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (account_id, endpoint)
);

-- 账号关系图谱：粉丝 / 关注（定期同步入库）
CREATE TABLE IF NOT EXISTS social (
    account_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    login TEXT NOT NULL,
    avatar_url TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (account_id, kind, login)
);
CREATE INDEX IF NOT EXISTS idx_social_login ON social(login);

-- 账号画像：仓库（用于关系图谱的「共同仓库」边）
CREATE TABLE IF NOT EXISTS account_repo (
    account_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    stars INTEGER DEFAULT 0,
    language TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (account_id, name)
);

-- 账号画像：自动聚类结果
CREATE TABLE IF NOT EXISTS account_profile (
    account_id INTEGER PRIMARY KEY,
    cluster_index INTEGER,
    cluster_label TEXT,
    features TEXT,
    updated_at TEXT NOT NULL
);

-- 出站 Webhook 投递日志
CREATE TABLE IF NOT EXISTS webhook_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,
    url TEXT NOT NULL,
    event_type TEXT NOT NULL,
    ok INTEGER DEFAULT 0,
    detail TEXT
);

-- 邮箱面板：本地缓存的邮件（仅用于展示，不参与任何自动填充）
CREATE TABLE IF NOT EXISTS email_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER,
    inbox_id INTEGER,
    email TEXT,
    message_id TEXT,
    sender TEXT,
    subject TEXT,
    body TEXT,
    code TEXT,
    received_at TEXT,
    created_at TEXT
);

-- 邮箱面板：独立邮箱清单（不依赖账号，如邮笺导出的取件链接）
CREATE TABLE IF NOT EXISTS email_inboxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT,
    url TEXT,
    created_at TEXT
);
"""

ACCOUNT_TYPES = ("personal", "bot", "org")

# API 剩余不超过该值视为「已封顶」：正常 Token 是 5000/小时，
# 而未认证请求只有 60/小时，所以 60 基本意味着 Token 没生效或已耗尽。
QUOTA_CAP = 60


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
        # 轻量迁移：旧库补列（CREATE IF NOT EXISTS 不会改已有表结构）
        cols = {row[1] for row in conn.execute("PRAGMA table_info(email_messages)")}
        if "inbox_id" not in cols:
            conn.execute("ALTER TABLE email_messages ADD COLUMN inbox_id INTEGER")


# ------------------------------ 邮箱面板 ------------------------------

def add_email_message(
    *,
    account_id: int | None,
    email: str,
    message_id: str,
    sender: str,
    subject: str,
    body: str,
    received_at: str = "",
    code: str | None = None,
    inbox_id: int | None = None,
) -> bool:
    """保存一封邮件；同账号/邮箱同 message_id 视为重复，返回是否新增。"""
    if not DB_FILE.exists():
        init_db()
    now = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        if message_id:
            row = conn.execute(
                "SELECT 1 FROM email_messages"
                " WHERE (account_id IS ? OR inbox_id IS ?) AND message_id=?",
                (account_id, inbox_id, message_id),
            ).fetchone()
            if row:
                return False
        conn.execute(
            "INSERT INTO email_messages"
            " (account_id, inbox_id, email, message_id, sender, subject, body, code, received_at, created_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?)",
            (
                account_id,
                inbox_id,
                email or "",
                message_id or "",
                sender or "",
                subject or "",
                body or "",
                code,
                received_at or "",
                now,
            ),
        )
    return True


def list_email_messages(
    account_id: int | None = None,
    q: str = "",
    limit: int = 100,
    inbox_id: int | None = None,
    sort: str = "time",
    has_code: bool = False,
) -> list[dict[str, Any]]:
    """列出本地缓存邮件（可按账号 / 独立邮箱 / 关键字筛选，支持验证码优先排序）。"""
    if not DB_FILE.exists():
        return []
    limit = max(1, min(int(limit or 100), 500))
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        sql = (
            "SELECT id, account_id, inbox_id, email, message_id, sender, subject, body, code,"
            " received_at, created_at FROM email_messages"
        )
        cond: list[str] = []
        params: list[Any] = []
        if account_id:
            cond.append("account_id=?")
            params.append(account_id)
        if inbox_id:
            cond.append("inbox_id=?")
            params.append(inbox_id)
        if has_code:
            cond.append("(code IS NOT NULL AND code<>'')")
        if q:
            cond.append("(subject LIKE ? OR body LIKE ? OR sender LIKE ? OR email LIKE ?)")
            like = f"%{q}%"
            params += [like, like, like, like]
        if cond:
            sql += " WHERE " + " AND ".join(cond)
        if sort == "code":
            sql += (" ORDER BY (CASE WHEN code IS NOT NULL AND code<>'' THEN 0 ELSE 1 END),"
                    " datetime(received_at) DESC, id DESC LIMIT ?")
        else:
            sql += " ORDER BY datetime(received_at) DESC, id DESC LIMIT ?"
        params.append(limit)
        rows = conn.execute(sql, params).fetchall()
    return [dict(r) for r in rows]


def delete_email_messages(
    account_id: int | None = None, ids: list[int] | None = None
) -> int:
    """删除邮件（按账号或指定 ID），返回删除条数。"""
    if not DB_FILE.exists():
        return 0
    cond: list[str] = []
    params: list[Any] = []
    if account_id:
        cond.append("account_id=?")
        params.append(account_id)
    if ids:
        cond.append(f"id IN ({','.join('?' * len(ids))})")
        params += list(ids)
    if not cond:
        return 0
    with _lock, _connect() as conn:
        cur = conn.execute(
            f"DELETE FROM email_messages WHERE {' AND '.join(cond)}", params
        )
    return cur.rowcount or 0


# ------------------------------ 独立邮箱清单 ------------------------------

def add_inbox(email: str, url: str) -> dict[str, Any] | None:
    """新增独立邮箱（同 url 去重），返回记录。"""
    if not DB_FILE.exists():
        init_db()
    email = (email or "").strip()
    url = (url or "").strip()
    if not url:
        return None
    now = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        row = conn.execute("SELECT id FROM email_inboxes WHERE url=?", (url,)).fetchone()
        if row:
            return None
        cur = conn.execute(
            "INSERT INTO email_inboxes (email, url, created_at) VALUES (?,?,?)",
            (email, url, now),
        )
        return {"id": cur.lastrowid, "email": email, "url": url, "created_at": now}


def list_inboxes() -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute(
            "SELECT id, email, url, created_at FROM email_inboxes ORDER BY id DESC"
        ).fetchall()
    return [dict(r) for r in rows]


def delete_inbox(inbox_id: int) -> bool:
    if not DB_FILE.exists():
        return False
    with _lock, _connect() as conn:
        cur = conn.execute("DELETE FROM email_inboxes WHERE id=?", (inbox_id,))
    return (cur.rowcount or 0) > 0


# ------------------------------ 审计日志 ------------------------------

def audit(action: str, account_id: int | None = None, username: str | None = None, detail: str = "") -> None:
    """记录审计日志。detail 必须不含 Token 等敏感信息。"""
    ts = datetime.now(timezone.utc).isoformat()
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            conn.execute(
                "INSERT INTO audit_log (ts, action, account_id, username, detail) VALUES (?,?,?,?,?)",
                (ts, action, account_id, username, detail[:500]),
            )


def list_audit(limit: int = 100, action: str | None = None) -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        if action:
            rows = conn.execute(
                "SELECT ts, action, account_id, username, detail FROM audit_log "
                "WHERE action=? ORDER BY id DESC LIMIT ?",
                (action, limit),
            ).fetchall()
        else:
            rows = conn.execute(
                "SELECT ts, action, account_id, username, detail FROM audit_log "
                "ORDER BY id DESC LIMIT ?",
                (limit,),
            ).fetchall()
    return [dict(r) for r in rows]


def prune_audit(limit: int = 5000, max_days: int = 90) -> int:
    """清理审计日志：只保留最近 limit 条，且不超过 max_days 天。

    审计日志是只增不减的，长期运行会无限膨胀，启动时调用一次即可。
    """
    if not DB_FILE.exists():
        return 0
    cutoff = (datetime.now(timezone.utc) - timedelta(days=max_days)).isoformat()
    with _lock, _connect() as conn:
        expired = conn.execute("DELETE FROM audit_log WHERE ts < ?", (cutoff,))
        expired_count = expired.rowcount
        overflow = conn.execute(
            """DELETE FROM audit_log WHERE id NOT IN (
                   SELECT id FROM audit_log ORDER BY id DESC LIMIT ?
               )""",
            (limit,),
        )
        return expired_count + overflow.rowcount


# ------------------------------ 同步状态 ------------------------------

def get_sync_state(account_id: int) -> dict[str, Any] | None:
    if not DB_FILE.exists():
        return None
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        row = conn.execute(
            "SELECT * FROM sync_state WHERE account_id=?", (account_id,)
        ).fetchone()
    return dict(row) if row else None


def all_sync_states() -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT * FROM sync_state ORDER BY account_id").fetchall()
    return [dict(r) for r in rows]


def upsert_sync_state(account_id: int, username: str, **fields: Any) -> None:
    """写入同步状态；type / group_name / tags 会同步到 account_meta。"""
    ts = datetime.now(timezone.utc).isoformat()
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            conn.execute(
                """INSERT INTO sync_state
                   (account_id, username, last_sync_at, last_status, last_error,
                    consecutive_failures, next_retry_at, sync_enabled, updated_at)
                   VALUES (?,?,?,?,?,?,?,?,?)
                   ON CONFLICT(account_id) DO UPDATE SET
                     username=excluded.username,
                     last_sync_at=COALESCE(excluded.last_sync_at, last_sync_at),
                     last_status=COALESCE(excluded.last_status, last_status),
                     last_error=excluded.last_error,
                     consecutive_failures=COALESCE(excluded.consecutive_failures, 0),
                     next_retry_at=excluded.next_retry_at,
                     sync_enabled=COALESCE(excluded.sync_enabled, sync_enabled),
                     updated_at=excluded.updated_at
                """,
                (
                    account_id,
                    username,
                    fields.get("last_sync_at"),
                    fields.get("last_status"),
                    fields.get("last_error"),
                    fields.get("consecutive_failures"),
                    fields.get("next_retry_at"),
                    1 if fields.get("sync_enabled", True) else 0,
                    ts,
                ),
            )
            if "type" in fields or "group_name" in fields or "tags" in fields:
                tags = fields.get("tags") or []
                conn.execute(
                    """INSERT INTO account_meta
                       (account_id, username, type, tags, note, alias, group_name, updated_at)
                       VALUES (?,?,?,?,?,?,?,?)
                       ON CONFLICT(account_id) DO UPDATE SET
                         username=excluded.username, type=excluded.type, tags=excluded.tags,
                         note=excluded.note, alias=excluded.alias, group_name=excluded.group_name,
                         updated_at=excluded.updated_at
                    """,
                    (
                        account_id,
                        username,
                        fields.get("type") or "personal",
                        json.dumps(tags, ensure_ascii=False),
                        fields.get("note") or "",
                        fields.get("alias") or "",
                        fields.get("group_name") or "",
                        ts,
                    ),
                )


def set_sync_enabled(account_id: int, enabled: bool, username: str | None = None) -> None:
    """启用 / 暂停某账号的同步。

    账号从未同步过（sync_state 里没有行）时也会创建状态行，
    否则暂停操作会静默失效。
    """
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute(
            "INSERT INTO sync_state (account_id, username, sync_enabled, updated_at) "
            "VALUES (?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET "
            "sync_enabled=excluded.sync_enabled, updated_at=excluded.updated_at",
            (account_id, username or "", 1 if enabled else 0, ts),
        )


def clear_sync_failure(account_id: int) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute(
            "UPDATE sync_state SET consecutive_failures=0, next_retry_at=NULL, updated_at=? WHERE account_id=?",
            (ts, account_id),
        )


# ------------------------------ API 配额 ------------------------------

def upsert_rate_limit(account_id: int, username: str, remaining: int, limit: int, reset_at: str | None) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            conn.execute(
                """INSERT INTO rate_limits (account_id, username, remaining, quota_limit, reset_at, updated_at)
                   VALUES (?,?,?,?,?,?)
                   ON CONFLICT(account_id) DO UPDATE SET
                     username=excluded.username, remaining=excluded.remaining,
                     quota_limit=excluded.quota_limit, reset_at=excluded.reset_at, updated_at=excluded.updated_at
                """,
                (account_id, username, remaining, limit, reset_at, ts),
            )


def all_rate_limits() -> dict[int, dict[str, Any]]:
    if not DB_FILE.exists():
        return {}
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT * FROM rate_limits").fetchall()
    return {r["account_id"]: dict(r) for r in rows}


# ------------------------------ 出站 Webhook 日志 ------------------------------

def log_webhook(url: str, event_type: str, ok: bool, detail: str = "") -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute(
            "INSERT INTO webhook_log (ts, url, event_type, ok, detail) VALUES (?,?,?,?,?)",
            (ts, url, event_type, 1 if ok else 0, detail),
        )


def list_webhook_log(limit: int = 50) -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute(
            "SELECT ts, url, event_type, ok, detail FROM webhook_log ORDER BY id DESC LIMIT ?",
            (limit,),
        ).fetchall()
    return [dict(r) for r in rows]


# ------------------------------ 关系图谱数据 ------------------------------

def replace_social(account_id: int, kind: str, entries: list[dict]) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute("DELETE FROM social WHERE account_id=? AND kind=?", (account_id, kind))
        for e in entries:
            conn.execute(
                "INSERT INTO social (account_id, kind, login, avatar_url, updated_at) VALUES (?,?,?,?,?)",
                (account_id, kind, e.get("login", ""), e.get("avatar_url", ""), ts),
            )


def replace_repos(account_id: int, repos: list[dict]) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute("DELETE FROM account_repo WHERE account_id=?", (account_id,))
        for r in repos:
            conn.execute(
                "INSERT INTO account_repo (account_id, name, stars, language, updated_at) VALUES (?,?,?,?,?)",
                (account_id, r.get("name", ""), int(r.get("stargazers_count", 0)), r.get("language") or "", ts),
            )


def all_social() -> list[dict]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT account_id, kind, login, avatar_url FROM social").fetchall()
    return [dict(r) for r in rows]


def all_repos() -> list[dict]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT account_id, name, stars, language FROM account_repo").fetchall()
    return [dict(r) for r in rows]


# ------------------------------ 账号画像 ------------------------------

def upsert_profile(account_id: int, cluster_index: int, cluster_label: str, features: str) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute(
            """INSERT INTO account_profile (account_id, cluster_index, cluster_label, features, updated_at)
               VALUES (?,?,?,?,?)
               ON CONFLICT(account_id) DO UPDATE SET
                 cluster_index=excluded.cluster_index, cluster_label=excluded.cluster_label,
                 features=excluded.features, updated_at=excluded.updated_at
            """,
            (account_id, cluster_index, cluster_label, features, ts),
        )


def all_profiles() -> dict[int, dict]:
    if not DB_FILE.exists():
        return {}
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT account_id, cluster_index, cluster_label FROM account_profile").fetchall()
    return {r["account_id"]: dict(r) for r in rows}


# ------------------------------ 增量同步 ETag ------------------------------

def get_etag(account_id: int, endpoint: str) -> str | None:
    if not DB_FILE.exists():
        return None
    with _lock, _connect() as conn:
        row = conn.execute(
            "SELECT etag FROM api_cache WHERE account_id=? AND endpoint=?",
            (account_id, endpoint),
        ).fetchone()
    return row[0] if row else None


def put_etag(account_id: int, endpoint: str, etag: str | None) -> None:
    ts = datetime.now(timezone.utc).isoformat()
    with _lock, _connect() as conn:
        conn.execute(
            """INSERT INTO api_cache (account_id, endpoint, etag, updated_at)
               VALUES (?,?,?,?)
               ON CONFLICT(account_id, endpoint) DO UPDATE SET
                 etag=excluded.etag, updated_at=excluded.updated_at
            """,
            (account_id, endpoint, etag, ts),
        )


def clear_etags(account_id: int | None = None) -> None:
    """清除 ETag 缓存（账号删除或 Token 变更时）。"""
    with _lock, _connect() as conn:
        if account_id is None:
            conn.execute("DELETE FROM api_cache")
        else:
            conn.execute("DELETE FROM api_cache WHERE account_id=?", (account_id,))


# ------------------------------ 账号元数据 ------------------------------

def rebuild_meta(accounts: list[dict[str, Any]]) -> None:
    """从账号配置重建元数据镜像（搜索/筛选用）。"""
    ts = datetime.now(timezone.utc).isoformat()
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            conn.execute("DELETE FROM account_meta")
            for a in accounts:
                tags = a.get("tags") or []
                if isinstance(tags, str):
                    tags = [t.strip() for t in tags.split(",") if t.strip()]
                conn.execute(
                    """INSERT INTO account_meta
                       (account_id, username, type, tags, note, alias, group_name, updated_at)
                       VALUES (?,?,?,?,?,?,?,?)""",
                    (
                        a["id"],
                        a.get("username", ""),
                        a.get("type") or "personal",
                        json.dumps(tags, ensure_ascii=False),
                        a.get("note") or "",
                        a.get("alias") or "",
                        a.get("group") or "",
                        ts,
                    ),
                )


def all_meta() -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT * FROM account_meta ORDER BY account_id").fetchall()
    out = []
    for r in rows:
        d = dict(r)
        try:
            d["tags"] = json.loads(d.get("tags") or "[]")
        except Exception:  # noqa: BLE001
            d["tags"] = []
        out.append(d)
    return out


# ------------------------------ 汇总缓存 ------------------------------

def upsert_summary(account_id: int, username: str, summary: dict[str, Any]) -> None:
    """把一次同步的汇总结果写入缓存（供仪表盘读取，避免实时请求 GitHub）。"""
    ts = datetime.now(timezone.utc).isoformat()
    payload = json.dumps(summary, ensure_ascii=False)
    with _lock:
        if not DB_FILE.exists():
            init_db()
        with _connect() as conn:
            conn.execute(
                """INSERT INTO summaries
                   (account_id, username, status, label, type, group_name, tags,
                    followers, following, public_repos, review_prs, my_prs,
                    assigned_issues, total_stars, payload, error, synced_at)
                   VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
                   ON CONFLICT(account_id) DO UPDATE SET
                     username=excluded.username, status=excluded.status, label=excluded.label,
                     type=excluded.type, group_name=excluded.group_name, tags=excluded.tags,
                     followers=excluded.followers, following=excluded.following,
                     public_repos=excluded.public_repos, review_prs=excluded.review_prs,
                     my_prs=excluded.my_prs, assigned_issues=excluded.assigned_issues,
                     total_stars=excluded.total_stars, payload=excluded.payload,
                     error=excluded.error, synced_at=excluded.synced_at
                """,
                (
                    account_id,
                    username,
                    summary.get("status", "unknown"),
                    summary.get("label"),
                    summary.get("type", "personal"),
                    summary.get("group_name", ""),
                    json.dumps(summary.get("tags", []), ensure_ascii=False),
                    int(summary.get("followers_count", 0)),
                    int(summary.get("following_count", 0)),
                    int(summary.get("public_repos", 0)),
                    int(summary.get("review_prs_count", 0)),
                    int(summary.get("my_prs_count", 0)),
                    int(summary.get("assigned_issues_count", 0)),
                    int(summary.get("total_stars", 0)),
                    payload,
                    summary.get("error"),
                    ts,
                ),
            )


def all_summaries() -> list[dict[str, Any]]:
    if not DB_FILE.exists():
        return []
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute("SELECT * FROM summaries ORDER BY account_id").fetchall()
    out = []
    for r in rows:
        d = dict(r)
        try:
            d["tags"] = json.loads(d.get("tags") or "[]")
        except Exception:  # noqa: BLE001
            d["tags"] = []
        out.append(d)
    return out


def get_summary(account_id: int) -> dict[str, Any] | None:
    if not DB_FILE.exists():
        return None
    with _lock, _connect() as conn:
        conn.row_factory = sqlite3.Row
        row = conn.execute("SELECT * FROM summaries WHERE account_id=?", (account_id,)).fetchone()
    if not row:
        return None
    d = dict(row)
    try:
        d["tags"] = json.loads(d.get("tags") or "[]")
    except Exception:  # noqa: BLE001
        d["tags"] = []
    return d


def get_summary_dict(account_id: int) -> dict[str, Any]:
    """返回汇总缓存的 payload（解析后的 dict），供增量同步做 304 兜底。"""
    row = get_summary(account_id)
    if not row:
        return {}
    try:
        return json.loads(row.get("payload") or "{}")
    except Exception:  # noqa: BLE001
        return {}
