// Package store is the persistence layer for qslotter.
//
// The Store interface is backend-agnostic so that v2 can add a CouchDB
// implementation (see internal/store/couchdb.go, deferred). v1 ships only the
// SQLite implementation below, using modernc.org/sqlite (pure Go, no cgo) so
// the binary cross-compiles cleanly to Windows from macOS.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the backend-agnostic persistence interface used by qslotter's
// sync, web, and udplistener packages. The v1 implementation is SQLiteStore
// (this file); a v2 CouchDB implementation is planned.
type Store interface {
	Close() error
	MetaGet(key string) (string, error)
	MetaSet(key, value string) error
	UpsertQSO(q *QSO) (isNew, changed bool, err error)
	RecentQSOsByCall(call string, n int) ([]*QSO, error)
	AllQSOs() ([]*QSO, error)
	GetQSO(qslKey string) (*QSO, error)
	SetQSLSentLocal(qslKey, method string) error
	SetQSLRcvdLocal(qslKey string) error
	BookReceived(keys []string) (booked int, err error)
	PendingPushBack() ([]*QSO, error)
	MarkPushed(snapshot *QSO) error
	Enqueue(item *QueueItem) error
	QueueByStatus(status string) ([]*QueueItem, error)
	QueueGet(qslKey string) (*QueueItem, error)
	// Guarded queue transitions: each moves one item between statuses inside a
	// transaction (queue row + qsos columns + event) and returns ErrConflict
	// when the item is not in a state the transition may start from.
	QueueList(statuses ...string) ([]*QueueItem, error)
	QueueAccept(qslKey string) error
	QueueWrittenNow(keys []string, rt Route) error
	QueueWritten(keys []string, rt Route) error
	QueueToPrint(keys []string, rt Route, note string) error
	QueueUnprint(keys ...string) error
	QueueStartRun() (keys []string, err error)
	QueueRunFailed(keys ...string) error
	QueueReprint(keys ...string) error
	QueueRunBack(keys ...string) error
	QueueConfirmRun() (keys []string, err error)
	QueueRequested(keys []string, req Request) error
	QueueDecline(keys ...string) error
	QueueDeskDecline(keys ...string) error
	QueueBack(keys ...string) error
	QueueReply(keys []string) error
	QueueReplyFinish(keys []string, how string, rt Route, note string) error
	QueueReopen(qslKey string) (inClublog string, err error)
	QueueCloseSentElsewhere(qslKey string) error
	QueueDiscardBacklog(before string) (int, error)
	QueueCounts() (queued, decided, pendingPush int, err error)
	ExpectedCount() (int, error)
	AppendEvent(e *Event) error
	CallHistory(call string, limit int) ([]*HistoryRow, error)
	GetStation(callsign string) (*StationInfo, error)
	PutStation(si *StationInfo) error
}

