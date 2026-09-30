//go:build windows

package tray

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// AcquireSingleInstance creates a named mutex and reports whether another
// qslotter instance is already running. Prevents two copies from fighting
// over the UDP port and the SQLite file. The mutex lives for the process
// lifetime; the OS releases it on exit.
func AcquireSingleInstance() (alreadyRunning bool, err error) {
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(`Local\qslotter`))
	if err == windows.ERROR_ALREADY_EXISTS {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("create mutex: %w", err)
	}
	// h is intentionally never closed: keep the mutex until process exit.
	_ = h
	return false, nil
}
