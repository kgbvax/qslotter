# qslotter

QSL card handling, DL9ET style.

qslotter watches your log in real time, decides which QSOs deserve a paper
QSL card, suggests how to send it (bureau, direct, via a manager — or not at
all), and gets the card out: printed on card stock or written by hand. QSL
state flows back to Clublog.

Electronic QSL (eQSL, LoTW, email) is deliberately out of scope — that is the
logbook's job.

## How it works

1. **Ingest** — Log4OM broadcasts each new QSO over UDP; qslotter ingests it
   within a second. A Clublog pull mirrors the full log as a reconciliation
   backstop; a card Clublog already shows as sent is closed, not duplicated.
2. **Qualify** — QSOs enter the *decision queue* unless they are digital
   (FT\*, JS8, ...; switchable under Settings), excluded by mode, older than the `qualify.since` cutoff, or
   already carded. Repeat contacts enter too, shown with their history. A
   marker (`QSL!`) in the notes force-includes a QSO.
3. **Decide** (New QSOs) — each new QSO is presented with what helps you
   decide: what QRZ says about the station's QSL habits, earlier QSOs with
   the station, whether a card was already sent or received. You answer
   **yes, card** or **no card**.
4. **Produce** (the Desk) — "yes" cards wait here, one card per station. The
   route (bureau, direct, via manager direct/bureau) is chosen when the card
   is written by hand (done = sent) or sent **to print**, with an optional
   note printed on the card (prefilled from the log's `QSLMSG`). Cards to
   print collect in the print queue at the top of the Desk; **Print** sends
   them all as one job, and once they came out right, **All fine** marks them
   sent (single cards can be printed again first). Stations using a QSL
   print service (e.g. DARC) **Export ADIF** instead: the queue goes into one
   `.adi` file for the service, confirmed the same way. Every path:
   [docs/STATES.md](docs/STATES.md).
5. **Reconcile** — sent cards go back to Clublog (`QSL_SENT=Y` plus
   `QSL_SENT_VIA` or `QSL_VIA`) when you choose; received cards are logged the
   same way.

## The UI

- **New QSOs** — the decision queue as a list with the card next to it (also
  the compact window from the tray), and card by card. Keyboard: `y` yes,
  `n` no card, arrows to browse.
- **Desk** — the print queue on top, the cards as a list grouped by route,
  and card by card, with the address to write to. Keyboard: `b` `d` `m` `v`
  route, `p` to print, `w` written by hand, `r` request their card, `n` no
  card.
- **Incoming QSLs** — mark a received card, answer it (written, to print,
  later at the Desk), see the cards you requested.
- **Done** — finished cards, with Reopen for a misclick.
- **Receive** — type a callsign, pick the QSOs, mark the card received.
- **Settings** — edit QRZ/Clublog credentials live (with immediate
  validation) and your station identity for the cards.

## Install

qslotter is one binary per OS, built from any machine (no C toolchain):
it shows its own app window using the WebView the OS already ships.

- **macOS:** unzip `qslotter-<version>-macos.zip`, move `qslotter.app` to
  Applications, start it. It is not notarized: the first start needs
  System Settings > Privacy & Security > "Open Anyway".
- **Windows:** put `qslotter.exe` anywhere (e.g. `%USERPROFILE%\qslotter`)
  and start it. Needs the Edge WebView2 Runtime (preinstalled on current
  Windows 10/11). For printing bundle [SumatraPDF](https://www.sumatrapdfreader.org/)
  **3.5.x** as `third_party\sumatrapdf\SumatraPDF.exe` next to the exe (the
  3.6 print engine hangs).
- **Linux:** unpack the tarball; needs WebKitGTK (`apt install
  libwebkit2gtk-4.1-0` or `libwebkitgtk-6.0-4`, Fedora `webkit2gtk4.1`,
  Arch `webkit2gtk-4.1`). For a menu entry copy `qslotter` to `~/.local/bin`
  (on the session PATH), `qslotter.desktop` to `~/.local/share/applications/`
  and `qslotter.png` to `~/.local/share/icons/`. No tray on Linux yet:
  closing the last window quits.

Without a WebView runtime qslotter opens a Chromium-family browser as an app
window instead (`-ui browser` forces that; `-ui headless` runs the server
only).

### Build

Go 1.27.1+. From any OS:

    scripts/release.sh          # all targets -> dist/
    scripts/macapp.sh           # on a Mac: dist/qslotter.app + zip

or just `go build ./cmd/qslotter` for the machine you are on.

## Run

1. Start qslotter. The first start creates a config in the user config dir
   (`~/Library/Application Support/qslotter/config.yaml`,
   `%AppData%\qslotter\config.yaml`, `~/.config/qslotter/config.yaml`) and
   opens **Settings**: enter your Clublog and QRZ credentials there. An
   existing `config.yaml` in the working directory, or `-config path`, is used
   instead.
2. The window shows the decision queue; the tray (macOS menu bar, Windows
   notification area - promote the icon to keep it visible) reopens it and
   has the compact window. Closing the window keeps qslotter running for the
   Log4OM feed; quit from the tray, the Dock (macOS) or Settings > Quit
   qslotter. Starting qslotter again brings the running window to the front.
3. Click **Pull from Clublog** (or wait for the background pull) to mirror
   your log. Only QSOs from the day qslotter first ran enter the decision
   queue; to work through older QSOs too, set `qualify.since` (a date, or
   `all`) and press **Recompute queue**. From then on, new QSOs arrive on
   their own.
4. Log file: `~/Library/Logs/qslotter/qslotter.log`,
   `%LOCALAPPDATA%\qslotter\qslotter.log`, `~/.cache/qslotter/qslotter.log`.
   The web UI also stays reachable at <http://127.0.0.1:8473/> (set
   `server.addr: 0.0.0.0:8473` for other machines on the LAN - there is no
   login).

### Configure Log4OM

In Log4OM → Settings → Program Configuration → **UDP Functions**:

- Add an **outbound** UDP connection pointing at qslotter, e.g.
  `127.0.0.1:1273` (`udp.listen` in `config.yaml`), service **ADIF MESSAGE**
  (a logged QSO; ADIF, not N1MM XML).
- Optional, for the **QSO in progress**: a second outbound connection to the
  same address with the service **CALLSIGN**. Log4OM then sends the call in
  its entry field; the Inbox shows that station on top with its research,
  and a card written during the QSO ("Card written during the QSO: Bureau /
  Direct") is booked the moment the QSO is logged. N1MM-family loggers
  (N1MM Logger+, DXLog) send the same as `<lookupinfo>` broadcasts.

Log4OM's HTTP webhook batches on a 2–3 minute timer; UDP is sub-second,
which is why it is the primary feed and Clublog is the backstop.

### Printing

**Print** (work queue) renders the card as a PDF from a YAML template (millimetre
coordinates, see `internal/template`) and sends it to the printer — on
Windows through SumatraPDF's silent print, elsewhere through `lp`. Set
`printer.name` to pin a printer; otherwise the system default is used.
Card defaults: 100 × 74 mm.

## Configuration

See [`config.example.yaml`](config.example.yaml) for every option with
comments — server, UI mode, station identity, Clublog, QRZ, printer, card
template, qualify rules, store, UDP.

## Documentation

- [`docs/VISION.md`](docs/VISION.md) — the consolidated product vision:
  founding requirements, the decision model, UI strategy, gap list, roadmap,
  and the decision log.
- [`CLAUDE.md`](CLAUDE.md) — repository guidance for coding agents
  (architecture, commands, known quirks).

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE). SumatraPDF (bundled on Windows)
is GPLv3.
