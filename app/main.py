"""GitHub 多账号状态仪表盘 —— FastAPI 后端。

启动：
    pip install -r requirements.txt
    cp config/accounts.example.yaml config/accounts.yaml  # 填入 token
    uvicorn app.main:app --reload --port 8000

然后浏览器打开 http://localhost:8000
"""
from __future__ import annotations

import asyncio
import json
import logging
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

from fastapi import FastAPI, File, Form, HTTPException, Query, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from . import config, dashboard, db, docx, notify, service, store, sync
from .query import query_accounts

logger = logging.getLogger("ghdashboard")

# 外部通知轮询器（有渠道配置时才启动）
poller = notify.NotificationPoller(interval_seconds=300)

# 后台同步引擎（有账号时启动）
sync_engine = sync.SyncEngine()

BASE_DIR = Path(__file__).resolve().parent.parent
STATIC_DIR = BASE_DIR / "static"
TEMPLATES_DIR = BASE_DIR / "templates"


@asynccontextmanager
async def lifespan(app: FastAPI):
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    store.init_db()
    try:
        pruned = store.prune_snapshots()
        if pruned.get("deleted"):
            logger.info("已清理历史快照 %d 条", pruned["deleted"])
    except Exception as e:  # noqa: BLE001
        logger.warning("快照清理失败: %s", e)
    try:
        audited = db.prune_audit()
        if audited:
            logger.info("已清理审计日志 %d 条", audited)
    except Exception as e:  # noqa: BLE001
        logger.warning("审计日志清理失败: %s", e)
    if notify.load_channels():
        poller.start()
        logger.info("外部通知轮询已启动")
    if config.load_accounts():
        try:
            db.rebuild_meta(config.load_accounts())
        except Exception as e:  # noqa: BLE001
            logger.warning("重建账号元数据失败: %s", e)
        sync_engine.start()
        logger.info("后台同步引擎已启动（间隔 %ss，并发 %s）",
                    sync_engine.cfg["interval_seconds"], sync_engine.cfg["concurrency"])
    logger.info("GitHub 多账号仪表盘已启动")
    yield
    await poller.stop()
    await sync_engine.stop()


app = FastAPI(title="GitHub 多账号状态仪表盘", version="1.3.0", lifespan=lifespan)


@app.middleware("http")
async def block_cross_site_writes(request: Request, call_next):
    """拒绝跨站发起的写操作（防止 CSRF 触发快照 / 消耗 GitHub 配额）。"""
    if request.method in ("POST", "PUT", "PATCH", "DELETE"):
        if request.headers.get("sec-fetch-site") == "cross-site":
            return JSONResponse(status_code=403, content={"detail": "跨站请求被拒绝"})
    return await call_next(request)


@app.exception_handler(config.ConfigError)
async def config_error_handler(request: Request, exc: config.ConfigError):
    return JSONResponse(status_code=500, content={"detail": str(exc)})


# ------------------------------ 请求模型 ------------------------------

class AccountIn(BaseModel):
    label: str = ""
    username: str
    token: str
    group: str = ""
    type: str = "personal"
    tags: list[str] = []
    note: str = ""
    alias: str = ""


class ExportIn(BaseModel):
    password: str = ""


# ------------------------------ 账号管理 ------------------------------

@app.post("/api/accounts")
async def create_account(payload: AccountIn) -> dict[str, Any]:
    """新增或更新账号（同 username 覆盖）。"""
    if not payload.username:
        raise HTTPException(status_code=400, detail="username 不能为空")
    if not payload.token:
        raise HTTPException(status_code=400, detail="token 不能为空")
    acc = config.add_account(
        payload.label,
        payload.username,
        payload.token,
        payload.group or "",
        type_=payload.type or "personal",
        alias=payload.alias or "",
        note=payload.note or "",
        tags=payload.tags or [],
    )
    db.clear_etags(acc["id"])
    db.audit("accounts.create", acc["id"], acc["username"], "新增/更新账号")
    return {
        "id": acc["id"],
        "label": acc["label"],
        "username": acc["username"],
        "group": acc.get("group", ""),
        "type": acc.get("type", "personal"),
        "token_masked": config.mask_token(acc["token"]),
    }


