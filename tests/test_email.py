"""邮箱面板测试：验证码提取、取件 URL、配置、导入与筛选。"""
from __future__ import annotations

import httpx
import pytest
from fastapi.testclient import TestClient

from app import config, db, email
from app.main import app


def test_extract_code_prefers_context():
    text = "您好，这是您的账户信息。\n验证码：823456，10 分钟内有效。\n如非本人操作请忽略。"
    assert email.extract_code(text) == "823456"


def test_extract_code_digits_without_context():
    assert email.extract_code("您的代码是 483920。") == "483920"


def test_extract_code_alnum_with_digit():
    assert email.extract_code("Verification code: A7B9C2D4") == "A7B9C2D4"


def test_extract_code_ignores_long_numbers():
    # 123456789 不应被当作 4-8 位验证码的一部分
    assert email.extract_code("订单号 123456789 已发货") is None


def test_extract_code_custom_pattern():
    text = "CODE[abcd-1234] 有效"
    assert email.extract_code(text, pattern=r"CODE\[([A-Za-z0-9-]+)\]") == "abcd-1234"


def test_extract_code_empty():
    assert email.extract_code("") is None


def test_build_fetch_url_full_link():
    cfg = email.DEFAULTS
    assert email.build_fetch_url("https://inbox.example/x?token=abc", cfg) == "https://inbox.example/x?token=abc"


def test_build_fetch_url_template():
    cfg = dict(email.DEFAULTS, api_base="https://inbox.example/api?address={email}")
    assert email.build_fetch_url("a@b.com", cfg) == "https://inbox.example/api?address=a%40b.com"


def test_build_fetch_url_default_template():
    got = email.build_fetch_url("kRDI-abc_123", email.DEFAULTS)
    assert got == "https://inbox.123456.world/i/kRDI-abc_123/json"


def test_build_fetch_url_not_configured():
    assert email.build_fetch_url("a@b.com", {"api_base": ""}) == ""


def test_with_page():
    assert email.with_page("https://x/i/t/json", 1) == "https://x/i/t/json"
    assert email.with_page("https://x/i/t/json", 2) == "https://x/i/t/json?page=2"
    assert email.with_page("https://x/i/t/json?a=1", 2) == "https://x/i/t/json?a=1&page=2"


def test_parse_payload_messages_and_code():
    payload = {
        "ok": True, "email": "x@icloud.com", "verificationCode": "123456",
        "messages": [{"id": "m1", "from": "GitHub", "subject": "登录", "body": "code 111111"}],
        "hasMore": False, "page": 1,
    }
    msgs, code = email.parse_payload(payload, "x@icloud.com")
    assert code == "123456"
    assert len(msgs) == 1
    assert msgs[0]["email"] == "x@icloud.com"
    assert msgs[0]["message_id"] == "m1"


def test_parse_messages_list():
    msgs = email.parse_messages(
        [{"id": "1", "sender": {"name": "GitHub", "address": "x@y"}, "subject": "登录",
          "body": "验证码 135790", "received_at": "2026-11-22T01:00:00Z"}],
        "me@inbox",
    )
    assert len(msgs) == 1
    assert msgs[0]["sender"] == "GitHub"
    assert msgs[0]["email"] == "me@inbox"


def test_parse_messages_wrapped_and_text():
    wrapped = email.parse_messages({"messages": [{"subject": "a", "body": "b"}]}, "e")
    assert len(wrapped) == 1
    raw = email.parse_messages("纯文本正文 998877", "e")
    assert raw[0]["body"] == "纯文本正文 998877"


def test_email_settings_roundtrip(monkeypatch):
    cfg = dict(email.DEFAULTS)
    cfg["enabled"] = True
    cfg["site_url"] = "https://inbox.123456.world"
    cfg["api_base"] = "https://inbox.123456.world/api?address={email}"
    saved = email.save_settings(cfg)
    assert saved["enabled"] is True
    assert email.load_settings()["api_base"] == cfg["api_base"]


