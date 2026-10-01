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
	Labels      Labels


	// Teardown, if set, runs the program's shutdown synchronously. macOS
	// calls it when the system asks the app to terminate (Dock Quit, Cmd-Q,
	// logout, restart): AppKit exits the process right after, so main's own
	// shutdown after Run would never run. Must be idempotent.
	Teardown func()
}

// Labels are the shell's texts in the UI language (tray menu, compact window
// title); empty fields fall back to English.
type Labels struct {
	Open, Compact, Quit, Tooltip, CompactTitle string
}

func (l Labels) or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

const (
	mainW, mainH       = 1200, 860
	compactW, compactH = 480, 640
	compactPath        = "/queue?compact=1&app=1"
)

// Shell is the running desktop UI.
type Shell struct {
	opts Options

	mu       sync.Mutex
	windows  map[string]glaze.WebView // open windows by role ("main", "compact")
	hasTray  bool
	pumping  bool            // a window event loop of ours is running on the UI thread
	creating map[string]bool // roles whose window is being built (glaze.New pumps messages)
	started  bool            // the first main window was created (StartPath used)
	quit     chan struct{}
	quitOne  sync.Once
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
	return &Shell{opts: opts, windows: map[string]glaze.WebView{}, creating: map[string]bool{}, quit: make(chan struct{})}
}

// Run shows the UI and blocks until Quit (tray "Quit", a signal, or - where
// there is no tray - closing the last window). Must run on the locked main
// goroutine.
func (s *Shell) Run() error {
	if s.opts.Mode == ModeHeadless {
		<-s.quit
		return nil
	}
	if s.quitting() { // a signal arrived while starting up
		return nil
	}
	if err := initUIThread(); err != nil {
		log.Printf("desktop: ui dispatcher: %v (a second launch cannot bring the window forward)", err)
	}
	err := tray.Run(tray.Config{
		Title:   "qslotter",
		Tooltip: s.opts.Labels.or(s.opts.Labels.Tooltip, "qslotter - QSL workbench"),
		Icon:    trayIcon(),
		Items: []tray.Item{
			{Title: s.opts.Labels.or(s.opts.Labels.Open, "Open qslotter"), OnClick: func() { s.open("main") }},
			{Title: s.opts.Labels.or(s.opts.Labels.Compact, "Compact Inbox"), OnClick: func() { s.open("compact") }},
			{Separator: true},
			{Title: s.opts.Labels.or(s.opts.Labels.Quit, "Quit qslotter"), OnClick: s.Quit},
		},
		OnReady: func() {
			if s.quitting() {
				// Quit came before the tray could be stopped: stop it now.
				tray.Stop()
				return
			}
			s.mu.Lock()
			s.hasTray = true
			s.mu.Unlock()
			if s.opts.Mode == ModeWindow {
				s.setupDock()
			}
			s.start()
		},
	})
	log.Printf("desktop: tray loop ended (%v)", err)
	if errors.Is(err, tray.ErrUnsupported) {
		// No tray backend (Linux): the windows are the app; the program ends
		// with the last one. Browser windows and the browser fallback are
		// separate processes - then wait for a signal instead.
		if !s.start() {
			<-s.quit
		}
		return nil
	}
	return err
}

// quitting reports whether Quit was called.
func (s *Shell) quitting() bool {
	select {
	case <-s.quit:
		return true
	default:
		return false
	}
}

// start opens the initial windows and keeps them serviced. It reports
// whether a native window was shown (false: browser windows / fallback).
func (s *Shell) start() bool {
	first := s.create("main")
	if s.opts.OpenCompact {
		if c := s.create("compact"); first == nil {
			first = c
		}
	}
	if first == nil {
		return false
	}
	s.loop(first)
	return true
}

// open shows the window for role - or brings an already open one to the
// front - and makes sure an event loop services it. UI thread only.
func (s *Shell) open(role string) {
	s.loop(s.create(role))
}

// target returns url, size and title of a window role.
func (s *Shell) target(role string) (url string, w, h int, title string) {
	if role == "compact" {
		return s.opts.BaseURL + compactPath, compactW, compactH, s.opts.Labels.or(s.opts.Labels.CompactTitle, s.opts.Title+" - compact")
	}
	path := "/queue"
	s.mu.Lock()
	if !s.started {
		path = s.opts.StartPath // only the very first main window (first run: /settings)
	}
	s.mu.Unlock()
	return s.opts.BaseURL + path, mainW, mainH, s.opts.Title
}

// create builds (or raises) the window for role without running a loop.
// Returns nil when the window already existed or when no native window could
// be created - then a browser app window is opened instead.
func (s *Shell) create(role string) glaze.WebView {
	if s.quitting() {
		return nil
	}
	url, width, height, title := s.target(role)
	if role == "main" {
		s.mu.Lock()
		s.started = true
		s.mu.Unlock()
	}
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
	if s.creating[role] {
		// glaze.New pumps the message queue while WebView2 starts; a tray
		// click or a second launch can land here meanwhile. One is enough.
		s.mu.Unlock()
		return nil
	}
	delete(s.windows, role) // closed meanwhile
	s.creating[role] = true
	s.mu.Unlock()

	w, err := glaze.New(false)

	s.mu.Lock()
	delete(s.creating, role)
	s.mu.Unlock()
	if s.quitting() {
		// Quit arrived while the window was being built; on Windows its
		// quit message was consumed by glaze's start-up pump - repost it.
		postQuit()
		if err == nil {
			w.Destroy()
		}
		return nil
	}
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
//   - no tray (Linux): glaze's Run serves one window until that window is
//     closed; keep running it for the remaining windows, then the program
//     ends.
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
	hasTray := s.hasTray
	s.mu.Unlock()

	for w != nil {
		w.Run()
		log.Printf("desktop: window loop ended (quitting=%v)", s.quitting())
		if s.quitting() {
			// Windows: Quit's quit message (one per thread) ended this nested
			// loop; repost it so the tray's outer loop ends too.
			postQuit()
			break
		}
		w = nil
		if !hasTray {
			w = s.anyOpenWindow() // Linux: another window is still open
		}
	}

	s.mu.Lock()
	s.pumping = false
	for role, ww := range s.windows {
		if ww.Window() == nil {
			delete(s.windows, role)
		}
	}
	s.mu.Unlock()
}

// anyOpenWindow returns a window that is still open, or nil.
func (s *Shell) anyOpenWindow() glaze.WebView {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.windows {
		if w.Window() != nil {
			return w
		}
	}
	return nil
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
		log.Printf("desktop: quit requested")
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
