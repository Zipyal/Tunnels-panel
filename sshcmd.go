package main

import (
	"regexp"
	"strconv"
	"strings"
)

// SSHCommand разбор строки вида
//
//	ssh -f -N -L 1111:11.11.1.11:222 login@333.333.33.33 -p22
//
// Строку целиком мы в shell не отдаём: достаём параметры и собираем аргументы заново,
// поэтому подсунуть лишнюю команду через неё нельзя.
type SSHCommand struct {
	User, Host, Port string
	Forwards         []string // значения -L как есть
	Extra            []string // остальные опции ssh, передаём без изменений
	LocalPorts       []int
	Target           string // куда ведёт первый проброс
	Err              string
}

const (
	withValue = "bcDEeFIiJLlmOoQRSWwp" // флаги, после которых идёт значение
	ignored   = "fNTn"                 // -N добавим сами, остальные про терминал
)

var (
	tokenRe   = regexp.MustCompile(`"([^"]*)"|'([^']*)'|(\S+)`)
	forwardRe = regexp.MustCompile(`^(?:(?:\[[^\]]+\]|[^:]+):)?(\d+):(\[[^\]]+\]|[^:]+):(\d+)$`)
)

func (c *SSHCommand) LocalPort() int {
	if len(c.LocalPorts) == 0 {
		return 0
	}
	return c.LocalPorts[0]
}

// Слова с учётом кавычек: -i "C:\my keys\id" остаётся одним словом
func tokenize(line string) []string {
	var out []string
	for _, m := range tokenRe.FindAllStringSubmatch(line, -1) {
		switch {
		case m[3] != "":
			out = append(out, m[3])
		case m[2] != "":
			out = append(out, m[2])
		default:
			out = append(out, m[1])
		}
	}
	return out
}

func ParseSSH(line string) *SSHCommand {
	c := &SSHCommand{}
	tokens := tokenize(strings.TrimSpace(line))

	// Первое слово может быть самим "ssh" или "ssh.exe"
	if len(tokens) > 0 {
		first := strings.ToLower(tokens[0])
		if first == "ssh" || strings.HasSuffix(first, "/ssh") || strings.HasSuffix(first, `\ssh.exe`) || first == "ssh.exe" {
			tokens = tokens[1:]
		}
	}

	var positional []string
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok[0] != '-' || len(tok) < 2 {
			positional = append(positional, tok)
			continue
		}
		// "-fN" это два флага, "-p22" это флаг со значением, поэтому идём по символам
		for j := 1; j < len(tok); j++ {
			flag := tok[j]
			if strings.IndexByte(withValue, flag) >= 0 {
				value := tok[j+1:]
				if value == "" && i+1 < len(tokens) {
					i++
					value = tokens[i] // значение отдельным словом: "-p 22"
				}
				c.takeOption(flag, value)
				break
			}
			if strings.IndexByte(ignored, flag) < 0 {
				c.Extra = append(c.Extra, "-"+string(flag))
			}
		}
	}

	if len(positional) == 0 {
		c.Err = "Не нашёл user@host в команде"
		return c
	}
	dest := positional[0]
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		c.User, dest = dest[:at], dest[at+1:]
	}
	c.Host = dest

	switch {
	case c.Err != "":
	case c.Port != "" && !isDigits(c.Port):
		c.Err = "Некорректный порт после -p"
	case len(c.Forwards) == 0:
		c.Err = "Не нашёл -L (проброс порта)"
	}
	return c
}

func (c *SSHCommand) takeOption(flag byte, value string) {
	switch flag {
	case 'L':
		m := forwardRe.FindStringSubmatch(value)
		if m == nil {
			c.Err = "Не понял значение -L"
			return
		}
		port, _ := strconv.Atoi(m[1])
		c.Forwards = append(c.Forwards, value)
		c.LocalPorts = append(c.LocalPorts, port)
		if len(c.LocalPorts) == 1 {
			c.Target = m[2] + ":" + m[3]
		}
	case 'p':
		c.Port = value
	case 'l':
		c.User = value
	default:
		c.Extra = append(c.Extra, "-"+string(flag), value)
	}
}

// Args собирает аргументы для запуска ssh
func (c *SSHCommand) Args(withPassword bool) []string {
	// Свои опции пользователя идут первыми: у ssh для -o побеждает первое значение
	a := append([]string{}, c.Extra...)
	a = append(a, "-N",
		"-o", "ExitOnForwardFailure=yes", // порт занят -> сразу ошибка, а не молчаливый туннель без проброса
		"-o", "StrictHostKeyChecking=accept-new", // новый сервер принимаем, при смене ключа ssh откажется
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=15",
	)
	if withPassword {
		a = append(a, "-o", "NumberOfPasswordPrompts=1", "-o", "PreferredAuthentications=password,keyboard-interactive")
	} else {
		a = append(a, "-o", "BatchMode=yes") // без пароля ssh не должен зависать и ждать ввод
	}
	for _, f := range c.Forwards {
		a = append(a, "-L", f)
	}
	if c.Port != "" {
		a = append(a, "-p", c.Port)
	}
	if c.User != "" {
		a = append(a, "-l", c.User)
	}
	return append(a, c.Host)
}

func isDigits(s string) bool {
	_, err := strconv.ParseUint(s, 10, 32)
	return err == nil
}
