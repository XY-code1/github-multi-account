@echo off
cd /d %~dp0

if not exist .venv\Scripts\activate.bat (
    echo Creating virtual environment ...
    python -m venv .venv
)

if exist .venv\Scripts\activate.bat (
    call .venv\Scripts\activate.bat
) else (
    echo [ERROR] Failed to create virtual environment.
    echo         Please check that Python 3 is installed and in PATH.
    pause
    exit /b 1
)

echo Installing dependencies ...
pip install -q -r requirements.txt

if not exist config\accounts.yaml (
    copy config\accounts.example.yaml config\accounts.yaml >nul
    echo.
    echo [NOTE] Created config/accounts.yaml
    echo        Please edit it and fill in your GitHub Token, then restart.
    echo.
)

rem --- Port: use argument if given, otherwise auto-detect ---
set "PORT=%1"
if not "%PORT%"=="" goto :run
for /f "tokens=*" %%P in ('python find_port.py') do set "PORT=%%P"

:run
if "%PORT%"=="" set "PORT=8000"
if "%PORT%"=="0" (
    echo [ERROR] No available port found in the candidate list.
    echo         Specify one manually, e.g.:  start.bat 8888
    pause
    exit /b 1
)

echo.
echo =========================================
echo   Dashboard: http://localhost:%PORT%
echo   Press Ctrl+C to stop.
echo =========================================

rem Open the browser once the server is up
start "" /b python -c "import time,webbrowser;time.sleep(2);webbrowser.open('http://localhost:%PORT%')"

uvicorn app.main:app --host 127.0.0.1 --port %PORT%

if errorlevel 1 (
    echo.
    echo [ERROR] Server exited with an error. See the messages above.
    pause
)
