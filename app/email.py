"""邮箱面板：配置、验证码提取、邮件解析与拉取（预留接口）。

用途：查看与账号关联邮箱收到的通知 / 安全邮件 / 登录验证码，**只做展示**，
不会把验证码自动填到任何外部网站。

邮箱站（如「邮笺」）通常是纯浏览器应用：每个邮箱对应一个公开取件链接，
看邮件时才用该链接连服务器读取。因此本模块不保存账号密码，只保存：
- 账号上的 `email` 字段（邮箱地址或取件链接）；
- 可选的 `email.api_base` 取件接口模板（把邮箱地址拼成请求 URL）。

配置（config/settings.yaml 的 email 段）：
    email:
      enabled: false
      site_url: https://inbox.123456.world
      api_base: ""              # 取件接口模板，支持 {email}；留空则用账号 email 里的完整 URL
      token: ""                 # 可选，Bearer 令牌
      fetch_interval_seconds: 300
      code_pattern: ""          # 自定义验证码正则，留空用内置规则
      highlight_context: true
"""
from __future__ import annotations

import json
import logging
import re
from pathlib import Path
from typing import Any
from urllib.parse import quote

import httpx
import yaml

from . import config, db
from .github import USER_AGENT

logger = logging.getLogger(__name__)

DEFAULTS: dict[str, Any] = {
    "enabled": False,
    # 「邮笺」取件接口：账号 email 字段可保存完整链接或 token
    "site_url": "https://inbox.123456.world",
    "api_base": "https://inbox.123456.world/i/{email}/json",
    "token": "",
    "fetch_interval_seconds": 300,
    "code_pattern": "",
    "highlight_context": True,
    # 单次刷新最多翻多少页（每页默认 pageSize=1，服务端 hasMore 控制）
    "max_pages": 5,
}

# 上下文关键词：优先在这些词附近提取验证码
CONTEXT_RE = re.compile(r"(验证码|校验码|动态码|verification|verify|one[- ]time|code)", re.I)
# 数字型验证码（4-8 位）
DIGITS_RE = re.compile(r"(?<!\d)(\d{4,8})(?!\d)")
# 字母数字混合型（6-8 位，至少含一个数字）
ALNUM_RE = re.compile(r"(?<![A-Za-z0-9])([A-Za-z0-9]{6,8})(?![A-Za-z0-9])")


def load_settings() -> dict[str, Any]:
    """读取 email 配置（缺失或损坏时用默认值）。"""
    cfg = dict(DEFAULTS)
    path = Path(config.CONFIG_DIR) / "settings.yaml"
    data: dict[str, Any] = {}
    try:
        if path.exists():
            data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    except Exception:  # noqa: BLE001
        data = {}
    for k, v in ((data or {}).get("email") or {}).items():
        if k in DEFAULTS:
            cfg[k] = v
    return cfg


def save_settings(cfg: dict[str, Any]) -> dict[str, Any]:
    """写回 email 配置（读取-合并-写回，保留其他分节）。"""
    clean = {k: cfg.get(k, DEFAULTS[k]) for k in DEFAULTS}
    data = config.read_settings()
    data["email"] = clean
    config.write_settings(data)
    return clean


def extract_code(text: str, pattern: str = "", context: bool = True) -> str | None:
    """从邮件正文提取验证码，只返回第一个匹配（展示用，不参与任何自动填充）。"""
    if not text:
        return None
    if pattern:
        try:
            m = re.search(pattern, text)
        except re.error:
            m = None
        if m:
            return m.group(1) if m.groups() else m.group(0)

    lines = [l for l in text.splitlines() if l.strip()]
    context_lines = [l for l in lines if CONTEXT_RE.search(l)] if context else []
    for line in context_lines + lines:
        m = DIGITS_RE.search(line)
        if m:
            return m.group(1)
        m = ALNUM_RE.search(line)
        if m and any(c.isdigit() for c in m.group(1)):
            return m.group(1)
    return None


def build_fetch_url(email: str, cfg: dict[str, Any]) -> str:
    """根据配置把账号 email 字段转换成取件 URL（无法构造时返回空串）。

    账号 email 可以是完整取件链接（直接使用），也可以是 token
    （用 api_base 模板拼接，如 https://inbox.123456.world/i/{email}/json）。
    """
    target = (email or "").strip()
    if not target:
        return ""
    if target.lower().startswith(("http://", "https://")):
        return target
    template = (cfg.get("api_base") or "").strip()
    if not template:
        return ""
    return template.replace("{email}", quote(target, safe=""))


def with_page(url: str, page: int) -> str:
    """给取件 URL 追加分页参数（page 从 1 开始）。"""
    if page <= 1:
        return url
    sep = "&" if "?" in url else "?"
    return f"{url}{sep}page={page}"


