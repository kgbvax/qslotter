//go:build darwin

package desktop

import (
	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego/objc"
)

// Window frames through NSWindow (purego objc, no cgo). Cocoa's origin is the
// bottom left of the main screen; frames are stored as Cocoa gives them. All
// calls on the UI thread.

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

func nsWindow(w glaze.WebView) objc.ID { return objc.ID(uintptr(w.Window())) }

func windowBounds(w glaze.WebView) (Rect, bool) {
	win := nsWindow(w)
	if win == 0 {
		return Rect{}, false
	}
	if objc.Send[bool](win, objc.RegisterName("isMiniaturized")) {
		return Rect{}, false
	}
	f := objc.Send[cgRect](win, objc.RegisterName("frame"))
	return Rect{int(f.Origin.X), int(f.Origin.Y), int(f.Size.W), int(f.Size.H)}, true
}

// placeWindow moves and sizes the window to r, unless r would leave the
// window out of reach (a monitor that is gone).
func placeWindow(w glaze.WebView, r Rect) bool {
	win := nsWindow(w)
	if win == 0 || !r.valid() || !onScreen(r) {
		return false
	}
	f := cgRect{cgPoint{float64(r.X), float64(r.Y)}, cgSize{float64(r.W), float64(r.H)}}
	win.Send(objc.RegisterName("setFrame:display:"), f, true)
	return true
}

// resizeWindow sets the content size and keeps the window's top edge where it
// was (Cocoa grows a window upwards from its bottom edge).
func resizeWindow(w glaze.WebView, cw, ch int) {
	win := nsWindow(w)
	if win == 0 {
		w.SetSize(cw, ch, glaze.HintNone)
		return
	}
	old := objc.Send[cgRect](win, objc.RegisterName("frame"))
	w.SetSize(cw, ch, glaze.HintNone)
	f := objc.Send[cgRect](win, objc.RegisterName("frame"))
	f.Origin.Y = old.Origin.Y + old.Size.H - f.Size.H
	win.Send(objc.RegisterName("setFrameOrigin:"), f.Origin)
}

// onScreen: the window's top-left area overlaps some screen's visible area.
func onScreen(r Rect) bool {
	sel := objc.RegisterName
	screens := objc.ID(objc.GetClass("NSScreen")).Send(sel("screens"))
	n := int(objc.Send[uint](screens, sel("count")))
	top := float64(r.Y + r.H)
	for i := 0; i < n; i++ {
		v := objc.Send[cgRect](objc.Send[objc.ID](screens, sel("objectAtIndex:"), uint(i)), sel("visibleFrame"))
		// a 120x30 patch of the title bar, at the left end
		x0, x1 := float64(r.X), float64(r.X)+120
		y0, y1 := top-30, top
		if x0 < v.Origin.X+v.Size.W && x1 > v.Origin.X && y0 < v.Origin.Y+v.Size.H && y1 > v.Origin.Y {
			return true
		}
	}
	return false
}
