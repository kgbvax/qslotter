// Command qslotter is the QSL card handling tool, DL9ET style.
// It runs a local web UI backed by a Clublog QSO source and a QRZ station-info
// cache. A UDP listener receives Log4OM's ADIF broadcast for sub-second
// ingestion of newly-logged QSOs; Clublog pull runs in the background for
// reconciliation (config-gated). See README.md, docs/VISION.md and
// config.example.yaml.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
	"github.com/dl9et/qslotter/internal/tray"
	"github.com/dl9et/qslotter/internal/udplistener"
	"github.com/dl9et/qslotter/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if cfg.Store.Driver != "sqlite" {
		log.Fatalf("store driver %q not supported in v1 (only sqlite)", cfg.Store.Driver)
	}
	// Single-instance guard before touching UDP or SQLite (Windows; no-op
	// elsewhere). Two instances would fight over the UDP port and the DB.
	if already, err := tray.AcquireSingleInstance(); err != nil {
		log.Fatalf("single-instance check: %v", err)
	} else if already {
		log.Fatalf("qslotter is already running")
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	broker := events.New()

	// Qualifier rules shared by the UDP listener (fast path) and the sync
	// orchestrator (full scan after Clublog pull).
	rules := qualify.NewRules(cfg.Qualify, st)
	if rules.Since != "" {
		log.Printf("qualify: queueing QSOs from %s on (qualify.since: all lifts the cutoff)", rules.Since)
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

	srv, err := web.New(cfg, st, broker, *cfgPath, refresher)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	// UDP listener: real-time feed from Log4OM.
	udp := udplistener.New(cfg.UDP.Listen, st, broker, rules, refresher)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := udp.Start(ctx); err != nil {
		log.Fatalf("udp listener: %v", err)
	}
	defer udp.Stop()
	log.Printf("udp listener on %s (Log4OM ADIF feed)", cfg.UDP.Listen)

	// Background reconciliation loop, config-gated (pull_interval>0 pulls,
	// push_interval>0 pushes). The manual buttons on the log page stay.
	var loopDone <-chan struct{} = nil
	clublogClient := clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword,
		cfg.Clublog.Call, cfg.Clublog.APIKey)
	if cfg.Clublog.PullInterval > 0 || cfg.Clublog.PushInterval > 0 {
		o := &sync.Orchestrator{Store: st, Clublog: clublogClient, Rules: rules, Broker: broker}
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

	httpSrv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: srv.Routes(),
	}

	go func() {
		log.Printf("qslotter listening on http://%s", cfg.Server.Addr)
		if cfg.Server.Wildcard() {
			log.Printf("server.addr is a wildcard: the UI is reachable from the network and has no login - keep it on a trusted LAN")
		}
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// Tray shell (Windows): menu opens the compact queue / log / receive and
	// shuts down via trayDone.
	trayDone := make(chan struct{})
	if cfg.Server.Tray {
		go tray.Run(tray.Options{
			BaseURL:            cfg.Server.LocalURL(),
			OpenCompactOnStart: cfg.Server.OpenCompact,
			OnExit:             func() { close(trayDone) },
		})
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-trayDone:
	}
	log.Printf("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	// Stop the background loop, then let an in-flight pull/push finish
	// (bounded) so it is not torn down mid-import.
	cancel()
	if loopDone != nil {
		select {
		case <-loopDone:
		case <-time.After(10 * time.Second):
			log.Printf("background sync still running; exiting anyway")
		}
	}
}
