package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/store"
)

// fakeClublog returns an httptest.Server that serves a fixed ADIF blob for
// getadif.php and accepts putlogs.php uploads.
func fakeClublog(t *testing.T, adif string) (*httptest.Server, *clublog.Client) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, adif)
	})
	mux.HandleFunc("/putlogs.php", func(w http.ResponseWriter, r *http.Request) {
		// Accept the upload; Clublog returns 200 on success.
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL = srv.URL
	cl.HTTP = srv.Client()
	return srv, cl
}

const sampleADIF = `<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<FREQ:5>14.20<RST_SENT:2>59<QSL_SENT:1>N<EOR>
<QSO_DATE:8>20240102<TIME_ON:6>130000<CALL:5>DL2CD<BAND:3>40m<MODE:2>CW<FREQ:5>7.030<RST_SENT:2>59<QSL_SENT:1>N<EOR>
<QSO_DATE:8>20240103<TIME_ON:6>140000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<FREQ:5>14.20<RST_SENT:2>58<QSL_SENT:1>N<EOR>
`

func TestPullAndUpsertAndPushBack(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, cl := fakeClublog(t, sampleADIF)
	o := &Orchestrator{Store: st, Clublog: cl}

	// First pull: 3 QSOs inserted.
	ins, upd, err := o.PullAndUpsert()
	if err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if ins != 3 || upd != 0 {
		t.Fatalf("first pull: inserted=%d updated=%d, want 3/0", ins, upd)
	}

	// Second pull: no changes.
	ins, upd, err = o.PullAndUpsert()
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if ins != 0 || upd != 0 {
		t.Fatalf("second pull: inserted=%d updated=%d, want 0/0", ins, upd)
	}

	// Recent QSOs by call: DL1AB has 2 QSOs, newest first.
	dl1ab, err := st.RecentQSOsByCall("DL1AB", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dl1ab) != 2 {
		t.Fatalf("DL1AB QSOs = %d, want 2", len(dl1ab))
	}
	if dl1ab[0].QSODate != "20240103" {
		t.Fatalf("newest DL1AB = %s, want 20240103", dl1ab[0].QSODate)
	}

	// Mark one sent locally, then push back.
	key := dl1ab[0].QSLKey
	if err := st.SetQSLSentLocal(key, "Y"); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingPushBack()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingPushBack = %d, want 1", len(pending))
	}
	pushed, err := o.PushBack()
	if err != nil {
		t.Fatalf("push back: %v", err)
	}
	if pushed != 1 {
		t.Fatalf("pushed = %d, want 1", pushed)
	}

	// After push back, pending should be 0.
	pending, err = st.PendingPushBack()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after push = %d, want 0", len(pending))
	}
}

// Test that the ADIF we push back contains the QSL_SENT=Y field. This is a
// smoke check that the round-trip ADIF is well-formed.
func TestPushBackADIFContainsQSL(t *testing.T) {
	// Use a server that captures the upload body.
	var captured string
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sampleADIF)
	})
	mux.HandleFunc("/putlogs.php", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = string(buf)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL = srv.URL
	cl.HTTP = srv.Client()

	st, _ := store.Open(":memory:")
	defer st.Close()
	o := &Orchestrator{Store: st, Clublog: cl}
	_, _, _ = o.PullAndUpsert()
	qsos, _ := st.RecentQSOsByCall("DL1AB", 10)
	_ = st.SetQSLSentLocal(qsos[0].QSLKey, "Y")
	_, _ = o.PushBack()
	if !strings.Contains(captured, "QSL_SENT") {
		t.Fatalf("push-back ADIF missing QSL_SENT: %q", captured)
	}
	if !strings.Contains(captured, "DL1AB") {
		t.Fatalf("push-back ADIF missing DL1AB: %q", captured)
	}
}

// pushCapture returns a fake Clublog client that records the last upload.
func pushCapture(t *testing.T) (*clublog.Client, *string) {
	var captured string
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sampleADIF)
	})
	mux.HandleFunc("/putlogs.php", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = string(buf)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL = srv.URL
	cl.HTTP = srv.Client()
	return cl, &captured
}

