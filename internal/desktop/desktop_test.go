package desktop

import (
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
	if url, w, _, _ := s.target("main"); url != "http://127.0.0.1:8473/settings" || w != mainW {
		t.Errorf("main = %s %d", url, w)
	}
	if url, w, h, title := s.target("compact"); url != "http://127.0.0.1:8473/queue?compact=1&app=1" || w != compactW || h != compactH || !strings.Contains(title, "compact") {
		t.Errorf("compact = %s %dx%d %q", url, w, h, title)
	}
}