// Open opens or creates the SQLite database at path and ensures the schema is
// present. It enables WAL and a busy timeout so reads never block the writer.
// Returns a Store interface; the concrete type is *SQLiteStore.
func Open(path string) (Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping store: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.ensureSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

// SQLiteStore is the SQLite-backed Store implementation.
type SQLiteStore struct {
	db *sql.DB
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

const schemaVersion = 2

func (s *SQLiteStore) ensureSchema() error {
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	// Column-level migrations for databases created before these columns
	// existed. ALTER TABLE ADD COLUMN fails on duplicate columns; that exact
	// error is expected and ignored. Fresh databases created from `schema`
	// above already have every column and skip all of these.
	migrations := []struct{ table, col, ddl string }{
		{"qsos", "name", `ALTER TABLE qsos ADD COLUMN name TEXT`},
		{"qsos", "qth", `ALTER TABLE qsos ADD COLUMN qth TEXT`},
		{"qsos", "qsl_sent_method_local", `ALTER TABLE qsos ADD COLUMN qsl_sent_method_local TEXT`},
		{"qsos", "qsl_sent_as", `ALTER TABLE qsos ADD COLUMN qsl_sent_as TEXT`},
		{"station_info", "qsl_confidence", `ALTER TABLE station_info ADD COLUMN qsl_confidence TEXT`},
		{"station_info", "qsl_reason", `ALTER TABLE station_info ADD COLUMN qsl_reason TEXT`},
		{"station_info", "name", `ALTER TABLE station_info ADD COLUMN name TEXT DEFAULT ''`},
		{"station_info", "attn", `ALTER TABLE station_info ADD COLUMN attn TEXT DEFAULT ''`},
		{"station_info", "not_found", `ALTER TABLE station_info ADD COLUMN not_found INTEGER DEFAULT 0`},
		{"qsl_work_queue", "send_via", `ALTER TABLE qsl_work_queue ADD COLUMN send_via TEXT DEFAULT ''`},
		{"qsl_work_queue", "note", `ALTER TABLE qsl_work_queue ADD COLUMN note TEXT DEFAULT ''`},
		{"qsl_work_queue", "channel", `ALTER TABLE qsl_work_queue ADD COLUMN channel TEXT DEFAULT ''`},
		{"qsos", "freq_rx", `ALTER TABLE qsos ADD COLUMN freq_rx TEXT DEFAULT ''`},
		{"qsl_work_queue", "card_note", `ALTER TABLE qsl_work_queue ADD COLUMN card_note TEXT DEFAULT ''`},
	}
	// sat_name came with freq_rx. Satellite QSOs stored before have neither:
	// clearing their hash makes the next Clublog pull store them again.
	if _, err := s.db.Exec(`ALTER TABLE qsos ADD COLUMN sat_name TEXT DEFAULT ''`); err == nil {
		if _, err := s.db.Exec(`UPDATE qsos SET hash='' WHERE upper(prop_mode)='SAT'`); err != nil {
			return fmt.Errorf("migrate qsos.sat_name: %w", err)
		}
	} else if !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate qsos.sat_name: %w", err)
	}
	// qslmsg likewise: open cards stored before it get it with the next pull.
	if _, err := s.db.Exec(`ALTER TABLE qsos ADD COLUMN qslmsg TEXT DEFAULT ''`); err == nil {
		if _, err := s.db.Exec(`UPDATE qsos SET hash='' WHERE qsl_key IN
			(SELECT qsl_key FROM qsl_work_queue WHERE status IN ('queued','decided'))`); err != nil {
			return fmt.Errorf("migrate qsos.qslmsg: %w", err)
		}
	} else if !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate qsos.qslmsg: %w", err)
	}
	for _, m := range migrations {
		if _, err := s.db.Exec(m.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate %s.%s: %w", m.table, m.col, err)
		}
	}
	// ALTER TABLE ADD COLUMN leaves NULL in pre-existing rows, but the scan
	// targets are plain strings (a NULL fails the whole row scan and the QSO
	// silently drops out of every list). Backfill to '' - idempotent, and a
	// no-op on fresh databases.
	backfills := []string{
		`UPDATE qsos SET name='' WHERE name IS NULL`,
		`UPDATE qsos SET qth='' WHERE qth IS NULL`,
		`UPDATE station_info SET qsl_confidence='' WHERE qsl_confidence IS NULL`,
		`UPDATE station_info SET qsl_reason='' WHERE qsl_reason IS NULL`,
	}
	for _, b := range backfills {
		if _, err := s.db.Exec(b); err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
	}
	// Legacy repair: older builds stopped at status "printed" (never marked
	// sent, never pushed, listed nowhere). A printed card with a recorded route
	// is a sent card; without one it goes back to the decision queue.
	legacy := []string{
		`UPDATE qsos SET qsl_sent_local='Y',
			qsl_sent_method_local=(SELECT q.desired_method FROM qsl_work_queue q WHERE q.qsl_key=qsos.qsl_key),
			qslsdate_local=(SELECT replace(substr(COALESCE(q.printed_at, q.added_at),1,10),'-','') FROM qsl_work_queue q WHERE q.qsl_key=qsos.qsl_key)
			WHERE qsl_sent_local IS NULL AND (qsl_sent IS NULL OR qsl_sent <> 'Y')
			AND qsl_key IN (SELECT qsl_key FROM qsl_work_queue WHERE status='printed' AND desired_method IN ('B','D','M'))`,
		`UPDATE qsl_work_queue SET status='sent', sent_at=COALESCE(printed_at, added_at)
			WHERE status='printed' AND desired_method IN ('B','D','M')`,
		`UPDATE qsl_work_queue SET status='queued' WHERE status='printed'`,
	}
	for _, l := range legacy {
		if _, err := s.db.Exec(l); err != nil {
			return fmt.Errorf("legacy repair: %w", err)
		}
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)`, schemaVersion)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS qsos (
  qsl_key    TEXT PRIMARY KEY,          -- CALL|YYYYMMDD|HHMMSS|BAND
  call       TEXT NOT NULL,
  qso_date   TEXT NOT NULL,             -- YYYYMMDD
  time_on    TEXT NOT NULL,             -- HHMMSS
  band       TEXT NOT NULL,
  mode       TEXT NOT NULL,
  freq       TEXT,
  rst_sent   TEXT,
  rst_rcvd   TEXT,
  qsl_sent   TEXT,                      -- Y/N/R/Q/I
  qsl_rcvd   TEXT,
  qslsdate   TEXT,                      -- YYYYMMDD
  qslrdate   TEXT,
  lotw_qsl_rcvd TEXT,
  dxcc       TEXT,
  prop_mode  TEXT,
  gridsquare TEXT,
  operator   TEXT,
  notes      TEXT,
  name       TEXT,                      -- operator name (ADIF NAME)
  qth        TEXT,                      -- station location (ADIF QTH)
  sat_name   TEXT DEFAULT '',           -- satellite (ADIF SAT_NAME) for PROP_MODE SAT
  freq_rx    TEXT DEFAULT '',           -- receive frequency, MHz (ADIF FREQ_RX), split/satellite
  qslmsg     TEXT DEFAULT '',           -- message for the card (ADIF QSLMSG)
  hash       TEXT NOT NULL,             -- sha256 of canonical ADIF repr
  first_seen_at TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  qsl_sent_local TEXT,                  -- qslotter-managed, not yet pushed to Clublog
  qsl_sent_method_local TEXT,           -- B/D/E/M used when sending (goes to QSL_SENT_VIA)
  qsl_rcvd_local TEXT,
  qslsdate_local  TEXT,
  qslrdate_local  TEXT,
  qsl_sent_as TEXT                       -- method last confirmed pushed to Clublog
);
CREATE INDEX IF NOT EXISTS qsos_call_idx ON qsos(call, qso_date DESC, time_on DESC);
CREATE INDEX IF NOT EXISTS qsos_band_idx ON qsos(band, qso_date DESC);
CREATE INDEX IF NOT EXISTS qsos_date_idx ON qsos(qso_date DESC);

CREATE TABLE IF NOT EXISTS station_info (
  callsign        TEXT PRIMARY KEY,
  qslmgr          TEXT,
  eqsl            TEXT,                 -- Y/N/''
  mqsl            TEXT,
  lotw            TEXT,
  email           TEXT,
  addr1           TEXT,
  addr2           TEXT,
  state           TEXT,
  zip             TEXT,
  country         TEXT,
  dxcc            TEXT,
  bio_text        TEXT,
  qsl_method      TEXT,                 -- B/D/E/M (suggestion from qsldetermine)
  qsl_route       TEXT,                 -- manager callsign if M, else ''
  refuse_paper    INTEGER DEFAULT 0,
  qsl_confidence  TEXT,                 -- high/medium/low
  qsl_reason      TEXT,                 -- human-readable why
  name            TEXT DEFAULT '',      -- QRZ first + last name
  attn            TEXT DEFAULT '',      -- QRZ "attn" line for the address
  not_found       INTEGER DEFAULT 0,    -- QRZ has no record (negative cache)
  fetched_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS qsl_work_queue (
  qsl_key        TEXT PRIMARY KEY REFERENCES qsos(qsl_key),
  desired_method TEXT,                  -- route B/D/M ('' = yes, route open), N no card, R requested, W legacy written
  manager        TEXT,
  status         TEXT NOT NULL,          -- queued/decided/toprint/printing/sent/skipped/requested (docs/STATES.md)
  override_reason TEXT,
  added_at       TEXT NOT NULL,
  printed_at     TEXT,
  sent_at        TEXT,
  send_via       TEXT DEFAULT '',        -- how the card travelled: B/D (manager cards too)
  note           TEXT DEFAULT '',        -- e.g. "written now", "backlog", a request's note
  channel        TEXT DEFAULT '',        -- requested cards: OQRS / PayPal / e-mail / other
  card_note      TEXT DEFAULT ''         -- printed on the card (qslmsg field), set when sent to printing
);
CREATE INDEX IF NOT EXISTS queue_status_idx ON qsl_work_queue(status);

CREATE TABLE IF NOT EXISTS qsl_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  qsl_key   TEXT NOT NULL,
  direction TEXT NOT NULL,              -- sent/rcvd/decision/print
  method    TEXT,
  via       TEXT,
  date      TEXT NOT NULL,
  source    TEXT NOT NULL,               -- auto/manual/clublog
  note      TEXT
);
CREATE INDEX IF NOT EXISTS events_key_idx ON qsl_events(qsl_key, id DESC);
`

// --- meta helpers ---

func (s *SQLiteStore) MetaGet(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *SQLiteStore) MetaSet(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- QSO upsert (called by sync) ---

// QSO is the row representation of a Clublog QSO as stored locally.
type QSO struct {
	QSLKey             string
	Call               string
	QSODate            string
	TimeOn             string
	Band               string
	Mode               string
	Freq               string
	RSTSent            string
	RSTRcvd            string
	QSLSent            string
	QSLRcvd            string
	QSLSDate           string
	QSLRDate           string
	LoTWQSLRcvd        string
	DXCC               string
	PropMode           string
	Gridsquare         string
	Operator           string
	Notes              string
	Name               string
	QTH                string
	SatName            string // ADIF SAT_NAME (PROP_MODE SAT)
	FreqRX             string // ADIF FREQ_RX, MHz: the receive frequency of a split or satellite QSO
	QSLMsg             string // ADIF QSLMSG: the message for the card, prefills the card note
	Hash               string
	FirstSeenAt        string
	UpdatedAt          string
	QSLSentLocal       sql.NullString
	QSLSentMethodLocal sql.NullString // B/D/E/M chosen in qslotter (goes to QSL_SENT_VIA)
	QSLRcvdLocal       sql.NullString
	QSLSDateLocal      sql.NullString
	QSLRDateLocal      sql.NullString
	QSLSentAs          sql.NullString // method last pushed to Clublog
}

// UpsertQSO inserts a QSO or updates the Clublog-sourced fields if the hash
// changed. Local QSL state columns are preserved across updates. Returns
// isNew (true if the row was inserted) and changed (true if the row was
// inserted or its Clublog-sourced fields changed).
func (s *SQLiteStore) UpsertQSO(q *QSO) (isNew, changed bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if q.FirstSeenAt == "" {
		q.FirstSeenAt = now
	}
	q.UpdatedAt = now
	// Query the previous hash first to detect change vs. new row.
	var prevHash string
	err = s.db.QueryRow(`SELECT hash FROM qsos WHERE qsl_key=?`, q.QSLKey).Scan(&prevHash)
	isNew = err == sql.ErrNoRows
	if err != nil && !isNew {
		return false, false, err
	}
	_, err = s.db.Exec(`INSERT INTO qsos (
		qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, sat_name, freq_rx, qslmsg,
		hash, first_seen_at, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?)
	ON CONFLICT(qsl_key) DO UPDATE SET
		call=excluded.call, qso_date=excluded.qso_date, time_on=excluded.time_on,
		band=excluded.band, mode=excluded.mode, freq=excluded.freq,
		rst_sent=excluded.rst_sent, rst_rcvd=excluded.rst_rcvd,
		qsl_sent=excluded.qsl_sent, qsl_rcvd=excluded.qsl_rcvd,
		qslsdate=excluded.qslsdate, qslrdate=excluded.qslrdate,
		lotw_qsl_rcvd=excluded.lotw_qsl_rcvd, dxcc=excluded.dxcc,
		prop_mode=excluded.prop_mode, gridsquare=excluded.gridsquare,
		operator=excluded.operator, notes=excluded.notes,
		name=excluded.name, qth=excluded.qth, sat_name=excluded.sat_name, freq_rx=excluded.freq_rx, qslmsg=excluded.qslmsg,
		hash=excluded.hash, updated_at=excluded.updated_at,
		qsl_rcvd_local=CASE WHEN excluded.qsl_rcvd='Y' AND qsos.qsl_rcvd_local='R' THEN NULL ELSE qsos.qsl_rcvd_local END
	WHERE qsos.hash <> excluded.hash`,
		q.QSLKey, q.Call, q.QSODate, q.TimeOn, q.Band, q.Mode, q.Freq,
		q.RSTSent, q.RSTRcvd, q.QSLSent, q.QSLRcvd, q.QSLSDate, q.QSLRDate,
		q.LoTWQSLRcvd, q.DXCC, q.PropMode, q.Gridsquare, q.Operator, q.Notes,
		q.Name, q.QTH, q.SatName, q.FreqRX, q.QSLMsg,
		q.Hash, q.FirstSeenAt, q.UpdatedAt)
	if err != nil {
		return false, false, err
	}
	return isNew, isNew || prevHash != q.Hash, nil
}

// RecentQSOsByCall returns the most recent n QSOs with a given callsign,
// ordered newest-first. Call must be upper-case.
func (s *SQLiteStore) RecentQSOsByCall(call string, n int) ([]*QSO, error) {
	call = strings.ToUpper(call)
	rows, err := s.db.Query(`SELECT qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, sat_name, freq_rx, qslmsg, hash, first_seen_at, updated_at,
		qsl_sent_local, qsl_sent_method_local, qsl_rcvd_local, qslsdate_local, qslrdate_local, qsl_sent_as
		FROM qsos WHERE call=? ORDER BY qso_date DESC, time_on DESC LIMIT ?`,
		call, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQSOs(rows)
}

// GetQSO returns a single QSO by its key, or nil if not found. Unlike
// RecentQSOsByCall it is not subject to any LIMIT, so queued QSOs are always
// found regardless of how many newer QSOs share the call.
func (s *SQLiteStore) GetQSO(qslKey string) (*QSO, error) {
	rows, err := s.db.Query(`SELECT qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, sat_name, freq_rx, qslmsg, hash, first_seen_at, updated_at,
		qsl_sent_local, qsl_sent_method_local, qsl_rcvd_local, qslsdate_local, qslrdate_local, qsl_sent_as
		FROM qsos WHERE qsl_key=?`, qslKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanQSOs(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

// AllQSOs returns every QSO, ordered newest-first. Used by qualifier + queue
// scans; at tens of k rows this is a few ms.
func (s *SQLiteStore) AllQSOs() ([]*QSO, error) {
	rows, err := s.db.Query(`SELECT qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, sat_name, freq_rx, qslmsg, hash, first_seen_at, updated_at,
		qsl_sent_local, qsl_sent_method_local, qsl_rcvd_local, qslsdate_local, qslrdate_local, qsl_sent_as
		FROM qsos ORDER BY qso_date DESC, time_on DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQSOs(rows)
}

func scanQSOs(rows *sql.Rows) ([]*QSO, error) {
	var out []*QSO
	for rows.Next() {
		q := &QSO{}
		if err := scanQSOInto(q, rows); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// qsoColumns is the column list scanQSOInto expects, with a table prefix
// (e.g. "q.") for joined queries.
func qsoColumns(prefix string) string {
	cols := []string{"qsl_key", "call", "qso_date", "time_on", "band", "mode", "freq",
		"rst_sent", "rst_rcvd", "qsl_sent", "qsl_rcvd", "qslsdate", "qslrdate",
		"lotw_qsl_rcvd", "dxcc", "prop_mode", "gridsquare", "operator", "notes",
		"name", "qth", "sat_name", "freq_rx", "qslmsg", "hash", "first_seen_at", "updated_at",
		"qsl_sent_local", "qsl_sent_method_local", "qsl_rcvd_local", "qslsdate_local", "qslrdate_local", "qsl_sent_as"}
	for i := range cols {
		cols[i] = prefix + cols[i]
	}
	return strings.Join(cols, ", ")
}

// scanQSOInto scans one row (columns in qsoColumns order, then any extra
// destinations) into q.
func scanQSOInto(q *QSO, sc interface{ Scan(dest ...any) error }, extra ...any) error {
	dest := []any{
		&q.QSLKey, &q.Call, &q.QSODate, &q.TimeOn, &q.Band, &q.Mode, &q.Freq,
		&q.RSTSent, &q.RSTRcvd, &q.QSLSent, &q.QSLRcvd, &q.QSLSDate, &q.QSLRDate,
		&q.LoTWQSLRcvd, &q.DXCC, &q.PropMode, &q.Gridsquare, &q.Operator, &q.Notes,
		&q.Name, &q.QTH, &q.SatName, &q.FreqRX, &q.QSLMsg,
		&q.Hash, &q.FirstSeenAt, &q.UpdatedAt,
		&q.QSLSentLocal, &q.QSLSentMethodLocal, &q.QSLRcvdLocal, &q.QSLSDateLocal, &q.QSLRDateLocal, &q.QSLSentAs,
	}
	return sc.Scan(append(dest, extra...)...)
}

// EffectiveSent reports whether a paper QSL for this QSO counts as sent, with
// the method and date. qslotter's local state (not yet pushed) wins over what
// Clublog last reported.
func (q *QSO) EffectiveSent() (sent bool, method, date string) {
	if q.QSLSentLocal.Valid && q.QSLSentLocal.String == "Y" {
		return true, q.QSLSentMethodLocal.String, q.QSLSDateLocal.String
	}
	if q.SentPerLog() {
		return true, q.QSLSentAs.String, q.QSLSDate
	}
	return false, "", ""
}

// SentPerLog reports that the log (Clublog, Log4OM) has the card as sent:
// QSL_SENT=Y, or a QSL sent date - Clublog's export carries QSLSDATE but
// never QSL_SENT, so the date is its only "sent" signal.
func (q *QSO) SentPerLog() bool {
	return q.QSLSent == "Y" || (q.QSLSent != "N" && q.QSLSDate != "")
}

// EffectiveRcvd reports whether a card from the other station is on record
// (local state first, then Clublog), with the date.
func (q *QSO) EffectiveRcvd() (rcvd bool, date string) {
	if q.QSLRcvdLocal.Valid && q.QSLRcvdLocal.String == "Y" {
		return true, q.QSLRDateLocal.String
	}
	if q.QSLRcvd == "Y" {
		return true, q.QSLRDate
	}
	return false, ""
}

// callLikeRe matches a part that looks like a callsign (ends in letters after
// a digit: DL1ABC, K1A, VK9X) rather than a bare prefix (KH6, EA8, 3D2).
var callLikeRe = regexp.MustCompile(`^[A-Z0-9]*[0-9][A-Z]+$`)

// BaseCall reduces a callsign with prefix/suffix to the operator's base call:
// "EA8/DL1ABC/P" -> "DL1ABC", "W1AW/1" -> "W1AW", "KH6/K1A" -> "K1A".
// Suffix parts (P, M, MM, AM, QRP, a single letter/digit) are dropped; of the
// remaining parts one that looks like a callsign beats a bare prefix, then the
// longer wins, then the later one (ITU form PREFIX/CALL: VP2V/W1AW -> W1AW).
func BaseCall(call string) string {
	call = strings.ToUpper(strings.TrimSpace(call))
	best, bestScore := "", -1
	for _, p := range strings.Split(call, "/") {
		switch p {
		case "", "P", "M", "MM", "AM", "QRP", "QRPP", "LH":
			continue
		}
		if len(p) <= 1 {
			continue
		}
		score := len(p)
		if callLikeRe.MatchString(p) {
			score += 100
		}
		if score >= bestScore {
			best, bestScore = p, score
		}
	}
	if best == "" {
		return call
	}
	return best
}

// HistoryRow is one QSO with a station and where its card stands.
type HistoryRow struct {
	QSO           *QSO
	QueueStatus   string // "" when the QSO was never queued
	DesiredMethod string
	Manager       string
}

// CallHistory returns the newest QSOs with a station, base-call aware: a
// portable operation (EA8/DL1ABC, DL1ABC/P) is the same station as DL1ABC.
// Each row carries the card's queue state.
func (s *SQLiteStore) CallHistory(call string, limit int) ([]*HistoryRow, error) {
	base := BaseCall(call)
	rows, err := s.db.Query(`SELECT `+qsoColumns("q.")+`,
		COALESCE(w.status,''), COALESCE(w.desired_method,''), COALESCE(w.manager,'')
		FROM qsos q LEFT JOIN qsl_work_queue w ON w.qsl_key = q.qsl_key
		WHERE q.call = ? OR q.call LIKE ? OR q.call LIKE ? OR q.call LIKE ?
		ORDER BY q.qso_date DESC, q.time_on DESC, q.qsl_key LIMIT ?`,
		base, base+"/%", "%/"+base, "%/"+base+"/%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*HistoryRow
	for rows.Next() {
		h := &HistoryRow{QSO: &QSO{}}
		if err := scanQSOInto(h.QSO, rows, &h.QueueStatus, &h.DesiredMethod, &h.Manager); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetQSLSentLocal marks a QSO as sent in qslotter's local state, outside the
// card lifecycle (the app sends cards through the queue transitions; tests
// seed with this). It records the given
// method (B/D/E/M) and today's date. The flag stored in qsl_sent_local is
// always "Y" (the ADIF QSL_SENT enum); the method goes to
// qsl_sent_method_local and is uploaded as QSL_SENT_VIA on push-back. These
// columns drive push-back to Clublog.
func (s *SQLiteStore) SetQSLSentLocal(qslKey, method string) error {
	today := time.Now().UTC().Format("20060102")
	method = strings.ToUpper(method)
	switch method {
	case "N":
		return fmt.Errorf("method N is not a send method") // "none" = decision not to send
	case "B", "D", "E", "M":
		// a real send method
	default:
		method = "" // e.g. "Y": sent flag only, no method recorded
	}
	_, err := s.db.Exec(`UPDATE qsos SET qsl_sent_local='Y', qsl_sent_method_local=?, qslsdate_local=? WHERE qsl_key=?`,
		method, today, qslKey)
	return err
}

func (s *SQLiteStore) SetQSLRcvdLocal(qslKey string) error {
	today := time.Now().UTC().Format("20060102")
	_, err := s.db.Exec(`UPDATE qsos SET qsl_rcvd_local='Y', qslrdate_local=? WHERE qsl_key=?`,
		today, qslKey)
	return err
}

// BookReceived records their card for the QSOs (QSL_RCVD=Y, today, for
// push-back) with an event each, in one transaction. QSOs whose card is on
// record already are left alone; booked counts the others.
func (s *SQLiteStore) BookReceived(keys []string) (booked int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	today := time.Now().UTC().Format("20060102")
	for _, key := range keys {
		res, err := tx.Exec(`UPDATE qsos SET qsl_rcvd_local='Y', qslrdate_local=? WHERE qsl_key=?
			AND COALESCE(qsl_rcvd_local,'') <> 'Y' AND COALESCE(qsl_rcvd,'') <> 'Y'`, today, key)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		booked++
		if err := appendEventTx(tx, &Event{QSLKey: key, Direction: "rcvd", Method: "Y", Note: "their card received"}); err != nil {
			return 0, err
		}
	}
	return booked, tx.Commit()
}

// pendingPushWhere selects QSOs whose local QSL state diverges from what
// Clublog last reported.
const pendingPushWhere = `(qsl_sent_local IS NOT NULL AND (qsl_sent IS NULL OR qsl_sent <> qsl_sent_local))
	   OR (qsl_sent_method_local IS NOT NULL AND (qsl_sent_as IS NULL OR qsl_sent_as <> qsl_sent_method_local))
	   OR (qsl_rcvd_local IS NOT NULL AND (qsl_rcvd IS NULL OR qsl_rcvd <> qsl_rcvd_local)
	       AND NOT (qsl_rcvd_local = 'R' AND COALESCE(qsl_rcvd,'') = 'Y'))`

// PendingPushBack returns all QSOs whose local QSL state diverges from the
// Clublog-sourced state (i.e. we have a local change not yet pushed). A QSO
// whose sent-flag already matches but whose chosen send method was not yet
// uploaded also counts as pending (QSL_SENT_VIA needs updating).
func (s *SQLiteStore) PendingPushBack() ([]*QSO, error) {
	rows, err := s.db.Query(`SELECT qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, sat_name, freq_rx, qslmsg, hash, first_seen_at, updated_at,
		qsl_sent_local, qsl_sent_method_local, qsl_rcvd_local, qslsdate_local, qslrdate_local, qsl_sent_as
		FROM qsos
		WHERE ` + pendingPushWhere)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQSOs(rows)
}

// MarkPushed clears the local divergence for the QSOs in an uploaded snapshot
// (after a successful push-back). Each local column is copied into its
// Clublog-sourced counterpart and cleared ONLY if it still holds the value
// that was part of the uploaded snapshot - state recorded while the upload
// was in flight stays pending and is picked up by the next push.
func (s *SQLiteStore) MarkPushed(snapshot *QSO) error {
	qslKey := snapshot.QSLKey
	_, err := s.db.Exec(`UPDATE qsos SET
		qsl_sent = CASE WHEN qsl_sent_local IS NOT NULL AND qsl_sent_local IS ? THEN qsl_sent_local ELSE qsl_sent END,
		qsl_sent_as = CASE WHEN qsl_sent_method_local IS NOT NULL AND qsl_sent_method_local IS ? THEN qsl_sent_method_local ELSE qsl_sent_as END,
		qsl_rcvd = CASE WHEN qsl_rcvd_local IS NOT NULL AND qsl_rcvd_local IS ? THEN qsl_rcvd_local ELSE qsl_rcvd END,
		qslsdate = CASE WHEN qslsdate_local IS NOT NULL AND qslsdate_local IS ? THEN qslsdate_local ELSE qslsdate END,
		qslrdate = CASE WHEN qslrdate_local IS NOT NULL AND qslrdate_local IS ? THEN qslrdate_local ELSE qslrdate END,
		qsl_sent_local = CASE WHEN qsl_sent_local IS ? THEN NULL ELSE qsl_sent_local END,
		qsl_sent_method_local = CASE WHEN qsl_sent_method_local IS ? THEN NULL ELSE qsl_sent_method_local END,
		qsl_rcvd_local = CASE WHEN qsl_rcvd_local IS ? THEN NULL ELSE qsl_rcvd_local END,
		qslsdate_local = CASE WHEN qslsdate_local IS ? THEN NULL ELSE qslsdate_local END,
		qslrdate_local = CASE WHEN qslrdate_local IS ? THEN NULL ELSE qslrdate_local END
		WHERE qsl_key=?`,
		snapshot.QSLSentLocal, snapshot.QSLSentMethodLocal, snapshot.QSLRcvdLocal,
		snapshot.QSLSDateLocal, snapshot.QSLRDateLocal,
		snapshot.QSLSentLocal, snapshot.QSLSentMethodLocal, snapshot.QSLRcvdLocal,
		snapshot.QSLSDateLocal, snapshot.QSLRDateLocal,
		qslKey)
	return err
}

// --- work queue ---

type QueueItem struct {
	QSLKey         string
	DesiredMethod  string // route B/D/M ("" while open), N no card, R requested
	Manager        string
	Status         string
	OverrideReason string
	AddedAt        string
	PrintedAt      sql.NullString
	SentAt         sql.NullString
	SendVia        string // how a sent card travelled: B or D ("" for legacy cards)
	Note           string // "written now", "backlog", a request's note, ...
	Channel        string // requested cards: OQRS / PayPal / e-mail / other
	CardNote       string // printed on the card (template field qslmsg)
}

// queueItemColumns is the column list scanQueueItem expects.
const queueItemColumns = `q.qsl_key, COALESCE(q.desired_method,''), COALESCE(q.manager,''), q.status,
	COALESCE(q.override_reason,''), q.added_at, q.printed_at, q.sent_at,
	COALESCE(q.send_via,''), COALESCE(q.note,''), COALESCE(q.channel,''), COALESCE(q.card_note,'')`

func scanQueueItem(sc interface{ Scan(dest ...any) error }) (*QueueItem, error) {
	qi := &QueueItem{}
	err := sc.Scan(&qi.QSLKey, &qi.DesiredMethod, &qi.Manager, &qi.Status, &qi.OverrideReason,
		&qi.AddedAt, &qi.PrintedAt, &qi.SentAt, &qi.SendVia, &qi.Note, &qi.Channel, &qi.CardNote)
	return qi, err
}

func (s *SQLiteStore) Enqueue(item *QueueItem) error {
	if item.AddedAt == "" {
		item.AddedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if item.Status == "" {
		item.Status = "queued"
	}
	// DO NOTHING: an existing item keeps its status and decision - enqueueing
	// (UDP, pull, recompute) must never reset work the operator already did.
	_, err := s.db.Exec(`INSERT INTO qsl_work_queue(qsl_key, desired_method, manager, status, override_reason, added_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(qsl_key) DO NOTHING`,
		item.QSLKey, item.DesiredMethod, item.Manager, item.Status, item.OverrideReason, item.AddedAt)
	return err
}

func (s *SQLiteStore) QueueByStatus(status string) ([]*QueueItem, error) {
	rows, err := s.db.Query(`SELECT `+queueItemColumns+`
		FROM qsl_work_queue q WHERE q.status=? ORDER BY q.added_at`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueueItem
	for rows.Next() {
		qi, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, qi)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) QueueGet(qslKey string) (*QueueItem, error) {
	qi, err := scanQueueItem(s.db.QueryRow(`SELECT `+queueItemColumns+`
		FROM qsl_work_queue q WHERE q.qsl_key=?`, qslKey))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return qi, nil
}

// --- guarded queue transitions ---

// ErrConflict is returned by the queue transitions when the item is missing or
// not in a state the transition may start from: a stale page, a second window
// or a double click already handled it.
var ErrConflict = errors.New("card was already handled (stale page?)")

// queueTx runs fn in a transaction after checking that the item's current
// status is one of from. Only tx may be used inside fn (in-memory databases
// give every extra connection its own empty schema).
func (s *SQLiteStore) queueTx(key string, from []string, fn func(tx *sql.Tx, cur *QueueItem) error) error {
	return s.queueTxMany([]string{key}, from, fn)
}

// queueTxMany is queueTx for several items moved together (one card covering
// several QSOs): every item must be in one of from, else nothing changes and
// the answer is ErrConflict; fn runs once per item, all in one transaction.
func (s *SQLiteStore) queueTxMany(keys []string, from []string, fn func(tx *sql.Tx, cur *QueueItem) error) error {
	if len(keys) == 0 {
		return ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	var items []*QueueItem
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		cur := &QueueItem{QSLKey: key}
		var method, manager sql.NullString
		err = tx.QueryRow(`SELECT status, desired_method, manager FROM qsl_work_queue WHERE qsl_key=?`, key).
			Scan(&cur.Status, &method, &manager)
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		cur.DesiredMethod, cur.Manager = method.String, manager.String
		allowed := false
		for _, f := range from {
			if cur.Status == f {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrConflict
		}
		items = append(items, cur)
	}
	for _, cur := range items {
		if err := fn(tx, cur); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func appendEventTx(tx *sql.Tx, e *Event) error {
	_, err := tx.Exec(`INSERT INTO qsl_events(qsl_key, direction, method, via, date, source, note)
		VALUES(?,?,?,?,?,'manual',?)`, e.QSLKey, e.Direction, e.Method, e.Via,
		time.Now().UTC().Format("20060102"), e.Note)
	return err
}

// QueueList returns the items in any of the given statuses, newest QSO first
// (the QSO just logged is the one to decide). Items whose QSO row is gone are
// not listed.
func (s *SQLiteStore) QueueList(statuses ...string) ([]*QueueItem, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.db.Query(`SELECT `+queueItemColumns+`
		FROM qsl_work_queue q JOIN qsos s ON s.qsl_key = q.qsl_key
		WHERE q.status IN (`+strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+`)
		ORDER BY s.qso_date DESC, s.time_on DESC, q.qsl_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueueItem
	for rows.Next() {
		qi, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, qi)
	}
	return out, rows.Err()
}

// Route is how a card goes out: chosen at the Desk when the card is written or
// printed, or bureau/direct for a card written now in the Inbox.
type Route struct {
	Method  string // B bureau, D direct, M via manager
	Via     string // how the card travels: B or D (equal to Method unless M)
	Manager string // the manager's callsign (Method M only)
}

// ErrBadRoute is returned when a card is finished without a valid route.
var ErrBadRoute = errors.New("invalid route")

// normalize validates a route: B and D travel as themselves, a manager route
// needs a manager callsign and travels via B or D.
func (rt Route) normalize() (Route, error) {
	rt.Method = strings.ToUpper(strings.TrimSpace(rt.Method))
	rt.Via = strings.ToUpper(strings.TrimSpace(rt.Via))
	rt.Manager = strings.ToUpper(strings.TrimSpace(rt.Manager))
	switch rt.Method {
	case "B", "D":
		return Route{Method: rt.Method, Via: rt.Method}, nil
	case "M":
		if rt.Manager == "" {
			return rt, fmt.Errorf("%w: via manager needs the manager's callsign", ErrBadRoute)
		}
		if rt.Via != "B" && rt.Via != "D" {
			return rt, fmt.Errorf("%w: via manager needs bureau or direct", ErrBadRoute)
		}
		return rt, nil
	case "":
		return rt, fmt.Errorf("%w: choose a route (bureau, direct or via manager)", ErrBadRoute)
	}
	return rt, fmt.Errorf("%w: unknown route %q", ErrBadRoute, rt.Method)
}

// QueueAccept records the Inbox decision "yes, card": queued -> decided with
// the route still open; it is chosen at the Desk when the card is finished.
func (s *SQLiteStore) QueueAccept(key string) error {
	return s.queueTx(key, []string{"queued"}, func(tx *sql.Tx, _ *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='decided', desired_method='', manager='', send_via='', note=''
			WHERE qsl_key=?`, key); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: key, Direction: "decision", Method: "Y", Note: "yes, card"})
	})
}

// finishSent completes a card with its route: status sent, local sent state
// for push-back (QSL_SENT_VIA = how it travelled; the manager goes to
// QSL_VIA from the queue row).
func finishSent(tx *sql.Tx, key string, rt Route, how, note string) error {
	now := time.Now().UTC()
	var printedAt sql.NullString
	if how == "printed" {
		printedAt = sql.NullString{String: now.Format(time.RFC3339), Valid: true}
	}
	if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='sent', desired_method=?, manager=?, send_via=?, note=?,
		sent_at=?, printed_at=COALESCE(printed_at, ?) WHERE qsl_key=?`,
		rt.Method, rt.Manager, rt.Via, note, now.Format(time.RFC3339), printedAt, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE qsos SET qsl_sent_local='Y', qsl_sent_method_local=?, qslsdate_local=? WHERE qsl_key=?`,
		rt.Via, now.Format("20060102"), key); err != nil {
		return err
	}
	if note != "" {
		how = note
	}
	return appendEventTx(tx, &Event{QSLKey: key, Direction: "sent", Method: rt.Method, Via: rt.Manager, Note: how + " via " + rt.Via})
}

// QueueWrittenNow records "written now" straight from the Inbox (queued): a
// card filled in during the QSO, which goes bureau or direct, never via a
// manager.
func (s *SQLiteStore) QueueWrittenNow(keys []string, rt Route) error {
	rt, err := rt.normalize()
	if err != nil {
		return err
	}
	if rt.Method == "M" {
		return fmt.Errorf("%w: a card written now goes bureau or direct", ErrBadRoute)
	}
	return s.queueTxMany(keys, []string{"queued"}, func(tx *sql.Tx, cur *QueueItem) error {
		return finishSent(tx, cur.QSLKey, rt, "written", "written now")
	})
}

// QueueWritten marks a Desk card (decided) written by hand with its route;
// keys are the QSOs the card covers.
func (s *SQLiteStore) QueueWritten(keys []string, rt Route) error {
	rt, err := rt.normalize()
	if err != nil {
		return err
	}
	return s.queueTxMany(keys, []string{"decided"}, func(tx *sql.Tx, cur *QueueItem) error {
		return finishSent(tx, cur.QSLKey, rt, "written", "")
	})
}

// Printing is two-step (docs/STATES.md): a Desk card is sent to printing
// (decided -> toprint, route and card note fixed), a print run sends every
// card waiting there as one job (toprint -> printing), and the run is
// confirmed as a whole (printing -> sent) once the cards came out right;
// single cards are reprinted or taken back to the print queue before that.
// One run is open at a time.

// QueueToPrint puts a Desk card into the print queue with its route and the
// note printed on it; keys are the QSOs the card covers.
func (s *SQLiteStore) QueueToPrint(keys []string, rt Route, note string) error {
	rt, err := rt.normalize()
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	return s.queueTxMany(keys, []string{"decided"}, func(tx *sql.Tx, cur *QueueItem) error {
		return toPrintTx(tx, cur.QSLKey, rt, note)
	})
}

func toPrintTx(tx *sql.Tx, key string, rt Route, note string) error {
	if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='toprint', desired_method=?, manager=?, send_via=?,
		card_note=?, note='' WHERE qsl_key=?`, rt.Method, rt.Manager, rt.Via, note, key); err != nil {
		return err
	}
	return appendEventTx(tx, &Event{QSLKey: key, Direction: "print", Method: rt.Method, Via: rt.Manager, Note: "to print via " + rt.Via})
}

// QueueUnprint takes cards out of the print queue back to the Desk
// (toprint -> decided); route and note stay as the preselection.
func (s *SQLiteStore) QueueUnprint(keys ...string) error {
	return s.printMove(keys, "toprint", "decided", "back to the Desk")
}

// QueueRunFailed puts a run whose job never reached the printer back into
// the print queue (printing -> toprint).
func (s *SQLiteStore) QueueRunFailed(keys ...string) error {
	return s.printMove(keys, "printing", "toprint", "print job failed")
}

// QueueRunBack takes cards of the open run back to the print queue (printing
// -> toprint): a misprint to correct, or nothing came out.
func (s *SQLiteStore) QueueRunBack(keys ...string) error {
	return s.printMove(keys, "printing", "toprint", "back to the print queue")
}

// QueueReprint records that cards of the open run were printed again; they
// stay in the run.
func (s *SQLiteStore) QueueReprint(keys ...string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.queueTxMany(keys, []string{"printing"}, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET printed_at=? WHERE qsl_key=?`, now, cur.QSLKey); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: cur.QSLKey, Direction: "print", Note: "reprinted"})
	})
}

func (s *SQLiteStore) printMove(keys []string, from, to, note string) error {
	return s.queueTxMany(keys, []string{from}, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status=? WHERE qsl_key=?`, to, cur.QSLKey); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: cur.QSLKey, Direction: "print", Note: note})
	})
}

// Print run conflicts; both are an ErrConflict.
var (
	ErrRunOpen        = fmt.Errorf("%w: the last print run is not confirmed yet", ErrConflict)
	ErrNothingToPrint = fmt.Errorf("%w: nothing waits to be printed", ErrConflict)
)

// QueueStartRun opens a print run: every card in the print queue moves to
// printing. ErrConflict when a run is still open (confirm it first) or
// nothing waits to be printed.
func (s *SQLiteStore) QueueStartRun() ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM qsl_work_queue WHERE status='printing'`).Scan(&open); err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, ErrRunOpen
	}
	keys, err := keysInTx(tx, "toprint")
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrNothingToPrint
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, k := range keys {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='printing', printed_at=? WHERE qsl_key=?`, now, k); err != nil {
			return nil, err
		}
		if err := appendEventTx(tx, &Event{QSLKey: k, Direction: "print", Note: "printed"}); err != nil {
			return nil, err
		}
	}
	return keys, tx.Commit()
}

// QueueConfirmRun confirms the open print run as a whole: every printed card
// is sent with its route (local sent state for push-back).
func (s *SQLiteStore) QueueConfirmRun() ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keys, err := keysInTx(tx, "printing")
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrConflict
	}
	for _, k := range keys {
		var rt Route
		if err := tx.QueryRow(`SELECT COALESCE(desired_method,''), COALESCE(send_via,''), COALESCE(manager,'')
			FROM qsl_work_queue WHERE qsl_key=?`, k).Scan(&rt.Method, &rt.Via, &rt.Manager); err != nil {
			return nil, err
		}
		if err := finishSent(tx, k, rt, "printed", ""); err != nil {
			return nil, err
		}
	}
	return keys, tx.Commit()
}

