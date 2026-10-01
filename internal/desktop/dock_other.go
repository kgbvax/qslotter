//go:build !darwin

package desktop

// setupDock: only macOS has a Dock to join (Windows windows get a taskbar
// button on their own).
func (s *Shell) setupDock() {}
