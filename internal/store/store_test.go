package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Meta
	if err := st.MetaSet("foo", "bar"); err != nil {
		t.Fatal(err)
	}
	v, err := st.MetaGet("foo")
	if err != nil {
		t.Fatal(err)
	}
	if v != "bar" {
		t.Fatalf("MetaGet = %q, want bar", v)
	}

	// Upsert a QSO
	q := &QSO{
		QSLKey: "DL1ABC|20240101|120000|20m", Call: "DL1ABC",
		QSODate: "20240101", TimeOn: "120000", Band: "20m", Mode: "SSB",
		RSTSent: "59", Hash: "h1",
	}
	isNew, changed, err := st.UpsertQSO(q)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || !changed {
		t.Fatal("first UpsertQSO should report isNew=true, changed=true")
	}

	// Re-upsert same hash -> not changed
	isNew, changed, err = st.UpsertQSO(q)
	if err != nil {
		t.Fatal(err)
	}
	if isNew || changed {
		t.Fatal("second UpsertQSO with same hash should report isNew=false, changed=false")
	}

	// Upsert with new hash -> changed (but not new)
	q.Hash = "h2"
	isNew, changed, err = st.UpsertQSO(q)
	if err != nil {
		t.Fatal(err)
	}
	if isNew || !changed {
		t.Fatal("UpsertQSO with new hash should report isNew=false, changed=true")
	}

	// Recent QSOs by call
	recs, err := st.RecentQSOsByCall("DL1ABC", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Call != "DL1ABC" {
		t.Fatalf("RecentQSOsByCall = %+v", recs)
	}

	// Mark sent/received locally
	if err := st.SetQSLSentLocal(q.QSLKey, "Y"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQSLRcvdLocal(q.QSLKey); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingPushBack()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingPushBack = %d, want 1", len(pending))
	}

	// Mark pushed -> clears local divergence (snapshot from the pending read)
	pending[0].QSLKey = q.QSLKey
	if err := st.MarkPushed(pending[0]); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingPushBack()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingPushBack after MarkPushed = %d, want 0", len(pending))
	}

	// Station info cache
	si := &StationInfo{Callsign: "DL1ABC", QSLMgr: "DL2DEF", Name: "Hans Meier"}
	if err := st.PutStation(si); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetStation("DL1ABC")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.QSLMgr != "DL2DEF" || got.Name != "Hans Meier" {
		t.Fatalf("GetStation = %+v", got)
	}
}

