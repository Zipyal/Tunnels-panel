//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// listeningPorts читает таблицу сокетов ядра: один файл вместо соединения с каждым портом.
// Соединяться с портом туннеля нельзя: ssh на каждое такое соединение ходит на удалённый сервер.
func listeningPorts() (map[int]bool, bool) {
	ports := map[int]bool{}
	read := false
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		read = true
		for i, line := range strings.Split(string(raw), "\n") {
			cols := strings.Fields(line)
			// колонки: sl local_address rem_address st ...; st=0A это LISTEN; первая строка заголовок
			if i == 0 || len(cols) < 4 || cols[3] != "0A" {
				continue
			}
			addr := cols[1]
			if p, err := strconv.ParseInt(addr[strings.LastIndexByte(addr, ':')+1:], 16, 32); err == nil {
				ports[int(p)] = true
			}
		}
	}
	return ports, read
}
