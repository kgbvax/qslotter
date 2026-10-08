// Package web serves the qslotter web UI. It uses chi for routing and the
// stdlib html/template for rendering. htmx attributes drive partial updates.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qsldetermine"
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
	cfgMu      sync.RWMutex
	cfg        *config.Config
	cfgPath    string
	store      store.Store
	broker     *events.Broker
	rules      *qualify.Rules
	refresher  *station.Refresher // shared with main (also used by the UDP listener)
	printer    printer.Printer
	tmpls      map[string]*template.Template // per UI language
	assessMu   sync.Mutex
	assessMemo map[string]*qsldetermine.Result // see classifyFor
	i18n       *i18n.Bundle

	// OpenExternal opens a URL in the system browser; set by main in the
	// desktop app (nil: the endpoint answers 501).
	OpenExternal func(url string) error

	// Quit ends the desktop app (set by main in window/browser mode): a way
	// out that does not depend on the tray icon being visible.
	Quit func()

	// Contacts is the QSO in progress (set by main; nil = not shown).
	Contacts *contact.Tracker

	// now is the clock (overdue requests); a field so tests can move it.
	now func() time.Time

	// validateFn checks the configured credentials (settings page); a field
	// so tests can stub the network out.
	validateFn func(*config.Config) (qrzStatus, clublogStatus i18n.Msg)

	// clublogFn builds the Clublog client of the manual pull/push; a field
	// so tests can point it at a fake Clublog.
	clublogFn func(config.ClublogCfg) *clublog.Client
}

// config returns the current effective config. Safe against concurrent
// swaps from the settings page.
func (s *Server) config() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Rules are the qualifier rules of the server. main hands the same instance to
// the UDP listener and the sync loop, so a filter switched under Settings
// applies to every path at once.
func (s *Server) Rules() *qualify.Rules { return s.rules }

// New wires the web server. refresher is shared with main (the UDP listener
// uses the same instance, so credential changes apply to both paths); pass
// nil when QRZ is not configured. cfgPath is the config file the settings
// page edits; it may be empty (settings editing disabled).
func New(cfg *config.Config, st store.Store, broker *events.Broker, cfgPath string, refresher *station.Refresher) (*Server, error) {
	rules := qualify.NewRules(cfg.Qualify, st)
	srv := &Server{
		cfg:       cfg,
		cfgPath:   cfgPath,
		store:     st,
		broker:    broker,
		rules:     rules,
		refresher: refresher,
		printer:   printer.New(),
		now:       time.Now,
		clublogFn: func(c config.ClublogCfg) *clublog.Client {
			return clublog.New(c.Email, c.AppPassword, c.Call, c.APIKey)
		},
	}
	srv.validateFn = srv.validateCredentials
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"fmtDate":    fmtDate,
		"fmtTime":    fmtTime,
		"upper":      strings.ToUpper,
		"since":      since,
		"q":          url.QueryEscape,
		"counts":     srv.navCounts,
		"methodName": methodName,
		"routeName":  routeName,
		"slice1":     func(s string) string { return s[:min(len(s), 1)] },
		"mdflag": func(md bool) string { // query value for the master-detail pane
			if md {
				return "1"
			}
			return ""
		},
		"yn": yn,
		// translation (VISION D3), bound per language below
		"t":    func(text string, args ...any) string { return text },
		"tm":   func(m i18n.Msg) string { return m.String() },
		"th":   func(text string, args ...any) template.HTML { return template.HTML(text) },
		"lang": func() string { return "en" },
		"jsT":  func() template.JS { return "{}" },
	}).ParseFS(pagesFS, "pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	srv.i18n = i18n.Default
	srv.tmpls = map[string]*template.Template{}
	for _, lang := range srv.i18n.Languages() {
		clone, err := tmpl.Clone()
		if err != nil {
			return nil, err
		}
		srv.tmpls[lang] = clone.Funcs(srv.langFuncs(lang))
	}
	return srv, nil
}

// langFuncs are the translation functions of one language: t translates an
// English message (with printf args), tm a Msg built in Go, th a message
// whose catalog text carries markup (the catalogs are embedded and trusted),
// lang the language code, since a duration in the language's units, jsT the
// strings static/app.js shows.
func (s *Server) langFuncs(lang string) template.FuncMap {
	b := s.i18n
	return template.FuncMap{
		"t":    func(text string, args ...any) string { return b.T(lang, text, args...) },
		"tm":   func(m i18n.Msg) string { return b.T(lang, m.Text, m.Args...) },
		"th":   func(text string, args ...any) template.HTML { return template.HTML(b.T(lang, text, args...)) },
		"lang": func() string { return lang },
		"since": func(t string) string {
			m := sinceMsg(t)
			return b.T(lang, m.Text, m.Args...)
		},
		"jsT": func() template.JS {
			m := map[string]string{}
			for _, k := range jsStrings {
				m[k] = b.T(lang, k)
			}
			raw, _ := json.Marshal(m)
			return template.JS(raw)
		},
	}
}

