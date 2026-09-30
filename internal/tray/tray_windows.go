//go:build windows

package tray

import (
	_ "embed"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconBytes []byte

func run(opts Options) {
	systray.Run(func() {
		systray.SetIcon(iconBytes)
		systray.SetTitle("qslotter")
		systray.SetTooltip("qslotter - QSL workbench")
		mQueue := systray.AddMenuItem("Queue (compact)", "Open the compact decision window")
		mLog := systray.AddMenuItem("Log", "Open the log page")
		mReceive := systray.AddMenuItem("Receive", "Open the received-card entry page")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Exit", "Shut qslotter down")
		if opts.OpenCompactOnStart {
			go openCompact(opts.BaseURL)
		}
		go func() {
			for {
				select {
				case <-mQueue.ClickedCh:
					go openCompact(opts.BaseURL)
				case <-mLog.ClickedCh:
					go openURL(opts.BaseURL + "/log")
				case <-mReceive.ClickedCh:
					go openURL(opts.BaseURL + "/receive")
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}, func() {
		if opts.OnExit != nil {
			opts.OnExit()
		}
	})
}
