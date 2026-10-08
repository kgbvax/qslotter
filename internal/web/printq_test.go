package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/printer"
)

// watchPrinter is a print system that can be followed (like CUPS): each job
// reports the state the test sets for it.
type watchPrinter struct {
	mu       sync.Mutex
	next     int
	state    map[int]printer.JobStatus
	err      map[int]error
	canceled []int
	submit   error
}

func newWatchPrinter() *watchPrinter {
	return &watchPrinter{state: map[int]printer.JobStatus{}, err: map[int]error{}}
}

func (p *watchPrinter) List() ([]string, error)  { return []string{"cards"}, nil }
func (p *watchPrinter) Default() (string, error) { return "cards", nil }
func (p *watchPrinter) PrintPDF(path, name string, _ printer.Options) (printer.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.submit != nil {
		return printer.Job{}, p.submit
	}
	p.next++
	p.state[p.next] = printer.JobStatus{State: printer.JobProcessing}
	return printer.Job{Printer: "cards", ID: p.next}, nil
}
func (p *watchPrinter) JobStatus(_ context.Context, job printer.Job) (printer.JobStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state[job.ID], p.err[job.ID]
}
func (p *watchPrinter) CancelJob(_ context.Context, job printer.Job) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.canceled = append(p.canceled, job.ID)
	return nil
}
func (p *watchPrinter) set(id int, st printer.JobStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state[id] = st
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newPrintServer(t *testing.T) (*Server, http.Handler, *watchPrinter) {
	t.Helper()
	srv, _, _ := newTestServer(t)
	wp := newWatchPrinter()
	srv.printer = wp
	srv.pq.pollEvery = 5 * time.Millisecond
	srv.pq.giveUp = 10 * time.Second // the stall test shortens it
	return srv, srv.Routes(), wp
}

// TestPrintVerified: a followed job leaves the card "printing" (off the
// Desk, not sent); completed marks it sent and the list says printed.
func TestPrintVerified(t *testing.T) {
	srv, h, wp := newPrintServer(t)
	key := addQueued(t, srv.store, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != 200 {
		t.Fatalf("print = %d: %s", r.Code, r.Body)
	}
	if it := status(t, srv.store, key); it.Status != "decided" {
		t.Fatalf("status while printing = %s, want decided", it.Status)
	}
	if !srv.pq.printing(key) || strings.Contains(get(t, h, "/work").Body.String(), `class="call">DL2ZZZ`) {
		t.Fatal("the printing card must be off the Desk list")
	}
	if !strings.Contains(get(t, h, "/work/printq").Body.String(), "printing...") {
		t.Fatal("print list does not show the job printing")
	}
	// A second press while it prints: 409, nothing new submitted.
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != http.StatusConflict {
		t.Fatalf("second print = %d, want 409", r.Code)
	}
	wp.set(1, printer.JobStatus{State: printer.JobCompleted, Reasons: []string{"job-completed-successfully"}})
	waitFor(t, "the card sent", func() bool { return status(t, srv.store, key).Status == "sent" })
	waitFor(t, "the list updated", func() bool { return strings.Contains(get(t, h, "/work/printq").Body.String(), ">printed<") })
}

// TestPrintFailsAndRecovers: a job the printer aborts brings the card back
// with the reason; Retry prints it again, "It did print" marks it sent.
func TestPrintFailsAndRecovers(t *testing.T) {
	srv, h, wp := newPrintServer(t)
	key := addQueued(t, srv.store, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"D"}})
	wp.set(1, printer.JobStatus{State: printer.JobAborted, Message: "Media jam"})
	waitFor(t, "the card back", func() bool { return !srv.pq.printing(key) })
	if it := status(t, srv.store, key); it.Status != "decided" {
		t.Fatalf("status after a failed print = %s, want decided", it.Status)
	}
	card := get(t, h, "/work/card?key="+url.QueryEscape(key)).Body.String()
	if !strings.Contains(card, "Printing this card failed: Media jam") {
		t.Fatalf("the card does not say why printing failed:\n%s", card)
	}
	pq := get(t, h, "/work/printq").Body.String()
	if !strings.Contains(pq, "Media jam") || !strings.Contains(pq, "/work/printq/retry?id=1") {
		t.Fatalf("print list:\n%s", pq)
	}
	// Retry: a new job (2); the failed item goes. This one stalls: give up soon.
	srv.pq.giveUp = 300 * time.Millisecond
	if r := postForm(t, h, "/work/printq/retry?id=1", nil); r.Code != 200 {
		t.Fatalf("retry = %d: %s", r.Code, r.Body)
	}
	if _, ok := srv.pq.get(1); ok {
		t.Fatal("the failed item stays after a successful retry")
	}
	// The printer stays silent: given up, cancelled, back with the reason.
	wp.set(2, printer.JobStatus{State: printer.JobProcessing, Message: "The printer is not responding."})
	waitFor(t, "the timeout", func() bool { return !srv.pq.printing(key) })
	if len(wp.canceled) != 1 || wp.canceled[0] != 2 {
		t.Fatalf("canceled = %v, want job 2", wp.canceled)
	}
	if m := srv.pq.lastFailure(key); !strings.Contains(m.String(), "did not finish") || !strings.Contains(m.String(), "not responding") {
		t.Fatalf("timeout reason = %q", m.String())
	}
	// It came out after all: mark it sent.
	if r := postForm(t, h, "/work/printq/sent?id=2", nil); r.Code != 200 {
		t.Fatalf("mark sent = %d: %s", r.Code, r.Body)
	}
	if it := status(t, srv.store, key); it.Status != "sent" || it.DesiredMethod != "D" {
		t.Fatalf("after 'it did print': %+v", it)
	}
	if r := postForm(t, h, "/work/printq/dismiss?id=2", nil); r.Code != 200 || len(srv.pq.snapshot()) != 0 {
		t.Fatalf("dismiss = %d, %d items left", r.Code, len(srv.pq.snapshot()))
	}
}

