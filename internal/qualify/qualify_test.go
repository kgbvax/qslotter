package qualify

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/config"
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
		QSLKey: call + "|" + date + "|" + time + "|" + band,
		Call:   call, QSODate: date, TimeOn: time, Band: band, Mode: mode,
		Hash: "h-" + call + date + time + band,
	}
}

// fco: first contact only - only the "new" column asks (default rows).
func fco(r *Rules) *Rules {
	r.SetFilter(FilterFrom(config.QualifyCfg{FirstContactOnly: true}))
	return r
}

func TestEligibleModeExcluded(t *testing.T) {
	r := &Rules{ExcludeModes: []string{"FT8"}}
	q := qso("DL1AB", "20240101", "120000", "20m", "FT8")
	ok, reason := r.Eligible(q, []*store.QSO{q})
	if ok {
		t.Fatalf("FT8 should be excluded; got ok=true (%s)", reason)
	}
}

func TestEligibleDigitalPrefixFallback(t *testing.T) {
	// FST4 is not in the explicit list but matches the FT/FST prefix fallback.
	r := &Rules{ExcludeModes: []string{"FT8"}}
	q := qso("DL1AB", "20240101", "120000", "20m", "FST4")
	ok, _ := r.Eligible(q, []*store.QSO{q})
	if ok {
		t.Fatal("FST4 should be excluded by prefix fallback")
	}
}

// TestIncludeDigital: qualify.include_digital lets FT8/FT4/FT2 & co. in,
// switchable at runtime, even when an old config lists FT8 in exclude_modes;
// exclude_modes still keeps the other modes out.
func TestIncludeDigital(t *testing.T) {
	r := NewRules(config.QualifyCfg{IncludeDigital: true, ExcludeModes: []string{"FT8", "SSTV"}, Since: "all"}, nil)
	for _, mode := range []string{"FT8", "FT4", "FT2", "FST4", "JS8"} {
		q := qso("DL1AB", "20240101", "120000", "20m", mode)
		if ok, reason := r.Eligible(q, []*store.QSO{q}); !ok {
			t.Fatalf("%s with include_digital: not eligible (%s)", mode, reason)
		}
	}
	sstv := qso("DL1AB", "20240101", "120000", "20m", "SSTV")
	if ok, _ := r.Eligible(sstv, []*store.QSO{sstv}); ok {
		t.Fatal("SSTV listed in exclude_modes was let in")
	}
	r.SetFilter(DefaultFilter)
	ft8 := qso("DL1AB", "20240101", "120000", "20m", "FT8")
	if ok, _ := r.Eligible(ft8, []*store.QSO{ft8}); ok {
		t.Fatal("FT8 still let in after switching include_digital off")
	}
}

func TestEligibleFirstContactOnly(t *testing.T) {
	r := fco(&Rules{})
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
	r := fco(&Rules{OverrideMarker: "QSL!"})
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
	r := fco(&Rules{})
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
	r := fco(&Rules{})
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
	r := fco(&Rules{})
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
	r := fco(&Rules{OverrideMarker: "QSL!"})
	n, _ := r.EnqueueAll(st)
	// q1 (older) is first-contact -> eligible.
	// q2 (newer) has override marker -> force-included.
	if n != 2 {
		t.Fatalf("EnqueueAll with override enqueued %d, want 2", n)
	}
}

// Ensure sql.NullString is referenced (kept in store.QSO; used by Eligible).
var _ = sql.NullString{}

// --- decision-queue intake ---

func TestAlreadySentBeatsOverride(t *testing.T) {
	r := &Rules{OverrideMarker: "QSL!"}
	q := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q.Notes = "QSL! memorable"
	q.QSLSent = "Y"
	if ok, reason := r.Eligible(q, []*store.QSO{q}); ok {
		t.Fatalf("a card that already went out must not be queued again, even with the marker (%s)", reason)
	}
	q.QSLSent = ""
	q.QSLSentLocal = sql.NullString{String: "Y", Valid: true}
	if ok, _ := r.EligibleForNewQSO(q, nil); ok {
		t.Fatal("locally sent card must not be queued again either")
	}
}

func TestSinceCutoff(t *testing.T) {
	r := &Rules{Since: "20240601", OverrideMarker: "QSL!"}
	old := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	fresh := qso("DL2CD", "20240601", "120000", "20m", "SSB")
	if ok, reason := r.Eligible(old, []*store.QSO{old}); ok || reason == "" {
		t.Fatalf("QSO before the cutoff must not be queued (%q)", reason)
	}
	if ok, _ := r.Eligible(fresh, []*store.QSO{fresh}); !ok {
		t.Fatal("QSO on the cutoff date must be queued")
	}
	// The marker still forces an old QSO in.
	old.Notes = "QSL!"
	if ok, _ := r.Eligible(old, []*store.QSO{old}); !ok {
		t.Fatal("override marker must bypass the cutoff")
	}
	if ok, _ := (&Rules{}).Eligible(qso("DL1AB", "19990101", "120000", "20m", "SSB"), nil); !ok {
		t.Fatal("no cutoff configured: everything is eligible")
	}
}

