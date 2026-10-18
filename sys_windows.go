//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// ssh запускаем без окна и в отдельной группе процессов, чтобы он пережил закрытие панели
func detach(cmd *exec.Cmd) {
	const createNoWindow, createNewProcessGroup = 0x08000000, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | createNewProcessGroup}
}

func pidAlive(pid int) bool {
	const queryLimited, stillActive = 0x1000, 259
	h, err := syscall.OpenProcess(queryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

// Job-объект Windows: когда панель завершается (в том числе крестиком на окне),
// система сама убивает все процессы, которые в него добавлены.
var job syscall.Handle

// JOBOBJECT_EXTENDED_LIMIT_INFORMATION (раскладка полей как в WinAPI)
type jobLimitInfo struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	IoCounters              [6]uint64
	ProcessMemoryLimit      uintptr
	JobMemoryLimit          uintptr
	PeakProcessMemoryUsed   uintptr
	PeakJobMemoryUsed       uintptr
}

func initJob() {
	const killOnJobClose, extendedLimitInformation = 0x2000, 9
	k := syscall.NewLazyDLL("kernel32.dll")
	h, _, _ := k.NewProc("CreateJobObjectW").Call(0, 0)
	if h == 0 {
		return
	}
	info := jobLimitInfo{LimitFlags: killOnJobClose}
	r, _, _ := k.NewProc("SetInformationJobObject").Call(h, extendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if r != 0 {
		job = syscall.Handle(h)
	}
}

// bindToPanel добавляет запущенный ssh в job панели
func bindToPanel(cmd *exec.Cmd) {
	const setQuota, terminate = 0x0100, 0x0001
	if job == 0 || cmd.Process == nil {
		return
	}
	h, err := syscall.OpenProcess(setQuota|terminate, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	syscall.NewLazyDLL("kernel32.dll").NewProc("AssignProcessToJobObject").Call(uintptr(job), uintptr(h))
}

func initPlatform() { initJob() }

// ssh вызывает askpass без кавычек вокруг пути, а в пути могут быть пробелы
// (C:\Users\Иван Петров\...). Короткое имя 8.3 пробелов не содержит.
func shortPath(p string) string {
	in, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return syscall.UTF16ToString(buf[:n])
}
