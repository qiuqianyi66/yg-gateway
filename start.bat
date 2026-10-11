@echo off
chcp 65001 >nul
cd /d "%~dp0"
if exist "%~dp0data\yg-gateway.pid" (
  for /f %%P in ('type "%~dp0data\yg-gateway.pid"') do taskkill /PID %%P /F >nul 2>&1
  del /q "%~dp0data\yg-gateway.pid" >nul 2>&1
  echo stopped old instance
)
start "yg-gateway" /MIN "" "%~dp0yg-gateway.exe" -config "%~dp0config.json" >> "%~dp0data\stdout.log" 2>> "%~dp0data\stderr.log"
timeout /t 3 >nul
for /f "tokens=5" %%P in ('netstat -ano ^| findstr "127.0.0.1:7863" ^| findstr LISTENING') do (
  echo %%P> "%~dp0data\yg-gateway.pid"
  echo started PID=%%P
)
echo panel: http://127.0.0.1:7863/panel/
