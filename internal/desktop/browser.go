package desktop

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/crgimenes/native/openurl"
)

// The browser fallback (and -ui browser): a Chromium-family browser opened as
// a chromeless app window on qslotter's own profile. The dedicated profile is
// what makes the window a window: Chromium merges invocations per profile, and
// an already-running browser silently ignores --app sizing flags otherwise.

// profileDir is the browser profile for qslotter's app windows. It lives under
// the user config dir, not the cache: a snap-packaged Chromium may not write to
// hidden cache dirs.
func profileDir() (dir string, fresh bool) {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir = filepath.Join(base, "qslotter", "browser-profile")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fresh = true
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir, fresh
}

// appArgs are the Chromium flags for an app window. The size is only passed
// on the first start of the profile: afterwards the browser remembers the size
// the user gave the window, and a fixed flag would override it every time.
func appArgs(url, profile string, w, h int, fresh bool) []string {
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--class=qslotter", // Linux: own taskbar/dock entry
	}
	if fresh {
		args = append(args, fmt.Sprintf("--window-size=%d,%d", w, h))
	}
	return args
}

// openAppWindow opens url as a browser app window, falling back to the
// default browser (plain tab) when no Chromium-family browser is found.
func openAppWindow(url string, w, h int) {
	profile, fresh := profileDir()
	if err := launchChromium(appArgs(url, profile, w, h, fresh)); err == nil {
		return
	} else {
		log.Printf("desktop: no Chromium-family browser for an app window (%v) - using the default browser", err)
	}
	if err := openurl.Open(url); err != nil {
		log.Printf("desktop: open browser: %v", err)
	}
}
