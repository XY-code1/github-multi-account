"""加密模块测试。"""
import pytest
from cryptography.fernet import Fernet

from app.crypto import decrypt_text, encrypt_text, get_or_create_key


def test_encrypt_decrypt_roundtrip(tmp_path):
    key = get_or_create_key(tmp_path)
    token = "ghp_supersecret123456"
    cipher = encrypt_text(token, key)
    assert cipher != token
    assert "supersecret" not in cipher
    assert decrypt_text(cipher, key) == token


def test_key_is_stable_and_persisted(tmp_path):
    first = get_or_create_key(tmp_path)
    second = get_or_create_key(tmp_path)
    assert first == second
    assert (tmp_path / ".secret.key").exists()


def test_wrong_key_raises(tmp_path):
    key = get_or_create_key(tmp_path)
    cipher = encrypt_text("secret", key)
    with pytest.raises(Exception):
        decrypt_text(cipher, Fernet.generate_key())
