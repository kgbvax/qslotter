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
   within a second. An hourly Clublog pull mirrors the full log as a
   reconciliation backstop.
2. **Qualify** — configurable rules decide which QSOs are card-worthy: skip
   FT\*/digital modes, first-contact-only, or force-include a memorable QSO
   with a marker (`QSL!`) in the notes.
3. **Decide** — QRZ data (structured fields + bio) drives a suggestion with
   confidence and reasoning. You confirm with one click or one keystroke,
   card by card.
4. **Fulfil** — print the QSO data onto card stock using a configurable
   template, or write the card by hand; both paths mark it sent with the
   chosen method.
5. **Reconcile** — push `QSL_SENT` (+ the method, as `QSL_SENT_AS`) back to
   Clublog when you choose; received cards are logged the same way.

## The UI

- **Decide** — one card at a time: the QSO as a card, the QRZ preference and
  mailing address alongside, and rubber-stamp buttons for the decision.
  Every decision advances to the next card. Keyboard: `b`/`d`/`m` stamp,
  `n` no card, `p` print, `h` handwritten, `s` sent, `k` skip.
- **Queue** — the full list with the same one-click stamps, batch actions,
  and live updates: new QSOs appear without a refresh.
- **Compact** — the same queue in a small always-open window (tray menu on
  Windows).
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

3. Click **Pull from Clublog** (or wait for the background pull), then
   **Recompute queue** — your card-worthy QSOs appear. From then on, new
   QSOs arrive on their own.
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

`Print` renders the card as a PDF from a YAML template (millimetre
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
