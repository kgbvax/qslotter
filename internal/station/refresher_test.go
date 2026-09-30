package station

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/store"
)

// parseQRZQuery parses a QRZ-style query string using ";" as the separator
// (the QRZ XML API uses semicolons, not ampersands, between params).
func parseQRZQuery(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitSemi(raw) {
		if eq := indexByte(pair, '='); eq >= 0 {
			out[pair[:eq]] = pair[eq+1:]
		}
	}
	return out
}

func splitSemi(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// fakeQRZ returns an httptest.Server serving QRZ XML responses and a qrz.Client
// pointed at it.
func fakeQRZ(t *testing.T) (*httptest.Server, *qrz.Client) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := parseQRZQuery(r.URL.RawQuery)
		// Login request (no "s=" session key).
		if q["s"] == "" {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<QRZDatabase version="1.34">
  <Session><Key>fake-session-key</Key><Count>0</Count><SubExp>subexp</SubExp></Session>
</QRZDatabase>`))
			return
		}
		// Lookup by callsign.
		call := q["callsign"]
		if call != "" {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<QRZDatabase version="1.34">
  <Session><Key>fake-session-key</Key><Count>1</Count><SubExp>subexp</SubExp></Session>
  <Callsign>
    <call>` + call + `</call>
    <fname>Fred</fname>
    <name>Flintstone</name>
    <addr1>123 Quarry Lane</addr1>
    <addr2>Bedrock</addr2>
    <state>BB</state>
    <zip>12345</zip>
    <country>USA</country>
    <dxcc>291</dxcc>
    <email>fred@example.com</email>
    <qslmgr>K2ABC</qslmgr>
    <eqsl>Y</eqsl>
    <mqsl>Y</mqsl>
    <lotw>N</lotw>
  </Callsign>
</QRZDatabase>`))
			return
		}
		// Bio fetch by html=.
		if q["html"] != "" {
			_, _ = w.Write([]byte(`<html><body>QSL via bureau only. No paper QSL for FT8.</body></html>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()
	return srv, cl
}

func TestRefresherLookupAndCache(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, q := fakeQRZ(t)
	broker := events.New()
	ref := New(st, q, broker, time.Hour)

	// Initial lookup: cache miss -> fetches from QRZ.
	si, err := ref.Get(t.Context(), "DL1AB")
	if err != nil {
		t.Fatal(err)
	}
	if si == nil {
		t.Fatal("expected station info, got nil")
	}
	if si.QSLMgr != "K2ABC" {
		t.Fatalf("QSLMgr = %q, want K2ABC", si.QSLMgr)
	}
	// The QRZ qslmgr field should drive the determination to M (manager).
	if si.QSLMethod != "M" {
		t.Fatalf("QSLMethod = %q, want M", si.QSLMethod)
	}
	if si.QSLRoute != "K2ABC" {
		t.Fatalf("QSLRoute = %q, want K2ABC", si.QSLRoute)
	}
	if si.RefusePaper {
		t.Fatal("RefusePaper should be false for qslmgr present")
	}

	// Second lookup: cache hit (within TTL) -> no new HTTP request expected.
	// We verify by checking the fetched_at timestamp is unchanged.
	firstFetched := si.FetchedAt
	si2, err := ref.Get(t.Context(), "DL1AB")
	if err != nil {
		t.Fatal(err)
	}
	if si2.FetchedAt != firstFetched {
		t.Fatalf("second Get fetched_at = %q, want %q (cache hit)", si2.FetchedAt, firstFetched)
	}
}

func TestRefresherDeterminationFromBio(t *testing.T) {
	// Use a QRZ response with no qslmgr but a bio containing "QSL via bureau".
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := parseQRZQuery(r.URL.RawQuery)
		if q["s"] == "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase><Session><Key>k</Key></Session></QRZDatabase>`))
			return
		}
		if q["callsign"] != "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase>
<Session><Key>k</Key><Count>1</Count></Session>
<Callsign><call>DL2CD</call><mqsl>Y</mqsl><eqsl>N</eqsl><lotw>N</lotw></Callsign>
</QRZDatabase>`))
			return
		}
		if q["html"] != "" {
			_, _ = w.Write([]byte(`<html><body>QSL via bureau please. 73!</body></html>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()

	ref := New(st, cl, nil, time.Hour)
	si, err := ref.Get(t.Context(), "DL2CD")
	if err != nil {
		t.Fatal(err)
	}
	if si == nil {
		t.Fatal("nil station info")
	}
	// Bio says "QSL via bureau" -> Method B.
	if si.QSLMethod != "B" {
		t.Fatalf("QSLMethod = %q, want B (from bio)", si.QSLMethod)
	}
	if si.RefusePaper {
		t.Fatal("RefusePaper should be false for bureau QSL")
	}
}