func TestNewRulesSince(t *testing.T) {
	st := newStore(t)
	today := time.Now().UTC().Format("20060102")

	// Default: the day qslotter first ran, remembered across restarts.
	r := NewRules(config.QualifyCfg{}, st)
	if r.Since != today {
		t.Fatalf("default Since = %q, want today %q", r.Since, today)
	}
	if v, _ := st.MetaGet("first_run_date"); v != today {
		t.Fatalf("first_run_date not remembered: %q", v)
	}
	_ = st.MetaSet("first_run_date", "20240315")
	if r := NewRules(config.QualifyCfg{}, st); r.Since != "20240315" {
		t.Fatalf("Since must follow the remembered first run, got %q", r.Since)
	}
	for in, want := range map[string]string{"all": "", "ALL": "", "none": "", "2023-05-06": "20230506", "20230506": "20230506"} {
		if r := NewRules(config.QualifyCfg{Since: in}, st); r.Since != want {
			t.Errorf("since %q -> %q, want %q", in, r.Since, want)
		}
	}
	if r := NewRules(config.QualifyCfg{Since: "yesterday"}, st); r.Since != "20240315" {
		t.Errorf("invalid since must fall back to the default, got %q", r.Since)
	}
}

func TestRepeatContactsQueueUnlessFirstContactOnly(t *testing.T) {
	q1 := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q2 := qso("DL1AB", "20240102", "130000", "40m", "SSB")
	all := []*store.QSO{q2, q1}
	if ok, _ := (&Rules{}).Eligible(q2, all); !ok {
		t.Fatal("by default a repeat contact is queued (the operator decides, with the history in front of them)")
	}
	if ok, _ := fco(&Rules{}).Eligible(q2, all); ok {
		t.Fatal("first_contact_only still filters repeat contacts")
	}
}

func TestEnqueueAllKeysAndOverrideReason(t *testing.T) {
	st := newStore(t)
	q1 := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	q2 := qso("DL1AB", "20240102", "130000", "20m", "FT8")
	q2.Notes = "QSL! wanted"
	_, _, _ = st.UpsertQSO(q1)
	_, _, _ = st.UpsertQSO(q2)
	r := &Rules{OverrideMarker: "QSL!"}
	keys, err := r.EnqueueAllKeys(st)
	if err != nil || len(keys) != 2 {
		t.Fatalf("EnqueueAllKeys = %v, %v", keys, err)
	}
	it, _ := st.QueueGet(q2.QSLKey)
	if it == nil || it.OverrideReason != "override: QSL! in notes" {
		t.Fatalf("override reason not recorded: %+v", it)
	}
	if keys2, _ := r.EnqueueAllKeys(st); len(keys2) != 0 {
		t.Fatalf("second scan enqueued %v", keys2)
	}
}

// TestDiscardBacklogOncePerCutoff: the backlog is filed as "no card" once; a
// card the operator reopens stays in the Inbox on the next start, and only a
// later cutoff runs the discard again.
func TestDiscardBacklogOncePerCutoff(t *testing.T) {
	st := newStore(t)
	add := func(call, date string) string {
		q := qso(call, date, "120000", "20m", "SSB")
		if _, _, err := st.UpsertQSO(q); err != nil {
			t.Fatal(err)
		}
		if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
			t.Fatal(err)
		}
		return q.QSLKey
	}
	old := add("DL1ABC", "20240101")
	mid := add("DL2ZZZ", "20240201")
	add("DL3YYY", "20240301")

	r := &Rules{Since: "20240115"}
	if n, err := r.DiscardBacklog(st); err != nil || n != 1 {
		t.Fatalf("first discard = %d, %v; want 1", n, err)
	}
	if _, err := st.QueueReopen(old); err != nil {
		t.Fatal(err)
	}
	if n, err := r.DiscardBacklog(st); err != nil || n != 0 {
		t.Fatalf("second start = %d, %v; want 0 (reopened card stays)", n, err)
	}
	if it, _ := st.QueueGet(old); it.Status != "queued" {
		t.Fatalf("reopened backlog card was discarded again: %+v", it)
	}
	later := &Rules{Since: "20240215"}
	if n, err := later.DiscardBacklog(st); err != nil || n != 2 {
		t.Fatalf("later cutoff = %d, %v; want 2", n, err)
	}
	if it, _ := st.QueueGet(mid); it.Status != "skipped" || it.Note != "backlog" {
		t.Fatalf("mid after later cutoff: %+v", it)
	}
	if n, err := (&Rules{}).DiscardBacklog(st); err != nil || n != 0 {
		t.Fatalf("no cutoff = %d, %v; want 0", n, err)
	}
}

