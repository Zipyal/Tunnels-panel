#!/bin/sh
cd "$(dirname "$0")" || exit 1
docker compose up -d --build || exit 1
echo "Панель: http://localhost:8099"
