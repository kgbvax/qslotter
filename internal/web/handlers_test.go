package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/printer"
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
	for _, want := range []string{"DL1ABC", "Alice", "Written", "/queue/none", "/queue/decide?method=B", "/queue/decide?method=M", key, `class="cnt"`} {
		if !strings.Contains(full.Body.String(), want) {
			t.Fatalf("/queue missing %q; body:\n%s", want, full.Body)
		}
	}
	// The decision queue offers decisions only; producing a card is the work queue's job.
	for _, gone := range []string{"/queue/print", "/queue/send", "/queue/skip", "/queue/handwrite", "/work/print"} {
		if strings.Contains(full.Body.String(), gone) {
			t.Fatalf("/queue still offers %q", gone)
		}
	}

	compact := get(t, h, "/queue?compact=1")
	if compact.Code != 200 || !strings.Contains(compact.Body.String(), "row-"+key) {
		t.Fatalf("/queue?compact=1 = %d, missing row for %s:\n%s", compact.Code, key, compact.Body)
	}

	// SSE-driven row fetch, both variants.
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)); r.Code != 200 {
		t.Fatalf("/queue/row = %d", r.Code)
	}
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)+"&compact=1"); r.Code != 200 {
		t.Fatalf("/queue/row compact = %d: %s", r.Code, r.Body)
	}
	if r := get(t, h, "/station/DL1ABC"); r.Code != 200 || !strings.Contains(r.Body.String(), "DL1ABC") {
		t.Fatalf("/station/DL1ABC = %d", r.Code)
	}
	for _, p := range []string{"/work", "/work/card", "/done", "/decide", "/nav"} {
		if r := get(t, h, p); r.Code != 200 {
			t.Fatalf("%s = %d: %s", p, r.Code, r.Body)
		}
	}
}

// TestListDecideLeavesQueue: deciding from the list removes the card from the
// decision queue (row, card view, count) and puts it into the work queue.
func TestListDecideLeavesQueue(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	other := addQueued(t, st, "DL2ZZZ", "20240103")

	r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != "" {
		t.Fatalf("list decide = %d, want 200 with an empty body (row removed): %q", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || it.DesiredMethod != "D" {
		t.Fatalf("after decide: %+v", it)
	}

	if body := get(t, h, "/queue").Body.String(); strings.Contains(body, "row-"+key) || !strings.Contains(body, "row-"+other) {
		t.Fatalf("/queue after decide must list only the undecided card:\n%s", body)
	}
	if body := get(t, h, "/decide").Body.String(); strings.Contains(body, "DL1ABC") || !strings.Contains(body, "DL2ZZZ") {
		t.Fatalf("/decide after decide must show only the undecided card:\n%s", body)
	}
	if body := get(t, h, "/work").Body.String(); !strings.Contains(body, "row-"+key) || !strings.Contains(body, "Direct") {
		t.Fatalf("/work must list the decided card under Direct:\n%s", body)
	}
}

func TestDecideViaManager(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// No manager, nothing to prefill from: refused.
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"M"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("M without manager = %d, want 400", r.Code)
	}
	// Free text is not a manager.
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"M"}, "manager": {"VIA BUREAU"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("M with junk manager = %d, want 400", r.Code)
	}
	if it := status(t, st, key); it.Status != "queued" {
		t.Fatalf("refused decisions must not change the card: %+v", it)
	}
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"M"}, "manager": {" dl2xyz "}}); r.Code != 200 {
		t.Fatalf("M with manager = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || it.DesiredMethod != "M" || it.Manager != "DL2XYZ" {
		t.Fatalf("after M: %+v", it)
	}

	// A valid suggested route is used when no manager is typed.
	k2 := addQueued(t, st, "DL3YYY", "20240104")
	if err := st.PutStation(&store.StationInfo{Callsign: "DL3YYY", QSLMethod: "M", QSLRoute: "K2ABC"}); err != nil {
		t.Fatal(err)
	}
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {k2}, "method": {"M"}}); r.Code != 200 {
		t.Fatalf("M with suggested route = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, k2); it.Manager != "K2ABC" {
		t.Fatalf("suggested manager not used: %+v", it)
	}
}

