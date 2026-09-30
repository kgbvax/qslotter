package qualify

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dl9et/qslotter/internal/store"
)

func newStore(t *testing.T) *store.SQLiteStore {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.(*store.SQLiteStore)
}

func qso(call, date, time, band, mode string) *store.QSO {
	return &store.QSO{
		QSLKey:  call + "|" + date + "|" + time + "|" + band,
		Call:    call, QSODate: date, TimeOn: time, Band: band, Mode: mode,
		Hash:    "h-" + call + date + time + band,
	}
}

func TestEligibleModeExcluded(t *testing.T) {
	r := &Rules{ExcludeModes: []string{"FT8"}, FirstContactOnly: false}
	q := qso("DL1AB", "20240101", "120000", "20m", "FT8")
	ok, reason := r.Eligible(q, []*store.QSO{q})
	if ok {
		t.Fatalf("FT8 should be excluded; got ok=true (%s)", reason)
	}
}

func TestEligibleDigitalPrefixFallback(t *testing.T) {
	// FST4 is not in the explicit list but matches the FT/FST prefix fallback.
	r := &Rules{ExcludeModes: []string{"FT8"}, FirstContactOnly: false}
	q := qso("DL1AB", "20240101", "120000", "20m", "FST4")
	ok, _ := r.Eligible(q, []*store.QSO{q})
	if ok {
		t.Fatal("FST4 should be excluded by prefix fallback")
	}
}

func TestEligibleFirstContactOnly(t *testing.T) {
	r := &Rules{FirstContactOnly: true}
	// Two QSOs with DL1AB: q1 newer, q2 older.
	q1 := qso("DL1AB", "20240102", "130000", "20m", "SSB")
	q2 := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	all := []*store.QSO{q1, q2}
	// q1 (newer) has an older prior (q2) -> not first contact.
	ok, _ := r.Eligible(q1, all)
	if ok {
		t.Fatal("q1 should be ineligible (q2 is an older prior QSO with DL1AB)")
	}
	// q2 (older) has no older prior -> first contact -> eligible.
	ok, _ = r.Eligible(q2, all)
	if !ok {
		t.Fatal("q2 should be eligible (no older prior)")
	}
	// A lone QSO with no priors -> first contact.
	solo := qso("DL3EF", "20240103", "140000", "20m", "SSB")
	ok, _ = r.Eligible(solo, []*store.QSO{solo})
	if !ok {
		t.Fatal("solo QSO should be eligible (no prior)")
	}
}

func TestEligibleOverride(t *testing.T) {
	r := &Rules{FirstContactOnly: true, OverrideMarker: "QSL!"}
	// A second QSO with DL1AB that has the override marker in notes.
	q := qso("DL1AB", "20240102", "130000", "20m", "SSB")
	q.Notes = "memorable QSO - QSL!"
	prior := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	all := []*store.QSO{q, prior}
	ok, reason := r.Eligible(q, all)
	if !ok {
		t.Fatalf("override should force-include; got ok=false (%s)", reason)
	}
	if reason != "override: QSL! in notes" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEligibleAlreadySent(t *testing.T) {
	r := &Rules{}
	q := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q.QSLSent = "Y"
	ok, _ := r.Eligible(q, []*store.QSO{q})
	if ok {
		t.Fatal("already-sent QSO should be ineligible")
	}
}

func TestEligibleForNewQSOFirstContact(t *testing.T) {
	r := &Rules{FirstContactOnly: true}
	// q is the newer QSO; prior is older.
	q := qso("DL1AB", "20240102", "130000", "20m", "SSB")
	prior := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	// priorQSOs includes q (already upserted) + the older prior.
	ok, _ := r.EligibleForNewQSO(q, []*store.QSO{q, prior})
	if ok {
		t.Fatal("should be ineligible with an older prior QSO present")
	}
	// Only the new QSO itself -> no older prior -> first contact.
	ok, _ = r.EligibleForNewQSO(q, []*store.QSO{q})
	if !ok {
		t.Fatal("should be eligible with no older prior QSO")
	}
}

func TestEnqueueAll(t *testing.T) {
	st := newStore(t)
	// Insert 3 QSOs: one SSB first-contact (eligible), one FT8 (excluded),
	// one SSB with a prior QSO (first-contact rule excludes).
	q1 := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q2 := qso("DL2CD", "20240101", "130000", "20m", "FT8")
	q3 := qso("DL1AB", "20240102", "140000", "20m", "SSB")
	for _, q := range []*store.QSO{q3, q2, q1} { // insert newest-first like AllQSOs returns
		_, _, err := st.UpsertQSO(q)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := &Rules{FirstContactOnly: true}
	n, err := r.EnqueueAll(st)
	if err != nil {
		t.Fatal(err)
	}
	// q1 (older DL1AB) is first-contact -> eligible.
	// q3 (newer DL1AB) has q1 as older prior -> ineligible.
	// q2 is FT8 -> excluded.
	if n != 1 {
		t.Fatalf("EnqueueAll enqueued %d, want 1 (q1)", n)
	}
	// Re-running should be a no-op (q1 already enqueued).
	n, _ = r.EnqueueAll(st)
	if n != 0 {
		t.Fatalf("second EnqueueAll enqueued %d, want 0", n)
	}
	// Verify the queue contents.
	items, _ := st.QueueByStatus("queued")
	if len(items) != 1 {
		t.Fatalf("queue has %d items, want 1", len(items))
	}
	if items[0].QSLKey != q1.QSLKey {
		t.Fatalf("queue QSLKey = %q, want %q", items[0].QSLKey, q1.QSLKey)
	}
}

func TestEnqueueAllRespectsTerminalStatus(t *testing.T) {
	st := newStore(t)
	q := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	_, _, _ = st.UpsertQSO(q)
	// Manually mark it skipped.
	_ = st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "skipped"})
	r := &Rules{FirstContactOnly: true}
	n, _ := r.EnqueueAll(st)
	if n != 0 {
		t.Fatalf("EnqueueAll should not re-enqueue a skipped QSO; got %d", n)
	}
}

func TestEnqueueAllOverride(t *testing.T) {
	st := newStore(t)
	// A second QSO with DL1AB that has the override marker - should be enqueued
	// despite the first-contact rule.
	q1 := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q2 := qso("DL1AB", "20240102", "130000", "20m", "SSB")
	q2.Notes = "memorable QSL!"
	_, _, _ = st.UpsertQSO(q1)
	_, _, _ = st.UpsertQSO(q2)
	r := &Rules{FirstContactOnly: true, OverrideMarker: "QSL!"}
	n, _ := r.EnqueueAll(st)
	// q1 (older) is first-contact -> eligible.
	// q2 (newer) has override marker -> force-included.
	if n != 2 {
		t.Fatalf("EnqueueAll with override enqueued %d, want 2", n)
	}
}

// Ensure sql.NullString is referenced (kept in store.QSO; used by Eligible).
var _ = sql.NullString{}