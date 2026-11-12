"""全局聚合：账号总览、统一待办、最近活动。

所有数据都来自本地缓存（summaries / sync_state / rate_limits），
不实时请求 GitHub —— 几百个账号时不会打爆 API。
"""
from __future__ import annotations

import json
import logging
from datetime import datetime, timezone
from typing import Any

from . import config, db

logger = logging.getLogger(__name__)

PRIORITY_ORDER = {"high": 0, "medium": 1, "low": 2}

TASK_TYPES = {
    "credential": "凭据异常",
    "quota": "API 配额不足",
    "review_pr": "待我 Review 的 PR",
    "my_pr": "我发起的 PR",
    "issue": "分配给我的 Issue",
    "sync_failed": "同步失败",
    "sync_stale": "长期未同步",
}


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _load_payload(s: dict[str, Any]) -> dict[str, Any]:
    try:
        return json.loads(s.get("payload") or "{}")
    except Exception:  # noqa: BLE001
        return {}


def build_summary() -> dict[str, Any]:
    """账号总览统计。"""
    accounts = config.load_accounts()
    summaries = {s["account_id"]: s for s in db.all_summaries()}
    states = {s["account_id"]: s for s in db.all_sync_states()}
    limits = db.all_rate_limits()

    total = len(accounts)
    healthy = attention = credential_risk = quota_low = 0
    review_prs = my_prs = issues = 0
    last_sync: str | None = None

    for a in accounts:
        s = summaries.get(a["id"])
        st = states.get(a["id"], {})
        status = s["status"] if s else (st.get("last_status") or "unknown")
        if status == "active":
            healthy += 1
        else:
            attention += 1
        if status in ("invalid", "error"):
            credential_risk += 1
        if s:
            review_prs += int(s.get("review_prs") or 0)
            my_prs += int(s.get("my_prs") or 0)
            issues += int(s.get("assigned_issues") or 0)
        rl = limits.get(a["id"])
        if rl and int(rl.get("remaining") or 0) < 100:
            quota_low += 1
        ts = st.get("last_sync_at")
        if ts and (last_sync is None or ts > last_sync):
            last_sync = ts

    return {
        "total": total,
        "healthy": healthy,
        "attention": attention,
        "credential_risk": credential_risk,
        "quota_low": quota_low,
        "review_prs": review_prs,
        "my_prs": my_prs,
        "issues": issues,
        "last_sync_at": last_sync,
        "synced_accounts": len(summaries),
        "generated_at": _now(),
    }


def _task(
    type_: str,
    priority: str,
    title: str,
    detail: str,
    account_label: str,
    account_id: int,
    account_username: str = "",
    time: str | None = None,
    action: str = "",
) -> dict[str, Any]:
    return {
        "id": f"{type_}:{account_id}",
        "type": type_,
        "type_label": TASK_TYPES.get(type_, type_),
        "priority": priority,
        "title": title,
        "detail": detail,
        "account_id": account_id,
        "account_label": account_label,
        "account_username": account_username,
        "time": time,
        "action": action,
    }


def build_tasks() -> list[dict[str, Any]]:
    """统一待办：按优先级聚合所有账号的待处理事项。"""
    summaries = {s["account_id"]: s for s in db.all_summaries()}
    states = {s["account_id"]: s for s in db.all_sync_states()}
    limits = db.all_rate_limits()
    accounts = {a["id"]: a for a in config.load_accounts()}

    tasks: list[dict[str, Any]] = []
    for aid, s in summaries.items():
        acc = accounts.get(aid, {})
        label = s.get("label") or acc.get("label") or acc.get("username") or f"账号 {aid}"
        status = s.get("status")
        st = states.get(aid, {})
        rl = limits.get(aid, {})
        synced_at = st.get("last_sync_at")

        if status in ("invalid", "error"):
            tasks.append(_task("credential", "high", "凭据无效或已过期",
                               s.get("error") or "登录失败，请检查 Token",
                               label, aid, acc.get("username", ""), synced_at, "重新录入凭据"))
        if rl and int(rl.get("remaining") or 0) < 100:
            tasks.append(_task("quota", "medium", "API 配额不足",
                               f"剩余 {rl.get('remaining')}/{rl.get('quota_limit')}",
                               label, aid, acc.get("username", ""), rl.get("updated_at"), "查看配额"))
        if int(s.get("review_prs") or 0) > 0:
            tasks.append(_task("review_pr", "medium", f"{s['review_prs']} 个 PR 等待你 Review",
                               "有人请求你审查代码", label, aid, acc.get("username", ""), synced_at, "去 Review"))
        if int(s.get("my_prs") or 0) > 0:
            tasks.append(_task("my_pr", "low", f"{s['my_prs']} 个我发起的 PR 未关闭",
                               "关注 CI 与评审进展", label, aid, acc.get("username", ""), synced_at, "查看"))
        if int(s.get("assigned_issues") or 0) > 0:
            tasks.append(_task("issue", "medium", f"{s['assigned_issues']} 个 Issue 待处理",
                               "分配给你的未关闭 Issue", label, aid, acc.get("username", ""), synced_at, "去处理"))
        if int(st.get("consecutive_failures") or 0) >= 3:
            tasks.append(_task("sync_failed", "high", "连续多次同步失败",
                               st.get("last_error") or "请检查网络与凭据",
                               label, aid, acc.get("username", ""), st.get("updated_at"), "立即重试"))
        if synced_at:
            try:
                age = (datetime.now(timezone.utc) - datetime.fromisoformat(synced_at)).total_seconds()
                if age > 3600 * 6:
                    tasks.append(_task("sync_stale", "low", "超过 6 小时未同步",
                                       f"上次同步 {synced_at}", label, aid, acc.get("username", ""), synced_at, "立即同步"))
            except ValueError:
                pass

    tasks.sort(key=lambda t: (PRIORITY_ORDER.get(t["priority"], 9), t.get("time") or ""))
    return tasks


def build_activities(limit: int = 100, type_filter: str = "all") -> list[dict[str, Any]]:
    """跨账号汇总最近动态（来自缓存的 events），并聚合重复活动。"""
    collected: list[dict[str, Any]] = []
    for s in db.all_summaries():
        payload = _load_payload(s)
        for ev in payload.get("events", []):
            etype = ev.get("type", "")
            if type_filter == "pr" and "PullRequest" not in etype:
                continue
            if type_filter == "issue" and "Issue" not in etype:
                continue
            actor = ev.get("actor") or {}
            repo = ev.get("repo") or {}
            collected.append(
                {
                    "account_id": s["account_id"],
                    "account_username": s.get("username", ""),
                    "account_label": s.get("label") or s.get("username"),
                    "type": etype,
                    "actor": actor.get("login", ""),
                    "actor_avatar": actor.get("avatar_url", ""),
                    "repo": repo.get("name", ""),
                    "action": (ev.get("payload") or {}).get("action", ""),
                    "created_at": ev.get("created_at", ""),
                }
            )

    # 聚合重复活动：同账号 + 同仓库 + 同类型 + 同动作
    grouped: dict[tuple, dict[str, Any]] = {}
    order: list[tuple] = []
    for item in collected:
        key = (item["account_id"], item["repo"], item["type"], item["action"])
        if key not in grouped:
            grouped[key] = dict(item, count=1)
            order.append(key)
            continue
        g = grouped[key]
        g["count"] += 1
        if (item.get("created_at") or "") > (g.get("created_at") or ""):
            g["created_at"] = item["created_at"]

    out = [grouped[k] for k in order]
    out.sort(key=lambda x: x.get("created_at") or "", reverse=True)
    return out[:limit]
