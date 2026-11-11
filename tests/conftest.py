"""pytest 公共 fixture：每个测试使用独立的配置目录与 SQLite 数据库。"""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).resolve().parent.parent
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))


@pytest.fixture(autouse=True)
def isolated_env(tmp_path, monkeypatch):
    from app import config, store

    cfg = tmp_path / "config"
    data = tmp_path / "data"
    cfg.mkdir(parents=True, exist_ok=True)
    data.mkdir(parents=True, exist_ok=True)

    monkeypatch.setattr(config, "CONFIG_DIR", cfg)
    monkeypatch.setattr(config, "ACCOUNTS_FILE", cfg / "accounts.yaml")
    monkeypatch.setattr(config, "_cache", {"mtime": None, "accounts": None})
    monkeypatch.setattr(store, "DATA_DIR", data)
    monkeypatch.setattr(store, "DB_FILE", data / "dashboard.db")
    store.init_db()
    yield
