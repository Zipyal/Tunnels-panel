//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// Отдельная сессия: ssh не умирает вместе с закрытым терминалом
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func initPlatform() {}

func shortPath(p string) string { return p }

// В Linux/macOS туннели гасит обработчик сигналов в main.go
func bindToPanel(cmd *exec.Cmd) {}
