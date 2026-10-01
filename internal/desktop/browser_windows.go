//go:build windows

package desktop

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// launchChromium starts Edge (preinstalled on Windows 10/11) or Chrome via
// ShellExecuteW, which resolves the exe through the App Paths registry key.
func launchChromium(args []string) error {
	quoted := make([]string, len(args))
	for i, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok && strings.ContainsAny(v, " \t") {
			a = k + `="` + v + `"` // profile paths can contain spaces
		}
		quoted[i] = a
	}
	params := strings.Join(quoted, " ")
	var lastErr error
	for _, exe := range []string{"msedge.exe", "chrome.exe"} {
		if lastErr = shellExecute(exe, params); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func shellExecute(file, params string) error {
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecuteW.Call(0, 0, uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(p)), 0, 1 /* SW_SHOWNORMAL */)
	if r <= 32 {
		return fmt.Errorf("ShellExecuteW %s failed (code %d)", file, r)
	}
	return nil
}
