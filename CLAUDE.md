# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

qslotter is a Go 1.26 single-binary local web tool for QSL card handling. It mirrors QSOs from Clublog, ingests new QSOs in real time via UDP from Log4OM, decides which QSOs need paper QSL cards, renders them to PDF, and pushes QSL state back to Clublog.

The consolidated product vision, requirements, gap list, and decision log live in `docs/VISION.md`. Check it before changing scope-relevant behavior; where README and VISION disagree, VISION wins.

## Common commands

Build the binary:

    go build ./cmd/qslotter

Cross-compile for Windows from macOS/Linux:

    GOOS=windows GOARCH=amd64 go build -o qslotter.exe ./cmd/qslotter

Optionally embed icon/version/manifest into the exe: `go run
github.com/tc-hib/go-winres@latest make` regenerates `rsrc_windows_amd64.syso`
from `winres/winres.json`; committed .syso files are linked by plain builds
automatically.

Run the server:

    ./qslotter -config config.yaml

Run all tests:

    go test ./...

Run a single test package or test:

    go test ./internal/qualify
    go test ./internal/qualify -run TestEligible

Static analysis:

    go vet ./...

There is no Makefile or CI config; use the standard Go toolchain.

## Running locally

1. Copy `config.example.yaml` to `config.yaml`.
2. Set Clublog credentials (`clublog.email`, `clublog.app_password`, `clublog.api_key`) and QRZ credentials (`qrz.username`, `qrz.password`). Secrets may be expanded from the environment via `${VAR}`.
3. Start the server and open http://127.0.0.1:8473/.
4. Click **Pull from Clublog** on the log page to populate the local database.

On Windows, install or bundle SumatraPDF under `third_party/sumatrapdf/` for printing. macOS and Linux use `lp`/`lpstat`.

## Deploying to the shack PC (bwpc)

