"""本地加密：用本机密钥对 Token 做对称加密（Fernet / AES-128-CBC + HMAC）。

密钥保存在 config/.secret.key（Unix 下 0600 权限）。Token 在 accounts.yaml
中以密文形式存储，明文不落地，降低误提交 / 截图 / 备份泄露的风险。

注意：密钥与配置文件在同一目录，这种方式防的是「明文泄露」，
不是「有人拿到你整台电脑」——后者请配合系统级加密（如 Windows BitLocker）。
"""
from __future__ import annotations

import os
from pathlib import Path

from cryptography.fernet import Fernet


def get_or_create_key(config_dir: Path) -> bytes:
    """读取或生成本机密钥。"""
    key_file = config_dir / ".secret.key"
    if key_file.exists():
        return key_file.read_bytes()
    config_dir.mkdir(parents=True, exist_ok=True)
    key = Fernet.generate_key()
    key_file.write_bytes(key)
    try:
        os.chmod(key_file, 0o600)
    except OSError:
        pass  # Windows 上 chmod 基本无效，忽略
    return key


def encrypt_text(plain: str, key: bytes) -> str:
    """加密字符串，返回可安全写入 YAML 的 ASCII 文本。"""
    return Fernet(key).encrypt(plain.encode("utf-8")).decode("ascii")


def decrypt_text(cipher: str, key: bytes) -> str:
    """解密字符串。"""
    return Fernet(key).decrypt(cipher.encode("ascii")).decode("utf-8")
