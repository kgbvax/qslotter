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

## 1a. Founding requirements (original concept, with status)

The first written concept of qslotter (the original upstream README) set the
requirements below. Kept here as the product's founding intent; each with
where it stands today.

- **The logbook is the QSL source, and QSL state should mirror back into it.**
  Done: Clublog is the reconciliation source, Log4OM the real-time feed;
  `QSL_SENT`/`QSL_SENT_AS`/`QSL_RCVD` push back via `putlogs.php`.
- **"It should not rely on UDP QSO propagation, this is unreliable."**
  Resolved by architecture: UDP is the *primary* feed (sub-second, works
  mid-pileup) and the Clublog pull is the reconciliation backstop for
  anything UDP missed. The system never depends on UDP alone.
- **Automatic qualification with simple, configurable rules** (skip FT\*
  modes, first-contact-only). Done: `qualify.exclude_modes`,
  `first_contact_only`.
- **Override for memorable QSOs, controlled via notes.** Done: the `QSL!`
  marker (`qualify.override_marker`) force-includes a QSO.
- **Smart method determination from QRZ and other sources — none, "no paper
  please", direct, bureau, manager — accurate enough for semi-automatic
  processing.** Done in the intended shape: `qsldetermine` suggests with
  confidence and reason, the operator confirms with one click. "Other
  sources" beyond QRZ (LLM bio interpretation) sits behind the `qsl-eval`
  gate (roadmap v2).
- **Synchronous and asynchronous mode** — prepare cards at the desk after
  each QSO, or in a batch later. Done: the Decide view is the synchronous
  path (one card, advance on decision); the Queue list with batch actions is
  the asynchronous one.
- **Print QSO data onto the card via a configurable template.** Done: YAML
  templates with millimetre coordinates; DX name and QTH included.
- **Hand-written QSL cards supported**, usually synchronously. Done: the
  Written action marks the card sent with the chosen method and shows the
  data to copy.
- **Electronic QSL is out of scope** — left to the logbook. Enforced since
  2026-09-30: E is no longer a decision; eQSL-only stations resolve to
  "no card".
- **Receiving: quick keyboard entry of received cards.** Partially done: the
  Receive page does callsign → pick QSOs → mark received. The automatic
  response card for "PSE QSL" is not built (roadmap v1.y/v2).
- **Optional photo capture of received cards with data extraction.** Not
  built (roadmap v2).

## 2. The core loop

Two queues, one card moving through them (decided 2026-09-30):

1. **QSO logged** in Log4OM -> UDP datagram reaches qslotter within ~1 s -> it
   enters the **decision queue** (`/queue`, `/decide`; live via SSE). Digital
   modes and already-carded QSOs stay out; repeat contacts come in, with their
   history.
2. **Decide.** The card view puts the research next to the decision: what QRZ
   says about the station's QSL habits (qslmgr text, bio lines, flags), the
   earlier QSOs with the station and what happened to their cards (sent,
   received, LoTW), other cards still pending for the station. The operator
   chooses **Bureau, Direct, Via manager, No card, or Written** (card filled in
   on the spot: done, no route). One or two clicks or keys.
3. **The decided card leaves the decision queue at once.** Bureau / Direct /
   Via manager go to the **work queue** (`/work`, `/work/card`), one card at a
   time: **Print** (PDF to card stock) or **Written**, whereupon the card is
   *sent* and the next one appears. A printer error leaves it in the queue.
4. **Done** (`/done`) lists finished cards; **Reopen** takes one back to the
   decision queue (a misclick). **Back** takes a decided card back before it
   is produced.
5. On the operator's schedule: **Push to Clublog** uploads `QSL_SENT=Y` with
   `QSL_SENT_VIA` (B/D) or `QSL_VIA` (manager); a Written-on-the-spot card
   pushes `QSL_SENT=Y` alone.

Steps 1-2 must work mid-pileup: sub-second, one small window, no full-log
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

Both end at queue status `sent`, and both push back the correct route to
Clublog (retired: the old "Mark Sent" that always uploaded bureau). "Written"
from the decision queue is its own outcome - the card is done on the spot and
no route is recorded.

## 5. UI strategy: one web UI, two modes

