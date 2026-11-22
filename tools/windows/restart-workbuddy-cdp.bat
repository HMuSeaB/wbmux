@echo off
setlocal
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0restart-workbuddy-cdp.ps1" %*
echo.
pause