// TestPushBackUploadsSentVia verifies the send route rides along as the ADIF
// field QSL_SENT_VIA (with QSL_SENT=Y), not as an invalid QSL_SENT=D and not
// as a made-up field.
func TestPushBackUploadsSentVia(t *testing.T) {
	cl, captured := pushCapture(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	o := &Orchestrator{Store: st, Clublog: cl}
	_, _, _ = o.PullAndUpsert()
	qsos, _ := st.RecentQSOsByCall("DL1AB", 10)
	if err := st.SetQSLSentLocal(qsos[0].QSLKey, "D"); err != nil {
		t.Fatal(err)
	}
	pushed, err := o.PushBack()
	if err != nil || pushed != 1 {
		t.Fatalf("push back: pushed=%d err=%v", pushed, err)
	}
	if strings.Contains(*captured, "<QSL_SENT:1>D") {
		t.Fatalf("method leaked into QSL_SENT (must be Y + QSL_SENT_VIA): %q", *captured)
	}
	if !strings.Contains(*captured, "<QSL_SENT_VIA:1>D") || strings.Contains(*captured, "QSL_SENT_AS") {
		t.Fatalf("push-back ADIF must carry QSL_SENT_VIA=D (and no QSL_SENT_AS): %q", *captured)
	}
	if !strings.Contains(*captured, "<QSL_SENT:1>Y") {
		t.Fatalf("push-back ADIF missing QSL_SENT=Y: %q", *captured)
	}
}

// TestPushBackManagerAndWritten: a manager card pushes how it travelled
// (QSL_SENT_VIA) plus the manager (QSL_VIA; ADIF's QSL_SENT_VIA=M is
// import-only); a card written now in the Inbox pushes its route.
func TestPushBackManagerAndWritten(t *testing.T) {
	cl, captured := pushCapture(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	o := &Orchestrator{Store: st, Clublog: cl}
	_, _, _ = o.PullAndUpsert()
	qsos, _ := st.RecentQSOsByCall("DL1AB", 10)
	key := qsos[0].QSLKey
	if err := st.Enqueue(&store.QueueItem{QSLKey: key, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueuePrinted([]string{key}, store.Route{Method: "M", Via: "B", Manager: "K2ABC"}); err != nil {
		t.Fatal(err)
	}
	if n, err := o.PushBack(); err != nil || n != 1 {
		t.Fatalf("push back: %d %v", n, err)
	}
	if !strings.Contains(*captured, "<QSL_VIA:5>K2ABC") || !strings.Contains(*captured, "<QSL_SENT_VIA:1>B") ||
		!strings.Contains(*captured, "<QSL_SENT:1>Y") {
		t.Fatalf("manager card must push QSL_SENT=Y + QSL_SENT_VIA=B + QSL_VIA=K2ABC: %q", *captured)
	}

	// Written now (Inbox): QSL_SENT=Y with its route, no manager.
	other := qsos[1].QSLKey
	if err := st.Enqueue(&store.QueueItem{QSLKey: other, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueWrittenNow([]string{other}, store.Route{Method: "D"}); err != nil {
		t.Fatal(err)
	}
	if n, err := o.PushBack(); err != nil || n != 1 {
		t.Fatalf("push back: %d %v", n, err)
	}
	if !strings.Contains(*captured, "<QSL_SENT_VIA:1>D") || strings.Contains(*captured, "QSL_VIA:") ||
		!strings.Contains(*captured, "<QSL_SENT:1>Y") {
		t.Fatalf("written-now card must push QSL_SENT=Y + QSL_SENT_VIA=D only: %q", *captured)
	}
}

// TestPushBackRequested: a requested card pushes QSL_RCVD=R and no QSL_SENT.
func TestPushBackRequested(t *testing.T) {
	cl, captured := pushCapture(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	o := &Orchestrator{Store: st, Clublog: cl}
	_, _, _ = o.PullAndUpsert()
	qsos, _ := st.RecentQSOsByCall("DL1AB", 10)
	key := qsos[0].QSLKey
	if err := st.Enqueue(&store.QueueItem{QSLKey: key, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueRequested([]string{key}, store.Request{Channel: "OQRS"}); err != nil {
		t.Fatal(err)
	}
	if n, err := o.PushBack(); err != nil || n != 1 {
		t.Fatalf("push back: %d %v", n, err)
	}
	if !strings.Contains(*captured, "<QSL_RCVD:1>R") || strings.Contains(*captured, "<QSL_SENT:1>Y") || strings.Contains(*captured, "QSL_SENT_VIA") {
		t.Fatalf("requested card must push QSL_RCVD=R only: %q", *captured)
	}
}

// TestNameQTHRoundTrip verifies ADIF NAME/QTH survive the store round trip and
// reappear in the push-back record (cards and the handwriting view need them).
func TestNameQTHRoundTrip(t *testing.T) {
	adifText := "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB" +
		"<NAME:5>Alice<QTH:7>Bavaria<EOR>\n"
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec, err := adif.NewReader(strings.NewReader(adifText)).Read()
	if err != nil {
		t.Fatal(err)
	}
	q, err := ToQSOFromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "Alice" || q.QTH != "Bavaria" {
		t.Fatalf("toQSO Name=%q QTH=%q", q.Name, q.QTH)
	}
	back := fromQSO(q)
	if back.Get("NAME") != "Alice" || back.Get("QTH") != "Bavaria" {
		t.Fatalf("fromQSO NAME=%q QTH=%q", back.Get("NAME"), back.Get("QTH"))
	}
}

// TestPullClosesItemSentElsewhere: an open queue item whose QSO Clublog now
// reports as sent (the card went out through another tool) is closed instead
// of producing a duplicate card, and open windows are told.
func TestPullClosesItemSentElsewhere(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	var body string
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
	broker := events.New()
	o := &Orchestrator{Store: st, Clublog: cl, Broker: broker}

	// First pull: the QSO is not sent; it is queued and decided.
	body = "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<QSL_SENT:1>N<EOR>\n"
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL1AB", 5)
	key := qsos[0].QSLKey
	if err := st.Enqueue(&store.QueueItem{QSLKey: key, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueAccept(key); err != nil {
		t.Fatal(err)
	}

	// Second pull: Clublog now says QSL_SENT=Y.
	ch, unsub := broker.Subscribe()
	defer unsub()
	body = "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<QSL_SENT:1>Y<QSLSDATE:8>20240110<EOR>\n"
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	it, _ := st.QueueGet(key)
	if it.Status != "sent" {
		t.Fatalf("item sent elsewhere was not closed: %+v", it)
	}
	if q, _ := st.GetQSO(key); q.QSLSentLocal.Valid {
		t.Fatalf("closing must not create local sent state to push: %+v", q.QSLSentLocal)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 0 {
		t.Fatalf("nothing to push after closing: %d", len(pend))
	}
	select {
	case ev := <-ch:
		if ev.Type != "queue_changed" || !strings.Contains(ev.Data, `"to":"sent"`) {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no queue_changed event for the closed item")
	}
}

// TestPullEnqueuesAndAnnounces: pulled QSOs that qualify enter the decision
// queue (respecting the since cutoff) and open windows are told.
func TestPullEnqueuesAndAnnounces(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	body := "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<EOR>\n" +
		"<QSO_DATE:8>20240701<TIME_ON:6>120000<CALL:5>DL2CD<BAND:3>40m<MODE:2>CW<EOR>\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
	broker := events.New()
	ch, unsub := broker.Subscribe()
	defer unsub()
	o := &Orchestrator{Store: st, Clublog: cl, Broker: broker, Rules: &qualify.Rules{Since: "20240601"}}
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	queued, _ := st.QueueList("queued")
	if len(queued) != 1 || !strings.HasPrefix(queued[0].QSLKey, "DL2CD|") {
		t.Fatalf("queued = %v, want only the QSO after the cutoff", queued)
	}
	select {
	case ev := <-ch:
		if ev.Type != "queue_changed" || !strings.Contains(ev.Data, `"to":"queued"`) || !strings.Contains(ev.Data, "DL2CD") {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no queue_changed event for the enqueued QSO")
	}
}

// TestLoopSkipsWithoutCredentials: the background loop takes the live
// credentials on each tick and makes no request while there are none.
func TestLoopSkipsWithoutCredentials(t *testing.T) {
	var pulls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pulls.Add(1)
		fmt.Fprint(w, sampleADIF)
	}))
	defer srv.Close()
	st, _ := store.Open(":memory:")
	defer st.Close()

	var haveCreds atomic.Bool
	o := &Orchestrator{Store: st, Configure: func() (*clublog.Client, bool) {
		if !haveCreds.Load() {
			return nil, false
		}
		cl := clublog.New("u", "p", "DL9ET", "k")
		cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
		return cl, true
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := Loop(ctx, o, 10*time.Millisecond, 0)
	time.Sleep(60 * time.Millisecond)
	if n := pulls.Load(); n != 0 {
		t.Fatalf("pulled %d times without credentials", n)
	}
	haveCreds.Store(true) // the operator saved credentials under Settings
	deadline := time.Now().Add(2 * time.Second)
	for pulls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if pulls.Load() == 0 {
		t.Fatal("no pull after credentials appeared")
	}
}

// TestPullClosesItemWithClublogSentDate: Clublog's export has QSLSDATE but
// no QSL_SENT; a sent date closes an open card like QSL_SENT=Y does.
func TestPullClosesItemWithClublogSentDate(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	var body string
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
	o := &Orchestrator{Store: st, Clublog: cl}
	body = "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<QSL_RCVD:1>N<EOR>\n"
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL1AB", 5)
	key := qsos[0].QSLKey
	if err := st.Enqueue(&store.QueueItem{QSLKey: key, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	body = "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<QSLSDATE:8>20240110<QSL_RCVD:1>N<EOR>\n"
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	if it, _ := st.QueueGet(key); it.Status != "sent" || it.Note != "sent elsewhere" {
		t.Fatalf("a Clublog sent date must close the card: %+v", it)
	}
}

func TestDiffQSO(t *testing.T) {
	a := &store.QSO{QSLKey: "K", Call: "DL1ABC", Name: "Hans", Hash: "x"}
	b := &store.QSO{QSLKey: "K", Call: "DL1ABC", Name: "Hans-Peter", QTH: "Bonn", Hash: "y"}
	got := diffQSO(a, b)
	if got != `Name "Hans" -> "Hans-Peter", QTH "" -> "Bonn"` {
		t.Errorf("diffQSO = %s", got)
	}
	if got := diffQSO(a, a); !strings.Contains(got, "hash") {
		t.Errorf("no field differs: %s", got)
	}
}

// One QSO as two Clublog records (FT4 and MFSK, same minute): inserted once,
// stable on the next pull, the last record wins.
func TestPullDuplicateKeyIsStable(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, cl := fakeClublog(t, `<QSO_DATE:8>20250819<TIME_ON:6>204600<CALL:5>DB5HA<BAND:3>40m<MODE:4>MFSK<RST_SENT:3>-10<EOR>
<QSO_DATE:8>20250819<TIME_ON:6>204600<CALL:5>DB5HA<BAND:3>40m<MODE:3>FT4<RST_SENT:3>599<EOR>
`)
	o := &Orchestrator{Store: st, Clublog: cl}
	for i, want := range [][2]int{{1, 0}, {0, 0}, {0, 0}} {
		ins, upd, err := o.PullAndUpsert()
		if err != nil {
			t.Fatal(err)
		}
		if ins != want[0] || upd != want[1] {
			t.Fatalf("pull %d: inserted=%d updated=%d, want %v", i+1, ins, upd, want)
		}
	}
	q, _ := st.GetQSO("DB5HA|20250819|204600|40M")
	if q == nil || q.Mode != "FT4" {
		t.Errorf("stored %+v, want the last record (FT4)", q)
	}
}
