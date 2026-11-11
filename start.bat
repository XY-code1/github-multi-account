@echo off
chcp 65001 >nul
cd /d %~dp0

if not exist .venv (
    echo 创建虚拟环境 .venv ...
    python -m venv .venv
)
call .venv\Scripts\activate.bat

echo 安装依赖 ...
pip install -q -r requirements.txt

if not exist config\accounts.yaml (
    copy config\accounts.example.yaml config\accounts.yaml >nul
    echo 已创建 config\accounts.yaml —— 请填入你的 GitHub Token 后重启
)

echo.
echo =========================================
echo   启动成功：http://localhost:8000
echo =========================================
uvicorn app.main:app --reload --port 8000
pause