// jsStrings are the messages static JavaScript shows (toasts, notices); the
// page hands them over translated as window.qslT.
var jsStrings = []string{
	"Error",
	"The card as it will print",
	"Close",
	"Network error - is the qslotter server running?",
	"Skipped %s - your card is still due.",
}

// lang is the UI language of a request: the ui.language setting, else the
// browser's Accept-Language, else English.
func (s *Server) lang(r *http.Request) string {
	return s.i18n.Match(s.config().UI.Language, r.Header.Get("Accept-Language"))
}

// tr translates an English message for the request's language.
func (s *Server) tr(r *http.Request, text string, args ...any) string {
	return s.i18n.T(s.lang(r), text, args...)
}

// notice shows a toast (static/app.js) with a translated message.
func (s *Server) notice(w http.ResponseWriter, r *http.Request, text string, args ...any) {
	raw, _ := json.Marshal(map[string]string{"qslNotice": s.tr(r, text, args...)})
	w.Header().Set("HX-Trigger", string(raw))
}

// fail answers an error whose text is an English catalog message.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, text string, args ...any) {
	http.Error(w, s.tr(r, text, args...), code)
}

// NavCounts are the badges in the site nav.
type NavCounts struct {
	New, Work, Push int  // awaiting a decision, awaiting production, not yet pushed to Clublog
	Expected        int  // requested cards not arrived yet
	ClublogPaused   bool // Clublog refused the credentials (403): no automatic sync
}

// navCounts is called from the templates on every page render; a store error
// just hides the badges.
func (s *Server) navCounts() NavCounts {
	q, d, p, err := s.store.QueueCounts()
	if err != nil {
		return NavCounts{}
	}
	e, _ := s.store.ExpectedCount()
	return NavCounts{New: q, Work: d, Push: p, Expected: e, ClublogPaused: s.clublogPausedAt() != ""}
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
	r.Get("/", s.pageQueue) // no start page: the Inbox (VISION D2)
	r.Get("/log", s.pageLog)
	r.Get("/queue", s.pageQueue)   // (a) decision queue list
	r.Get("/decide", s.pageDecide) // (a) decision queue, one card at a time
	r.Get("/work", s.pageWork)     // (b) work queue list
	r.Get("/work/card", s.pageWorkCard)
	r.Get("/done", s.pageDone)
	r.Get("/receive", s.pageReceive)
	r.Post("/receive/lookup", s.htmxReceiveLookup)
	r.Post("/receive/book", s.htmxReceiveBook)        // key=... per QSO the card confirms
	r.Post("/receive/reply", s.htmxReceiveReply)      // key=..., how=written|print|later
	r.Get("/receive/research", s.htmxReceiveResearch) // key=... of a reply card (live QRZ refresh)
	r.Get("/settings", s.pageSettings)
	r.Post("/settings/save", s.saveSettings) // tab= limits it to that tab's fields
	r.Get("/settings/printing", s.pageSettingsPrinting)
	r.Get("/settings/general", s.pageSettingsGeneral)
	r.Post("/settings/printer", s.postSettingsPrinter)   // name=: printer.name
	r.Post("/settings/testcard", s.postSettingsTestCard) // x=, y=: the active layout with the mm ruler
	// Card layout editor (layout.go): ?name= is a layout in cards/ or :builtin.
	r.Get("/settings/cards", s.pageCards)
	r.Post("/settings/cards/preview", s.htmxCardsPreview) // JSON layout -> the card as the printer lays it out
	r.Post("/settings/cards/save", s.htmxCardsSave)       // JSON layout, ?name= (&create=1&from=)
	r.Post("/settings/cards/activate", s.postCardsActivate)
	r.Post("/settings/cards/rename", s.postCardsRename)
	r.Post("/settings/cards/delete", s.postCardsDelete)
	r.Get("/settings/cards/image", s.getCardsImage)
	r.Post("/settings/cards/image", s.postCardsImage) // multipart "image": the card scan shown behind the fields
	r.Post("/settings/cards/image/delete", s.postCardsImageDelete)
	r.Post("/settings/cards/offset", s.postCardsOffset) // x=, y=: printer.offset_mm
	r.Post("/settings/cards/media", s.postCardsMedia)   // paper=, tray=: printer.paper, printer.tray
	r.Post("/settings/cards/test", s.htmxCardsTest)     // JSON layout: print a test card with the mm ruler
	r.Post("/settings/cards/pdf", s.htmxCardsPDF)       // JSON layout: the card as a PDF
	// Card actions take ?key=... (form/query value): keys contain "|" and
	// portable calls contain "/", which would break {key} path segments.
	r.Post("/queue/yes", s.htmxQueueYes)
	r.Post("/queue/none", s.htmxQueueNone)
	r.Post("/queue/reopen", s.htmxQueueReopen)
	r.Post("/work/print", s.htmxWorkPrint)           // Desk card actions take key=... once per QSO on the card; print = to the print queue
	r.Get("/work/printq", s.pagePrintQueue)          // the Print queue view; htmx: the section alone; ?badge=1: the band button
	r.Post("/work/printrun", s.htmxPrintRun)         // print every queued card as one job, open the run
	r.Post("/work/exportrun", s.htmxExportRun)       // instead: every queued card into one ADIF file for a print service
	r.Get("/work/export", s.htmxExportFile)          // ?name=qsl-....adi: download an exported file
	r.Post("/work/printconfirm", s.htmxPrintConfirm) // the run came out right: its cards are sent
	r.Post("/work/reprint", s.htmxReprint)           // lead=... (+ card:<lead>=key ...): print these again (how=export: export again)
	r.Post("/work/printback", s.htmxPrintBack)       // lead=...: from the run back to the print queue
	r.Post("/work/unprint", s.htmxUnprint)           // key=...: from the print queue back to the Desk
	r.Post("/work/written", s.htmxWorkWritten)
	r.Post("/work/requested", s.htmxWorkRequested)
	r.Post("/work/none", s.htmxWorkNone)
	r.Get("/work/manager", s.htmxWorkManager) // ?manager=CALL: who a manager card goes to
	r.Get("/work/preview", s.htmxWorkPreview) // key=... per QSO, route=, manager=: the card as a PDF
	r.Get("/nav", s.htmxNav)                  // nav bar fragment, refreshed by live.js
	r.Post("/api/open-external", s.apiOpenExternal)
	r.Post("/api/quit", s.apiQuit)
	r.Post("/sync/pull", s.htmxSyncPull)
	r.Post("/sync/push", s.htmxSyncPush)
	r.Get("/events", s.sseEvents)
	r.Get("/queue/row", s.htmxQueueRow)            // ?key=... for SSE-driven fetch
	r.Get("/queue/list", s.htmxQueueList)          // Inbox master list (live refresh)
	r.Get("/queue/current", s.htmxCurrent)         // the QSO in progress (live refresh; ?compact=1)
	r.Post("/current/decide", s.htmxCurrentDecide) // call=, decision=yes|no: card / no card during the QSO
	r.Post("/current/cancel", s.htmxCurrentCancel) // call=: drop a decision (the QSO may never be logged)
	r.Get("/work/list", s.htmxWorkList)            // Desk master list (live refresh)
	r.Post("/queue/recompute", s.htmxQueueRecompute)
	r.Post("/station/refresh", s.htmxStationRefresh) // ?call=... (query: works for portable calls)
	r.Handle("/static/*", staticHandler())
	// Reject cross-origin browser POSTs (CSRF): any web page could otherwise
	// drive the queue, settings or /api/* on 127.0.0.1 - or on the LAN when
	// server.addr is a wildcard. Same-origin and non-browser clients pass.
	return http.NewCrossOriginProtection().Handler(r)
}

