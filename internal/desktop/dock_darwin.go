//go:build darwin

package desktop

import (
	"log"

	"github.com/ebitengine/purego/objc"
)

// native/tray makes the process a menu-bar-only app (no Dock icon). In window
// mode qslotter wants to be a regular app: its own Dock icon (it must not get
// lost among other windows), a Dock click that reopens the window, and Quit
// from the Dock / Cmd-Q going through the normal shutdown. Neither glaze nor
// the tray installs an app delegate when the tray owns the run loop, so this
// one is free to.

var dockDelegate objc.ID // kept alive for the process lifetime

// setupDock runs on the UI thread once the tray's run loop is up.
func (s *Shell) setupDock() {
	sel := objc.RegisterName
	cls, err := objc.RegisterClass("QslotterAppDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{
			Cmd: sel("applicationShouldHandleReopen:hasVisibleWindows:"),
			Fn: func(self objc.ID, _cmd objc.SEL, app objc.ID, hasVisible bool) bool {
				s.open("main") // Dock click: bring back the window
				return true
			},
		},
		{
			Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"),
			Fn:  func(self objc.ID, _cmd objc.SEL, app objc.ID) bool { return false },
		},
		{
			// Quit from the Dock menu or Cmd-Q: cancel AppKit's hard exit and
			// run qslotter's own shutdown instead (HTTP, sync loop, DB).
			Cmd: sel("applicationShouldTerminate:"),
			Fn: func(self objc.ID, _cmd objc.SEL, app objc.ID) uint {
				go s.Quit()
				return 0 // NSTerminateCancel
			},
		},
	})
	if err != nil {
		log.Printf("desktop: dock delegate: %v", err)
		return
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	dockDelegate = objc.ID(cls).Send(sel("new"))
	app.Send(sel("setDelegate:"), dockDelegate)
	app.Send(sel("setActivationPolicy:"), 0) // NSApplicationActivationPolicyRegular
	app.Send(sel("activateIgnoringOtherApps:"), true)
}