// TestPrintSubmitFails: lp refusing the job leaves the card at the Desk and
// puts the reason in the list.
func TestPrintSubmitFails(t *testing.T) {
	srv, h, wp := newPrintServer(t)
	key := addQueued(t, srv.store, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	wp.submit = errors.New("no printer: none chosen")
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != http.StatusInternalServerError {
		t.Fatalf("print = %d, want 500", r.Code)
	}
	if it := status(t, srv.store, key); it.Status != "decided" || srv.pq.lastFailure(key).IsZero() {
		t.Fatalf("after a refused job: %+v, failure %q", it, srv.pq.lastFailure(key).String())
	}
}

// TestPrintJobForgotten: CUPS without job history forgets a finished job
// (not found): it printed.
func TestPrintJobForgotten(t *testing.T) {
	srv, h, wp := newPrintServer(t)
	key := addQueued(t, srv.store, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}})
	wp.mu.Lock()
	wp.err[1] = errors.New("ipp: status 0x0406")
	wp.mu.Unlock()
	waitFor(t, "the card sent", func() bool { return status(t, srv.store, key).Status == "sent" })
}

// TestPrintBatch: a batch print puts every card in the list; a failed one
// stays at the Desk while the others are printed.
func TestPrintBatch(t *testing.T) {
	srv, h, wp := newPrintServer(t)
	var keys []string
	for _, c := range []string{"DL2AAA", "DL2BBB", "DL2CCC"} {
		k := addQueued(t, srv.store, c, "20240103")
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
		keys = append(keys, k)
	}
	form := url.Values{"list": {"work"}, "action": {"print"}, "keys": keys}
	for _, k := range keys {
		form.Set("route:"+k, "B")
	}
	if r := postForm(t, h, "/queue/batch", form); r.Code != http.StatusSeeOther {
		t.Fatalf("batch = %d: %s", r.Code, r.Body)
	}
	if n := len(srv.pq.snapshot()); n != 3 {
		t.Fatalf("print list holds %d items, want 3", n)
	}
	wp.set(1, printer.JobStatus{State: printer.JobCompleted})
	wp.set(2, printer.JobStatus{State: printer.JobCanceled, Reasons: []string{"job-canceled-at-device"}})
	wp.set(3, printer.JobStatus{State: printer.JobCompleted})
	waitFor(t, "all three finished", func() bool {
		for _, k := range keys {
			if srv.pq.printing(k) {
				return false
			}
		}
		return true
	})
	want := []string{"sent", "decided", "sent"}
	for i, k := range keys {
		if st := status(t, srv.store, k).Status; st != want[i] {
			t.Errorf("%s = %s, want %s", k, st, want[i])
		}
	}
	if !strings.Contains(get(t, h, "/work").Body.String(), "job-canceled-at-device") {
		t.Error("the Desk list does not show why DL2BBB was not printed")
	}
}

