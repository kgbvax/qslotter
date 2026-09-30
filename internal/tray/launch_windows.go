//go:build windows

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

// openCompact opens the compact decision queue as a sized, chromeless
// Chromium app window. Edge is preinstalled on Windows 10/11 and ShellExecuteW
// resolves it via the App Paths registry key - no path hunting needed.
//
// The dedicated --user-data-dir is mandatory: Chromium merges invocations per
// profile, and an already-running Edge/Chrome silently ignores
// --window-size/--window-position when launched into an existing profile.
func openCompact(baseURL string) {
	url := baseURL + "/queue?compact=1&app=1"
	// The user-data-dir is quoted: profile paths under %LOCALAPPDATA% can
	// contain spaces (C:\Users\Foo Bar\...), which would split the argument.
	args := fmt.Sprintf(`--app=%s --window-size=480,640 --no-first-run --no-default-browser-check --user-data-dir="%s"`,
		url, userDataDir())
	for _, exe := range []string{"msedge.exe", "chrome.exe"} {
		if err := shellExecute("", exe, args); err == nil {
			return
		}
	}
	openURL(url) // fall back to the default browser without geometry control
}

// openURL opens a URL in the default browser (no window-geometry control).
func openURL(url string) {
	_ = shellExecute("open", url, "")
}

// shellExecute is a thin ShellExecuteW wrapper. An empty verb means "open"
// default; for executables resolved via App Paths (msedge.exe/chrome.exe) the
// file is the exe name and args the parameter string.
func shellExecute(verb, file, args string) error {
	v, err := utf16Ptr(verb)
	if err != nil {
		return err
	}
	f, err := utf16Ptr(file)
	if err != nil {
		return err
	}
	p, err := utf16Ptr(args)
	if err != nil {
		return err
	}
	r, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(f)),
		uintptr(unsafe.Pointer(p)),
		0,
		1, // SW_SHOWNORMAL
	)
	if r <= 32 {
		if callErr != nil && callErr.Error() != "The operation completed successfully." {
			return callErr
		}
		return fmt.Errorf("ShellExecuteW %s failed (code %d)", file, r)
	}
	return nil
}

func utf16Ptr(s string) (*uint16, error) {
	if s == "" {
		return nil, nil // ShellExecuteW accepts NULL for empty verb
	}
	return windows.UTF16PtrFromString(s)
}

// userDataDir returns the dedicated browser profile directory for qslotter's
// app windows, under %LOCALAPPDATA%\qslotter.
func userDataDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "qslotter", "edge-app-profile")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
