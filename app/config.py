"""账号配置管理：读写 config/accounts.yaml。

配置文件格式：
    accounts:
      - label: 主账号
        username: XY-code1
        token: ghp_xxxx

账号在列表中的位置即为其 id（从 1 开始）。
"""
from __future__ import annotations

import os
from pathlib import Path
from threading import Lock
from typing import Any

import yaml

BASE_DIR = Path(__file__).resolve().parent.parent
CONFIG_DIR = Path(os.getenv("GAD_CONFIG_DIR", BASE_DIR / "config"))
ACCOUNTS_FILE = CONFIG_DIR / "accounts.yaml"

_lock = Lock()


def _ensure_config_dir() -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)


def load_accounts() -> list[dict[str, Any]]:
    """读取全部账号配置。文件不存在时返回空列表。"""
    if not ACCOUNTS_FILE.exists():
        return []
    with ACCOUNTS_FILE.open("r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    accounts: list[dict[str, Any]] = []
    for idx, acc in enumerate(data.get("accounts") or [], start=1):
        username = (acc or {}).get("username") or ""
        accounts.append(
            {
                "id": idx,
                "label": (acc or {}).get("label") or username or f"账号 {idx}",
                "username": username,
                "token": (acc or {}).get("token") or "",
            }
        )
    return accounts


def save_accounts(accounts: list[dict[str, Any]]) -> None:
    """全量写回账号配置。"""
    _ensure_config_dir()
    payload = {
        "accounts": [
            {
                "label": a.get("label", ""),
                "username": a.get("username", ""),
                "token": a.get("token", ""),
            }
            for a in accounts
        ]
    }
    with _lock, ACCOUNTS_FILE.open("w", encoding="utf-8") as f:
        yaml.safe_dump(payload, f, allow_unicode=True, sort_keys=False)


def add_account(label: str, username: str, token: str) -> dict[str, Any]:
    accounts = load_accounts()
    # 同一 username 已存在则更新，否则追加
    for acc in accounts:
        if acc["username"] == username and username:
            acc["label"] = label or acc["label"]
            acc["token"] = token
            save_accounts(accounts)
            return acc
    accounts.append({"id": len(accounts) + 1, "label": label or username, "username": username, "token": token})
    # 重新编号，保证 id 连续
    for idx, acc in enumerate(accounts, start=1):
        acc["id"] = idx
    save_accounts(accounts)
    return accounts[-1]


def delete_account(account_id: int) -> bool:
    accounts = load_accounts()
    new_accounts = [a for a in accounts if a["id"] != account_id]
    if len(new_accounts) == len(accounts):
        return False
    for idx, acc in enumerate(new_accounts, start=1):
        acc["id"] = idx
    save_accounts(new_accounts)
    return True


def mask_token(token: str) -> str:
    """脱敏显示 token。"""
    if not token:
        return ""
    if len(token) <= 8:
        return "*" * len(token)
    return f"{token[:4]}{'*' * 8}{token[-4:]}"
