//go:build windows

package desktop

import (
	"unsafe"

	"github.com/crgimenes/glaze"
)

// Window frames through user32. Pixels, screen coordinates (negative on a
// monitor left of / above the primary). All calls on the UI thread.

var (
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procSetWindowPos    = user32.NewProc("SetWindowPos")
	procIsIconic        = user32.NewProc("IsIconic")
	procMonitorFromRect = user32.NewProc("MonitorFromRect")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

const (
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

func windowBounds(w glaze.WebView) (Rect, bool) {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return Rect{}, false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		return Rect{}, false
	}
	var r winRect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return Rect{}, false
	}
	return Rect{int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top)}, true
}

// placeWindow moves and sizes the window to r, unless r lies on no monitor
// (one that is gone).
func placeWindow(w glaze.WebView, r Rect) bool {
	hwnd := uintptr(w.Window())
	if hwnd == 0 || !r.valid() {
		return false
	}
	// the title bar's left end must be on a monitor
	probe := winRect{int32(r.X), int32(r.Y), int32(r.X + 120), int32(r.Y + 30)}
	if mon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&probe)), 0); mon == 0 {
		return false
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H), swpNoZOrder|swpNoActivate)
	return true
}

// resizeWindow sets the content size; the top-left corner stays.
func resizeWindow(w glaze.WebView, cw, ch int) { w.SetSize(cw, ch, glaze.HintNone) }