def parse_payload(payload: Any, account_email: str) -> tuple[list[dict[str, Any]], str]:
    """解析取件接口响应，返回 (邮件列表, 顶层验证码)。

    「邮笺」响应形态：{ok, email, verificationCode, latestMessage, messages[], total,
    page, pageSize, hasMore, expiresAt}；兼容 messages/mails/items/data 等包装。
    """
    if not isinstance(payload, dict):
        return parse_messages(payload, account_email), ""
    verify_code = str(payload.get("verificationCode") or "")
    messages = payload.get("messages")
    if not isinstance(messages, list):
        for key in ("mails", "items", "data"):
            if isinstance(payload.get(key), list):
                messages = payload[key]
                break
    if messages is None:
        messages = []
    parsed = parse_messages(messages, account_email)
    if parsed:
        parsed[0]["email"] = parsed[0].get("email") or str(payload.get("email") or account_email)
    return parsed, verify_code


def parse_messages(payload: Any, account_email: str) -> list[dict[str, Any]]:
    """把接口响应解析成统一邮件结构。

    兼容三种常见形态：列表、{"messages": [...]}、以及无法解析的原始文本
    （此时作为单封邮件的正文保存，便于人工查看）。
    """
    items: list[Any] = []
    if isinstance(payload, list):
        items = payload
    elif isinstance(payload, dict):
        for key in ("messages", "mails", "items", "data"):
            if isinstance(payload.get(key), list):
                items = payload[key]
                break
        if not items:
            items = [payload]
    elif isinstance(payload, str) and payload.strip():
        items = [{"body": payload}]

    out: list[dict[str, Any]] = []
    for it in items:
        if not isinstance(it, dict):
            if isinstance(it, str) and it.strip():
                it = {"body": it}
            else:
                continue
        body = (
            it.get("body")
            or it.get("text")
            or it.get("content")
            or it.get("html")
            or ""
        )
        if not isinstance(body, str):
            body = json.dumps(body, ensure_ascii=False)
        sender = (
            it.get("sender")
            or it.get("from")
            or it.get("from_name")
            or it.get("sender_name")
            or ""
        )
        if isinstance(sender, dict):
            sender = sender.get("name") or sender.get("address") or ""
        out.append(
            {
                "email": it.get("email") or account_email,
                "sender": str(sender)[:200],
                "subject": str(it.get("subject") or it.get("title") or "")[:300],
                "body": body[:20000],
                "message_id": str(it.get("id") or it.get("message_id") or "")[:200],
                "received_at": str(
                    it.get("received_at") or it.get("date") or it.get("created_at") or ""
                )[:40],
            }
        )
    return out


async def fetch_account_mail(
    account: dict[str, Any], cfg: dict[str, Any] | None = None
) -> dict[str, Any]:
    """拉取单个账号邮箱的邮件（通用预留实现）。

    - 账号 email 是完整 URL → 直接 GET；
    - 否则用 email.api_base 模板（支持 {email}）拼接；
    - 两者都无法构造时返回 not_configured 与提示。
    """
    cfg = cfg or load_settings()
    email = (account.get("email") or "").strip()
    url = build_fetch_url(email, cfg)
    if not url:
        return {
            "ok": False,
            "reason": "not_configured",
            "hint": "账号 email 需填写取件链接，或在 email.api_base 配置取件接口模板（支持 {email}）",
        }
    headers = {"User-Agent": USER_AGENT, "Accept": "application/json,text/plain,*/*"}
    if cfg.get("token"):
        headers["Authorization"] = f"Bearer {cfg['token']}"
    max_pages = max(1, int(cfg.get("max_pages") or 5))
    messages: list[dict[str, Any]] = []
    verify_code = ""
    try:
        async with httpx.AsyncClient(timeout=15.0, follow_redirects=True) as client:
            for page in range(1, max_pages + 1):
                resp = await client.get(with_page(url, page), headers=headers)
                if resp.status_code != 200:
                    if page == 1:
                        return {
                            "ok": False,
                            "reason": "http_error",
                            "status": resp.status_code,
                            "url": url,
                        }
                    break
                payload: Any
                if "json" in resp.headers.get("content-type", ""):
                    try:
                        payload = resp.json()
                    except ValueError:
                        payload = resp.text
                else:
                    payload = resp.text
                batch, code = parse_payload(payload, email)
                verify_code = verify_code or code
                messages.extend(batch)
                if not isinstance(payload, dict) or not payload.get("hasMore") or not batch:
                    break
    except Exception as e:  # noqa: BLE001
        return {"ok": False, "reason": "network_error", "error": str(e), "url": url}

    added = 0
    pattern = cfg.get("code_pattern") or ""
    use_context = bool(cfg.get("highlight_context", True))
    for idx, msg in enumerate(messages):
        code = (
            msg.get("code")
            or extract_code(msg["body"], pattern, use_context)
            or (verify_code if idx == 0 else "")
        )
        if db.add_email_message(
            account_id=account.get("id"),
            email=msg["email"] or email,
            message_id=msg["message_id"],
            sender=msg["sender"],
            subject=msg["subject"],
            body=msg["body"],
            received_at=msg["received_at"],
            code=code or None,
        ):
            added += 1
    return {
        "ok": True,
        "url": url,
        "fetched": len(messages),
        "added": added,
        "verificationCode": verify_code,
        "email": email,
    }
