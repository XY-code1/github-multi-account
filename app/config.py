"""账号配置管理：读写 config/accounts.yaml。

Token 使用本机密钥（config/.secret.key）加密存储，明文不落地。
兼容旧版明文 token：首次加载时自动迁移为加密格式并写回。

配置文件格式：
    accounts:
      - label: 主账号
        username: XY-code1
        token_encrypted: gAAAAAB...

账号在列表中的位置即为其 id（从 1 开始）。
"""
from __future__ import annotations

import os
from datetime import datetime, timezone
from pathlib import Path
from threading import Lock
from typing import Any

import yaml

from .crypto import decrypt_text, encrypt_text, get_or_create_key

BASE_DIR = Path(__file__).resolve().parent.parent
CONFIG_DIR = Path(os.getenv("GAD_CONFIG_DIR", BASE_DIR / "config"))
ACCOUNTS_FILE = CONFIG_DIR / "accounts.yaml"

_lock = Lock()


def _ensure_config_dir() -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)


def load_accounts() -> list[dict[str, Any]]:
    """读取全部账号配置（自动解密 Token）。"""
    if not ACCOUNTS_FILE.exists():
        return []
    with ACCOUNTS_FILE.open("r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}

    key: bytes | None = None
    plain_found = False
    accounts: list[dict[str, Any]] = []
    for idx, acc in enumerate(data.get("accounts") or [], start=1):
        acc = acc or {}
        username = acc.get("username") or ""
        token = ""
        encrypted = acc.get("token_encrypted")
        if encrypted:
            if key is None:
                key = get_or_create_key(CONFIG_DIR)
            try:
                token = decrypt_text(encrypted, key)
            except Exception:
                token = ""
        elif acc.get("token"):
            token = acc["token"]
            plain_found = True
        accounts.append(
            {
                "id": idx,
                "label": acc.get("label") or username or f"账号 {idx}",
                "username": username,
                "token": token,
            }
        )
    if plain_found:
        save_accounts(accounts)
    return accounts


def save_accounts(accounts: list[dict[str, Any]]) -> None:
    """全量写回账号配置（Token 加密存储）。"""
    _ensure_config_dir()
    key = get_or_create_key(CONFIG_DIR)
    payload = {
        "accounts": [
            {
                "label": a.get("label", ""),
                "username": a.get("username", ""),
                "token_encrypted": encrypt_text(a.get("token", ""), key),
            }
            for a in accounts
        ]
    }
    with _lock, ACCOUNTS_FILE.open("w", encoding="utf-8") as f:
        yaml.safe_dump(payload, f, allow_unicode=True, sort_keys=False)


def add_account(label: str, username: str, token: str) -> dict[str, Any]:
    accounts = load_accounts()
    for acc in accounts:
        if acc["username"] == username and username:
            acc["label"] = label or acc["label"]
            acc["token"] = token
            save_accounts(accounts)
            return acc
    accounts.append(
        {"id": len(accounts) + 1, "label": label or username, "username": username, "token": token}
    )
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


def export_backup() -> dict[str, Any]:
    """导出备份（含 Token 明文，请妥善保管）。"""
    accounts = load_accounts()
    return {
        "version": 1,
        "app": "github-multi-account",
        "exported_at": datetime.now(timezone.utc).isoformat(),
        "accounts": [
            {"label": a["label"], "username": a["username"], "token": a["token"]}
            for a in accounts
        ],
    }


def import_backup(payload: dict[str, Any]) -> dict[str, Any]:
    """从备份导入账号（同 username 覆盖）。"""
    items = payload.get("accounts") or []
    accounts = load_accounts()
    added = updated = skipped = 0
    for item in items:
        item = item or {}
        username = (item.get("username") or "").strip()
        token = item.get("token") or ""
        if not username or not token:
            skipped += 1
            continue
        label = item.get("label") or username
        existing = next((a for a in accounts if a["username"] == username), None)
        if existing:
            existing["label"] = label
            existing["token"] = token
            updated += 1
        else:
            accounts.append({"id": 0, "label": label, "username": username, "token": token})
            added += 1
    if added or updated:
        for idx, acc in enumerate(accounts, start=1):
            acc["id"] = idx
        save_accounts(accounts)
    return {"added": added, "updated": updated, "skipped": skipped}