def _client():
    with TestClient(app) as c:
        yield c


def test_email_import_list_filter_and_code():
    config.add_account("邮箱号", "mail-u", "ghp_fake", email="me@inbox.example")
    with TestClient(app) as client:
        resp = client.post(
            "/api/email/import",
            json={"messages": [
                {"email": "me@inbox.example", "sender": "GitHub",
                 "subject": "登录验证码", "body": "验证码 246810，5 分钟内有效",
                 "receivedAt": "2026-11-22T01:00:00Z", "messageId": "m1"},
                {"email": "me@inbox.example", "sender": "GitHub", "subject": "重复",
                 "body": "x", "messageId": "m1"},
            ]},
        )
        assert resp.status_code == 200
        assert resp.json()["added"] == 1
        assert resp.json()["skipped"] == 1

        listed = client.get("/api/email/messages").json()
        assert listed["total"] == 1
        assert listed["items"][0]["code"] == "246810"

        filtered = client.get("/api/email/messages?q=不存在").json()
        assert filtered["total"] == 0


def test_email_api_flow():
    """一个会话内跑完邮箱 API：设置 / 刷新保护 / 邮件导入删除 / 邮箱清单导入。"""
    with TestClient(app) as client:
        # 设置保存与读取
        resp = client.post(
            "/api/email/settings",
            json={"enabled": False, "site_url": "https://inbox.123456.world"},
        )
        assert resp.status_code == 200
        got = client.get("/api/email/settings").json()
        assert got["settings"]["enabled"] is False
        assert got["settings"]["site_url"] == "https://inbox.123456.world"

        # 未启用时刷新被拦截
        res = client.post("/api/email/refresh").json()
        assert res["ok"] is False and res["reason"] == "disabled"

        # 邮件导入 / 列表 / 删除
        imported = client.post(
            "/api/email/import",
            json={"messages": [{"email": "x@y", "subject": "s", "body": "b", "messageId": "d1"}]},
        ).json()
        assert imported["added"] == 1
        items = client.get("/api/email/messages").json()["items"]
        assert len(items) == 1
        deleted = client.request(
            "DELETE", "/api/email/messages", json={"ids": [items[0]["id"]]}
        ).json()
        assert deleted["deleted"] == 1

        # 邮箱清单解析 + 回填账号
        config.add_account("Alice", "alice", "ghp_x", email="")
        config.add_account("Bob", "bob", "ghp_y")
        text = (
            "alice https://inbox.123456.world/i/tok-a/json\n"
            "bob@x.com https://inbox.123456.world/i/tok-b/json"
        )
        parsed = client.post("/api/email/inboxes/parse", json={"text": text}).json()
        candidates = {c["email"]: c for c in parsed["candidates"]}
        assert candidates["alice"]["accountId"] == 1
        assert candidates["bob@x.com"]["accountId"] == 2

        picked = [c for c in parsed["candidates"] if c["accountId"]]
        imported_acc = client.post(
            "/api/email/inboxes/import", json={"items": picked}
        ).json()
        assert imported_acc["updated"] == 2
        accounts = {a["username"]: a for a in config.load_accounts()}
        assert accounts["alice"]["email"].endswith("/i/tok-a/json")
        assert accounts["bob"]["email"].endswith("/i/tok-b/json")


def test_db_message_dedup_and_account_filter():
    db.add_email_message(account_id=1, email="a@b", message_id="z1", sender="s",
                         subject="t", body="b", received_at="")
    assert db.add_email_message(account_id=1, email="a@b", message_id="z1", sender="s",
                                subject="t", body="b", received_at="") is False
    db.add_email_message(account_id=2, email="c@d", message_id="z2", sender="s",
                         subject="t2", body="b2", received_at="")
    assert len(db.list_email_messages(account_id=1)) == 1
    assert len(db.list_email_messages()) == 2
    assert db.delete_email_messages(account_id=1) == 1
    assert len(db.list_email_messages()) == 1


