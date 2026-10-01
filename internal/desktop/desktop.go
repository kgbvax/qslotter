// Package desktop gives qslotter its own application window: a native
// WebView (glaze: WKWebView / WebView2 / WebKitGTK through purego, no cgo)
// showing the embedded web UI from the loopback server, with a tray on macOS
// and Windows. It is the only package that knows glaze and native; the web UI
// is loaded by URL only (never through a framework asset handler, which would
// buffer the SSE stream), so the backend can be swapped without touching it.
//
// Threading: Run must be called on the main goroutine, locked to the main OS
// thread (runtime.LockOSThread in main). Other goroutines reach the UI only
// through Show and Quit.
package desktop

import (
	"errors"
	"log"
	"runtime"
	"sync"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/native/tray"
)

// Mode selects the user interface.
type Mode string

const (
	ModeWindow   Mode = "window"   // own app window(s) + tray (default)
	ModeBrowser  Mode = "browser"  // tray + browser app windows (no WebView)
	ModeHeadless Mode = "headless" // server only, no UI at all
)

// ParseMode maps a flag/config value to a Mode; "" means window.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", ModeWindow:
		return ModeWindow, nil
	case ModeBrowser, ModeHeadless:
		return Mode(s), nil
	}
	return "", errors.New(`ui mode must be "window", "browser" or "headless"`)
}

// Options configures the desktop shell.
type Options struct {
	Mode        Mode
	BaseURL     string // e.g. http://127.0.0.1:8473 (always the loopback URL)
	StartPath   string // first page of the main window, e.g. "/queue" or "/settings"
	Title       string
	OpenCompact bool // also open the compact decision window at start
}

const (
	mainW, mainH       = 1200, 860
	compactW, compactH = 480, 640
	compactPath        = "/queue?compact=1&app=1"
)

// Shell is the running desktop UI.
type Shell struct {
	opts Options

	mu      sync.Mutex
	windows map[string]glaze.WebView // open windows by role ("main", "compact")
	hasTray bool
	pumping bool // a window event loop of ours is running on the UI thread
	quit    chan struct{}
	quitOne sync.Once
}

// New prepares the shell; nothing is shown until Run.
func New(opts Options) *Shell {
	if opts.Title == "" {
		opts.Title = "qslotter"
	}
	if opts.StartPath == "" {
		opts.StartPath = "/queue"
	}
	if opts.Mode == "" {
		opts.Mode = ModeWindow
	}
	return &Shell{opts: opts, windows: map[string]glaze.WebView{}, quit: make(chan struct{})}
}

// Run shows the UI and blocks until Quit (tray "Quit", a signal, or - where
// there is no tray - closing the last window). Must run on the locked main
// goroutine.
func (s *Shell) Run() error {
	if s.opts.Mode == ModeHeadless {
		<-s.quit
		return nil
	}
	if err := initUIThread(); err != nil {
		log.Printf("desktop: ui dispatcher: %v (a second launch cannot bring the window forward)", err)
	}
	err := tray.Run(tray.Config{
		Title:   "qslotter",
		Tooltip: "qslotter - QSL workbench",
		Icon:    trayIcon(),
		Items: []tray.Item{
			{Title: "Open qslotter", OnClick: func() { s.open("main") }},
			{Title: "Compact queue", OnClick: func() { s.open("compact") }},
			{Separator: true},
			{Title: "Quit qslotter", OnClick: s.Quit},
		},
		OnReady: func() {
			s.mu.Lock()
			s.hasTray = true
			s.mu.Unlock()
			s.start()
		},
	})
	if errors.Is(err, tray.ErrUnsupported) {
		// No tray backend (Linux): the windows are the app; closing the last
		// one ends glaze's loop and with it the program.
		s.start()
		if s.opts.Mode == ModeBrowser {
			<-s.quit // browser windows are separate processes; wait for a signal
		}
		return nil
	}
	return err
}

