//go:build !darwin && !windows

package desktop

import "github.com/crgimenes/glaze"

// No portable way to read or place a GTK window through glaze: the window
// opens at its default size and position.

func windowBounds(glaze.WebView) (Rect, bool) { return Rect{}, false }

func placeWindow(glaze.WebView, Rect) bool { return false }

// resizeWindow sets the content size, keeping the window where it is.
func resizeWindow(w glaze.WebView, cw, ch int) { w.SetSize(cw, ch, glaze.HintNone) }
