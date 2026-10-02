# qslotter — consolidated vision

Consolidated product vision and requirements for qslotter. This reconciles the
original v1 scope (see README.md) with the operator-workflow requirements added
September 2026. Where README and this document disagree, this document wins.

Big Idea:
qslotter is scratching the itch of DL9ET. He likes paper QSLs bt found that the handling of paper QSL in his favorite logger is way to cumbersome for his needs. Other loggers may have better QSL handling but then they have other downsides. The realisation is that his dream log does not exist. The idea behind qslotter is to just do the QSL cards handling o in a very streamlined way. qslotter leaves all digital QSL handling to the log (or other solutions), it is only concerned with paper QSL handling.

In order to perform this, qslotter needs to be aware of what is being logged.  This happens in two ways: Realtime through the logs broadcast and consolidation via clublog- Clublog because it is supported by most loggers.


## 1. Product thesis

qslotter is a local QSL workbench. It mirrors the log in real time (Log4OM UDP
+ Clublog reconciliation), lets the operator decide which QSOs deserve a paper card (Inbox: yes / no /
written now), then *how* each card goes out at the Desk (bureau, direct, via
manager direct or bureau - or the station's card is requested via OQRS
instead), treats printing and handwriting as equal fulfillment paths, and
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
  `QSL_SENT=Y` with `QSL_SENT_VIA` (bureau/direct) and `QSL_VIA` (manager),
  plus `QSL_RCVD`, push back via `putlogs.php`.
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
  processing.** Suggestion done: `qsldetermine` suggests with confidence and
  reason. Since 2026-10-01 it informs yes/no in the Inbox and preselects the
  route at the Desk (B4, partial). "Other
  sources" beyond QRZ (LLM bio interpretation) sits behind the `qpc` eval
  gate (`cmd/qpc-lab`, roadmap v2).
- **Synchronous and asynchronous mode** — prepare cards at the desk after
  each QSO, or in a batch later. Done in the 2026-09-30 build, reshaped
  2026-10-01: synchronous = written now in the Inbox (A3, bureau or direct);
  asynchronous = the Desk, card by card or batch Print (B1, B5).
- **Print QSO data onto the card via a configurable template.** Done: YAML
  templates with millimetre coordinates; DX name and QTH included.
- **Hand-written QSL cards supported**, usually synchronously. Partly done:
  Written at the Desk records the decided route and shows the data to copy;
  written now in the Inbox is still route-less (A3: it must record bureau or
  direct).
- **Electronic QSL is out of scope** — left to the logbook. Enforced since
  2026-09-30: E is no longer a decision; eQSL-only stations resolve to
  "no card".
- **Receiving: quick keyboard entry of received cards.** Done 2026-10-01:
  callsign, Enter, tick, Enter; the reply question and answering right there,
  and cards requested via OQRS as expected (section 2.3).
- **Optional photo capture of received cards with data extraction.** Not
  built (roadmap v2).

## 2. The three work areas (operator walkthrough, 2026-10-01)

The operator's own walkthrough of how QSL handling should feel (dictated;
"proposal" marks additions that are not from the walkthrough). Three areas,
which are also the top-level menu: **Eingang / Inbox** (qualify QSOs),
**Schreibtisch / Desk** (write or print the cards), **Posteingang / Incoming
QSLs** (book received cards, answer them). The UI is multilingual (D3); this
document uses the English names.

Electronic confirmations need no decision at all - the logbook handles them;
every question here is about the *paper* card: whether, and how.

Status column: state of the build deployed 2026-09-30 (`done` / `partial` /
`missing` / `open` = not decided yet).

### 2.1 Inbox - qualify QSOs

The purpose of this queue is to qualify QSOs: decide *whether* a paper card
goes out. *How* it goes out is decided at the Desk (operator, 2026-10-01).
While operating it is a glance-and-tap surface; in deliberation mode (sitting
down to it) it is a master-detail view.

