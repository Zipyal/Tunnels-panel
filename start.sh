#!/bin/sh
cd "$(dirname "$0")"
if ! command -v php >/dev/null 2>&1; then
  echo "PHP не найден. Устанавливаю..."
  if command -v brew >/dev/null 2>&1; then brew install php
  elif command -v apt >/dev/null 2>&1; then sudo apt install -y php-cli
  elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y php-cli
  else echo "Установите PHP вручную."; exit 1; fi
fi
command -v ssh >/dev/null 2>&1 || { echo "Не найден ssh (пакет openssh-client)."; exit 1; }
echo "Панель открывается в браузере. Остановить: Ctrl+C (туннели продолжат работать)."
exec php tunnels.php
