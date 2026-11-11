"""GitHub 多账号状态仪表盘 —— FastAPI 后端。

启动：
    pip install -r requirements.txt
    cp config/accounts.example.yaml config/accounts.yaml  # 填入 token
    uvicorn app.main:app --reload --port 8000

然后浏览器打开 http://localhost:8000
"""
from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from . import config, notify, service, store

logger = logging.getLogger("ghdashboard")

# 外部通知轮询器（有渠道配置时才启动）
poller = notify.NotificationPoller(interval_seconds=300)

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
    if notify.load_channels():
        poller.start()
        logger.info("外部通知轮询已启动")
    logger.info("GitHub 多账号仪表盘已启动")
    yield
    await poller.stop()


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


class ExportIn(BaseModel):
    password: str = ""


# ------------------------------ 账号管理 ------------------------------

@app.get("/api/accounts")
async def list_accounts() -> dict[str, Any]:
    """账号列表（不含 token 原文）。"""
    accounts = config.load_accounts()
    return {
        "accounts": [
            {
                "id": a["id"],
                "label": a["label"],
                "username": a["username"],
                "group": a.get("group", ""),
                "token_masked": config.mask_token(a["token"]),
                "has_token": bool(a["token"]),
            }
            for a in accounts
        ]
    }


@app.post("/api/accounts")
async def create_account(payload: AccountIn) -> dict[str, Any]:
    """新增或更新账号（同 username 覆盖）。"""
    if not payload.username:
        raise HTTPException(status_code=400, detail="username 不能为空")
    if not payload.token:
        raise HTTPException(status_code=400, detail="token 不能为空")
    acc = config.add_account(payload.label, payload.username, payload.token, payload.group or "")
    return {
        "id": acc["id"],
        "label": acc["label"],
        "username": acc["username"],
        "group": acc.get("group", ""),
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
        "version": "1.1.0",
        "repo": "https://github.com/XY-code1/github-multi-account",
    }


@app.get("/api/search")
async def search(q: str = "", kind: str = "all") -> dict[str, Any]:
    """跨账号搜索 PR / Issue 标题。kind: all | pr | issue"""
    accounts = config.load_accounts()
    return await service.global_search(accounts, q, kind)


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
    """导出账号备份（明文，含 Token，请妥善保管）。"""
    data = config.export_backup()
    return JSONResponse(
        data,
        headers={"Content-Disposition": "attachment; filename=github-accounts-backup.json"},
    )


@app.post("/api/accounts/export")
async def export_accounts_encrypted(payload: ExportIn) -> JSONResponse:
    """导出加密备份（password 非空时加密）。"""
    data = config.export_backup(payload.password or None)
    filename = "github-accounts-backup-encrypted.json" if payload.password else "github-accounts-backup.json"
    return JSONResponse(
        data,
        headers={"Content-Disposition": f"attachment; filename={filename}"},
    )


@app.post("/api/accounts/import")
async def import_accounts(payload: dict[str, Any]) -> dict[str, Any]:
    """从备份导入账号（同 username 覆盖）。支持加密备份。"""
    password = payload.pop("password", "") or None
    try:
        return config.import_backup(payload, password)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


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