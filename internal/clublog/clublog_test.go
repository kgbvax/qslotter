package clublog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake returns a client talking to a test server running h; the counter
// tells how many requests reached it.
func fake(t *testing.T, email, password string, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(email, password, "DL9ET", "key")
	c.BaseURL, c.HTTP = srv.URL, srv.Client()
	return c, &n
}

// TestPullLogSendsCredentials: credentials with form metacharacters (a "+"
// in the e-mail, "&", "=", "%" in the password) arrive unchanged; a garbled
// login is a 403, and repeated 403s get the IP banned.
func TestPullLogSendsCredentials(t *testing.T) {
	var got map[string]string
	c, _ := fake(t, "dl9et+log@example.org", "a&b=c%d+e f", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getadif.php" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		got = map[string]string{}
		for k := range r.PostForm {
			got[k] = r.PostForm.Get(k)
		}
		io.WriteString(w, "<CALL:5>DL1AB<EOR>\n")
	})
	body, err := c.PullLog("")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<CALL:5>DL1AB<EOR>\n" {
		t.Errorf("body = %q", body)
	}
	want := map[string]string{"email": "dl9et+log@example.org", "password": "a&b=c%d+e f", "call": "DL9ET", "api": "key"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("form = %v, want only %v (no date range for a full pull)", got, want)
	}
}

// TestPullLogDateRange: a YYYY-MM-DD start date becomes startyear/month/day;
// anything else pulls the whole log.
func TestPullLogDateRange(t *testing.T) {
	var form map[string]string
	c, _ := fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = map[string]string{"y": r.PostForm.Get("startyear"), "m": r.PostForm.Get("startmonth"), "d": r.PostForm.Get("startday")}
	})
	if _, err := c.PullLog("2026-10-02"); err != nil {
		t.Fatal(err)
	}
	if form["y"] != "2026" || form["m"] != "10" || form["d"] != "02" {
		t.Errorf("date range = %v", form)
	}
	for _, bad := range []string{"20261002", "2026/10/02", "2026-10-2"} {
		if _, err := c.PullLog(bad); err != nil {
			t.Fatal(err)
		}
		if form["y"]+form["m"]+form["d"] != "" {
			t.Errorf("PullLog(%q) sent a date range %v", bad, form)
		}
	}
}

// TestPullLogErrors: a 403 is reported as such (stop, no retry), other
// statuses and network failures as errors.
func TestPullLogErrors(t *testing.T) {
	c, n := fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := c.PullLog(""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403: err = %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("a 403 must not be retried: %d requests", n.Load())
	}

	c, _ = fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := c.PullLog(""); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("503: err = %v", err)
	}

	c, _ = fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {})
	c.BaseURL = "http://127.0.0.1:1" // nothing listens
	if _, err := c.PullLog(""); err == nil || !strings.Contains(err.Error(), "clublog pull") {
		t.Errorf("network: err = %v", err)
	}
}

// TestPushLogs: the upload is a multipart form with the credentials and the
// ADIF as the file part.
func TestPushLogs(t *testing.T) {
	const adif = "<CALL:5>DL1AB<QSL_SENT:1>Y<EOR>\n"
	var fields map[string]string
	var file, filename string
	c, _ := fake(t, "dl9et+log@example.org", "a&b", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/putlogs.php" {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		file, filename = string(b), h.Filename
	})
	if err := c.PushLogs([]byte(adif)); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"email": "dl9et+log@example.org", "password": "a&b", "callsign": "DL9ET", "api": "key"}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %q, want %q", k, fields[k], v)
		}
	}
	if file != adif || !strings.HasSuffix(filename, ".adi") {
		t.Errorf("file %q = %q", filename, file)
	}
}

func TestPushLogsErrors(t *testing.T) {
	c, n := fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if err := c.PushLogs([]byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403: err = %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("a 403 must not be retried: %d requests", n.Load())
	}

	c, _ = fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "upload queue full")
	})
	if err := c.PushLogs([]byte("x")); err == nil || !strings.Contains(err.Error(), "HTTP 500: upload queue full") {
		t.Errorf("500: err = %v", err)
	}
}

// TestCheckCredentials: a tiny pull of today; 403 reads as rejected
// credentials, other failures pass through unchanged.
func TestCheckCredentials(t *testing.T) {
	code := http.StatusOK
	var start string
	c, _ := fake(t, "e", "p", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		start = r.PostForm.Get("startyear") + "-" + r.PostForm.Get("startmonth") + "-" + r.PostForm.Get("startday")
		w.WriteHeader(code)
	})
	if err := c.CheckCredentials(); err != nil {
		t.Fatalf("200: %v", err)
	}
	if today := time.Now().UTC().Format("2006-01-02"); start != today {
		t.Errorf("checked from %s, want today (%s) - a full pull is too big for a check", start, today)
	}
	code = http.StatusForbidden
	if err := c.CheckCredentials(); err == nil || !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("403: err = %v", err)
	}
	code = http.StatusBadGateway
	if err := c.CheckCredentials(); err == nil || strings.Contains(err.Error(), "credentials") {
		t.Errorf("502 is not a credential problem: err = %v", err)
	}
}
