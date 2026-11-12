"""从 .docx 文档中识别 GitHub Token 与用户名。

.docx 本质是一个 zip 包，正文在 word/document.xml。这里不依赖 python-docx，
直接解压 + 解析 XML，按段落（<w:p>）提取文本，表格单元格同样按段落处理。

Token 形态：
- Classic PAT：ghp_ / gho_ / ghu_ / ghs_ / ghr_ + 36 位以上字母数字
- Fine-grained PAT：github_pat_ + 下划线与字母数字

用户名的权威来源是 GitHub /user 接口（用 Token 反查 login），文档里识别到的
用户名只作为无效 Token 的兜底显示名。
"""
from __future__ import annotations

import io
import re
import zipfile
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from typing import Any

W_NS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

TOKEN_RE = re.compile(r"gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}")
LABEL_RE = re.compile(
    r"(?:用户名|账号|登录名|登陆名|login|username|user|name)\s*[:：]\s*"
    r"([A-Za-z0-9][A-Za-z0-9-]{0,37})",
    re.IGNORECASE,
)
MENTION_RE = re.compile(r"@([A-Za-z0-9][A-Za-z0-9-]{0,37})")
WORD_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{0,37}")
BARE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{0,37}")

SKIP_WORDS = {
    "token", "github", "pat", "ghp", "gho", "ghu", "ghs", "ghr",
    "用户名", "账号", "密码", "password", "secret", "key", "access",
}


@dataclass
class Candidate:
    """一个待导入账号。token 只在服务端使用，不对外返回。"""

    index: int
    username: str
    label: str
    token_masked: str
    token: str = ""
    valid: bool = False
    error: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "username": self.username,
            "label": self.label,
            "token_masked": self.token_masked,
            "valid": self.valid,
            "error": self.error,
        }


@dataclass
class Found:
    """文档里识别到的一条原始记录。"""

    username: str
    token: str
    extra: dict[str, Any] = field(default_factory=dict)


def extract_lines(data: bytes) -> list[str]:
    """按段落提取 .docx 文本（缺失或损坏时返回空列表）。"""
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as z:
            xml = z.read("word/document.xml")
    except (KeyError, zipfile.BadZipFile, OSError):
        return []
    try:
        root = ET.fromstring(xml)
    except ET.ParseError:
        return []
    lines: list[str] = []
    for p in root.iter(f"{{{W_NS}}}p"):
        text = "".join(t.text or "" for t in p.iter(f"{{{W_NS}}}t")).strip()
        if text:
            lines.append(text)
    return lines


def find_candidates(data: bytes) -> list[Found]:
    """从文档字节中识别 Token 与上下文用户名，按出现顺序去重。"""
    lines = extract_lines(data)
    found: list[Found] = []
    seen: set[str] = set()
    for idx, line in enumerate(lines):
        for token in TOKEN_RE.findall(line):
            if token in seen:
                continue
            seen.add(token)
            found.append(Found(username=_username_near(lines, idx, line), token=token))
    return found


def _username_near(lines: list[str], idx: int, line: str) -> str:
    """从当前行 / 相邻行猜测 GitHub 用户名。"""
    m = LABEL_RE.search(line) or MENTION_RE.search(line)
    if m:
        return m.group(1)

    cleaned = TOKEN_RE.sub(" ", line)
    m = MENTION_RE.search(cleaned)
    if m:
        return m.group(1)
    for word in WORD_RE.findall(cleaned):
        if word.lower() not in SKIP_WORDS:
            return word

    for j in (idx - 1, idx + 1):
        if 0 <= j < len(lines):
            m = LABEL_RE.search(lines[j]) or MENTION_RE.search(lines[j])
            if m:
                return m.group(1)
            bare = lines[j].strip()
            if BARE_RE.fullmatch(bare) and bare.lower() not in SKIP_WORDS:
                return bare
    return ""


def build_candidates(found: list[Found], mask: Any) -> list[Candidate]:
    """把原始记录转成对外返回的候选（Token 脱敏）。"""
    out: list[Candidate] = []
    for i, f in enumerate(found):
        label = f.username or ""
        out.append(Candidate(
            index=i, username=f.username, label=label,
            token_masked=mask(f.token), token=f.token,
        ))
    return out