func keysInTx(tx *sql.Tx, status string) ([]string, error) {
	rows, err := tx.Query(`SELECT qsl_key FROM qsl_work_queue WHERE status=? ORDER BY qsl_key`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// Request is how the station's card was requested instead of sending one
// (VISION B4b): the channel and a free-text note (amount, date, reference).
type Request struct {
	Channel string // OQRS, PayPal, e-mail, other
	Note    string
}

// RequestChannels are the accepted request channels, in display order.
var RequestChannels = []string{"OQRS", "PayPal", "e-mail", "other"}

// QueueRequested records "requested (OQRS)" for a Desk card: no own card goes
// out, the station's card is requested. Status requested, the channel and
// note on the queue row, sent_at = when it was requested; the QSO gets
// QSL_RCVD=R for push-back (QSL_SENT stays as it is).
func (s *SQLiteStore) QueueRequested(keys []string, req Request) error {
	channel := ""
	for _, c := range RequestChannels {
		if strings.EqualFold(strings.TrimSpace(req.Channel), c) {
			channel = c
		}
	}
	if channel == "" {
		return fmt.Errorf("%w: request channel must be one of %s", ErrBadRoute, strings.Join(RequestChannels, ", "))
	}
	note := strings.TrimSpace(req.Note)
	now := time.Now().UTC()
	return s.queueTxMany(keys, []string{"decided"}, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='requested', desired_method='R', manager='', send_via='',
			channel=?, note=?, sent_at=? WHERE qsl_key=?`, channel, note, now.Format(time.RFC3339), cur.QSLKey); err != nil {
			return err
		}
		// Only where it changes something: never over a received card (Y), and
		// not when Clublog already has the request (R) - a stale local R would
		// later be pushed over the Y that arrives with their card.
		if _, err := tx.Exec(`UPDATE qsos SET qsl_rcvd_local='R' WHERE qsl_key=?
			AND COALESCE(qsl_rcvd_local,'') NOT IN ('Y','R') AND COALESCE(qsl_rcvd,'') NOT IN ('Y','R')`, cur.QSLKey); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: cur.QSLKey, Direction: "rcvd", Method: "R", Via: channel,
			Note: strings.TrimSpace("requested via " + channel + " " + note)})
	})
}

// QueueDecline records the Inbox decision "no card": queued -> skipped.
func (s *SQLiteStore) QueueDecline(keys ...string) error {
	return s.decline([]string{"queued"}, keys)
}

// QueueDeskDecline is "no card after all" at the Desk (B6): decided ->
// skipped, for the QSOs of the card.
func (s *SQLiteStore) QueueDeskDecline(keys ...string) error {
	return s.decline([]string{"decided"}, keys)
}

func (s *SQLiteStore) decline(from, keys []string) error {
	return s.queueTxMany(keys, from, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='skipped', desired_method='N', manager='', send_via='', note=''
			WHERE qsl_key=?`, cur.QSLKey); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: cur.QSLKey, Direction: "decision", Method: "N", Note: "decision: no paper QSL"})
	})
}

