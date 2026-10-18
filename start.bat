@echo off
chcp 65001 >nul
cd /d "%~dp0"

docker info >nul 2>nul
if errorlevel 1 (
  echo Docker не запущен или не установлен.
  echo.
  echo Если не установлен, выполните в консоли одну команду:
  echo   winget install -e --id Docker.DockerDesktop
  echo Потом перезагрузите компьютер и запустите Docker Desktop.
  pause
  exit /b 1
)

docker compose up -d --build
if errorlevel 1 ( pause & exit /b 1 )

start "" http://localhost:8099