// TestEnqueueKeepsDecision: enqueueing an item that already exists (UDP,
// pull, recompute) never resets its status or route.
func TestEnqueueKeepsDecision(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	q := &QSO{QSLKey: "DL1ABC|20240101|120000|20m", Call: "DL1ABC",
		QSODate: "20240101", TimeOn: "120000", Band: "20m", Mode: "SSB", Hash: "h1"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(q.QSLKey); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{q.QSLKey}, Route{Method: "M", Via: "D", Manager: "DL2XYZ"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	item, _ := st.QueueGet(q.QSLKey)
	if item.Status != "sent" || item.DesiredMethod != "M" || item.Manager != "DL2XYZ" || item.SendVia != "D" {
		t.Fatalf("re-enqueue reset the decision: %+v", item)
	}

	// QueueGet on an unknown key returns nil, not an error.
	if item, err := st.QueueGet("NOPE|1|1|1"); err != nil || item != nil {
		t.Fatalf("QueueGet unknown = %v, %v", item, err)
	}
}

func TestSetQSLSentLocalRecordsMethod(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	q := &QSO{QSLKey: "DL1ABC|20240101|120000|20m", Call: "DL1ABC",
		QSODate: "20240101", TimeOn: "120000", Band: "20m", Mode: "SSB", Hash: "h1"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}

	// "none" is a decision not to send, never a sent flag.
	if err := st.SetQSLSentLocal(q.QSLKey, "N"); err == nil {
		t.Fatal("SetQSLSentLocal with N should fail")
	}

	// A real method: sent flag is Y, method rides along separately.
	if err := st.SetQSLSentLocal(q.QSLKey, "D"); err != nil {
		t.Fatal(err)
	}
	recs, err := st.PendingPushBack()
	if err != nil || len(recs) != 1 {
		t.Fatalf("PendingPushBack = %d, %v", len(recs), err)
	}
	if recs[0].QSLSentLocal.String != "Y" {
		t.Fatalf("QSLSentLocal = %q, want Y (ADIF QSL_SENT enum)", recs[0].QSLSentLocal.String)
	}
	if recs[0].QSLSentMethodLocal.String != "D" {
		t.Fatalf("QSLSentMethodLocal = %q, want D", recs[0].QSLSentMethodLocal.String)
	}

	// After MarkPushed the method is remembered in qsl_sent_as, locals cleared.
	if err := st.MarkPushed(recs[0]); err != nil {
		t.Fatal(err)
	}
	all, err := st.AllQSOs()
	if err != nil {
		t.Fatal(err)
	}
	if all[0].QSLSentAs.String != "D" || all[0].QSLSentLocal.Valid {
		t.Fatalf("after MarkPushed: QSLSentAs=%v QSLSentLocal=%v", all[0].QSLSentAs, all[0].QSLSentLocal)
	}
	if pending, _ := st.PendingPushBack(); len(pending) != 0 {
		t.Fatalf("pending after MarkPushed = %d, want 0", len(pending))
	}

	// MarkPushed with a STALE snapshot must not swallow newer local state:
	// record a method change after the pending read, mark the old snapshot
	// pushed, and the new state has to stay pending.
	if err := st.SetQSLSentLocal(q.QSLKey, "M"); err != nil {
		t.Fatal(err)
	}
	pending2, _ := st.PendingPushBack()
	if err := st.MarkPushed(pending2[0]); err != nil { // snapshot has M
		t.Fatal(err)
	}
	// Change the method AFTER the snapshot was taken but BEFORE MarkPushed.
	if err := st.SetQSLSentLocal(q.QSLKey, "D"); err != nil {
		t.Fatal(err)
	}
	stale := *pending2[0] // snapshot still says M
	if err := st.MarkPushed(&stale); err != nil {
		t.Fatal(err)
	}
	left, _ := st.PendingPushBack()
	if len(left) != 1 || left[0].QSLSentMethodLocal.String != "D" {
		t.Fatalf("stale MarkPushed swallowed newer state: left=%d method=%q",
			len(left), left[0].QSLSentMethodLocal.String)
	}

	// Non-method values ("Y") record a sent flag without a method.
	if err := st.SetQSLSentLocal(q.QSLKey, "Y"); err != nil {
		t.Fatal(err)
	}
	recs, _ = st.PendingPushBack()
	if len(recs) != 1 || recs[0].QSLSentMethodLocal.String != "" {
		t.Fatalf("plain-Y send: pending=%d method=%q, want 1/empty", len(recs), recs[0].QSLSentMethodLocal.String)
	}
}

const v1SchemaDDL = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE qsos (
  qsl_key TEXT PRIMARY KEY, call TEXT NOT NULL, qso_date TEXT NOT NULL,
  time_on TEXT NOT NULL, band TEXT NOT NULL, mode TEXT NOT NULL, freq TEXT,
  rst_sent TEXT, rst_rcvd TEXT, qsl_sent TEXT, qsl_rcvd TEXT, qslsdate TEXT,
  qslrdate TEXT, lotw_qsl_rcvd TEXT, dxcc TEXT, prop_mode TEXT, gridsquare TEXT,
  operator TEXT, notes TEXT, hash TEXT NOT NULL, first_seen_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, qsl_sent_local TEXT, qsl_rcvd_local TEXT,
  qslsdate_local TEXT, qslrdate_local TEXT
);
CREATE TABLE station_info (
  callsign TEXT PRIMARY KEY, qslmgr TEXT, eqsl TEXT, mqsl TEXT, lotw TEXT,
  email TEXT, addr1 TEXT, addr2 TEXT, state TEXT, zip TEXT, country TEXT,
  dxcc TEXT, bio_text TEXT, qsl_method TEXT, qsl_route TEXT,
  refuse_paper INTEGER DEFAULT 0, fetched_at TEXT NOT NULL
);
CREATE TABLE qsl_work_queue (
  qsl_key TEXT PRIMARY KEY REFERENCES qsos(qsl_key), desired_method TEXT,
  manager TEXT, status TEXT NOT NULL, override_reason TEXT, added_at TEXT NOT NULL,
  printed_at TEXT, sent_at TEXT
);
CREATE TABLE qsl_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, qsl_key TEXT NOT NULL, direction TEXT NOT NULL,
  method TEXT, via TEXT, date TEXT NOT NULL, source TEXT NOT NULL, note TEXT
);
INSERT INTO meta(key, value) VALUES('schema_version', '1');`

// TestMigrateFromV1Schema opens a database that still has the v1 tables
// (without the newer columns) and verifies ensureSchema adds them.
func TestMigrateFromV1Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	v1 := v1SchemaDDL
	if _, err := db.Exec(v1); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v1 database: %v", err)
	}
	defer st.Close()

	// New columns are usable end-to-end.
	q := &QSO{QSLKey: "DL1ABC|20240101|120000|20m", Call: "DL1ABC",
		QSODate: "20240101", TimeOn: "120000", Band: "20m", Mode: "SSB",
		Name: "Alice", QTH: "Bavaria", Hash: "h1"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RecentQSOsByCall("DL1ABC", 1)
	if err != nil || len(recs) != 1 || recs[0].Name != "Alice" || recs[0].QTH != "Bavaria" {
		t.Fatalf("RecentQSOsByCall after migration = %+v, %v", recs, err)
	}
	if err := st.SetQSLSentLocal(q.QSLKey, "B"); err != nil {
		t.Fatal(err)
	}
	si := &StationInfo{Callsign: "DL1ABC", QSLMgr: "direct"}
	if err := st.PutStation(si); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetStation("DL1ABC")
	if got == nil || got.QSLMgr != "direct" {
		t.Fatalf("GetStation after migration = %+v", got)
	}
}

// TestMigrationBackfillsNulls: rows that predate the name/qth/qsl_confidence/
// qsl_reason columns must stay readable after migration (a NULL would make the
// string scan fail and the QSO silently vanish from every list).
func TestMigrationBackfillsNulls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1rows.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	seed := v1SchemaDDL + `
INSERT INTO qsos(qsl_key, call, qso_date, time_on, band, mode, freq, rst_sent, rst_rcvd,
    qsl_sent, qsl_rcvd, qslsdate, qslrdate, lotw_qsl_rcvd, dxcc, prop_mode, gridsquare,
    operator, notes, hash, first_seen_at, updated_at)
  VALUES('DL1ABC|20240101|120000|20m','DL1ABC','20240101','120000','20m','SSB','','59','59',
    '','','','','','','','','','','h','t','t');
INSERT INTO station_info(callsign, qslmgr, eqsl, mqsl, lotw, email, addr1, addr2, state, zip,
    country, dxcc, bio_text, qsl_method, qsl_route, fetched_at)
  VALUES('DL1ABC','','','','','','','','','','','','','','','2024-01-01T00:00:00Z');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if q, err := st.GetQSO("DL1ABC|20240101|120000|20m"); err != nil || q == nil {
		t.Fatalf("GetQSO on migrated row = %v, %v", q, err)
	}
	if all, err := st.AllQSOs(); err != nil || len(all) != 1 {
		t.Fatalf("AllQSOs on migrated row = %d, %v", len(all), err)
	}
	if recent, err := st.RecentQSOsByCall("DL1ABC", 5); err != nil || len(recent) != 1 {
		t.Fatalf("RecentQSOsByCall on migrated row = %d, %v", len(recent), err)
	}
	if si, err := st.GetStation("DL1ABC"); err != nil || si == nil {
		t.Fatalf("GetStation on migrated row = %v, %v", si, err)
	}
}

