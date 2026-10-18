package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Установка встроенного в Windows клиента OpenSSH одной кнопкой.
//
// Add-WindowsCapability требует прав администратора, поэтому внешний PowerShell
// запускает внутренний через Start-Process -Verb RunAs: Windows сама покажет окно
// "Разрешить этому приложению вносить изменения?". Из повышенного процесса вывод
// не прочитать, поэтому он пишет результат в install.log, а мы читаем файл.

// psEncode готовит скрипт для -EncodedCommand (так не нужно думать об экранировании кавычек)
func psEncode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func installScripts(logPath string) (outer string) {
	quoted := strings.ReplaceAll(logPath, "'", "''")
	inner := fmt.Sprintf(`$log = '%s'
try {
  $r = Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0 -ErrorAction Stop
  "OK restart=$($r.RestartNeeded)" | Out-File -Encoding utf8 $log
} catch {
  "ERR $($_.Exception.Message)" | Out-File -Encoding utf8 $log
  exit 1
}`, quoted)

	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$p = Start-Process powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList '-NoProfile','-EncodedCommand','%s'
exit $p.ExitCode`, psEncode(inner))
}

// InstallSSH запускает установку в фоне. Возвращает текст ошибки, если запустить нельзя.
func (m *Manager) InstallSSH() string {
	if runtime.GOOS != "windows" {
		return "Автоустановка есть только в Windows. Поставьте openssh-client через пакетный менеджер системы."
	}
	m.mu.Lock()
	if m.installing {
		m.mu.Unlock()
		return ""
	}
	m.installing, m.installErr, m.installDone = true, "", ""
	m.mu.Unlock()

	logPath := filepath.Join(m.store.dir, "install.log")
	_ = os.Remove(logPath)
	logf("Установка ssh: жду подтверждения в окне Windows...")

	go func() {
		cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
			"-EncodedCommand", psEncode(installScripts(logPath)))
		detach(cmd) // без чёрного окна
		runErr := cmd.Run()
		path := findSSH()
		result := readInstallLog(logPath)

		var done, failure string
		switch {
		case path != "":
			done = sshVersion(path)
		case result == "" && runErr != nil:
			failure = "Установка отменена: в окне Windows нужно ответить «Да»."
		case strings.HasPrefix(result, "ERR"):
			failure = explainInstallError(result)
		case runErr != nil:
			failure = "Установка не завершилась (" + runErr.Error() + "). Подробности: " + logPath
		default:
			failure = "Установка завершилась, но ssh не найден. Перезапустите программу."
		}

		m.mu.Lock()
		m.installing = false
		m.installErr, m.installDone = failure, done
		if path != "" {
			m.sshPath = path
		}
		m.mu.Unlock()

		if failure != "" {
			logf("Установка ssh не удалась: %s", failure)
		} else {
			logf("ssh установлен: %s", done)
		}
	}()
	return ""
}

func readInstallLog(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Out-File -Encoding utf8 пишет BOM, убираем его
	return strings.TrimSpace(strings.TrimPrefix(string(raw), "\xef\xbb\xbf"))
}

// Понятный текст вместо кода ошибки Windows
func explainInstallError(logText string) string {
	text := strings.TrimSpace(strings.TrimPrefix(logText, "ERR"))
	lower := strings.ToLower(text)
	for _, code := range []string{"0x8024402c", "0x80072ee7", "0x800f0954", "0x800f0950", "0x80240438"} {
		if strings.Contains(lower, code) {
			return "Windows не смогла скачать ssh: нет интернета или доступ к Windows Update закрыт " +
				"(так бывает на рабочих компьютерах). Код: " + code
		}
	}
	return "Windows не смогла установить ssh: " + text
}

// sshVersion запускает "ssh -V", это заодно проверка, что файл действительно работает
func sshVersion(path string) string {
	out, err := exec.Command(path, "-V").CombinedOutput() // версию ssh печатает в stderr
	if err != nil {
		return "найден, но не запускается: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

// InstallStatus нужен интерфейсу, чтобы показывать ход и результат установки
func (m *Manager) InstallStatus() (installing bool, errText, done string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installing, m.installErr, m.installDone
}
