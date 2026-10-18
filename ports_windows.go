//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// listeningPorts спрашивает у Windows список слушающих TCP-портов (то же, что показывает netstat -an),
// без соединений с самими портами.
func listeningPorts() (map[int]bool, bool) {
	const (
		afInet               = 2
		tcpTableOwnerPidList = 3 // TCP_TABLE_OWNER_PID_LISTENER
		errInsufficient      = 122
	)
	proc := syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidList, 0)
	if r != errInsufficient || size == 0 {
		return nil, false
	}
	buf := make([]byte, size)
	r, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidList, 0)
	if r != 0 {
		return nil, false
	}

	// Структура: DWORD число записей, затем записи по 6 DWORD:
	// state, localAddr, localPort, remoteAddr, remotePort, pid. Порт в сетевом порядке байт.
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	ports := map[int]bool{}
	for i := uint32(0); i < count; i++ {
		off := 4 + i*24
		if int(off)+24 > len(buf) {
			break
		}
		raw := *(*uint32)(unsafe.Pointer(&buf[off+8]))
		ports[int(raw&0xff)<<8|int(raw>>8&0xff)] = true
	}
	return ports, true
}
