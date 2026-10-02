# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

qslotter is a Go 1.27 single-binary QSL card workbench: a local web UI shown in its own app window (native WebView via glaze, cgo-free). It mirrors QSOs from Clublog, ingests new QSOs in real time via UDP from Log4OM, decides which QSOs need paper QSL cards, renders them to PDF, and pushes QSL state back to Clublog.

The consolidated product vision, requirements, gap list, and decision log live in `docs/VISION.md`. Check it before changing scope-relevant behavior; where README and VISION disagree, VISION wins.

## Common commands

Build the binary:

    go build ./cmd/qslotter

Release builds for every OS (cgo-free cross-compile, from any machine):

    scripts/release.sh          # dist/: darwin, windows (GUI exe + go-winres icon), linux
    scripts/macapp.sh           # on a Mac, after release.sh: dist/qslotter.app + zip

Run it (window + tray; `-ui browser` / `-ui headless` for the alternatives):

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

Deploy cycle (the exe is the windowed GUI build with icon; it runs in the
interactive session via the at-logon scheduled task `qslotter`):

    scripts/release.sh                              # dist/qslotter-windows-amd64.exe
    # clean shutdown (closes the DB; taskkill without /f does not reach the session):
    ssh iotte@bwpc "powershell -NoProfile -c \"Invoke-WebRequest -UseBasicParsing -Method Post http://127.0.0.1:8473/api/quit\""
    ssh iotte@bwpc "cd qslotter && mkdir backup-<name> & copy /y qslotter.exe backup-<name>\ & copy /y qslotter.db backup-<name>\"
    scp dist/qslotter-windows-amd64.exe iotte@bwpc:qslotter/qslotter.exe
    ssh iotte@bwpc "schtasks /run /tn qslotter"

Verify from the box itself (`server.addr` is `0.0.0.0:8473` there, so the LAN
reaches it too); the log is `%LOCALAPPDATA%\qslotter\qslotter.log`:

    ssh iotte@bwpc "powershell -NoProfile -c \"(Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8473/ -TimeoutSec 5).StatusCode\""

Longer PowerShell is easiest sent as `-EncodedCommand` (UTF-16LE base64): the
German-locale cmd mangles quotes and umlauts.

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

- **Current contact:** on the same UDP port, Log4OM's CALLSIGN service (the bare callsign as text) or N1MM-family `<lookupinfo>` XML is the QSO in progress (`internal/contact`: `Classify` tells datagrams apart, `Tracker` holds the contact and cards "written now" during it; booked on the QSO when it is logged - UDP or Clublog pull via `Orchestrator.OnNewQSO`). The Inbox shows it in a "QSO in progress" box (`internal/web/current.go`, SSE event `current_contact`).
- **Primary feed:** Log4OM UDP ADIF datagrams on `udp.listen` (`127.0.0.1:1273` by default). `internal/udplistener` parses each datagram and upserts it into the store in a sub-second path. New QSOs are auto-enqueued by `internal/qualify` and published as `new_qso` events on the in-process event broker.
- **Reconciliation feed:** Clublog pull via `internal/clublog` and `internal/sync`, run in a config-gated background loop (`clublog.pull_interval` > 0; `sync.Loop`) plus manual "Pull from Clublog" / "Push back to Clublog" buttons. The orchestrator fetches the full ADIF log, diffs/upserts using a SHA-256 hash of the canonical ADIF record, then runs the qualifier to enqueue newly eligible QSOs. Push-back runs on `clublog.push_interval` (default off) or the button, and uploads `QSL_SENT=Y` plus `QSL_SENT_VIA=<B|D>` (and `QSL_VIA=<manager>` for a manager card; ADIF marks `QSL_SENT_VIA=M` import-only) from the local columns. A pull also closes open queue items whose QSO Clublog now reports as sent.

The web UI (`internal/web`) consumes from the store and the event broker. Every card move publishes `queue_changed` ({key,to}) on the broker; `/events` (SSE) carries it (plus `station_updated`) to `static/live.js`, which keeps the queue lists, card views and nav badges current without refresh (master-detail pages reload their list via `/queue/list` / `/work/list`; card actions carry `next=`/`prev=` so the card below follows). Card-view keyboard handling is shared in `static/keys.js`.

### Store and local state

`internal/store.Store` is a backend-agnostic interface. v1 implements it with SQLite using `modernc.org/sqlite` (pure Go, no cgo) so cross-compilation to Windows works without a C toolchain.

