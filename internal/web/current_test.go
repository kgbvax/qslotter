package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/store"
)

// TestCurrentContactBox: the QSO in progress shows on top of the Inbox with
// its research and the card / no card question (VISION A1b).
func TestCurrentContactBox(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	tr := contact.NewTracker(st, srv.broker, nil)
	srv.Contacts = tr
	if b := get(t, h, "/queue").Body.String(); !strings.Contains(b, `class="current strip idle"`) {
		t.Fatalf("no QSO in progress: the box is idle:\n%s", b)
	}
	addQSO(t, st, "VU2ATN", "20250101", "20m") // worked before
	tr.Set(contact.Contact{Call: "VU2ATN", Band: "20m", Mode: "SSB", Source: "lookupinfo"})
	page := get(t, h, "/queue").Body.String()
	for _, want := range []string{"QSO in progress", `data-call="VU2ATN"`, "1 earlier QSO, last 2025-01-01", "/current/decide?decision=no&amp;call=VU2ATN"} {
		if !strings.Contains(page, want) {
			t.Fatalf("/queue misses %q:\n%s", want, page)
		}
	}
	// The chip sits right under the callsign line, above the decision row, and is not repeated in the research panel.
	head, chip, decide := strings.Index(page, `class="current-head"`), strings.Index(page, "1 earlier QSO, last 2025-01-01"), strings.Index(page, "/current/decide?decision=yes")
	if !(head >= 0 && head < chip && chip < decide) || strings.Count(page, "earlier QSO") != 1 || strings.Contains(page, "earlier QSO(s)") {
		t.Fatalf("history chip under the callsign line (head %d, chip %d, decide %d):\n%s", head, chip, decide, page)
	}
	addQSO(t, st, "VU2ATN", "20250315", "40m") // newest earlier QSO
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "2 earlier QSOs, last 2025-03-15") {
		t.Fatalf("several earlier QSOs:\n%s", b)
	}
	tr.Set(contact.Contact{Call: "N0NEW"})
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "first QSO") || strings.Contains(b, "earlier") {
		t.Fatalf("a new station:\n%s", b)
	}
	tr.Set(contact.Contact{Call: "VU2ATN", Band: "20m", Mode: "SSB", Source: "lookupinfo"})
	if c := get(t, h, "/queue?compact=1").Body.String(); !strings.Contains(c, `class="current compact"`) || strings.Contains(c, "QRZ:") {
		t.Fatalf("compact box: slim, no research panel:\n%s", c)
	}
	if strings.Contains(page, "/current/written") || strings.Contains(page, "Already written") {
		t.Fatalf("the box offers only card / no card:\n%s", page)
	}
}

