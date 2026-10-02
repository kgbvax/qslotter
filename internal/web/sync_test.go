package web

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
)

// fakeClublog stands in for clublog.org behind the Pull/Push buttons.
type fakeClublog struct {
	mu     gosync.Mutex
	adif   string // getadif.php answer
	status int    // non-zero: every request fails with it
	email  string // the e-mail of the last request
	upload string // the last putlogs.php file
}

func (f *fakeClublog) install(t *testing.T, srv *Server) {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		f.email = r.FormValue("email")
		switch r.URL.Path {
		case "/getadif.php":
			io.WriteString(w, f.adif)
		case "/putlogs.php":
			if file, _, err := r.FormFile("file"); err == nil {
				b, _ := io.ReadAll(file)
				f.upload = string(b)
			}
		}
	}))
	t.Cleanup(hs.Close)
	srv.clublogFn = func(c config.ClublogCfg) *clublog.Client {
		cl := clublog.New(c.Email, c.AppPassword, c.Call, c.APIKey)
		cl.BaseURL, cl.HTTP = hs.URL, hs.Client()
		return cl
	}
	srv.cfg.Clublog.Email, srv.cfg.Clublog.AppPassword, srv.cfg.Clublog.APIKey = "dl9et@example.org", "pw", "key"
}

// TestSyncButtonsNeedCredentials: without Clublog credentials the buttons
// answer at once and nothing is sent (a failed login counts towards a ban).
func TestSyncButtonsNeedCredentials(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.clublogFn = func(config.ClublogCfg) *clublog.Client {
		t.Error("Clublog contacted without credentials")
		return clublog.New("", "", "", "")
	}
	srv.cfg.Clublog.Email, srv.cfg.Clublog.APIKey = "dl9et@example.org", "key" // no app password
	h := srv.Routes()
	for _, p := range []string{"/sync/pull", "/sync/push"} {
		rec := postForm(t, h, p, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Clublog not configured") {
			t.Errorf("%s = %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

// TestSyncPullButton: the pull stores the log with the live credentials,
// announces the new QSOs and ends the QSO in progress they log.
func TestSyncPullButton(t *testing.T) {
	srv, st, _ := newTestServer(t)
	f := &fakeClublog{}
	f.install(t, srv)
	now := time.Now().UTC()
	f.adif = fmt.Sprintf("<QSO_DATE:8>%s<TIME_ON:6>%s<CALL:6>DL7XYZ<BAND:3>20m<MODE:3>SSB<EOR>\n",
		now.Format("20060102"), now.Format("150405"))
	srv.Contacts = contact.NewTracker(st, nil, nil)
	srv.Contacts.Set(contact.Contact{Call: "DL7XYZ"})
	srv.rules.Since = "" // no cutoff
	ch, unsub := srv.broker.Subscribe()
	defer unsub()

	rec := postForm(t, srv.Routes(), "/sync/pull", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "last pull:") {
		t.Fatalf("pull = %d %q", rec.Code, rec.Body.String())
	}
	if f.email != "dl9et@example.org" {
		t.Errorf("pulled as %q, want the configured e-mail", f.email)
	}
	key := "DL7XYZ|" + now.Format("20060102|150405") + "|20M"
	if q, _ := st.GetQSO(key); q == nil {
		t.Fatalf("pulled QSO %s not stored", key)
	}
	if it := status(t, st, key); it.Status != "queued" {
		t.Errorf("pulled QSO not in the Inbox: %+v", it)
	}
	select {
	case ev := <-ch:
		if ev.Type != "queue_changed" || !strings.Contains(ev.Data, "DL7XYZ") {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("no queue_changed for the pulled QSO")
	}
	if cur := srv.Contacts.Current(); cur != nil {
		t.Errorf("the pulled QSO must end the QSO in progress: %+v", cur)
	}
}

// TestSyncButtonErrors: a refused pull or push reaches the operator as an
// error (the page shows it as a toast) and changes nothing.
func TestSyncButtonErrors(t *testing.T) {
	srv, st, key := newTestServer(t)
	f := &fakeClublog{status: http.StatusForbidden}
	f.install(t, srv)
	if err := st.SetQSLSentLocal(key, "B"); err != nil {
		t.Fatal(err)
	}
	h := srv.Routes()
	for _, p := range []string{"/sync/pull", "/sync/push"} {
		rec := postForm(t, h, p, nil)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "403") {
			t.Errorf("%s = %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 1 {
		t.Errorf("a refused push must stay pending: %d", len(pend))
	}
}

// TestSyncPushButton: the push uploads the cards sent here and clears them.
func TestSyncPushButton(t *testing.T) {
	srv, st, key := newTestServer(t)
	f := &fakeClublog{}
	f.install(t, srv)
	if err := st.SetQSLSentLocal(key, "D"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, srv.Routes(), "/sync/push", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "last push:") {
		t.Fatalf("push = %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.upload, "<CALL:6>DL1ABC") || !strings.Contains(f.upload, "<QSL_SENT_VIA:1>D") {
		t.Errorf("upload = %q", f.upload)
	}
	if pend, _ := st.PendingPushBack(); len(pend) != 0 {
		t.Errorf("pending after the push: %d", len(pend))
	}
}

// TestEventsStream: /events greets, relays broker events as SSE and
// unsubscribes when the window goes away.
func TestEventsStream(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	exited := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		close(exited)
	}))
	defer hs.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/events", nil)
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	lines := bufio.NewReader(resp.Body)
	readEvent := func() string {
		t.Helper()
		var ev []string
		for {
			l, err := lines.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v (so far %q)", err, ev)
			}
			if l == "\n" {
				return strings.Join(ev, "")
			}
			ev = append(ev, l)
		}
	}
	if ev := readEvent(); ev != "event: hello\ndata: {}\n" {
		t.Fatalf("first event = %q", ev)
	}
	srv.broker.Publish(events.QueueChanged("DL1ABC|20240101|120000|20m", "decided"))
	if ev := readEvent(); ev != "event: queue_changed\ndata: {\"key\":\"DL1ABC|20240101|120000|20m\",\"to\":\"decided\"}\n" {
		t.Fatalf("relayed event = %q", ev)
	}

	cancel() // the window closes
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler keeps running after the client left")
	}
}

func TestEventsWithoutBroker(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(&config.Config{}, st, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, srv.Routes(), "/events"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/events without a broker = %d", rec.Code)
	}
}

// TestStaticCaching: app assets revalidate (a new build must not run old
// JavaScript from the WebView cache), fonts are cached for good.
func TestStaticCaching(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	rec := get(t, h, "/static/live.js")
	etag := rec.Header().Get("ETag")
	if rec.Code != 200 || etag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("live.js = %d, ETag %q, Cache-Control %q", rec.Code, etag, rec.Header().Get("Cache-Control"))
	}
	req := httptest.NewRequest(http.MethodGet, "/static/live.js", nil)
	req.Header.Set("If-None-Match", etag)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified {
		t.Errorf("revalidation with the current ETag = %d, want 304", again.Code)
	}
	font := get(t, h, "/static/fonts/ibmplexmono-400.woff2")
	if font.Code != 200 || !strings.Contains(font.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("font = %d, Cache-Control %q", font.Code, font.Header().Get("Cache-Control"))
	}
	if rec := get(t, h, "/static/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d", rec.Code)
	}
}

