"""账号配置管理：读写 config/accounts.yaml。

Token 使用本机密钥（config/.secret.key）加密存储，明文不落地。
兼容旧版明文 token：首次加载时自动迁移为加密格式并写回。

写入采用「临时文件 + 原子替换」并保留 .bak，避免进程中途被杀
（Ctrl+C / 断电 / --reload 重启）导致配置文件损坏、Token 全丢。
"""
from __future__ import annotations

import os
import shutil
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

# 内存缓存：避免每个请求都读盘 + 解密（文件 mtime 变化时自动失效）
_cache: dict[str, Any] = {"mtime": None, "accounts": None}


class ConfigError(Exception):
    """配置无法读取（文件损坏 / 密钥丢失等）。"""


def _ensure_config_dir() -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)


def read_settings() -> dict[str, Any]:
    """读取 config/settings.yaml 原始内容（缺失或损坏时返回空字典）。

    通知渠道与同步设置共用这一个文件，任何一方保存时都必须先读取再合并，
    否则会把另一方的配置整份覆盖掉。
    """
    try:
        data = yaml.safe_load((CONFIG_DIR / "settings.yaml").read_text(encoding="utf-8")) or {}
    except Exception:  # noqa: BLE001
        return {}
    return data if isinstance(data, dict) else {}


def write_settings(data: dict[str, Any]) -> None:
    """写回 config/settings.yaml（保留文件中其他分节）。"""
    _ensure_config_dir()
    (CONFIG_DIR / "settings.yaml").write_text(
        yaml.safe_dump(data, allow_unicode=True, sort_keys=False), encoding="utf-8"
    )


def load_accounts() -> list[dict[str, Any]]:
    """读取全部账号配置（自动解密 Token，带内存缓存）。"""
    try:
        mtime = ACCOUNTS_FILE.stat().st_mtime_ns
    except FileNotFoundError:
        return []

    with _lock:
        if _cache["accounts"] is not None and _cache["mtime"] == mtime:
            return [dict(a) for a in _cache["accounts"]]

    with ACCOUNTS_FILE.open("r", encoding="utf-8") as f:
        try:
            data = yaml.safe_load(f) or {}
        except yaml.YAMLError as e:
            raise ConfigError(f"accounts.yaml 解析失败，文件可能已损坏：{e}") from e

    raw_accounts = data.get("accounts") if isinstance(data, dict) else None
    if raw_accounts is None:
        raw_accounts = []
    if not isinstance(raw_accounts, list):
        raise ConfigError("accounts.yaml 格式不正确：accounts 必须是列表")

    # 密钥丢失检测：存在密文但没有密钥文件时，拒绝继续，
    # 避免生成新密钥后把无法解密的旧密文覆盖掉。
    has_encrypted = any(isinstance(a, dict) and a.get("token_encrypted") for a in raw_accounts)
    if has_encrypted and not (CONFIG_DIR / ".secret.key").exists():
        raise ConfigError(
            "检测到已加密的 Token，但密钥文件 config/.secret.key 缺失，无法解密。"
            "请恢复密钥文件，或手动清空 accounts.yaml 后重新录入账号。"
        )

    key: bytes | None = None
    plain_found = False
    accounts: list[dict[str, Any]] = []
    for idx, acc in enumerate(raw_accounts, start=1):
        acc = acc if isinstance(acc, dict) else {}
        username = acc.get("username") or ""
        token = ""
        encrypted = acc.get("token_encrypted")
        if encrypted:
            if key is None:
                key = get_or_create_key(CONFIG_DIR)
            try:
                token = decrypt_text(encrypted, key)
            except Exception as e:  # noqa: BLE001
                raise ConfigError(
                    "Token 解密失败：.secret.key 与 accounts.yaml 不匹配（密钥可能被替换）。"
                    "请恢复原始密钥文件。"
                ) from e
        elif acc.get("token"):
            token = acc["token"]
            plain_found = True
        tags = acc.get("tags") or []
        if isinstance(tags, str):
            tags = [t.strip() for t in tags.split(",") if t.strip()]
        accounts.append(
            {
                "id": idx,
                "label": acc.get("label") or username or f"账号 {idx}",
                "username": username,
                "group": acc.get("group") or "",
                "type": acc.get("type") or "personal",
                "alias": acc.get("alias") or "",
                "note": acc.get("note") or "",
                "tags": tags,
                "token": token,
            }
        )

    if plain_found:
        save_accounts(accounts)
        try:
            mtime = ACCOUNTS_FILE.stat().st_mtime_ns
        except OSError:
            pass

    with _lock:
        _cache["mtime"] = mtime
        _cache["accounts"] = [dict(a) for a in accounts]
    return accounts