// --- guarded queue transitions ---

func seedQueued(t *testing.T, st Store, call, date string) string {
	t.Helper()
	q := &QSO{QSLKey: call + "|" + date + "|120000|20m", Call: call, QSODate: date,
		TimeOn: "120000", Band: "20m", Mode: "SSB", Hash: "h-" + call + date}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	return q.QSLKey
}

func openTemp(t *testing.T) Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func statusOf(t *testing.T, st Store, key string) *QueueItem {
	t.Helper()
	it, err := st.QueueGet(key)
	if err != nil || it == nil {
		t.Fatalf("QueueGet(%s) = %v, %v", key, it, err)
	}
	return it
}

func TestQueueAcceptMovesToDeskWithoutRoute(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")

	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, key); it.Status != "decided" || it.DesiredMethod != "" || it.Manager != "" {
		t.Fatalf("after yes: %+v", it)
	}
	if q, _ := st.QueueList("queued"); len(q) != 0 {
		t.Fatalf("accepted item still listed as queued: %v", q)
	}
	if d, _ := st.QueueList("decided"); len(d) != 1 {
		t.Fatalf("accepted item missing from the Desk: %v", d)
	}
	// A stale second "yes" is a conflict, not a silent no-op.
	if err := st.QueueAccept(key); !errors.Is(err, ErrConflict) {
		t.Fatalf("second yes = %v, want ErrConflict", err)
	}
}

