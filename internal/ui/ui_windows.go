//go:build windows

package ui

// Package ui is how openmini presents itself on Windows: a small window of
// its own with the log, a tray icon, the detached relaunch that frees the
// console, and the console tweak for the wizard. Everywhere else it is a set
// of no-ops and the server stays in the terminal.
//
// On Windows the server runs in a small window of its own: the log scrolls in
// it, the minimise button hides it to the notification area (a tray icon
// brings it back), and the close button stops openmini. The console the exe
// was started from is released once the window is up, so this works the same
// whether the classic console or Windows Terminal opened it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassEx  = user32.NewProc("RegisterClassExW")
	pCreateWindowEx   = user32.NewProc("CreateWindowExW")
	pDefWindowProc    = user32.NewProc("DefWindowProcW")
	pShowWindow       = user32.NewProc("ShowWindow")
	pGetMessage       = user32.NewProc("GetMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessage  = user32.NewProc("DispatchMessageW")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")
	pPostMessage      = user32.NewProc("PostMessageW")
	pSendMessage      = user32.NewProc("SendMessageW")
	pDestroyWindow    = user32.NewProc("DestroyWindow")
	pMoveWindow       = user32.NewProc("MoveWindow")
	pGetClientRect    = user32.NewProc("GetClientRect")
	pSetWindowText    = user32.NewProc("SetWindowTextW")
	pLoadCursor       = user32.NewProc("LoadCursorW")
	pLoadIcon         = user32.NewProc("LoadIconW")
	pCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	pAppendMenu       = user32.NewProc("AppendMenuW")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
	pTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	pGetCursorPos     = user32.NewProc("GetCursorPos")
	pSetForeground    = user32.NewProc("SetForegroundWindow")
	pGetModuleHandle  = kernel32.NewProc("GetModuleHandleW")
	pFreeConsole      = kernel32.NewProc("FreeConsole")
	pShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
	pExtractIcon      = shell32.NewProc("ExtractIconW")
	pGetStockObject   = gdi32.NewProc("GetStockObject")
)

const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmSetFont       = 0x0030
	wmSysCommand    = 0x0112
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000
	wmAppLog        = wmApp + 1 // new log lines are waiting
	wmAppTray       = wmApp + 2 // notification from the tray icon
	scMinimize      = 0xF020

	swHide    = 0
	swShow    = 5
	swRestore = 9

	nimAdd     = 0
	nimDelete  = 2
	nifMessage = 1
	nifIcon    = 2
	nifTip     = 4

	menuOpen = 1
	menuShow = 2
	menuQuit = 3

	maxLines = 400
)

