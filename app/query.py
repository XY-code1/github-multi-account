"""账号列表：服务端搜索 / 筛选 / 排序 / 分页。

几百个账号时在内存中过滤已经足够快（亚毫秒级），但接口形态是服务端的：
只返回当前页数据，不把全部账号推给前端。
"""
from __future__ import annotations

import logging
from typing import Any

from . import config, db

logger = logging.getLogger(__name__)

SORT_FIELDS = {
    "last_activity": "last_sync_at",
    "last_sync": "last_sync_at",
    "quota": "remaining",
    "todos": "todos",
    "username": "username",
    "created": "id",
}

STATUS_ALIASES = {
    "healthy": "active",
    "warning": "warning",
    "invalid": "invalid",
    "error": "error",
    "sync_failed": "sync_failed",
}


def _merged_rows() -> list[dict[str, Any]]:
    accounts = config.load_accounts()
    summaries = {s["account_id"]: s for s in db.all_summaries()}
    states = {s["account_id"]: s for s in db.all_sync_states()}
    limits = db.all_rate_limits()

    rows = []
    for a in accounts:
        s = summaries.get(a["id"], {})
        st = states.get(a["id"], {})
        rl = limits.get(a["id"], {})
        status = s.get("status") or st.get("last_status") or "unknown"
        if (st.get("consecutive_failures") or 0) >= 3 and status == "active":
            status = "sync_failed"
        remaining = int(rl.get("remaining") or 0) if rl else 0
        rows.append(
            {
                "id": a["id"],
                "username": a.get("username", ""),
                "label": a.get("label", ""),
                "alias": a.get("alias", ""),
                "note": a.get("note", ""),
                "type": a.get("type", "personal"),
                "group": a.get("group", ""),
                "tags": a.get("tags", []),
                "status": status,
                "error": s.get("error") or st.get("last_error"),
                "last_sync_at": st.get("last_sync_at"),
                "last_status": st.get("last_status"),
                "consecutive_failures": int(st.get("consecutive_failures") or 0),
                "sync_enabled": st.get("sync_enabled") if st.get("sync_enabled") is not None else True,
                "remaining": remaining,
                "quota_limit": int(rl.get("quota_limit") or 0) if rl else 0,
                "review_prs": int(s.get("review_prs") or 0),
                "my_prs": int(s.get("my_prs") or 0),
                "assigned_issues": int(s.get("assigned_issues") or 0),
                "followers": int(s.get("followers") or 0),
                "following": int(s.get("following") or 0),
                "public_repos": int(s.get("public_repos") or 0),
                "total_stars": int(s.get("total_stars") or 0),
                "todos": int(s.get("review_prs") or 0) + int(s.get("assigned_issues") or 0),
                "token_masked": config.mask_token(a.get("token", "")),
            }
        )
    return rows


def query_accounts(
    page: int = 1,
    page_size: int = 50,
    query: str = "",
    status: str = "",
    type_: str = "",
    group: str = "",
    tags: str = "",
    sort: str = "last_activity",
    order: str = "desc",
) -> dict[str, Any]:
    rows = _merged_rows()

    # --- 搜索 ---
    q = (query or "").strip().lower()
    if q:
        rows = [
            r for r in rows
            if q in r["username"].lower()
            or q in r["label"].lower()
            or q in r["alias"].lower()
            or q in r["note"].lower()
            or any(q in t.lower() for t in r["tags"])
        ]

    # --- 筛选 ---
    if status:
        want = STATUS_ALIASES.get(status, status)
        rows = [r for r in rows if r["status"] == want]
    if type_:
        rows = [r for r in rows if r["type"] == type_]
    if group:
        rows = [r for r in rows if r["group"] == group]
    if tags:
        wanted = {t.strip().lower() for t in tags.split(",") if t.strip()}
        rows = [r for r in rows if wanted & {t.lower() for t in r["tags"]}]

    # --- 排序 ---
    key = SORT_FIELDS.get(sort, "last_sync_at")
    reverse = (order or "desc").lower() == "desc"

    def sort_key(r: dict[str, Any]) -> Any:
        v = r.get(key)
        if v is None:
            return "" if reverse else "￿"
        return v

    rows.sort(key=sort_key, reverse=reverse)

    total = len(rows)
    page = max(1, page)
    page_size = max(1, min(page_size, 200))
    start = (page - 1) * page_size
    items = rows[start:start + page_size]

    return {
        "items": items,
        "total": total,
        "page": page,
        "page_size": page_size,
        "total_pages": max(1, (total + page_size - 1) // page_size),
        "groups": sorted({r["group"] for r in _merged_rows() if r["group"]}),
        "tags": sorted({t for r in _merged_rows() for t in r["tags"]}),
    }
