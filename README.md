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
   (FT\*, JS8, ...), excluded by mode, older than the `qualify.since` cutoff, or
   already carded. Repeat contacts enter too, shown with their history. A
   marker (`QSL!`) in the notes force-includes a QSO.
3. **Decide** (the decision queue) — each new QSO is presented with what helps
   you decide: what QRZ says about the station's QSL habits (manager field, bio
   lines, flags), earlier QSOs with the station, whether a card was already
   sent or received. You choose **Bureau**, **Direct**, **Via manager**, **No
   card**, or **Written** (card filled in on the spot: done, nothing to send).
   A decided QSO leaves the decision queue at once.
4. **Produce** (the work queue) — Bureau / Direct / Via-manager cards wait
   here, one card at a time: print it (template on card stock) or write it by
   hand. Done = sent; the next card appears.
5. **Reconcile** — sent cards go back to Clublog (`QSL_SENT=Y` plus
   `QSL_SENT_VIA` or `QSL_VIA`) when you choose; received cards are logged the
   same way.

## The UI

- **Queue** — the decision queue as a list (also the compact window: tray menu
  on Windows). One-click stamps per row, batch actions, live updates.
- **Decide** — the decision queue card by card with the full research panel.
  Keyboard: `b` bureau, `d` direct, `m` via manager (type the call, Enter),
  `n` no card, `w` written on the spot, arrows to browse.
- **Work** / **Cards** — the work queue as a list grouped by route, and card by
  card with the address to write to. Keyboard: `p` print, `w` written,
  `u` back to the decision queue.
- **Done** — finished cards, with Reopen for a misclick.
- **Receive** — type a callsign, pick the QSOs, mark the card received.
- **Settings** — edit QRZ/Clublog credentials live (with immediate
  validation) and your station identity for the cards.

## Install

You need Go 1.26+ and, on Windows, [SumatraPDF](https://www.sumatrapdfreader.org/)
for printing — use **3.5.x** (the 3.6 print engine hangs; bundle the portable
build as `third_party/sumatrapdf/SumatraPDF.exe` next to `qslotter.exe`).
macOS/Linux print via `lp`.

    go build ./cmd/qslotter

Cross-compile for Windows:

    GOOS=windows GOARCH=amd64 go build -o qslotter.exe ./cmd/qslotter

Optionally embed the tray icon and version info first (`go run
github.com/tc-hib/go-winres@latest make` — see `winres/`). A tray build can
hide the console with `-ldflags "-H windowsgui"`.

## Run

1. Copy `config.example.yaml` to `config.yaml` and fill in your Clublog and
   QRZ credentials (secrets can come from the environment via `${VAR}`).
2. Start the server and open <http://127.0.0.1:8473/>:

       ./qslotter -config config.yaml

3. Click **Pull from Clublog** (or wait for the background pull) to mirror
   your log. Only QSOs from the day qslotter first ran enter the decision
   queue; to work through older QSOs too, set `qualify.since` (a date, or
   `all`) and press **Recompute queue**. From then on, new QSOs arrive on
   their own.
4. Wrong QRZ credentials? Fix them under **Settings** — changes apply
   immediately and are validated on the spot.

### Configure Log4OM

In Log4OM → Settings → Program Configuration → **UDP Functions**:

- Add an **outbound** UDP destination pointing at qslotter, e.g.
  `127.0.0.1:1273` (`udp.listen` in `config.yaml`).
- **Format: ADIF** (not N1MM XML — XML datagrams are skipped with a warning).
- Enable on **QSO added**.

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
comments — server, tray, station identity, Clublog, QRZ, printer, card
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