The production instance runs on **bwpc** (`192.168.1.197`, also `bwpc.local`) —
a German-locale Windows 11 box. SSH: `iotte@bwpc`, key auth (use
`ConnectTimeout >= 20`; the first banner is slow). Deploy dir:
`C:\Users\iotte\qslotter\` with `qslotter.exe`, `config.yaml` (real secrets,
absolute `store.path`), `third_party\sumatrapdf\SumatraPDF.exe` (must stay on
**3.5.x** — the 3.6 print engine hangs).

Deploy cycle:

    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o qslotter.exe ./cmd/qslotter
    ssh iotte@bwpc "taskkill /im qslotter.exe /f"   # a running exe locks the file
    scp qslotter.exe iotte@bwpc:qslotter/
    ssh iotte@bwpc "schtasks /run /tn qslotter"     # at-logon task, interactive

Verify from the box itself — the server binds `127.0.0.1:8473` and is not
reachable from the mac:

    ssh iotte@bwpc "powershell -NoProfile -c \"(Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8473/decide -TimeoutSec 5).StatusCode\""

Gotchas learned the hard way: templates/static are embedded in the binary, so
a fresh checkout run without `go build` fails to parse pages; SSH-spawned
processes die when the session closes (use the scheduled task, not
`start /b`, for durable runs); action URLs take the QSL key as a query/form
value (`?key=`), never as a path segment — keys contain `|` and portable
calls contain `/`. Never write config credentials into shell commands; edit
`config.yaml` on the box or use the /settings view.

## High-level architecture

### Data flow

Two feeds populate the store:

- **Primary feed:** Log4OM UDP ADIF datagrams on `udp.listen` (`127.0.0.1:1273` by default). `internal/udplistener` parses each datagram and upserts it into the store in a sub-second path. New QSOs are auto-enqueued by `internal/qualify` and published as `new_qso` events on the in-process event broker.
- **Reconciliation feed:** Clublog pull via `internal/clublog` and `internal/sync`, run in a config-gated background loop (`clublog.pull_interval` > 0; `sync.Loop`) plus manual "Pull from Clublog" / "Push back to Clublog" buttons. The orchestrator fetches the full ADIF log, diffs/upserts using a SHA-256 hash of the canonical ADIF record, then runs the qualifier to enqueue newly eligible QSOs. Push-back runs on `clublog.push_interval` (default off) or the button, and uploads `QSL_SENT=Y` + `QSL_SENT_AS=<method>` from the local columns.

The web UI (`internal/web`) consumes from the store and the event broker. The queue page subscribes to `/events` (SSE) so new QSOs appear without refresh.

### Store and local state

`internal/store.Store` is a backend-agnostic interface. v1 implements it with SQLite using `modernc.org/sqlite` (pure Go, no cgo) so cross-compilation to Windows works without a C toolchain.

QSOs are keyed by `QSLKey`, formatted as `CALL|YYYYMMDD|HHMMSS|BAND`.

Local QSL state is kept in `qsl_sent_local`, `qsl_rcvd_local`, `qslsdate_local`, and `qslrdate_local` columns. These track changes made inside qslotter that have not yet been pushed to Clublog. `PendingPushBack()` returns the divergent rows; `PushBack()` in `internal/sync` uploads them and `MarkPushed()` clears the divergence.

### Qualifier rules

`internal/qualify.Rules` decides whether a QSO should enter the work queue. Rules are configured in `config.yaml` under `qualify`:

- `exclude_modes`: exact modes to skip.
- Any mode starting with `FT`, `JS8`, `WSPR`, `MSK`, or `FST` is also skipped.
- `first_contact_only`: only the first-ever QSO with a callsign is eligible.
- `override_marker`: a substring (e.g. `QSL!`) in the QSO notes force-includes the QSO.

`EnqueueAll()` scans the full log and enqueues newly eligible QSOs, while preserving queue items the user has already acted on (`printed`, `sent`, `skipped`). The UDP path uses `EligibleForNewQSO()` with only the recent QSOs for the same call to avoid a full scan per datagram.

### QRZ station info

`internal/qrz` calls the QRZ XML API. `internal/station.Refresher` caches results in the `station_info` table and publishes `station_updated` events so the queue page can refresh method/manager columns asynchronously.

### PDF cards and printing

`internal/template` loads YAML card templates with millimetre-based field coordinates. `internal/printer` renders QSO data onto a PDF with `go-pdf/fpdf` and sends it to the configured printer. Platform print commands are isolated in `printer_unix.go` and `printer_windows.go`.

### Package layout

- `cmd/qslotter`: entrypoint, wiring, shutdown.
- `cmd/ocr-eval`: offline calibration CLI for received-card photo intake. Runs OCR output (from `tools/ocr-dump.swift`, Apple Vision) through `internal/intake` against a copy of the database and reports auto/pick/miss rates. Working data lives in `/eval/` (git-ignored). Not part of the app.
- `cmd/qsl-eval`: offline calibration CLI comparing LLM-based (Ollama) QSL-method determination against the `qsldetermine` heuristic. Not part of the app; scratch outputs (`qsl-eval`, `qsl-eval.jsonl`, `ww` in the repo root) are not shipped artifacts.
- `internal/adif`: minimal ADIF reader/writer used for Clublog round-trip.
- `internal/clublog`: Clublog HTTP client (`getadif.php`, `putlogs.php`).
- `internal/config`: YAML loader with `${ENV}` expansion.
- `internal/events`: in-process pub/sub broker used for UDP → SSE (event names `new_qso`, `station_updated`; the queue page listens on `/events`).
- `internal/intake`: pure-function matcher from OCR text of a photographed incoming card to a QSO in the log (callsign match tolerant of OCR-confusable characters, date/band/mode scoring, `auto`/`pick`/`miss` classes). Roadmap v2 "receive-card photo + OCR"; not yet wired into the web UI.
- `internal/llmqsl`: LLM-based QSL-method vocabulary/mapping (`none/direct/buero/manager-*`); used only by `cmd/qsl-eval`.
- `internal/printer`: PDF rendering and platform print shims.
- `internal/qualify`: eligibility rules and auto-enqueue logic.
- `internal/qrz`: QRZ XML session, lookup, and bio parsing.
- `internal/qsldetermine`: QSL method decision ladder.
- `internal/station`: QRZ cache refresher.
- `internal/store`: `Store` interface and SQLite implementation.
- `internal/sync`: pull/parse/diff/upsert plus push-back orchestration and background loop.
- `internal/template`: YAML card layout templates.
- `internal/tray`: optional Windows tray shell (fyne.io/systray, build-tagged; no-op elsewhere) with the compact-window launcher.
- `internal/udplistener`: Log4OM UDP ADIF receiver.
- `internal/web`: chi router, `html/template` pages, htmx partials, SSE endpoint.

## Notes

- No Cursor rules (`.cursor/`, `.cursorrules`) or Copilot instructions (`.github/copilot-instructions.md`) were present.
- The `card.template` config value is a path to a YAML template file; if empty or missing, the built-in default template is used.
- Known deltas, by design until the v1.x roadmap in `docs/VISION.md` lands: none — the v1.x decision-first UI shipped 2026-09-29 (method chooser with suggestion preselect, Handwritten/None actions, `desired_method` wired end-to-end, compact + expanded modes, htmx vendored). Remaining roadmap: v1.y batch actions, v2 CouchDB/OCR/LLM gate.
- Queue statuses are `queued/decided/printed/sent/skipped` (`overridden` appears in schema comments only). `decided` = method stamped in the Decide view, card still awaiting print/send; `EnqueueAll` skips QSOs already present in any queue status, so user decisions survive recompute.
- ADIF push-back: `qsl_sent_local` always holds `Y` (the ADIF enum); the chosen send method is stored in `qsl_sent_method_local` and uploaded as `QSL_SENT_AS`. "None" is a decision (`desired_method=N`, queue status `skipped`), never a sent flag.
- `/settings` edits the config file on disk (YAML-node edit, comments preserved; empty password fields keep the stored secret), swaps the live config + QRZ client immediately (web + UDP listener share one `station.Refresher`), and runs credential checks inline. Startup also validates credentials and logs the outcome (`[startup] QRZ credentials: ...`) without exiting — a transient outage must not kill the feed.