QSOs are keyed by `QSLKey`, formatted as `CALL|YYYYMMDD|HHMMSS|BAND`.

The card lifecycle lives in `qsl_work_queue.status`, moved only by guarded store transitions (`QueueAccept/WrittenNow/Decline` for the Inbox - from `queued` only; `QueueWritten/Printed/Requested/DeskDecline/Back` for the Desk - from `decided` only, so a stale page of either gets 409; `QueueReply` for Incoming QSLs - no item/`queued`/`skipped` -> `decided`, `decided` untouched, `sent`/`requested` 409; `QueueReopen/CloseSentElsewhere/DiscardBacklog`: one transaction each for queue row + `qsos` columns + `qsl_events` row, `ErrConflict` -> HTTP 409 for stale pages, `ErrBadRoute` -> 400):

    queued --yes--> decided --print/written + route--> sent      (queued --written now B|D--> sent)
    decided --requested (OQRS..., channel + note)--> requested   (QSL_RCVD=R; no own card)
    queued/decided --none--> skipped      decided --back--> queued      sent/skipped/requested --reopen--> queued
    (no item)/queued/skipped --reply (their card arrived)--> decided   (then written/print at once, or later at the Desk)

`queued` = the Inbox (`/queue` master-detail - the list only selects, the detail pane decides; `/queue?compact=1` yes/no rows; `/decide` card by card: whether a card goes out); `decided` = the Desk (`/work` master-detail grouped by route, `/work/card` card by card, code in `internal/web/desk.go`: one card per worked callsign covering all its open QSOs - printed one row per QSO -, the route - bureau, direct, via manager direct/bureau - chosen when the card is printed or written, preselected from a route recorded earlier or the QRZ suggestion; Desk actions post `key=` once per QSO on the card and move them in one transaction via `queueTxMany`); `sent`/`skipped`/`requested` = `/done`. Incoming QSLs (`/receive`, `internal/web/receive.go`): book their card for ticked QSOs (`qsl_rcvd_local=Y`), then a reply per worked callsign (one card, incl. that call's QSOs already at the Desk): `QueueReply` puts them onto the Desk (creating, accepting or overruling "no card"), optionally finished at once (written now / print); a card answering a request needs no reply; requested cards are listed as expected per request, overdue after `receive.overdue_weeks`. Reply keys need two presses (w/p/l/x, then the same key or Enter), so typing the next callsign over an open panel cannot answer it. `store.BaseCall` prefers a callsign-like part over a prefix (KH6/K1A -> K1A). At startup `qualify.DiscardBacklog` files Inbox QSOs before the cutoff as "no card" (note `backlog`), once per cutoff.

Local QSL state is kept in `qsl_sent_local`, `qsl_rcvd_local`, `qslsdate_local`, and `qslrdate_local` columns. These track changes made inside qslotter that have not yet been pushed to Clublog. `PendingPushBack()` returns the divergent rows; `PushBack()` in `internal/sync` uploads them and `MarkPushed()` clears the divergence.

### Qualifier rules

`internal/qualify.Rules` decides whether a QSO enters the decision queue. Rules are configured in `config.yaml` under `qualify` (build them with `qualify.NewRules(cfg.Qualify, store)`):

- A QSO whose card already went out (Clublog `QSL_SENT=Y` or local) is never queued - checked first, before the override.
- `override_marker`: a substring (e.g. `QSL!`) in the QSO notes force-includes the QSO despite mode, cutoff and first-contact rules; the reason is stored in `override_reason` and shown on the card.
- `since`: only QSOs on/after this date are queued. Empty = the day qslotter first ran (`meta.first_run_date`), `all` = no cutoff.
- `exclude_modes`: exact modes to skip; any mode starting with `FT`, `JS8`, `WSPR`, `MSK`, or `FST` is also skipped.
- `first_contact_only` (default off): only the first-ever QSO with a callsign is eligible. Off by default: repeat contacts are queued and shown with their history.

`EnqueueAllKeys()` scans the full log and enqueues newly eligible QSOs. `Enqueue` never overwrites an existing item (`ON CONFLICT DO NOTHING`), so a recompute cannot reset a decision. The UDP path uses `EligibleForNewQSO()` with only the recent QSOs for the same call to avoid a full scan per datagram.

### QRZ station info

`internal/qrz` calls the QRZ XML API (bio HTML is stripped of style/script; "Not found" is an answer, not an error). `internal/station.Refresher` (always constructed; nil client until credentials exist, swapped live from `/settings`) caches results in the `station_info` table - including a 24 h negative entry for stations QRZ does not know - and publishes `station_updated` so open pages refresh asynchronously. Automatic lookups use `Get` (TTL, in-flight dedup, 2 min cooldown after a failure); only the Refresh button in the research panel (`POST /station/refresh?call=`, answers 204; the `station_updated` event reloads the open panels) forces one. `internal/qsldetermine.Assess` reads the stored raw fields (qslmgr, mqsl/eqsl/lotw, bio) into signals with their source and quoted words and suggests (B/D/M/N) only from *stated* evidence; flags and eQSL/LoTW mentions are facts, never a route. It is computed on read (memoised in `web.assessFor`), not stored: `station_info.qsl_method/qsl_route/refuse_paper/qsl_confidence/qsl_reason` are legacy columns, written empty. Suggestions are shown tentatively (chip strip + dashed hint); the Desk preselects a route only when QRZ states one. `internal/qsldetermine/testdata/corpus.json` is the golden set of real records.

### PDF cards and printing

`internal/template` loads YAML card templates with millimetre-based field coordinates. `internal/printer` renders QSO data onto a PDF with `go-pdf/fpdf` and sends it to the configured printer. Platform print commands are isolated in `printer_unix.go` and `printer_windows.go`.

### Package layout

- `cmd/qslotter`: entrypoint, wiring, config bootstrap (user config dir on first start), log file, shutdown.
- `cmd/ocr-eval`: offline calibration CLI for received-card photo intake. Runs OCR output (from `tools/ocr-dump.swift`, Apple Vision) through `internal/intake` against a copy of the database and reports auto/pick/miss rates. Working data lives in `/eval/` (git-ignored). Not part of the app.
- `cmd/qsl-eval`: offline calibration CLI comparing LLM-based (Ollama) QSL-method determination against `qsldetermine.Assess`. Not part of the app; scratch outputs (`qsl-eval`, `qsl-eval.jsonl`, `ww` in the repo root) are not shipped artifacts.
- `internal/adif`: minimal ADIF reader/writer used for Clublog round-trip.
- `internal/clublog`: Clublog HTTP client (`getadif.php`, `putlogs.php`).
- `internal/config`: YAML loader with `${ENV}` expansion.
- `internal/events`: in-process pub/sub broker used for UDP → SSE (event names `new_qso`, `station_updated`; the queue page listens on `/events`).
- `internal/intake`: pure-function matcher from OCR text of a photographed incoming card to a QSO in the log (callsign match tolerant of OCR-confusable characters, date/band/mode scoring, `auto`/`pick`/`miss` classes). Roadmap v2 "receive-card photo + OCR"; not yet wired into the web UI.
- `internal/llmqsl`: LLM-based QSL-method vocabulary/mapping (`none/direct/buero/manager-*`); used only by `cmd/qsl-eval`.
- `internal/printer`: PDF rendering and platform print shims.
- `internal/qualify`: eligibility rules and auto-enqueue logic.
- `internal/qrz`: QRZ XML session, lookup, and bio parsing.
- `internal/qsldetermine`: what QRZ states about QSL (signals + suggestion, `Assess`).
- `internal/station`: QRZ cache refresher.
- `internal/store`: `Store` interface and SQLite implementation.
- `internal/sync`: pull/parse/diff/upsert plus push-back orchestration and background loop.
- `internal/template`: YAML card layout templates.
- `internal/desktop`: the app shell - glaze WebView windows (main + compact), native/tray (macOS/Windows), single instance, cgo-free UI-thread dispatcher, macOS Dock delegate, browser app-window fallback. The only package that imports glaze/native. Threading rule: the UI runs on the main goroutine (locked in `init`); other goroutines only call `Shell.Show`/`Shell.Quit`.
- `internal/udplistener`: Log4OM UDP receiver: ADIF QSOs, plus the current-contact broadcast handed to `internal/contact`.
- `internal/contact`: the QSO in progress (VISION A1b): datagram classification and the tracker for cards written during a QSO.
- `internal/web`: chi router, `html/template` pages, htmx partials, SSE endpoint.
- `internal/i18n`: UI translation (VISION D3). Catalogs `locales/<lang>/*.json` map the English text to the translation (English needs none; missing = English); `i18n.M` builds a message in Go, templates translate with `{{t "English %s" .X}}`, `{{tm .Msg}}` (an `i18n.Msg` is a struct - test it with `.IsZero`, never `{{if .Msg}}`) and `{{th ...}}` for catalog text with markup (escape every argument with `html`). Templates are cloned per language at startup; `Server.render(w, r, ...)` picks the request's language (`ui.language`, else Accept-Language). Errors to the user go through `s.fail(w, r, code, "English")`, toasts through `s.notice`. `TestGermanComplete` renders every page/flow in German and lists missing texts - add new UI text to `locales/de` in the same change.

## Notes

- No Cursor rules (`.cursor/`, `.cursorrules`) or Copilot instructions (`.github/copilot-instructions.md`) were present.
- The `card.template` config value is a path to a YAML template file; if empty or missing, the built-in default template is used.
- Roadmap status: see `docs/VISION.md` §7-8. The two-queue workflow (decision queue -> work queue -> done) was rebuilt 2026-09-30; the three work areas of the 2026-10-01 operator walkthrough (VISION §2) are built (roadmap v1.z complete): Inbox (yes / no / written now, QSO in progress) and Desk (route at finish, requested (OQRS), no card, research panel, one card per station) as master-detail views, Incoming QSLs (reply question, expected/overdue requests), the three-area menu and the German/English UI; next: later v2 CouchDB/OCR/LLM gate, envelope/label printing. Checked against Clublog 2026-10-01: a push lands (`QSLSDATE`, `QSL_RCVD=Y` come back), but Clublog's export never contains `QSL_SENT`, `QSL_SENT_VIA` or `QSL_VIA` - whether it stores the route is unknowable; a sent card shows only as `QSLSDATE`, so `QSO.SentPerLog` (QSL_SENT=Y or a QSL sent date) is the "sent per log" test everywhere (EffectiveSent, qualifier, close-sent-elsewhere, reopen). Each pull resets `qsl_sent` to empty. Still unverified: `QSL_RCVD=R`, and whether a pull overwrites NAME/QTH/NOTES that arrived via UDP.
- Queue statuses are `queued/decided/sent/skipped` (`printed` is legacy: migrated once at startup to `sent`, or back to `queued` when no route was recorded). `desired_method` holds the route `B/D/M` (empty while a "yes" card waits at the Desk; decided cards from builds before 2026-10-01 may carry one, used as preselection), `N` (no card), or legacy `W` (written without a route). `send_via` = how the card travelled (B/D, also for manager cards); `note` = `written now` / `backlog` / `sent elsewhere` / a request's note; `channel` = how a `requested` card was requested. `EnqueueAll` skips QSOs already present in any status, so user decisions survive recompute.
- ADIF push-back: `qsl_sent_local` always holds `Y`; `qsl_sent_method_local` holds how the card travelled, B or D (uploaded as `QSL_SENT_VIA`), and a manager route adds `QSL_VIA` = the queue row's manager. Legacy rows may hold `M` (QSL_VIA only) or nothing (written without a route). "None" is a decision (`desired_method=N`, status `skipped`), never a sent flag. A request pushes `QSL_RCVD=R`, but never over a received card: a local R is not set when Clublog has R or Y, and a pull bringing Y drops a pending local R.
- `/settings` edits the config file on disk (YAML-node edit, comments preserved; empty password fields keep the stored secret), swaps the live config + QRZ client immediately (web + UDP listener share one `station.Refresher`), and runs credential checks inline. Startup also validates credentials and logs the outcome (`[startup] QRZ credentials: ...`) without exiting — a transient outage must not kill the feed.

- All routes sit behind `http.NewCrossOriginProtection` (cross-origin browser POSTs are rejected; same-origin htmx and non-browser clients pass). Shutdown is one idempotent `teardown` in main, also run by the macOS terminate delegate (Dock Quit, Cmd-Q, logout) before AppKit exits. The background Clublog loop takes live credentials per tick (`Orchestrator.Configure`) and skips while there are none.
- Desktop app (2026-10-01): the window loads the loopback server by URL; never serve the UI through glaze's `app://` scheme (it buffers the SSE stream). Relative `store.path` / `card.template` are relative to the config file. Logs: `~/Library/Logs/qslotter`, `%LOCALAPPDATA%\qslotter`, `~/.cache/qslotter`. bwpc runs the windowed exe via the scheduled task `qslotter` (`cmd /c ... qslotter.exe -config config.yaml`).
