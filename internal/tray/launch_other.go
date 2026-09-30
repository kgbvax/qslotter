//go:build !windows

package tray

import (
	"os/exec"
	"runtime"
)

// openURL opens a URL in the default browser. Non-Windows platforms have no
// reliable window-geometry flags, so there is no compact variant here.
func openURL(url string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", url)
	} else {
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func openCompact(baseURL string) { openURL(baseURL + "/queue?compact=1&app=1") }
