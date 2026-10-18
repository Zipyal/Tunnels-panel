//go:build windows

package main

import (
	"errors"
	"math"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Значок в области уведомлений (трей). Только стандартная библиотека и системные вызовы Windows.
// Окно консоли у программы нет вообще (сборка с -H=windowsgui), управление идёт через значок:
// клик открывает панель, правая кнопка показывает меню.

func init() {
	// Окно и цикл сообщений должны жить в одном потоке, а это главный поток программы
	runtime.LockOSThread()
}

const (
	wmDestroy       = 0x0002
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmTray          = 0x0400 + 1 // своё сообщение: Windows присылает его при действиях со значком

	nimAdd, nimModify, nimDelete = 0, 1, 2
	nifMessage, nifIcon, nifTip  = 0x1, 0x2, 0x4
	nifInfo                      = 0x10
	niifInfo                     = 0x1

	mfString, mfSeparator        = 0x0, 0x800
	tpmRightButton, tpmReturnCmd = 0x2, 0x100
	menuOpen, menuExit           = 1, 2
	idiApplication               = 32512
	reopenDelay                  = 800 * time.Millisecond
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassEx  = user32.NewProc("RegisterClassExW")
	pCreateWindowEx   = user32.NewProc("CreateWindowExW")
	pDefWindowProc    = user32.NewProc("DefWindowProcW")
	pGetMessage       = user32.NewProc("GetMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessage  = user32.NewProc("DispatchMessageW")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")
	pPostMessage      = user32.NewProc("PostMessageW")
	pCreateIcon       = user32.NewProc("CreateIcon")
	pLoadIcon         = user32.NewProc("LoadIconW")
	pCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	pAppendMenu       = user32.NewProc("AppendMenuW")
	pTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
	pGetCursorPos     = user32.NewProc("GetCursorPos")
	pSetForegroundWin = user32.NewProc("SetForegroundWindow")
	pRegisterWinMsg   = user32.NewProc("RegisterWindowMessageW")
	pMessageBox       = user32.NewProc("MessageBoxW")
	pShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle  = kernel32.NewProc("GetModuleHandleW")
)

// Структуры повторяют раскладку WinAPI
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        [4]uint32
	BalloonIcon     uintptr
}

type tray struct {
	url        string
	onExit     func()
	hwnd       uintptr
	icon       uintptr
	taskbarMsg uintptr // сообщение "проводник перезапустился", значок нужно добавить заново
	lastOpen   time.Time
}

// runTray создаёт значок и крутит цикл сообщений. Возвращается только при ошибке.
func runTray(url string, onExit func()) {
	t := &tray{url: url, onExit: onExit}
	if err := t.create(); err != nil {
		logf("Не удалось создать значок в трее: %v", err)
		select {} // без значка продолжаем работать, выйти можно кнопкой на странице
	}
	t.loop()
}

func (t *tray) create() error {
	inst, _, _ := pGetModuleHandle.Call(0)
	className := u16("SSHTunnelsTray")
	wc := wndClassEx{WndProc: syscall.NewCallback(t.proc), Instance: inst, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	// Обычное окно, которое никогда не показываем: меню значка требует настоящего окна-владельца
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(u16("SSH-туннели"))),
		0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	t.hwnd = hwnd
	t.taskbarMsg, _, _ = pRegisterWinMsg.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))

	t.icon = makeIcon()
	if t.icon == 0 {
		t.icon, _, _ = pLoadIcon.Call(0, idiApplication) // стандартный значок, если свой не получился
	}
	if !t.add() {
		return errors.New("Shell_NotifyIcon вернул ошибку")
	}
	t.balloon("Панель работает в трее. Нажмите на значок, чтобы открыть её.")
	return nil
}

func (t *tray) data() notifyIconData {
	nid := notifyIconData{Wnd: t.hwnd, ID: 1, Icon: t.icon}
	nid.Size = uint32(unsafe.Sizeof(nid))
	return nid
}

func (t *tray) add() bool {
	nid := t.data()
	nid.Flags = nifMessage | nifIcon | nifTip
	nid.CallbackMessage = wmTray
	fill(nid.Tip[:], "SSH-туннели: нажмите, чтобы открыть панель")
	r, _, _ := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	return r != 0
}

func (t *tray) balloon(text string) {
	nid := t.data()
	nid.Flags = nifInfo
	nid.InfoFlags = niifInfo
	fill(nid.Info[:], text)
	fill(nid.InfoTitle[:], "SSH-туннели")
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (t *tray) remove() {
	nid := t.data()
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (t *tray) loop() {
	var m winMsg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// proc обрабатывает сообщения окна. Здесь только короткие действия.
func (t *tray) proc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if msg == t.taskbarMsg && t.taskbarMsg != 0 {
		t.add() // проводник перезапустился, значок пропал
		return 0
	}
	switch msg {
	case wmTray:
		switch uint32(lParam) {
		case wmLButtonUp, wmLButtonDblClk:
			t.open()
		case wmRButtonUp:
			t.menu()
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

// При двойном клике приходят два сообщения "кнопка отпущена", не открываем вкладку дважды
func (t *tray) open() {
	if time.Since(t.lastOpen) < reopenDelay {
		return
	}
	t.lastOpen = time.Now()
	openBrowser(t.url)
}

func (t *tray) menu() {
	h, _, _ := pCreatePopupMenu.Call()
	if h == 0 {
		return
	}
	defer pDestroyMenu.Call(h)
	pAppendMenu.Call(h, mfString, menuOpen, uintptr(unsafe.Pointer(u16("Открыть панель"))))
	pAppendMenu.Call(h, mfSeparator, 0, 0)
	pAppendMenu.Call(h, mfString, menuExit, uintptr(unsafe.Pointer(u16("Выход (выключит все туннели)"))))

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWin.Call(t.hwnd) // без этого меню не закрывается при клике мимо
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	pPostMessage.Call(t.hwnd, 0, 0, 0)

	switch cmd {
	case menuOpen:
		t.open()
	case menuExit:
		t.remove()
		t.onExit()
	}
}

// makeIcon рисует значок в коде: синий круг с белой полосой (туннель). Отдельный файл .ico не нужен.
func makeIcon() uintptr {
	const size = 32
	xor := make([]byte, size*size*4)
	and := make([]byte, size*size/8) // маска нулевая: прозрачность берётся из альфа-канала
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-15.5, float64(y)-15.5)
			alpha := math.Max(0, math.Min(1, 15.5-d)) // сглаженный край круга
			i := (y*size + x) * 4
			b, g, r := byte(0xff), byte(0x8d), byte(0x4c) // цвет интерфейса #4c8dff, порядок BGRA
			if y >= 13 && y <= 18 && x >= 6 && x <= 25 {
				b, g, r = 0xff, 0xff, 0xff
			}
			xor[i], xor[i+1], xor[i+2], xor[i+3] = b, g, r, byte(alpha*255)
		}
	}
	h, _, _ := pCreateIcon.Call(0, size, size, 1, 32, uintptr(unsafe.Pointer(&and[0])), uintptr(unsafe.Pointer(&xor[0])))
	return h
}

func u16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// fill копирует строку в массив фиксированной длины (с завершающим нулём)
func fill(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	n := copy(dst, u)
	if n == len(dst) {
		dst[n-1] = 0
	}
}

// showError: у программы нет консоли, поэтому ошибку при старте показываем окошком
func showError(msg string) {
	pMessageBox.Call(0, uintptr(unsafe.Pointer(u16(msg))), uintptr(unsafe.Pointer(u16("SSH-туннели"))), 0x10)
}
