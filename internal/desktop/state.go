package desktop

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// The two views of the one app window.
const (
	viewFull    = "full"
	viewCompact = "compact"
)

// Rect is a window frame in the platform's own units (points on macOS, pixels
// on Windows); only the platform code that produced it reads it back.
type Rect struct {
	X, Y, W, H int
}

// valid reports whether the frame is a plausible window (not minimized, not
// collapsed).
func (r Rect) valid() bool { return r.W >= 200 && r.H >= 150 }

// state is what the shell remembers between runs: which view the window was
// in, and where each view's window sat.
type state struct {
	View    string `json:"view"`
	Full    *Rect  `json:"full,omitempty"`
	Compact *Rect  `json:"compact,omitempty"`
}

func (st *state) rect(view string) *Rect {
	if view == viewCompact {
		return st.Compact
	}
	return st.Full
}

func (st *state) setRect(view string, r Rect) {
	if view == viewCompact {
		st.Compact = &r
	} else {
		st.Full = &r
	}
}

// loadState reads the state file; a missing or damaged file is an empty state.
func loadState(path string) state {
	var st state
	if path == "" {
		return st
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(b, &st); err != nil {
		log.Printf("desktop: window state %s unreadable (%v) - starting fresh", path, err)
		return state{}
	}
	if st.View != viewCompact {
		st.View = viewFull
	}
	return st
}

// saveState writes the state file atomically.
func saveState(path string, st state) {
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if err = os.WriteFile(tmp, b, 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			log.Printf("desktop: saving window state: %v", err)
		}
	}
}