def test_fetch_account_mail_not_configured(monkeypatch):
    import asyncio

    out = asyncio.run(email.fetch_account_mail({"id": 1, "email": "a@b.com"}, {"api_base": ""}))
    assert out["ok"] is False
    assert out["reason"] == "not_configured"


def test_export_hides_email_without_credentials():
    config.add_account("邮箱号", "exp-u", "ghp_fake", email="token-secret")
    info = config.export_backup(include_credentials=False)
    assert "email" not in info["accounts"][0]
    full = config.export_backup(include_credentials=True)
    assert full["accounts"][0]["email"] == "token-secret"
    # 从凭据备份恢复时保留关联邮箱
    config.import_backup(full)
    acc = next(a for a in config.load_accounts() if a["username"] == "exp-u")
    assert acc["email"] == "token-secret"


def test_parse_inbox_list_same_line():
    items = email.parse_inbox_list(
        "abc@icloud.com https://inbox.123456.world/i/tok1/json\n"
        "zzz@x.com https://inbox.123456.world/i/tok2/json"
    )
    assert len(items) == 2
    assert items[0]["email"] == "abc@icloud.com"
    assert items[0]["url"].endswith("/tok1/json")


def test_parse_inbox_list_adjacent_lines():
    items = email.parse_inbox_list(
        "abc@icloud.com\nhttps://inbox.123456.world/i/tok/json\n"
    )
    assert len(items) == 1
    assert items[0]["email"] == "abc@icloud.com"
    assert items[0]["url"].endswith("/json")


def test_parse_inbox_list_json_and_dedup():
    items = email.parse_inbox_list(
        '[{"email":"a@b.com","url":"https://x/i/1/json"},'
        '{"address":"c@d.com","token":"tok2"}]'
    )
    assert len(items) == 2
    assert items[1]["url"] == "tok2"
    dedup = email.parse_inbox_list(
        "a@b.com https://x/1\na@b.com https://x/1\nhttps://x/2/json"
    )
    assert len(dedup) == 2
    assert dedup[-1]["email"] == ""


def test_match_inbox_account():
    accounts = [
        {"id": 1, "username": "alice", "email": "a@b.com"},
        {"id": 2, "username": "bob", "email": ""},
    ]
    assert email.match_inbox_account("a@b.com", accounts)["id"] == 1
    assert email.match_inbox_account("bob", accounts)["id"] == 2
    assert email.match_inbox_account("bob@x.com", accounts)["id"] == 2
    assert email.match_inbox_account("nobody@x.com", accounts) is None


def test_fetch_account_mail_paginates_and_extracts_code(monkeypatch):
    import asyncio
    from urllib.parse import parse_qs, urlparse

    pages = {
        1: {"ok": True, "email": "x@icloud.com", "verificationCode": "123456",
            "messages": [{"id": "a", "from": "GitHub", "subject": "登录",
                          "body": "code 111111", "date": "2026-11-22T01:00:00Z"}],
            "hasMore": True, "page": 1},
        2: {"ok": True, "messages": [{"id": "b", "from": "Apple", "subject": "安全",
                                      "body": "验证码 222222", "date": "2026-11-22T02:00:00Z"}],
            "hasMore": False, "page": 2},
    }
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        page = int(parse_qs(urlparse(str(request.url)).query).get("page", ["1"])[0])
        calls.append(page)
        return httpx.Response(200, json=pages[page],
                              headers={"content-type": "application/json"})

    real_client = httpx.AsyncClient
    monkeypatch.setattr(email.httpx, "AsyncClient",
                        lambda **kw: real_client(transport=httpx.MockTransport(handler)))
    out = asyncio.run(email.fetch_account_mail({"id": 1, "email": "tok"}, email.DEFAULTS))
    assert out["ok"] and out["fetched"] == 2 and out["added"] == 2
    assert out["verificationCode"] == "123456"
    assert calls == [1, 2]

    items = db.list_email_messages(account_id=1)
    assert {i["code"] for i in items} <= {"123456", "111111", "222222"}
    assert any(i["code"] for i in items)
