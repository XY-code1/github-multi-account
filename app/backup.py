"""备份加密：PBKDF2-HMAC-SHA256 派生密钥 + AES-256-GCM。

加密后的备份结构：
    {
      "encrypted": true,
      "app": "github-multi-account",
      "salt": "<base64>",
      "iterations": 200000,
      "payload": "<base64(nonce || ciphertext)>"
    }

Go 版使用完全相同的算法与参数，两边可以互相导入导出。
"""
from __future__ import annotations

import base64
import hashlib
import json
import os

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

ITERATIONS = 200_000
KEY_LEN = 32
SALT_LEN = 16
NONCE_LEN = 12


def _derive_key(password: str, salt: bytes, iterations: int) -> bytes:
    return hashlib.pbkdf2_hmac(
        "sha256", password.encode("utf-8"), salt, iterations, dklen=KEY_LEN
    )


def encrypt_backup(data: dict, password: str) -> dict:
    salt = os.urandom(SALT_LEN)
    key = _derive_key(password, salt, ITERATIONS)
    nonce = os.urandom(NONCE_LEN)
    ct = AESGCM(key).encrypt(nonce, json.dumps(data, ensure_ascii=False).encode("utf-8"), None)
    return {
        "encrypted": True,
        "app": "github-multi-account",
        "salt": base64.b64encode(salt).decode("ascii"),
        "iterations": ITERATIONS,
        "payload": base64.b64encode(nonce + ct).decode("ascii"),
    }


def decrypt_backup(payload: dict, password: str) -> dict:
    if not password:
        raise ValueError("该备份已加密，请提供密码")
    try:
        salt = base64.b64decode(payload["salt"])
        iterations = int(payload.get("iterations", ITERATIONS))
        key = _derive_key(password, salt, iterations)
        raw = base64.b64decode(payload["payload"])
        nonce, ct = raw[:NONCE_LEN], raw[NONCE_LEN:]
        plain = AESGCM(key).decrypt(nonce, ct, None)
        return json.loads(plain)
    except ValueError:
        raise
    except Exception as e:  # noqa: BLE001 - 密码错误 / 数据损坏
        raise ValueError("解密失败：密码错误或备份文件已损坏") from e
