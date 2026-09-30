package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
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
	return srv, st, q.QSLKey
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
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

func TestQueuePagesRender(t *testing.T) {
	srv, _, key := newTestServer(t)
	h := srv.Routes()

	full := get(t, h, "/queue")
	if full.Code != 200 {
		t.Fatalf("/queue = %d: %s", full.Code, full.Body)
	}
	for _, want := range []string{"DL1ABC", "Alice", "Written", "None", "/method?method=B", key} {
		if !strings.Contains(full.Body.String(), want) {
			t.Fatalf("/queue missing %q; body:\n%s", want, full.Body)
		}
	}

	compact := get(t, h, "/queue?compact=1")
	if compact.Code != 200 {
		t.Fatalf("/queue?compact=1 = %d: %s", compact.Code, compact.Body)
	}
	if !strings.Contains(compact.Body.String(), "row-"+key) {
		t.Fatalf("compact queue missing row for %s:\n%s", key, compact.Body)
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
}

func TestMethodDecisionFlow(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// Record a direct decision.
	if r := postForm(t, h, "/queue/method", url.Values{"key": {key}, "method": {"D"}}); r.Code != 200 {
		t.Fatalf("method D = %d: %s", r.Code, r.Body)
	}
	item, _ := st.QueueGet(key)
	if item == nil || item.DesiredMethod != "D" {
		t.Fatalf("QueueGet after method D = %+v", item)
	}

	// Send uses the recorded decision, not a hardcoded bureau default.
	if r := postForm(t, h, "/queue/send", url.Values{"key": {key}}); r.Code != 200 {
		t.Fatalf("send = %d: %s", r.Code, r.Body)
	}
	item, _ = st.QueueGet(key)
	if item.Status != "sent" {
		t.Fatalf("status = %q, want sent", item.Status)
	}
	qsos, _ := st.RecentQSOsByCall("DL1ABC", 1)
	if qsos[0].QSLSentLocal.String != "Y" || qsos[0].QSLSentMethodLocal.String != "D" {
		t.Fatalf("local sent state = %v/%v, want Y/D", qsos[0].QSLSentLocal, qsos[0].QSLSentMethodLocal)
	}

	// None skips with the decision recorded.
	srv2, st2, key2 := newTestServer(t)
	h2 := srv2.Routes()
	if r := postForm(t, h2, "/queue/none", url.Values{"key": {key2}}); r.Code != 200 {
		t.Fatalf("none = %d: %s", r.Code, r.Body)
	}
	item2, _ := st2.QueueGet(key2)
	if item2.Status != "skipped" || item2.DesiredMethod != "N" {
		t.Fatalf("after none: %+v, want skipped/N", item2)
	}
	// Recompute must not resurrect the skipped item.
	if r := postForm(t, h2, "/queue/recompute", nil); r.Code != 200 {
		t.Fatalf("recompute = %d", r.Code)
	}
	queued, _ := st2.QueueByStatus("queued")
	for _, it := range queued {
		if it.QSLKey == key2 {
			t.Fatal("recompute resurrected a skipped (none) item")
		}
	}

	// Handwrite marks sent via the chosen method.
	srv3, st3, key3 := newTestServer(t)
	h3 := srv3.Routes()
	_ = st3.QueueSetMethod(key3, "M", "DL2XYZ")
	if r := postForm(t, h3, "/queue/handwrite", url.Values{"key": {key3}}); r.Code != 200 {
		t.Fatalf("handwrite = %d: %s", r.Code, r.Body)
	}
	item3, _ := st3.QueueGet(key3)
	if item3.Status != "sent" {
		t.Fatalf("handwrite status = %q, want sent", item3.Status)
	}
	qsos3, _ := st3.RecentQSOsByCall("DL1ABC", 1)
	if qsos3[0].QSLSentMethodLocal.String != "M" {
		t.Fatalf("handwrite method = %q, want M", qsos3[0].QSLSentMethodLocal.String)
	}
}

// TestSendRejectsDeclinedCard: a recorded "none" decision must never be
// flipped to a send by the Sent/Handwritten buttons.
func TestSendRejectsDeclinedCard(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	_ = st.QueueSetMethod(key, "N", "")
	if r := postForm(t, h, "/queue/send", url.Values{"key": {key}}); r.Code != http.StatusBadRequest {
		t.Fatalf("send on declined card = %d, want 400", r.Code)
	}
	if r := postForm(t, h, "/queue/handwrite", url.Values{"key": {key}}); r.Code != http.StatusBadRequest {
		t.Fatalf("handwrite on declined card = %d, want 400", r.Code)
	}
	item, _ := st.QueueGet(key)
	if item.Status != "queued" {
		t.Fatalf("status after rejected sends = %q, want queued", item.Status)
	}
	qsos, _ := st.RecentQSOsByCall("DL1ABC", 1)
	if qsos[0].QSLSentLocal.Valid {
		t.Fatalf("declined card marked sent locally: %v", qsos[0].QSLSentLocal)
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

// TestQueueRowForNonQueuedQSO: SSE fires for every new QSO, eligible or not;
// the row endpoint must not blow up on a QSO that is not in the queue.
func TestQueueRowForNonQueuedQSO(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	q := &store.QSO{QSLKey: "DL2ZZZ|20240103|140000|40m", Call: "DL2ZZZ",
		QSODate: "20240103", TimeOn: "140000", Band: "40m", Mode: "CW", Hash: "h3"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	r := get(t, h, "/queue/row?key="+url.QueryEscape(q.QSLKey))
	if r.Code != http.StatusNoContent {
		t.Fatalf("/queue/row for non-queued QSO = %d, want 204", r.Code)
	}
}

// TestBatchActions: one POST applies an action to many rows, each with its
// own chosen method; declined cards are left untouched.
func TestBatchActions(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// A second item with a recorded direct decision.
	q2 := &store.QSO{QSLKey: "DL2ZZZ|20240103|140000|40m", Call: "DL2ZZZ",
		QSODate: "20240103", TimeOn: "140000", Band: "40m", Mode: "CW", Hash: "h3"}
	if _, _, err := st.UpsertQSO(q2); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q2.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueSetMethod(q2.QSLKey, "D", ""); err != nil {
		t.Fatal(err)
	}
	// A declined card stays queued here to prove batch-send skips it.
	q3 := &store.QSO{QSLKey: "DL3YYY|20240104|150000|40m", Call: "DL3YYY",
		QSODate: "20240104", TimeOn: "150000", Band: "40m", Mode: "CW", Hash: "h4"}
	if _, _, err := st.UpsertQSO(q3); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q3.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueSetMethod(q3.QSLKey, "N", ""); err != nil {
		t.Fatal(err)
	}

	r := postForm(t, h, "/queue/batch", url.Values{
		"action": {"sent"},
		"keys":   {key, q2.QSLKey, q3.QSLKey},
	})
	if r.Code != http.StatusSeeOther {
		t.Fatalf("batch sent = %d, want 303", r.Code)
	}
	item1, _ := st.QueueGet(key)
	if item1.Status != "sent" || item1.DesiredMethod != "" {
		t.Fatalf("batch item1 = %+v (method should resolve to suggestion/B at send)", item1)
	}
	item2, _ := st.QueueGet(q2.QSLKey)
	if item2.Status != "sent" {
		t.Fatalf("batch item2 status = %q, want sent", item2.Status)
	}
	sent2, _ := st.GetQSO(q2.QSLKey)
	if sent2.QSLSentMethodLocal.String != "D" {
		t.Fatalf("batch item2 method = %q, want D (recorded decision)", sent2.QSLSentMethodLocal.String)
	}
	item3, _ := st.QueueGet(q3.QSLKey)
	if item3.Status != "queued" {
		t.Fatalf("declined item status = %q, want untouched queued", item3.Status)
	}

	// none batch skips everything selected.
	r = postForm(t, h, "/queue/batch", url.Values{"action": {"none"}, "keys": {q3.QSLKey}})
	if r.Code != http.StatusSeeOther {
		t.Fatalf("batch none = %d, want 303", r.Code)
	}
	item3, _ = st.QueueGet(q3.QSLKey)
	if item3.Status != "skipped" || item3.DesiredMethod != "N" {
		t.Fatalf("after batch none: %+v, want skipped/N", item3)
	}

	// Compact redirect lands back on the compact page.
	r = postForm(t, h, "/queue/batch", url.Values{"action": {"skip"}, "keys": {key}, "compact": {"1"}})
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/queue?compact=1" {
		t.Fatalf("batch compact redirect = %d %q", r.Code, r.Header().Get("Location"))
	}
}

// TestDecideFlow: the decide view shows one card; every one-click action
// advances to the next queued card; an empty queue renders the invitation.
func TestDecideFlow(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()

	// A second queued card.
	q2 := &store.QSO{QSLKey: "DL2ZZZ|20240103|140000|40m", Call: "DL2ZZZ",
		QSODate: "20240103", TimeOn: "140000", Band: "40m", Mode: "CW",
		RSTSent: "599", Hash: "h3"}
	if _, _, err := st.UpsertQSO(q2); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q2.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}

	// The decide page shows the first card with one-click stamps.
	r := get(t, h, "/decide")
	if r.Code != 200 {
		t.Fatalf("/decide = %d", r.Code)
	}
	body := r.Body.String()
	for _, want := range []string{"Confirming two-way QSO with", "DL1ABC", "data-key=\"b\"", "method=D&amp;key=", "work=1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/decide missing %q; body:\n%s", want, body)
		}
	}

	// Stamping the first card advances to the second (not a table row).
	r = postForm(t, h, "/queue/method", url.Values{"key": {key}, "method": {"D"}, "work": {"1"}})
	if r.Code != 200 {
		t.Fatalf("decide stamp = %d: %s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), "DL2ZZZ") || strings.Contains(r.Body.String(), "DL1ABC") {
		t.Fatalf("decide did not advance to next card:\n%s", r.Body)
	}
	if item, _ := st.QueueGet(key); item.DesiredMethod != "D" || item.Status != "decided" {
		t.Fatalf("stamp did not record decision: %+v", item)
	}

	// Declining the last card lands on the empty state.
	r = postForm(t, h, "/queue/none", url.Values{"key": {q2.QSLKey}, "work": {"1"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "No cards waiting") {
		t.Fatalf("decide empty state = %d:\n%s", r.Code, r.Body)
	}
	if item, _ := st.QueueGet(q2.QSLKey); item.Status != "skipped" || item.DesiredMethod != "N" {
		t.Fatalf("none in decide mode: %+v", item)
	}

	// Queue-page rows carry the one-click stamp strip.
	r = get(t, h, "/queue")
	for _, want := range []string{"/method?method=B", "/none", "stamp b"} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatalf("/queue missing stamp strip element %q", want)
		}
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
	if r := postForm(t, h, "/queue/skip", url.Values{"key": {q.QSLKey}}); r.Code != 200 {
		t.Fatalf("skip for portable key = %d: %s", r.Code, r.Body)
	}
	if item, _ := st.QueueGet(q.QSLKey); item == nil || item.Status != "skipped" {
		t.Fatalf("portable key skip did not land: %+v", item)
	}
}