func TestRefresherNoPaper(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := parseQRZQuery(r.URL.RawQuery)
		if q["s"] == "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase><Session><Key>k</Key></Session></QRZDatabase>`))
			return
		}
		if q["callsign"] != "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase>
<Session><Key>k</Key><Count>1</Count></Session>
<Callsign><call>DL3NO</call><mqsl>N</mqsl><eqsl>Y</eqsl><lotw>Y</lotw></Callsign>
</QRZDatabase>`))
			return
		}
		if q["html"] != "" {
			_, _ = w.Write([]byte(`<html><body>NO PAPER QSL PLEASE. eQSL only.</body></html>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()

	ref := New(st, cl, nil, time.Hour)
	si, err := ref.Get(t.Context(), "DL3NO")
	if err != nil {
		t.Fatal(err)
	}
	if si == nil {
		t.Fatal("nil")
	}
	if !si.RefusePaper {
		t.Fatal("RefusePaper should be true for 'NO PAPER QSL'")
	}
}

func TestRefresherDedupConcurrent(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()

	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := parseQRZQuery(r.URL.RawQuery)
		if q["s"] == "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase><Session><Key>k</Key></Session></QRZDatabase>`))
			return
		}
		if q["callsign"] != "" {
			callCount++
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase>
<Session><Key>k</Key><Count>` + itoa(callCount) + `</Count></Session>
<Callsign><call>DL4DUP</call><qslmgr>K1XX</qslmgr></Callsign>
</QRZDatabase>`))
			return
		}
		if q["html"] != "" {
			_, _ = w.Write([]byte(`<html><body>QSL via manager</body></html>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()

	ref := New(st, cl, nil, time.Hour)
	// Fire 5 concurrent Get calls; the inFlight dedup should mean only 1-2
	// QRZ lookups happen (the first triggers the fetch, the rest wait or
	// return the cache after the first completes).
	done := make(chan *store.StationInfo, 5)
	for i := 0; i < 5; i++ {
		go func() {
			si, _ := ref.Get(t.Context(), "DL4DUP")
			done <- si
		}()
	}
	for i := 0; i < 5; i++ {
		si := <-done
		if si == nil {
			t.Fatal("nil result from Get")
		}
	}
	// We allow at most 2 lookups (one for the first Get, possibly one for a
	// concurrent one that raced the inFlight lock). 5 would mean no dedup.
	if callCount > 2 {
		t.Fatalf("expected at most 2 QRZ lookups due to dedup, got %d", callCount)
	}
}

func TestRefresherNilQRZIsNoOp(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	ref := New(st, nil, nil, time.Hour) // no QRZ client
	si, err := ref.Get(t.Context(), "DL5NIL")
	if err != nil {
		t.Fatalf("Get with nil QRZ should not error: %v", err)
	}
	if si != nil {
		t.Fatalf("Get with nil QRZ should return nil, got %+v", si)
	}
}

func TestRefresherStaleTriggersRefetch(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()

	lookups := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := parseQRZQuery(r.URL.RawQuery)
		if q["s"] == "" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase><Session><Key>k</Key></Session></QRZDatabase>`))
			return
		}
		if q["callsign"] != "" {
			lookups++
			_, _ = w.Write([]byte(`<?xml version="1.0"?><QRZDatabase>
<Session><Key>k</Key><Count>` + itoa(lookups) + `</Count></Session>
<Callsign><call>DL6ST</call><qslmgr>K2YY</qslmgr></Callsign>
</QRZDatabase>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body>bio</body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl := qrz.New("u", "p", "qslotter/test")
	cl.BaseURL = srv.URL + "/"
	cl.HTTP = srv.Client()

	// TTL of 10ms so the cache goes stale quickly.
	ref := New(st, cl, nil, 10*time.Millisecond)
	_, _ = ref.Get(t.Context(), "DL6ST")
	if lookups != 1 {
		t.Fatalf("after first Get: lookups = %d, want 1", lookups)
	}
	time.Sleep(30 * time.Millisecond)
	_, _ = ref.Get(t.Context(), "DL6ST")
	if lookups != 2 {
		t.Fatalf("after stale Get: lookups = %d, want 2", lookups)
	}
}

func TestRefresherPublishesEvent(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	_, q := fakeQRZ(t)
	broker := events.New()
	ch, unsub := broker.Subscribe()
	defer unsub()
	ref := New(st, q, broker, time.Hour)
	_, _ = ref.Get(t.Context(), "DL1AB")
	select {
	case ev := <-ch:
		if ev.Type != "station_updated" || ev.Data != "DL1AB" {
			t.Fatalf("event = %+v, want {station_updated DL1AB}", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for station_updated event")
	}
}

// itoa is a tiny strconv-free helper for test fixtures.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}