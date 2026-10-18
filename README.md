# SSH-туннели

Веб-панель для SSH-туннелей: один исполняемый файл, интерфейс встроен (`public/`), внешних зависимостей Go нет. Использует системный `ssh`.

## Сборка (нужен Go 1.21+)

    # Windows, без чёрного окна консоли
    GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o SSH-tunnels.exe .
    # Linux
    GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o ssh-tunnels-linux .
    # macOS (Intel / Apple Silicon)
    GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w" -o ssh-tunnels-mac-intel .
    GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w" -o ssh-tunnels-mac-apple-silicon .

Тесты: `go test ./...`

Данные (туннели, пароли) лежат в `tunnels-data/` рядом с программой, в git их не добавлять.