| # | Requirement | Status |
|---|---|---|
| A1 | Holds the QSOs not qualified yet, from the cutoff (`qualify.since`, default: the day qslotter first ran) on, live from Log4OM. Reverse chronological: newest on top, older below. The **backlog** before the cutoff is **discarded** once: every QSO before the cutoff still waiting for a decision is filed as "no card" (note "backlog", reopenable from Done); older QSOs never enter. Only new QSOs count. | done (2026-10-01): cutoff, newest-first, and the one-time backlog discard at startup (`qualify.DiscardBacklog`: once per cutoff, override-marker QSOs stay, a reopened card is not discarded again); rows re-entering live land on top until reload |
| A1b | Ideally also the QSO *in progress* ("eventuell", "idealerweise"), so a card filled in during a rag-chew can be booked at once. **Source: the logger's "current contact" broadcast over UDP**, in two accepted forms: (1) the **bare callsign** as plain text (Log4OM's CALLSIGN service, e.g. `VU2ATN`), (2) the **standard N1MM `<lookupinfo>` XML** (N1MM-family loggers). The UDP listener tells the datagrams apart: ADIF = QSO saved, plain callsign or `<lookupinfo>` = current contact; other N1MM XML is ignored. The Go decoder in `stationa/logger-spot-bridge` handles both forms and can be reused. Shape: the Inbox shows the current contact on top with the research panel (QRZ, history); "written now" is remembered for that call and applied to the next logged QSO with it (the QSL key - date, time, band - exists only once the QSO is saved). | done (2026-10-01): the UDP listener tells datagrams apart (package `contact`: ADIF = QSO saved; bare callsign or `<lookupinfo>` = current contact; empty = cleared; other N1MM XML ignored); the Inbox (master-detail and compact) shows "QSO in progress" on top with the research panel (compact: the badges) and "card written during the QSO: bureau / direct" (undo); the card is booked on the QSO when it is logged (UDP or Clublog pull; also a QSO the qualifier would not queue), the box reports it. Pending cards live in memory (12 h; lost on a restart) |
| A2 | Inbox decision: **yes, card** (goes to the Desk, no route yet), **no card**, or **written now**. | done (2026-10-01): `/queue/yes` -> Desk with the route open |
| A3 | **Written now**: the card was filled in during a rag-chew QSO - "it's done, I never want to see it again". It records its route: **bureau** or **direct** (no manager, no "other"); supersedes the 2026-09-30 decision "Written is its own outcome". | done (2026-10-01): bureau or direct, `note=written now`; `w` then `b`/`d` on the card view (two keys, so a slip cannot mark a card sent) |
| A4 | Whatever the decision, the QSO leaves the view at once and the next one is offered. | done |
| A5 | **Compact list** for operating, e.g. mid-pileup, little screen space: **yes / no** only ("ja nein ja nein"). | done (2026-10-01) |
| A6 | **Master-detail** for deliberation: the list and the selected QSO's details *in one view*. The list only selects (arrow keys); decisions are made in the detail pane only (buttons/keys: yes / no / written now via bureau / direct), after which the selection moves to the next QSO below. | done (2026-10-01): `/queue` is the master-detail view - the list (click / up-down arrows) only selects, the detail pane is the decide card; after a decision the QSO below follows (also on the card-by-card `/decide`); live: new QSOs appear on top, a QSO handled in another window hands the selection to the one that took its place |
| A7 | Detail content: QRZ data (preferred route - direct or bureau -, manager, address), the QSO data, the history: worked before? sent a card before? received one? | done on `/decide` (research panel), with limits: history covers the 12 newest QSOs with the station; the shown QSO's own received state is not displayed. Shown in the `/queue` master-detail pane since 2026-10-01 |

### 2.2 Desk - the work queue

Cards with "yes, card". Typically worked through in the evening, away from the
radio. Here the route is chosen.