Decision (2026-09): **no Flutter app.** One web UI with two presentation
modes, plus a tray-resident shell. Rationale: single binary, single
toolchain, browser windows cover the small-window case, and the Go backend
stays the only source of truth.

### Compact mode — the decision surface

- Small window (~480×640), dense rows, no nav chrome. Served as
  `/queue?compact=1`.
- Live via SSE: `queue_changed` inserts and removes rows (a decided card
  vanishes at once, in every open window), `station_updated` refreshes the
  suggestion in place; nav badges follow.
- Row content: date, call, band/mode, name, QRZ suggestion (dashed hint, never
  a decision).
- Row actions: **B / D / M (+ manager call) / none / Written** - deciding is
  the only thing the decision queue offers; producing the card (Print) belongs
  to the work queue.
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

Status after the two-queue rebuild (2026-09-30). The previous version of this
table (2026-09-29) marked the workflow "done" while `/decide` served a bare
fragment (no htmx, so no button worked), list decisions never removed a card,
no work queue existed and Print ended in a dead-end status. Those are fixed and
covered by tests; the browser flow was walked end to end.

| Operator requirement | Status | Evidence |
|---|---|---|
| Live decision queue from Log4OM UDP | **done** | `internal/udplistener` -> `qualify` -> `queue_changed` -> `static/live.js` |
| Research next to the decision (QRZ preferences, bio lines, history, cards sent/received) | **done** | `researchFor` + `research.html`; `Store.CallHistory` (base-call aware) |
| Decide Bureau / Direct / Via manager / No card / Written; decided card leaves the queue | **done** | `QueueDecide/Written/Decline` transitions; `TestListDecideLeavesQueue` |
| Work queue: decided cards, one at a time, Print or Written, next appears | **done** | `/work`, `/work/card`; `QueuePrinted` only after a successful print |
| Done view + undo (Back, Reopen) | **done** | `/done`, `QueueBack`, `QueueReopen` (warns if Clublog already has it) |
| Correct push-back (`QSL_SENT_VIA` / `QSL_VIA`) | **done** | `sync.PushBack`; the old `QSL_SENT_AS` is not an ADIF field |
| Suggestions that mislead less | **done** | `qsldetermine`: callsign-only manager, free-text keywords, QRZ 1/0 flags, negations |
| Compact decision window, tray launcher, background loop | **done** | unchanged from 2026-09-29 |

Remaining gaps (v2 / later):

- Envelope/label printing for direct cards; a "bureau parcel shipped" step;
  one card covering several QSOs with a station (the research panel shows the
  pending siblings).
- Received card -> automatic reply entry in the decision queue.
- Unverified against the real services: `putlogs.php` accepting
  `QSL_SENT_VIA`/`QSL_VIA`; whether a Clublog pull overwrites NAME/QTH/NOTES
  that arrived via UDP.
- Windows: tray and SumatraPDF paths were not exercised in the rebuild;
  macOS `lpstat -d` is parsed with an English-only string (a localized
  system falls back to `lp -d ''`).
- Windows exe icon/version embedding: config ready in `winres/winres.json`;
  the one-step `go run github.com/tc-hib/go-winres@latest make` is left to
  the operator.
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
- **2026-09-30 — Two queues, one lifecycle.** Decision queue (`queued`) ->
  work queue (`decided`) -> done (`sent`/`skipped`). A decision removes the
  card from the decision queue immediately; producing a card (Print/Written)
  happens in the work queue. Replaces "decide and produce on the same row".
- **2026-09-30 — "Written" is its own outcome.** A card filled in on the spot
  is done: status `sent`, `desired_method=W`, `QSL_SENT=Y` without a route.
- **2026-09-30 — Sent = card done.** Print success or Written marks the card
  sent (and pending for push-back); no separate "mailed" step.
- **2026-09-30 — Repeat contacts are shown, not filtered.** `first_contact_only`
  defaults off; repeat contacts arrive with history badges. A `qualify.since`
  cutoff (default: first run) keeps the first Clublog pull from flooding the
  queue.
- **2026-09-30 — Push-back speaks ADIF.** `QSL_SENT_VIA` for bureau/direct,
  `QSL_VIA` for a manager (ADIF: `QSL_SENT_VIA=M` is import-only).
- **2026-09-30 — Every card move is one guarded transition** (transaction +
  `ErrConflict`), so stale pages and second windows cannot double-act.

