package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
)

// newTestServer builds a Server against a temp store with one queued QSO.
func newTestServer(t *testing.T) (*Server, store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	q := &store.QSO{QSLKey: "DL1ABC|20240101|120000|20m", Call: "DL1ABC",
		QSODate: "20240101", TimeOn: "120000", Band: "20m", Mode: "SSB",
		RSTSent: "59", Name: "Alice", Hash: "h1"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Clublog.Call = "DL9ET"
	srv, err := New(cfg, st, events.New(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.printer = &fakePrinter{}
	return srv, st, q.QSLKey
}

// addQueued adds one more QSO awaiting a decision and returns its key.
func addQueued(t *testing.T, st store.Store, call, date string) string {
	t.Helper()
	q := &store.QSO{QSLKey: call + "|" + date + "|140000|40m", Call: call,
		QSODate: date, TimeOn: "140000", Band: "40m", Mode: "CW",
		RSTSent: "599", Name: "N-" + call, Hash: "h-" + call + date}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	return q.QSLKey
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getHX(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func status(t *testing.T, st store.Store, key string) *store.QueueItem {
	t.Helper()
	it, err := st.QueueGet(key)
	if err != nil || it == nil {
		t.Fatalf("QueueGet(%s) = %v, %v", key, it, err)
	}
	return it
}

// fakePrinter records PrintPDF calls instead of talking to a real printer.
type fakePrinter struct {
	printed []string
	err     error
}

func (f *fakePrinter) List() ([]string, error)  { return []string{"fake"}, nil }
func (f *fakePrinter) Default() (string, error) { return "fake", nil }
func (f *fakePrinter) PrintPDF(path, name string, _ printer.Options) error {
	if f.err != nil {
		return f.err
	}
	f.printed = append(f.printed, path)
	return nil
}

func TestQueuePagesRender(t *testing.T) {
	srv, _, key := newTestServer(t)
	h := srv.Routes()

	full := get(t, h, "/queue")
	if full.Code != 200 {
		t.Fatalf("/queue = %d: %s", full.Code, full.Body)
	}
	for _, want := range []string{"DL1ABC", "Alice", "/queue/yes?key=", "/queue/none", "/queue/written?route=B&amp;key=", "/queue/written?route=D&amp;key=", key, `class="cnt"`} {
		if !strings.Contains(full.Body.String(), want) {
			t.Fatalf("/queue missing %q; body:\n%s", want, full.Body)
		}
	}
	// The Inbox decides whether; routes for the other cards are the Desk's job.
	for _, gone := range []string{"/queue/decide", "/work/print", "route=MD", "route=MB"} {
		if strings.Contains(full.Body.String(), gone) {
			t.Fatalf("/queue still offers %q", gone)
		}
	}

	// Compact: yes / no only.
	compact := get(t, h, "/queue?compact=1")
	if compact.Code != 200 || !strings.Contains(compact.Body.String(), "row-"+key) {
		t.Fatalf("/queue?compact=1 = %d, missing row for %s:\n%s", compact.Code, key, compact.Body)
	}
	if b := compact.Body.String(); !strings.Contains(b, "/queue/yes?key=") || strings.Contains(b, "/queue/written") {
		t.Fatalf("compact rows must offer yes/no only:\n%s", b)
	}

	// SSE-driven row fetch, both variants.
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)); r.Code != 200 {
		t.Fatalf("/queue/row = %d", r.Code)
	}
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)+"&compact=1"); r.Code != 200 || strings.Contains(r.Body.String(), "/queue/written") {
		t.Fatalf("/queue/row compact = %d: %s", r.Code, r.Body)
	}
	for _, p := range []string{"/work", "/work/card", "/done", "/decide", "/nav"} {
		if r := get(t, h, p); r.Code != 200 {
			t.Fatalf("%s = %d: %s", p, r.Code, r.Body)
		}
	}
}

// TestListYesLeavesInbox: "yes" from the list removes the card from the Inbox
// (row, card view, count) and puts it on the Desk with the route still open.
func TestListYesLeavesInbox(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	other := addQueued(t, st, "DL2ZZZ", "20240103")

	r := postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != "" {
		t.Fatalf("list yes = %d, want 200 with an empty body (row removed): %q", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || it.DesiredMethod != "" {
		t.Fatalf("after yes: %+v", it)
	}

	if body := get(t, h, "/queue").Body.String(); strings.Contains(body, "row-"+key) || !strings.Contains(body, "row-"+other) {
		t.Fatalf("/queue after yes must list only the undecided card:\n%s", body)
	}
	if body := get(t, h, "/decide").Body.String(); strings.Contains(body, "DL1ABC") || !strings.Contains(body, "DL2ZZZ") {
		t.Fatalf("/decide after yes must show only the undecided card:\n%s", body)
	}
	if body := get(t, h, "/work").Body.String(); !strings.Contains(body, "row-"+key) || !strings.Contains(body, "Not chosen yet <small>") {
		t.Fatalf("/work must list the card under Route open:\n%s", body)
	}
}

// TestDeskManagerRoute: a manager route needs the manager's callsign; it is
// recorded with how the card travels.
func TestDeskManagerRoute(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})

	for _, f := range []url.Values{
		{"key": {key}, "route": {"MD"}},                            // no manager
		{"key": {key}, "route": {"MB"}, "manager": {"VIA BUREAU"}}, // free text is not a manager
		{"key": {key}},                 // no route at all
		{"key": {key}, "route": {"X"}}, // unknown route
		{"key": {key}, "route": {"M"}}, // manager without bureau/direct
	} {
		if r := postForm(t, h, "/work/written", f); r.Code != http.StatusBadRequest {
			t.Fatalf("written %v = %d, want 400", f, r.Code)
		}
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("refused routes must not change the card: %+v", it)
	}
	if r := postForm(t, h, "/work/written", url.Values{"key": {key}, "route": {"MB"}, "manager": {" k2abc "}}); r.Code != 200 {
		t.Fatalf("written via manager = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "sent" || it.DesiredMethod != "M" || it.SendVia != "B" || it.Manager != "K2ABC" {
		t.Fatalf("after manager route: %+v", it)
	}
	if q, _ := st.GetQSO(key); q.QSLSentMethodLocal.String != "B" {
		t.Fatalf("a manager card via the bureau goes as QSL_SENT_VIA=B: %v", q.QSLSentMethodLocal)
	}
	if body := get(t, h, "/done").Body.String(); !strings.Contains(body, "via manager K2ABC (bureau), written") {
		t.Fatalf("/done must spell out the manager route:\n%s", body)
	}
}

// TestWrittenNow: written now in the Inbox records bureau or direct - never a
// manager, never no route.
func TestWrittenNow(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	for _, f := range []url.Values{
		{"key": {key}},
		{"key": {key}, "route": {"MD"}, "manager": {"K2ABC"}},
	} {
		if r := postForm(t, h, "/queue/written", f); r.Code != http.StatusBadRequest {
			t.Fatalf("written now %v = %d, want 400", f, r.Code)
		}
	}
	if it := status(t, st, key); it.Status != "queued" {
		t.Fatalf("refused written-now changed the card: %+v", it)
	}
	if r := postForm(t, h, "/queue/written", url.Values{"key": {key}, "route": {"D"}}); r.Code != 200 {
		t.Fatalf("written now = %d: %s", r.Code, r.Body)
	}
	it := status(t, st, key)
	if it.Status != "sent" || it.DesiredMethod != "D" || it.Note != "written now" {
		t.Fatalf("after written now: %+v", it)
	}
	q, _ := st.GetQSO(key)
	if q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "D" {
		t.Fatalf("local sent state = %v / method %v, want Y / D", q.QSLSentLocal, q.QSLSentMethodLocal)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Fatalf("written card not pending push-back: %d", len(pend))
	}
}

func TestNoneDeclinesAndSurvivesRecompute(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/queue/none", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("none = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "skipped" || it.DesiredMethod != "N" {
		t.Fatalf("after none: %+v, want skipped/N", it)
	}
	if r := postForm(t, h, "/queue/recompute", nil); r.Code != 200 {
		t.Fatalf("recompute = %d", r.Code)
	}
	if it := status(t, st, key); it.Status != "skipped" {
		t.Fatalf("recompute resurrected a declined card: %+v", it)
	}
}

// TestStaleActionsConflict: a page acting on a card that was handled in the
// meantime (second window, double click) gets 409 and changes nothing.
func TestStaleActionsConflict(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/queue/written", url.Values{"key": {key}, "route": {"B"}}); r.Code != 200 {
		t.Fatalf("written = %d", r.Code)
	}
	for _, p := range []string{"/queue/yes", "/queue/none", "/queue/written", "/queue/back", "/work/print"} {
		if r := postForm(t, h, p, url.Values{"key": {key}, "route": {"D"}}); r.Code != http.StatusConflict {
			t.Fatalf("%s on a sent card = %d, want 409", p, r.Code)
		}
	}
	if r := postForm(t, h, "/queue/none", url.Values{"key": {"NOPE|20240101|000000|20m"}}); r.Code != http.StatusConflict {
		t.Fatalf("unknown key = %d, want 409", r.Code)
	}
	if it := status(t, st, key); it.Status != "sent" || it.DesiredMethod != "B" {
		t.Fatalf("stale actions changed the card: %+v", it)
	}
	if len(srv.printer.(*fakePrinter).printed) != 0 {
		t.Fatal("stale print reached the printer")
	}
}

// TestDecideFlow: the decide view is a full page showing the newest card;
// every decision advances to the next card; the empty state invites.
func TestDecideFlow(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	newer := addQueued(t, st, "DL2ZZZ", "20240103")

	body := get(t, h, "/decide").Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "DL2ZZZ", "card 1 of 2", `data-key="y"`, `data-key="wb"`, "/queue/yes?key=", "work=1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/decide missing %q; body:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `data-qslkey="DL2ZZZ|`) { // the older QSO waits behind the newest one
		t.Fatalf("newest QSO must be the displayed card:\n%s", body)
	}

	// "Yes" on the newest card shows the next one, as a bare fragment.
	r := postForm(t, h, "/queue/yes", url.Values{"key": {newer}, "work": {"1"}})
	if r.Code != 200 || strings.Contains(r.Body.String(), "<!DOCTYPE") {
		t.Fatalf("yes in card view = %d:\n%s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "DL1ABC") || strings.Contains(r.Body.String(), "DL2ZZZ") {
		t.Fatalf("card view did not advance:\n%s", r.Body)
	}
	if it := status(t, st, newer); it.Status != "decided" {
		t.Fatalf("decision not recorded: %+v", it)
	}

	// Declining the last card lands on the empty state.
	r = postForm(t, h, "/queue/none", url.Values{"key": {key}, "work": {"1"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "No QSOs waiting for a decision") {
		t.Fatalf("empty state = %d:\n%s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "/work/card") {
		t.Fatalf("empty state should point at the waiting Desk cards:\n%s", r.Body)
	}
}

func TestDecideBrowse(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	addQueued(t, st, "DL2ZZZ", "20240103")
	old := "DL1ABC|20240101|120000|20m"

	// Browsing selects a card by key without deciding anything.
	r := getHX(t, h, "/decide?key="+url.QueryEscape(old))
	if r.Code != 200 || strings.Contains(r.Body.String(), "<!DOCTYPE") ||
		!strings.Contains(r.Body.String(), "card 2 of 2") || !strings.Contains(r.Body.String(), "data-key=\"arrowleft\"") {
		t.Fatalf("browse to second card = %d:\n%s", r.Code, r.Body)
	}
}

func TestBackReturnsToQueue(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r := postForm(t, h, "/queue/back", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("back = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after back: %+v", it)
	}
	if !strings.Contains(get(t, h, "/queue").Body.String(), "row-"+key) {
		t.Fatal("card missing from the Inbox after back")
	}
}

// TestWorkQueue: Desk cards are grouped by the route offered first (recorded,
// else the QRZ suggestion); one card at a time with an optional filter.
func TestWorkQueue(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	kd := addQueued(t, st, "DL2ZZZ", "20240103")
	km := addQueued(t, st, "DL3YYY", "20240104")
	kb := addQueued(t, st, "DL4WWW", "20240105")
	for call, info := range map[string]*store.StationInfo{
		"DL2ZZZ": {QSLMethod: "D"}, "DL3YYY": {QSLMethod: "M", QSLRoute: "K2ABC"}, "DL4WWW": {QSLMethod: "B"},
	} {
		info.Callsign = call
		if err := st.PutStation(info); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{key, kd, km, kb} {
		if r := postForm(t, h, "/queue/yes", url.Values{"key": {k}}); r.Code != 200 {
			t.Fatalf("yes %s = %d: %s", k, r.Code, r.Body)
		}
	}

	list := get(t, h, "/work").Body.String()
	iO, iD, iM, iB := strings.Index(list, "Not chosen yet <small>"), strings.Index(list, "Direct <small>"), strings.Index(list, "Via manager <small>"), strings.Index(list, "Bureau <small>")
	if iO < 0 || iD < iO || iM < iD || iB < iM {
		t.Fatalf("/work groups must read Not chosen yet, Direct, Via manager, Bureau:\n%s", list)
	}
	if !strings.Contains(list, `form="batch" value="K2ABC"`) || !strings.Contains(list, `form="batch" value="MD"`) || !strings.Contains(list, "Via manager, direct K2ABC") {
		t.Fatalf("/work must preselect the suggested manager route:\n%s", list)
	}

	// Card view: full page, newest Desk card first, its suggested route preselected.
	page := get(t, h, "/work/card").Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "DL4WWW", "card 1 of 4", `id="workcard"`, `data-route="B"`, `value="B" data-key="b" checked`, "suggested: by QRZ", "/work/print?view=work", `data-key="p"`, "What QRZ says"} {
		if !strings.Contains(page, want) {
			t.Fatalf("/work/card missing %q:\n%s", want, page)
		}
	}
	// The filter narrows the stack; other groups are not shown.
	frag := getHX(t, h, "/work/card?filter=M").Body.String()
	if !strings.Contains(frag, "DL3YYY") || !strings.Contains(frag, `value="MD" data-key="m" checked`) || !strings.Contains(frag, `value="K2ABC"`) ||
		strings.Contains(frag, "DL4WWW") || !strings.Contains(frag, "card 1 of 1") || strings.Contains(frag, "<!DOCTYPE") {
		t.Fatalf("/work/card?filter=M (fragment):\n%s", frag)
	}
	if b := getHX(t, h, "/work/card?filter=O").Body.String(); !strings.Contains(b, "DL1ABC") || !strings.Contains(b, `data-route=""`) || strings.Contains(b, " checked") {
		t.Fatalf("filter=O must show the card without a route, nothing preselected:\n%s", b)
	}
	if !strings.Contains(getHX(t, h, "/work/card?filter=D").Body.String(), "DL2ZZZ") {
		t.Fatal("filter=D lost the direct card")
	}
}

// TestWorkPrintMarksSentAndPending: printing a Desk card completes it: it is
// marked sent with the chosen route, waits for push-back, and the next card
// shows.
func TestWorkPrintMarksSentAndPending(t *testing.T) {
	srv, st, key := newTestServer(t)
	fp := srv.printer.(*fakePrinter)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/queue/yes", url.Values{"key": {k2}})

	r := postForm(t, h, "/work/print", url.Values{"key": {k2}, "route": {"B"}, "view": {"work"}})
	if r.Code != 200 || strings.Contains(r.Body.String(), "<!DOCTYPE") {
		t.Fatalf("print = %d:\n%s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "DL1ABC") || strings.Contains(r.Body.String(), "DL2ZZZ") {
		t.Fatalf("work card did not advance to the next card:\n%s", r.Body)
	}
	if len(fp.printed) != 1 {
		t.Fatalf("printer called %d times, want 1", len(fp.printed))
	}
	it := status(t, st, k2)
	if it.Status != "sent" || it.DesiredMethod != "B" || !it.PrintedAt.Valid {
		t.Fatalf("after print: %+v", it)
	}
	q, _ := st.GetQSO(k2)
	if q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "B" {
		t.Fatalf("local sent state = %v/%v, want Y/B", q.QSLSentLocal, q.QSLSentMethodLocal)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 || pend[0].QSLKey != k2 {
		t.Fatalf("pending push-back = %v", pend)
	}

	// The last card lands on the empty state.
	r = postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"D"}, "view": {"work"}})
	if !strings.Contains(r.Body.String(), "Nothing to write or print") {
		t.Fatalf("empty Desk:\n%s", r.Body)
	}
}

// TestWorkPrintFailureKeepsCardDecided: a printer error must not complete the
// card - it stays on the Desk.
func TestWorkPrintFailureKeepsCardDecided(t *testing.T) {
	srv, st, key := newTestServer(t)
	srv.printer = &fakePrinter{err: fmt.Errorf("lp: no default destination")}
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})

	r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"D"}, "view": {"work"}})
	if r.Code != http.StatusInternalServerError || !strings.Contains(r.Body.String(), "no default destination") {
		t.Fatalf("failed print = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || it.PrintedAt.Valid {
		t.Fatalf("failed print changed the card: %+v", it)
	}
	if q, _ := st.GetQSO(key); q.QSLSentLocal.Valid {
		t.Fatalf("failed print marked the QSO sent: %v", q.QSLSentLocal)
	}
}

// TestPrintNeedsADecisionAndARoute: nothing is printed for an Inbox card, nor
// for a Desk card without a chosen route (no fallback route is invented).
func TestPrintNeedsADecisionAndARoute(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("print of an Inbox card = %d, want 409", r.Code)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}}); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "Choose how to send") {
		t.Fatalf("print without a route = %d: %s", r.Code, r.Body)
	}
	if n := len(srv.printer.(*fakePrinter).printed); n != 0 {
		t.Fatalf("printer called %d times", n)
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("card changed: %+v", it)
	}
}

// TestPrintFindsOldQSO: printing looks the QSO up by key, so a first contact
// buried under many newer QSOs with the same call is still found.
func TestPrintFindsOldQSO(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fp := srv.printer.(*fakePrinter)
	h := srv.Routes()

	old := &store.QSO{QSLKey: "DL9OLD|20200101|100000|20m", Call: "DL9OLD",
		QSODate: "20200101", TimeOn: "100000", Band: "20m", Mode: "SSB", Hash: "old"}
	if _, _, err := st.UpsertQSO(old); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: old.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ { // newer QSOs push the old one past a LIMIT 50 lookup
		q := &store.QSO{QSLKey: fmt.Sprintf("DL9OLD|2024%04d|120000|40m", i+101), Call: "DL9OLD",
			QSODate: fmt.Sprintf("2024%04d", i+101), TimeOn: "120000", Band: "40m", Mode: "CW",
			Hash: fmt.Sprintf("n%d", i)}
		if _, _, err := st.UpsertQSO(q); err != nil {
			t.Fatal(err)
		}
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {old.QSLKey}})
	if r := postForm(t, h, "/work/print", url.Values{"key": {old.QSLKey}, "route": {"B"}}); r.Code != 200 {
		t.Fatalf("print old QSO = %d: %s", r.Code, r.Body)
	}
	if len(fp.printed) != 1 {
		t.Fatalf("printer called %d times, want 1", len(fp.printed))
	}
}

func TestWorkWrittenRecordsRouteAndBack(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/queue/yes", url.Values{"key": {k2}})

	if r := postForm(t, h, "/work/written", url.Values{"key": {key}, "route": {"D"}, "view": {"work"}}); r.Code != 200 {
		t.Fatalf("written in work view = %d: %s", r.Code, r.Body)
	}
	it := status(t, st, key)
	q, _ := st.GetQSO(key)
	if it.Status != "sent" || it.DesiredMethod != "D" || q.QSLSentMethodLocal.String != "D" || it.PrintedAt.Valid || it.Note != "" {
		t.Fatalf("written records the chosen route: %+v / %v", it, q.QSLSentMethodLocal)
	}
	if r := postForm(t, h, "/queue/back", url.Values{"key": {k2}, "view": {"work"}}); r.Code != 200 {
		t.Fatalf("back in work view = %d", r.Code)
	}
	if it := status(t, st, k2); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after back: %+v", it)
	}
}

func TestDoneAndReopen(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	kn := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/written", url.Values{"key": {key}, "route": {"B"}})
	postForm(t, h, "/queue/none", url.Values{"key": {kn}})

	body := get(t, h, "/done").Body.String()
	for _, want := range []string{"row-" + key, "Bureau, written during the QSO", "row-" + kn, "no card", "/queue/reopen?key="} {
		if !strings.Contains(body, want) {
			t.Fatalf("/done missing %q:\n%s", want, body)
		}
	}
	if r := postForm(t, h, "/queue/reopen", url.Values{"key": {key}}); r.Code != 200 || r.Header().Get("HX-Trigger") != "" {
		t.Fatalf("reopen = %d, trigger %q", r.Code, r.Header().Get("HX-Trigger"))
	}
	if it := status(t, st, key); it.Status != "queued" || it.DesiredMethod != "" || it.Note != "" {
		t.Fatalf("after reopen: %+v", it)
	}
	if q, _ := st.GetQSO(key); q.QSLSentLocal.Valid {
		t.Fatalf("reopen left local sent state: %v", q.QSLSentLocal)
	}
	if strings.Contains(get(t, h, "/done").Body.String(), "row-"+key) || !strings.Contains(get(t, h, "/queue").Body.String(), "row-"+key) {
		t.Fatal("reopened card must leave /done and return to /queue")
	}

	// Reopening a card Clublog already has as sent warns the operator.
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}})
	pend, _ := st.PendingPushBack()
	if err := st.MarkPushed(pend[0]); err != nil {
		t.Fatal(err)
	}
	r := postForm(t, h, "/queue/reopen", url.Values{"key": {key}})
	if r.Code != 200 || !strings.Contains(r.Header().Get("HX-Trigger"), "qslNotice") {
		t.Fatalf("reopen of a pushed card = %d, trigger %q", r.Code, r.Header().Get("HX-Trigger"))
	}
}

// TestDoneShowsBacklog: backlog cards are told apart from a "no card" decision.
func TestDoneShowsBacklog(t *testing.T) {
	srv, st, key := newTestServer(t)
	if n, err := st.QueueDiscardBacklog("20990101"); err != nil || n != 1 {
		t.Fatalf("discard = %d, %v", n, err)
	}
	body := get(t, srv.Routes(), "/done").Body.String()
	if !strings.Contains(body, "row-"+key) || !strings.Contains(body, "no card (backlog)") {
		t.Fatalf("/done must show the backlog card:\n%s", body)
	}
}

// TestQueueChangedEvent: every move tells the other windows.
func TestQueueChangedEvent(t *testing.T) {
	srv, _, key := newTestServer(t)
	ch, unsub := srv.broker.Subscribe()
	defer unsub()
	postForm(t, srv.Routes(), "/queue/yes", url.Values{"key": {key}})
	select {
	case ev := <-ch:
		if ev.Type != "queue_changed" || !strings.Contains(ev.Data, `"to":"decided"`) || !strings.Contains(ev.Data, key) {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no queue_changed event after a decision")
	}
}

func TestNavShowsCounts(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	addQueued(t, st, "DL2ZZZ", "20240103")
	nav := get(t, h, "/nav").Body.String()
	if !strings.Contains(nav, `New QSOs <span class="cnt">2</span>`) || strings.Contains(nav, `Desk <span`) {
		t.Fatalf("nav = %s", nav)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	nav = get(t, h, "/nav").Body.String()
	if !strings.Contains(nav, `New QSOs <span class="cnt">1</span>`) || !strings.Contains(nav, `Desk <span class="cnt">1</span>`) {
		t.Fatalf("nav after yes = %s", nav)
	}
}

// fakeQRZ serves the QRZ XML API for any callsign (address included); with
// down set, lookups fail.
func fakeQRZ(t *testing.T, down *atomic.Bool) *qrz.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.RawQuery
		switch {
		case strings.Contains(q, "username="):
			_, _ = w.Write([]byte(`<QRZDatabase><Session><Key>k</Key></Session></QRZDatabase>`))
		case down.Load():
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.Contains(q, "callsign="):
			call, _ := url.QueryUnescape(q[strings.Index(q, "callsign=")+len("callsign="):])
			_, _ = w.Write([]byte(`<QRZDatabase><Session><Key>k</Key></Session><Callsign><call>` + call +
				`</call><addr1>1 Main St</addr1><country>Spain</country></Callsign></QRZDatabase>`))
		default: // bio
			_, _ = w.Write([]byte(`<html><body>QSL via bureau.</body></html>`))
		}
	}))
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()
	return cl
}

// TestStationRefresh: the research panel's Refresh forces a QRZ re-lookup
// (portable calls too: the call travels in the query) and answers 204 - the
// station_updated event reloads the open panels. Without QRZ there is no
// button and the endpoint says why; a failed lookup keeps the cache.
func TestStationRefresh(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/station/refresh?call=DL1ABC", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("refresh without QRZ = %d, want 503", r.Code)
	}
	if b := get(t, h, "/decide").Body.String(); strings.Contains(b, "/station/refresh") {
		t.Fatal("Refresh offered while QRZ lookups are off")
	}

	// A fresh cache entry: rendering the card starts no background lookup.
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", FetchedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	srv.refresher = station.New(st, fakeQRZ(t, &down), srv.broker, time.Hour)
	if b := get(t, h, "/decide").Body.String(); !strings.Contains(b, `hx-post="/station/refresh?call=DL1ABC"`) {
		t.Fatalf("research panel without Refresh:\n%s", b)
	}
	if r := postForm(t, h, "/station/refresh", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("refresh without call = %d, want 400", r.Code)
	}

	evs, cancel := srv.broker.Subscribe()
	defer cancel()
	if r := postForm(t, h, "/station/refresh?call="+url.QueryEscape("ea8/dl1abc"), nil); r.Code != http.StatusNoContent {
		t.Fatalf("refresh = %d: %s", r.Code, r.Body)
	}
	si, err := st.GetStation("EA8/DL1ABC")
	if err != nil || si == nil || si.Addr1 != "1 Main St" {
		t.Fatalf("station after refresh = %+v, %v", si, err)
	}
	select {
	case ev := <-evs:
		if ev.Type != "station_updated" || ev.Data != "EA8/DL1ABC" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no station_updated event")
	}

	down.Store(true)
	if r := postForm(t, h, "/station/refresh?call="+url.QueryEscape("EA8/DL1ABC"), nil); r.Code != http.StatusBadGateway {
		t.Fatalf("refresh with QRZ down = %d, want 502", r.Code)
	}
	if si, _ := st.GetStation("EA8/DL1ABC"); si == nil || si.Addr1 != "1 Main St" {
		t.Fatalf("cache lost after a failed lookup: %+v", si)
	}
}

// TestQueueRowOnlyForUndecided: SSE fires for every card move (and every new
// QSO, eligible or not); the row endpoint only renders cards awaiting a
// decision and never blows up on the others.
func TestQueueRowOnlyForUndecided(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	q := &store.QSO{QSLKey: "DL2ZZZ|20240103|140000|40m", Call: "DL2ZZZ",
		QSODate: "20240103", TimeOn: "140000", Band: "40m", Mode: "CW", Hash: "h3"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(q.QSLKey)); r.Code != http.StatusNoContent {
		t.Fatalf("/queue/row for non-queued QSO = %d, want 204", r.Code)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)); r.Code != http.StatusNoContent {
		t.Fatalf("/queue/row for a Desk card = %d, want 204", r.Code)
	}
	if r := get(t, h, "/queue/row?key="+url.QueryEscape("GONE|20240101|000000|20m")); r.Code != http.StatusNotFound {
		t.Fatalf("/queue/row for unknown QSO = %d, want 404", r.Code)
	}
}

// TestBatchActions: the Desk list applies one action to many ticked cards;
// the Inbox has no batch (a yes/no is as quick as ticking).
func TestBatchActions(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	q2 := addQueued(t, st, "DL2ZZZ", "20240103")
	for _, k := range []string{key, q2} {
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
		if it := status(t, st, k); it.Status != "decided" {
			t.Fatalf("%s after yes: %+v", k, it)
		}
	}

	// No batch in the Inbox, whatever the action.
	for _, a := range []string{"yes", "none", "B", "print"} {
		if r := postForm(t, h, "/queue/batch", url.Values{"action": {a}, "keys": {key}}); r.Code != http.StatusBadRequest {
			t.Fatalf("Inbox batch %q = %d, want 400", a, r.Code)
		}
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("a refused batch touched the card: %+v", it)
	}

	// Desk batches use each row's route field; a row without a route fails.
	fp := srv.printer.(*fakePrinter)
	r := postForm(t, h, "/queue/batch", url.Values{"list": {"work"}, "action": {"print"}, "keys": {key, q2},
		"route:" + key: {"MD"}, "manager:" + key: {"K2ABC"}})
	if r.Header().Get("Location") != "/work?done=1&failed=1" || len(fp.printed) != 1 {
		t.Fatalf("batch print = %q, printed %d", r.Header().Get("Location"), len(fp.printed))
	}
	if it := status(t, st, key); it.Status != "sent" || !it.PrintedAt.Valid || it.DesiredMethod != "M" || it.SendVia != "D" || it.Manager != "K2ABC" {
		t.Fatalf("batch print: %+v", it)
	}
	if it := status(t, st, q2); it.Status != "decided" {
		t.Fatalf("a card without a route must stay on the Desk: %+v", it)
	}
	r = postForm(t, h, "/queue/batch", url.Values{"list": {"work"}, "action": {"back"}, "keys": {q2}})
	if it := status(t, st, q2); it.Status != "queued" {
		t.Fatalf("batch back = %q: %+v", r.Header().Get("Location"), it)
	}
}

// TestPortableCallActions: keys with "/" (portable calls) must reach the
// action handlers - they take the key as a query/form value, not a path
// segment, so the slash cannot break routing.
func TestPortableCallActions(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	q := &store.QSO{QSLKey: "W1AW/1|20260929|151631|15M", Call: "W1AW/1",
		QSODate: "20260929", TimeOn: "151631", Band: "15M", Mode: "SSB", Hash: "hp"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if r := postForm(t, h, "/queue/yes", url.Values{"key": {q.QSLKey}}); r.Code != 200 {
		t.Fatalf("yes for portable key = %d: %s", r.Code, r.Body)
	}
	if r := postForm(t, h, "/work/print", url.Values{"key": {q.QSLKey}, "route:" + q.QSLKey: {"D"}}); r.Code != 200 {
		t.Fatalf("print for portable key = %d: %s", r.Code, r.Body)
	}
	if item, _ := st.QueueGet(q.QSLKey); item == nil || item.Status != "sent" {
		t.Fatalf("portable key did not complete: %+v", item)
	}
}

// TestDecidePageIsFullDocument: /decide must be a complete page (htmx, CSS and
// the keyboard handler live in the shell, without them no button works), while
// work=1 actions answer with the swappable fragment only.
func TestDecidePageIsFullDocument(t *testing.T) {
	srv, _, key := newTestServer(t)
	h := srv.Routes()

	body := get(t, h, "/decide").Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "/static/htmx.min.js", "/static/style.css", "/static/live.js", "/static/keys.js", "qslKeys.inbox()", `id="decide"`, "DL1ABC"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/decide missing %q:\n%s", want, body)
		}
	}
	r := postForm(t, h, "/queue/none", url.Values{"key": {key}, "work": {"1"}})
	if r.Code != 200 || strings.Contains(r.Body.String(), "<!DOCTYPE") || !strings.Contains(r.Body.String(), `id="decide"`) {
		t.Fatalf("work=1 action must return the bare fragment, got %d:\n%s", r.Code, r.Body)
	}
}

// TestKeyMapsMatchButtons: every key advertised in a card view's legend must
// have a control (the keyboard handler acts on the element whose data-key
// matches; a two-key sequence like "w b" is data-key="wb").
func TestKeyMapsMatchButtons(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {k2}})

	for _, page := range []string{"/decide", "/work/card"} {
		body := get(t, h, page).Body.String()
		keys := map[string]bool{}
		for _, m := range regexp.MustCompile(`data-key="(\w+)"`).FindAllStringSubmatch(body, -1) {
			for _, c := range m[1] {
				keys[string(c)] = true
			}
		}
		legend := regexp.MustCompile(`<kbd>(\w)</kbd>`).FindAllStringSubmatch(body, -1)
		if len(keys) == 0 || len(legend) == 0 {
			t.Fatalf("%s: %d keys, %d legend keys", page, len(keys), len(legend))
		}
		for _, m := range legend {
			if !keys[m[1]] {
				t.Errorf("%s: legend advertises key %q but no control has it in data-key", page, m[1])
			}
		}
		if !strings.Contains(body, "/static/keys.js") || !strings.Contains(body, "qslKeys.") {
			t.Errorf("%s: no keyboard handler in the page", page)
		}
	}
}

// TestDecideCardShowsResearch: the card carries what the operator needs to
// decide: what the station says about QSL, and what happened with earlier
// cards (sent / received), including portable operations of the same station.
func TestDecideCardShowsResearch(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// An earlier QSO (portable spelling) whose card went out via bureau and
	// whose card came back; plus another card still waiting for this station.
	prior := &store.QSO{QSLKey: "EA8/DL1ABC|20230501|100000|40m", Call: "EA8/DL1ABC", QSODate: "20230501",
		TimeOn: "100000", Band: "40m", Mode: "CW", Hash: "p1", QSLSent: "Y", QSLSDate: "20230502",
		QSLRcvd: "Y", QSLRDate: "20230610", LoTWQSLRcvd: "Y"}
	if _, _, err := st.UpsertQSO(prior); err != nil {
		t.Fatal(err)
	}
	other := addQueued(t, st, "DL1ABC", "20240102")
	_ = other
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", QSLMgr: "VIA BUREAU, DIRECT. LotW.",
		MQSL: "1", EQSL: "0", LoTW: "1", QSLMethod: "B", QSLConfidence: "medium",
		QSLReason: "qslmgr text: bureau and direct both accepted", Name: "Hans Meier",
		Addr1: "Hauptstr. 1", Addr2: "Berlin", Country: "Germany", Zip: "10115",
		BioText:   "Hello and welcome!\nI love CW.\nQSL via bureau is fine, direct needs SAE + 2 IRC.\nLoTW uploaded daily.",
		FetchedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{
		"earlier QSO(s) with this station", // history
		"card already sent 2023-05-02",     // effective sent
		"their card received 2023-06-10",   // received: reply is due
		"LoTW confirmed",
		"more QSO(s) with DL1ABC wait in New QSOs",
		"EA8/DL1ABC",                // portable spelling in the history table
		"a hint, not a decision",    // suggestion is tentative
		"VIA BUREAU, DIRECT. LotW.", // raw qslmgr text shown verbatim
		"paper (mQSL): yes",         // QRZ 1/0 flags read
		"eQSL: no",
		"QSL via bureau is fine, direct needs SAE",
		"cached ",
		"Hans Meier", "Hauptstr. 1", "10115",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("decide card missing %q", want)
		}
	}
	// The excerpt (first bio block) carries only the QSL-relevant lines; the
	// full bio sits behind "full bio".
	excerpt := regexp.MustCompile(`(?s)<pre class="bio">(.*?)</pre>`).FindStringSubmatch(body)
	if excerpt == nil || strings.Contains(excerpt[1], "I love CW") || !strings.Contains(excerpt[1], "2 IRC") {
		t.Errorf("bio excerpt = %q", excerpt)
	}
	if !strings.Contains(body, "full bio") {
		t.Error("the full bio should be one click away")
	}
}

func TestDecideCardResearchStates(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// No history, no station info, QRZ not configured (test server has no refresher).
	body := get(t, h, "/decide").Body.String()
	for _, want := range []string{"first QSO", "QRZ lookups are off"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	// Negative cache entry.
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", NotFound: true}); err != nil {
		t.Fatal(err)
	}
	if body := get(t, h, "/decide").Body.String(); !strings.Contains(body, "station not on QRZ") {
		t.Errorf("not-found state missing:\n%s", body)
	}
	// A forced-in QSO says why it is in the queue.
	q := &store.QSO{QSLKey: "DL2ZZZ|20240103|140000|40m", Call: "DL2ZZZ", QSODate: "20240103",
		TimeOn: "140000", Band: "40m", Mode: "CW", Hash: "ov", Notes: "QSL! rare one"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued",
		OverrideReason: "override: QSL! in notes"}); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/decide").Body.String() // newest first: DL2ZZZ
	for _, want := range []string{"Queued because: override: QSL! in notes", "notes: QSL! rare one"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	_ = key
}

// TestOpenExternal: the app window hands target=_blank links to the system
// browser - only for allow-listed hosts.
func TestOpenExternal(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/api/open-external", url.Values{"url": {"https://www.qrz.com/db/DL1ABC"}}); r.Code != http.StatusNotImplemented {
		t.Fatalf("without a desktop opener = %d, want 501", r.Code)
	}
	var opened []string
	srv.OpenExternal = func(u string) error { opened = append(opened, u); return nil }
	for _, ok := range []string{"https://www.qrz.com/db/DL1ABC", "https://qrz.com/db/K2ABC", "https://clublog.org/logsearch/DL1ABC"} {
		if r := postForm(t, h, "/api/open-external", url.Values{"url": {ok}}); r.Code != http.StatusNoContent {
			t.Errorf("%s = %d, want 204", ok, r.Code)
		}
	}
	for _, bad := range []string{"https://evil.example/qrz.com", "file:///etc/passwd", "javascript:alert(1)", "https://qrz.com.evil.example/", "http://127.0.0.1:8473/settings", ""} {
		if r := postForm(t, h, "/api/open-external", url.Values{"url": {bad}}); r.Code != http.StatusForbidden {
			t.Errorf("%q = %d, want 403", bad, r.Code)
		}
	}
	if len(opened) != 3 {
		t.Fatalf("opened %v", opened)
	}
}

// TestCrossOriginPostsRejected: a web page the operator happens to visit (or
// any LAN page when server.addr is a wildcard) must not be able to drive the
// app through POSTs; same-origin and non-browser clients are fine.
func TestCrossOriginPostsRejected(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	post := func(site string) int {
		req := httptest.NewRequest("POST", "/queue/none", strings.NewReader(url.Values{"key": {key}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("cross-site"); code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", code)
	}
	if it := status(t, st, key); it.Status != "queued" {
		t.Fatalf("cross-site POST changed the card: %+v", it)
	}
	if code := post("same-origin"); code != http.StatusOK {
		t.Fatalf("same-origin POST = %d, want 200", code)
	}
}

func TestQuitEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/api/quit", nil); r.Code != http.StatusNotImplemented {
		t.Fatalf("quit without desktop = %d, want 501", r.Code)
	}
	if strings.Contains(get(t, h, "/settings").Body.String(), "/api/quit") {
		t.Fatal("settings offers Quit outside the desktop app")
	}
	quit := make(chan struct{})
	srv.Quit = func() { close(quit) }
	if !strings.Contains(get(t, h, "/settings").Body.String(), "/api/quit") {
		t.Fatal("settings must offer Quit in the desktop app")
	}
	if r := postForm(t, h, "/api/quit", nil); r.Code != http.StatusOK {
		t.Fatalf("quit = %d", r.Code)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("Quit was not called")
	}
}
