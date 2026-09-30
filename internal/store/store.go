// Package store is the persistence layer for qslotter.
//
// The Store interface is backend-agnostic so that v2 can add a CouchDB
// implementation (see internal/store/couchdb.go, deferred). v1 ships only the
// SQLite implementation below, using modernc.org/sqlite (pure Go, no cgo) so
// the binary cross-compiles cleanly to Windows from macOS.
package store

import (
	"database/sql"
	"fmt"
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
	PendingPushBack() ([]*QSO, error)
	MarkPushed(snapshot *QSO) error
	Enqueue(item *QueueItem) error
	QueueByStatus(status string) ([]*QueueItem, error)
	QueueGet(qslKey string) (*QueueItem, error)
	QueueSetStatus(qslKey, status string) error
	QueueSetMethod(qslKey, method, manager string) error
	AppendEvent(e *Event) error
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
	}
	for _, m := range migrations {
		if _, err := s.db.Exec(m.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate %s.%s: %w", m.table, m.col, err)
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
  hash       TEXT NOT NULL,             -- sha256 of canonical ADIF repr
  first_seen_at TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  qsl_sent_local TEXT,                  -- qslotter-managed, not yet pushed to Clublog
  qsl_sent_method_local TEXT,           -- B/D/E/M used when sending (goes to QSL_SENT_AS)
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
  fetched_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS qsl_work_queue (
  qsl_key        TEXT PRIMARY KEY REFERENCES qsos(qsl_key),
  desired_method TEXT,                  -- B/D/M
  manager        TEXT,
  status         TEXT NOT NULL,          -- queued/printed/sent/skipped/overridden
  override_reason TEXT,
  added_at       TEXT NOT NULL,
  printed_at     TEXT,
  sent_at        TEXT
);
CREATE INDEX IF NOT EXISTS queue_status_idx ON qsl_work_queue(status);

CREATE TABLE IF NOT EXISTS qsl_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  qsl_key   TEXT NOT NULL,
  direction TEXT NOT NULL,              -- sent/rcvd
  method    TEXT,
  via       TEXT,
  date      TEXT NOT NULL,
  source    TEXT NOT NULL,               -- auto/manual
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
	Hash               string
	FirstSeenAt        string
	UpdatedAt          string
	QSLSentLocal       sql.NullString
	QSLSentMethodLocal sql.NullString // B/D/E/M chosen in qslotter (goes to QSL_SENT_AS)
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
		name, qth,
		hash, first_seen_at, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?)
	ON CONFLICT(qsl_key) DO UPDATE SET
		call=excluded.call, qso_date=excluded.qso_date, time_on=excluded.time_on,
		band=excluded.band, mode=excluded.mode, freq=excluded.freq,
		rst_sent=excluded.rst_sent, rst_rcvd=excluded.rst_rcvd,
		qsl_sent=excluded.qsl_sent, qsl_rcvd=excluded.qsl_rcvd,
		qslsdate=excluded.qslsdate, qslrdate=excluded.qslrdate,
		lotw_qsl_rcvd=excluded.lotw_qsl_rcvd, dxcc=excluded.dxcc,
		prop_mode=excluded.prop_mode, gridsquare=excluded.gridsquare,
		operator=excluded.operator, notes=excluded.notes,
		name=excluded.name, qth=excluded.qth,
		hash=excluded.hash, updated_at=excluded.updated_at
	WHERE qsos.hash <> excluded.hash`,
		q.QSLKey, q.Call, q.QSODate, q.TimeOn, q.Band, q.Mode, q.Freq,
		q.RSTSent, q.RSTRcvd, q.QSLSent, q.QSLRcvd, q.QSLSDate, q.QSLRDate,
		q.LoTWQSLRcvd, q.DXCC, q.PropMode, q.Gridsquare, q.Operator, q.Notes,
		q.Name, q.QTH,
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
		name, qth, hash, first_seen_at, updated_at,
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
		name, qth, hash, first_seen_at, updated_at,
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
		name, qth, hash, first_seen_at, updated_at,
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
		err := rows.Scan(
			&q.QSLKey, &q.Call, &q.QSODate, &q.TimeOn, &q.Band, &q.Mode, &q.Freq,
			&q.RSTSent, &q.RSTRcvd, &q.QSLSent, &q.QSLRcvd, &q.QSLSDate, &q.QSLRDate,
			&q.LoTWQSLRcvd, &q.DXCC, &q.PropMode, &q.Gridsquare, &q.Operator, &q.Notes,
			&q.Name, &q.QTH,
			&q.Hash, &q.FirstSeenAt, &q.UpdatedAt,
			&q.QSLSentLocal, &q.QSLSentMethodLocal, &q.QSLRcvdLocal, &q.QSLSDateLocal, &q.QSLRDateLocal, &q.QSLSentAs,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// SetQSLSentLocal marks a QSO as sent in qslotter's local state with the given
// method (B/D/E/M) and today's date. The flag stored in qsl_sent_local is
// always "Y" (the ADIF QSL_SENT enum); the method goes to
// qsl_sent_method_local and is uploaded as QSL_SENT_AS on push-back. These
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

// PendingPushBack returns all QSOs whose local QSL state diverges from the
// Clublog-sourced state (i.e. we have a local change not yet pushed). A QSO
// whose sent-flag already matches but whose chosen send method was not yet
// uploaded also counts as pending (QSL_SENT_AS needs updating).
func (s *SQLiteStore) PendingPushBack() ([]*QSO, error) {
	rows, err := s.db.Query(`SELECT qsl_key, call, qso_date, time_on, band, mode, freq,
		rst_sent, rst_rcvd, qsl_sent, qsl_rcvd, qslsdate, qslrdate,
		lotw_qsl_rcvd, dxcc, prop_mode, gridsquare, operator, notes,
		name, qth, hash, first_seen_at, updated_at,
		qsl_sent_local, qsl_sent_method_local, qsl_rcvd_local, qslsdate_local, qslrdate_local, qsl_sent_as
		FROM qsos
		WHERE (qsl_sent_local IS NOT NULL AND (qsl_sent IS NULL OR qsl_sent <> qsl_sent_local))
		   OR (qsl_sent_method_local IS NOT NULL AND (qsl_sent_as IS NULL OR qsl_sent_as <> qsl_sent_method_local))
		   OR (qsl_rcvd_local IS NOT NULL AND (qsl_rcvd IS NULL OR qsl_rcvd <> qsl_rcvd_local))`)
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
	DesiredMethod  string
	Manager        string
	Status         string
	OverrideReason string
	AddedAt        string
	PrintedAt      sql.NullString
	SentAt         sql.NullString
}

func (s *SQLiteStore) Enqueue(item *QueueItem) error {
	if item.AddedAt == "" {
		item.AddedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if item.Status == "" {
		item.Status = "queued"
	}
	_, err := s.db.Exec(`INSERT INTO qsl_work_queue(qsl_key, desired_method, manager, status, override_reason, added_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(qsl_key) DO UPDATE SET
			desired_method=excluded.desired_method, manager=excluded.manager,
			status=excluded.status, override_reason=excluded.override_reason`,
		item.QSLKey, item.DesiredMethod, item.Manager, item.Status, item.OverrideReason, item.AddedAt)
	return err
}

func (s *SQLiteStore) QueueByStatus(status string) ([]*QueueItem, error) {
	rows, err := s.db.Query(`SELECT qsl_key, desired_method, manager, status, override_reason, added_at, printed_at, sent_at
		FROM qsl_work_queue WHERE status=? ORDER BY added_at`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueueItem
	for rows.Next() {
		qi := &QueueItem{}
		if err := rows.Scan(&qi.QSLKey, &qi.DesiredMethod, &qi.Manager, &qi.Status, &qi.OverrideReason, &qi.AddedAt, &qi.PrintedAt, &qi.SentAt); err != nil {
			return nil, err
		}
		out = append(out, qi)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) QueueSetStatus(qslKey, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	switch status {
	case "printed":
		_, err := s.db.Exec(`UPDATE qsl_work_queue SET status=?, printed_at=? WHERE qsl_key=?`, status, now, qslKey)
		return err
	case "sent":
		_, err := s.db.Exec(`UPDATE qsl_work_queue SET status=?, sent_at=? WHERE qsl_key=?`, status, now, qslKey)
		return err
	default:
		_, err := s.db.Exec(`UPDATE qsl_work_queue SET status=? WHERE qsl_key=?`, status, qslKey)
		return err
	}
}

// QueueGet returns a single queue item, or nil if the key is not queued.
func (s *SQLiteStore) QueueGet(qslKey string) (*QueueItem, error) {
	qi := &QueueItem{}
	err := s.db.QueryRow(`SELECT qsl_key, desired_method, manager, status, override_reason, added_at, printed_at, sent_at
		FROM qsl_work_queue WHERE qsl_key=?`, qslKey).
		Scan(&qi.QSLKey, &qi.DesiredMethod, &qi.Manager, &qi.Status, &qi.OverrideReason, &qi.AddedAt, &qi.PrintedAt, &qi.SentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return qi, nil
}

// QueueSetMethod records the operator's method decision on a queue item. The
// decision survives queue recompute (recompute never touches existing items).
func (s *SQLiteStore) QueueSetMethod(qslKey, method, manager string) error {
	method = strings.ToUpper(method)
	_, err := s.db.Exec(`UPDATE qsl_work_queue SET desired_method=?, manager=? WHERE qsl_key=?`,
		method, manager, qslKey)
	return err
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
	Callsign      string
	QSLMgr        string
	EQSL          string
	MQSL          string
	LoTW          string
	Email         string
	Addr1         string
	Addr2         string
	State         string
	Zip           string
	Country       string
	DXCC          string
	BioText       string
	QSLMethod     string
	QSLRoute      string
	RefusePaper   bool
	QSLConfidence string // high/medium/low (from qsldetermine)
	QSLReason     string
	FetchedAt     string
}

func (s *SQLiteStore) GetStation(callsign string) (*StationInfo, error) {
	callsign = strings.ToUpper(callsign)
	si := &StationInfo{Callsign: callsign}
	var refuse int
	err := s.db.QueryRow(`SELECT callsign, qslmgr, eqsl, mqsl, lotw, email, addr1, addr2,
		state, zip, country, dxcc, bio_text, qsl_method, qsl_route, refuse_paper,
		qsl_confidence, qsl_reason, fetched_at
		FROM station_info WHERE callsign=?`, callsign).Scan(
		&si.Callsign, &si.QSLMgr, &si.EQSL, &si.MQSL, &si.LoTW, &si.Email, &si.Addr1, &si.Addr2,
		&si.State, &si.Zip, &si.Country, &si.DXCC, &si.BioText, &si.QSLMethod, &si.QSLRoute, &refuse,
		&si.QSLConfidence, &si.QSLReason, &si.FetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	si.RefusePaper = refuse != 0
	return si, nil
}

func (s *SQLiteStore) PutStation(si *StationInfo) error {
	si.Callsign = strings.ToUpper(si.Callsign)
	r := 0
	if si.RefusePaper {
		r = 1
	}
	if si.FetchedAt == "" {
		si.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO station_info (callsign, qslmgr, eqsl, mqsl, lotw, email,
		addr1, addr2, state, zip, country, dxcc, bio_text, qsl_method, qsl_route, refuse_paper,
		qsl_confidence, qsl_reason, fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?)
		ON CONFLICT(callsign) DO UPDATE SET
			qslmgr=excluded.qslmgr, eqsl=excluded.eqsl, mqsl=excluded.mqsl, lotw=excluded.lotw,
			email=excluded.email, addr1=excluded.addr1, addr2=excluded.addr2, state=excluded.state,
			zip=excluded.zip, country=excluded.country, dxcc=excluded.dxcc, bio_text=excluded.bio_text,
			qsl_method=excluded.qsl_method, qsl_route=excluded.qsl_route,
			refuse_paper=excluded.refuse_paper, qsl_confidence=excluded.qsl_confidence,
			qsl_reason=excluded.qsl_reason, fetched_at=excluded.fetched_at`,
		si.Callsign, si.QSLMgr, si.EQSL, si.MQSL, si.LoTW, si.Email, si.Addr1, si.Addr2,
		si.State, si.Zip, si.Country, si.DXCC, si.BioText, si.QSLMethod, si.QSLRoute, r,
		si.QSLConfidence, si.QSLReason, si.FetchedAt)
	return err
}
