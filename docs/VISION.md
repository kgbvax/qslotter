# qslotter — consolidated vision

Consolidated product vision and requirements for qslotter. This reconciles the
original v1 scope (see README.md) with the operator-workflow requirements added
September 2026. Where README and this document disagree, this document wins.

## 1. Product thesis

qslotter is a local QSL workbench. It mirrors the log in real time (Log4OM UDP
+ Clublog reconciliation), decides which QSOs deserve a paper card, helps the
operator decide *how* each card goes out (direct, bureau, via manager, or
none), treats printing and handwriting as equal fulfillment paths, and
reconciles QSL state back to Clublog. One Go binary, local web UI, SQLite.

It stays a discovery tool first: the goal is to learn what QSL process actually
fits, then harden it. Automation of decisions that belong to the operator is
deferred, not accumulated.

## 2. The core loop

The daily workflow while operating:

1. QSO logged in Log4OM → UDP datagram reaches qslotter within ~1 s → the QSO
   appears in the decision queue (SSE, no refresh).
2. The operator glances at the row: suggested QSL method with confidence and
   reason, plus the QSO data needed to write a card by hand.
3. The operator takes the decision: confirm the suggestion or override it
   (direct / bureau / via manager / none). One or two clicks.
4. Act now or later: **Print** (PDF to card stock), **Handwritten → Sent**, or
   leave queued for a batch session.
5. On the operator's schedule: **Push to Clublog** uploads QSL_SENT with the
   chosen method.

Steps 1–3 must work mid-pileup: sub-second, one small window, no full-log
scans on the hot path.

## 3. Decision model: suggestion ≠ decision

- The heuristic ladder in `internal/qsldetermine` (QRZ structured fields + bio
  regexes) produces a *suggestion*: method, manager, refuse-paper, confidence,
  reason.
- The suggestion is displayed, never silently acted on.
- The operator's decision is recorded on the queue item (`desired_method`) and
  survives queue recompute.
- "None" is a first-class decision meaning *no paper card* (electronic-only or
  refused), not a silent no-op. It maps onto the existing `RefusePaper`
  semantics and leaves the queue with the reason recorded.
- LLM-based determination (`internal/llmqsl`, `cmd/qsl-eval`, local Ollama)
  stays an offline calibration effort. If it beats the heuristic on the eval
  set it may become a second suggestion source; it does not enter the app
  before that gate.

## 4. Fulfillment: print or handwrite

Two parallel paths, equal citizens:

- **Print:** PDF via the YAML template → printer (existing path).
- **Handwritten:** the operator writes the card; the UI's job is to put the
  right data in front of them — their name and address, date, call, band,
  mode, RST, my call, manager if via-manager — and record the outcome.

Both end at queue status `sent` with the chosen method, and both push back the
correct method to Clublog. Today "Mark Sent" always uploads bureau ("B"),
independent of the card's actual route — a defect this vision retires.

## 5. UI strategy: one web UI, two modes

Decision (2026-09): **no Flutter app.** One web UI with two presentation
modes, plus a tray-resident shell. Rationale: single binary, single
toolchain, browser windows cover the small-window case, and the Go backend
stays the only source of truth.

### Compact mode — the decision surface

- Small window (~480×640), dense rows, no nav chrome. Served as
  `/queue?compact=1`.
- Live via SSE: `new_qso` inserts rows, `station_updated` refreshes method
  columns in place (existing wiring, reused).
- Row content: date, call, band, mode, RST, name, suggested method + route,
  status.
- Row actions: method chooser (suggestion preselected), Print, Handwritten →
  Sent, Skip/None.
- Built for glance-ability during operation.

### Expanded mode — the deliberation surface

- The station page grown up: full QRZ picture — `qslmgr`, eqsl/mqsl/lotw
  flags, email, address, bio text — and the suggestion with confidence and
  reason.
- Same decision controls as compact mode, plus context: previous QSOs with
  this station, cards sent/received.
- This is where tricky cases get decided (manager changed, "direct only"
  buried in the bio).

### Tray shell (Windows-first)

- The binary hosts a system-tray icon: *Queue (compact)*, *Log*, *Receive*,
  *Exit*. No second process.
- Library: `fyne.io/systray` — Windows backend is pure Win32 syscalls, so the
  `GOOS=windows CGO_ENABLED=0` cross-compile is unaffected. Tray code lives in
  `internal/tray` behind `//go:build windows` with a no-op stub; macOS/Linux
  builds are unchanged.
- exe icon/version/manifest via `go-winres`; build with `-ldflags "-H
  windowsgui"` (no console) plus file logging under `%LOCALAPPDATA%`; a
  single-instance mutex; one shared shutdown path for SIGINT and tray Exit.
- Windows hides newly registered tray icons until the user promotes them;
  document this in the run instructions.

### Compact window sizing

A page cannot resize a browser tab the user opened manually. The compact
window is therefore *launched* at size, not resized afterwards:

    msedge --app=http://127.0.0.1:8473/queue?compact=1 --window-size=480,640 ^
      --window-position=X,Y --no-first-run --no-default-browser-check ^
      --user-data-dir=%LOCALAPPDATA%\qslotter\edge-profile