// QueueBack takes Desk cards back to the Inbox (decided -> queued).
func (s *SQLiteStore) QueueBack(keys ...string) error {
	return s.queueTxMany(keys, []string{"decided"}, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='queued', desired_method='', manager='', send_via='', note=''
			WHERE qsl_key=?`, cur.QSLKey); err != nil {
			return err
		}
		return appendEventTx(tx, &Event{QSLKey: cur.QSLKey, Direction: "decision", Note: "back to decision"})
	})
}

// QueueReply puts the QSOs of a received card that need an answer onto the
// Desk ("yes, card", route open), whatever the Inbox said about them: a QSO
// never queued (digital mode, before the cutoff) gets a queue item, one in
// the Inbox is accepted, a "no card" decision (also the backlog) is
// overruled by their card, and a request their card answered may still get
// your card. A QSO whose card went out or sits in the print queue, or a
// request whose card has not arrived, is a conflict (stale page); one
// already at the Desk is left as it is.
func (s *SQLiteStore) QueueReply(keys []string) error {
	return s.replyTx(keys, nil)
}

// QueueReplyFinish is QueueReply and the answer in one transaction: how
// "written" (written by hand now, sent with rt) or "toprint" (into the print
// queue with rt and the card note).
func (s *SQLiteStore) QueueReplyFinish(keys []string, how string, rt Route, note string) error {
	rt, err := rt.normalize()
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	switch how {
	case "written":
		return s.replyTx(keys, func(tx *sql.Tx, key string) error { return finishSent(tx, key, rt, "written", "") })
	case "toprint":
		return s.replyTx(keys, func(tx *sql.Tx, key string) error { return toPrintTx(tx, key, rt, note) })
	}
	return fmt.Errorf("unknown reply %q", how)
}

func (s *SQLiteStore) replyTx(keys []string, then func(tx *sql.Tx, key string) error) error {
	if len(keys) == 0 {
		return ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		var status sql.NullString
		err := tx.QueryRow(`SELECT status FROM qsl_work_queue WHERE qsl_key=?`, key).Scan(&status)
		accept := true
		switch {
		case err == sql.ErrNoRows:
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM qsos WHERE qsl_key=?`, key).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrConflict
			}
			if _, err := tx.Exec(`INSERT INTO qsl_work_queue(qsl_key, desired_method, manager, status, override_reason, added_at)
				VALUES(?, '', '', 'decided', 'reply to their card', ?)`, key, now); err != nil {
				return err
			}
		case err != nil:
			return err
		case status.String == "queued" || status.String == "skipped":
		case status.String == "requested":
			var rcvd int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM qsos WHERE qsl_key=?
				AND (COALESCE(qsl_rcvd_local,'')='Y' OR COALESCE(qsl_rcvd,'')='Y')`, key).Scan(&rcvd); err != nil {
				return err
			}
			if rcvd == 0 {
				return ErrConflict // still expected: nothing to answer yet
			}
		case status.String == "decided":
			accept = false
		default:
			return ErrConflict
		}
		if accept && status.Valid {
			if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='decided', desired_method='', manager='', send_via='',
				channel='', note='' WHERE qsl_key=?`, key); err != nil {
				return err
			}
		}
		if accept {
			if err := appendEventTx(tx, &Event{QSLKey: key, Direction: "decision", Method: "Y", Note: "reply to their card"}); err != nil {
				return err
			}
		}
		if then != nil {
			if err := then(tx, key); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// QueueReopen puts a finished card (sent, declined or requested) back into
// the Inbox and clears its unpushed local state. inClublog reports what
// Clublog already holds and reopening cannot undo there: "sent" (QSL_SENT=Y)
// or "requested" (QSL_RCVD=R); "" when nothing.
func (s *SQLiteStore) QueueReopen(key string) (inClublog string, err error) {
	err = s.queueTx(key, []string{"sent", "skipped", "requested"}, func(tx *sql.Tx, cur *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='queued', desired_method='', manager='',
			send_via='', channel='', note='', sent_at=NULL, printed_at=NULL WHERE qsl_key=?`, key); err != nil {
			return err
		}
		if cur.Status == "requested" {
			if _, err := tx.Exec(`UPDATE qsos SET qsl_rcvd_local=NULL WHERE qsl_key=? AND qsl_rcvd_local='R'`, key); err != nil {
				return err
			}
			var rcvd sql.NullString
			if err := tx.QueryRow(`SELECT qsl_rcvd FROM qsos WHERE qsl_key=?`, key).Scan(&rcvd); err != nil && err != sql.ErrNoRows {
				return err
			}
			if rcvd.String == "R" {
				inClublog = "requested"
			}
		}
		if cur.Status == "sent" {
			if _, err := tx.Exec(`UPDATE qsos SET qsl_sent_local=NULL, qsl_sent_method_local=NULL, qslsdate_local=NULL
				WHERE qsl_key=?`, key); err != nil {
				return err
			}
			var sent, sdate sql.NullString
			if err := tx.QueryRow(`SELECT qsl_sent, qslsdate FROM qsos WHERE qsl_key=?`, key).Scan(&sent, &sdate); err != nil && err != sql.ErrNoRows {
				return err
			}
			if (&QSO{QSLSent: sent.String, QSLSDate: sdate.String}).SentPerLog() {
				inClublog = "sent"
			}
		}
		return appendEventTx(tx, &Event{QSLKey: key, Direction: "decision", Note: "reopened"})
	})
	return inClublog, err
}

// QueueCloseSentElsewhere closes a card that is still open (queued, decided
// or in the print queue) because Clublog reports QSL_SENT=Y for the QSO - the
// card went out through another tool. Local sent columns are left alone:
// there is nothing to push. A card of the open print run is not touched: it
// is printed, the operator confirms it.
func (s *SQLiteStore) QueueCloseSentElsewhere(key string) error {
	return s.queueTx(key, []string{"queued", "decided", "toprint"}, func(tx *sql.Tx, _ *QueueItem) error {
		if _, err := tx.Exec(`UPDATE qsl_work_queue SET status='sent', note='sent elsewhere', sent_at=? WHERE qsl_key=?`,
			time.Now().UTC().Format(time.RFC3339), key); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO qsl_events(qsl_key, direction, method, via, date, source, note)
			VALUES(?, 'sent', '', '', ?, 'clublog', 'closed: Clublog reports QSL sent')`,
			key, time.Now().UTC().Format("20060102"))
		return err
	})
}

