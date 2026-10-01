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
	"time"

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
	Mode      Mode
	BaseURL   string // e.g. http://127.0.0.1:8473 (always the loopback URL)
	StartPath string // first page of the main window, e.g. "/queue" or "/settings"
	Title     string
	Labels    Labels
	// StatePath is the file remembering the window's view (full / compact)
	// and positions between runs; empty = nothing remembered.
	StatePath string

	// Teardown, if set, runs the program's shutdown synchronously. macOS
	// calls it when the system asks the app to terminate (Dock Quit, Cmd-Q,
	// logout, restart): AppKit exits the process right after, so main's own
	// shutdown after Run would never run. Must be idempotent.
	Teardown func()
}

// Labels are the shell's texts in the UI language (tray menu, compact view
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
	windows  map[string]glaze.WebView // the open window, under the one role "main"
	view     string                   // viewFull or viewCompact: what the window shows
	st       state                    // remembered view and positions
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
	st := loadState(opts.StatePath)
	if st.View == "" {
		st.View = viewFull
	}
	return &Shell{opts: opts, st: st, view: st.View, windows: map[string]glaze.WebView{}, creating: map[string]bool{}, quit: make(chan struct{})}
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
			{Title: s.opts.Labels.or(s.opts.Labels.Open, "Open qslotter"), OnClick: func() { s.open(viewFull) }},
			{Title: s.opts.Labels.or(s.opts.Labels.Compact, "Compact Inbox"), OnClick: func() { s.open(viewCompact) }},
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

// start opens the window (in the view it had last time) and keeps it
// serviced. It reports whether a native window was shown (false: browser
// window / fallback).
func (s *Shell) start() bool {
	first := s.create()
	if first == nil {
		return false
	}
	go s.trackBounds()
	s.loop(first)
	return true
}

// open shows the window - or brings the open one to the front - and makes
// sure an event loop services it. view is viewFull / viewCompact, or "" for
// the view the window had last. UI thread only.
func (s *Shell) open(view string) {
	if view != "" && s.raiseOrSwitch(view) {
		return
	}
	s.loop(s.create())
}

// raiseOrSwitch handles an open window: it moves to view and comes to the
// front. With no window open it only notes the wanted view for create and
// reports false.
func (s *Shell) raiseOrSwitch(view string) bool {
	s.mu.Lock()
	w, ok := s.windows["main"]
	s.mu.Unlock()
	if !ok || w.Window() == nil {
		s.rememberView(view)
		return false
	}
	s.setView(w, view)
	w.Raise()
	return true
}

// rememberView records the wanted view without touching a window.
func (s *Shell) rememberView(view string) {
	if view != viewCompact {
		view = viewFull
	}
	s.mu.Lock()
	s.view = view
	s.st.View = view
	st := s.st
	s.mu.Unlock()
	saveState(s.opts.StatePath, st)
}

// setView switches the open window between the full and the compact view:
// the current position is remembered, the window takes the other view's
// remembered position (or default size) and loads its page. UI thread only.
func (s *Shell) setView(w glaze.WebView, view string) {
	if view != viewCompact {
		view = viewFull
	}
	s.mu.Lock()
	same := s.view == view
	s.mu.Unlock()
	if same {
		return
	}
	s.captureBounds() // the old view's position, before the window moves
	s.rememberView(view)
	url, cw, ch, title := s.target()
	w.SetTitle(title)
	s.mu.Lock()
	saved := s.st.rect(view)
	s.mu.Unlock()
	if saved == nil || !placeWindow(w, *saved) {
		resizeWindow(w, cw, ch)
	}
	w.Navigate(url)
}

// captureBounds notes where the open window is, for the current view, and
// saves the state file when that changed. UI thread only.
func (s *Shell) captureBounds() {
	s.mu.Lock()
	w, ok := s.windows["main"]
	view := s.view
	s.mu.Unlock()
	if !ok || w.Window() == nil {
		return
	}
	r, ok := windowBounds(w)
	if !ok || !r.valid() {
		return
	}
	s.mu.Lock()
	cur := s.st.rect(view)
	if cur != nil && *cur == r {
		s.mu.Unlock()
		return
	}
	s.st.setRect(view, r)
	st := s.st
	s.mu.Unlock()
	saveState(s.opts.StatePath, st)
}

// trackBounds keeps the remembered position current while the app runs (there
// is no portable "window moved / closed" hook, and the window may be closed
// or the app quit at any moment).
func (s *Shell) trackBounds() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			uiDo(s.captureBounds)
		}
	}
}

// target returns url, default content size and title of the current view.
func (s *Shell) target() (url string, w, h int, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view == viewCompact {
		return s.opts.BaseURL + compactPath, compactW, compactH, s.opts.Labels.or(s.opts.Labels.CompactTitle, s.opts.Title+" - compact")
	}
	path := "/queue"
	if !s.started {
		path = s.opts.StartPath // only the very first window (first run: /settings)
	}
	return s.opts.BaseURL + path, mainW, mainH, s.opts.Title
}

// create builds (or raises) the app window, in the current view, without
// running a loop. Returns nil when the window already existed or when no
// native window could be created - then a browser app window is opened
// instead.
func (s *Shell) create() glaze.WebView {
	const role = "main"
	if s.quitting() {
		return nil
	}
	url, width, height, title := s.target()
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
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
	s.mu.Lock()
	saved := s.st.rect(s.view)
	s.mu.Unlock()
	if saved != nil {
		placeWindow(w, *saved)
	}
	// The pages call this to switch between the full and the compact view
	// (they show their switch only when it exists).
	if err := w.Bind("qslotterView", func(view string) {
		w.Dispatch(func() { s.setView(w, view) })
	}); err != nil {
		log.Printf("desktop: bind view switch: %v", err)
	}
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
		url, w, h, _ := s.target()
		openAppWindow(url, w, h)
		return
	}
	if uiDo(func() { s.open("") }) {
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
		saveState(s.opts.StatePath, s.st)
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