type wndClassEx struct {
	size, style                        uint32
	wndProc                            uintptr
	clsExtra, wndExtra                 int32
	instance, icon, cursor, background uintptr
	menuName, className                *uint16
	iconSm                             uintptr
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type rect struct{ left, top, right, bottom int32 }

type notifyIconData struct {
	size            uint32
	hwnd            uintptr
	id, flags       uint32
	callbackMessage uint32
	icon            uintptr
	tip             [128]uint16
	state, stateMsk uint32
	info            [256]uint16
	version         uint32
	infoTitle       [64]uint16
	infoFlags       uint32
	guid            [16]byte
	balloonIcon     uintptr
}

type appWindow struct {
	hwnd, edit uintptr
	icon       uintptr
	url        string
	quit       func()
	lines      chan string
	buf        []string
}

var theWindow *appWindow
var wndProcCB = syscall.NewCallback(wndProc)

// Run opens openmini's own window on Windows and mirrors the log into
// it. quit is called when the user closes the window or picks Quit in the
// tray menu. It returns false when the window could not be created, in which
// case the caller keeps the console.
func Run(title, url string, quit func(), sink func(func(string)), logf func(string, ...any)) bool {
	ready := make(chan bool, 1)
	go func() {
		runtime.LockOSThread()
		defer func() {
			if r := recover(); r != nil {
				logf("window: panic: %v", r)
			}
			logf("window: message loop ended")
		}()
		w, ok := newAppWindow(title, url, quit, logf)
		ready <- ok
		if !ok {
			return
		}
		theWindow = w
		sink(w.append)
		w.loop(logf)
	}()
	return <-ready
}

func utf16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func newAppWindow(title, url string, quit func(), logf func(string, ...any)) (*appWindow, bool) {
	inst, _, _ := pGetModuleHandle.Call(0)
	cursor, _, _ := pLoadCursor.Call(0, 32512) // IDC_ARROW
	var icon uintptr
	if exe, err := os.Executable(); err == nil {
		icon, _, _ = pExtractIcon.Call(inst, uintptr(unsafe.Pointer(utf16(exe))), 0)
	}
	if icon == 0 || icon == 1 {
		icon, _, _ = pLoadIcon.Call(0, 32512) // IDI_APPLICATION
	}
	cls := wndClassEx{wndProc: wndProcCB, instance: inst, icon: icon, cursor: cursor, background: 6 /* COLOR_WINDOW+1 */, className: utf16("openmini-window"), iconSm: icon}
	cls.size = uint32(unsafe.Sizeof(cls))
	if r, _, e := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&cls))); r == 0 {
		logf("window: RegisterClassEx failed: %v", e)
		return nil, false
	}
	w := &appWindow{icon: icon, url: url, quit: quit, lines: make(chan string, 2000)}
	theWindow = w
	const wsOverlappedWindow = 0x00CF0000
	const cwUseDefault = 0x80000000
	hwnd, _, _ := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(utf16("openmini-window"))), uintptr(unsafe.Pointer(utf16(title))),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, 820, 440, 0, 0, inst, 0)
	if hwnd == 0 {
		logf("window: CreateWindowEx failed")
		return nil, false
	}
	w.hwnd = hwnd
	logf("window: created hwnd=%#x", hwnd)
	const editStyle = 0x40000000 | 0x10000000 | 0x00200000 | 0x0004 | 0x0800 | 0x0040 // child, visible, vscroll, multiline, readonly, autovscroll
	w.edit, _, _ = pCreateWindowEx.Call(0x200, uintptr(unsafe.Pointer(utf16("EDIT"))), 0, editStyle, 0, 0, 0, 0, hwnd, 0, inst, 0)
	font, _, _ := pGetStockObject.Call(17) // DEFAULT_GUI_FONT
	pSendMessage.Call(w.edit, wmSetFont, font, 1)
	pSendMessage.Call(w.edit, 0xC5 /* EM_SETLIMITTEXT */, 4<<20, 0)
	w.resize()
	w.trayIcon(nimAdd)
	pShowWindow.Call(hwnd, swShow)
	return w, true
}

func (w *appWindow) trayIcon(op uintptr) {
	nid := notifyIconData{hwnd: w.hwnd, id: 1, flags: nifMessage | nifIcon | nifTip, callbackMessage: wmAppTray, icon: w.icon}
	nid.size = uint32(unsafe.Sizeof(nid))
	copy(nid.tip[:], syscall.StringToUTF16("openmini"))
	pShellNotifyIcon.Call(op, uintptr(unsafe.Pointer(&nid)))
}

func (w *appWindow) resize() {
	var r rect
	pGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
	pMoveWindow.Call(w.edit, 0, 0, uintptr(r.right), uintptr(r.bottom), 1)
}

// append queues one log line for the window; safe from any goroutine.
func (w *appWindow) append(line string) {
	select {
	case w.lines <- line:
	default:
	}
	pPostMessage.Call(w.hwnd, wmAppLog, 0, 0)
}

