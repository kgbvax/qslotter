package contact

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/store"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind Kind
		call string
	}{
		{"<CALL:5>DL1AB<QSO_DATE:8>20261001<TIME_ON:6>120000<BAND:3>20m<MODE:3>SSB<EOR>", KindQSO, ""},
		{"<call:5>DL1AB<eor>", KindQSO, ""},
		{"<CALL:5>DL1AB<QSO_DATE:8>20261001", KindQSO, ""}, // ADIF without <EOR>: let the ADIF path report it
		{"VU2ATN", KindContact, "VU2ATN"},
		{" dl1abc/p \r\n", KindContact, "DL1ABC/P"},
		{"RAEM", KindContact, "RAEM"},
		{"", KindClear, ""},
		{"   ", KindClear, ""},
		{"DL", KindPartial, ""},     // half-typed
		{"14.025", KindUnknown, ""}, // not a call
		{"hello world", KindUnknown, ""},
		{`<?xml version="1.0"?><lookupinfo><call>ea8xyz</call><band>20</band><mode>CW</mode><txfreq>1402500</txfreq></lookupinfo>`, KindContact, "EA8XYZ"},
		{`<lookupinfo><call></call><reason>CallChanged</reason></lookupinfo>`, KindClear, ""},
		{`<contactinfo><call>DL1AB</call></contactinfo>`, KindIgnore, ""},
		{`<RadioInfo><Freq>1402500</Freq></RadioInfo>`, KindIgnore, ""},
		{`<?xml version="1.0"?><contact><call>DL1AB</call></contact>`, KindIgnore, ""},
	} {
		kind, got := Classify([]byte(c.in))
		if kind != c.kind || got.Call != c.call {
			t.Errorf("Classify(%q) = %v %q, want %v %q", c.in, kind, got.Call, c.kind, c.call)
		}
	}
	_, li := Classify([]byte(`<lookupinfo><call>EA8XYZ</call><band>20</band><mode>CW</mode><txfreq>1402500</txfreq></lookupinfo>`))
	if li.FreqHz != 14025000 || li.Mode != "CW" || li.Source != "lookupinfo" {
		t.Fatalf("lookupinfo fields: %+v", li)
	}
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func logQSO(t *testing.T, st store.Store, call, band string, queued bool) *store.QSO {
	t.Helper()
	now := time.Now().UTC()
	d, tm := now.Format("20060102"), now.Format("150405")
	q := &store.QSO{QSLKey: call + "|" + d + "|" + tm + "|" + band, Call: call, QSODate: d, TimeOn: tm,
		Band: band, Mode: "SSB", Hash: "h-" + call + band}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if queued {
		if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	return q
}

// TestWrittenDuringTheQSO: "written now" for the QSO in progress is booked
// on the QSO when it is logged - queued or not - and ends the QSO in
// progress.
func TestWrittenDuringTheQSO(t *testing.T) {
	st := openStore(t)
	tr := NewTracker(st, nil, nil)
	tr.Set(Contact{Call: "vu2atn", Source: "callsign"})
	if cur := tr.Current(); cur == nil || cur.Call != "VU2ATN" {
		t.Fatalf("current = %+v", cur)
	}
	tr.MarkWritten("VU2ATN", store.Route{Method: "B"})
	if p := tr.Pending(); len(p) != 1 || p[0].Call != "VU2ATN" {
		t.Fatalf("pending = %+v", p)
	}
	other := logQSO(t, st, "DL1ABC", "20m", true)
	tr.QSOLogged(other)
	if len(tr.Pending()) != 1 || tr.Current() == nil {
		t.Fatal("a QSO with another call changes nothing")
	}
	q := logQSO(t, st, "VU2ATN", "15m", false) // not queued (e.g. digital mode)
	tr.QSOLogged(q)
	it, _ := st.QueueGet(q.QSLKey)
	if it == nil || it.Status != "sent" || it.DesiredMethod != "B" || it.Note != "written now" || it.OverrideReason != "written during the QSO" {
		t.Fatalf("booked card: %+v", it)
	}
	if len(tr.Pending()) != 0 || tr.Current() != nil {
		t.Fatalf("after logging: pending %v, current %+v", tr.Pending(), tr.Current())
	}
	if a := tr.LastApplied(); a == nil || a.QSLKey != q.QSLKey || a.Err != "" {
		t.Fatalf("applied = %+v", a)
	}
}

func TestPendingPortableAndExpiry(t *testing.T) {
	st := openStore(t)
	now := time.Now().UTC()
	tr := NewTracker(st, nil, nil)
	tr.now = func() time.Time { return now }
	tr.MarkWritten("DL1ABC", store.Route{Method: "D"})
	q := logQSO(t, st, "DL1ABC/P", "40m", true) // logged under the portable call
	tr.QSOLogged(q)
	if it, _ := st.QueueGet(q.QSLKey); it.Status != "sent" || it.DesiredMethod != "D" {
		t.Fatalf("portable form must match: %+v", it)
	}
	tr.MarkWritten("K1ABC", store.Route{Method: "B"})
	now = now.Add(PendingTTL + time.Minute)
	if len(tr.Pending()) != 0 {
		t.Fatal("an old pending card expires")
	}
	q2 := logQSO(t, st, "K1ABC", "20m", true)
	tr.QSOLogged(q2)
	if it, _ := st.QueueGet(q2.QSLKey); it.Status != "queued" {
		t.Fatalf("an expired card must not be booked: %+v", it)
	}
}

func TestLookupOnlyWhenTheCallStays(t *testing.T) {
	var looked atomic.Int32
	tr := NewTracker(openStore(t), nil, func(ctx context.Context, call string) {
		if call == "VU2ATN" {
			looked.Add(1)
		}
	})
	tr.Set(Contact{Call: "VU2"})
	tr.Set(Contact{Call: "VU2ATN"})
	time.Sleep(lookupDelay + 300*time.Millisecond)
	if looked.Load() != 1 {
		t.Fatalf("lookups for VU2ATN = %d, want 1 (and none for the half-typed call)", looked.Load())
	}
	tr.Clear()
	if tr.Current() != nil {
		t.Fatal("clear")
	}
}

// TestOlderQSODoesNotTakeTheCard: a QSO with the station that started
// before the QSO in progress (back-filled by a Clublog pull) neither takes
// the pending card nor ends the QSO in progress.
func TestOlderQSODoesNotTakeTheCard(t *testing.T) {
	st := openStore(t)
	tr := NewTracker(st, nil, nil)
	tr.Set(Contact{Call: "VP6A"})
	tr.MarkWritten("VP6A", store.Route{Method: "D"})
	old := &store.QSO{QSLKey: "VP6A|20150101|100000|20M", Call: "VP6A", QSODate: "20150101", TimeOn: "100000", Band: "20M", Mode: "SSB", Hash: "old"}
	if _, _, err := st.UpsertQSO(old); err != nil {
		t.Fatal(err)
	}
	tr.QSOLogged(old)
	if it, _ := st.QueueGet(old.QSLKey); it != nil {
		t.Fatalf("an old QSO must not take the card: %+v", it)
	}
	if len(tr.Pending()) != 1 || tr.Current() == nil {
		t.Fatal("the card waits, the QSO stays in progress")
	}
	q := logQSO(t, st, "VP6A", "40M", true)
	tr.QSOLogged(q)
	if it, _ := st.QueueGet(q.QSLKey); it == nil || it.Status != "sent" {
		t.Fatalf("the QSO in progress takes it: %+v", it)
	}
}

// TestStaleCurrent: a call the logger stops sending is marked stale, then
// dropped.
func TestStaleCurrent(t *testing.T) {
	now := time.Now()
	tr := NewTracker(openStore(t), nil, nil)
	tr.now = func() time.Time { return now }
	tr.Set(Contact{Call: "VU2ATN"})
	now = now.Add(staleAfter + time.Second)
	if c := tr.Current(); c == nil || !c.Stale {
		t.Fatalf("stale: %+v", c)
	}
	now = now.Add(currentTTL)
	if tr.Current() != nil {
		t.Fatal("dropped after the TTL")
	}
}
