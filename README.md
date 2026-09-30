# qslotter

QSL card handling, DL9ET style. A local web tool that pulls your log from
Clublog, decides which QSOs deserve a paper QSL card, looks up the DX's QSL
method on QRZ, prints the QSO data onto card stock, and pushes QSL state back
to Clublog. Also: quick keyboard-driven entry of received cards.

This is a work-in-progress discovery tool — the goal is to learn what kind of
QSL process I actually want, then harden it. The consolidated vision and
requirements live in [docs/VISION.md](docs/VISION.md).

## Status (v1)

- [x] Pull QSOs from Clublog (`getadif.php`) into a local SQLite mirror
- [x] Diff + upsert on re-pull (hash-based)
- [x] Real-time UDP ingestion from Log4OM (sub-second on QSO added)
- [x] SSE push to the queue page (`GET /events`) — new QSOs appear without refresh
- [x] Web UI: log, send queue, receive, station detail
- [x] Receive flow: type callsign -> list QSOs -> mark received
- [x] PDF rendering of QSO data onto card stock (`go-pdf/fpdf`)
- [x] Cross-platform printing: `lp`/`lpstat` on macOS/Linux, SumatraPDF on Windows
- [x] QRZ XML client + heuristic bio parsing for QSL method
- [x] Qualifier rules: mode filters (FT*), first-contact-only, override marker
- [x] Push-back to Clublog (`putlogs.php`) with QSL_SENT/QSL_RCVD updates
- [x] Store interface (backend-agnostic; CouchDB v2 ready)
- [x] Auto-enqueue eligible QSOs into the work queue (Recompute button; UDP path auto-enqueues)
- [x] QRZ lookup wired into the queue view (method/manager columns refresh via SSE)
- [x] Per-card method decision: direct / bureau / electronic / manager / none (suggestion preselected, decision survives recompute)
- [x] Handwritten cards as a parallel fulfillment path (one click marks sent with the chosen method)
- [x] Compact decision queue (`/queue?compact=1`) + expanded station view with confidence/reason
- [x] Push-back to Clublog uploads the chosen method (`QSL_SENT=Y` + `QSL_SENT_AS`)
- [x] htmx vendored locally (UI works offline)
- [x] Windows tray shell: compact queue / log / receive / exit from the notification area
- [x] Background reconciliation loop (config-gated: `pull_interval`, `push_interval`)
- [x] Batch actions on the queue (select rows → sent / handwritten / none / skip)
- [ ] Photo capture + OCR of received cards (v2)
- [ ] Template editor + calibration grid in the UI

## Real-time ingestion (Log4OM UDP)

Log4OM emits a UDP datagram containing one ADIF record within ~1 second of a
QSO being added. qslotter listens on `127.0.0.1:1273` (configurable via
`udp.listen`) and upserts each datagram into the store, then publishes a
`new_qso` event on the SSE stream so the queue page auto-refreshes.

### Configure Log4OM

