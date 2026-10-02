package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	gosync "sync"
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

// recorder is a fake Clublog whose answers a test can change; it counts the
// requests per endpoint and keeps the last upload. Safe for the loop
// goroutine.
type recorder struct {
	mu           gosync.Mutex
	pullStatus   int
	pushStatus   int
	pulls, pushs int
	upload       string
}

func (f *recorder) client(t *testing.T) *clublog.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/getadif.php":
			f.pulls++
			if f.pullStatus != 0 {
				w.WriteHeader(f.pullStatus)
				return
			}
			fmt.Fprint(w, sampleADIF)
		case "/putlogs.php":
			f.pushs++
			if f.pushStatus != 0 {
				w.WriteHeader(f.pushStatus)
				return
			}
			if file, _, err := r.FormFile("file"); err == nil {
				b, _ := io.ReadAll(file)
				f.upload = string(b)
			}
		}
	}))
	t.Cleanup(srv.Close)
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
	return cl
}

func (f *recorder) counts() (pulls, pushs int, upload string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls, f.pushs, f.upload
}

// TestLoopPushes: the push ticker uploads what is pending; a failed upload
// is logged and the next tick tries again; no pull runs while its interval
// is off.
func TestLoopPushes(t *testing.T) {
	// A file: the loop goroutine and the test may hold two connections, and
	// each connection to ":memory:" is a database of its own.
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &recorder{}
	cl := f.client(t)
	if _, _, err := (&Orchestrator{Store: st, Clublog: cl}).PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL2CD", 1)
	if err := st.SetQSLSentLocal(qsos[0].QSLKey, "B"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.pulls, f.pushStatus = 0, http.StatusServiceUnavailable
	f.mu.Unlock()

	o := &Orchestrator{Store: st, Configure: func() (*clublog.Client, bool) { return cl, true }}
	ctx, cancel := context.WithCancel(context.Background())
	done := Loop(ctx, o, 0, 10*time.Millisecond)
	defer func() { cancel(); <-done }()

	waitFor(t, "a failed push", func() bool { _, n, _ := f.counts(); return n >= 1 })
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Fatalf("a failed upload must stay pending: %d", len(pend))
	}
	f.mu.Lock()
	f.pushStatus = 0
	f.mu.Unlock()
	waitFor(t, "the retried push", func() bool { pend, _ := st.PendingPushBack(); return len(pend) == 0 })
	pulls, _, upload := f.counts()
	if !strings.Contains(upload, "<CALL:5>DL2CD") || !strings.Contains(upload, "<QSL_SENT_VIA:1>B") {
		t.Errorf("upload = %q", upload)
	}
	if pulls != 0 {
		t.Errorf("pull interval off, but %d pulls", pulls)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // generous: a loaded machine
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPushBackFailureKeepsPending: when Clublog refuses the upload, nothing
// is marked pushed, so the next push sends it again.
func TestPushBackFailureKeepsPending(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := &recorder{}
	o := &Orchestrator{Store: st, Clublog: f.client(t)}
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL2CD", 1)
	_ = st.SetQSLSentLocal(qsos[0].QSLKey, "D")
	f.pushStatus = http.StatusForbidden
	if n, err := o.PushBack(); err == nil || n != 0 {
		t.Fatalf("PushBack = %d, %v; want 0 and the 403", n, err)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Fatalf("pending after a refused upload = %d, want 1", len(pend))
	}
	if last, _ := st.MetaGet("clublog_last_push_at"); last != "" {
		t.Errorf("a refused upload is no push: last push %q", last)
	}
}

// TestPullFailureStoresNothing: a refused pull leaves the log and the
// last-pull time alone.
func TestPullFailureStoresNothing(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := &recorder{pullStatus: http.StatusForbidden}
	o := &Orchestrator{Store: st, Clublog: f.client(t)}
	if _, _, err := o.PullAndUpsert(); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the 403", err)
	}
	if all, _ := st.AllQSOs(); len(all) != 0 {
		t.Errorf("stored %d QSOs from a refused pull", len(all))
	}
	if last, _ := st.MetaGet("clublog_last_pull_at"); last != "" {
		t.Errorf("a refused pull is no pull: last pull %q", last)
	}
}

// TestPushBackSendsReceivedDate: a card booked as received pushes
// QSL_RCVD=Y with the day it arrived.
func TestPushBackSendsReceivedDate(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := &recorder{}
	o := &Orchestrator{Store: st, Clublog: f.client(t)}
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL2CD", 1)
	if err := st.SetQSLRcvdLocal(qsos[0].QSLKey); err != nil {
		t.Fatal(err)
	}
	if n, err := o.PushBack(); err != nil || n != 1 {
		t.Fatalf("PushBack = %d, %v", n, err)
	}
	today := time.Now().UTC().Format("20060102")
	if _, _, up := f.counts(); !strings.Contains(up, "<QSL_RCVD:1>Y") || !strings.Contains(up, "<QSLRDATE:8>"+today) {
		t.Errorf("received card must push QSL_RCVD=Y + QSLRDATE=%s: %q", today, up)
	}
}

// TestPullReportsNewQSOs: OnNewQSO hears about each QSO a pull adds (a card
// written during it gets booked), never about known or updated ones.
func TestPullReportsNewQSOs(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	var body string
	mux := http.NewServeMux()
	mux.HandleFunc("/getadif.php", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := clublog.New("u", "p", "DL9ET", "k")
	cl.BaseURL, cl.HTTP = srv.URL, srv.Client()
	var seen []string
	o := &Orchestrator{Store: st, Clublog: cl, OnNewQSO: func(q *store.QSO) { seen = append(seen, q.Call) }}

	body = sampleADIF
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "DL1AB,DL2CD,DL1AB" {
		t.Fatalf("first pull reported %v", seen)
	}
	seen = nil
	body = strings.Replace(sampleADIF, "<RST_SENT:2>58", "<RST_SENT:2>57", 1) +
		"<QSO_DATE:8>20240104<TIME_ON:6>150000<CALL:5>DL3EF<BAND:3>20m<MODE:2>CW<EOR>\n"
	ins, upd, err := o.PullAndUpsert()
	if err != nil {
		t.Fatal(err)
	}
	if ins != 1 || upd != 1 || strings.Join(seen, ",") != "DL3EF" {
		t.Fatalf("second pull: inserted=%d updated=%d reported %v; want 1/1 [DL3EF]", ins, upd, seen)
	}
}

// TestNoteLogin: a 403 pauses these credentials (and only these), other
// failures change nothing, a success ends the pause. The secrets are not
// stored.
func TestNoteLogin(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	c := clublog.New("e", "secret", "DL9ET", "k")
	NoteLogin(st, c, nil) // nothing to clear
	if at := RefusedAt(st, c); at != "" {
		t.Fatalf("refused without a 403: %q", at)
	}
	NoteLogin(st, c, fmt.Errorf("clublog pull: %w", clublog.ErrForbidden))
	if RefusedAt(st, c) == "" {
		t.Fatal("a 403 must pause these credentials")
	}
	if fp, _ := st.MetaGet(metaRefusedCreds); strings.Contains(fp, "secret") || len(fp) != 64 {
		t.Errorf("stored %q: a fingerprint, not the secret", fp)
	}
	if at := RefusedAt(st, clublog.New("e", "new secret", "DL9ET", "k")); at != "" {
		t.Error("changed credentials are not paused")
	}
	if RefusedAt(st, nil) != "" {
		t.Error("no client, no pause")
	}
	NoteLogin(st, c, fmt.Errorf("clublog pull: HTTP 503"))
	if RefusedAt(st, c) == "" {
		t.Error("an outage says nothing about the credentials: still paused")
	}
	NoteLogin(st, c, nil)
	if at := RefusedAt(st, c); at != "" {
		t.Errorf("a login that got through ends the pause: %q", at)
	}
}

// TestRefusalByPullAndPush: a 403 on either request pauses, a pull by hand
// that gets through ends the pause.
func TestRefusalByPullAndPush(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := &recorder{}
	o := &Orchestrator{Store: st, Clublog: f.client(t)}
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL2CD", 1)
	_ = st.SetQSLSentLocal(qsos[0].QSLKey, "B")
	f.pushStatus = http.StatusForbidden
	if _, err := o.PushBack(); !errors.Is(err, clublog.ErrForbidden) {
		t.Fatalf("push: %v", err)
	}
	if RefusedAt(st, o.Clublog) == "" {
		t.Fatal("a refused push must pause")
	}
	if _, _, err := o.PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	if at := RefusedAt(st, o.Clublog); at != "" {
		t.Fatalf("a pull that got through must end the pause: %q", at)
	}
	f.pullStatus = http.StatusForbidden
	_, _, _ = o.PullAndUpsert()
	if RefusedAt(st, o.Clublog) == "" {
		t.Fatal("a refused pull must pause")
	}
}

// TestLoopPausesAfter403: after a 403 the loop stops logging in - pull and
// push - until the credentials change.
func TestLoopPausesAfter403(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db")) // see TestLoopPushes
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &recorder{}
	base := f.client(t)
	if _, _, err := (&Orchestrator{Store: st, Clublog: base}).PullAndUpsert(); err != nil {
		t.Fatal(err)
	}
	qsos, _ := st.RecentQSOsByCall("DL2CD", 1)
	_ = st.SetQSLSentLocal(qsos[0].QSLKey, "B")
	f.mu.Lock()
	f.pulls, f.pullStatus, f.pushStatus = 0, http.StatusForbidden, http.StatusForbidden
	f.mu.Unlock()

	var password atomic.Value
	password.Store("p")
	o := &Orchestrator{Store: st, Configure: func() (*clublog.Client, bool) {
		cl := *base
		cl.AppPassword = password.Load().(string)
		return &cl, true
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := Loop(ctx, o, 10*time.Millisecond, 10*time.Millisecond)
	defer func() { cancel(); <-done }()

	waitFor(t, "the refused login", func() bool { p, n, _ := f.counts(); return p+n >= 1 })
	time.Sleep(150 * time.Millisecond) // ~15 ticks of each
	if p, n, _ := f.counts(); p+n != 1 {
		t.Fatalf("logins after the 403: %d pulls, %d pushes; want the one refused", p, n)
	}

	f.mu.Lock()
	f.pullStatus, f.pushStatus = 0, 0
	f.mu.Unlock()
	password.Store("fixed under Settings")
	waitFor(t, "sync with the new credentials", func() bool { pend, _ := st.PendingPushBack(); return len(pend) == 0 })
	if p, _, _ := f.counts(); p < 1 {
		t.Error("the pull must resume too")
	}
}