// start opens the initial windows and keeps them serviced.
func (s *Shell) start() {
	first := s.create("main")
	if s.opts.OpenCompact {
		s.create("compact")
	}
	s.loop(first)
}

// open shows the window for role - or brings an already open one to the
// front - and makes sure an event loop services it. UI thread only.
func (s *Shell) open(role string) {
	s.loop(s.create(role))
}

// target returns url, size and title of a window role.
func (s *Shell) target(role string) (url string, w, h int, title string) {
	if role == "compact" {
		return s.opts.BaseURL + compactPath, compactW, compactH, s.opts.Title + " - compact"
	}
	return s.opts.BaseURL + s.opts.StartPath, mainW, mainH, s.opts.Title
}

// create builds (or raises) the window for role without running a loop.
// Returns nil when the window already existed or when no native window could
// be created - then a browser app window is opened instead.
func (s *Shell) create(role string) glaze.WebView {
	url, width, height, title := s.target(role)
	if s.opts.Mode == ModeBrowser {
		openAppWindow(url, width, height)
		return nil
	}
	s.mu.Lock()
	if w, ok := s.windows[role]; ok && w.Window() != nil {
		s.mu.Unlock()
		w.Raise()
		return nil
	}
	delete(s.windows, role) // closed meanwhile
	s.mu.Unlock()

	w, err := glaze.New(false)
	if err != nil {
		// No usable WebView (WebView2 / WebKitGTK missing): a browser app
		// window is better than nothing.
		log.Printf("desktop: no native window (%v) - opening a browser window instead", err)
		openAppWindow(url, width, height)
		return nil
	}
	w.SetTitle(title)
	w.SetSize(width, height, glaze.HintNone)
	w.Navigate(url)
	s.mu.Lock()
	s.windows[role] = w
	s.mu.Unlock()
	return w
}

// loop makes sure something pumps the window events, per platform:
//   - macOS with tray: the tray's NSApp loop services every glaze window and
//     closing a window never stops it - nothing to do.
//   - Windows: closing the last window posts WM_QUIT, which would end the
//     tray's loop; so run a nested message loop (any window's Run serves all
//     windows of the thread) until the last window is closed.
//   - no tray (Linux): glaze's Run drives the loop until the last window is
//     closed, then the program ends.
//
// While our loop runs, further windows are just created; the loop serves them.
func (s *Shell) loop(w glaze.WebView) {
	if w == nil {
		return
	}
	s.mu.Lock()
	if s.pumping || (runtime.GOOS == "darwin" && s.hasTray) {
		s.mu.Unlock()
		return
	}
	s.pumping = true
	s.mu.Unlock()

	w.Run()

	s.mu.Lock()
	s.pumping = false
	for role, ww := range s.windows {
		if ww.Window() == nil {
			delete(s.windows, role)
		}
	}
	s.mu.Unlock()
}

// Show brings the main window to the front, opening it if it was closed.
// Safe from any goroutine: a second launch of the program calls it.
func (s *Shell) Show() {
	switch s.opts.Mode {
	case ModeHeadless:
		return
	case ModeBrowser:
		url, w, h, _ := s.target("main")
		openAppWindow(url, w, h)
		return
	}
	if uiDo(func() { s.open("main") }) {
		return
	}
	// No dispatcher (Linux): reach the UI thread through an open window.
	s.mu.Lock()
	w, ok := s.windows["main"]
	s.mu.Unlock()
	if ok && w.Window() != nil {
		w.Dispatch(func() { w.Raise() })
	}
}

// Quit closes every window and stops the tray, so Run returns. Safe from any
// goroutine; repeated calls are no-ops.
func (s *Shell) Quit() {
	s.quitOne.Do(func() {
		close(s.quit)
		s.mu.Lock()
		open := make([]glaze.WebView, 0, len(s.windows))
		for _, w := range s.windows {
			if w.Window() != nil {
				open = append(open, w)
			}
		}
		s.mu.Unlock()
		for _, w := range open {
			w.Terminate()
		}
		tray.Stop()
	})
}
