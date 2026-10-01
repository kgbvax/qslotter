//go:build !windows

package desktop

// postQuit: only Windows has one quit flag per thread that a nested loop can
// swallow.
func postQuit() {}