// TestClublogSentDateIsSent: a QSO Clublog has with a QSL sent date is not
// queued (Clublog exports QSLSDATE, never QSL_SENT).
func TestClublogSentDateIsSent(t *testing.T) {
	r := &Rules{}
	q := qso("DL1AB", "20260101", "120000", "20m", "SSB")
	q.QSLSDate = "20260105"
	if ok, _ := r.Eligible(q, nil); ok {
		t.Fatal("a QSO with a QSL sent date must not be queued")
	}
}

func TestModeGroup(t *testing.T) {
	for mode, want := range map[string]string{"SSB": GroupPhone, "usb": GroupPhone, "FM": GroupPhone, "DSTAR": GroupPhone,
		"CW": GroupCW, "RTTY": GroupKeyboard, "PSK31": GroupKeyboard, "SSTV": GroupKeyboard, "FT8": GroupDigital, "JS8": GroupDigital} {
		if got := ModeGroup(mode); got != want {
			t.Errorf("ModeGroup(%s) = %s, want %s", mode, got, want)
		}
	}
}

// TestMatrix: every QSO lands in one cell of the filter - the row by mode
// or satellite, the column by how new the contact is.
func TestMatrix(t *testing.T) {
	old := qso("DL1AB", "20240101", "120000", "20m", "SSB")
	newBand := qso("DL1AB", "20240201", "120000", "40m", "CW")
	sameBand := qso("DL1AB", "20240301", "120000", "20m", "FT8")
	sat1 := qso("DL1AB", "20240401", "120000", "70cm", "FT4")
	sat1.PropMode, sat1.SatName = "SAT", "RS-44"
	sat2 := qso("DL1AB", "20240501", "120000", "70cm", "SSB")
	sat2.PropMode, sat2.SatName = "SAT", "RS-44"
	sat3 := qso("DL1AB", "20240601", "120000", "70cm", "FM")
	sat3.PropMode, sat3.SatName = "SAT", "QO-100"
	rtty := qso("DL1AB", "20240701", "120000", "40m", "RTTY")
	all := []*store.QSO{rtty, sat3, sat2, sat1, sameBand, newBand, old}
	for _, tc := range []struct {
		q        *store.QSO
		row, col string
	}{
		{old, GroupPhone, ColNew},
		{newBand, GroupCW, ColBand},
		{sameBand, GroupDigital, ColRepeat},
		{sat1, RowSatellite, ColBand}, // first via RS-44 (worked before on HF)
		{sat2, RowSatellite, ColRepeat},
		{sat3, RowSatellite, ColBand},
		{rtty, GroupKeyboard, ColRepeat}, // 40m worked before (CW)
	} {
		row, col := Rows[Row(tc.q)], Cols[Column(tc.q, all)]
		if row != tc.row || col != tc.col {
			t.Errorf("%s %s: cell %s/%s, want %s/%s", tc.q.QSODate, tc.q.Mode, row, col, tc.row, tc.col)
		}
		// Only that cell ticked: eligible; every other cell: not.
		var f Filter
		r := &Rules{}
		f[Row(tc.q)][Column(tc.q, all)] = true
		r.SetFilter(f)
		if ok, reason := r.Eligible(tc.q, all); !ok {
			t.Errorf("%s: not eligible with its cell ticked (%s)", tc.q.QSODate, reason)
		}
		f[Row(tc.q)][Column(tc.q, all)] = false
		for ri := range Rows {
			for ci := range Cols {
				f[ri][ci] = !(ri == Row(tc.q) && ci == Column(tc.q, all))
			}
		}
		r.SetFilter(f)
		if ok, _ := r.Eligible(tc.q, all); ok {
			t.Errorf("%s: eligible with its cell unticked", tc.q.QSODate)
		}
	}
}

// TestFilterFrom: qualify.ask, the older switches, and the round trip.
func TestFilterFrom(t *testing.T) {
	if FilterFrom(config.QualifyCfg{}) != DefaultFilter {
		t.Fatal("empty config is not the default filter")
	}
	digi := slices.Index(Rows[:], GroupDigital)
	if f := FilterFrom(config.QualifyCfg{IncludeDigital: true}); !f[digi][2] {
		t.Fatal("include_digital does not tick the digital row")
	}
	f := FilterFrom(config.QualifyCfg{FirstContactOnly: true})
	for r := range Rows {
		if want := [3]bool{Rows[r] != GroupDigital, false, false}; f[r] != want {
			t.Errorf("first_contact_only row %s = %v, want %v", Rows[r], f[r], want)
		}
	}
	ask := map[string][]string{"cw": {"new"}, "digital": {"New", "repeat"}}
	f = FilterFrom(config.QualifyCfg{Ask: ask, IncludeDigital: false})
	if f[1] != [3]bool{true, false, false} || f[digi] != [3]bool{true, false, true} || f[0] != DefaultFilter[0] {
		t.Fatalf("ask %v -> %v", ask, f)
	}
	if FilterFrom(config.QualifyCfg{Ask: f.Ask()}) != f {
		t.Fatal("Ask() does not round-trip")
	}
}
