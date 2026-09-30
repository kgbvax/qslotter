// Package tray provides the optional system-tray shell. On Windows it hosts a
// notification-area icon whose menu opens the compact queue (a sized,
// chromeless browser window) and the log/receive pages, and shuts the server
// down. On other platforms Run is a no-op except for honoring
// OpenCompactOnStart, which opens the compact queue in the default browser.
//
// The Windows implementation uses fyne.io/systray, whose backend is pure Win32
// syscalls - CGO_ENABLED=0 cross-compilation is unaffected.
package tray

// Options configures the tray shell.
type Options struct {
	// BaseURL is the server root, e.g. "http://127.0.0.1:8473".
	BaseURL string
	// OpenCompactOnStart opens the compact queue window on startup.
	OpenCompactOnStart bool
	// OnExit is called after the user chooses Exit and the tray has shut
	// down. Wire it to the server shutdown path.
	OnExit func()
}

// Run starts the tray. On Windows it blocks until Exit is chosen (run it in a
// goroutine); elsewhere it returns immediately. OnExit is called on Windows
// after the tray loop returns.
func Run(opts Options) { run(opts) }
