package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/store"
)

// addQSO stores a QSO without a queue item (e.g. a digital-mode contact).
func addQSO(t *testing.T, st store.Store, call, date, band string) string {
	t.Helper()
	q := &store.QSO{QSLKey: call + "|" + date + "|100000|" + band, Call: call, QSODate: date, TimeOn: "100000",
		Band: band, Mode: "SSB", RSTSent: "59", Hash: "h-" + call + date + band}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	return q.QSLKey
}

// TestReceiveLookupPortableAndYourCard: the lookup finds the station's
// portable QSOs too (C1) and says where your card stands for each (C2).
func TestReceiveLookupPortableAndYourCard(t *testing.T) {
	srv, st, key := newTestServer(t) // DL1ABC 2024-01-01, in the Inbox
	h := srv.Routes()
	p := addQSO(t, st, "EA8/DL1ABC", "20240201", "15m")
	if r := postForm(t, h, "/receive/lookup", url.Values{"call": {"dl1abc"}}); r.Code != 200 ||
		!strings.Contains(r.Body.String(), "EA8/DL1ABC") || !strings.Contains(r.Body.String(), "in New QSOs") ||
		!strings.Contains(r.Body.String(), `value="`+p+`"`) || !strings.Contains(r.Body.String(), `value="`+key+`"`) {
		t.Fatalf("lookup:\n%s", r.Body)
	}
	if b := get(t, h, "/receive?call=EA8/DL1ABC").Body.String(); !strings.Contains(b, "DL1ABC") || !strings.Contains(b, `id="book"`) {
		t.Fatalf("/receive?call= must show the lookup:\n%s", b)
	}
}

