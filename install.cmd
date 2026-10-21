@echo off
chcp 65001 >nul
rem 관리자 권한이 아니면 관리자 권한으로 다시 실행합니다.
net session >nul 2>&1
if %errorlevel% neq 0 (
  powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
cd /d "%~dp0"
start "" /wait "%~dp0gcfldrive.exe" install
pause