@app.delete("/api/accounts/{account_id}")
async def remove_account(account_id: int) -> dict[str, Any]:
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    username = target.get("username", "")
    if not config.delete_account(account_id):
        raise HTTPException(status_code=404, detail="账号不存在")
    if username:
        store.delete_history(username)
    db.clear_etags(account_id)
    db.audit("accounts.delete", account_id, username, "删除账号")
    return {"ok": True}


# ------------------------------ 状态聚合 ------------------------------

@app.post("/api/verify")
async def verify(payload: AccountIn) -> dict[str, Any]:
    """测试连接：仅验证 token 有效性，不保存。"""
    return await service.verify_token(payload.username, payload.token)


@app.get("/api/status")
async def all_status() -> dict[str, Any]:
    """全部账号的汇总（侧栏用，轻量）。"""
    accounts = config.load_accounts()
    if not accounts:
        return {"accounts": []}
    results = await service.fetch_all_summaries(accounts)
    return {"accounts": results}


@app.get("/api/status/{account_id}")
async def account_status(account_id: int) -> dict[str, Any]:
    """单个账号的完整详情。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await service.fetch_account_detail(target)
    return {"account": result}


@app.post("/api/accounts/{account_id}/verify")
async def verify_account(account_id: int) -> dict[str, Any]:
    """仅验证登录有效性（轻量，不拉 PR/Issue）。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await service.fetch_account_summary(target)
    return {
        "id": result["id"],
        "status": result["status"],
        "user": result["user"],
        "error": result["error"],
    }


@app.get("/api/version")
async def version() -> dict[str, Any]:
    """当前程序版本与仓库地址。"""
    return {
        "version": "1.3.0",
        "repo": "https://github.com/XY-code1/github-multi-account",
    }


@app.get("/api/search")
async def search(q: str = "", kind: str = "all") -> dict[str, Any]:
    """跨账号搜索 PR / Issue 标题。kind: all | pr | issue"""
    accounts = config.load_accounts()
    return await service.global_search(accounts, q, kind)


# ------------------------------ 运维中心 ------------------------------

@app.get("/api/dashboard/summary")
async def dashboard_summary() -> dict[str, Any]:
    """全局账号总览。"""
    return dashboard.build_summary()


@app.get("/api/dashboard/tasks")
async def dashboard_tasks(priority: str = "", type: str = "", accountId: str = "") -> dict[str, Any]:
    """统一待办（按优先级排序，支持按优先级 / 类型 / 账号筛选）。"""
    tasks = dashboard.build_tasks()
    if priority:
        tasks = [t for t in tasks if t["priority"] == priority]
    if type:
        tasks = [t for t in tasks if t["type"] == type]
    if accountId:
        try:
            aid = int(accountId)
        except ValueError:
            aid = None
        if aid is not None:
            tasks = [t for t in tasks if t["account_id"] == aid]
    return {
        "tasks": tasks,
        "total": len(tasks),
        "high": sum(1 for t in tasks if t["priority"] == "high"),
        "medium": sum(1 for t in tasks if t["priority"] == "medium"),
        "low": sum(1 for t in tasks if t["priority"] == "low"),
    }


@app.get("/api/dashboard/activities")
async def dashboard_activities(type: str = "all", limit: int = 100) -> dict[str, Any]:
    """跨账号最近动态。type: all | pr | issue | account"""
    limit = max(1, min(limit, 500))
    return {"activities": dashboard.build_activities(limit, type)}


@app.get("/api/accounts")
async def list_accounts(
    page: int = 1,
    pageSize: int = 50,
    query: str = "",
    status: str = "",
    type: str = "",
    group: str = "",
    tags: str = "",
    sort: str = "last_activity",
    order: str = "desc",
) -> dict[str, Any]:
    """账号列表（服务端分页 / 搜索 / 筛选 / 排序）。"""
    return query_accounts(
        page=page,
        page_size=pageSize,
        query=query,
        status=status,
        type_=type,
        group=group,
        tags=tags,
        sort=sort,
        order=order,
    )