// QueueDiscardBacklog files every QSO dated before the cutoff that still waits
// for an Inbox decision as "no card" (note "backlog") in one transaction.
// QSOs forced in by the override marker stay: the operator flagged them.
// Reopenable from Done like any other decision.
func (s *SQLiteStore) QueueDiscardBacklog(before string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const backlog = `SELECT w.qsl_key FROM qsl_work_queue w JOIN qsos q ON q.qsl_key = w.qsl_key
		WHERE w.status='queued' AND q.qso_date < ? AND COALESCE(w.override_reason,'') = ''`
	if _, err := tx.Exec(`INSERT INTO qsl_events(qsl_key, direction, method, via, date, source, note)
		SELECT qsl_key, 'decision', 'N', '', ?, 'auto', 'backlog: before the cutoff, filed as no card'
		FROM (`+backlog+`)`, time.Now().UTC().Format("20060102"), before); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE qsl_work_queue SET status='skipped', desired_method='N', manager='', send_via='', note='backlog'
		WHERE qsl_key IN (`+backlog+`)`, before)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// QueueCounts feeds the nav badges: QSOs awaiting a decision, Desk cards
// (distinct calls) awaiting production, and QSOs with local QSL state not yet
// pushed to Clublog.
func (s *SQLiteStore) QueueCounts() (queued, decided, pendingPush int, err error) {
	// The Desk counts cards: QSOs with the same call share one.
	// The print queue and the open run are Desk cards too.
	rows, err := s.db.Query(`SELECT CASE WHEN status='queued' THEN 'queued' ELSE 'decided' END AS st,
			CASE WHEN status='queued' THEN COUNT(*)
			ELSE COUNT(DISTINCT status || '|' || upper(substr(qsl_key, 1, instr(qsl_key, '|') - 1))) END
		FROM qsl_work_queue WHERE status IN ('queued','decided','toprint','printing') GROUP BY st`)
	if err != nil {
		return 0, 0, 0, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		if st == "queued" {
			queued = n
		} else {
			decided = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM qsos WHERE ` + pendingPushWhere).Scan(&pendingPush)
	return queued, decided, pendingPush, err
}

