@echo off
chcp 65001 >nul
cd /d %~dp0

echo =========================================
echo   GitHub Dashboard - build (Windows)
echo =========================================

where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go not found. Please install from https://go.dev/dl/
    pause
    exit /b 1
)

echo [1/3] Copying frontend assets ...
if not exist assets mkdir assets
xcopy /E /I /Y "..\static" "assets\static" >nul
copy /Y "..\templates\index.html" "assets\index.html" >nul

echo [2/3] Building ...
go build -ldflags "-H windowsgui -s -w" -o "GitHub仪表盘.exe" .
if errorlevel 1 (
    echo [ERROR] Build failed. See the messages above.
    pause
    exit /b 1
)

echo [3/3] Done: %~dp0GitHub仪表盘.exe
echo.
echo Place this exe next to the config\ folder and double-click it.
pause
