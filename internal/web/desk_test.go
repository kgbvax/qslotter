package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/store"
)

// twoQSOCard puts a second QSO with DL1ABC (another band) next to the test
// server's DL1ABC QSO and answers "yes" for both: one Desk card, two QSOs.
func twoQSOCard(t *testing.T, srv *Server, st store.Store, first string) (second string) {
	t.Helper()
	q := &store.QSO{QSLKey: "DL1ABC|20240102|090000|40m", Call: "DL1ABC",
		QSODate: "20240102", TimeOn: "090000", Band: "40m", Mode: "CW", RSTSent: "599", Hash: "h-dl1abc-2"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	h := srv.Routes()
	for _, k := range []string{first, q.QSLKey} {
		if r := postForm(t, h, "/queue/yes", url.Values{"key": {k}}); r.Code != 200 {
			t.Fatalf("yes %s = %d", k, r.Code)
		}
	}
	return q.QSLKey
}

// TestDeskOneCardForSeveralQSOs: open QSOs with the same call are one card
// (B9); printing it prints once and finishes every QSO on it.
func TestDeskOneCardForSeveralQSOs(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	addQueued(t, st, "DL1ABC/P", "20240103") // a /P operation is a separate card
	postForm(t, h, "/queue/yes", url.Values{"key": {"DL1ABC/P|20240103|140000|40m"}})

	page := get(t, h, "/work/card?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{"card 2 of 2", "Confirming 2 two-way QSOs with", `name="key" value="` + key + `" checked`, `name="key" value="` + k2 + `" checked`} {
		if !strings.Contains(page, want) {
			t.Fatalf("/work/card missing %q:\n%s", want, page)
		}
	}
	list := get(t, h, "/work").Body.String()
	if strings.Count(list, `class="md-row"`) != 2 || !strings.Contains(list, "2 QSOs, one card") {
		t.Fatalf("/work must list one row per card:\n%s", list)
	}

	fp := srv.printer.(*fakePrinter)
	r := postForm(t, h, "/work/print?view=work", url.Values{"key": {key, k2}, "route": {"B"}})
	if r.Code != 200 || len(fp.printed) != 1 {
		t.Fatalf("print card = %d, printed %d: %s", r.Code, len(fp.printed), r.Body)
	}
	for _, k := range []string{key, k2} {
		if it := status(t, st, k); it.Status != "sent" || it.DesiredMethod != "B" || !it.PrintedAt.Valid {
			t.Fatalf("%s after printing the card: %+v", k, it)
		}
	}
}

// TestDeskUntickedQSOStays: a QSO left off the card stays at the Desk.
func TestDeskUntickedQSOStays(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	if r := postForm(t, h, "/work/written?view=work", url.Values{"key": {k2}, "route": {"D"}}); r.Code != 200 {
		t.Fatalf("written = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, k2); it.Status != "sent" {
		t.Fatalf("ticked QSO: %+v", it)
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("unticked QSO must stay at the Desk: %+v", it)
	}
	if r := postForm(t, h, "/work/written", url.Values{"route": {"D"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("no QSO ticked = %d, want 400", r.Code)
	}
	// A reload keeps the operator's unsaved choices.
	body := getHX(t, h, "/work/card?key="+url.QueryEscape(key)+"&route=MB&manager=k2abc").Body.String()
	if !strings.Contains(body, `value="MB" data-key="v" checked`) || !strings.Contains(body, `value="K2ABC"`) {
		t.Fatalf("reload lost the chosen route/manager:\n%s", body)
	}
}

// TestDeskAtomic: when one QSO of a card was handled elsewhere, nothing on
// the card changes (409) and nothing is printed.
func TestDeskAtomic(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	if err := st.QueueDeskDecline(k2); err != nil {
		t.Fatal(err)
	}
	if r := postForm(t, h, "/work/print", url.Values{"key": {key, k2}, "route": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("print with a stale QSO = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/work/written", url.Values{"key": {key, k2}, "route": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("written with a stale QSO = %d, want 409", r.Code)
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("the other QSO changed: %+v", it)
	}
	if n := len(srv.printer.(*fakePrinter).printed); n != 0 {
		t.Fatalf("printer called %d times", n)
	}
}

// TestDeskRequested: "requested (OQRS)" records the channel and note, sends
// no card, marks QSL_RCVD=R for push-back and is reopenable (B4b).
func TestDeskRequested(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})

	if r := postForm(t, h, "/work/requested", url.Values{"key": {key}, "channel": {"fax"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("unknown channel = %d, want 400", r.Code)
	}
	r := postForm(t, h, "/work/requested?view=work", url.Values{"key": {key}, "channel": {"oqrs"}, "note": {" 2 USD via Clublog "}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Nothing to write or print") {
		t.Fatalf("requested = %d:\n%s", r.Code, r.Body)
	}
	it := status(t, st, key)
	if it.Status != "requested" || it.DesiredMethod != "R" || it.Channel != "OQRS" || it.Note != "2 USD via Clublog" || !it.SentAt.Valid {
		t.Fatalf("after requested: %+v", it)
	}
	q, _ := st.GetQSO(key)
	if q.QSLRcvdLocal.String != "R" || q.QSLSentLocal.Valid {
		t.Fatalf("requested must set QSL_RCVD=R and leave QSL_SENT alone: rcvd %v sent %v", q.QSLRcvdLocal, q.QSLSentLocal)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Fatalf("pending push = %d, want 1", len(pend))
	}
	if body := get(t, h, "/done").Body.String(); !strings.Contains(body, "their card requested via OQRS: 2 USD via Clublog") {
		t.Fatalf("/done must show the request:\n%s", body)
	}
	if _, err := st.QueueReopen(key); err != nil {
		t.Fatal(err)
	}
	if q, _ := st.GetQSO(key); q.QSLRcvdLocal.Valid {
		t.Fatalf("reopen must clear the unpushed request: %v", q.QSLRcvdLocal)
	}
}

// TestDeskNoCardAndBack: change of mind at the Desk (B6), and back to the
// Inbox, both for the whole card.
func TestDeskNoCardAndBack(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	if r := postForm(t, h, "/work/back", url.Values{"key": {key, k2}}); r.Code != 200 {
		t.Fatalf("back = %d", r.Code)
	}
	for _, k := range []string{key, k2} {
		if it := status(t, st, k); it.Status != "queued" {
			t.Fatalf("%s after back: %+v", k, it)
		}
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
	}
	if r := postForm(t, h, "/work/none?view=work", url.Values{"key": {key, k2}}); r.Code != 200 {
		t.Fatalf("no card = %d", r.Code)
	}
	for _, k := range []string{key, k2} {
		if it := status(t, st, k); it.Status != "skipped" || it.DesiredMethod != "N" {
			t.Fatalf("%s after no card: %+v", k, it)
		}
	}
}

// TestDeskManagerAddressAndOQRSHint: the manager route shows the manager's
// address (B3); QRZ mentioning OQRS marks "requested" as the suggestion.
func TestDeskManagerAddressAndOQRSHint(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", QSLMgr: "K2ABC", BioText: "QSL via OQRS only"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutStation(&store.StationInfo{Callsign: "K2ABC", Name: "Joe Manager", Addr1: "1 Main St", Addr2: "Springfield", Country: "USA"}); err != nil {
		t.Fatal(err)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})

	page := get(t, h, "/work/card").Body.String()
	for _, want := range []string{`value="MD" data-key="m" checked`, "Joe Manager", "1 Main St", "oqrs-hint", `class="mini suggested" data-key="r"`, `data-mgr="K2ABC"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("/work/card missing %q:\n%s", want, page)
		}
	}
	if b := get(t, h, "/work/manager?manager=k2abc").Body.String(); !strings.Contains(b, "Joe Manager") {
		t.Fatalf("manager block:\n%s", b)
	}
	if b := get(t, h, "/work/manager?manager=via+bureau").Body.String(); !strings.Contains(b, "Type the manager") {
		t.Fatalf("manager block for junk:\n%s", b)
	}
	if b := get(t, h, "/work").Body.String(); !strings.Contains(b, ">OQRS</span>") {
		t.Fatalf("/work must flag OQRS:\n%s", b)
	}
}

// TestDeskBatchCoversWholeCard: a ticked card in the list finishes all of its
// QSOs with the card's route.
func TestDeskBatchCoversWholeCard(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	lead := k2 // newest QSO leads the card
	// The row's QSOs come along as card:<lead>; a QSO with the call that
	// joined the Desk after the page was drawn is not swept along.
	late := addQueued(t, st, "DL1ABC", "20240104")
	postForm(t, h, "/queue/yes", url.Values{"key": {late}})
	r := postForm(t, h, "/queue/batch", url.Values{"list": {"work"}, "action": {"written"}, "keys": {lead},
		"card:" + lead: {key, k2}, "route:" + lead: {"MB"}, "manager:" + lead: {"K2ABC"}})
	if r.Header().Get("Location") != "/work?done=1&failed=0" {
		t.Fatalf("batch = %q", r.Header().Get("Location"))
	}
	for _, k := range []string{key, k2} {
		if it := status(t, st, k); it.Status != "sent" || it.DesiredMethod != "M" || it.SendVia != "B" || it.Manager != "K2ABC" {
			t.Fatalf("%s after batch: %+v", k, it)
		}
	}
	if it := status(t, st, late); it.Status != "decided" {
		t.Fatalf("a QSO the operator did not see was finished by the batch: %+v", it)
	}
}

// TestDeskStaleAfterBack: a Desk page whose card went back to the Inbox in
// another window gets 409, it does not record the QSOs as "written now".
func TestDeskStaleAfterBack(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	k2 := twoQSOCard(t, srv, st, key)
	postForm(t, h, "/work/back", url.Values{"key": {key, k2}})
	for _, p := range []string{"/work/written", "/work/none", "/work/print", "/work/requested"} {
		if r := postForm(t, h, p, url.Values{"key": {key, k2}, "route": {"B"}, "channel": {"OQRS"}}); r.Code != http.StatusConflict {
			t.Fatalf("%s on cards back in the Inbox = %d, want 409", p, r.Code)
		}
	}
	for _, k := range []string{key, k2} {
		if it := status(t, st, k); it.Status != "queued" {
			t.Fatalf("%s changed: %+v", k, it)
		}
	}
}

// TestDeskReloadCarriesChoicesOnlyToTheirCard: route/manager carried through
// a reload apply only when the key is still on the shown card; an empty
// route or manager is not a choice and keeps the preselection.
func TestDeskReloadCarriesChoicesOnlyToTheirCard(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", QSLMgr: "direct"}); err != nil {
		t.Fatal(err)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	gone := "DL9GON|20240101|120000|20m"
	b := getHX(t, h, "/work/card?key="+url.QueryEscape(gone)+"&route=MB&manager=K2ABC").Body.String()
	if strings.Contains(b, `value="MB" data-key="v" checked`) || strings.Contains(b, `value="K2ABC"`) || !strings.Contains(b, `value="D" data-key="d" checked`) {
		t.Fatalf("choices for a card that left were applied to another card:\n%s", b)
	}
	b = getHX(t, h, "/work/card?key="+url.QueryEscape(key)+"&route=&manager=").Body.String()
	if !strings.Contains(b, `value="D" data-key="d" checked`) || !strings.Contains(b, "suggested: by QRZ") {
		t.Fatalf("an empty carried route wiped the preselection:\n%s", b)
	}
}

// TestDeskManagerBlockWithoutQRZ: no endless "looking up" when lookups are off.
func TestDeskManagerBlockWithoutQRZ(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if b := get(t, srv.Routes(), "/work/manager?manager=K2ABC").Body.String(); !strings.Contains(b, "QRZ lookups are off") {
		t.Fatalf("manager block without QRZ:\n%s", b)
	}
}

// TestDeskBrokenTemplateStopsPrint: a card template that exists but does not
// load fails the print (the card stays on the Desk); a missing one falls
// back to the built-in template.
func TestDeskBrokenTemplateStopsPrint(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	bad := filepath.Join(t.TempDir(), "card.yaml")
	if err := os.WriteFile(bad, []byte("rows:\n  max: 3\n  pitch_mm: 5,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.cfg.Card.Template = bad
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != http.StatusInternalServerError || !strings.Contains(r.Body.String(), "card template") {
		t.Fatalf("print with a broken template = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || len(srv.printer.(*fakePrinter).printed) != 0 {
		t.Fatalf("a failed template must not print or finish the card: %+v", it)
	}
	srv.cfg.Card.Template = filepath.Join(t.TempDir(), "missing.yaml")
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != 200 {
		t.Fatalf("print with a missing template file = %d: %s", r.Code, r.Body)
	}
}

// TestDeskNameNewestAndReopenNotice: the card shows the newest non-empty
// name; reopening a pushed request says so (not "as sent").
func TestDeskNameNewestAndReopenNotice(t *testing.T) {
	srv, st, key := newTestServer(t) // DL1ABC 2024-01-01, NAME Alice
	h := srv.Routes()
	q := &store.QSO{QSLKey: "DL1ABC|20240105|090000|40m", Call: "DL1ABC", QSODate: "20240105", TimeOn: "090000",
		Band: "40m", Mode: "CW", Name: "Alice B.", Hash: "h-newer"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/queue/yes", url.Values{"key": {q.QSLKey}})
	if b := get(t, h, "/work").Body.String(); !strings.Contains(b, `<span class="muted">Alice B.`) {
		t.Fatalf("the Desk must show the newest name:\n%s", b)
	}
	postForm(t, h, "/work/requested", url.Values{"key": {key, q.QSLKey}, "channel": {"OQRS"}})
	pend, _ := st.PendingPushBack()
	for _, p := range pend {
		if err := st.MarkPushed(p); err != nil {
			t.Fatal(err)
		}
	}
	r := postForm(t, h, "/queue/reopen", url.Values{"key": {key}})
	if tr := r.Header().Get("HX-Trigger"); !strings.Contains(tr, "as requested") || strings.Contains(tr, "as sent") {
		t.Fatalf("reopen notice for a pushed request = %q", tr)
	}
}

// TestResearchBadgesPortableIsSeparate: a /P operation is a separate card,
// so the badge must not claim one card could cover it.
func TestResearchBadgesPortableIsSeparate(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	addQueued(t, st, "DL1ABC/P", "20240103")
	b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	if !strings.Contains(b, "under other calls of this station (DL1ABC/P) - separate card(s)") || strings.Contains(b, "could cover") {
		t.Fatalf("portable badge:\n%s", b)
	}
}
