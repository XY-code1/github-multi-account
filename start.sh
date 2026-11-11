#!/usr/bin/env bash
# macOS / Linux 一键启动
set -e
cd "$(dirname "$0")"

if [ ! -d .venv ]; then
    echo "创建虚拟环境 .venv ..."
    python3 -m venv .venv
fi
source .venv/bin/activate

echo "安装依赖 ..."
pip install -q -r requirements.txt

if [ ! -f config/accounts.yaml ]; then
    cp config/accounts.example.yaml config/accounts.yaml
    echo "已创建 config/accounts.yaml —— 请填入你的 GitHub Token 后重启"
fi

echo ""
echo "========================================="
echo "  启动成功：http://localhost:8000"
echo "========================================="
uvicorn app.main:app --reload --port 8000
