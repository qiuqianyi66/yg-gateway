@echo off
chcp 65001 >nul
cd /d "%~dp0"
for /f "tokens=5" %%P in ('netstat -ano ^| findstr "127.0.0.1:7863" ^| findstr LISTENING') do echo running PID=%%P
if errorlevel 1 echo NOT running
curl -s http://127.0.0.1:7863/healthz
echo.
