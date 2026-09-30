//go:build !windows

package tray

import "log"

// run is a no-op without Windows: there is no tray. If OpenCompactOnStart is
// set, the compact queue still opens in the default browser.
func run(opts Options) {
	if opts.OpenCompactOnStart {
		go openURL(opts.BaseURL + "/queue?compact=1&app=1")
		return
	}
	log.Printf("tray: not supported on this platform (Windows only); server UI at %s", opts.BaseURL)
}
