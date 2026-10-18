// SSH-туннели: локальная веб-панель в одном exe-файле.
// Интерфейс (папка public) вшит в программу, данные лежат рядом в папке tunnels-data.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

//go:embed public
var publicFS embed.FS

const defaultPort = 8099

func main() {
	// Этот же exe ssh вызывает, когда ему нужен пароль (см. manager.go, SSH_ASKPASS).
	// Отдельный скрипт-помощник не нужен, поэтому и пути с пробелами не ломаются.
	if os.Getenv("SSH_TUNNELS_ASKPASS") == "1" {
		fmt.Println(os.Getenv("SSH_PW"))
		return
	}
	initPlatform()

	dataDir, err := pickDataDir()
	if err != nil {
		fatal("Не удалось создать папку для данных: " + err.Error())
	}
	store := NewStore(dataDir)
	self, _ := os.Executable()
	mgr := NewManager(store, self)
	logPath = filepath.Join(dataDir, "panel.log")
	quit := func() {
		mgr.StopAll()
		os.Exit(0)
	}
	api := &API{store: store, mgr: mgr, quit: quit}

	// Туннели живут, пока работает панель: закрыли окно - всё выключилось.
	// Записи о процессах прошлого запуска больше не нужны.
	store.UpdateState(func(s map[string]StateEntry) {
		for id := range s {
			delete(s, id)
		}
	})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		<-ch
		quit()
	}()

	sub, _ := fs.Sub(publicFS, "public")
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", http.FileServer(http.FS(sub)))

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultPort))
	if err != nil {
		// Порт занят. Если там уже наша панель, просто открываем её, а не запускаем вторую
		if isOurPanel(defaultPort) {
			openBrowser(fmt.Sprintf("http://localhost:%d/", defaultPort))
			return
		}
		ln, err = net.Listen("tcp", "127.0.0.1:0") // любой свободный порт
		if err != nil {
			fatal("Не удалось запустить панель: " + err.Error())
		}
	}
	url := fmt.Sprintf("http://localhost:%d/", ln.Addr().(*net.TCPAddr).Port)

	logf("Панель запущена: %s", url)
	if p := mgr.SSHPath(); p != "" {
		logf("ssh: %s", p)
	}
	fmt.Println("SSH-туннели запущены:", url)
	fmt.Println("Если закрыть это окно, панель и все её туннели выключатся.")
	go openBrowser(url)

	go func() {
		if err := http.Serve(ln, hostGuard(mux)); err != nil {
			fatal(err.Error())
		}
	}()
	// В Windows здесь крутится значок в трее, в других системах просто ждём сигнала выхода
	runTray(url, quit)
}

// hostGuard пропускает только запросы к localhost. Панель умеет запускать
// программы, и без проверки чужой сайт мог бы обратиться к ней через DNS-rebinding.
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Данные рядом с exe, чтобы всё лежало в одной распакованной папке.
// Если туда писать нельзя (например, Program Files), берём папку профиля.
func pickDataDir() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "tunnels-data")
		if os.MkdirAll(filepath.Join(dir, "logs"), 0o700) == nil {
			return dir, nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "ssh-tunnels")
	return dir, os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
}

func isOurPanel(port int) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct{ App string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.App == "ssh-tunnels"
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// Ошибку при старте нужно успеть прочитать: в Windows у программы нет консоли, поэтому окошко
func fatal(msg string) {
	logf("ОШИБКА: %s", msg)
	showError(msg)
	os.Exit(1)
}

var logPath string

// logf пишет в консоль и в файл tunnels-data/panel.log. Файл нужен, потому что в Windows консоли нет,
// а при проблемах по нему проще всего понять, что произошло.
func logf(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
	fmt.Print(line)
	if logPath == "" {
		return
	}
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.WriteString(line)
		f.Close()
	}
}
