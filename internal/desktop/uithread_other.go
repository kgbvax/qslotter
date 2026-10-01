//go:build !windows && !darwin

package desktop

// No dispatcher outside macOS/Windows: Show reaches the UI thread through an
// open window instead (on Linux the program lives exactly as long as a window).

func initUIThread() error { return nil }

func uiDo(func()) bool { return false }
