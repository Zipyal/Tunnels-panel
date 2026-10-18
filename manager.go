package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// View это туннель вместе с текущим статусом, в таком виде он уходит в интерфейс
type View struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Command     string  `json:"command"`
	HasPassword bool    `json:"hasPassword"`
	URL         string  `json:"url"`
	LocalPort   int     `json:"localPort"`
	Host        string  `json:"host"`
	User        string  `json:"user"`
	SSHPort     string  `json:"sshPort"`
	Target      string  `json:"target"`
	Status      string  `json:"status"` // down | connecting | up | failed | invalid
	Error       *string `json:"error"`
	Warn        *string `json:"warn"`
}

// proc это процесс ssh, запущенный в этой сессии панели
type proc struct {
	cmd    *exec.Cmd
	exited atomic.Bool
}

type Manager struct {
	store   *Store
	self    string // путь к нашему exe, он же askpass
	sshPath string // найденный ssh, пусто если нет

	mu          sync.Mutex // защищает procs и поля ниже
	procs       map[string]*proc
	installing  bool   // идёт установка ssh (см. install.go)
	installErr  string // почему не удалась последняя установка
	installDone string // версия ssh, если установка прошла успешно
}

func NewManager(store *Store, self string) *Manager {
	return &Manager{store: store, self: self, sshPath: findSSH(), procs: map[string]*proc{}}
}

const loginPlaceholder = "{login}"
const needLoginMsg = "Укажите свой логин в поле вверху страницы."

// resolve подставляет данные пользователя: {login} -> логин, пустой пароль туннеля -> общий пароль
func resolve(t Tunnel, set Settings) (command, password string) {
	command = strings.ReplaceAll(t.Command, loginPlaceholder, set.Login)
	password = t.Password
	if password == "" {
		password = set.Password
	}
	return
}

func needsLogin(t Tunnel, set Settings) bool {
	return strings.Contains(t.Command, loginPlaceholder) && set.Login == ""
}

func (m *Manager) SSHPath() string { return m.ssh() }

func (m *Manager) SSHMissing() bool { return m.ssh() == "" }

// ssh возвращает путь к ssh. Он может появиться, пока панель работает (после автоустановки).
func (m *Manager) ssh() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sshPath
}

const sshHint = "Не найден ssh. Откройте PowerShell от имени администратора и выполните: " +
	"Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0"

func findSSH() string {
	if p := os.Getenv("SSH_BIN"); p != "" { // для тестов
		return p
	}
	if p, err := exec.LookPath("ssh"); err == nil {
		return p
	}
	// Программа запущена двойным щелчком и могла получить устаревший PATH,
	// поэтому смотрим и в обычных местах установки
	for _, p := range knownSSHPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func knownSSHPaths() []string {
	var out []string
	add := func(env string, parts ...string) {
		if base := os.Getenv(env); base != "" {
			out = append(out, filepath.Join(append([]string{base}, parts...)...))
		}
	}
	add("SystemRoot", "System32", "OpenSSH", "ssh.exe") // встроенный в Windows 10/11
	add("ProgramFiles", "Git", "usr", "bin", "ssh.exe") // Git for Windows
	add("ProgramFiles(x86)", "Git", "usr", "bin", "ssh.exe")
	add("LocalAppData", "Programs", "Git", "usr", "bin", "ssh.exe") // Git, установленный только для себя
	add("USERPROFILE", "scoop", "apps", "git", "current", "usr", "bin", "ssh.exe")
	add("ProgramFiles", "OpenSSH", "ssh.exe")
	return out
}

// Views считает статусы всех туннелей. Порты проверяем параллельно,
// поэтому список отвечает быстро даже при десятке туннелей.
func (m *Manager) Views(list []Tunnel) []View {
	set := m.store.Settings()
	cmds := make([]*SSHCommand, len(list))
	var ports []int
	for i, t := range list {
		command, _ := resolve(t, set)
		cmds[i] = ParseSSH(command)
		if cmds[i].Err == "" {
			ports = append(ports, cmds[i].LocalPort())
		}
	}
	open := openPorts(ports)
	state := m.store.State()

	views := make([]View, len(list))
	for i, t := range list {
		views[i] = m.view(t, cmds[i], open, state, set)
	}
	return views
}

func (m *Manager) view(t Tunnel, c *SSHCommand, open map[int]bool, state map[string]StateEntry, set Settings) View {
	v := View{
		ID: t.ID, Name: t.Name, Command: t.Command, HasPassword: t.Password != "" || set.Password != "",
		URL: t.URL, LocalPort: c.LocalPort(), Host: c.Host, User: c.User, SSHPort: c.Port,
		Target: c.Target, Status: "down",
	}
	if v.SSHPort == "" {
		v.SSHPort = "22"
	}
	if v.URL == "" && v.LocalPort != 0 {
		v.URL = "http://localhost:" + strconv.Itoa(v.LocalPort)
	}
	if c.Err != "" {
		v.Status, v.Error = "invalid", &c.Err
		return v
	}
	if needsLogin(t, set) {
		warn := needLoginMsg
		v.Warn = &warn
	}

	st, started := state[t.ID]
	switch {
	case open[v.LocalPort]:
		v.Status = "up"
	case started:
		if m.alive(t.ID, st) {
			v.Status = "connecting" // ssh работает, порт ещё не открыт
		} else {
			// Процесс умер, а мы его не останавливали: пароль не подошёл, порт занят, сервер недоступен
			msg := m.logTail(t.ID)
			if msg == "" {
				msg = "Процесс ssh завершился без сообщения"
			}
			v.Status, v.Error = "failed", &msg
		}
	}
	return v
}

// Start запускает туннель. Возвращает текст ошибки или пустую строку.
func (m *Manager) Start(t Tunnel) string {
	set := m.store.Settings()
	if needsLogin(t, set) {
		return needLoginMsg
	}
	command, password := resolve(t, set)
	c := ParseSSH(command)
	if c.Err != "" {
		return c.Err
	}
	sshPath := m.ssh()
	if sshPath == "" {
		return sshHint
	}
	if openPorts([]int{c.LocalPort()})[c.LocalPort()] {
		return "" // уже работает
	}
	if st, ok := m.store.State()[t.ID]; ok && m.alive(t.ID, st) {
		return "" // уже запускается
	}

	logFile, err := os.Create(m.store.LogPath(t.ID))
	if err != nil {
		return "Не удалось создать лог: " + err.Error()
	}

	cmd := exec.Command(sshPath, c.Args(password != "")...)
	// Пароль ssh берёт у askpass. Askpass это наш же exe: он печатает пароль из SSH_PW.
	// В командной строке ssh пароля нет.
	cmd.Env = append(os.Environ(),
		"SSH_ASKPASS="+shortPath(m.self),
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=localhost:0",
		"SSH_TUNNELS_ASKPASS=1",
		"SSH_PW="+password,
	)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd) // без окна

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return "Не удалось запустить ssh: " + err.Error()
	}
	bindToPanel(cmd) // туннель живёт, пока работает панель
	p := &proc{cmd: cmd}
	m.mu.Lock()
	m.procs[t.ID] = p
	m.mu.Unlock()

	// Wait нужен, чтобы забрать завершившийся процесс и узнать, что он умер
	go func() {
		_ = cmd.Wait()
		p.exited.Store(true)
		logFile.Close()
	}()

	m.store.UpdateState(func(s map[string]StateEntry) {
		s[t.ID] = StateEntry{Pid: cmd.Process.Pid, Started: time.Now().Unix()}
	})
	return ""
}