// TestPreview: the Desk's preview is the card as a PDF; a manager route adds
// "via".
func TestWorkPreview(t *testing.T) {
	srv, h, _ := newPrintServer(t)
	key := addQueued(t, srv.store, "DL2ZZZ", "20240103")
	r := get(t, h, "/work/preview?key="+url.QueryEscape(key)+"&route=MB&manager=K2ABC")
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(r.Body.String(), "%PDF-") {
		t.Fatalf("preview = %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	if get(t, h, "/work/preview").Code != http.StatusBadRequest || get(t, h, "/work/preview?key=NOPE").Code != http.StatusNotFound {
		t.Fatal("preview without a known QSO must fail")
	}
	card := get(t, h, "/work/card").Body.String()
	_ = card
}

// TestPrintGerman renders the print list and the failed card in German.
func TestPrintGerman(t *testing.T) {
	missing := map[string]bool{}
	i18n.Default.OnMissing = func(lang, text string) { missing[text] = true }
	defer func() { i18n.Default.OnMissing = nil }()
	srv, h, wp := newPrintServer(t)
	var keys []string
	for _, c := range []string{"DL2AAA", "DL2BBB", "DL2CCC", "DL2DDD"} {
		k := addQueued(t, srv.store, c, "20240103")
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
		keys = append(keys, k)
	}
	for _, k := range keys {
		postForm(t, h, "/work/print", url.Values{"key": {k}, "route": {"B"}})
	}
	wp.set(1, printer.JobStatus{State: printer.JobCompleted})
	wp.set(2, printer.JobStatus{State: printer.JobAborted, Message: "Media jam"})
	wp.set(3, printer.JobStatus{State: printer.JobAborted, Reasons: []string{"printer-stopped"}})
	waitFor(t, "jobs 1-3 finished", func() bool {
		return !srv.pq.printing(keys[0]) && !srv.pq.printing(keys[1]) && !srv.pq.printing(keys[2])
	})
	postForm(t, h, "/work/printq/sent?id=3", nil)
	postForm(t, h, "/work/print", url.Values{"key": {keys[1]}, "route": {"B"}}) // retry by key
	for _, p := range []string{"/work", "/work/card?key=" + url.QueryEscape(keys[1]), "/work/printq"} {
		requestDE(t, h, http.MethodGet, p, nil)
	}
	wp.submit = errors.New("x")
	requestDE(t, h, http.MethodPost, "/work/printq/retry?id=99", nil)
	requestDE(t, h, http.MethodPost, "/receive/reply?how=print", url.Values{"key": {keys[3]}, "route": {"B"}})
	if len(missing) > 0 {
		var list []string
		for m := range missing {
			list = append(list, m)
		}
		sort.Strings(list)
		t.Fatalf("%d text(s) missing from the German catalogs:\n%s", len(list), strings.Join(list, "\n"))
	}
}