def save_accounts(accounts: list[dict[str, Any]]) -> None:
    """全量写回账号配置（加密 + 原子替换 + 备份）。"""
    _ensure_config_dir()
    key = get_or_create_key(CONFIG_DIR)
    payload = {
        "accounts": [
            {
                "label": a.get("label", ""),
                "username": a.get("username", ""),
                "group": a.get("group", ""),
                "type": a.get("type", "personal"),
                "alias": a.get("alias", ""),
                "note": a.get("note", ""),
                "tags": a.get("tags", []),
                "token_encrypted": encrypt_text(a.get("token", ""), key),
            }
            for a in accounts
        ]
    }
    tmp_file = ACCOUNTS_FILE.with_name(ACCOUNTS_FILE.name + ".tmp")
    with _lock:
        with tmp_file.open("w", encoding="utf-8") as f:
            yaml.safe_dump(payload, f, allow_unicode=True, sort_keys=False)
            f.flush()
            os.fsync(f.fileno())
        if ACCOUNTS_FILE.exists():
            try:
                shutil.copy2(ACCOUNTS_FILE, ACCOUNTS_FILE.with_name(ACCOUNTS_FILE.name + ".bak"))
            except OSError:
                pass
        os.replace(tmp_file, ACCOUNTS_FILE)
        _cache["mtime"] = None
        _cache["accounts"] = None


def add_account(
    label: str,
    username: str,
    token: str,
    group: str = "",
    type_: str = "personal",
    alias: str = "",
    note: str = "",
    tags: list[str] | None = None,
) -> dict[str, Any]:
    accounts = load_accounts()
    fields = {
        "label": label or username,
        "token": token,
        "group": group,
        "type": type_ if type_ in ("personal", "bot", "org") else "personal",
        "alias": alias,
        "note": note,
        "tags": tags or [],
    }
    for acc in accounts:
        if acc["username"] == username and username:
            acc.update(fields)
            save_accounts(accounts)
            return acc
    accounts.append({"id": len(accounts) + 1, "username": username, **fields})
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


def export_backup(
    password: str | None = None, include_credentials: bool = True
) -> dict[str, Any]:
    """导出备份。

    include_credentials=True 时包含 Token 明文（请妥善保管）；
    为 False 时只导出账号信息，用于「账号信息备份」。
    password 非空时对整份备份加密。
    """
    accounts = load_accounts()
    data = {
        "version": 1,
        "app": "github-multi-account",
        "exported_at": datetime.now(timezone.utc).isoformat(),
        "accounts": [
            {
                "label": a["label"],
                "username": a["username"],
                "group": a.get("group", ""),
                "type": a.get("type", "personal"),
                "alias": a.get("alias", ""),
                "note": a.get("note", ""),
                "tags": a.get("tags", []),
                **({"token": a["token"]} if include_credentials else {}),
            }
            for a in accounts
        ],
    }
    if password:
        from .backup import encrypt_backup

        return encrypt_backup(data, password)
    return data


def import_backup(payload: dict[str, Any], password: str | None = None) -> dict[str, Any]:
    """从备份导入账号（同 username 覆盖）。"""
    if payload.get("encrypted"):
        from .backup import decrypt_backup

        payload = decrypt_backup(payload, password or "")
    if not isinstance(payload, dict):
        raise ValueError("备份格式不正确：应为 JSON 对象")
    items = payload.get("accounts") or []
    if not isinstance(items, list):
        raise ValueError("备份格式不正确：accounts 应为列表")

    accounts = load_accounts()
    added = updated = skipped = 0
    for item in items:
        if not isinstance(item, dict):
            skipped += 1
            continue
        username = item.get("username")
        token = item.get("token")
        if not isinstance(username, str) or not isinstance(token, str):
            skipped += 1
            continue
        username = username.strip()
        if not username or not token:
            skipped += 1
            continue
        label = item.get("label")
        label = label if isinstance(label, str) and label.strip() else username
        group = item.get("group")
        group = group.strip() if isinstance(group, str) else ""
        atype = item.get("type")
        atype = atype if atype in ("personal", "bot", "org") else "personal"
        alias = item.get("alias")
        alias = alias if isinstance(alias, str) else ""
        note = item.get("note")
        note = note if isinstance(note, str) else ""
        tags = item.get("tags") or []
        if isinstance(tags, str):
            tags = [t.strip() for t in tags.split(",") if t.strip()]
        elif not isinstance(tags, list):
            tags = []
        existing = next((a for a in accounts if a["username"] == username), None)
        if existing:
            existing["label"] = label
            existing["token"] = token
            existing["group"] = group
            existing["type"] = atype
            existing["alias"] = alias
            existing["note"] = note
            existing["tags"] = tags
            updated += 1
        else:
            accounts.append(
                {
                    "id": 0,
                    "label": label,
                    "username": username,
                    "group": group,
                    "type": atype,
                    "alias": alias,
                    "note": note,
                    "tags": tags,
                    "token": token,
                }
            )
            added += 1
    if added or updated:
        for idx, acc in enumerate(accounts, start=1):
            acc["id"] = idx
        save_accounts(accounts)
    return {"added": added, "updated": updated, "skipped": skipped}