// TestReceiveBookAndReply: booking records the card and asks the reply
// question per QSO; a due reply goes out written now, printed, or later via
// the Desk (C2, C3).
func TestReceiveBookAndReply(t *testing.T) {
	srv, st, inbox := newTestServer(t) // DL1ABC 2024-01-01 in the Inbox
	h := srv.Routes()
	never := addQSO(t, st, "DL1ABC", "20240301", "40m") // never queued: reply due
	sent := addQSO(t, st, "DL1ABC", "20240401", "17m")
	if err := st.SetQSLSentLocal(sent, "B"); err != nil {
		t.Fatal(err)
	}

	r := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {inbox, never, sent}})
	body := r.Body.String()
	if r.Code != 200 || strings.Count(body, `<b class="err">your card due</b>`) != 2 || !strings.Contains(body, "your card went out") || !strings.Contains(body, `id="reply-0"`) {
		t.Fatalf("book:\n%s", body)
	}
	for _, k := range []string{inbox, never, sent} {
		if q, _ := st.GetQSO(k); q.QSLRcvdLocal.String != "Y" {
			t.Fatalf("%s not booked received", k)
		}
	}
	if strings.Contains(body, `name="key" value="`+sent+`"`) {
		t.Fatal("a QSO whose card went out must not be part of the reply")
	}
	// Reply written now via the bureau: both due QSOs on one card, done.
	if r := postForm(t, h, "/receive/reply?how=written", url.Values{"key": {inbox, never}}); r.Code != http.StatusBadRequest {
		t.Fatalf("written reply without a route = %d, want 400", r.Code)
	}
	r = postForm(t, h, "/receive/reply?how=written", url.Values{"key": {inbox, never}, "route": {"B"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Your card to DL1ABC written (Bureau)") {
		t.Fatalf("reply written:\n%s", r.Body)
	}
	for _, k := range []string{inbox, never} {
		if it := status(t, st, k); it.Status != "sent" || it.DesiredMethod != "B" {
			t.Fatalf("%s after the reply: %+v", k, it)
		}
	}
	// Booking again: nothing due any more.
	if b := postForm(t, h, "/receive/book", url.Values{"key": {never}}).Body.String(); strings.Contains(b, "your card due") || !strings.Contains(b, "Nothing to send back") {
		t.Fatalf("second booking:\n%s", b)
	}
}

func TestReceiveReplyLaterAndPrint(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	if err := st.QueueDecline(key); err != nil { // "no card" - their card overrules it
		t.Fatal(err)
	}
	other := addQSO(t, st, "DL2ZZZ", "20240105", "20m")
	if r := postForm(t, h, "/receive/reply?how=later", url.Values{"key": {key}}); r.Code != 200 || !strings.Contains(r.Body.String(), "at the Desk") {
		t.Fatalf("later = %d %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" || it.DesiredMethod != "" {
		t.Fatalf("a later reply goes to the Desk: %+v", it)
	}
	fp := srv.printer.(*fakePrinter)
	if r := postForm(t, h, "/receive/reply?how=print", url.Values{"key": {other}, "route": {"D"}}); r.Code != 200 || len(fp.printed) != 1 {
		t.Fatalf("print reply = %d, printed %d: %s", r.Code, len(fp.printed), r.Body)
	}
	if it := status(t, st, other); it.Status != "sent" || !it.PrintedAt.Valid || it.OverrideReason != "reply to their card" {
		t.Fatalf("printed reply: %+v", it)
	}
}

// TestReceiveExpectedAndOverdue: requested cards are listed as expected,
// overdue after the configured weeks; booking one says no reply is needed and
// takes it off the list (C5).
func TestReceiveExpectedAndOverdue(t *testing.T) {
	srv, st, key := newTestServer(t)
	srv.cfg.Receive.OverdueWeeks = 12
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/work/requested", url.Values{"key": {key}, "channel": {"OQRS"}, "note": {"2 USD"}})

	page := get(t, h, "/receive").Body.String()
	if !strings.Contains(page, "Expected cards") || !strings.Contains(page, "DL1ABC") || strings.Contains(page, "chip warn\">overdue") {
		t.Fatalf("expected list (fresh request):\n%s", page)
	}
	srv.now = func() time.Time { return time.Now().Add(13 * 7 * 24 * time.Hour) }
	if page = get(t, h, "/receive").Body.String(); !strings.Contains(page, `class="chip warn">overdue`) {
		t.Fatalf("a request older than 12 weeks must be marked overdue:\n%s", page)
	}
	if l := postForm(t, h, "/receive/lookup", url.Values{"call": {"DL1ABC"}}).Body.String(); !strings.Contains(l, "requested via OQRS") || !strings.Contains(l, "overdue") || !strings.Contains(l, `value="`+key+`" checked`) {
		t.Fatalf("lookup of an expected card:\n%s", l)
	}
	b := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {key}}).Body.String()
	if !strings.Contains(b, "nothing to send back") || strings.Contains(b, `class="reply"`) {
		t.Fatalf("booking a requested card:\n%s", b)
	}
	if page = get(t, h, "/receive").Body.String(); strings.Contains(page, "Expected cards") {
		t.Fatalf("an arrived card is no longer expected:\n%s", page)
	}
	if q, _ := st.GetQSO(key); q.QSLRcvdLocal.String != "Y" {
		t.Fatalf("their card must be booked Y over the request: %v", q.QSLRcvdLocal)
	}
}

// TestReceiveNeverAnotherStation: a prefix as long as the call must not turn
// the lookup into "everyone from that prefix", nor pre-tick their QSO.
func TestReceiveNeverAnotherStation(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	other := addQSO(t, st, "VP2V/W1XY", "20240201", "15m")
	home := addQSO(t, st, "W1AW", "20240301", "20m")
	b := postForm(t, h, "/receive/lookup", url.Values{"call": {"VP2V/W1AW"}}).Body.String()
	if strings.Contains(b, other) || !strings.Contains(b, `value="`+home+`" checked`) {
		t.Fatalf("lookup for VP2V/W1AW must list W1AW (ticked) and not VP2V/W1XY:\n%s", b)
	}
}

// TestReceiveReplyCardsPerCallWithDeskQSOs: a reply answers one worked call
// per card and takes along that call's QSOs already at the Desk (no second
// card); a /P call gets its own panel; mixed calls are refused for one card.
func TestReceiveReplyCardsPerCallWithDeskQSOs(t *testing.T) {
	srv, st, atDesk := newTestServer(t) // DL1ABC 2024-01-01
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {atDesk}})
	never := addQSO(t, st, "DL1ABC", "20240301", "40m")
	port := addQSO(t, st, "EA8/DL1ABC", "20240401", "15m")
	b := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {never, port}}).Body.String()
	if !strings.Contains(b, `id="reply-0"`) || !strings.Contains(b, `id="reply-1"`) {
		t.Fatalf("home call and /P need separate reply panels:\n%s", b)
	}
	home := b[strings.Index(b, `data-call="DL1ABC"`):]
	home = home[:strings.Index(home, "reply-research")]
	if !strings.Contains(home, `value="`+atDesk+`"`) || !strings.Contains(home, `value="`+never+`"`) || strings.Contains(home, port) {
		t.Fatalf("the DL1ABC reply card must hold the booked QSO and the Desk QSO, not the /P one:\n%s", home)
	}
	if r := postForm(t, h, "/receive/reply?how=print", url.Values{"key": {never, port}, "route": {"B"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("one card for two calls = %d, want 400", r.Code)
	}
	if r := postForm(t, h, "/receive/reply?how=written", url.Values{"key": {atDesk, never}, "route": {"D"}}); r.Code != 200 {
		t.Fatalf("reply = %d: %s", r.Code, r.Body)
	}
	for _, k := range []string{atDesk, never} {
		if it := status(t, st, k); it.Status != "sent" {
			t.Fatalf("%s must go out on the reply card: %+v", k, it)
		}
	}
}

// TestReceiveExpectedPerRequest: two requests to one station are two rows;
// the older, overdue one is not hidden behind the newer one.
func TestReceiveExpectedPerRequest(t *testing.T) {
	srv, st, k1 := newTestServer(t)
	srv.cfg.Receive.OverdueWeeks = 12
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {k1}})
	postForm(t, h, "/work/requested", url.Values{"key": {k1}, "channel": {"OQRS"}, "note": {"first"}})
	srv.now = func() time.Time { return time.Now().Add(20 * 7 * 24 * time.Hour) }
	k2 := addQueued(t, st, "DL1ABC", "20240601")
	postForm(t, h, "/queue/yes", url.Values{"key": {k2}})
	postForm(t, h, "/work/requested", url.Values{"key": {k2}, "channel": {"PayPal"}, "note": {"second"}})
	page := get(t, h, "/receive").Body.String()
	exp := page[strings.Index(page, `id="expected"`):]
	if strings.Count(exp, `<a class="call"`) != 2 || !strings.Contains(exp, "first") || !strings.Contains(exp, "second") || !strings.Contains(exp, "overdue</span>") {
		t.Fatalf("expected list per request:\n%s", exp)
	}
}

