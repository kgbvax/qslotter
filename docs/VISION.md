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
  `first_contact_only`, `include_digital` (Settings checkbox, 2026-10-06);
  2026-10-08 the filter is a matrix under Settings > General (`qualify.ask`).
- **Override for memorable QSOs, controlled via notes.** Done: the `QSL!`
  marker (`qualify.override_marker`) force-includes a QSO.
- **Smart method determination from QRZ and other sources — none, "no paper
  please", direct, bureau, manager — accurate enough for semi-automatic
  processing.** Suggestion done: `qsldetermine` reads what QRZ *states*
  (signals with their quoted words) and suggests only from that (2026-10-02).
  It informs yes/no in the Inbox and preselects the route at the Desk when QRZ
  states one (B4, partial). "Other
  sources" beyond QRZ (LLM bio interpretation): the `qpc` classifier beat
  the old heuristic but trails the heuristic as improved 2026-10-02..03
  (decision log); its own value is the note (preferences, conditions,
  requirements) and free-text qslmgr fields. Not integrated (roadmap v2).
- **Synchronous and asynchronous mode** — prepare cards at the desk after
  each QSO, or in a batch later. Done in the 2026-09-30 build, reshaped
  2026-10-01: synchronous = written now in the Inbox (A3, bureau or direct);
  asynchronous = the Desk, card by card or batch Print (B1, B5).
