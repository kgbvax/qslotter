// Command qslotter is the QSL card handling tool, DL9ET style.
// It runs a local web UI backed by a Clublog QSO source and a QRZ station-info
// cache, shown in its own application window (native WebView; -ui browser or
// headless for the alternatives). A UDP listener receives Log4OM's ADIF
// broadcast for sub-second ingestion of newly-logged QSOs; Clublog pull runs in
// the background for reconciliation (config-gated). See README.md,
// docs/VISION.md and config.example.yaml.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	gosync "sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/desktop"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
	"github.com/dl9et/qslotter/internal/udplistener"
	"github.com/dl9et/qslotter/internal/web"
)

// The native window and the tray must run on the main OS thread (macOS
// requires it); lock before anything else starts.
func init() { runtime.LockOSThread() }

const appID = "qslotter" // single-instance identity

func main() {
	cfgFlag := flag.String("config", "", "path to config.yaml (default: ./config.yaml if present, else the user config dir; created on first start)")
	uiFlag := flag.String("ui", "", "user interface: window (own app window + tray), browser (tray + browser app windows), headless (server only); default from ui.mode")
	flag.Parse()

	logPath := setupLog()

	// Single instance first - before the config is resolved (a hand-off
	// launch must not create a starter config) and before UDP or SQLite (two
	// copies would fight over the port and the DB). A second launch asks the
	// running one to show its window, then exits.
	var shell atomic.Pointer[desktop.Shell]
	release, err := desktop.SingleInstance(appID, func() {
		if s := shell.Load(); s != nil {
			s.Show()
		}
	})
	if errors.Is(err, desktop.ErrAlreadyRunning) {
		log.Printf("qslotter is already running - asked it to show its window")
		return
	}
	if err != nil {
		log.Printf("single-instance check: %v (continuing)", err)
	}
	rotateLog(logPath)

	cfgPath, firstRun, err := resolveConfig(*cfgFlag)
	if err != nil {
		fatal("Configuration", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("Configuration", err)
	}
	log.Printf("config %s, database %s, log %s", cfgPath, cfg.Store.Path, logPath)
	uiValue := cfg.UI.Mode
	if *uiFlag != "" {
		uiValue = *uiFlag
	}
	mode, err := desktop.ParseMode(uiValue)
	if err != nil {
		fatal("Configuration", err)
	}

	if cfg.Store.Driver != "sqlite" {
		fatal("Configuration", fmt.Errorf("store driver %q not supported in v1 (only sqlite)", cfg.Store.Driver))
	}

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		fatal("Database", err)
	}

	broker := events.New()

	// Qualifier rules shared by the UDP listener (fast path) and the sync
	// orchestrator (full scan after Clublog pull).
	rules := qualify.NewRules(cfg.Qualify, st)
	if rules.Since != "" {
		log.Printf("qualify: queueing QSOs from %s on (qualify.since: all lifts the cutoff)", rules.Since)
	}
	// One-time backlog discard (VISION A1): only QSOs from the cutoff on count.
	if n, err := rules.DiscardBacklog(st); err != nil {
		log.Printf("qualify: backlog discard: %v", err)
	} else if n > 0 {
		log.Printf("qualify: %d QSO(s) before %s were still waiting for a decision - filed as \"no card\" (backlog, reopenable under Done)", n, rules.Since)
	}

	// Station-info refresher (QRZ lookup + cache), shared by the web UI and
	// the UDP listener so credential changes in the settings UI apply to both.
	// Always constructed: without QRZ credentials it has a nil client (lookups
	// are no-ops), and credentials saved later under /settings are swapped in
	// live via SetClient - no restart needed.
	var qrzClient *qrz.Client
	if cfg.QRZ.Username != "" {
		qrzClient = qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
		log.Printf("qrz station-info refresher enabled (cache TTL %s)", cfg.QRZ.CacheTTL)
	} else {
		log.Printf("qrz not configured - station info will not be auto-fetched (set it under Settings)")
	}
	refresher := station.New(st, qrzClient, broker, cfg.QRZ.CacheTTL)

	srv, err := web.New(cfg, st, broker, cfgPath, refresher)
	if err != nil {
		fatal("Web UI", err)
	}
	if mode == desktop.ModeWindow {
		srv.OpenExternal = desktop.OpenExternal // only the app window needs it
	}

	// The QSO in progress (logger's current-contact broadcast) and cards
	// written during it, shared by the UDP feed, the Clublog pull and the UI.
	contacts := contact.NewTracker(st, broker, func(ctx context.Context, call string) { _, _ = refresher.Get(ctx, call) })
	srv.Contacts = contacts

	// UDP listener: real-time feed from Log4OM.
	udp := udplistener.New(cfg.UDP.Listen, st, broker, rules, refresher)
	udp.Contacts = contacts
	ctx, cancel := context.WithCancel(context.Background())
	if err := udp.Start(ctx); err != nil {
		fatal("Log4OM UDP feed", err)
	}
	log.Printf("udp listener on %s (Log4OM ADIF feed)", cfg.UDP.Listen)

	// Background reconciliation loop, config-gated (pull_interval>0 pulls,
	// push_interval>0 pushes). The manual buttons on the log page stay. Each
	// tick uses the live credentials (changed under Settings) and is skipped
	// while there are none - no failed logins against Clublog.
	var loopDone <-chan struct{}
	clublogClient := clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword,
		cfg.Clublog.Call, cfg.Clublog.APIKey)
	if cfg.Clublog.PullInterval > 0 || cfg.Clublog.PushInterval > 0 {
		o := &sync.Orchestrator{Store: st, Rules: rules, Broker: broker, OnNewQSO: contacts.QSOLogged,
			Configure: func() (*clublog.Client, bool) {
				c := srv.CurrentConfig().Clublog
				if c.Email == "" || c.APIKey == "" || c.AppPassword == "" {
					return nil, false
				}
				return clublog.New(c.Email, c.AppPassword, c.Call, c.APIKey), true
			}}
		loopDone = sync.Loop(ctx, o, cfg.Clublog.PullInterval, cfg.Clublog.PushInterval)
		log.Printf("background sync: pull every %s, push every %s",
			cfg.Clublog.PullInterval, cfg.Clublog.PushInterval)
	}

	// Startup credential validation: surface bad QRZ/Clublog credentials in
	// the log right away. Non-fatal - a transient outage must not kill the
	// shack feed; the settings page can fix and re-check credentials live.
	go func() {
		if qrzClient != nil {
			if err := qrzClient.CheckCredentials(); err != nil {
				log.Printf("[startup] QRZ credentials: %v", err)
			} else {
				log.Printf("[startup] QRZ credentials OK (%s)", cfg.QRZ.Username)
			}
		}
		if cfg.Clublog.Email != "" && cfg.Clublog.APIKey != "" {
			if err := clublogClient.CheckCredentials(); err != nil {
				log.Printf("[startup] Clublog credentials: %v", err)
			} else {
				log.Printf("[startup] Clublog credentials OK (%s)", cfg.Clublog.Call)
			}
		}
	}()

	// Bind before any window opens, so the window never races the server.
	// Request contexts derive from reqCtx: cancelling it ends the long-lived
	// SSE streams at shutdown instead of waiting out the shutdown timeout.
	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		fatal("Web server", fmt.Errorf("listen on %s: %w (is another program using the port?)", cfg.Server.Addr, err))
	}
	reqCtx, reqCancel := context.WithCancel(context.Background())
	httpSrv := &http.Server{
		Handler:     srv.Routes(),
		BaseContext: func(net.Listener) context.Context { return reqCtx },
	}
	go func() {
		log.Printf("qslotter listening on http://%s", cfg.Server.Addr)
		if cfg.Server.Wildcard() {
			log.Printf("server.addr is a wildcard: the UI is reachable from the network and has no login - keep it on a trusted LAN")
		}
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http server: %v", err)
		}
	}()

	// One shutdown path for every way out (tray, window, Dock/Cmd-Q, logout,
	// signal): idempotent, bounded.
	var teardownOnce gosync.Once
	teardown := func() {
		teardownOnce.Do(func() {
			log.Printf("shutting down...")
			reqCancel()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutCancel()
			_ = httpSrv.Shutdown(shutCtx)
			// Stop the background loop, then let an in-flight pull/push
			// finish (bounded) so it is not torn down mid-import.
			cancel()
			if loopDone != nil {
				select {
				case <-loopDone:
				case <-time.After(10 * time.Second):
					log.Printf("background sync still running; exiting anyway")
				}
			}
			udp.Stop()
			_ = st.Close()
			release()
		})
	}

	// The UI owns the main thread until the user quits (or a signal).
	startPath := "/queue"
	if firstRun {
		startPath = "/settings" // new install: enter credentials first
	}
	// The shell's own texts follow ui.language, else the system language
	// (the window gets the browser language from the WebView).
	lang := i18n.Default.Match(cfg.UI.Language, desktop.SystemLanguage())
	sh := desktop.New(desktop.Options{
		Mode:      mode,
		BaseURL:   cfg.Server.LocalURL(),
		StartPath: startPath,
		StatePath: filepath.Join(filepath.Dir(cfgPath), "window-state.json"),
		Teardown:  teardown,
		Labels: desktop.Labels{
			Open:         i18n.Default.T(lang, "Open qslotter"),
			Compact:      i18n.Default.T(lang, "Compact Inbox"),
			Quit:         i18n.Default.T(lang, "Quit qslotter"),
			Tooltip:      i18n.Default.T(lang, "qslotter - QSL workbench"),
			CompactTitle: i18n.Default.T(lang, "qslotter - compact"),
		},
	})
	shell.Store(sh)
	if mode != desktop.ModeHeadless {
		srv.Quit = sh.Quit
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		sh.Quit()
	}()
	if err := sh.Run(); err != nil {
		log.Printf("desktop: %v", err)
	}
	teardown()
}

// resolveConfig picks the config file: the -config flag, else ./config.yaml
// when present (existing installs), else the user config dir - where a
// starter config is written on the very first start (firstRun).
func resolveConfig(flagPath string) (path string, firstRun bool, err error) {
	if flagPath != "" {
		return flagPath, false, nil
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml", false, nil
	}
	path, err = config.DefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := config.WriteDefault(path); err != nil {
			return "", false, fmt.Errorf("create %s: %w", path, err)
		}
		log.Printf("first start: created %s", path)
		return path, true, nil
	}
	return path, false, nil
}

// fatal reports a startup error where the user can see it - a dialog, since a
// double-clicked app has no console - and exits.
func fatal(what string, err error) {
	log.Printf("%s: %v", what, err)
	desktop.ShowError("qslotter cannot start", fmt.Sprintf("%s: %v", what, err))
	os.Exit(1)
}
