#!/bin/sh
# Состояние прошлого запуска не нужно: при перезапуске контейнера все ssh-процессы всё равно умерли
rm -f /data/state.json /data/logs/*

# Ключи из папки keys/. Docker на Windows показывает файлам права 777,
# а ssh такие ключи не принимает, поэтому копируем и ставим 600
mkdir -p /root/.ssh && chmod 700 /root/.ssh
for key in /keys/*; do
    [ -f "$key" ] && cp "$key" /root/.ssh/ && chmod 600 "/root/.ssh/$(basename "$key")"
done

# exec, чтобы php стал основным процессом и получал сигнал остановки от docker
exec php -S 0.0.0.0:8099 -t /app/public /app/index.php