// ExpectedCount counts the open requests (B4b) whose card has not arrived;
// QSOs requested together (one card) count once.
func (s *SQLiteStore) ExpectedCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT upper(q.call) || '|' || COALESCE(w.sent_at,'') || '|' || COALESCE(w.channel,'') || '|' || COALESCE(w.note,''))
		FROM qsl_work_queue w JOIN qsos q ON q.qsl_key = w.qsl_key
		WHERE w.status = 'requested' AND COALESCE(q.qsl_rcvd_local,'') <> 'Y' AND COALESCE(q.qsl_rcvd,'') <> 'Y'`).Scan(&n)
	return n, err
}

// --- events ---

type Event struct {
	ID        int64
	QSLKey    string
	Direction string
	Method    string
	Via       string
	Date      string
	Source    string
	Note      string
}

func (s *SQLiteStore) AppendEvent(e *Event) error {
	_, err := s.db.Exec(`INSERT INTO qsl_events(qsl_key, direction, method, via, date, source, note)
		VALUES(?,?,?,?,?,?,?)`, e.QSLKey, e.Direction, e.Method, e.Via, e.Date, e.Source, e.Note)
	return err
}

// --- station info cache ---

type StationInfo struct {
	Callsign  string
	QSLMgr    string
	EQSL      string
	MQSL      string
	LoTW      string
	Email     string
	Addr1     string
	Addr2     string
	State     string
	Zip       string
	Country   string
	DXCC      string
	BioText   string
	Name      string // QRZ first + last name
	Attn      string // QRZ "attn" line
	NotFound  bool   // QRZ has no record of this call (negative cache entry)
	FetchedAt string
}

func (s *SQLiteStore) GetStation(callsign string) (*StationInfo, error) {
	callsign = strings.ToUpper(callsign)
	si := &StationInfo{Callsign: callsign}
	var notFound int
	// The qsl_method / qsl_route / refuse_paper / qsl_confidence / qsl_reason
	// columns are legacy: the suggestion is computed on read from the raw
	// fields (internal/qsldetermine), never stored.
	err := s.db.QueryRow(`SELECT callsign, qslmgr, eqsl, mqsl, lotw, email, addr1, addr2,
		state, zip, country, dxcc, bio_text, COALESCE(name,''), COALESCE(attn,''), COALESCE(not_found,0), fetched_at
		FROM station_info WHERE callsign=?`, callsign).Scan(
		&si.Callsign, &si.QSLMgr, &si.EQSL, &si.MQSL, &si.LoTW, &si.Email, &si.Addr1, &si.Addr2,
		&si.State, &si.Zip, &si.Country, &si.DXCC, &si.BioText, &si.Name, &si.Attn, &notFound, &si.FetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	si.NotFound = notFound != 0
	return si, nil
}

func (s *SQLiteStore) PutStation(si *StationInfo) error {
	si.Callsign = strings.ToUpper(si.Callsign)
	if si.FetchedAt == "" {
		si.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}
	nf := 0
	if si.NotFound {
		nf = 1
	}
	// The five legacy suggestion columns are still written (empty): they are
	// nullable without defaults, and an older binary scanning NULLs into
	// strings would fail the whole row after a rollback.
	_, err := s.db.Exec(`INSERT INTO station_info (callsign, qslmgr, eqsl, mqsl, lotw, email,
		addr1, addr2, state, zip, country, dxcc, bio_text, qsl_method, qsl_route, refuse_paper,
		qsl_confidence, qsl_reason, name, attn, not_found, fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?, '', '', 0, '', '', ?,?,?,?)
		ON CONFLICT(callsign) DO UPDATE SET
			qslmgr=excluded.qslmgr, eqsl=excluded.eqsl, mqsl=excluded.mqsl, lotw=excluded.lotw,
			email=excluded.email, addr1=excluded.addr1, addr2=excluded.addr2, state=excluded.state,
			zip=excluded.zip, country=excluded.country, dxcc=excluded.dxcc, bio_text=excluded.bio_text,
			qsl_method='', qsl_route='', refuse_paper=0, qsl_confidence='', qsl_reason='',
			name=excluded.name, attn=excluded.attn,
			not_found=excluded.not_found, fetched_at=excluded.fetched_at`,
		si.Callsign, si.QSLMgr, si.EQSL, si.MQSL, si.LoTW, si.Email, si.Addr1, si.Addr2,
		si.State, si.Zip, si.Country, si.DXCC, si.BioText, si.Name, si.Attn, nf, si.FetchedAt)
	return err
}
