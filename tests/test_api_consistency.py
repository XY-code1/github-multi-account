"""双版本 API 一致性测试。

同一份请求分别打到 Python 后端和 Go 后端，逐字段比对响应结构，
防止两套实现行为分叉（字段名、状态值、错误处理不一致）。

需要 Go 工具链；未安装时自动跳过。
"""
from __future__ import annotations

import json
import os
import shutil
import socket
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest

PROJECT = Path(__file__).resolve().parent.parent


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def wait_ready(port: int, timeout: int = 25) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/api/version", timeout=1):
                return True
        except Exception:  # noqa: BLE001
            time.sleep(0.2)
    return False


def get(port: int, path: str) -> tuple[int, dict]:
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=10) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:  # noqa: PERF203
        return e.code, json.loads(e.read().decode())


def keys_of(value, prefix: str = "") -> set[str]:
    """递归收集 JSON 的字段名（用于结构比对）。"""
    out: set[str] = set()
    if isinstance(value, dict):
        for k, v in value.items():
            out.add(f"{prefix}{k}")
            out |= keys_of(v, f"{prefix}{k}.")
    elif isinstance(value, list) and value:
        out |= keys_of(value[0], f"{prefix}[0].")
    return out


@pytest.fixture(scope="module")
def backends(tmp_path_factory):
    """启动 Python 与 Go 两个后端，返回 (python_port, go_port)。"""
    if not shutil.which("go"):
        pytest.skip("未安装 Go，跳过双版本一致性测试")
    if not shutil.which("python3"):
        pytest.skip("未找到 python3")

    base = tmp_path_factory.mktemp("consistency")
    config = base / "config"
    data = base / "data"
    config.mkdir()
    data.mkdir()

    # --- Go ---
    go_bin = base / "ghdash-go"
    env = dict(os.environ, GOFLAGS="-mod=mod")
    build = subprocess.run(
        ["go", "build", "-o", str(go_bin), "."],
        cwd=PROJECT / "go-app", env=env, capture_output=True, timeout=600,
    )
    if build.returncode != 0:
        pytest.skip(f"Go 构建失败: {build.stderr.decode()[:200]}")

    # Go 应用自行选择端口并写入 data/server.port
    port_file = base / "data" / "server.port"
    go_env = dict(os.environ, GAD_BASE_DIR=str(base))
    go_proc = subprocess.Popen(
        [str(go_bin)], env=go_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )

    go_port = 0
    deadline = time.time() + 25
    while time.time() < deadline:
        if port_file.exists() and port_file.read_text().strip().isdigit():
            go_port = int(port_file.read_text().strip())
            break
        time.sleep(0.2)
    assert go_port > 0, "Go 后端未能写出 data/server.port"

    # --- Python ---
    py_port = free_port()
    py_env = dict(os.environ, GAD_CONFIG_DIR=str(config), GAD_DATA_DIR=str(data))
    py_proc = subprocess.Popen(
        ["python3", "-m", "uvicorn", "app.main:app", "--port", str(py_port), "--log-level", "error"],
        cwd=PROJECT, env=py_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )

    try:
        assert wait_ready(go_port), "Go 后端未能启动"
        assert wait_ready(py_port), "Python 后端未能启动"
        yield py_port, go_port
    finally:
        for p in (go_proc, py_proc):
            p.terminate()
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill()


# 这些字段值每次运行都会变，比对时忽略
VOLATILE = {
    "last_checked", "exported_at", "version", "created_at",
    "first", "last", "id", "account_id",
}

CASES = [
    "/api/accounts",
    "/api/status",
    "/api/search?q=test",
    "/api/version",
]


@pytest.mark.parametrize("path", CASES)
def test_response_structure_matches(backends, path):
    py_port, go_port = backends
    py_status, py_body = get(py_port, path)
    go_status, go_body = get(go_port, path)

    assert py_status == go_status, f"{path}: 状态码不一致 Python={py_status} Go={go_status}"

    py_keys = {k for k in keys_of(py_body) if k.split(".")[-1].rstrip("[0]") not in VOLATILE}
    go_keys = {k for k in keys_of(go_body) if k.split(".")[-1].rstrip("[0]") not in VOLATILE}

    missing_in_go = sorted(py_keys - go_keys)
    missing_in_py = sorted(go_keys - py_keys)
    assert not missing_in_go, f"{path}: Go 缺少字段 {missing_in_go}"
    assert not missing_in_py, f"{path}: Go 多出字段 {missing_in_py}"


@pytest.mark.parametrize("path", ["/api/status/999", "/api/history/999"])
def test_error_handling_matches(backends, path):
    py_port, go_port = backends
    py_status, _ = get(py_port, path)
    go_status, _ = get(go_port, path)
    assert py_status == go_status == 404, f"{path}: Python={py_status} Go={go_status}"