@app.post("/api/accounts/batch-refresh")
async def batch_refresh(payload: dict[str, Any]) -> dict[str, Any]:
    """批量刷新指定账号。"""
    ids = payload.get("ids") or []
    if not isinstance(ids, list) or not ids:
        raise HTTPException(status_code=400, detail="ids 必须是非空列表")
    db.audit("accounts.batch_refresh", detail=f"批量刷新 {len(ids)} 个账号")
    result = await sync_engine.sync_all(only_ids=[int(i) for i in ids])
    return {"ok": True, **result}


@app.post("/api/accounts/batch-toggle-sync")
async def batch_toggle_sync(payload: dict[str, Any]) -> dict[str, Any]:
    """批量启用 / 暂停账号同步。"""
    ids = payload.get("ids") or []
    enabled = bool(payload.get("enabled", True))
    if not isinstance(ids, list) or not ids:
        raise HTTPException(status_code=400, detail="ids 必须是非空列表")
    accounts = {a["id"]: a for a in config.load_accounts()}
    for aid in ids:
        aid = int(aid)
        db.set_sync_enabled(aid, enabled, accounts.get(aid, {}).get("username"))
    db.audit(
        "accounts.batch_toggle_sync",
        detail=f"批量{'启用' if enabled else '暂停'} {len(ids)} 个账号的同步",
    )
    return {"ok": True, "updated": len(ids)}


@app.post("/api/accounts/batch-tag")
async def batch_tag(payload: dict[str, Any]) -> dict[str, Any]:
    """批量给账号追加标签。"""
    ids = payload.get("ids") or []
    tags = payload.get("tags") or []
    if not isinstance(ids, list) or not ids:
        raise HTTPException(status_code=400, detail="ids 必须是非空列表")
    clean = [t.strip() for t in tags if isinstance(t, str) and t.strip()]
    if not clean:
        raise HTTPException(status_code=400, detail="tags 必须是非空列表")
    wanted = {int(i) for i in ids}
    accounts = config.load_accounts()
    changed = 0
    for acc in accounts:
        if acc["id"] in wanted:
            merged = list(acc.get("tags") or [])
            for t in clean:
                if t not in merged:
                    merged.append(t)
            acc["tags"] = merged
            changed += 1
    if changed:
        config.save_accounts(accounts)
        db.rebuild_meta(config.load_accounts())
    db.audit("accounts.batch_tag", detail=f"批量打标签 {changed} 个账号：{', '.join(clean)}")
    return {"ok": True, "updated": changed}


@app.post("/api/accounts/batch-group")
async def batch_group(payload: dict[str, Any]) -> dict[str, Any]:
    """批量把账号移动到指定分组。"""
    ids = payload.get("ids") or []
    group = str(payload.get("group", "")).strip()
    if not isinstance(ids, list) or not ids:
        raise HTTPException(status_code=400, detail="ids 必须是非空列表")
    if not group:
        raise HTTPException(status_code=400, detail="group 不能为空")
    wanted = {int(i) for i in ids}
    accounts = config.load_accounts()
    changed = 0
    for acc in accounts:
        if acc["id"] in wanted:
            acc["group"] = group
            changed += 1
    if changed:
        config.save_accounts(accounts)
        db.rebuild_meta(config.load_accounts())
    db.audit("accounts.batch_group", detail=f"批量移动到分组「{group}」：{changed} 个账号")
    return {"ok": True, "updated": changed}


def _select_candidates(candidates: list[docx.Candidate], indices: str) -> list[docx.Candidate]:
    if not indices.strip():
        return []
    try:
        want = {int(x) for x in indices.split(",") if x.strip() != ""}
    except ValueError:
        raise HTTPException(status_code=400, detail="indices 格式不正确，应为逗号分隔的数字")
    return [c for c in candidates if c.index in want]


def _parse_overrides(raw: str) -> dict[int, dict[str, str]]:
    try:
        data = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        return {}
    if not isinstance(data, dict):
        return {}
    out: dict[int, dict[str, str]] = {}
    for k, v in data.items():
        if isinstance(v, dict):
            try:
                out[int(k)] = {
                    "username": str(v.get("username", "")),
                    "label": str(v.get("label", "")),
                }
            except (TypeError, ValueError):
                continue
    return out