// StopAll гасит все процессы ssh, запущенные этой панелью (вызывается при выходе)
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		_ = p.cmd.Process.Kill()
	}
}

// Stop останавливает туннель. Возвращает текст ошибки или пустую строку.
func (m *Manager) Stop(t Tunnel) string {
	c := ParseSSH(t.Command)
	st, known := m.store.State()[t.ID]

	m.mu.Lock()
	p := m.procs[t.ID]
	delete(m.procs, t.ID)
	m.mu.Unlock()

	switch {
	case p != nil:
		_ = p.cmd.Process.Kill()
	case known && pidAlive(st.Pid):
		// Туннель запущен в прошлый раз: процесс ищем по pid из state.json
		if proc, err := os.FindProcess(st.Pid); err == nil {
			_ = proc.Kill()
		}
	case c.Err == "" && openPorts([]int{c.LocalPort()})[c.LocalPort()]:
		return "Этот туннель запущен не из панели, и я не знаю его процесс. " +
			"Закройте его вручную (в Windows: taskkill /IM ssh.exe /F)."
	}
	m.store.UpdateState(func(s map[string]StateEntry) { delete(s, t.ID) })
	return ""
}

func (m *Manager) alive(id string, st StateEntry) bool {
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p != nil {
		return !p.exited.Load()
	}
	return pidAlive(st.Pid) // после перезапуска панели у нас остаётся только pid
}

var noiseRe = regexp.MustCompile(`Permanently added`)

// Последние строки лога ssh без шума: это и есть текст ошибки для пользователя
func (m *Manager) logTail(id string) string {
	raw, _ := os.ReadFile(m.store.LogPath(id))
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r", ""), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !noiseRe.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, "\n")
}

// openPorts проверяет, какие из локальных портов сейчас слушаются.
// Сначала спрашиваем у системы (быстро и без побочных эффектов), а если система
// такого не умеет, подключаемся к портам сами.
func openPorts(ports []int) map[int]bool {
	out := make(map[int]bool, len(ports))
	if listening, ok := listeningPorts(); ok {
		for _, port := range ports {
			out[port] = listening[port]
		}
		return out
	}

	// Запасной способ. Минус: ssh на каждое такое соединение ходит на удалённый сервер
	res := make([]bool, len(ports))
	var wg sync.WaitGroup
	for i, port := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond)
			if err == nil {
				conn.Close()
				res[i] = true
			}
		}()
	}
	wg.Wait()
	for i, port := range ports {
		out[port] = res[i]
	}
	return out
}
