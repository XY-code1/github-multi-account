@echo off
cd /d %~dp0
echo Stopping GitHub Dashboard ...
powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*app.main*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }" 2>nul
echo Done. Press any key to close.
pause >nul
