// Package web serves the qslotter web UI. It uses chi for routing and the
// stdlib html/template for rendering. htmx attributes drive partial updates.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/go-chi/chi/v5"
)

// Templates and static assets are embedded so the binary runs from anywhere
// (no repo checkout needed on the deployment machine).
//
//go:embed pages
var pagesFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfgMu     sync.RWMutex
	cfg       *config.Config
	cfgPath   string
	store     store.Store
	broker    *events.Broker
	rules     *qualify.Rules
	refresher *station.Refresher // shared with main (also used by the UDP listener)
	printer   printer.Printer
	tmpl      *template.Template

	// OpenExternal opens a URL in the system browser; set by main in the
	// desktop app (nil: the endpoint answers 501).
	OpenExternal func(url string) error

	// validateFn checks the configured credentials (settings page); a field
	// so tests can stub the network out.
	validateFn func(*config.Config) (qrzStatus, clublogStatus string)
}

// config returns the current effective config. Safe against concurrent
// swaps from the settings page.
func (s *Server) config() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// New wires the web server. refresher is shared with main (the UDP listener
// uses the same instance, so credential changes apply to both paths); pass
// nil when QRZ is not configured. cfgPath is the config file the settings
// page edits; it may be empty (settings editing disabled).
func New(cfg *config.Config, st store.Store, broker *events.Broker, cfgPath string, refresher *station.Refresher) (*Server, error) {
	rules := qualify.NewRules(cfg.Qualify, st)
	srv := &Server{
		cfg:        cfg,
		cfgPath:    cfgPath,
		store:      st,
		broker:     broker,
		rules:      rules,
		refresher:  refresher,
		printer:    printer.New(),
		validateFn: validateCredentials,
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"fmtDate":    fmtDate,
		"fmtTime":    fmtTime,
		"upper":      strings.ToUpper,
		"since":      since,
		"q":          url.QueryEscape,
		"counts":     srv.navCounts,
		"methodName": methodName,
		"yn":         yn,
	}).ParseFS(pagesFS, "pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	srv.tmpl = tmpl
	return srv, nil
}

// NavCounts are the badges in the site nav.
type NavCounts struct {
	New, Work, Push int // awaiting a decision, awaiting production, not yet pushed to Clublog
}

// navCounts is called from the templates on every page render; a store error
// just hides the badges.
func (s *Server) navCounts() NavCounts {
	q, d, p, err := s.store.QueueCounts()
	if err != nil {
		return NavCounts{}
	}
	return NavCounts{New: q, Work: d, Push: p}
}

// methodName spells out a decision/method letter.
func methodName(m string) string {
	switch strings.ToUpper(m) {
	case "B":
		return "Bureau"
	case "D":
		return "Direct"
	case "M":
		return "Via manager"
	case "N":
		return "No card"
	case "W":
		return "Written"
	}
	return m
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.pageLog)
	r.Get("/log", s.pageLog)
	r.Get("/queue", s.pageQueue)   // (a) decision queue list
	r.Get("/decide", s.pageDecide) // (a) decision queue, one card at a time
	r.Get("/work", s.pageWork)     // (b) work queue list
	r.Get("/work/card", s.pageWorkCard)
	r.Get("/done", s.pageDone)
	r.Get("/receive", s.pageReceive)
	r.Post("/receive/lookup", s.htmxReceiveLookup)
	r.Post("/receive/mark", s.htmxReceiveMark)
	r.Get("/station/*", s.pageStation) // wildcard: portable calls contain "/" (EA8/DL1ABC)
	r.Get("/settings", s.pageSettings)
	r.Post("/settings/save", s.saveSettings)
	// Card actions take ?key=... (form/query value): keys contain "|" and
	// portable calls contain "/", which would break {key} path segments.
	r.Post("/queue/decide", s.htmxQueueDecide)
	r.Post("/queue/none", s.htmxQueueNone)
	r.Post("/queue/written", s.htmxQueueWritten)
	r.Post("/queue/back", s.htmxQueueBack)
	r.Post("/queue/reopen", s.htmxQueueReopen)
	r.Post("/queue/batch", s.batchQueue)
	r.Post("/work/print", s.htmxWorkPrint)
	r.Get("/nav", s.htmxNav) // nav bar fragment, refreshed by live.js
	r.Post("/api/open-external", s.apiOpenExternal)
	r.Post("/sync/pull", s.htmxSyncPull)
	r.Post("/sync/push", s.htmxSyncPush)
	r.Get("/events", s.sseEvents)
	r.Get("/queue/row", s.htmxQueueRow) // ?key=... for SSE-driven fetch
	r.Post("/queue/recompute", s.htmxQueueRecompute)
	r.Post("/station/refresh", s.htmxStationRefresh) // ?call=... (query: works for portable calls)
	r.Handle("/static/*", http.FileServer(http.FS(staticFS)))
	return r
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fmtDate(s string) string {
	if len(s) == 8 {
		return s[0:4] + "-" + s[4:6] + "-" + s[6:8]
	}
	return s
}
func fmtTime(s string) string {
	if len(s) == 6 {
		return s[0:2] + ":" + s[2:4]
	}
	if len(s) == 4 {
		return s[0:2] + ":" + s[2:4]
	}
	return s
}
func since(t string) string {
	ts, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return t
	}
	d := time.Since(ts)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// yn renders a QRZ yes/no flag (QRZ sends 1/0) readably.
func yn(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "1", "Y", "YES", "TRUE":
		return "yes"
	case "0", "N", "NO", "FALSE":
		return "no"
	}
	return "?"
}
