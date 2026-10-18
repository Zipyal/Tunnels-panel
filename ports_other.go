//go:build !linux && !windows

package main

// Для остальных систем (macOS) таблицы портов нет, используем запасной способ из manager.go
func listeningPorts() (map[int]bool, bool) { return nil, false }
