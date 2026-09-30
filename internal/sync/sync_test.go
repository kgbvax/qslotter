package sync

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/clublog"
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

// TestPushBackUploadsSentAs verifies the chosen method rides along as
// QSL_SENT_AS (with QSL_SENT=Y), not as an invalid QSL_SENT=B.
func TestPushBackUploadsSentAs(t *testing.T) {
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
	if err := st.SetQSLSentLocal(qsos[0].QSLKey, "D"); err != nil {
		t.Fatal(err)
	}
	pushed, err := o.PushBack()
	if err != nil || pushed != 1 {
		t.Fatalf("push back: pushed=%d err=%v", pushed, err)
	}
	if strings.Contains(captured, "<QSL_SENT:1>D") {
		t.Fatalf("method leaked into QSL_SENT (must be Y + QSL_SENT_AS): %q", captured)
	}
	if !strings.Contains(captured, "QSL_SENT_AS") || !strings.Contains(captured, ">D<") {
		t.Fatalf("push-back ADIF missing QSL_SENT_AS=D: %q", captured)
	}
	if !strings.Contains(captured, "<QSL_SENT:1>Y") {
		t.Fatalf("push-back ADIF missing QSL_SENT=Y: %q", captured)
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