- The dedicated `--user-data-dir` is mandatory: Chromium merges invocations
  per profile, and an already-running Edge/Chrome silently ignores
  `--window-size`/`--window-position`.
- Inside an app window the page may call `window.resizeTo()` to height-fit
  content — progressive enhancement, guarded by `?app=1` or the
  `display-mode: standalone` media query. Never relied upon.
- Firefox has no viable geometry flags: fall back to opening the URL plainly.
- Escape hatch if browser-flag quirks bite: WebView2 embedding
  (`jchv/go-webview2`, pure Go) owns the window outright. Recorded, not
  chosen.
- htmx is vendored to `/static` (today it loads from unpkg; the shack PC must
  work offline).

## 6. Unchanged pillars

- Receive flow stays keyboard-driven.
- Push-back stays a manual button until a background sync loop lands; the
  button remains for on-demand pushes afterwards.
- SQLite now; CouchDB remains the v2 target (the `Store` interface is already
  backend-agnostic).
- Card templates stay YAML with millimetre coordinates.

## 7. Where we are today: gaps vs. this vision

Status after the v1.x + shell + v1.y-loop implementation (2026-09-29):

| Operator requirement | Status | Evidence |
|---|---|---|
| Live queue from Log4OM UDP | **done** | `internal/udplistener` → auto-enqueue → SSE (`queue.html`) |
| One-click "sent" for handwritten cards | **done** | `htmxQueueHandwrite` uses the recorded decision |
| Per-card method decision (direct/bureau/none) | **done** | method chooser preselects the suggestion; `QueueSetMethod` persists; survives recompute |
| Handwritten as option parallel to Print | **done** | `/queue/{key}/handwrite`, same `sent` semantics |
| Compact decision window | **done** | `/queue?compact=1` + Edge `--app` launcher (`internal/tray`); htmx vendored |
| Expanded QRZ-preference view | **done** | station page: confidence + reason + decision controls |
| Queue rows carry handwriting-relevant QSO data | **done** | rows show name + RST (`qsos.name/qth` columns, ADIF-mapped); card gets name/QTH/my_name |
| Tray-resident launcher | **done** | `internal/tray` (fyne.io/systray, `//go:build windows`), single-instance mutex, `server.tray`/`open_compact` config |
| Background pull/push loop | **done** | `sync.Loop`, gated by `pull_interval`/`push_interval`; buttons retained |

Remaining gaps (v2 / later):

- Windows exe icon/version embedding: config ready in `winres/winres.json`;
  the one-step `go run github.com/tc-hib/go-winres@latest make` is left to
  the operator (writes `rsrc_windows_amd64.syso`, then builds link it).
- `qsl-eval`, `qsl-eval.jsonl`, and `ww` in the repo root are scratch output
  from the method-determination calibration effort; they are not shipped
  artifacts.
- Everything in section 8 marked v2.

## 8. Roadmap

**v1.x — decision-first UI (web):**

1. Wire `desired_method` end-to-end: refresher suggestion preselects the
   chooser; queue rows get a method chooser; `htmxQueueSend` uses the chosen
   method (`SetQSLSentLocal(method)`), and push-back uploads it.
2. Handwritten → Sent action.
3. "None" decision (leaves queue with recorded reason; recompute must not
   resurrect it).
4. Compact mode: `?compact=1` template, responsive CSS, vendored htmx.
5. Expanded station mode: confidence + reason + decision controls.
6. Richer card/queue data: name and address available for handwriting and the
   card template.

**v1.x+ — shell:**

7. `internal/tray` (Windows) + go-winres icon + `-H windowsgui` + file log +
   single-instance mutex; compact-window launcher using the Edge `--app`
   recipe; optional auto-open on server start.

**v1.y — operations:**

8. Background pull/push loop (buttons retained); batch actions on the queue.

**v2 — platform:**

9. CouchDB store backend + migration command.
10. Receive-card photo + OCR.
11. LLM method suggestion behind the eval gate (only if `qsl-eval` shows it
    beats the heuristic).

## 9. Decision log

- **2026-09-29 — No Flutter; web UI with two modes + tray shell.** One
  toolchain, one binary, browser windows cover the compact case.
- **2026-09-29 — Compact window via Chromium `--app` launch at fixed size,
  dedicated user-data-dir; in-page `resizeTo` as enhancement.** Pages cannot
  resize manually-opened tabs; Firefox gives no geometry control.
- **2026-09-29 — Tray via `fyne.io/systray` behind `//go:build windows`.**
  Pure-syscall Windows backend preserves the CGO_ENABLED=0 cross-compile;
  `getlantern/systray` is stalled, `lxn/walk` dormant, the full Fyne toolkit
  needs cgo.
- **2026-09-29 — Suggestion ≠ decision.** `qsldetermine` stays the suggestion
  source; the operator's choice is recorded in `desired_method`; the hardcoded
  bureau push-back is retired.
- **2026-09-29 — Print and Handwritten are parallel fulfillment paths,** both
  ending at status `sent` with the chosen method.