// CurrentConfig returns the live config (it changes when Settings are saved).
func (s *Server) CurrentConfig() *config.Config { return s.config() }

// render executes a template in the request's UI language.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	t := s.tmpls[s.lang(r)]
	if t == nil {
		t = s.tmpls["en"]
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
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

// since is how long ago an RFC 3339 time was, in English ("3h"); the
// templates get it in their language (langFuncs).
func since(t string) string { return sinceMsg(t).String() }

// sinceMsg is since as a translatable message.
func sinceMsg(t string) i18n.Msg {
	ts, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return i18n.M("%s", t)
	}
	d := time.Since(ts)
	switch {
	case d < time.Minute:
		return i18n.M("<1m")
	case d < time.Hour:
		return i18n.M("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return i18n.M("%dh", int(d.Hours()))
	}
	return i18n.M("%dd", int(d.Hours()/24))
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

// staticHandler serves the embedded assets. An embed.FS carries no modification
// time, so without help every page load downloaded the stylesheet, scripts and
// fonts again - and the fonts arrived after the first paint (a flash of
// another typeface on every switch between pages). Fonts never change under
// their name (rename the file when one does) and are cached for good;
// everything else is revalidated against a content hash (a 304, no body).
func staticHandler() http.Handler {
	files := http.FileServer(http.FS(staticFS))
	var etags sync.Map // path -> ETag
	etag := func(path string) string {
		if v, ok := etags.Load(path); ok {
			return v.(string)
		}
		b, err := fs.ReadFile(staticFS, strings.TrimPrefix(path, "/"))
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(b)
		e := `"` + hex.EncodeToString(sum[:8]) + `"`
		etags.Store(path, e)
		return e
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if e := etag(r.URL.Path); e != "" {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", e)
		}
		files.ServeHTTP(w, r)
	})
}