func (w *appWindow) flushLines() {
	changed := false
	for {
		select {
		case l := <-w.lines:
			w.buf = append(w.buf, l)
			changed = true
			continue
		default:
		}
		break
	}
	if !changed {
		return
	}
	if len(w.buf) > maxLines {
		w.buf = w.buf[len(w.buf)-maxLines:]
	}
	text := strings.Join(w.buf, "\r\n") + "\r\n"
	pSetWindowText.Call(w.edit, uintptr(unsafe.Pointer(utf16(text))))
	n := uintptr(len(text))
	pSendMessage.Call(w.edit, 0xB1 /* EM_SETSEL */, n, n)
	pSendMessage.Call(w.edit, 0xB7 /* EM_SCROLLCARET */, 0, 0)
}

func (w *appWindow) show() {
	pShowWindow.Call(w.hwnd, swShow)
	pShowWindow.Call(w.hwnd, swRestore)
	pSetForeground.Call(w.hwnd)
}

func (w *appWindow) menu() {
	m, _, _ := pCreatePopupMenu.Call()
	pAppendMenu.Call(m, 0, menuOpen, uintptr(unsafe.Pointer(utf16("Open dashboard"))))
	pAppendMenu.Call(m, 0, menuShow, uintptr(unsafe.Pointer(utf16("Show window"))))
	pAppendMenu.Call(m, 0x800 /* MF_SEPARATOR */, 0, 0)
	pAppendMenu.Call(m, 0, menuQuit, uintptr(unsafe.Pointer(utf16("Quit openmini"))))
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForeground.Call(w.hwnd) // so the menu closes when the user clicks elsewhere
	cmd, _, _ := pTrackPopupMenu.Call(m, 0x100|0x2 /* TPM_RETURNCMD|TPM_RIGHTBUTTON */, uintptr(pt.x), uintptr(pt.y), 0, w.hwnd, 0)
	pDestroyMenu.Call(m)
	switch cmd {
	case menuOpen:
		windows.ShellExecute(0, utf16("open"), utf16(w.url), nil, nil, swShow)
	case menuShow:
		w.show()
	case menuQuit:
		w.quit()
	}
}

func wndProc(hwnd uintptr, m uint32, wp, lp uintptr) uintptr {
	w := theWindow
	switch m {
	case wmSize:
		if w != nil {
			w.resize()
		}
		return 0
	case wmAppLog:
		if w != nil {
			w.flushLines()
		}
		return 0
	case wmSysCommand:
		if wp&0xFFF0 == scMinimize { // minimise hides to the tray instead
			pShowWindow.Call(hwnd, swHide)
			return 0
		}
	case wmAppTray:
		switch lp {
		case wmLButtonUp, wmLButtonDblClk:
			if w != nil {
				w.show()
			}
		case wmRButtonUp:
			if w != nil {
				w.menu()
			}
		}
		return 0
	case wmClose: // the close button stops openmini, like Ctrl+C in a console
		if w != nil {
			w.quit()
		}
		return 0
	case wmDestroy:
		if w != nil {
			w.trayIcon(nimDelete)
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, uintptr(m), wp, lp)
	return r
}

func (w *appWindow) loop(logf func(string, ...any)) {
	var m msg
	for {
		r, _, e := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == 0 || int32(r) == -1 {
			logf("window: GetMessage returned %d (%v)", int32(r), e)
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// SpawnDetached starts this exe again with the same arguments but no console
// at all, so the child's own window is the only thing on screen; the caller
// exits right after. Its stderr goes to a file in the log directory, so a
// crash still leaves a trace.
func SpawnDetached(logDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	errFile, _ := os.Create(filepath.Join(logDir, "window-stderr.txt"))
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "OPENMINI_WINDOW_CHILD=1")
	cmd.Stderr = errFile // stdout is not kept: the log file already has every line
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 /* DETACHED_PROCESS */}
	return cmd.Start()
}

// Close removes the tray icon on the way out.
func Close() {
	if theWindow != nil {
		theWindow.trayIcon(nimDelete)
		pDestroyWindow.Call(theWindow.hwnd)
	}
}
