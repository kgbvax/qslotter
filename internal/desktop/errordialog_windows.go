//go:build windows

package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procMessageBoxW = user32.NewProc("MessageBoxW")

// ShowError shows a modal error message (startup failures: a windowed app has
// no console to print to).
func ShowError(title, msg string) {
	t, err1 := windows.UTF16PtrFromString(title)
	m, err2 := windows.UTF16PtrFromString(msg)
	if err1 != nil || err2 != nil {
		return
	}
	const mbIconError = 0x10
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
}
