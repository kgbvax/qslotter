package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/store"
)

// requestDE sends a request with a German browser.
func requestDE(t *testing.T, h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if method == http.MethodGet {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9,en;q=0.8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestGermanComplete renders every page and flow in German and fails on any
// text the German catalogs lack (VISION D3: no English left in a German UI).
func TestGermanComplete(t *testing.T) {
	missing := map[string]bool{}
	i18n.Default.OnMissing = func(lang, text string) { missing[text] = true }
	defer func() { i18n.Default.OnMissing = nil }()

	srv, st, key := newTestServer(t)
	h := srv.Routes()
	// Data that makes every panel show up: QRZ info with a manager and OQRS,
	// history (sent, received, LoTW), a Desk card with two QSOs, a request,
	// a sent card, a backlog card, an expected card.
	if err := st.PutStation(&store.StationInfo{Callsign: "DL1ABC", Name: "Hans", Addr1: "Str. 1", Country: "Germany",
		QSLMgr: "K2ABC OQRS", BioText: "QSL via OQRS or direct\nLoTW yes", MQSL: "1", LoTW: "1", EQSL: "0"}); err != nil {
		t.Fatal(err)
	}
	old := addQSO(t, st, "DL1ABC", "20230101", "40m")
	if err := st.SetQSLSentLocal(old, "B"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQSLRcvdLocal(old); err != nil {
		t.Fatal(err)
	}
	desk1 := addQueued(t, st, "DL2ZZZ", "20240103")
	desk2 := addQueued(t, st, "DL2ZZZ", "20240104")
	req := addQueued(t, st, "DL3YYY", "20240105")
	sent := addQueued(t, st, "DL4WWW", "20240106")
	addQueued(t, st, "DL1ABC/P", "20240107")
	for _, k := range []string{desk1, desk2, req, sent} {
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
	}
	postForm(t, h, "/work/requested", url.Values{"key": {req}, "channel": {"OQRS"}, "note": {"2 USD"}})
	postForm(t, h, "/work/written", url.Values{"key": {sent}, "route": {"MB"}, "manager": {"K2ABC"}})
	// One station per way the signal strip can look (chips, hint, every note).
	for call, in := range map[string][2]string{
		"DL6AAA": {"LoTW - eQSL", ""}, "DL6BBB": {"", "No paper QSL for FT8."}, "DL6CCC": {"direct", "No paper QSL."},
		"DL6DDD": {"bureau only, no direct, QSL Card, SAE", ""}, "DL6EEE": {"BUREAU, DIRECT", ""},
		"DL6FFF": {"direct only, no bureau", ""}, "DL6GGG": {"eQSL only", ""}, "DL6HHH": {"", ""},
		"DL6III": {"K2ABC (bureau only)", ""},
	} {
		if err := st.PutStation(&store.StationInfo{Callsign: call, QSLMgr: in[0], BioText: in[1], MQSL: "1",
			FetchedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
		k := addQueued(t, st, call, "20240108")
		requestDE(t, h, http.MethodGet, "/decide?key="+url.QueryEscape(k), nil)
		requestDE(t, h, http.MethodGet, "/station/"+call, nil)
	}
	backlog := addQueued(t, st, "DL5VVV", "20200101")
	if _, err := st.QueueDiscardBacklog("20210101"); err != nil {
		t.Fatal(err)
	}
	_ = backlog

	pages := []string{"/", "/queue", "/queue?compact=1", "/decide", "/decide?key=" + url.QueryEscape(key),
		"/work", "/work/card", "/work/card?filter=O", "/done", "/log", "/settings", "/receive",
		"/nav", "/queue/list", "/work/list", "/work/manager?manager=K2ABC", "/work/manager?manager=",
		"/receive?call=DL3YYY"}
	for _, p := range pages {
		if r := requestDE(t, h, http.MethodGet, p, nil); r.Code != 200 {
			t.Errorf("%s = %d", p, r.Code)
		}
	}
	posts := []struct {
		path string
		form url.Values
	}{
		{"/receive/lookup", url.Values{"call": {"DL1ABC"}}},
		{"/receive/book", url.Values{"call": {"DL1ABC"}, "key": {key, old}}},
		{"/receive/book", url.Values{"call": {"DL3YYY"}, "key": {req}}},
		{"/receive/reply?how=later", url.Values{"key": {key}}},
		{"/queue/recompute", nil},
		{"/work/written", url.Values{"key": {desk1}}}, // no route: error text
		{"/queue/none", url.Values{"key": {"NOPE|1|1|1"}}},
	}
	for _, p := range posts {
		requestDE(t, h, http.MethodPost, p.path, p.form)
	}
	if len(missing) > 0 {
		var list []string
		for m := range missing {
			list = append(list, m)
		}
		sort.Strings(list)
		t.Fatalf("%d text(s) missing from the German catalogs (internal/i18n/locales/de):\n%s", len(list), strings.Join(list, "\n"))
	}
}

// TestLanguageChoice: the setting wins over the browser, empty = browser.
func TestLanguageChoice(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	if b := requestDE(t, h, http.MethodGet, "/nav", nil).Body.String(); !strings.Contains(b, "Neue QSOs") {
		t.Fatalf("German browser, no setting: %s", b)
	}
	if b := get(t, h, "/nav").Body.String(); !strings.Contains(b, ">New QSOs") {
		t.Fatalf("no Accept-Language: English: %s", b)
	}
	srv.cfg.UI.Language = "en"
	if b := requestDE(t, h, http.MethodGet, "/nav", nil).Body.String(); !strings.Contains(b, ">New QSOs") {
		t.Fatalf("setting en wins over the browser: %s", b)
	}
	if b := requestDE(t, h, http.MethodGet, "/queue", nil).Body.String(); !strings.Contains(b, `<html lang="en">`) {
		t.Fatal("the page must declare its language")
	}
}
