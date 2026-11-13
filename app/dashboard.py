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
from .service import repo_short

logger = logging.getLogger(__name__)

PRIORITY_ORDER = {"high": 0, "medium": 1, "low": 2}

TASK_TYPES = {
    "credential": "凭据异常",
    "quota": "API 配额不足",
    "quota_capped": "API 已封顶",
    "cross_account": "跨账号关联",
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
    healthy = attention = credential_risk = quota_low = quota_capped = 0
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
        if rl:
            remaining = int(rl.get("remaining") or 0)
            if remaining < 100:
                quota_low += 1
            if int(rl.get("quota_limit") or 0) > 0 and remaining <= db.QUOTA_CAP:
                quota_capped += 1
        ts = st.get("last_sync_at")
        if ts and (last_sync is None or ts > last_sync):
            last_sync = ts

    return {
        "total": total,
        "healthy": healthy,
        "attention": attention,
        "credential_risk": credential_risk,
        "quota_low": quota_low,
        "quota_capped": quota_capped,
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
        if rl:
            rl_remaining = int(rl.get("remaining") or 0)
            rl_limit = int(rl.get("quota_limit") or 0)
            if rl_limit > 0 and rl_remaining <= db.QUOTA_CAP:
                tasks.append(_task("quota_capped", "high", "API 已封顶",
                                   f"剩余 {rl_remaining}/{rl_limit}（未认证额度仅 60/小时）",
                                   label, aid, acc.get("username", ""), rl.get("updated_at"), "检查凭据"))
            elif rl_remaining < 100:
                tasks.append(_task("quota", "medium", "API 配额不足",
                                   f"剩余 {rl_remaining}/{rl_limit}",
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

    # 跨账号关联：同一 PR / Issue 被多个账号看到时，合并成一条待办，
    # 避免同一个事项在不同账号下重复出现。
    item_accounts: dict[str, dict[str, Any]] = {}
    for other_id, s in summaries.items():
        payload = _load_payload(s)
        for key in ("review_prs", "my_prs", "assigned_issues"):
            for it in payload.get(key, []):
                url = it.get("html_url")
                if not url:
                    continue
                entry = item_accounts.setdefault(url, {"accounts": set(), "item": it})
                entry["accounts"].add(other_id)

    def _label_of(aid: int) -> str:
        s = summaries.get(aid, {})
        acc = accounts.get(aid, {})
        return s.get("label") or acc.get("label") or acc.get("username") or f"账号 {aid}"

    for url, entry in item_accounts.items():
        if len(entry["accounts"]) < 2:
            continue
        it = entry["item"]
        ids_sorted = sorted(entry["accounts"])
        tasks.append({
            "id": f"cross:{url}",
            "type": "cross_account",
            "type_label": "跨账号关联",
            "priority": "medium",
            "title": it.get("title") or url,
            "detail": f"{repo_short(it.get('repository_url', ''))}#{it.get('number', '')}"
                      f" · {len(entry['accounts'])} 个账号相关",
            "account_id": 0,
            "account_label": "、".join(_label_of(aid) for aid in ids_sorted),
            "account_username": "",
            "time": it.get("created_at"),
            "action": "打开",
            "url": url,
        })

    tasks.sort(key=lambda t: (PRIORITY_ORDER.get(t["priority"], 9), t.get("time") or ""))
    return tasks


CLUSTER_NAMES = ("核心", "活跃", "沉寂")


def _dist(a: list[float], b: list[float]) -> float:
    return sum((x - y) ** 2 for x, y in zip(a, b)) ** 0.5


def _kmeans(vectors: list[list[float]], k: int, iterations: int = 25) -> tuple[list[list[float]], list[int]]:
    """k-means：用最远点初始化，返回 (质心, 每个点的簇号)。"""
    n = len(vectors)
    dim = len(vectors[0])
    centroids = [list(vectors[0])]
    while len(centroids) < k:
        best, best_d = 0, -1.0
        for i, v in enumerate(vectors):
            d = min(_dist(v, c) for c in centroids)
            if d > best_d:
                best, best_d = i, d
        centroids.append(list(vectors[best]))
    assign = [0] * n
    for _ in range(iterations):
        changed = False
        for i, v in enumerate(vectors):
            d = [_dist(v, c) for c in centroids]
            c = d.index(min(d))
            if c != assign[i]:
                assign[i] = c
                changed = True
        for c in range(k):
            members = [vectors[i] for i in range(n) if assign[i] == c]
            if members:
                centroids[c] = [sum(m[d] for m in members) / len(members) for d in range(dim)]
        if not changed:
            break
    return centroids, assign


def build_profiles() -> dict[str, Any]:
    """账号画像：按影响力 / 活跃度自动聚类（k-means，k=3）。

    特征：粉丝、总 Star、公开仓库、待 Review PR、我的 PR、分配 Issue、动态数。
    结果写入 account_profile，并在账号列表里作为 cluster_label 展示。
    """
    accounts = config.load_accounts()
    summaries = {s["account_id"]: s for s in db.all_summaries()}

    ids: list[int] = []
    vectors: list[list[float]] = []
    for a in accounts:
        s = summaries.get(a["id"], {})
        events = _load_payload(s).get("events") or []
        vectors.append([
            float(s.get("followers") or 0),
            float(s.get("total_stars") or 0),
            float(s.get("public_repos") or 0),
            float(s.get("review_prs") or 0),
            float(s.get("my_prs") or 0),
            float(s.get("assigned_issues") or 0),
            float(len(events)),
        ])
        ids.append(a["id"])

    if not vectors:
        return {"clusters": [], "accounts": []}

    maxes = [max((v[i] for v in vectors), default=0.0) or 1.0 for i in range(len(vectors[0]))]
    norm = [[v[i] / maxes[i] for i in range(len(v))] for v in vectors]

    k = min(3, len(norm))
    centroids, assign = _kmeans(norm, k)

    order = sorted(range(k), key=lambda c: sum(centroids[c]), reverse=True)
    labels: dict[int, str] = {}
    for rank, c in enumerate(order):
        labels[c] = CLUSTER_NAMES[rank] if rank < len(CLUSTER_NAMES) else f"簇{rank}"

    accounts_out = []
    for i, aid in enumerate(ids):
        db.upsert_profile(aid, assign[i], labels[assign[i]], json.dumps(vectors[i]))
        accounts_out.append({
            "account_id": aid,
            "cluster": assign[i],
            "cluster_label": labels[assign[i]],
            "features": vectors[i],
        })

    clusters = [
        {
            "index": c,
            "label": labels[c],
            "size": sum(1 for a in assign if a == c),
            "centroid": [round(x, 4) for x in centroids[c]],
        }
        for c in range(k)
    ]
    return {"clusters": clusters, "accounts": accounts_out}


def build_graph() -> dict[str, Any]:
    """账号关系图谱：关注边 + 共同仓库边 + 共同关注边。

    节点是受管账号；边来自慢速同步入库的 follower/following 与代表仓库。
    """
    accounts = {a["id"]: a for a in config.load_accounts()}
    social = db.all_social()
    repos = db.all_repos()

    avatars: dict[int, str] = {}
    for s in db.all_summaries():
        avatars[s["account_id"]] = (_load_payload(s).get("user") or {}).get("avatar_url") or ""

    nodes = [
        {
            "id": aid,
            "username": a.get("username", ""),
            "label": a.get("label") or a.get("username"),
            "avatar_url": avatars.get(aid, ""),
        }
        for aid, a in accounts.items()
    ]

    login_to_id = {a.get("username", "").lower(): aid for aid, a in accounts.items()}
    following: dict[int, set[str]] = {}
    for row in social:
        if row["kind"] == "following":
            following.setdefault(row["account_id"], set()).add(row["login"].lower())

    edges: list[dict[str, Any]] = []
    # 按 (节点对, 类型) 去重：同一对账号可能同时存在关注与共同仓库关系
    seen: set[tuple[int, int, str]] = set()

    def add_edge(src: int, dst: int, type_: str, label: str) -> None:
        lo, hi = min(src, dst), max(src, dst)
        if lo == hi:
            return
        key = (lo, hi, type_)
        if key in seen:
            return
        seen.add(key)
        edges.append({"source": lo, "target": hi, "type": type_, "label": label})

    for aid, follows in following.items():
        for login in follows:
            target = login_to_id.get(login)
            if target is not None:
                add_edge(aid, target, "follows", "关注")

    repo_owners: dict[str, set[int]] = {}
    for row in repos:
        repo_owners.setdefault(row["name"].lower(), set()).add(row["account_id"])
    for name, owners in repo_owners.items():
        if len(owners) < 2:
            continue
        ids = sorted(owners)
        for i in range(len(ids)):
            for j in range(i + 1, len(ids)):
                add_edge(ids[i], ids[j], "shared_repo", f"共同仓库 {name}")

    ids = sorted(following)
    for i in range(len(ids)):
        for j in range(i + 1, len(ids)):
            common = following[ids[i]] & following[ids[j]]
            if len(common) >= 3:
                add_edge(ids[i], ids[j], "shared_following", f"共同关注 {len(common)}")

    return {"nodes": nodes, "edges": edges}


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