// TestOutcomeTexts: every way a card can end is spelled out on /done, in
// English and German.
func TestOutcomeTexts(t *testing.T) {
	printed := sql.NullString{String: "2024-05-06T10:00:00Z", Valid: true}
	for _, c := range []struct {
		it   store.QueueItem
		want string
	}{
		{store.QueueItem{Status: "skipped", Note: "backlog"}, "no card (backlog)"},
		{store.QueueItem{Status: "skipped", DesiredMethod: "N"}, "no card"},
		{store.QueueItem{Status: "requested", Channel: "OQRS", Note: "2 USD"}, "their card requested via OQRS: 2 USD"},
		{store.QueueItem{Status: "requested", Channel: "PayPal"}, "their card requested via PayPal"},
		{store.QueueItem{Status: "sent", Note: "sent elsewhere"}, "sent elsewhere (per Clublog)"},
		{store.QueueItem{Status: "sent", DesiredMethod: "W"}, "written during the QSO"},
		{store.QueueItem{Status: "sent", DesiredMethod: "B", PrintedAt: printed}, "Bureau, printed"},
		{store.QueueItem{Status: "sent", DesiredMethod: "D"}, "Direct, written"},
		{store.QueueItem{Status: "sent", DesiredMethod: "D", Note: "written now"}, "Direct, written during the QSO"},
		{store.QueueItem{Status: "sent", DesiredMethod: "M", Manager: "K2ABC", SendVia: "B"}, "via manager K2ABC (bureau), written"},
		{store.QueueItem{Status: "sent", DesiredMethod: "M", Manager: "K2ABC", SendVia: "D", PrintedAt: printed}, "via manager K2ABC (direct), printed"},
		{store.QueueItem{Status: "sent", DesiredMethod: "M", Manager: "K2ABC"}, "via manager K2ABC, written"},
		{store.QueueItem{Status: "sent", DesiredMethod: "M"}, "via manager, written"},
		{store.QueueItem{Status: "sent"}, "sent, written"},
	} {
		it := c.it
		m := outcomeOf(&it)
		if got := m.String(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.it, got, c.want)
		}
		var missing []string
		i18n.Default.OnMissing = func(_, text string) { missing = append(missing, text) }
		i18n.Default.T("de", m.Text, m.Args...)
		i18n.Default.OnMissing = nil
		if len(missing) > 0 {
			t.Errorf("%q: missing from the German catalogs: %q", c.want, missing)
		}
	}
}

