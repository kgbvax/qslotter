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
	rules := &qualify.Rules{
		ExcludeModes:     cfg.Qualify.ExcludeModes,
		FirstContactOnly: cfg.Qualify.FirstContactOnly,
		OverrideMarker:   cfg.Qualify.OverrideMarker,
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"fmtDate": fmtDate,
		"fmtTime": fmtTime,
		"upper":   strings.ToUpper,
		"since":   since,
		"q":       url.QueryEscape,
	}).ParseFS(pagesFS, "pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		cfg:        cfg,
		cfgPath:    cfgPath,
		store:      st,
		broker:     broker,
		rules:      rules,
		refresher:  refresher,
		printer:    printer.New(),
		tmpl:       tmpl,
		validateFn: validateCredentials,
	}, nil
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.pageLog)
	r.Get("/log", s.pageLog)
	r.Get("/queue", s.pageQueue)
	r.Get("/decide", s.pageDecide)
	r.Get("/receive", s.pageReceive)
	r.Post("/receive/lookup", s.htmxReceiveLookup)
	r.Post("/receive/mark", s.htmxReceiveMark)
	r.Get("/station/*", s.pageStation) // wildcard: portable calls contain "/" (EA8/DL1ABC)
	r.Get("/settings", s.pageSettings)
	r.Post("/settings/save", s.saveSettings)
	// Queue actions take ?key=... (form/query value): keys contain "|" and
	// portable calls contain "/", which would break {key} path segments.
	r.Post("/queue/print", s.htmxQueuePrint)
	r.Post("/queue/skip", s.htmxQueueSkip)
	r.Post("/queue/send", s.htmxQueueSend)
	r.Post("/queue/handwrite", s.htmxQueueHandwrite)
	r.Post("/queue/method", s.htmxQueueMethod)
	r.Post("/queue/none", s.htmxQueueNone)
	r.Post("/queue/batch", s.batchQueue)
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
	d := time.Since(ts).Round(time.Minute)
	return d.String()
}
