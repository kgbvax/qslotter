//go:build windows

package desktop

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A message-only window on the UI thread receives a private message and runs
// queued functions there. The tray's message loop (and glaze's nested loop)
// dispatch to it like to any window of the thread - no cgo needed.

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procGetModuleHandleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

const (
	wmUIDo      = 0x8000 + 0x51 // WM_APP + 0x51
	hwndMessage = ^uintptr(2)   // HWND_MESSAGE = (HWND)-3
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

var ui struct {
	hwnd  uintptr
	mu    sync.Mutex
	queue []func()
}

// initUIThread creates the dispatcher window; call on the UI thread.
func initUIThread() error {
	cls, err := windows.UTF16PtrFromString("qslotterUIDispatch")
	if err != nil {
		return err
	}
	hinst, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassExW{lpfnWndProc: syscall.NewCallback(uiWndProc), hInstance: hinst, lpszClassName: cls}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW: %v", e)
	}
	h, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, hinst, 0)
	if h == 0 {
		return fmt.Errorf("CreateWindowExW: %v", e)
	}
	ui.hwnd = h
	return nil
}

func uiWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmUIDo {
		ui.mu.Lock()
		q := ui.queue
		ui.queue = nil
		ui.mu.Unlock()
		for _, f := range q {
			f()
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// uiDo runs f on the UI thread; false when no dispatcher exists.
func uiDo(f func()) bool {
	if ui.hwnd == 0 {
		return false
	}
	ui.mu.Lock()
	ui.queue = append(ui.queue, f)
	ui.mu.Unlock()
	r, _, _ := procPostMessageW.Call(ui.hwnd, wmUIDo, 0, 0)
	return r != 0
}