async def _verify_candidates(candidates: list[docx.Candidate]) -> list[docx.Candidate]:
    """并发验证 Token（并发上限 5），用 GitHub 返回的 login 作为权威用户名。"""
    sem = asyncio.Semaphore(5)

    async def one(c: docx.Candidate) -> docx.Candidate:
        async with sem:
            try:
                status, user, msg = await service.verify_token(c.username, c.token)
            except Exception as e:  # noqa: BLE001
                c.valid, c.error = False, str(e)
                return c
            if status == "active" and user:
                login = user.get("login") or c.username
                c.username = login
                c.label = c.label or login
                c.valid, c.error = True, ""
            else:
                c.valid, c.error = False, msg
            return c

    return list(await asyncio.gather(*(one(c) for c in candidates)))


@app.post("/api/accounts/import-docx")
async def import_docx(
    file: UploadFile = File(...),
    preview: bool = Query(False, description="true=仅预览不写库"),
    indices: str = Query("", description="要导入的候选序号（逗号分隔）"),
    overrides: str = Form("{}", description="前端编辑的用户名 / 显示名覆盖"),
) -> dict[str, Any]:
    """上传 .docx，识别 Token 与用户名；preview=true 只返回预览。

    Token 只在服务端使用，响应里只回脱敏值。
    """
    data = await file.read()
    if not data:
        raise HTTPException(status_code=400, detail="文件为空")
    found = docx.find_candidates(data)
    if not found:
        return {"candidates": [], "total": 0, "message": "没有在文档里找到 GitHub Token"}
    candidates = docx.build_candidates(found, config.mask_token)

    targets = candidates if preview else _select_candidates(candidates, indices)
    verified = await _verify_candidates(targets)
    if preview:
        return {"candidates": [c.to_dict() for c in verified], "total": len(verified), "message": ""}

    ov = _parse_overrides(overrides)
    existing = {a["username"] for a in config.load_accounts()}
    added = updated = skipped = 0
    accounts: list[dict[str, Any]] = []
    for c in verified:
        username = c.username or ov.get(c.index, {}).get("username", "")
        label = ov.get(c.index, {}).get("label") or c.label or username
        if not username or not c.token:
            skipped += 1
            continue
        if username in existing:
            updated += 1
        else:
            added += 1
        acc = config.add_account(label, username, c.token, "", type_="personal")
        accounts.append({
            "id": acc["id"],
            "username": acc["username"],
            "label": acc["label"],
            "token_masked": config.mask_token(acc["token"]),
        })
    db.audit("accounts.import_docx", detail=f"从 Word 导入：新增 {added}，更新 {updated}，跳过 {skipped}")
    return {"ok": True, "added": added, "updated": updated, "skipped": skipped, "accounts": accounts}


