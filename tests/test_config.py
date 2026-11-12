"""账号配置测试：加密迁移、增删、导入导出。"""
import yaml

import pytest

from app import config


def _write_yaml(path, accounts):
    path.write_text(
        yaml.safe_dump({"accounts": accounts}, allow_unicode=True),
        encoding="utf-8",
    )


def test_plain_token_auto_migrated():
    _write_yaml(
        config.ACCOUNTS_FILE,
        [{"label": "主账号", "username": "u1", "token": "ghp_plain"}],
    )
    accounts = config.load_accounts()
    assert accounts[0]["token"] == "ghp_plain"

    content = config.ACCOUNTS_FILE.read_text(encoding="utf-8")
    assert "token_encrypted" in content
    assert "ghp_plain" not in content


def test_encrypted_token_roundtrip():
    config.add_account("A", "u1", "ghp_secret")
    accounts = config.load_accounts()
    assert accounts[0]["token"] == "ghp_secret"


def test_add_updates_existing_username():
    config.add_account("A", "u1", "ghp_first")
    config.add_account("A2", "u1", "ghp_second")
    accounts = config.load_accounts()
    assert len(accounts) == 1
    assert accounts[0]["token"] == "ghp_second"
    assert accounts[0]["label"] == "A2"


def test_delete_account():
    config.add_account("A", "u1", "ghp_1")
    config.add_account("B", "u2", "ghp_2")
    assert config.delete_account(1) is True
    assert config.delete_account(99) is False
    accounts = config.load_accounts()
    assert [a["username"] for a in accounts] == ["u2"]
    assert accounts[0]["id"] == 1


def test_export_import_roundtrip():
    config.add_account("A", "u1", "ghp_1")
    config.add_account("B", "u2", "ghp_2")
    backup = config.export_backup()
    assert len(backup["accounts"]) == 2
    assert backup["accounts"][0]["token"] == "ghp_1"

    # 导入到空配置
    config.ACCOUNTS_FILE.write_text("", encoding="utf-8")
    config._cache["accounts"] = None
    result = config.import_backup(backup)
    assert result["added"] == 2
    assert [a["username"] for a in config.load_accounts()] == ["u1", "u2"]


def test_export_without_credentials_hides_token():
    config.add_account("A", "u1", "ghp_hidden", group="g", type_="bot")
    backup = config.export_backup(include_credentials=False)
    assert len(backup["accounts"]) == 1
    assert "token" not in backup["accounts"][0]
    assert backup["accounts"][0]["username"] == "u1"
    assert backup["accounts"][0]["type"] == "bot"


def test_add_account_keeps_metadata():
    config.add_account(
        "A", "u1", "ghp_1", group="工作", type_="bot", alias="U1", note="备注", tags=["核心"]
    )
    account = config.load_accounts()[0]
    assert account["group"] == "工作"
    assert account["type"] == "bot"
    assert account["alias"] == "U1"
    assert account["note"] == "备注"
    assert account["tags"] == ["核心"]


def test_settings_sections_do_not_overwrite_each_other():
    """通知渠道与同步设置共用 settings.yaml，任一方保存都不应清空另一方。"""
    from app import notify, sync

    notify.save_channels([{"type": "bark", "name": "iPhone", "url": "https://x", "enabled": True}])
    sync.save_settings({"enabled": True, "interval_seconds": 900, "concurrency": 5})

    channels = notify.load_channels()
    assert len(channels) == 1 and channels[0]["name"] == "iPhone"
    assert sync.load_settings()["interval_seconds"] == 900

    # 反向：先写同步设置，再写渠道，同步设置也应保留
    sync.save_settings({"enabled": False, "interval_seconds": 600, "concurrency": 3})
    notify.save_channels([])
    assert sync.load_settings()["interval_seconds"] == 600
    assert sync.load_settings()["enabled"] is False


def test_import_is_idempotent_and_skips_invalid():
    backup = {
        "accounts": [
            {"label": "A", "username": "u1", "token": "ghp_1"},
            {"label": "", "username": "", "token": ""},
        ]
    }
    config.import_backup(backup)
    result = config.import_backup(backup)
    assert result["added"] == 0
    assert result["updated"] == 1
    assert result["skipped"] == 1


def test_cache_invalidated_on_save():
    config.add_account("A", "u1", "ghp_1")
    first = config.load_accounts()
    assert first[0]["token"] == "ghp_1"
    # 缓存命中：直接改文件后 mtime 不变则仍返回缓存（此处验证缓存生效）
    assert config.load_accounts()[0]["token"] == "ghp_1"
    config.add_account("B", "u2", "ghp_2")
    assert len(config.load_accounts()) == 2


def test_save_is_atomic_and_keeps_backup():
    config.add_account("A", "u1", "ghp_1")
    config.add_account("B", "u2", "ghp_2")
    # 第二次保存应留下 .bak
    assert (config.CONFIG_DIR / "accounts.yaml.bak").exists()
    assert not (config.CONFIG_DIR / "accounts.yaml.tmp").exists()


def test_missing_key_raises_config_error():
    import yaml
    _write_yaml(
        config.ACCOUNTS_FILE,
        [{"label": "A", "username": "u1", "token_encrypted": "gAAAAABfake"}],
    )
    config._cache["accounts"] = None
    with pytest.raises(config.ConfigError):
        config.load_accounts()


def test_import_rejects_bad_structure():
    with pytest.raises(ValueError):
        config.import_backup({"accounts": "not-a-list"})
    with pytest.raises(ValueError):
        config.import_backup({"accounts": {"a": 1}})


def test_import_skips_malformed_items():
    result = config.import_backup(
        {
            "accounts": [
                {"username": "u", "token": 123},
                {"username": 5, "token": "t"},
                {"username": "ok", "token": "ghp_x"},
            ]
        }
    )
    assert result["added"] == 1
    assert result["skipped"] == 2