In Log4OM -> Settings -> Program Configuration -> **UDP Functions** (see
https://www.log4om.com/integrated/ for the UDP integration overview):

- Add an **outbound** UDP destination pointing at qslotter:
  `127.0.0.1:1273` (or whatever you set `udp.listen` to).
- **Format: ADIF** (not N1MM XML — qslotter skips XML datagrams with a warning).
- Enable on **QSO added**.

### Why not the HTTP POST webhook?

Log4OM's HTTP POST path batches outgoing POSTs on a ~2–3 minute timer
(intentional, so a quickly-edited QSO isn't uploaded). That doesn't meet the
sub-minute goal. UDP is sub-second. The hourly Clublog pull remains as a
reconciliation backstop for any missed UDP datagrams and for QSL-state edits
(which Log4OM does not re-broadcast over UDP).

## Reconciliation (Clublog pull)

The hourly Clublog pull (`clublog.pull_interval: 1h`) is a *reconciliation*
backstop, not the primary feed. It catches:
- QSOs the UDP listener missed (datagram loss, qslotter down, Log4OM restart).
- QSL-state edits in Log4OM (which are not re-broadcast over UDP).
- QSOs logged while qslotter was offline.

It is idempotent (hash-based upsert) so any QSO the UDP listener already
ingested is a no-op. You can also trigger a pull manually via the "Pull from
Clublog" button on the log page.

## Stack

- **Go**, single binary, local web UI (chi router + stdlib `html/template` + htmx)
- **SQLite** via `modernc.org/sqlite` (pure Go, no cgo) — cross-compiles to Windows
- **PDF** via `go-pdf/fpdf`
- **Clublog** HTTP API (`getadif.php` / `putlogs.php`) — see internal/clublog
- **QRZ** XML API (`xmldata.qrz.com/xml/current/`) — see internal/qrz

## Build

    go build ./cmd/qslotter

Cross-compile for Windows (from macOS):

    GOOS=windows GOARCH=amd64 go build -o qslotter.exe ./cmd/qslotter

To embed the tray icon, version info, and DPI manifest into the exe, generate
the resource object once (needs network; uses
[go-winres](https://github.com/tc-hib/go-winres)):

    go run github.com/tc-hib/go-winres@latest make   # writes rsrc_windows_amd64.syso

Commit the resulting `rsrc_windows_amd64.syso` and plain `go build` links it
automatically; rerun the command after editing `winres/winres.json`.

With `server.tray: true` you can hide the console window by adding
`-ldflags "-H windowsgui"` (log output then goes nowhere - rely on the web UI).
The tray icon shows Queue (compact) / Log / Receive / Exit; the compact entry
opens Edge in app mode at a small fixed size (a dedicated browser profile under
`%LOCALAPPDATA%\qslotter\` keeps the window-size flags working). Windows hides
newly registered tray icons until you drag them onto the visible taskbar area.

## Run

1. Copy `config.example.yaml` to `config.yaml` and fill in your Clublog API
   key + app password and your QRZ credentials. Secrets can be expanded from
   the environment via `${VAR}`.
2. On Windows, printing needs SumatraPDF: install it, or bundle the portable
   build as `third_party/sumatrapdf/SumatraPDF.exe` next to `qslotter.exe`.
   Use **3.5.x** — the 3.6 print engine hung on the shack PC, and
   `-list-printers` can hang too (current builds resolve the default printer
   via winspool instead). On macOS/Linux nothing extra is needed.
3. Start the server:

       ./qslotter -config config.yaml

4. Open http://127.0.0.1:8473/ in a browser. Click "Pull from Clublog" to
   populate the log view.

## Layout

    cmd/qslotter/       main entrypoint
    internal/adif/      minimal ADIF reader/writer (Clublog round-trip)
    internal/clublog/   Clublog HTTP client (pull + push)
    internal/config/    YAML config loader with ${ENV} expansion
    internal/events/    in-process pub/sub broker (UDP -> SSE)
    internal/printer/   PDF rendering + platform print shims (unix/windows)
    internal/qualify/   QSO eligibility rules (FT exclusion, first-contact, override)
    internal/qrz/       QRZ XML client (session, lookup, bio)
    internal/qsldetermine/ QSL method decision ladder
    internal/store/     Store interface + SQLite backend (CouchDB-ready for v2)
    internal/sync/      pull/parse/diff/upsert + push-back orchestrator
    internal/template/  YAML card layout templates
    internal/udplistener/ Log4OM UDP ADIF receiver (sub-second ingestion)
    internal/web/       chi router + html/template pages + htmx + SSE

## Future (v2)

- **CouchDB backend** for `internal/store`. The `Store` interface is already
  backend-agnostic; v2 adds `internal/store/couchdb.go` (via go-kivik/kivik)
  and a `qslotter migrate-sqlite-to-couchdb` command. Goal: a single
  network-accessible DB for qslotter + future QSO tools (stats, awards,
  eventual Log4OM replacement), with offline replication to a second CouchDB
  on the couch mac. SQLite remains as the v1 default and a fallback.

## License

AGPL-3.0-or-later. See LICENSE. SumatraPDF (bundled on Windows) is GPLv3.