func TestDecideRejectsOtherMethods(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	for _, m := range []string{"", "N", "E", "X", "W"} { // None and Written have their own actions
		if r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {m}}); r.Code != http.StatusBadRequest {
			t.Fatalf("decide method %q = %d, want 400", m, r.Code)
		}
	}
	if it := status(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("rejected methods changed the card: %+v", it)
	}
}

// TestWrittenOnTheSpot: "Written" from the decision queue is its own outcome:
// done, no route, pushed as sent without a send method.
func TestWrittenOnTheSpot(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/queue/written", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("written = %d: %s", r.Code, r.Body)
	}
	it := status(t, st, key)
	if it.Status != "sent" || it.DesiredMethod != "W" {
		t.Fatalf("after written: %+v", it)
	}
	q, _ := st.GetQSO(key)
	if q.QSLSentLocal.String != "Y" || q.QSLSentMethodLocal.String != "" {
		t.Fatalf("local sent state = %v / method %v, want Y / none", q.QSLSentLocal, q.QSLSentMethodLocal)
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
	if r := postForm(t, h, "/queue/written", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("written = %d", r.Code)
	}
	for _, p := range []string{"/queue/none", "/queue/written", "/queue/back", "/work/print"} {
		if r := postForm(t, h, p, url.Values{"key": {key}}); r.Code != http.StatusConflict {
			t.Fatalf("%s on a sent card = %d, want 409", p, r.Code)
		}
	}
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("decide on a sent card = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/queue/none", url.Values{"key": {"NOPE|20240101|000000|20m"}}); r.Code != http.StatusConflict {
		t.Fatalf("unknown key = %d, want 409", r.Code)
	}
	if it := status(t, st, key); it.Status != "sent" || it.DesiredMethod != "W" {
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
	for _, want := range []string{"<!DOCTYPE html>", "Confirming two-way QSO with", "DL2ZZZ", "card 1 of 2", "data-key=\"b\"", "method=D&amp;key=", "work=1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/decide missing %q; body:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `data-qslkey="DL2ZZZ|`) { // the older QSO waits behind the newest one
		t.Fatalf("newest QSO must be the displayed card:\n%s", body)
	}

	// Deciding the newest card shows the next one, as a bare fragment.
	r := postForm(t, h, "/queue/decide", url.Values{"key": {newer}, "method": {"D"}, "work": {"1"}})
	if r.Code != 200 || strings.Contains(r.Body.String(), "<!DOCTYPE") {
		t.Fatalf("decide in card view = %d:\n%s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "DL1ABC") || strings.Contains(r.Body.String(), "DL2ZZZ") {
		t.Fatalf("card view did not advance:\n%s", r.Body)
	}
	if it := status(t, st, newer); it.Status != "decided" || it.DesiredMethod != "D" {
		t.Fatalf("decision not recorded: %+v", it)
	}

	// Declining the last card lands on the empty state.
	r = postForm(t, h, "/queue/none", url.Values{"key": {key}, "work": {"1"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "No QSOs waiting for a decision") {
		t.Fatalf("empty state = %d:\n%s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "/work/card") {
		t.Fatalf("empty state should point at the waiting work cards:\n%s", r.Body)
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
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"B"}})
	if r := postForm(t, h, "/queue/back", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("back = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after back: %+v", it)
	}
	if !strings.Contains(get(t, h, "/queue").Body.String(), "row-"+key) {
		t.Fatal("card missing from the decision queue after back")
	}
}

// TestWorkQueue: decided cards are grouped by route, one card at a time with
// an optional route filter.
func TestWorkQueue(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	kd := addQueued(t, st, "DL2ZZZ", "20240103")
	km := addQueued(t, st, "DL3YYY", "20240104")
	for _, c := range []struct{ k, m, mgr string }{{key, "B", ""}, {kd, "D", ""}, {km, "M", "K2ABC"}} {
		if r := postForm(t, h, "/queue/decide", url.Values{"key": {c.k}, "method": {c.m}, "manager": {c.mgr}}); r.Code != 200 {
			t.Fatalf("decide %s = %d: %s", c.m, r.Code, r.Body)
		}
	}

	list := get(t, h, "/work").Body.String()
	iD, iM, iB := strings.Index(list, "Direct <small>"), strings.Index(list, "Via manager <small>"), strings.Index(list, "Bureau <small>")
	if iD < 0 || iM < iD || iB < iM {
		t.Fatalf("/work groups must read Direct, Via manager, Bureau:\n%s", list)
	}
	if !strings.Contains(list, "K2ABC") {
		t.Fatalf("/work must show the manager:\n%s", list)
	}

	// Card view: full page, newest decided card first; the route is spelled out.
	page := get(t, h, "/work/card").Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "DL3YYY", "K2ABC", "card 1 of 3", `id="workcard"`, "/work/print?key=", "data-key=\"p\""} {
		if !strings.Contains(page, want) {
			t.Fatalf("/work/card missing %q:\n%s", want, page)
		}
	}
	// The route filter narrows the stack; other routes are not shown.
	frag := getHX(t, h, "/work/card?filter=B").Body.String()
	if !strings.Contains(frag, "DL1ABC") || strings.Contains(frag, "DL3YYY") || !strings.Contains(frag, "card 1 of 1") || strings.Contains(frag, "<!DOCTYPE") {
		t.Fatalf("/work/card?filter=B (fragment):\n%s", frag)
	}
	if !strings.Contains(getHX(t, h, "/work/card?filter=D").Body.String(), "DL2ZZZ") {
		t.Fatal("filter=D lost the direct card")
	}
}

// TestWorkPrintMarksSentAndPending: printing a decided card completes it: it
// is marked sent with its route, waits for push-back, and the next card shows.
func TestWorkPrintMarksSentAndPending(t *testing.T) {
	srv, st, key := newTestServer(t)
	fp := srv.printer.(*fakePrinter)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
	postForm(t, h, "/queue/decide", url.Values{"key": {k2}, "method": {"B"}})

	r := postForm(t, h, "/work/print", url.Values{"key": {k2}, "view": {"work"}})
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
	r = postForm(t, h, "/work/print", url.Values{"key": {key}, "view": {"work"}})
	if !strings.Contains(r.Body.String(), "Nothing to produce") {
		t.Fatalf("empty work queue:\n%s", r.Body)
	}
}

// TestWorkPrintFailureKeepsCardDecided: a printer error must not complete the
// card - it stays in the work queue.
func TestWorkPrintFailureKeepsCardDecided(t *testing.T) {
	srv, st, key := newTestServer(t)
	srv.printer = &fakePrinter{err: fmt.Errorf("lp: no default destination")}
	h := srv.Routes()
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})

	r := postForm(t, h, "/work/print", url.Values{"key": {key}, "view": {"work"}})
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

// TestPrintNeedsADecision: nothing is printed for a card that was not decided
// (no fallback route is invented).
func TestPrintNeedsADecision(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}}); r.Code != http.StatusConflict {
		t.Fatalf("print of undecided card = %d, want 409", r.Code)
	}
	if n := len(srv.printer.(*fakePrinter).printed); n != 0 {
		t.Fatalf("printer called %d times", n)
	}
	if it := status(t, st, key); it.Status != "queued" {
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
	postForm(t, h, "/queue/decide", url.Values{"key": {old.QSLKey}, "method": {"B"}})
	if r := postForm(t, h, "/work/print", url.Values{"key": {old.QSLKey}}); r.Code != 200 {
		t.Fatalf("print old QSO = %d: %s", r.Code, r.Body)
	}
	if len(fp.printed) != 1 {
		t.Fatalf("printer called %d times, want 1", len(fp.printed))
	}
}

func TestWorkWrittenKeepsRouteAndBack(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
	postForm(t, h, "/queue/decide", url.Values{"key": {k2}, "method": {"B"}})

	if r := postForm(t, h, "/queue/written", url.Values{"key": {key}, "view": {"work"}}); r.Code != 200 {
		t.Fatalf("written in work view = %d: %s", r.Code, r.Body)
	}
	it := status(t, st, key)
	q, _ := st.GetQSO(key)
	if it.Status != "sent" || it.DesiredMethod != "D" || q.QSLSentMethodLocal.String != "D" || it.PrintedAt.Valid {
		t.Fatalf("written keeps the decided route: %+v / %v", it, q.QSLSentMethodLocal)
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
	postForm(t, h, "/queue/written", url.Values{"key": {key}})
	postForm(t, h, "/queue/none", url.Values{"key": {kn}})

	body := get(t, h, "/done").Body.String()
	for _, want := range []string{"row-" + key, "written on the spot", "row-" + kn, "no card", "/queue/reopen?key="} {
		if !strings.Contains(body, want) {
			t.Fatalf("/done missing %q:\n%s", want, body)
		}
	}
	if r := postForm(t, h, "/queue/reopen", url.Values{"key": {key}}); r.Code != 200 || r.Header().Get("HX-Trigger") != "" {
		t.Fatalf("reopen = %d, trigger %q", r.Code, r.Header().Get("HX-Trigger"))
	}
	if it := status(t, st, key); it.Status != "queued" || it.DesiredMethod != "" {
		t.Fatalf("after reopen: %+v", it)
	}
	if q, _ := st.GetQSO(key); q.QSLSentLocal.Valid {
		t.Fatalf("reopen left local sent state: %v", q.QSLSentLocal)
	}
	if strings.Contains(get(t, h, "/done").Body.String(), "row-"+key) || !strings.Contains(get(t, h, "/queue").Body.String(), "row-"+key) {
		t.Fatal("reopened card must leave /done and return to /queue")
	}

	// Reopening a card Clublog already has as sent warns the operator.
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"B"}})
	postForm(t, h, "/work/print", url.Values{"key": {key}})
	pend, _ := st.PendingPushBack()
	if err := st.MarkPushed(pend[0]); err != nil {
		t.Fatal(err)
	}
	r := postForm(t, h, "/queue/reopen", url.Values{"key": {key}})
	if r.Code != 200 || !strings.Contains(r.Header().Get("HX-Trigger"), "qslNotice") {
		t.Fatalf("reopen of a pushed card = %d, trigger %q", r.Code, r.Header().Get("HX-Trigger"))
	}
}

// TestQueueChangedEvent: every move tells the other windows.
func TestQueueChangedEvent(t *testing.T) {
	srv, _, key := newTestServer(t)
	ch, unsub := srv.broker.Subscribe()
	defer unsub()
	postForm(t, srv.Routes(), "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
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
	if !strings.Contains(nav, `Queue <span class="cnt">2</span>`) || strings.Contains(nav, `Work <span`) {
		t.Fatalf("nav = %s", nav)
	}
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
	nav = get(t, h, "/nav").Body.String()
	if !strings.Contains(nav, `Queue <span class="cnt">1</span>`) || !strings.Contains(nav, `Work <span class="cnt">1</span>`) {
		t.Fatalf("nav after decide = %s", nav)
	}
}

// TestPortableCallStationPage: callsigns containing "/" must reach the
// station page and its decision panel.
func TestPortableCallStationPage(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	q := &store.QSO{QSLKey: "EA8/DL1ABC|20240102|130000|20m", Call: "EA8/DL1ABC",
		QSODate: "20240102", TimeOn: "130000", Band: "20m", Mode: "SSB", Hash: "h2"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	r := get(t, h, "/station/EA8/DL1ABC")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "EA8/DL1ABC") {
		t.Fatalf("/station/EA8/DL1ABC = %d", r.Code)
	}
	if !strings.Contains(r.Body.String(), "row-"+q.QSLKey) {
		t.Fatalf("station page missing decision panel row for %s", q.QSLKey)
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
	postForm(t, h, "/queue/decide", url.Values{"key": {key}, "method": {"D"}})
	if r := get(t, h, "/queue/row?key="+url.QueryEscape(key)); r.Code != http.StatusNoContent {
		t.Fatalf("/queue/row for a decided card = %d, want 204", r.Code)
	}
	if r := get(t, h, "/queue/row?key="+url.QueryEscape("GONE|20240101|000000|20m")); r.Code != http.StatusNotFound {
		t.Fatalf("/queue/row for unknown QSO = %d, want 404", r.Code)
	}
}

// TestBatchActions: one POST applies an action to many rows; cards that were
// handled meanwhile are counted as failed, not applied twice.
func TestBatchActions(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	q2 := addQueued(t, st, "DL2ZZZ", "20240103")
	q3 := addQueued(t, st, "DL3YYY", "20240104")
	postForm(t, h, "/queue/written", url.Values{"key": {q3}}) // already handled

	r := postForm(t, h, "/queue/batch", url.Values{"action": {"D"}, "keys": {key, q2, q3}})
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/queue?done=2&failed=1" {
		t.Fatalf("batch D = %d %q", r.Code, r.Header().Get("Location"))
	}
	for _, k := range []string{key, q2} {
		if it := status(t, st, k); it.Status != "decided" || it.DesiredMethod != "D" {
			t.Fatalf("%s after batch D: %+v", k, it)
		}
	}
	if it := status(t, st, q3); it.Status != "sent" || it.DesiredMethod != "W" {
		t.Fatalf("batch touched an already handled card: %+v", it)
	}
	if body := get(t, h, "/queue?done=2&failed=1").Body.String(); !strings.Contains(body, "2 done") || !strings.Contains(body, "1 skipped") {
		t.Fatalf("queue page must report the batch result:\n%s", body)
	}

	// No guessing: via-manager needs a callsign per card, so it is not a batch action.
	if r := postForm(t, h, "/queue/batch", url.Values{"action": {"M"}, "keys": {key}}); r.Code != http.StatusBadRequest {
		t.Fatalf("batch M = %d, want 400", r.Code)
	}
	if r := postForm(t, h, "/queue/batch", url.Values{"action": {"print"}, "keys": {key}}); r.Code != http.StatusBadRequest {
		t.Fatalf("print is a work-queue batch action, got %d on the decision queue", r.Code)
	}

	// Compact redirect lands back on the compact page.
	k4 := addQueued(t, st, "DL4WWW", "20240105")
	r = postForm(t, h, "/queue/batch", url.Values{"action": {"none"}, "keys": {k4}, "compact": {"1"}})
	if r.Header().Get("Location") != "/queue?compact=1&done=1&failed=0" {
		t.Fatalf("batch compact redirect = %q", r.Header().Get("Location"))
	}
	if it := status(t, st, k4); it.Status != "skipped" || it.DesiredMethod != "N" {
		t.Fatalf("batch none: %+v", it)
	}

	// Work-queue batches: print and back.
	fp := srv.printer.(*fakePrinter)
	r = postForm(t, h, "/queue/batch", url.Values{"list": {"work"}, "action": {"print"}, "keys": {key}})
	if r.Header().Get("Location") != "/work?done=1&failed=0" || len(fp.printed) != 1 {
		t.Fatalf("batch print = %q, printed %d", r.Header().Get("Location"), len(fp.printed))
	}
	if it := status(t, st, key); it.Status != "sent" || !it.PrintedAt.Valid {
		t.Fatalf("batch print: %+v", it)
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
	if r := postForm(t, h, "/queue/decide", url.Values{"key": {q.QSLKey}, "method": {"D"}}); r.Code != 200 {
		t.Fatalf("decide for portable key = %d: %s", r.Code, r.Body)
	}
	if r := postForm(t, h, "/work/print", url.Values{"key": {q.QSLKey}}); r.Code != 200 {
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
	for _, want := range []string{"<!DOCTYPE html>", "/static/htmx.min.js", "/static/style.css", "/static/live.js", "keydown", `id="decide"`, "DL1ABC"} {
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
// have a button (the keyboard handler clicks the button whose data-key matches).
func TestKeyMapsMatchButtons(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/decide", url.Values{"key": {k2}, "method": {"D"}})
	_ = key

	for _, page := range []string{"/decide", "/work/card"} {
		body := get(t, h, page).Body.String()
		buttons := map[string]bool{}
		for _, m := range regexp.MustCompile(`data-key="(\w)"`).FindAllStringSubmatch(body, -1) {
			buttons[m[1]] = true
		}
		legend := regexp.MustCompile(`<kbd>(\w)</kbd>`).FindAllStringSubmatch(body, -1)
		if len(buttons) == 0 || len(legend) == 0 {
			t.Fatalf("%s: %d buttons, %d legend keys", page, len(buttons), len(legend))
		}
		for _, m := range legend {
			if !buttons[m[1]] {
				t.Errorf("%s: legend advertises key %q but no button has data-key=%q", page, m[1], m[1])
			}
		}
		if !strings.Contains(body, "keydown") {
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
		"other card(s) for this station still pending",
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
	for _, want := range []string{"first QSO with this station", "QRZ lookups are off"} {
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
