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
// its research; "written now" is remembered and booked when the QSO is
// logged (VISION A1b).
func TestCurrentContactBox(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	tr := contact.NewTracker(st, srv.broker, nil)
	srv.Contacts = tr
	if b := get(t, h, "/queue").Body.String(); !strings.Contains(b, `class="current idle"`) {
		t.Fatalf("no QSO in progress: the box is idle:\n%s", b)
	}
	addQSO(t, st, "VU2ATN", "20250101", "20m") // worked before
	tr.Set(contact.Contact{Call: "VU2ATN", Band: "20m", Mode: "SSB", Source: "lookupinfo"})
	page := get(t, h, "/queue").Body.String()
	for _, want := range []string{"QSO in progress", `data-call="VU2ATN"`, "1 earlier QSO(s) with this station", "/current/written?route=B&amp;call=VU2ATN"} {
		if !strings.Contains(page, want) {
			t.Fatalf("/queue misses %q:\n%s", want, page)
		}
	}
	if c := get(t, h, "/queue?compact=1").Body.String(); !strings.Contains(c, `class="current compact"`) || strings.Contains(c, "What QRZ says") {
		t.Fatalf("compact box: slim, no research panel:\n%s", c)
	}
	// Stale page: the call moved on.
	if r := postForm(t, h, "/current/written", url.Values{"call": {"DL1XX"}, "route": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("written for another call = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/current/written", url.Values{"call": {"VU2ATN"}, "route": {"M"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("written now via manager = %d, want 400", r.Code)
	}
	r := postForm(t, h, "/current/written", url.Values{"call": {"VU2ATN"}, "route": {"D"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "booked on the QSO as soon as the QSO is logged") {
		t.Fatalf("written now = %d:\n%s", r.Code, r.Body)
	}
	// The QSO is logged: the card is booked, the box reports it.
	d, tm := time.Now().UTC().Format("20060102"), time.Now().UTC().Format("150405")
	q := &store.QSO{QSLKey: "VU2ATN|" + d + "|" + tm + "|20m", Call: "VU2ATN", QSODate: d, TimeOn: tm, Band: "20m", Mode: "SSB", Hash: "h-now"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	tr.QSOLogged(q)
	if it := status(t, st, q.QSLKey); it.Status != "sent" || it.DesiredMethod != "D" || it.Note != "written now" {
		t.Fatalf("booked: %+v", it)
	}
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "Card written during the QSO with VU2ATN booked") || strings.Contains(b, "QSO in progress") {
		t.Fatalf("after logging:\n%s", b)
	}
	// Undo before the QSO is logged.
	tr.Set(contact.Contact{Call: "JA1ZZZ"})
	tr.MarkWritten("JA1ZZZ", store.Route{Method: "B"})
	tr.Set(contact.Contact{Call: "K1ABC"}) // moved on: the pending card is listed
	if b := get(t, h, "/queue/current").Body.String(); !strings.Contains(b, "JA1ZZZ: card written now") {
		t.Fatalf("pending card of a call no longer in progress:\n%s", b)
	}
	postForm(t, h, "/current/cancel", url.Values{"call": {"JA1ZZZ"}})
	if len(tr.Pending()) != 0 {
		t.Fatal("undo")
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
	tr.MarkWritten("VU2ATN", store.Route{Method: "B"})
	tr.Set(contact.Contact{Call: "K1ABC"})
	for _, p := range []string{"/queue", "/queue?compact=1", "/queue/current"} {
		requestDE(t, h, http.MethodGet, p, nil)
	}
	requestDE(t, h, http.MethodPost, "/current/written", url.Values{"call": {"K1ABC"}, "route": {"B"}})
	requestDE(t, h, http.MethodPost, "/current/written", url.Values{"call": {"X"}, "route": {"B"}})
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