// TestCurrentContactDecide: card / no card can be decided for the QSO in
// progress; it is booked when the QSO is logged, and stays undoable while
// the QSO may never be logged.
func TestCurrentContactDecide(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	tr := contact.NewTracker(st, srv.broker, nil)
	srv.Contacts = tr
	tr.Set(contact.Contact{Call: "VU2ATN", Band: "20m", Mode: "SSB", Source: "callsign"})
	page := get(t, h, "/queue").Body.String()
	for _, want := range []string{"/current/decide?decision=yes&amp;call=VU2ATN", "/current/decide?decision=no&amp;call=VU2ATN"} {
		if !strings.Contains(page, want) {
			t.Fatalf("/queue misses %q:\n%s", want, page)
		}
	}
	if r := postForm(t, h, "/current/decide", url.Values{"call": {"DL1XX"}, "decision": {"yes"}}); r.Code != http.StatusConflict {
		t.Fatalf("decide for another call = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/current/decide", url.Values{"call": {"VU2ATN"}, "decision": {"maybe"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("unknown decision = %d, want 400", r.Code)
	}
	r := postForm(t, h, "/current/decide", url.Values{"call": {"VU2ATN"}, "decision": {"no"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "No card - recorded on the QSO as soon as it is logged") {
		t.Fatalf("decide no = %d:\n%s", r.Code, r.Body)
	}
	// The QSO is never logged: the call moves on, the decision stays listed.
	tr.Set(contact.Contact{Call: "K1ABC"})
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "VU2ATN: no card, waiting for the QSO to be logged (forgotten after 12 h if it never is)") {
		t.Fatalf("pending decision of a call no longer in progress:\n%s", b)
	}
	postForm(t, h, "/current/cancel", url.Values{"call": {"VU2ATN"}})
	if len(tr.Pending()) != 0 {
		t.Fatal("undo")
	}
	// Card wanted, then the QSO is logged: it waits at the Desk.
	if r := postForm(t, h, "/current/decide", url.Values{"call": {"K1ABC"}, "decision": {"yes"}}); r.Code != 200 {
		t.Fatalf("decide yes = %d:\n%s", r.Code, r.Body)
	}
	d, tm := time.Now().UTC().Format("20060102"), time.Now().UTC().Format("150405")
	q := &store.QSO{QSLKey: "K1ABC|" + d + "|" + tm + "|20m", Call: "K1ABC", QSODate: d, TimeOn: tm, Band: "20m", Mode: "SSB", Hash: "h-k1"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	tr.QSOLogged(q)
	if it := status(t, st, q.QSLKey); it.Status != "decided" {
		t.Fatalf("booked: %+v", it)
	}
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "Card wanted, decided during the QSO with K1ABC: the logged QSO is at the Desk.") {
		t.Fatalf("after logging:\n%s", b)
	}
}

// TestCurrentContactGerman: the box is fully translated.
func TestCurrentContactGerman(t *testing.T) {
	missing := map[string]bool{}
	i18n.Default.OnMissing = func(lang, text string) { missing[text] = true }
	defer func() { i18n.Default.OnMissing = nil }()
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	tr := contact.NewTracker(st, nil, nil)
	srv.Contacts = tr
	tr.Set(contact.Contact{Call: "VU2ATN"})
	tr.MarkDecision("VU2ATN", contact.Yes)
	tr.Set(contact.Contact{Call: "K1ABC"})
	for _, p := range []string{"/queue", "/queue?compact=1", "/queue/current"} {
		requestDE(t, h, http.MethodGet, p, nil)
	}
	requestDE(t, h, http.MethodPost, "/current/decide", url.Values{"call": {"K1ABC"}, "decision": {"yes"}})
	requestDE(t, h, http.MethodPost, "/current/decide", url.Values{"call": {"K1ABC"}, "decision": {"maybe"}})
	requestDE(t, h, http.MethodPost, "/current/decide", url.Values{"call": {"X"}, "decision": {"no"}})
	tr.Set(contact.Contact{Call: "DL1XX"})
	tr.MarkDecision("DL1XX", contact.No)
	tr.Set(contact.Contact{Call: "K1ABC"})
	requestDE(t, h, http.MethodGet, "/queue/current", nil)
	d, tm := time.Now().UTC().Format("20060102"), time.Now().UTC().Format("150405")
	q := &store.QSO{QSLKey: "K1ABC|" + d + "|" + tm + "|20m", Call: "K1ABC", QSODate: d, TimeOn: tm, Band: "20m", Mode: "SSB", Hash: "h"}
	_, _, _ = st.UpsertQSO(q)
	tr.QSOLogged(q)
	requestDE(t, h, http.MethodGet, "/queue/current", nil)
	if len(missing) > 0 {
		var l []string
		for m := range missing {
			l = append(l, m)
		}
		sort.Strings(l)
		t.Fatalf("missing German:\n%s", strings.Join(l, "\n"))
	}
}

// TestEarlierMsg: the history badge counts the earlier QSOs and says how long
// ago the last one was - days up to a week, weeks up to 30 days, then the date.
func TestEarlierMsg(t *testing.T) {
	now := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		n    int
		date string
		want string
	}{
		{1, "20261006", "1 earlier QSO, last today"},
		{3, "20261005", "3 earlier QSOs, last 1 day ago"},
		{3, "20261001", "3 earlier QSOs, last 5 days ago"},
		{2, "20260930", "2 earlier QSOs, last 6 days ago"},
		{2, "20260929", "2 earlier QSOs, last 1 week ago"},
		{2, "20260922", "2 earlier QSOs, last 2 weeks ago"},
		{2, "20260908", "2 earlier QSOs, last 4 weeks ago"},
		{2, "20260907", "2 earlier QSOs, last 4 weeks ago"},
		{2, "20260906", "2 earlier QSOs, last 2026-09-06"},
		{5, "20250101", "5 earlier QSOs, last 2025-01-01"},
		{1, "20261007", "1 earlier QSO, last today"}, // a clock a day behind: never negative
		{1, "garbage", "1 earlier QSO, last garbage"},
	} {
		if got := earlierMsg(c.n, c.date, now).String(); got != c.want {
			t.Errorf("earlierMsg(%d, %q) = %q, want %q", c.n, c.date, got, c.want)
		}
	}
}