// TestClublogPauseShown: after a 403 every page's nav and the settings page
// say the automatic sync is paused; a pull by hand that gets through ends it.
func TestClublogPauseShown(t *testing.T) {
	srv, _, _ := newTestServer(t)
	f := &fakeClublog{status: http.StatusForbidden}
	f.install(t, srv)
	h := srv.Routes()
	if b := get(t, h, "/nav").Body.String(); strings.Contains(b, "Clublog paused") {
		t.Fatal("paused before any 403")
	}
	if rec := postForm(t, h, "/sync/pull", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("refused pull = %d", rec.Code)
	}
	if b := get(t, h, "/nav").Body.String(); !strings.Contains(b, "Clublog paused") || !strings.Contains(b, `href="/settings"`) {
		t.Errorf("nav after a 403:\n%s", b)
	}
	if b := get(t, h, "/settings").Body.String(); !strings.Contains(b, "Clublog refused these credentials (403)") {
		t.Errorf("settings page after a 403 does not explain the pause")
	}

	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if rec := postForm(t, h, "/sync/pull", nil); rec.Code != 200 {
		t.Fatalf("pull = %d", rec.Code)
	}
	if b := get(t, h, "/nav").Body.String(); strings.Contains(b, "Clublog paused") {
		t.Error("a pull that got through must end the pause")
	}
}

// TestSettingsCheckNotesRefusal: the credential check on saving the settings
// pauses after a 403 and ends a pause when Clublog lets it in.
func TestSettingsCheckNotesRefusal(t *testing.T) {
	srv, st, _ := newTestServer(t)
	f := &fakeClublog{status: http.StatusForbidden}
	f.install(t, srv)
	cfg := srv.config()
	_, msg := srv.validateCredentials(cfg)
	if !strings.HasPrefix(msg.String(), "ERROR") || sync.RefusedAt(st, srv.clublogFn(cfg.Clublog)) == "" {
		t.Fatalf("refused check: %q, paused %v", msg, srv.clublogPausedAt())
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if _, msg = srv.validateCredentials(cfg); !strings.HasPrefix(msg.String(), "OK") || srv.clublogPausedAt() != "" {
		t.Fatalf("check that got through: %q, still paused %q", msg, srv.clublogPausedAt())
	}
}
