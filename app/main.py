"""GitHub 多账号状态仪表盘 —— FastAPI 后端。

启动：
    pip install -r requirements.txt
    cp config/accounts.example.yaml config/accounts.yaml  # 填入 token
    uvicorn app.main:app --reload --port 8000

然后浏览器打开 http://localhost:8000
"""
from __future__ import annotations

from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from . import config, service

BASE_DIR = Path(__file__).resolve().parent.parent
STATIC_DIR = BASE_DIR / "static"
TEMPLATES_DIR = BASE_DIR / "templates"

app = FastAPI(title="GitHub 多账号状态仪表盘", version="1.0.0")


# ------------------------------ 请求模型 ------------------------------

class AccountIn(BaseModel):
    label: str = ""
    username: str
    token: str


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
    acc = config.add_account(payload.label, payload.username, payload.token)
    return {
        "id": acc["id"],
        "label": acc["label"],
        "username": acc["username"],
        "token_masked": config.mask_token(acc["token"]),
    }


@app.delete("/api/accounts/{account_id}")
async def remove_account(account_id: int) -> dict[str, Any]:
    if not config.delete_account(account_id):
        raise HTTPException(status_code=404, detail="账号不存在")
    return {"ok": True}


# ------------------------------ 状态聚合 ------------------------------

@app.post("/api/verify")
async def verify(payload: AccountIn) -> dict[str, Any]:
    """测试连接：仅验证 token 有效性，不保存。"""
    return await service.verify_token(payload.username, payload.token)


@app.get("/api/status")
async def all_status() -> dict[str, Any]:
    """全部账号的状态摘要（含数量统计）。"""
    accounts = config.load_accounts()
    if not accounts:
        return {"accounts": []}
    results = await service.fetch_all_status(accounts)
    return {"accounts": results}


@app.get("/api/status/{account_id}")
async def account_status(account_id: int) -> dict[str, Any]:
    """单个账号的完整状态。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await service.fetch_account_status(target)
    return {"account": result}


@app.post("/api/accounts/{account_id}/verify")
async def verify_account(account_id: int) -> dict[str, Any]:
    """仅验证登录有效性（轻量，不拉 PR/Issue）。"""
    accounts = config.load_accounts()
    target = next((a for a in accounts if a["id"] == account_id), None)
    if target is None:
        raise HTTPException(status_code=404, detail="账号不存在")
    result = await service.fetch_account_status(target)
    return {
        "id": result["id"],
        "status": result["status"],
        "user": result["user"],
        "error": result["error"],
    }


# ------------------------------ 前端页面 ------------------------------

@app.get("/")
async def index() -> FileResponse:
    return FileResponse(TEMPLATES_DIR / "index.html")


# 静态资源放最后挂载，避免拦截 API 路由
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("app.main:app", host="127.0.0.1", port=8000, reload=True)
