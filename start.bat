@echo off
chcp 65001 >nul
cd /d "%~dp0"
title SSH-туннели

where php >nul 2>nul
if errorlevel 1 (
  echo PHP не найден. Устанавливаю одной командой через winget...
  echo.
  winget install --id PHP.PHP.8.3 -e --accept-source-agreements --accept-package-agreements
  echo.
  echo Если установка прошла успешно: закройте это окно и запустите start.bat ещё раз.
  pause
  exit /b
)

where ssh >nul 2>nul
if errorlevel 1 (
  echo Не найден ssh. Откройте PowerShell от имени администратора и выполните:
  echo   Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0
  echo Потом запустите start.bat ещё раз.
  pause
  exit /b
)

echo Панель открывается в браузере. Это окно НЕ закрывайте, пока работаете.
echo Остановить панель: Ctrl+C. Туннели при этом продолжат работать.
echo.
php tunnels.php
pause
