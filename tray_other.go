//go:build !windows

package main

import (
	"fmt"
	"os"
)

// В Linux и macOS значка в трее нет: программа работает в терминале, выход по Ctrl+C
func runTray(url string, onExit func()) { select {} }

func showError(msg string) { fmt.Fprintln(os.Stderr, "ОШИБКА:", msg) }
