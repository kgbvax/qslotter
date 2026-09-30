package store

import (
	"database/sql"
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
	si := &StationInfo{Callsign: "DL1ABC", QSLMgr: "DL2DEF", QSLMethod: "M", QSLRoute: "DL2DEF"}
	if err := st.PutStation(si); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetStation("DL1ABC")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.QSLMethod != "M" {
		t.Fatalf("GetStation = %+v", got)
	}
}

func TestQueueMethodPersistence(t *testing.T) {
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

	item, err := st.QueueGet(q.QSLKey)
	if err != nil || item == nil {
		t.Fatalf("QueueGet = %v, %v", item, err)
	}

	// Record a direct decision, then a manager decision.
	if err := st.QueueSetMethod(q.QSLKey, "d", ""); err != nil {
		t.Fatal(err)
	}
	item, _ = st.QueueGet(q.QSLKey)
	if item.DesiredMethod != "D" {
		t.Fatalf("DesiredMethod = %q, want D", item.DesiredMethod)
	}
	if err := st.QueueSetMethod(q.QSLKey, "M", "DL2XYZ"); err != nil {
		t.Fatal(err)
	}
	item, _ = st.QueueGet(q.QSLKey)
	if item.DesiredMethod != "M" || item.Manager != "DL2XYZ" {
		t.Fatalf("QueueGet = %+v, want M/DL2XYZ", item)
	}
	// The decision survives a status change (recompute never overwrites it).
	if err := st.QueueSetStatus(q.QSLKey, "printed"); err != nil {
		t.Fatal(err)
	}
	item, _ = st.QueueGet(q.QSLKey)
	if item.DesiredMethod != "M" || item.Status != "printed" {
		t.Fatalf("after status change: %+v", item)
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

// TestMigrateFromV1Schema opens a database that still has the v1 tables
// (without the newer columns) and verifies ensureSchema adds them.
func TestMigrateFromV1Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	v1 := `
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
	si := &StationInfo{Callsign: "DL1ABC", QSLConfidence: "high", QSLReason: "qslmgr field"}
	if err := st.PutStation(si); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetStation("DL1ABC")
	if got == nil || got.QSLConfidence != "high" || got.QSLReason != "qslmgr field" {
		t.Fatalf("GetStation after migration = %+v", got)
	}
}