func TestQueueWrittenNowRecordsBureauOrDirect(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")
	// Written now needs a route, and never via a manager.
	if err := st.QueueWrittenNow([]string{key}, Route{}); !errors.Is(err, ErrBadRoute) {
		t.Fatalf("written without route = %v, want ErrBadRoute", err)
	}
	if err := st.QueueWrittenNow([]string{key}, Route{Method: "M", Via: "D", Manager: "K2ABC"}); !errors.Is(err, ErrBadRoute) {
		t.Fatalf("written now via manager = %v, want ErrBadRoute", err)
	}
	if it := statusOf(t, st, key); it.Status != "queued" {
		t.Fatalf("a rejected route must not move the card: %+v", it)
	}
	if err := st.QueueWrittenNow([]string{key}, Route{Method: "b"}); err != nil {
		t.Fatal(err)
	}
	it := statusOf(t, st, key)
	if it.Status != "sent" || it.DesiredMethod != "B" || it.SendVia != "B" || it.Note != "written now" || !it.SentAt.Valid {
		t.Fatalf("after written now: %+v", it)
	}
	q, _ := st.GetQSO(key)
	if q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "B" || q.QSLSDateLocal.String == "" {
		t.Fatalf("local sent state: %+v", q)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Fatalf("pending push = %d, want 1", len(pend))
	}
	if err := st.QueueWrittenNow([]string{key}, Route{Method: "B"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("written twice = %v, want ErrConflict", err)
	}
}

func TestQueueWrittenAndPrintedRecordDeskRoute(t *testing.T) {
	st := openTemp(t)
	kw := seedQueued(t, st, "DL1ABC", "20240101")
	kp := seedQueued(t, st, "DL2ZZZ", "20240102")
	for _, k := range []string{kw, kp} {
		if err := st.QueueAccept(k); err != nil {
			t.Fatal(err)
		}
	}
	// Printing needs a Desk card.
	if err := st.QueuePrinted([]string{seedQueued(t, st, "DL3YYY", "20240103")}, Route{Method: "D"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("print of an Inbox card = %v, want ErrConflict", err)
	}
	// A manager route needs the manager and how the card travels.
	if err := st.QueuePrinted([]string{kp}, Route{Method: "M", Via: "B"}); !errors.Is(err, ErrBadRoute) {
		t.Fatalf("manager route without call = %v, want ErrBadRoute", err)
	}
	if err := st.QueuePrinted([]string{kp}, Route{Method: "M", Manager: "K2ABC"}); !errors.Is(err, ErrBadRoute) {
		t.Fatalf("manager route without bureau/direct = %v, want ErrBadRoute", err)
	}
	if err := st.QueueWritten([]string{kw}, Route{Method: "D"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{kp}, Route{Method: "m", Via: "b", Manager: " k2abc "}); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, kw); it.Status != "sent" || it.DesiredMethod != "D" || it.SendVia != "D" || it.Note != "" || it.PrintedAt.Valid {
		t.Fatalf("written at the Desk: %+v", it)
	}
	if it := statusOf(t, st, kp); it.Status != "sent" || it.DesiredMethod != "M" || it.SendVia != "B" || it.Manager != "K2ABC" || !it.PrintedAt.Valid {
		t.Fatalf("printed via manager: %+v", it)
	}
	// The manager card travelled via the bureau: that is its QSL_SENT_VIA.
	if q, _ := st.GetQSO(kp); q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "B" {
		t.Fatalf("manager card local sent state: %+v", q)
	}
}

func TestQueueDiscardBacklog(t *testing.T) {
	st := openTemp(t)
	old := seedQueued(t, st, "DL1ABC", "20240101")
	forced := seedQueued(t, st, "DL2ZZZ", "20240102")
	accepted := seedQueued(t, st, "DL3YYY", "20240103")
	fresh := seedQueued(t, st, "DL4XXX", "20240301")
	db := st.(*SQLiteStore).db
	if _, err := db.Exec(`UPDATE qsl_work_queue SET override_reason='override: QSL! in notes' WHERE qsl_key=?`, forced); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(accepted); err != nil {
		t.Fatal(err)
	}
	n, err := st.QueueDiscardBacklog("20240201")
	if err != nil || n != 1 {
		t.Fatalf("discard = %d, %v; want 1", n, err)
	}
	if it := statusOf(t, st, old); it.Status != "skipped" || it.DesiredMethod != "N" || it.Note != "backlog" {
		t.Fatalf("backlog item: %+v", it)
	}
	if it := statusOf(t, st, forced); it.Status != "queued" {
		t.Fatalf("an override QSO stays in the Inbox: %+v", it)
	}
	if it := statusOf(t, st, accepted); it.Status != "decided" {
		t.Fatalf("a Desk card is not backlog: %+v", it)
	}
	if it := statusOf(t, st, fresh); it.Status != "queued" {
		t.Fatalf("a QSO after the cutoff stays: %+v", it)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM qsl_events WHERE qsl_key=? AND note LIKE 'backlog%'`, old).Scan(&events); err != nil || events != 1 {
		t.Fatalf("backlog event = %d, %v", events, err)
	}
	// Reopenable like any decision.
	if _, err := st.QueueReopen(old); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, old); it.Status != "queued" || it.Note != "" {
		t.Fatalf("reopened backlog item: %+v", it)
	}
}

func TestQueueDeclineBackAndReopen(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")

	if err := st.QueueBack(key); !errors.Is(err, ErrConflict) {
		t.Fatalf("back from queued = %v, want ErrConflict", err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueBack(key); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after back: %+v", it)
	}
	if err := st.QueueDecline(key); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, key); it.Status != "skipped" || it.DesiredMethod != "N" {
		t.Fatalf("after decline: %+v", it)
	}
	if in, err := st.QueueReopen(key); err != nil || in != "" {
		t.Fatalf("reopen declined = %q, %v", in, err)
	}
	if it := statusOf(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after reopen: %+v", it)
	}

	// Reopening a sent card clears the unpushed local sent state.
	if err := st.QueueWrittenNow([]string{key}, Route{Method: "D"}); err != nil {
		t.Fatal(err)
	}
	if in, err := st.QueueReopen(key); err != nil || in != "" {
		t.Fatalf("reopen sent = %q, %v", in, err)
	}
	q, _ := st.GetQSO(key)
	if q.QSLSentLocal.Valid || q.QSLSentMethodLocal.Valid || q.QSLSDateLocal.Valid {
		t.Fatalf("local sent state not cleared: %+v", q)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 0 {
		t.Fatalf("reopened card still pending push: %d", len(pend))
	}
	if _, err := st.QueueReopen(key); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen of queued = %v, want ErrConflict", err)
	}
}

func TestQueueReopenReportsAlreadyPushed(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{key}, Route{Method: "B"}); err != nil {
		t.Fatal(err)
	}
	pend, _ := st.PendingPushBack()
	if err := st.MarkPushed(pend[0]); err != nil {
		t.Fatal(err)
	}
	if in, err := st.QueueReopen(key); err != nil || in != "sent" {
		t.Fatalf("reopen after push = %q, %v; want sent", in, err)
	}
}

func TestQueueListNewestFirstAndCounts(t *testing.T) {
	st := openTemp(t)
	old := seedQueued(t, st, "DL1ABC", "20240101")
	mid := seedQueued(t, st, "DL2ZZZ", "20240201")
	newest := seedQueued(t, st, "DL3YYY", "20240301")
	if err := st.QueueAccept(mid); err != nil {
		t.Fatal(err)
	}
	got, err := st.QueueList("queued", "decided")
	if err != nil || len(got) != 3 || got[0].QSLKey != newest || got[1].QSLKey != mid || got[2].QSLKey != old {
		t.Fatalf("order = %v, %v", got, err)
	}
	if q, d, p, err := st.QueueCounts(); err != nil || q != 2 || d != 1 || p != 0 {
		t.Fatalf("counts = %d/%d/%d, %v", q, d, p, err)
	}
	if err := st.QueueWrittenNow([]string{old}, Route{Method: "B"}); err != nil {
		t.Fatal(err)
	}
	if q, d, p, _ := st.QueueCounts(); q != 1 || d != 1 || p != 1 {
		t.Fatalf("counts after written = %d/%d/%d", q, d, p)
	}
}

func TestEnqueueDoesNotClobberDecision(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&QueueItem{QSLKey: key, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, key); it.Status != "decided" {
		t.Fatalf("re-enqueue reset the decision: %+v", it)
	}
}

func TestLegacyPrintedRepaired(t *testing.T) {
	st := openTemp(t)
	withRoute := seedQueued(t, st, "DL1ABC", "20240101")
	noRoute := seedQueued(t, st, "DL2ZZZ", "20240102")
	db := st.(*SQLiteStore).db
	if _, err := db.Exec(`UPDATE qsl_work_queue SET status='printed', desired_method='D',
		printed_at='2024-05-06T10:00:00Z' WHERE qsl_key=?`, withRoute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE qsl_work_queue SET status='printed', printed_at='2024-05-06T10:00:00Z' WHERE qsl_key=?`, noRoute); err != nil {
		t.Fatal(err)
	}
	if err := st.(*SQLiteStore).ensureSchema(); err != nil {
		t.Fatal(err)
	}
	if it := statusOf(t, st, withRoute); it.Status != "sent" || it.DesiredMethod != "D" {
		t.Fatalf("printed with route: %+v", it)
	}
	q, _ := st.GetQSO(withRoute)
	if q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "D" || q.QSLSDateLocal.String != "20240506" {
		t.Fatalf("legacy printed card not marked sent locally: %+v", q)
	}
	if it := statusOf(t, st, noRoute); it.Status != "queued" {
		t.Fatalf("printed without route should go back to the decision queue: %+v", it)
	}
}

func TestBaseCall(t *testing.T) {
	for in, want := range map[string]string{
		"DL1ABC": "DL1ABC", "dl1abc": "DL1ABC", "DL1ABC/P": "DL1ABC", "EA8/DL1ABC": "DL1ABC",
		"EA8/DL1ABC/P": "DL1ABC", "W1AW/1": "W1AW", "VK9/DL1ABC/MM": "DL1ABC", "K1ABC/QRP": "K1ABC",
		// a prefix as long as the call is not the station
		"KH6/K1A": "K1A", "OH0/K1A": "K1A", "VP2V/W1AW": "W1AW", "VK9X/K1ZZ": "K1ZZ", "W1AW/KH6": "W1AW",
	} {
		if got := BaseCall(in); got != want {
			t.Errorf("BaseCall(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCallHistoryBaseCallAndQueueState(t *testing.T) {
	st := openTemp(t)
	add := func(call, date string) string {
		q := &QSO{QSLKey: call + "|" + date + "|120000|20m", Call: call, QSODate: date,
			TimeOn: "120000", Band: "20m", Mode: "SSB", Hash: "h" + call + date}
		if _, _, err := st.UpsertQSO(q); err != nil {
			t.Fatal(err)
		}
		return q.QSLKey
	}
	k1 := add("DL1ABC", "20240101")
	k2 := add("DL1ABC/P", "20240201")
	k3 := add("EA8/DL1ABC", "20240301")
	add("DL1ABCD", "20240401") // a different station that merely starts the same
	add("XDL1ABC", "20240501") // ... or ends the same
	if err := st.Enqueue(&QueueItem{QSLKey: k1, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(k1); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{k1}, Route{Method: "M", Via: "D", Manager: "k2abc"}); err != nil {
		t.Fatal(err)
	}

	// The lookup works from any spelling of the station.
	for _, from := range []string{"DL1ABC", "EA8/DL1ABC", "dl1abc/p"} {
		hist, err := st.CallHistory(from, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) != 3 || hist[0].QSO.QSLKey != k3 || hist[1].QSO.QSLKey != k2 || hist[2].QSO.QSLKey != k1 {
			var keys []string
			for _, h := range hist {
				keys = append(keys, h.QSO.QSLKey)
			}
			t.Fatalf("CallHistory(%q) = %v", from, keys)
		}
	}
	hist, _ := st.CallHistory("DL1ABC", 10)
	if h := hist[2]; h.QueueStatus != "sent" || h.DesiredMethod != "M" || h.Manager != "K2ABC" {
		t.Fatalf("queue state missing from history row: %+v", h)
	}
	if h := hist[0]; h.QueueStatus != "" {
		t.Fatalf("never-queued QSO must have no queue state: %+v", h)
	}
	if hist, _ := st.CallHistory("DL1ABC", 2); len(hist) != 2 {
		t.Fatalf("limit ignored: %d", len(hist))
	}
}

func TestEffectiveSentAndRcvd(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	// Local (unpushed) state wins over Clublog's view.
	q := &QSO{QSLSent: "N", QSLSentLocal: ns("Y"), QSLSentMethodLocal: ns("D"), QSLSDateLocal: ns("20240506")}
	if sent, m, d := q.EffectiveSent(); !sent || m != "D" || d != "20240506" {
		t.Fatalf("local sent = %v %q %q", sent, m, d)
	}
	// After push: local cleared, Clublog holds Y and the pushed method.
	q = &QSO{QSLSent: "Y", QSLSDate: "20240506", QSLSentAs: ns("B")}
	if sent, m, d := q.EffectiveSent(); !sent || m != "B" || d != "20240506" {
		t.Fatalf("clublog sent = %v %q %q", sent, m, d)
	}
	if sent, _, _ := (&QSO{QSLSent: "N"}).EffectiveSent(); sent {
		t.Fatal("N is not sent")
	}
	if r, d := (&QSO{QSLRcvd: "Y", QSLRDate: "20240102"}).EffectiveRcvd(); !r || d != "20240102" {
		t.Fatalf("clublog rcvd = %v %q", r, d)
	}
	if r, d := (&QSO{QSLRcvdLocal: ns("Y"), QSLRDateLocal: ns("20240103")}).EffectiveRcvd(); !r || d != "20240103" {
		t.Fatalf("local rcvd = %v %q", r, d)
	}
}

func TestStationInfoNameAttnNotFound(t *testing.T) {
	st := openTemp(t)
	if err := st.PutStation(&StationInfo{Callsign: "dl1abc", Name: "Hans Meier", Attn: "c/o Club", QSLMgr: "K2ABC"}); err != nil {
		t.Fatal(err)
	}
	si, _ := st.GetStation("DL1ABC")
	if si == nil || si.Name != "Hans Meier" || si.Attn != "c/o Club" || si.NotFound {
		t.Fatalf("GetStation = %+v", si)
	}
	if err := st.PutStation(&StationInfo{Callsign: "XX1XX", NotFound: true}); err != nil {
		t.Fatal(err)
	}
	if si, _ := st.GetStation("XX1XX"); si == nil || !si.NotFound {
		t.Fatalf("negative cache entry = %+v", si)
	}
}

func TestQueueRequested(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")
	if err := st.QueueRequested([]string{key}, Request{Channel: "OQRS"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("request from the Inbox = %v, want ErrConflict", err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueRequested([]string{key}, Request{Channel: "carrier pigeon"}); !errors.Is(err, ErrBadRoute) {
		t.Fatalf("unknown channel = %v, want ErrBadRoute", err)
	}
	if err := st.QueueRequested([]string{key}, Request{Channel: "paypal", Note: " 3 EUR "}); err != nil {
		t.Fatal(err)
	}
	it := statusOf(t, st, key)
	if it.Status != "requested" || it.DesiredMethod != "R" || it.Channel != "PayPal" || it.Note != "3 EUR" || !it.SentAt.Valid {
		t.Fatalf("after requested: %+v", it)
	}
	q, _ := st.GetQSO(key)
	if q.QSLRcvdLocal.String != "R" || q.QSLSentLocal.Valid {
		t.Fatalf("local state: rcvd %v sent %v", q.QSLRcvdLocal, q.QSLSentLocal)
	}
	if qd, d, _, _ := st.QueueCounts(); qd != 0 || d != 0 {
		t.Fatalf("a requested card is neither in the Inbox nor at the Desk: %d/%d", qd, d)
	}
	// After the push Clublog has R: reopening reports it.
	pend, _ := st.PendingPushBack()
	if len(pend) != 1 {
		t.Fatalf("pending = %d", len(pend))
	}
	if err := st.MarkPushed(pend[0]); err != nil {
		t.Fatal(err)
	}
	if in, err := st.QueueReopen(key); err != nil || in != "requested" {
		t.Fatalf("reopen requested after push = %q, %v; want requested", in, err)
	}
}

// TestQueueRequestedKeepsReceivedCard: a card already received stays Y.
func TestQueueRequestedKeepsReceivedCard(t *testing.T) {
	st := openTemp(t)
	key := seedQueued(t, st, "DL1ABC", "20240101")
	if err := st.SetQSLRcvdLocal(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueRequested([]string{key}, Request{Channel: "OQRS"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := st.GetQSO(key); q.QSLRcvdLocal.String != "Y" {
		t.Fatalf("received card overwritten by the request: %v", q.QSLRcvdLocal)
	}
}

// TestQueueManyIsAtomic: a card covering several QSOs moves all or nothing.
func TestQueueManyIsAtomic(t *testing.T) {
	st := openTemp(t)
	k1 := seedQueued(t, st, "DL1ABC", "20240101")
	k2 := seedQueued(t, st, "DL1ABC", "20240102")
	if err := st.QueueAccept(k1); err != nil {
		t.Fatal(err)
	}
	// k2 is still in the Inbox: printing both must fail and change nothing.
	if err := st.QueuePrinted([]string{k1, k2}, Route{Method: "B"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mixed print = %v, want ErrConflict", err)
	}
	if it := statusOf(t, st, k1); it.Status != "decided" {
		t.Fatalf("k1 changed by a failed card move: %+v", it)
	}
	if err := st.QueueAccept(k2); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{k1, k2, k1}, Route{Method: "D"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{k1, k2} {
		if it := statusOf(t, st, k); it.Status != "sent" || it.DesiredMethod != "D" {
			t.Fatalf("%s: %+v", k, it)
		}
	}
	if err := st.QueueBack(); !errors.Is(err, ErrConflict) {
		t.Fatalf("no keys = %v, want ErrConflict", err)
	}
}

func TestQueueCountsDeskCards(t *testing.T) {
	st := openTemp(t)
	for _, k := range []string{seedQueued(t, st, "DL1ABC", "20240101"), seedQueued(t, st, "DL1ABC", "20240102"), seedQueued(t, st, "DL2ZZZ", "20240103")} {
		if err := st.QueueAccept(k); err != nil {
			t.Fatal(err)
		}
	}
	seedQueued(t, st, "DL3YYY", "20240104")
	if q, d, _, err := st.QueueCounts(); err != nil || q != 1 || d != 2 {
		t.Fatalf("counts = %d/%d, %v; want 1 Inbox QSO, 2 Desk cards", q, d, err)
	}
}

// TestRequestNeverDowngradesReceived: a request (R) must never be pushed over
// a card Clublog reports as received (Y) - neither when the pull with Y
// arrives before the push, nor when Clublog already had the request.
func TestRequestNeverDowngradesReceived(t *testing.T) {
	st := openTemp(t)
	pull := func(key, rcvd, hash string) {
		t.Helper()
		q, _ := st.GetQSO(key)
		q.QSLRcvd, q.Hash = rcvd, hash
		if _, _, err := st.UpsertQSO(q); err != nil {
			t.Fatal(err)
		}
	}
	// (b) request recorded, not pushed, then their card arrives (pull Y).
	k1 := seedQueued(t, st, "DL1ABC", "20240101")
	if err := st.QueueAccept(k1); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueRequested([]string{k1}, Request{Channel: "OQRS"}); err != nil {
		t.Fatal(err)
	}
	pull(k1, "Y", "h-y1")
	if pend, _ := st.PendingPushBack(); len(pend) != 0 {
		t.Fatalf("R would be pushed over Y: %d pending", len(pend))
	}
	if q, _ := st.GetQSO(k1); q.QSLRcvdLocal.Valid {
		t.Fatalf("stale local R kept: %v", q.QSLRcvdLocal)
	} else if r, _ := q.EffectiveRcvd(); !r {
		t.Fatal("the received card must count as received")
	}
	// (a) Clublog already has R when the request is recorded.
	k2 := seedQueued(t, st, "DL2ZZZ", "20240102")
	pull(k2, "R", "h-r2")
	if err := st.QueueAccept(k2); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueRequested([]string{k2}, Request{Channel: "OQRS"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := st.GetQSO(k2); q.QSLRcvdLocal.Valid {
		t.Fatalf("no local R when Clublog already has it: %v", q.QSLRcvdLocal)
	}
	pull(k2, "Y", "h-y2")
	if pend, _ := st.PendingPushBack(); len(pend) != 0 {
		t.Fatalf("after Y arrived nothing may be pending: %d", len(pend))
	}
}

// TestInboxAndDeskTransitionsStartFromTheirOwnStatus: a stale Inbox page
// cannot act on a Desk card and vice versa (409, nothing changes).
func TestInboxAndDeskTransitionsStartFromTheirOwnStatus(t *testing.T) {
	st := openTemp(t)
	inbox := seedQueued(t, st, "DL1ABC", "20240101")
	desk := seedQueued(t, st, "DL2ZZZ", "20240102")
	if err := st.QueueAccept(desk); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"written (Desk) on an Inbox QSO": st.QueueWritten([]string{inbox}, Route{Method: "B"}),
		"written now on a Desk QSO":      st.QueueWrittenNow([]string{desk}, Route{Method: "B"}),
		"no card (Inbox) on a Desk QSO":  st.QueueDecline(desk),
		"no card (Desk) on an Inbox QSO": st.QueueDeskDecline(inbox),
		"requested on an Inbox QSO":      st.QueueRequested([]string{inbox}, Request{Channel: "OQRS"}),
		"printed on an Inbox QSO":        st.QueuePrinted([]string{inbox}, Route{Method: "B"}),
	} {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s = %v, want ErrConflict", name, err)
		}
	}
	if statusOf(t, st, inbox).Status != "queued" || statusOf(t, st, desk).Status != "decided" {
		t.Fatal("a refused transition changed a card")
	}
}

// TestQueueReply: a received card's QSOs that need an answer go to the Desk,
// whatever the Inbox said; sent or requested ones are a conflict.
func TestQueueReply(t *testing.T) {
	st := openTemp(t)
	queued := seedQueued(t, st, "DL1ABC", "20240101")
	skipped := seedQueued(t, st, "DL2ZZZ", "20240102")
	decided := seedQueued(t, st, "DL3YYY", "20240103")
	if err := st.QueueDecline(skipped); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(decided); err != nil {
		t.Fatal(err)
	}
	never := &QSO{QSLKey: "DL4XXX|20240104|120000|20m", Call: "DL4XXX", QSODate: "20240104", TimeOn: "120000", Band: "20m", Mode: "FT8", Hash: "h4"}
	if _, _, err := st.UpsertQSO(never); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueReply([]string{queued, skipped, decided, never.QSLKey}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{queued, skipped, decided, never.QSLKey} {
		if it := statusOf(t, st, k); it.Status != "decided" || it.DesiredMethod != "" {
			t.Fatalf("%s after reply: %+v", k, it)
		}
	}
	if it := statusOf(t, st, never.QSLKey); it.OverrideReason != "reply to their card" {
		t.Fatalf("new item reason: %+v", it)
	}
	if err := st.QueueWritten([]string{queued}, Route{Method: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueReply([]string{queued}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reply to a sent card = %v, want ErrConflict", err)
	}
	if err := st.QueueReply([]string{"NOPE|1|1|1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reply to an unknown QSO = %v, want ErrConflict", err)
	}
}

// TestSentPerLog: Clublog exports QSLSDATE but never QSL_SENT; the date
// alone means the card went out.
func TestSentPerLog(t *testing.T) {
	for _, c := range []struct {
		sent, date string
		want       bool
	}{{"Y", "", true}, {"", "20260930", true}, {"N", "20260930", false}, {"", "", false}, {"N", "", false}} {
		q := &QSO{QSLSent: c.sent, QSLSDate: c.date}
		if got := q.SentPerLog(); got != c.want {
			t.Errorf("SentPerLog(%q, %q) = %v", c.sent, c.date, got)
		}
		if sent, _, _ := q.EffectiveSent(); sent != c.want {
			t.Errorf("EffectiveSent(%q, %q) = %v", c.sent, c.date, sent)
		}
	}
}
