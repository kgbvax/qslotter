package desktop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeWindow, "window": ModeWindow, "browser": ModeBrowser, "headless": ModeHeadless} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMode("tray"); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestAppArgsSizeOnlyOnFirstStart(t *testing.T) {
	first := appArgs("http://127.0.0.1:8473/queue", "/p", 480, 640, true)
	later := appArgs("http://127.0.0.1:8473/queue", "/p", 480, 640, false)
	for _, want := range []string{"--app=http://127.0.0.1:8473/queue", "--user-data-dir=/p", "--no-first-run"} {
		if !slices.Contains(first, want) || !slices.Contains(later, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !slices.Contains(first, "--window-size=480,640") {
		t.Error("first start must size the window")
	}
	for _, a := range later {
		if strings.HasPrefix(a, "--window-size") {
			t.Error("later starts must not override the size the user chose")
		}
	}
}

func TestTargets(t *testing.T) {
	s := New(Options{BaseURL: "http://127.0.0.1:8473", StartPath: "/settings"})
	if url, w, _, _ := s.target(); url != "http://127.0.0.1:8473/settings" || w != mainW {
		t.Errorf("full = %s %d", url, w)
	}
	s.rememberView(viewCompact)
	if url, w, h, title := s.target(); url != "http://127.0.0.1:8473/queue?compact=1&app=1" || w != compactW || h != compactH || !strings.Contains(title, "compact") {
		t.Errorf("compact = %s %dx%d %q", url, w, h, title)
	}
}

// The first-run start page (Settings) is for the first window only;
// reopening the window later shows the queue.
func TestStartPathOnlyForTheFirstWindow(t *testing.T) {
	s := New(Options{BaseURL: "http://127.0.0.1:8473", StartPath: "/settings"})
	if url, _, _, _ := s.target(); url != "http://127.0.0.1:8473/settings" {
		t.Fatalf("first window = %s", url)
	}
	s.started = true
	if url, _, _, _ := s.target(); url != "http://127.0.0.1:8473/queue" {
		t.Fatalf("reopened window = %s", url)
	}
}

// The view and the positions of both views survive a restart.
func TestStateRemembered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "window-state.json")
	s := New(Options{StatePath: path})
	if s.view != viewFull {
		t.Fatalf("default view = %q", s.view)
	}
	s.rememberView(viewCompact)
	s.st.setRect(viewFull, Rect{10, 20, 1200, 860})
	s.st.setRect(viewCompact, Rect{30, 40, 480, 640})
	saveState(path, s.st)

	n := New(Options{StatePath: path})
	if n.view != viewCompact {
		t.Errorf("view = %q, want compact", n.view)
	}
	if r := n.st.rect(viewFull); r == nil || *r != (Rect{10, 20, 1200, 860}) {
		t.Errorf("full rect = %v", r)
	}
	if r := n.st.rect(viewCompact); r == nil || *r != (Rect{30, 40, 480, 640}) {
		t.Errorf("compact rect = %v", r)
	}
}

func TestStateDamagedFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window-state.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := New(Options{StatePath: path}); s.view != viewFull || s.st.Full != nil {
		t.Errorf("damaged state not ignored: %+v", s.st)
	}
}

func TestPosixToTag(t *testing.T) {
	for in, want := range map[string]string{"de_DE.UTF-8": "de-DE", "en_US": "en-US", "de": "de", "sr_RS@latin": "sr-RS"} {
		if got := posixToTag(in); got != want {
			t.Errorf("posixToTag(%q) = %q, want %q", in, got, want)
		}
	}
}