- **Print QSO data onto the card via a configurable template.** Done: YAML
  templates with millimetre coordinates; DX name and QTH included. Since
  2026-10-04 configured visually (Settings > Cards & printer, see section 4).
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
| A1b | Ideally also the QSO *in progress* ("eventuell", "idealerweise"), so a card filled in during a rag-chew can be booked at once. **Source: the logger's "current contact" broadcast over UDP**, in two accepted forms: (1) the **bare callsign** as plain text (Log4OM's CALLSIGN service, e.g. `VU2ATN`), (2) the **standard N1MM `<lookupinfo>` XML** (N1MM-family loggers). The UDP listener tells the datagrams apart: ADIF = QSO saved, plain callsign or `<lookupinfo>` = current contact; other N1MM XML is ignored. The Go decoder in `stationa/logger-spot-bridge` handles both forms and can be reused. Shape: the Inbox shows the current contact on top with the research panel (QRZ, history); "written now" is remembered for that call and applied to the next logged QSO with it (the QSL key - date, time, band - exists only once the QSO is saved). | done (2026-10-01): the UDP listener tells datagrams apart (package `contact`: ADIF = QSO saved; bare callsign or `<lookupinfo>` = current contact; empty = cleared; other N1MM XML ignored); the Inbox (master-detail and compact) shows "QSO in progress" on top with the research panel (compact: the badges) and "card written during the QSO: bureau / direct" (undo); the card is booked on the QSO when it is logged (UDP or Clublog pull; also a QSO the qualifier would not queue), the box reports it. Pending cards live in memory (12 h; lost on a restart); 2026-10-02: the box also takes the **card / no card** decision (yes goes to the Desk with the route open, no files "no card"), booked when the QSO is logged like a written card. The call may never be logged: nothing is stored until it is, the decision stays listed with Undo and is forgotten after 12 h; a "no" creates the queue item on logging so the Clublog-pull qualifier cannot resurrect the QSO in the Inbox |
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
| B2 | Master-detail like the Inbox: the list on one side (proposal: grouped by the QRZ-suggested route), the card on the other; finishing a card moves to the next. The card-by-card view stays. | done (2026-10-01): `/work` is the master-detail view - cards grouped by the route offered first (route open, direct, via manager, bureau), the selected card in the detail pane, finishing moves to the card below; no tick boxes or batch actions since 2026-10-06 (the print queue collects the cards for one print run); the card-by-card view `/work/card` stays |
| B3 | The detail holds *everything needed to write the card*: QSO data, QRZ data and indicators (wants paper?), history (worked before, cards exchanged before), the address for the chosen route (station, or manager). | done (2026-10-01): the work card shows every QSO on the card (freq, RST sent/rcvd, notes), the research panel (QRZ, indicators, history without the card's own QSOs), the station address for direct and the manager's QRZ address for the manager routes (refreshed as the call is typed; live update when the lookup lands) |
| B4 | The route is chosen **when finishing the card**: **bureau**, **direct**, **via manager (direct)**, **via manager (bureau)** - the QRZ suggestion preselected. Finish with **written** or **printed**; both record the chosen route. | done (2026-10-01): bureau / direct / via manager direct / via manager bureau, preselected from a route recorded earlier, else the QRZ suggestion; Written and To print record it; manager cards print "via <manager>" |
| B4b | **Requested (OQRS)** instead of sending: some stations want no card from me, but their card can be ordered - via OQRS or another way (e.g. money via PayPal). No own card goes out; their card is requested. A button next to written / printed / no card, with the **channel** (OQRS, PayPal, e-mail, other) and a free-text **note** (amount, date, reference). QRZ mentioning OQRS (manager field, bio) marks the button as suggestion. | done (2026-10-01): "Requested..." (`r`) with channel OQRS / PayPal / e-mail / other and a note; status `requested`, `QSL_RCVD=R` pushed, `QSL_SENT` untouched; QRZ mentioning OQRS (manager field or bio) marks the button and the list row. Listed as *expected* on Incoming QSLs (C5) |
| B5 | **Print** the back of the card from the template for bulk sessions (say 50 cards); hand-writing stays for synchronous or special cards. **Two-step (operator, 2026-10-05):** a card is *sent to printing* (with a note for the card, unless the log has one) into a print queue; printing the queue sends one job; the run is confirmed as a whole, or single cards are reprinted. | done (2026-10-05): `p` ("To print") puts the card into the print queue (`toprint`; since 2026-10-06 its own Desk view `/work/printq`, third button in the band with a live count - on top of `/work` it pushed list and card below the fold) with its route and note (prefilled from ADIF `QSLMSG`, printed in the template field `qslmsg`); **Print N cards** renders all of them into one PDF, one printer job (`printing`); **All fine - sent** confirms the run (only then `sent` and pending push-back), **Print ticked again** reprints single cards, **Ticked back to the print queue** / **back to the Desk** for a correction; one open run at a time; a failed job puts the run back |
| B6 | Change of mind: **no card** after all - a button next to written / printed (the card goes to Done, reopenable). | done (2026-10-01): "No card" (`n`) on the work card and the list, for the whole card |
| B7 | A finished card leaves the queue for good. Written or printed means **sent** - no separate "mailed" step (confirms 2026-09-30). | done; printed = the print run confirmed (2026-10-05) |
| B9 | **One card for several QSOs** with the same station (e.g. different bands): open QSOs of that call are combined into one card - printed with one row per QSO, up to what the template holds; "written"/"printed" finishes them all. Proposal: "same station" = the same worked callsign (a `/P` operation is a separate card). | done (2026-10-01): a Desk card = all open QSOs with the same worked callsign (/P is a separate card); each QSO can be unticked (stays at the Desk); printed one row per QSO (default template: 3 rows, further cards when more); Print / Written / Requested / No card / Back move all ticked QSOs in one transaction; the Desk badge counts cards |

### 2.3 Incoming QSLs

| # | Requirement | Status |
|---|---|---|
| C1 | Enter the DX callsign of a received card, pick the QSO(s) it confirms, book the card received - with as few key presses as possible. | done (2026-10-01): `/receive` - callsign, Enter; the station's QSOs base-call aware (portable calls found both ways), up to 50; tick the QSOs the card confirms (preselected: the requested ones, else the only open one), Enter books them all |
| C2 | Show whether I already sent a card - i.e. whether this card **needs a reply**. | done (2026-10-01): per QSO "your card": sent (date, route; local state included), at the Desk, in the Inbox, no card decided, requested; after booking a verdict per QSO - reply due, or why not |
| C3 | **Answer** or **don't answer**. Answering shows the same data as at the Desk (the QSO the card is about, date, time, QRZ data) with three ways: **written now** (route chosen, "Büro-Karte geschrieben, fertig"), **print** right there, or **later** (the reply goes to the Desk as "yes, card"). | done (2026-10-01): reply panel for the QSOs that need one (one card): one panel per worked callsign (a /P call is its own card), taking along that call's QSOs already at the Desk; the QSO data (RST, freq, notes), the research panel (refreshed live when QRZ data lands) and the manager's address; route b/d/m/v (QRZ suggestion preselected); written now (w), to the print queue (p, with a card note; 2026-10-05), later via the Desk (l, as "yes, card"), no reply (x) - each pressed twice (or key, Enter), so typing the next callsign cannot answer; a reply overrules an earlier "no card" (also the backlog) and creates a queue item for a QSO that was never queued; a booked QSO with an open reply stays answerable from the lookup |
| C4 | Or just record the card as received, so the status is known. | done |
| C5 | Cards **requested** via OQRS (B4b) are listed as *expected*. Booking such a card shows "requested on ... via ..., no reply needed" instead of the reply question. Requests still open after **12 weeks** (configurable) are marked overdue there - a marker only, no mail. | done (2026-10-01): "Expected cards" on `/receive`, one row per request (not arrived; oldest first; refreshed with each booking), overdue after `receive.overdue_weeks` (default 12) - a marker only; booking such a card says "requested ... - no reply needed" for every QSO it confirms and pushes `QSL_RCVD=Y`; "Your card to the Desk" still sends yours (2026-10-05); Done shows the request as received |

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
| E2 | Explore ("vielleicht", "interesting how this could be done"): whether a simple, cheap language model can read QRZ free text - e.g. "please QSL via ..." in the manager field - to help the route decision. How, and at what cost, is open. | heuristic improved 2026-09-30 and 2026-10-02 (address-only rule); LLM classifier `qpc` (qwen3.5:4b) beat the old heuristic on a fresh blind sample (59% vs 48% fully right) but ties the improved one (71% vs 72%); its distinct value is the note and free-text qslmgr; integration open (roadmap v2); Jev-style decision models tried 2026-10-09 and stopped (60-65% at best, no signal as a second opinion - decision log) |
| E3 | **Future (operator, 2026-10-02):** once the QSL analysis ("qpc") is integrated, the "QSO in progress" box also **summarizes that information** for the call in progress, so the card / no card decision can be made from the box. What the analysis delivers and how it is condensed is open until it exists. | roadmap v2 |

### 2.6 Invariants carried over

- Live: a QSO logged in Log4OM reaches the Inbox within ~1 s (UDP; Clublog
  pull as backstop); deciding works mid-pileup - one small window, no Go-side
  full-log loads on the hot path. (SQLite still scans `qsos` for the call
  history and the push badge; worth an index when the log grows.)
- One card lifecycle: Inbox (`queued`) -> Desk (`decided`, print queue
  `toprint` -> `printing`) -> done (`sent` / `skipped` / `requested`); every
  move is one guarded transition; Back and Reopen undo misclicks. All paths:
  `docs/STATES.md`.
- Push to Clublog on the operator's schedule: `QSL_SENT=Y` with
  `QSL_SENT_VIA` = B or D for every sent card, plus `QSL_VIA` = the manager's
  call for the two manager routes (ADIF: `QSL_SENT_VIA=M` is import-only).
  A requested card pushes `QSL_RCVD=R` ("the logging station has requested a
  QSL card") and leaves `QSL_SENT=N`; when it arrives, `QSL_RCVD=Y`.

## 3. Decision model: suggestion ≠ decision

- `internal/qsldetermine` classifies the QRZ record by the operator's
  labelling rules (`qpc/LABELS.md` on the qpc branch, 2026-10-02/03): status
  (paper / no-paper / unclear / unknown), every accepted route (bureau,
  direct, OQRS), the preferred one, a via callsign and a contribution flag.
  What qslmgr and the bio's card sentences state decides; when they state
  nothing, mQSL and the postal address do (full address = direct, mQSL 1 =
  bureau, mQSL 0 = no paper); direct needs a full address. Shown as chips
  with the classifier's reason in the Inbox, the QSO in progress and on the
  Desk card. It replaced the "never guess" `Assess` (2026-10-04): on the
  operator's 350 labelled stations it is right on 314 (status, routes, via),
  `Assess` on 149 (silent on 150).
- The suggestion is displayed, never silently acted on. The Desk preselects
  the suggested route (the preferred one when stated); unclear, unknown and
  OQRS-only preselect nothing.
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
  beat the heuristic on a fresh, operator-labelled sample, until the
  operator's address-only rule went into the heuristic as well; since then
  both get about 72% of stations fully right, failing on different ones
  (decision log). Its distinct value is the note (preferences, conditions,
  requirements) and free-text qslmgr fields; whether that is worth a second
  suggestion source - shown with its evidence quote, never acted on - is
  open. Not wired into the app. Answer: status (paper, no-paper, unknown,
  unclear), every accepted route (bureau, direct, oqrs), the preferred route,
  a via callsign (manager or home call) and an English note; rules in
  `qpc/LABELS.md`.

## 4. Fulfillment: print or handwrite

Two parallel paths, equal citizens:

- **Print:** the card goes into the print queue (route + note); a print run
  renders every queued card into one PDF via the YAML template and sends one
  job; the run is confirmed (or cards reprinted) before the cards are sent.
- **Card layout:** the
  layout is set visually under **Settings > Cards & printer** (2026-10-04): the
  fields are dragged onto a scan of the pre-printed card (shown, never
  printed); the card is drawn from the printer's own layout code, so
  shrink-to-fit, overlaps and off-card elements show before printing. Several
  layouts live in `cards/` next to the config, one active (`card.template`).
  Fields: data fields (incl. satellite name / RX frequency per QSO row and
  the operator's QTH), fixed text, lines and boxes, bold. A test card prints
  a millimetre ruler; the measured feed error goes into `printer.offset_mm`,
  which shifts every print.
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
- The research panel (it superseded the old station page, removed
  2026-10-02): full QRZ picture — `qslmgr`, eqsl/mqsl/lotw flags, email,
  address, bio text — the signals QRZ states, with the suggestion and its
  quoted reason, and a Refresh that looks the station up on QRZ again.
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
- Card templates stay YAML with millimetre coordinates (the layout editor
  writes them; hand-editing still works).

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
| Suggestions that mislead less | **done** (redesigned 2026-10-02) | `qsldetermine.Assess`: stated evidence only, signals with quotes, computed on read, golden corpus of real QRZ records |
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
- Windows: tray and SumatraPDF paths were not exercised in the rebuild.
  (macOS: the default printer is found in any system language since
  2026-10-04 - `lpstat -d` is matched against the bare names of `lpstat -e`.)
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
14a. Visual card layout editor: several layouts, a scan of the card behind
    the fields, live preview from the printer's layout code, test card with
    ruler and printer offset; bold, lines/boxes, satellite row fields.
    **Done 2026-10-04.**

**v2 — platform (stage 2: AI support):**

15. CouchDB store backend + migration command.
16. Phone app: photograph incoming cards, OCR, book with minimal taps (E1).
17. LLM route suggestion from QRZ free text, at low cost, behind the eval
    gate (only if `qpc-lab` shows it beats the heuristic) (E2). 2026-10-02:
    beat the old heuristic, ties the improved one; open whether its notes
    and free-text reading justify integration (qwen3.5:4b via `qpcd`).

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
- **2026-09-30 — "Written" is its own outcome.** *(Superseded 2026-10-01:
  written now carries a route, bureau or direct.)* A card filled in on the spot
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
- **2026-10-02 — QSL suggestion: evidence, not verdict.** A QRZ entry with
  qslmgr "LOTW, QRZ, EQSL, QSL Card" and mQSL=yes was suggested as "No card"
  because eQSL/LoTW were merely mentioned. In real QRZ data the flags and
  eQSL/LoTW are nearly always set and say nothing about how to send a card.
  So: the suggestion rests only on stated evidence (a manager callsign, bureau
  / direct words with negations, an explicit "no QSL" / "eQSL only"); a stated
  route beats a refusal, qslmgr beats the bio, contradictions suggest nothing;
  electronic-only lists and mQSL give a fact, not a suggestion; both bureau and
  direct stated = bureau (cheaper). Computed on read from the stored raw
  fields (the `qsl_*` columns are legacy and no longer filled). With no stated
  route the Desk preselects nothing ("Not chosen yet").
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
- **2026-10-02 — The station page is gone.** `/station/<call>` predated the
  master-detail views and showed less than the research panel next to every
  card (no bio, no card history, raw QRZ flags), plus a second set of Inbox
  buttons that missed cards already at the Desk. Its one unique function, the
  forced QRZ re-lookup, is now the **Refresh** button in the panel's "QRZ:"
  header (shown while QRZ lookups are possible). Callsigns in lists and
  cards are plain text. Lost on purpose: a QRZ/history view for a station with
  no open card (from Log and Done).
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
  (2026-10-04: the built-in 140x90 layout holds one QSO per card for now; a
  station's open QSOs print as one card each. More rows: layout editor.)
- **2026-10-01 — Route data model.** `desired_method` = B / D / M (empty while
  a "yes" card waits at the Desk), `send_via` = how the card travelled (B/D,
  also for manager cards), `manager`; `note` marks "written now", "backlog",
  "sent elsewhere". `qsl_sent_method_local` holds B/D only (pushed as
  `QSL_SENT_VIA`); cards from older builds keep `M`/`W`. Desk keys: `b d m v`
  pick the route, `p`/`w` finish; Inbox "written now" is `w` then `b`/`d`.

- **2026-10-06 — Digital modes are a switch, not a hard rule.** The digital
  filter (FT8/FT4/FT2, FST4, JS8, WSPR, MSK144 - any mode starting with FT,
  JS8, WSPR, MSK, FST) stays on by default but can be turned off under
  Settings (`qualify.include_digital`), live for UDP, sync and Recompute;
  switched on, a scan queues the digital QSOs since the cutoff at once. The
  switch alone decides for digital modes - also over FT4/FT8 in an older
  config's `exclude_modes`, which now covers the other modes only.

- **2026-10-08 — New QSOs filter as a matrix.** Rows: how the QSO was made
  (phone, CW, RTTY & other typed digital modes, FT8 & co., satellite - any
  mode via satellite); columns: how new the contact is (new station, new
  band - for satellites new satellite -, repeat), from the whole log. Every
  QSO lands in one cell, a ticked cell asks, the rest is skipped silently;
  no "always ask" rules beside the `QSL!` marker (operator's call). "First
  contact only" is the first column alone. A change counts for new QSOs and
  queues what it now lets in since the cutoff; waiting QSOs stay.
- **2026-10-05 — Printing is two-step: print queue, print run, confirm.**
  "Print" no longer prints and sends at once (it replaces every print-and-send
  path: Desk card, Desk batch, Incoming reply). A card is sent to printing
  with its route and a note for the card (prefilled from ADIF `QSLMSG`) and
  waits in the print queue (`toprint`; moved 2026-10-06 from the top of the
  Desk to its own view `/work/printq` - the block pushed the Desk below the
  fold; the band's "Print queue" button carries the count). One print run
  renders all of them into one PDF and one printer job (`printing`); the run
  is confirmed as a whole - only then are the cards `sent` and pending
  push-back - or single cards are printed again or taken back first. One run
  is open at a time; a job that fails puts the run back. Amends 2026-10-01
  "Written or printed = sent": printed means the run was confirmed. For
  stations whose cards a QSL print service (e.g. DARC) produces, the run can
  be an **ADIF export** instead of a print job: one `.adi` file (QSO fields,
  `QSLMSG` = the note, `QSL_SENT_VIA`, `QSL_VIA`, `STATION_CALLSIGN`) in
  `card.export_dir`, confirmed like a printed run.
- **2026-10-05 — Lifecycle documented in `docs/STATES.md`** (diagrams, every
  transition, known gaps). Fixed with it: a reply (written / to print) is one
  transaction with the move to the Desk; marking a card received is one
  transaction with its event; a request whose card arrived may still get your
  card (Incoming QSLs) and shows as received on Done. Left as known gaps: a
  "no card" QSO Clublog later reports as sent stays "no card"; Reopen cannot
  undo what Clublog holds.

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
  (the manager's own instructions are a later phase). Direct needs a full
  postal address (QRZ or bio); without one the next accepted route, else
  `unclear` - the model sees the address, and a code check (`address_guard`)
  is compared as a variant. First model choice: `qwen3.5:4b` (multilingual,
  JSON schema), thinking off.
- **2026-10-02 — qpc answers with all accepted routes, not one.** Labelling
  showed that stations offering bureau and direct often state a preference
  or a condition ("direct only if no electronic QSL possible"). The answer is
  now status + every accepted route + the station's preferred route + an
  English note (preferences, conditions, requirements); choosing among the
  accepted routes is the operator's (later: qslotter's) decision, not the
  classifier's. The "cheapest route" rule is gone from the classifier.
- **2026-10-02 — qpc passed the gate (narrowly).** Two random samples of
  100 stations from the log, labelled blind by the operator. On the first,
  the prompts were developed (v1-v5) and qwen3.5:4b + v5 reached 69% fully
  right vs 60% for the heuristic - optimistic, tuned there. On the second,
  fresh sample (no prompt written after seeing it): qwen3.5:4b + v5 59%
  fully right (status, every route, preferred, via) vs heuristic 48%
  (discordant 18:7, McNemar p = 0.04); status 74% vs 61% (p = 0.004), route
  set 71% vs 66%. gemma4:e4b, qwen3.5:2b, ministral-3:3b and llama3.1:8b
  were worse; giving the model the postal address and a code check for
  "direct without address" changed nothing; a hybrid (heuristic when sure,
  else LLM) scored below the LLM alone. The model's confidence is not
  informative (83 "high", 55 right). Remaining errors: no-paper vs unknown
  for bare mqsl = 0 records, bureau added where only direct is stated,
  verbose notes. 41% of answers have at least one field wrong, so qpc can
  only suggest. About 2.5 s per station on an M2 Pro.
- **2026-10-02 — Address-only records: direct (heuristic and qpc).** When a
  QRZ record says nothing about QSL in qslmgr or the bio, the operator's
  rule decides: mqsl 0 -> no paper; a full postal address (street + city)
  with mqsl 1 or empty -> direct; without one, mqsl 1 -> bureau, mqsl empty
  -> unknown; eqsl/lotw flags do not matter. `qsldetermine` follows it, so
  the Desk now preselects direct for such stations (was bureau for mqsl 1,
  nothing for mqsl empty) and the Inbox sees "no paper" for mqsl 0 alone
  (was only with eqsl/lotw). Cached station info keeps the old suggestion
  until its next refresh. `qpc/LABELS.md` rule 6 and prompt v6 carry it;
  prompts v1-v5 are frozen with the rules they were evaluated with.
- **2026-10-02 — With that rule the heuristic catches up with qpc.** Labels
  re-checked under the rule (18 stations changed over both samples, applied
  mechanically to records with nothing about QSL - this slightly favours
  the heuristic, which implements exactly that rule). Fresh sample: heuristic
  72% fully right (was 48%), qwen3.5:4b + v6 71% (discordant 14:13, no
  difference); first sample 75% vs 77%. They fail on different stations
  (both wrong: 15). Split by what the heuristic based its answer on, qpc is
  better on free-text qslmgr fields (+3 on each sample), the heuristic on
  flags/address-only records; "qpc for free-text qslmgr, heuristic
  otherwise" - chosen on the first sample - gives 75% on the fresh one, not
  significant. qpc's distinct value is the note, which the heuristic cannot
  produce.
- **2026-10-02 — A contribution flag next to the note.** The most common
  note content (18 of the operator's 34 notes) is what a station asks in
  return for a card: SAE/SASE, IRC, green stamps, money, PayPal, a fee. It
  becomes a discrete flag, `contribution` = required / not-needed / not
  stated (kind and amount stay in the note), answered by qpc (prompt v7) and
  detected by keywords in `qsldetermine` (qslmgr and card-related bio
  sentences, with negations). The app computes it but does not store or show
  it yet; the Desk could flag it next to a direct route. Result on both
  samples (23 stations asking, labels reviewed): the keyword heuristic
  finds 20, qwen3.5:4b (v7) 12, neither raises a false alarm; the flag is
  right on 98% of stations for the heuristic, 92-95% for qwen. The
  negation patterns were tightened after seeing these samples, so the
  heuristic's numbers are somewhat optimistic. Asking qwen for the flag
  cost it about 3 points on the main answer (v6 71% -> v7 68% fully right
  on the fresh sample) - the heuristic is the better source for the flag.
- **2026-10-02 — Third sample, unlabelled: heuristic vs qpc.** 200 more
  stations (`qpc-lab compare`): the heuristic and qwen3.5:4b (v7) give the
  same status, routes and via on 70%. On the two labelled samples such
  agreement was right 91% of the time, while a disagreement was a coin toss
  (heuristic right 25, qwen 22, neither 15 of 62), so agreement is the
  useful confidence signal, not either classifier alone. The comparison
  exposed a heuristic bug: "no bureau" was read as "so direct" even in
  "No Buro, No Direct" or "NO Paper NO Bureau"; fixed (refusals first),
  which took the heuristic to 75% fully right on the fresh sample (was 72%;
  qwen v7 68%) and the third-sample agreement to 72%. qwen's typical slip
  there: mqsl 0 overriding an explicit "VIA BUREAU" in qslmgr (rule 7).
- **2026-10-02 — Jev-style decision model (tev1:4b) is far behind.** qpc can
  now ask decision models through Ollama's `/v1/systemone` (variant kind
  `decision`, specs in `qpc/decisions`; no note, no via). tev1:4b, a
  Qwen3.5-4B fine-tune for scored choices, gets the status about as often
  as the others (75-80%) but the routes badly: fully right 31-34% with
  status plus a true/false per route (d1-split), 15-21% with one choice
  over the whole answer (d1-single), against 75% for the heuristic and
  68-76% for qwen3.5:4b (v7) on the same labelled samples. It does not
  apply the conditional rules (address only = direct; "no bureau"), picks
  bureau for most direct-only stations (split) or bureau+direct for
  nearly everything (single), and names a preferred route where none is
  stated. Keeping only a long bio's QSL sentences changed little. ~2.5 s
  a station. Not pursued unless a stronger decision model appears.
- **2026-10-02 — Prompt v8: the same rules at half the size.** v7's system
  prompt was about 2,450 tokens (rules, definitions, nine examples, a
  "common mistakes" list) against ~250 for the station itself. v8 folds the
  definitions into the answer description, keeps five examples and drops
  the mistakes list: ~1,350 fixed tokens. qwen3.5:4b is not worse with it,
  if anything better: fully right 77% / 71% on the two labelled samples
  (v7: 76% / 68%), status 87% / 90% (v7: 83% / 81%). v8 is the default
  prompt; its rules are inlined, so a LABELS.md edit no longer changes it.
- **2026-10-02 — Heuristic: managers with a lead-in, SASE = direct.** The
  third-sample comparison with qwen v8 showed `qsldetermine` missing
  managers written "QSL MGR EA5GL", "QSL VIA EC1DD" or "QSL Manager:
  EA7FTR" in qslmgr, taking the station's own call as its manager ("QSL
  via PD3JWB (bureau)" on PD3JWB), and not reading "LOTW or SASE" as
  direct. Fixed; on the labelled samples the heuristic is now fully right
  on 75% / 77% (status, routes and via: 78% vs qwen v8 76%), and it agrees
  with qwen v8 on 76% of the third sample (was 74%).
- **2026-10-02 — A list of electronic services is no refusal.** Operator's
  rule: when qslmgr lists only electronic services ("LoTW, eQSL, Club
  Log"), mqsl and the postal address decide as if nothing were said
  (LABELS.md rule 8); only an explicit "eQSL only" / "LoTW only" refuses
  paper. `qsldetermine` used to read such a list as no paper. Changing it
  also exposed three bio-reading faults the old shortcut had hidden
  ("direction"/"directive" read as direct, "I don't answer paper QSL
  cards", "bureau ... no longer"), all fixed, plus "ONLY VIA EB7DX" as a
  manager. Three labels were brought in line with the rule (SP8QC/P,
  A61BG, UT5RB). Status, routes and via right on the labelled samples:
  heuristic 81%, qwen3.5:4b v8 78%; fully right 78% / 80% against 79% / 72%.
- **2026-10-03 — A clean 50-station holdout: still level.** 50 stations
  drawn after all tuning (none of the 400 seen before), labelled blind:
  fully right heuristic 76%, qwen3.5:4b v8 74%; status, routes and via
  76% vs 82%. Over all 250 labelled stations (status, routes, via):
  heuristic 80%, qwen v8 79%; when they agree (198) they are right 91% of
  the time. The heuristic's misses here: OQRS, which it never answers (3
  of 12), managers named in the bio, missed refusals; qwen's: a preferred
  route where none is stated (4).
- **2026-10-03 — The heuristic reads OQRS.** `qsldetermine` sets
  `Result.OQRS` when qslmgr or the bio offers an OQRS / Club Log request
  ("no OQRS" excluded); it joins the other routes, is a route of its own
  when nothing else is named (the flags and address no longer decide
  then), and turns "No cards needed! If you need one, use Club Log OQRS"
  from a refusal into an OQRS card. Computed, not yet stored or shown by
  the app (Method stays B/D/M). On the labelled samples it fixed 5
  stations and broke 1 (9A7YY, whose label leaves out the OQRS its bio
  offers); fully right now heuristic 80% / 82% / 80% against qwen v8
  80% / 72% / 74%. Still missed: the labelling convention that a card
  "requested via OQRS" for bureau or direct is an OQRS route only.
- **2026-10-03 — Heuristic round 3; prompt v9 rejected; bwpc runs qpc.**
  The heuristic now reads postal wording ("VIA MAIL", "to the above
  address", "P.O.Box"), a DARC DOK as bureau, managers inside free text
  ("ALL QSL's via N4GNR Direct Only", "via bureau DL8KAC") and in the bio
  with their routes, email-only refusals, and routes only from the bio's
  sentences about cards: status, routes and via right on 212 of the
  operator's 250 labelled stations (was 205); fully right 85% / 82% / 84%
  on samples 1, 2 and the holdout. A 60-station sample labelled by the
  assistant (eval/qpc5, not reviewed by the operator) gives 87%. Tried and
  dropped: adding the bio's routes to a qslmgr text (3 fixed, 6 broken)
  and dropping direct without a full QRZ address (breaks 3 operator labels
  that do not follow LABELS rule 5). Prompt v9 (the rules as an ordered
  procedure: refusal, routes named, then flags and address) lost to v8 on
  all three operator samples (78/71/70% vs 80/72/74%), won only on the
  assistant's sample: v8 stays the default. qpc runs fine on bwpc's
  Ollama (LAN, `qpc/experiments/bwpc.yaml`), about 1.5 s a station
  against 2.5-3 s on the laptop.
- **2026-10-03 — Prompt v11: the model reads the text, code applies the
  flags.** Rewording the mqsl/address rules for qwen3.5:4b only moved its
  errors around: v9 (an ordered procedure) turned mqsl 0 + address into
  direct, v10 (mqsl first) let mqsl 0 overrule "VIA BUREAU" - both lost to
  v8. v11 hides mqsl/eqsl/lotw from the model; it answers "unknown" when
  qslmgr and the bio say nothing about cards, and the classifier then
  applies LABELS.md rule 6 itself (the prompt declares it). Fully right on
  the operator's 250 labelled stations: v11 193, v8 189, heuristic 209;
  v11 also wins on both assistant-labelled samples (87% / 80% vs 82% /
  78%) and runs in ~1.4 s a station on bwpc. v11 is the default prompt.
  The heuristic still leads.
- **2026-10-03 — The operator's labelling rules; prompt v12 tried.** Five
  answers settled what the labels contradicted (LABELS.md: direct needs a
  full address on QRZ or in the bio; preferred only among two or more
  routes; a card sent only on an OQRS request is OQRS; postage named for a
  manager means direct via it; "no bureau" alone with mqsl 0 is no paper).
  14 labels were brought in line. The heuristic follows them and is now
  fully right on 91% / 85% / 84% of the operator's samples (218 of 250)
  and 92% / 93% of the assistant's two samples. v12 (v11 plus the five
  rules) gained on preferred routes, managers and OQRS but started
  answering "bureau" for address-only records instead of leaving them to
  the code's flag rule: 189 of 250 against v11's 193, so v11 stays the
  default. bwpc's Ollama hung once for about two hours and recovered by
  itself.
- **2026-10-04/05 — Verified print jobs (superseded 2026-10-06).** On a
  branch, qslotter followed each print job in CUPS (IPP) and marked the card
  sent only when the job completed, with a per-job print list (Retry / "It
  did print"). Merged 2026-10-06 with the two-step print queue winning: the
  run is confirmed by the operator, the IPP watcher (`printer.Watcher`,
  `ipp.go`) stays as code to follow a run's job later. Kept from the branch:
  the Desk preview (key `s`), the layout editor, 140 x 90 mm default, the
  satellite column, Settings help and Pull button.
- **2026-10-04 — Card layout is configured visually.** The operator prints on
  pre-printed stock: the editor shows a scan of the card behind the fields
  and asks the server to lay every change out (the browser never sets text
  itself, so the preview cannot drift from the print). Layouts stay YAML in
  `cards/` next to the config, several kept, one active via `card.template`
  (empty = built-in). The printer's feed error is corrected globally
  (`printer.offset_mm`), measured on a test card with a millimetre ruler.
  Out of scope: envelope/label printing, per-row shapes, perspective
  correction of a skewed scan (it is stretched to the card), fonts beyond the
  PDF core three.
- **2026-10-09 — Decision models (Jev-style) stopped.** Two decision
  models behind Ollama `/v1/systemone` (tev1 4B, clef-flash 9B with a
  16k context) scored 33-36% fully right on the operator's labelled
  stations against 77-84% for qwen3.5:4b and 85-92% for the heuristic;
  size and context made no difference. The causes are structural: one
  probability per question from one pass cannot do a conjunction of
  fields ("direct named AND a full address": direct answered false in
  20 of 100 stations), rules asked of the model fail (mqsl/address,
  DCL, preference only with two routes), and an absent mention scores
  0.5-0.9 (the plausibility of the route, not what the text says).
  Layout `text` (spec d2-text) fixes what can be fixed: every question
  asks only whether the text names something, code composes the rest
  and the cut per question is fitted on labelled answers (`threshold`
  in the spec, fitted on qpc/qpc2): tev1 reaches 60% on the 350
  stations (65 / 58 / 52 / 60%), a logistic layer over all its
  probabilities (`qpc-lab fit`, held out sample by sample) 65%, with
  the heuristic's via callsign spliced in 69%. Still 20 points below
  the chat model and 25 below the heuristic, and as a second opinion it
  carries no information: it differs from the heuristic on 36% of the
  stations, and there the heuristic is right in 85% (overall 90%). No
  further decision-model work; the chat model's distinct value (the
  note, via from free text) stays with qwen3.5:4b. The decision-model
  code (variant kind `decision`, the specs, `bio_focus`, `qpc-lab fit`)
  was removed the same day; the runs stay in `eval/*/runs/tev1-*` and
  `clef-*`, the analysis in commits cc223aa and b1ac434.
- **2026-10-03 — 100 more stations, operator-reviewed; v13 default.** The
  assistant pre-labelled a fourth sample of 100 (eval/qpc7) and the
  operator reviewed all of them, changing 6 (one more rule came out of
  it: DCL mentioned = DARC member = bureau too, LABELS.md 6a). With the
  day's fixes the heuristic is fully right on 310 of the operator's 350
  labelled stations (89%): 91 / 85 / 84 / 92% per sample. qwen3.5:4b
  with v13 (v11's split - the model reads the text, code applies the
  flags - plus the operator's rules) gets 280 (80%), v11 276, so v13 is
  the default prompt; the heuristic still leads by about 9 points.
  bwpc's ROCm GPU wedged twice (model load hanging); a reboot fixed it.
