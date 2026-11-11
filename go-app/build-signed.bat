@echo off
chcp 65001 >nul
cd /d %~dp0

echo =========================================
echo   签名构建（需要代码签名证书）
echo =========================================

if "%~1"=="" (
    echo.
    echo 用法:  build-signed.bat ^<证书指纹^>
    echo.
    echo 证书指纹获取方式（管理员 PowerShell）:
    echo   Get-ChildItem -Cert Cert:\CurrentUser\My ^| Format-Table Subject, Thumbprint
    echo.
    echo 示例:  build-signed.bat 0A1B2C3D4E5F...
    echo.
    pause
    exit /b 1
)

set CERT_THUMBPRINT=%~1
set TIMESTAMP_URL=http://timestamp.digicert.com

where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go not found. Please install from https://go.dev/dl/
    pause
    exit /b 1
)

echo [1/4] 同步前端资源 ...
if not exist assets mkdir assets
xcopy /E /I /Y "..\static" "assets\static" >nul
copy /Y "..\templates\index.html" "assets\index.html" >nul

echo [2/4] 构建 ...
go build -ldflags "-H windowsgui -s -w" -o "GitHub仪表盘-unsigned.exe" .
if errorlevel 1 (
    echo [ERROR] Build failed.
    pause
    exit /b 1
)

echo [3/4] 数字签名 ...
signtool sign /fd sha256 /tr %TIMESTAMP_URL% /td sha256 /sha1 %CERT_THUMBPRINT% /v "GitHub仪表盘-unsigned.exe"
if errorlevel 1 (
    echo [ERROR] 签名失败。请确认证书指纹正确、证书已安装、且本机时间准确。
    pause
    exit /b 1
)

echo [4/4] 完成
move /Y "GitHub仪表盘-unsigned.exe" "GitHub仪表盘.exe" >nul
echo.
echo 已生成签名版本: %~dp0GitHub仪表盘.exe
echo 用 signtool verify /pa "GitHub仪表盘.exe" 可校验签名
pause
