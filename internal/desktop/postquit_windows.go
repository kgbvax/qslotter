//go:build windows

package desktop

var procPostQuitMessage = user32.NewProc("PostQuitMessage")

// postQuit posts WM_QUIT to the UI thread's queue (call on the UI thread).
func postQuit() { procPostQuitMessage.Call(0) }
