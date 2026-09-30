package qrz

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStripHTMLDropsStyleAndScript(t *testing.T) {
	in := `<html><head><style>#biodata { color:#fc0; text-shadow: 1px 1px }</style>
<script>var x = "qsl direct only";</script></head>
<body><!-- comment: qsl bureau -->
<p>QSL via <b>bureau</b> &amp; LoTW.</p><p>&nbsp;</p><p>Direct: SAE + $2</p>
<div>73<br>Hans</div></body></html>`
	got := stripHTML(in)
	for _, junk := range []string{"biodata", "text-shadow", "var x", "comment", "<", ">", "&amp;", "&nbsp;"} {
		if strings.Contains(got, junk) {
			t.Errorf("stripHTML left %q in %q", junk, got)
		}
	}
	for _, want := range []string{"QSL via bureau & LoTW.", "Direct: SAE + $2", "73\nHans"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripHTML lost %q; got %q", want, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank lines not collapsed: %q", got)
	}
}

// fakeQRZ serves login + lookup with a scripted lookup body.
func fakeQRZ(t *testing.T, lookupBody string, lookups *int32, rawQueries *[]string) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rawQueries != nil {
			*rawQueries = append(*rawQueries, r.URL.RawQuery)
		}
		if strings.Contains(r.URL.RawQuery, "username=") {
			fmt.Fprint(w, `<QRZDatabase><Session><Key>K1</Key></Session></QRZDatabase>`)
			return
		}
		atomic.AddInt32(lookups, 1)
		fmt.Fprint(w, lookupBody)
	}))
	t.Cleanup(srv.Close)
	c := New("u", "p", "test/1.0")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	return c
}

func TestLookupNotFoundIsAnAnswer(t *testing.T) {
	var n int32
	c := fakeQRZ(t, `<QRZDatabase><Session><Error>Not found: XX1XX</Error></Session></QRZDatabase>`, &n, nil)
	cs, err := c.Lookup("XX1XX")
	if err != nil || cs != nil {
		t.Fatalf("not found = %v, %v; want nil, nil", cs, err)
	}
}

func TestLookupSessionRetryIsBounded(t *testing.T) {
	var n int32
	c := fakeQRZ(t, `<QRZDatabase><Session><Error>Invalid session key</Error></Session></QRZDatabase>`, &n, nil)
	if _, err := c.Lookup("DL1ABC"); err == nil {
		t.Fatal("persistent session errors must surface as an error")
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("lookup attempts = %d, want exactly 2 (one retry after re-login)", got)
	}
}

func TestLoginEscapesCredentials(t *testing.T) {
	var n int32
	var queries []string
	c := fakeQRZ(t, `<QRZDatabase><Callsign><call>DL1ABC</call></Callsign></QRZDatabase>`, &n, &queries)
	c.Password = "p;a&ss w/rd"
	if cs, err := c.Lookup("DL1ABC"); err != nil || cs == nil || cs.Call != "DL1ABC" {
		t.Fatalf("lookup = %v, %v", cs, err)
	}
	login := queries[0]
	if strings.Contains(login, "p;a&ss") || !strings.Contains(login, "password=p%3Ba%26ss+w%2Frd") {
		t.Fatalf("password not escaped in login query: %q", login)
	}
}
