//go:build !windows

package desktop

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// chromiumCandidates lists the Chromium-family browsers to try, per OS.
func chromiumCandidates() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	}
	return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}
}

// launchChromium starts the first Chromium-family browser found.
func launchChromium(args []string) error {
	for _, c := range chromiumCandidates() {
		path := c
		if runtime.GOOS != "darwin" {
			p, err := exec.LookPath(c)
			if err != nil {
				continue
			}
			path = p
		} else if _, err := os.Stat(c); err != nil {
			continue
		}
		cmd := exec.Command(path, args...)
		if err := cmd.Start(); err != nil {
			continue
		}
		go func() { _ = cmd.Wait() }() // reap
		return nil
	}
	return errors.New("none of Chrome, Edge, Chromium, Brave found")
}