@app.post("/api/accounts/{account_id}/sync")
async def sync_one_account(account_id: int) -> dict[str, Any]:
    """立即同步单个账号。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await sync_engine.sync_account(target)
    db.audit("accounts.sync", account_id=account_id, username=target["username"], detail="手动同步")
    return result


@app.get("/api/tasks")
async def tasks_status() -> dict[str, Any]:
    """同步引擎进度与配置。"""
    return {
        "progress": sync_engine.progress,
        "config": sync_engine.cfg,
        "running": sync_engine.is_running(),
        "states": db.all_sync_states(),
    }


@app.get("/api/audit")
async def audit_log(limit: int = 100, action: str = "") -> dict[str, Any]:
    """审计日志。"""
    limit = max(1, min(limit, 500))
    return {"items": db.list_audit(limit, action or None)}


class SyncSettingsIn(BaseModel):
    enabled: bool | None = None
    interval_seconds: int | None = None
    concurrency: int | None = None
    max_retries: int | None = None
    base_backoff_seconds: int | None = None
    max_backoff_seconds: int | None = None


@app.post("/api/sync/settings")
async def save_sync_settings(payload: SyncSettingsIn) -> dict[str, Any]:
    """配置后台同步（并发、间隔、退避）。"""
    mapping = {
        "enabled": "enabled",
        "interval_seconds": "interval_seconds",
        "concurrency": "concurrency",
        "max_retries": "max_retries",
        "base_backoff_seconds": "base_backoff_seconds",
        "max_backoff_seconds": "max_backoff_seconds",
    }
    settings = {mapping[k]: v for k, v in payload.model_dump().items() if v is not None}
    if settings:
        sync_engine.configure(settings)
        sync.save_settings(sync_engine.cfg)
        db.audit("sync.settings", detail="更新同步配置")
    return {"ok": True, "config": sync_engine.cfg}


@app.post("/api/sync/run")
async def run_sync_now() -> dict[str, Any]:
    """立即触发一次全量同步。"""
    result = await sync_engine.sync_all()
    db.audit("sync.run", detail="手动触发全量同步")
    return {"ok": True, **result}


@app.get("/api/notifications/settings")
async def get_notification_settings() -> dict[str, Any]:
    """外部通知渠道配置。"""
    return {"channels": notify.load_channels(), "types": list(notify.CHANNEL_TYPES)}


class ChannelsIn(BaseModel):
    channels: list[dict[str, Any]] = []


@app.post("/api/notifications/settings")
async def save_notification_settings(payload: ChannelsIn) -> dict[str, Any]:
    channels = [
        c for c in payload.channels
        if isinstance(c, dict) and c.get("type") in notify.CHANNEL_TYPES
    ]
    notify.save_channels(channels)
    if channels and not poller.is_running():
        poller.start()
    return {"ok": True, "channels": channels}


@app.post("/api/notifications/test")
async def test_notification() -> dict[str, Any]:
    """向所有启用的渠道发送一条测试通知。"""
    channels = notify.load_channels()
    if not channels:
        raise HTTPException(status_code=400, detail="请先配置通知渠道")
    sent = await notify.send_all(channels, "GitHub 仪表盘", "这是一条测试通知，收到说明配置成功 ✓")
    return {"ok": True, "sent": sent}


# ------------------------------ 备份 / 历史 ------------------------------

@app.get("/api/accounts/export")
async def export_accounts() -> JSONResponse:
    """导出账号信息备份（不含凭据 / Token）。"""
    data = config.export_backup(include_credentials=False)
    db.audit("accounts.export", detail="导出账号信息（不含凭据）")
    return JSONResponse(
        data,
        headers={"Content-Disposition": "attachment; filename=github-accounts-backup.json"},
    )


@app.post("/api/accounts/export")
async def export_accounts_encrypted(payload: ExportIn) -> JSONResponse:
    """导出凭据备份（含 Token，password 非空时加密）。"""
    data = config.export_backup(payload.password or None, include_credentials=True)
    db.audit("accounts.export_credentials", detail="导出凭据备份")
    filename = "github-accounts-backup-encrypted.json" if payload.password else "github-accounts-backup-credentials.json"
    return JSONResponse(
        data,
        headers={"Content-Disposition": f"attachment; filename={filename}"},
    )


@app.post("/api/accounts/import")
async def import_accounts(payload: dict[str, Any]) -> dict[str, Any]:
    """从备份导入账号（同 username 覆盖）。支持加密备份。"""
    password = payload.pop("password", "") or None
    try:
        result = config.import_backup(payload, password)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    db.audit("accounts.import", detail=f"导入备份：新增 {result['added']}，更新 {result['updated']}，跳过 {result['skipped']}")
    return result


@app.get("/api/history/{account_id}")
async def account_history(account_id: int, limit: int = 300) -> dict[str, Any]:
    """账号历史快照（时间升序）。"""
    limit = max(1, min(limit, 1000))
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    username = target.get("username", "")
    return {
        "history": store.get_history(username, limit),
        "stats": store.get_stats(username),
    }


@app.post("/api/snapshot/{account_id}")
async def take_snapshot(account_id: int) -> dict[str, Any]:
    """立即拉取并强制记录一条快照（忽略节流）。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await service.fetch_account_detail(target, force_record=True)
    return {"ok": True, "stats": store.get_stats(target.get("username", "")), "status": result["status"]}


# ------------------------------ 前端页面 ------------------------------

@app.get("/")
async def index() -> FileResponse:
    return FileResponse(TEMPLATES_DIR / "index.html")


# 静态资源放最后挂载，避免拦截 API 路由
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("app.main:app", host="127.0.0.1", port=8000, reload=True)