// TestReceiveExpectedCardAnswersAll: a card answering your request needs no
// reply for any QSO it confirms; the Expected list is refreshed out of band.
func TestReceiveExpectedCardAnswersAll(t *testing.T) {
	srv, st, req := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {req}})
	postForm(t, h, "/work/requested", url.Values{"key": {req}, "channel": {"OQRS"}})
	ft8 := addQSO(t, st, "DL1ABC", "20240301", "30m")
	b := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {req, ft8}}).Body.String()
	if strings.Contains(b, `class="reply"`) || !strings.Contains(b, "answers your request") {
		t.Fatalf("an expected card asks for no reply:\n%s", b)
	}
	if !strings.Contains(b, `id="expected" hx-swap-oob="true"`) {
		t.Fatalf("the Expected list must be refreshed with the booking:\n%s", b)
	}
}

// TestReceiveSentInQueueCounts: a card the queue has as sent counts as sent
// even when Clublog's state flipped back after the push.
func TestReceiveSentInQueueCounts(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/work/written", url.Values{"key": {key}, "route": {"MB"}, "manager": {"K2ABC"}})
	pend, _ := st.PendingPushBack()
	for _, p := range pend {
		if err := st.MarkPushed(p); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := st.GetQSO(key)
	q.QSLSent, q.Hash = "N", "h-flipped"
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	b := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {key}}).Body.String()
	if strings.Contains(b, `class="reply"`) || !strings.Contains(b, "your card went out") || !strings.Contains(b, "manager K2ABC") {
		t.Fatalf("queue status sent must count as sent (with the manager):\n%s", b)
	}
}

// TestReceiveReplyRecoverableAndPrintFailure: a booked QSO still waiting
// for your reply can be answered later from the lookup; a failed print says
// the reply waits at the Desk (in place of the panel).
func TestReceiveReplyRecoverableAndPrintFailure(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {key}})
	l := postForm(t, h, "/receive/lookup", url.Values{"call": {"DL1ABC"}}).Body.String()
	if !strings.Contains(l, `class="err">your card due</b>`) || !strings.Contains(l, `name="key" value="`+key+`"`) {
		t.Fatalf("a booked QSO with an open reply must stay answerable:\n%s", l)
	}
	if b := postForm(t, h, "/receive/book", url.Values{"call": {"DL1ABC"}, "key": {key}}).Body.String(); !strings.Contains(b, `id="reply-0"`) {
		t.Fatalf("ticking it again opens the reply:\n%s", b)
	}
	if r := get(t, h, "/receive/research?key="+url.QueryEscape(key)); r.Code != 200 || !strings.Contains(r.Body.String(), "QRZ:") {
		t.Fatalf("/receive/research = %d", r.Code)
	}
	srv.printer = &fakePrinter{err: errPrinterGone}
	r := postForm(t, h, "/receive/reply?how=print", url.Values{"key": {key}, "route": {"B"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "waits at the Desk") {
		t.Fatalf("failed print = %d: %s", r.Code, r.Body)
	}
	if it := status(t, st, key); it.Status != "decided" {
		t.Fatalf("after a failed print the reply waits at the Desk: %+v", it)
	}
}

var errPrinterGone = errorString("lp: printer gone")

type errorString string

func (e errorString) Error() string { return string(e) }