| # | Requirement | Status |
|---|---|---|
| B1 | Work through card by card; when one is done the next appears without paging. | done (`/work/card`) |
| B2 | Master-detail like the Inbox: the list on one side (proposal: grouped by the QRZ-suggested route), the card on the other; finishing a card moves to the next. The card-by-card view stays. | done (2026-10-01): `/work` is the master-detail view - cards grouped by the route offered first (route open, direct, via manager, bureau), the selected card in the detail pane, finishing moves to the card below; batch actions on ticked cards use the route shown; the card-by-card view `/work/card` stays |
| B3 | The detail holds *everything needed to write the card*: QSO data, QRZ data and indicators (wants paper?), history (worked before, cards exchanged before), the address for the chosen route (station, or manager). | done (2026-10-01): the work card shows every QSO on the card (freq, RST sent/rcvd, notes), the research panel (QRZ, indicators, history without the card's own QSOs), the station address for direct and the manager's QRZ address for the manager routes (refreshed as the call is typed; live update when the lookup lands) |
| B4 | The route is chosen **when finishing the card**: **bureau**, **direct**, **via manager (direct)**, **via manager (bureau)** - the QRZ suggestion preselected. Finish with **written** or **printed**; both record the chosen route. | done (2026-10-01): bureau / direct / via manager direct / via manager bureau, preselected from a route recorded earlier, else the QRZ suggestion; Print and Written record it; manager cards print "via <manager>" |
| B4b | **Requested (OQRS)** instead of sending: some stations want no card from me, but their card can be ordered - via OQRS or another way (e.g. money via PayPal). No own card goes out; their card is requested. A button next to written / printed / no card, with the **channel** (OQRS, PayPal, e-mail, other) and a free-text **note** (amount, date, reference). QRZ mentioning OQRS (manager field, bio) marks the button as suggestion. | done (2026-10-01): "Requested..." (`r`) with channel OQRS / PayPal / e-mail / other and a note; status `requested`, `QSL_RCVD=R` pushed, `QSL_SENT` untouched; QRZ mentioning OQRS (manager field or bio) marks the button and the list row. Listed as *expected* on Incoming QSLs (C5) |
| B5 | **Print** the back of the card from the template for bulk sessions (say 50 cards); hand-writing stays for synchronous or special cards. | done: card by card (`p` on `/work/card`) or batch Print of ticked rows on `/work`; one print job per card, no select-all |
| B6 | Change of mind: **no card** after all - a button next to written / printed (the card goes to Done, reopenable). | done (2026-10-01): "No card" (`n`) on the work card and the list, for the whole card |
| B7 | A finished card leaves the queue for good. Written or printed means **sent** - no separate "mailed" step (confirms 2026-09-30). | done |
| B9 | **One card for several QSOs** with the same station (e.g. different bands): open QSOs of that call are combined into one card - printed with one row per QSO, up to what the template holds; "written"/"printed" finishes them all. Proposal: "same station" = the same worked callsign (a `/P` operation is a separate card). | done (2026-10-01): a Desk card = all open QSOs with the same worked callsign (/P is a separate card); each QSO can be unticked (stays at the Desk); printed one row per QSO (default template: 3 rows, further cards when more); Print / Written / Requested / No card / Back move all ticked QSOs in one transaction; the Desk badge counts cards |

### 2.3 Incoming QSLs

| # | Requirement | Status |
|---|---|---|
| C1 | Enter the DX callsign of a received card, pick the QSO(s) it confirms, book the card received - with as few key presses as possible. | done (2026-10-01): `/receive` - callsign, Enter; the station's QSOs base-call aware (portable calls found both ways), up to 50; tick the QSOs the card confirms (preselected: the requested ones, else the only open one), Enter books them all |
| C2 | Show whether I already sent a card - i.e. whether this card **needs a reply**. | done (2026-10-01): per QSO "your card": sent (date, route; local state included), at the Desk, in the Inbox, no card decided, requested; after booking a verdict per QSO - reply due, or why not |
| C3 | **Answer** or **don't answer**. Answering shows the same data as at the Desk (the QSO the card is about, date, time, QRZ data) with three ways: **written now** (route chosen, "Büro-Karte geschrieben, fertig"), **print** right there, or **later** (the reply goes to the Desk as "yes, card"). | done (2026-10-01): reply panel for the QSOs that need one (one card): one panel per worked callsign (a /P call is its own card), taking along that call's QSOs already at the Desk; the QSO data (RST, freq, notes), the research panel (refreshed live when QRZ data lands) and the manager's address; route b/d/m/v (QRZ suggestion preselected); written now (w), print (p), later via the Desk (l, as "yes, card"), no reply (x) - each pressed twice (or key, Enter), so typing the next callsign cannot answer; a reply overrules an earlier "no card" (also the backlog) and creates a queue item for a QSO that was never queued; a booked QSO with an open reply stays answerable from the lookup |
| C4 | Or just record the card as received, so the status is known. | done |
| C5 | Cards **requested** via OQRS (B4b) are listed as *expected*. Booking such a card shows "requested on ... via ..., no reply needed" instead of the reply question. Requests still open after **12 weeks** (configurable) are marked overdue there - a marker only, no mail. | done (2026-10-01): "Expected cards" on `/receive`, one row per request (not arrived; oldest first; refreshed with each booking), overdue after `receive.overdue_weeks` (default 12) - a marker only; booking such a card says "requested ... - no reply needed" for every QSO it confirms and pushes `QSL_RCVD=Y` |

### 2.4 Navigation

| # | Requirement | Status |
|---|---|---|
| D1 | The three areas are the top-level menu. Log, Done and Settings are secondary. | done (2026-10-01): Eingang / Schreibtisch / Posteingang (Inbox / Desk / Incoming QSLs) with counters (Inbox QSOs, Desk cards, expected cards); Log, Done, Settings and "to push" small on the right; card-by-card and compact views are linked from their pages |
| D3 | **Multilingual UI**: German and English, more languages possible (translation files, no hard-coded strings in templates). German names: Eingang / Schreibtisch / Posteingang; English: Inbox / Desk / Incoming QSLs. Proposal: the language is chosen under Settings, defaulting to the browser language. | done (2026-10-01): German and English; catalogs `internal/i18n/locales/<lang>/*.json` keyed by the English text (a missing translation falls back to English; a new language = a new directory); Settings -> Language: automatic (browser language - in the app window the system language), Deutsch, English (`ui.language`); the tray menu follows it, else the system language; a test renders every page and flow in German and fails on any missing text. Still English: the QRZ-derived reason text and low-level errors (printer, database, Clublog) |
| D2 | No separate start page: `/` opens the Inbox; the counters in the menu (Inbox, Desk, pending push) are the overview. Statistics later, if at all. | done (2026-10-01): `/` opens the Inbox |

### 2.5 Stage 2 - AI support (after the forms and flows above work)

| # | Requirement | Status |
|---|---|---|
| E1 | Phone app (the operator's idea): photograph the back of an incoming card and process it directly, with as few key presses as possible. What is extracted (proposal: call/date/band/mode) and how it is booked is open. | roadmap v2 |
| E2 | Explore ("vielleicht", "interesting how this could be done"): whether a simple, cheap language model can read QRZ free text - e.g. "please QSL via ..." in the manager field - to help the route decision. How, and at what cost, is open. | heuristic improved 2026-09-30; LLM experiment `qpc` + `cmd/qpc-lab` started 2026-10-02, stays behind its gate (roadmap v2) |

### 2.6 Invariants carried over

- Live: a QSO logged in Log4OM reaches the Inbox within ~1 s (UDP; Clublog
  pull as backstop); deciding works mid-pileup - one small window, no Go-side
  full-log loads on the hot path. (SQLite still scans `qsos` for the call
  history and the push badge; worth an index when the log grows.)
- One card lifecycle: Inbox (`queued`) -> Desk (`decided`) -> done
  (`sent` / `skipped` / requested); every move is one guarded transition;
  Back and Reopen undo misclicks.
- Push to Clublog on the operator's schedule: `QSL_SENT=Y` with
  `QSL_SENT_VIA` = B or D for every sent card, plus `QSL_VIA` = the manager's
  call for the two manager routes (ADIF: `QSL_SENT_VIA=M` is import-only).
  A requested card pushes `QSL_RCVD=R` ("the logging station has requested a
  QSL card") and leaves `QSL_SENT=N`; when it arrives, `QSL_RCVD=Y`.

## 3. Decision model: suggestion ≠ decision

- The heuristic ladder in `internal/qsldetermine` (QRZ structured fields + bio
  regexes) produces a *suggestion*: method, manager, refuse-paper, confidence,
  reason.
- The suggestion is displayed, never silently acted on.
- Two decisions: the Inbox decision (yes / no card / written now) is recorded
  on the queue item and survives queue recompute; the route (bureau, direct,
  via manager direct, via manager bureau) - or "requested (OQRS)" - is
  recorded when the card is finished at the Desk (B4, B4b), with the QRZ
  suggestion preselected.
- "No card" is a first-class decision, in the Inbox (A2) and, as a change of
  mind, at the Desk (B6). It means *no paper card* for any reason
  (electronic-only, refused, or the operator's choice), not a silent no-op:
  `desired_method=N`, status `skipped`, reopenable from Done.
- LLM-based determination (`qpc`: classifier library, backend `cmd/qpcd`,
  eval harness `cmd/qpc-lab`; any OpenAI-compatible endpoint, local Ollama)
  stays an offline calibration effort. If it beats the heuristic on the
  operator-labelled sample it may become a second suggestion source; it does
  not enter the app before that gate. Labels: a route (bureau, direct, oqrs,
  unclear, no-paper, unknown) plus a separate via callsign (manager or home
  call); rules in `qpc/LABELS.md`.

## 4. Fulfillment: print or handwrite

Two parallel paths, equal citizens:

- **Print:** PDF via the YAML template → printer (existing path).
- **Handwritten:** the operator writes the card; the UI's job is to put the
  right data in front of them — their name and address, date, call, band,
  mode, RST, my call, manager if via-manager — and record the outcome.

Both end at queue status `sent`, and both push back the correct route to
Clublog (retired: the old "Mark Sent" that always uploaded bureau). A card
written on the spot (during the QSO) records its route: bureau or direct (A3;
replaces the 2026-09-30 decision "Written is its own outcome"). For every
other card the route is chosen when it is written or printed: at the Desk
(B4), or on Incoming QSLs for a reply (C3). A third outcome at the Desk sends
no card at all but requests the station's card (OQRS, B4b).

## 5. UI strategy: one web UI, two modes

Top-level menu = the three work areas of section 2: **Eingang / Inbox**,
**Schreibtisch / Desk**, **Posteingang / Incoming QSLs** (D1, multilingual
D3). The Inbox comes in two presentations: a
compact list for operating and a master-detail view (list + selected QSO's
details in one view) for deliberation (A5/A6). The Desk gets the same
master-detail view, plus its card-by-card view (B2).

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
- Row actions: **yes / no** only (A5). Written now (bureau or direct, A3) is
  in the master-detail view. For every other card the route and Print/Written
  belong to the Desk (B4).
- Built for glance-ability during operation.

### Expanded mode — the deliberation surface (master-detail)

- One view: the queue list on one side (it only selects), the selected QSO's
  details on the other; deciding in the detail pane moves the selection to the
  next card down (A6). Same pattern for the Desk (B2). Built 2026-10-01
  (`/queue`, `/work`); the card-by-card pages `/decide`, `/work/card` stay.
- The station page grown up: full QRZ picture — `qslmgr`, eqsl/mqsl/lotw
  flags, email, address, bio text — and the suggestion with confidence and
  reason.
- Decision controls: yes / no / written now via bureau or direct (A6), plus
  context: previous QSOs with this station, cards sent/received.
- This is where the hard *whether* cases get decided (refuses paper, already
  confirmed, repeat contact). Route questions (manager changed, "direct only"
  buried in the bio, OQRS) are settled at the Desk when the card is finished
  (B3, B4, B4b).

### Desktop app: own window, tray, one binary per OS (2026-10-01)

- `internal/desktop` shows the web UI in a native WebView window via
  **glaze** (purego, no cgo: WKWebView / WebView2 / WebKitGTK). The window
  loads the loopback server by URL - never through a framework asset handler,
  which would buffer the SSE stream - so htmx/SSE run unchanged and the
  backend can be swapped (Wails v3 is the recorded escape hatch).
- Every target cross-compiles from the Mac with `CGO_ENABLED=0`
  (`scripts/release.sh`; `scripts/macapp.sh` for the `.app`).
- Tray via `crgimenes/native/tray` on macOS and Windows: open the main
  window, the compact window (480x640, its own second window), quit. Linux has
  no tray yet: closing the window quits. Target labels follow D1 (Inbox,
  Desk, Incoming QSLs).
- macOS: a regular app with its own Dock icon (an app delegate via
  purego/objc): a Dock click reopens the window, Quit from the Dock runs the
  normal shutdown.
- Closing a window keeps qslotter running (UDP feed); a second launch brings
  the window back (`native/singleinstance` + a cgo-free UI-thread
  dispatcher: message-only window on Windows, `dispatch_async_f` on macOS).
- Fallback without a WebView runtime: a Chromium-family browser as app
  window on its own profile (size passed only on the profile's first start),
  else the default browser. `-ui browser|headless`, `ui.mode`.
- Windows exe: GUI subsystem (no console), icon/version via go-winres, log
  file under `%LOCALAPPDATA%\qslotter`. First start without a config writes
  one to the user config dir and opens Settings; startup errors show a
  dialog.
- htmx is vendored to `/static` (`internal/web/static/htmx.min.js`), so the
  shack PC works offline.

## 6. Unchanged pillars

- Receive flow stays keyboard-driven.
- Push-back runs on `clublog.push_interval` (default off) in the background
  loop; the button stays for on-demand pushes.
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
| Research next to the Inbox decision (QRZ preferences, bio lines, history, cards sent/received) | **done** in the Inbox (`/queue` detail pane, `/decide`) and at the Desk (B3, 2026-10-01) | `researchFor` + `research.html`; `Store.CallHistory` (base-call aware) |
| Decision queue: a decided card leaves the queue at once | **done** (the 2026-09-30 route stamps B / D / M / Written in the queue were superseded 2026-10-01, see A2/A3/B4) | `QueueDecide/Written/Decline` transitions; `TestListDecideLeavesQueue` |
| Work queue: decided cards, one at a time, Print or Written, next appears | **done** | `/work`, `/work/card`; `QueuePrinted` only after a successful print |
| Done view + undo (Back, Reopen) | **done** | `/done`, `QueueBack`, `QueueReopen` (warns if Clublog already has it) |
| Correct push-back (`QSL_SENT_VIA` / `QSL_VIA`) | **done**: `QSL_SENT_VIA` = B/D for every card, plus `QSL_VIA` = the manager for manager routes (2026-10-01) | `sync.PushBack`; the old `QSL_SENT_AS` is not an ADIF field |
| Suggestions that mislead less | **done** | `qsldetermine`: callsign-only manager, free-text keywords, QRZ 1/0 flags, negations |
| Compact decision window, tray launcher, background loop | **done** (compact rows still carry B/D/M/-/Written; yes/no only is open, A5) | unchanged from 2026-09-29 |

Open after the operator walkthrough of 2026-10-01 (section 2, status
`partial`/`missing`):

- A7: research panel limits (12 newest QSOs; own received state). (B3/B4/
  B4b/B6/B9, the Desk: done 2026-10-01.)

Remaining gaps (v2 / later):

- Envelope/label printing for direct cards; (if wanted) a bureau-parcel log
  for bookkeeping only - it never changes card status, cards are `sent` when
  written or printed (B7);
  (one card for several QSOs is now B9).
- Clublog (checked 2026-10-01): pushes land - `QSLSDATE` and `QSL_RCVD=Y`
  come back - but the export never carries `QSL_SENT`, `QSL_SENT_VIA` or
  `QSL_VIA`, so the route cannot be read back; qslotter keeps it locally and
  treats a QSL sent date from the log as "card sent". Unverified:
  `QSL_RCVD=R`; whether a pull overwrites NAME/QTH/NOTES from UDP.
- Windows: tray and SumatraPDF paths were not exercised in the rebuild;
  macOS `lpstat -d` is parsed with an English-only string (a localized
  system falls back to `lp -d ''`).
- Windows exe icon/version embedding: config ready in `winres/winres.json`;
  the one-step `go run github.com/tc-hib/go-winres@latest make` is left to
  the operator.
- `qsl-eval`, `qsl-eval.jsonl`, and `ww` in the repo root are leftovers of
  the retired `qsl-eval` tool; calibration data now lives in the git-ignored
  `eval/` directory. None of it is a shipped artifact.
- Everything in section 8 marked v2.

## 8. Roadmap

**v1.x, v1.x+, v1.y — done 2026-09-29/30** (decision-first UI, tray shell,
background loop, batch actions; the two-queue rebuild of 2026-09-30):

1. `desired_method` end-to-end; push-back uploads the route. (The route
   chooser on queue rows of that build was superseded 2026-10-01: the route
   is chosen at the Desk, B4.)
2. Handwritten -> Sent action.
3. "No card" decision; recompute never resurrects it.
4. Compact mode: `?compact=1`, responsive CSS, vendored htmx.
5. Expanded station view: confidence + reason + decision controls.
6. Name and address for handwriting and the card template.
7. `internal/tray` (Windows), single-instance mutex, Edge `--app` launcher.
8. Background pull/push loop (buttons retained); batch actions.

**v1.z — the three work areas (section 2, stage 1: forms and flows):**

9. Inbox decides *whether*: yes (route-less, to the Desk) / no / written now
   via bureau or direct; compact rows yes/no; one-time backlog discard
   (A1, A2, A3, A5). **Done 2026-10-01**, together with the route choice at
   the Desk (B4 core) - without it a "yes" card could not get a route.
10. Master-detail views for Inbox and Desk - the list selects, the detail
    decides; compact list stays for operating (A6, B2). **Done 2026-10-01.**
11. Desk: route chosen when finishing (bureau, direct, manager direct, manager
    bureau; QRZ suggestion preselected), Print and Written use it; "no card"
    button; "requested (OQRS)" with channel and note; full research panel and
    the address for the chosen route; one card for several QSOs with a
    station (B3, B4, B4b, B6, B9). **Done 2026-10-01.**
12. Incoming QSLs: reply due? answer / don't answer - written now, print, or
    later via the Desk; requested cards listed as expected, overdue after 12
    weeks (C2, C3, C5); portable calls (C1). **Done 2026-10-01.**
13. Top menu = Eingang/Inbox, Schreibtisch/Desk, Posteingang/Incoming QSLs;
    `/` opens the Inbox; German and English UI (D1, D2, D3). **Done 2026-10-01.**
14. QSO in progress from Log4OM's call broadcast (A1b). **Done 2026-10-01.**

**v2 — platform (stage 2: AI support):**

15. CouchDB store backend + migration command.
16. Phone app: photograph incoming cards, OCR, book with minimal taps (E1).
17. LLM route suggestion from QRZ free text, at low cost, behind the eval
    gate (only if `qpc-lab` shows it beats the heuristic) (E2).

## 9. Decision log

- **2026-09-29 — No Flutter; web UI with two modes + tray shell.** One
  toolchain, one binary, browser windows cover the compact case.
- **2026-09-29 — Compact window via Chromium `--app` launch at fixed size,
  dedicated user-data-dir; in-page `resizeTo` as enhancement.** Pages cannot
  resize manually-opened tabs; Firefox gives no geometry control.
  (Superseded 2026-10-01 by the desktop app; the `--app` launch remains the
  fallback.)
- **2026-09-29 — Tray via `fyne.io/systray` behind `//go:build windows`.**
  (Superseded 2026-10-01: `crgimenes/native/tray` on macOS and Windows.)
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
- **2026-10-01 — Three work areas: Eingang / Schreibtisch / Posteingang**
  (English: Inbox / Desk / Incoming QSLs). They are the top-level menu; `/`
  opens the Inbox, no separate start page. Inbox and Desk get master-detail
  views (the list selects, the detail pane decides); the Inbox also keeps a
  compact list for operating.
- **2026-10-02 — UI wording review (EN + DE).** The first area is called
  **New QSOs / Neue QSOs** in the UI: an inbox holds mail that arrived, and
  that is Incoming QSLs / Posteingang (Eingang vs Posteingang collided in
  German). "Inbox" stays the name of the `queued` state in code and in these
  docs. Also: "route" is **Send via / Versand über** in the UI; "written now"
  is **Already written** (card filled in during the QSO) and **Written by
  hand - sent** at the Desk; "reply" is **your card due**; "book as received"
  is **Mark as received**; "No reply" is **Not now**; "Requested..." is
  **Request their card...**; Print / Written say they mark the card as sent.
  Stored values (`desired_method`, `note = "written now"`) are unchanged.
- **2026-10-01 — The Inbox decides *whether*, the Desk decides *how*.** Inbox:
  yes (route-less) / no / written now. The route (bureau, direct, via manager
  direct, via manager bureau) is chosen at the Desk when the card is written
  or printed, with the QRZ suggestion preselected. Supersedes the 2026-09-30
  route stamps in the decision queue. In the pileup (compact list): yes / no
  only.
- **2026-10-01 — Written now carries a route: bureau or direct.** Supersedes
  2026-09-30 "Written is its own outcome". No route "other".
- **2026-10-01 — "No card" also at the Desk.** A change of mind does not need
  a trip back to the Inbox.
- **2026-10-01 — Written or printed = sent, confirmed.** No separate "mailed"
  step (confirms 2026-09-30 "Sent = card done").
- **2026-10-01 — "Requested (OQRS)" is a third outcome at the Desk.** For
  stations that want no card but let you order theirs (OQRS, PayPal, ...): no
  own card goes out; channel and note are recorded; the card is listed as
  expected on Incoming QSLs; Clublog gets `QSL_RCVD=R`, `QSL_SENT` stays `N`.
- **2026-10-01 — Manager routes push both fields.** `QSL_SENT_VIA` = B or D
  (how the card travelled) plus `QSL_VIA` = the manager's call; ADIF marks
  `QSL_SENT_VIA=M` import-only.
- **2026-10-01 — Incoming card = reply question.** Booking a received card
  shows whether a card already went out and offers to answer right there.
- **2026-10-01 — AI is stage 2.** Phone scanning of incoming cards and a
  cheap language model for QRZ free text are explored after the forms and
  flows work.
- **2026-10-01 — The QSO in progress comes from the logger's "current
  contact" broadcast:** a bare callsign (Log4OM) or the standard N1MM
  `<lookupinfo>` XML - both accepted. The Inbox can show it before the QSO is
  logged; a "written now" is applied to the QSO once it is saved.
- **2026-10-01 — Backlog discarded.** QSOs before the cutoff that still wait
  for a decision are filed once as "no card" (reopenable); only new QSOs
  count. Supersedes "older unanswered QSOs, each qualified individually".
- **2026-10-01 — One card for several QSOs with a station.** Open QSOs of the
  same call are combined at the Desk; one "written"/"printed" finishes them.
- **2026-10-01 — Replying to an incoming card:** written now, print, or later
  via the Desk. OQRS requests are marked overdue after 12 weeks.
- **2026-10-01 — Multilingual UI** (German and English, extensible).
- **2026-10-01 — The Desk is card-centric.** A Desk card is every open QSO
  with one worked callsign; actions take the card's keys and move them in one
  transaction. "Requested" is its own status (`requested`, `desired_method=R`,
  `channel`, `note`, `sent_at` = when requested) pushing `QSL_RCVD=R`.
  The default card template holds 3 QSO rows; more continue on a second card.
- **2026-10-01 — Route data model.** `desired_method` = B / D / M (empty while
  a "yes" card waits at the Desk), `send_via` = how the card travelled (B/D,
  also for manager cards), `manager`; `note` marks "written now", "backlog",
  "sent elsewhere". `qsl_sent_method_local` holds B/D only (pushed as
  `QSL_SENT_VIA`); cards from older builds keep `M`/`W`. Desk keys: `b d m v`
  pick the route, `p`/`w` finish; Inbox "written now" is `w` then `b`/`d`.

- **2026-10-01 — Desktop app: own window via glaze, one binary per OS.** The
  browser was the fiddly part (start, window size, lost among tabs). A native
  rewrite (Fyne/Gio/...) was weighed (15-30 days, loses LAN access) and
  rejected; glaze (purego, no cgo) keeps the htmx UI and the cross-compile
  from the Mac. Verified on macOS and Windows 11; Linux built, not yet tested.
  Risks accepted: glaze is young (v0.0.x, one maintainer) and uses an
  undocumented WebView2 export - mitigated by the browser fallback and a
  backend that only loads a URL.
- **2026-10-02 — LLM route classifier `qpc` replaces `llmqsl`/`qsl-eval`.**
  The old tool never produced a result. `qpc` is a standalone package and
  backend (`cmd/qpcd`, OpenAI-compatible API) that imports nothing from
  `internal/`; `cmd/qpc-lab` samples real stations, takes blind manual labels
  and compares model x prompt variants with the heuristic. Labels are a route
  plus a separate via callsign; a manager with no stated route is `unclear`
  (the manager's own instructions are a later phase). First model choice:
  `qwen3.5:4b` (multilingual, JSON schema), thinking off.
