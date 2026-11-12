"""Word 文档导入测试：Token / 用户名识别与接口。"""
from __future__ import annotations

import io
import zipfile

import pytest
from fastapi.testclient import TestClient

from app import config, docx
from app.main import app


@pytest.fixture
def client():
    with TestClient(app) as c:
        yield c


def make_docx(lines: list[str]) -> bytes:
    """构造一个最小的 .docx（zip + word/document.xml）。"""
    paras = "".join(f"<w:p><w:r><w:t>{line}</w:t></w:r></w:p>" for line in lines)
    xml = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
        f"<w:body>{paras}</w:body></w:document>"
    )
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("word/document.xml", xml)
    return buf.getvalue()


DOCX_MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


def test_extract_lines():
    assert docx.extract_lines(make_docx(["第一段", "第二段", ""])) == ["第一段", "第二段"]


def test_not_a_docx():
    assert docx.extract_lines(b"not a zip") == []
    assert docx.extract_lines(b"") == []


def test_find_candidates_with_label():
    data = make_docx([
        "账号：alice",
        "Token: " + "ghp_" + "a" * 36,
        "用户名: bob",
        "ghp_" + "b" * 36,
    ])
    found = docx.find_candidates(data)
    assert len(found) == 2
    assert found[0].username == "alice"
    assert found[0].token.startswith("ghp_")
    assert found[1].username == "bob"


def test_find_candidates_prev_line_bare_username():
    data = make_docx(["carol", "ghp_" + "c" * 36])
    found = docx.find_candidates(data)
    assert len(found) == 1
    assert found[0].username == "carol"


def test_find_candidates_dedup():
    token = "ghp_" + "a" * 36
    assert len(docx.find_candidates(make_docx([token, token]))) == 1


def test_find_candidates_fine_grained():
    token = "github_pat_" + "a" * 22 + "_" + "b" * 20
    found = docx.find_candidates(make_docx(["dave", token]))
    assert len(found) == 1
    assert found[0].token == token
    assert found[0].username == "dave"


def test_build_candidates_masks_token():
    found = [docx.Found(username="alice", token="ghp_" + "a" * 36)]
    cands = docx.build_candidates(found, config.mask_token)
    assert cands[0].token_masked.startswith("ghp_")
    assert "a" * 36 not in cands[0].token_masked
    # 对外字典里不得出现明文 Token
    assert "token" not in cands[0].to_dict()


def test_import_docx_preview(client, monkeypatch):
    async def fake_verify(username, token):
        return "active", {"login": "real-login"}, ""

    monkeypatch.setattr("app.service.verify_token", fake_verify)
    data = make_docx(["alice", "ghp_" + "a" * 36])
    resp = client.post(
        "/api/accounts/import-docx?preview=true",
        files={"file": ("accounts.docx", data, DOCX_MIME)},
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["total"] == 1
    c = body["candidates"][0]
    assert c["username"] == "real-login"
    assert c["valid"] is True
    assert "token" not in c
    assert "a" * 36 not in resp.text


def test_import_docx_adds_accounts(client, monkeypatch):
    async def fake_verify(username, token):
        return "active", {"login": "alice"}, ""

    monkeypatch.setattr("app.service.verify_token", fake_verify)
    data = make_docx(["alice", "ghp_" + "a" * 36])
    resp = client.post(
        "/api/accounts/import-docx?preview=false&indices=0",
        files={"file": ("accounts.docx", data, DOCX_MIME)},
    )
    assert resp.status_code == 200
    assert resp.json()["added"] == 1
    accounts = config.load_accounts()
    assert len(accounts) == 1
    assert accounts[0]["username"] == "alice"
    assert accounts[0]["token"] == "ghp_" + "a" * 36


def test_import_docx_no_token(client):
    data = make_docx(["什么都没有", "只是一些文字"])
    resp = client.post(
        "/api/accounts/import-docx?preview=true",
        files={"file": ("accounts.docx", data, DOCX_MIME)},
    )
    assert resp.status_code == 200
    assert resp.json()["total"] == 0
    assert "message" in resp.json()


def test_import_docx_empty_indices(client, monkeypatch):
    async def fake_verify(username, token):
        return "active", {"login": "alice"}, ""

    monkeypatch.setattr("app.service.verify_token", fake_verify)
    data = make_docx(["alice", "ghp_" + "a" * 36])
    resp = client.post(
        "/api/accounts/import-docx?preview=false&indices=",
        files={"file": ("accounts.docx", data, DOCX_MIME)},
    )
    assert resp.status_code == 200
    assert resp.json()["added"] == 0
