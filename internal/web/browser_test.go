package web

// Browser tests for static/keys.js and static/live.js: the real pages, htmx
// and scripts in headless Chrome against the real server. "Another window"
// is a plain HTTP request; a QSO from the logger is a store insert plus the
// event the UDP feed publishes. Without Chrome, or with -short, they skip.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/store"
)

// One headless Chrome for the package; each test gets its own tab.
var browser struct {
	once   gosync.Once
	ctx    context.Context
	cancel func()
	err    error
}

func browserCtx(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("browser test (-short)")
	}
	browser.once.Do(func() {
		alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), chromedp.DefaultExecAllocatorOptions[:]...)
		ctx, cancelCtx := chromedp.NewContext(alloc)
		browser.ctx, browser.cancel = ctx, func() { cancelCtx(); cancelAlloc() }
		browser.err = chromedp.Run(ctx) // starts Chrome
	})
	if browser.err != nil {
		t.Skipf("no headless Chrome: %v", browser.err)
	}
	return browser.ctx
}

func TestMain(m *testing.M) {
	code := m.Run()
	if browser.cancel != nil {
		browser.cancel()
	}
	os.Exit(code)
}

// esHook records the page's EventSource, so a test can wait until live.js
// listens: the server sends the first byte only after subscribing.
const esHook = `(function () {
  var ES = window.EventSource;
  if (!ES) return;
  window.__es = [];
  window.EventSource = function (u, o) { var e = new ES(u, o); window.__es.push(e); return e; };
  window.EventSource.prototype = ES.prototype;
})();`

type browserEnv struct {
	t    *testing.T
	srv  *Server
	st   store.Store
	base string
	tab  context.Context

	mu     gosync.Mutex
	jsErrs []string
}

// newBrowserEnv serves the UI and opens a tab; setup runs before the server
// takes requests. A JavaScript exception on any page fails the test.
func newBrowserEnv(t *testing.T, setup ...func(*Server)) *browserEnv {
	t.Helper()
	bctx := browserCtx(t)
	// A file: the browser's parallel requests use several connections, and
	// each connection to ":memory:" is a database of its own.
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{}
	cfg.Clublog.Call = "DL9ET"
	srv, err := New(cfg, st, events.New(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.printer = &fakePrinter{}
	for _, f := range setup {
		f(srv)
	}
	hs := httptest.NewServer(srv.Routes())
	t.Cleanup(func() { hs.CloseClientConnections(); hs.Close() })
	tab, cancel := chromedp.NewContext(bctx)
	t.Cleanup(cancel) // first: the tab and its event stream go before the server
	e := &browserEnv{t: t, srv: srv, st: st, base: hs.URL, tab: tab}
	chromedp.ListenTarget(tab, func(ev any) {
		if ex, ok := ev.(*runtime.EventExceptionThrown); ok {
			e.mu.Lock()
			e.jsErrs = append(e.jsErrs, ex.ExceptionDetails.Error())
			e.mu.Unlock()
		}
	})
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(esHook).Do(ctx)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, m := range e.jsErrs {
			t.Errorf("JavaScript exception: %s", m)
		}
	})
	return e
}

// open loads a page and waits until live.js listens to the event stream.
func (e *browserEnv) open(path string) {
	e.t.Helper()
	if err := chromedp.Run(e.tab, chromedp.Navigate(e.base+path)); err != nil {
		e.t.Fatal(err)
	}
	e.waitFor("the live event stream", `window.__es && window.__es.length && window.__es[0].readyState === 1`)
}

func (e *browserEnv) str(expr string) string {
	e.t.Helper()
	var s string
	if err := chromedp.Run(e.tab, chromedp.Evaluate(expr, &s)); err != nil {
		e.t.Fatalf("%s: %v", expr, err)
	}
	return s
}

// waitFor polls a JavaScript condition: live.js reacts after a settle delay.
func (e *browserEnv) waitFor(what, cond string) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ok bool
		if err := chromedp.Run(e.tab, chromedp.Evaluate("!!("+cond+")", &ok)); err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s\n  (%s)", what, cond)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// still asserts a condition holds once live.js had time to react (more than
// its settle delay plus a request).
func (e *browserEnv) still(what, cond string) {
	e.t.Helper()
	time.Sleep(900 * time.Millisecond)
	var ok bool
	if err := chromedp.Run(e.tab, chromedp.Evaluate("!!("+cond+")", &ok)); err != nil || !ok {
		e.t.Fatalf("%s no longer holds (%v)\n  (%s)", what, err, cond)
	}
}

// key types keys on the focused element (the page when nothing is) the way
// a person does: not while htmx is still swapping (the new card's buttons
// are wired when it settles), and with the text on the keydown, so a
// handler's preventDefault keeps a key out of the field it focuses
// (chromedp's KeyEvent sends the text as an event of its own).
func (e *browserEnv) key(keys string, mods ...input.Modifier) {
	e.t.Helper()
	e.waitFor("htmx to settle", `!document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')`)
	for _, r := range keys {
		var down, up *input.DispatchKeyEventParams
		text := ""
		for _, ev := range kb.Encode(r) {
			switch ev.Type {
			case input.KeyDown:
				down = ev
			case input.KeyUp:
				up = ev
			case input.KeyChar:
				text = ev.Text
			}
		}
		if text == "" || len(mods) > 0 {
			down.Type = input.KeyRawDown // no text: an arrow, Escape, a shortcut
		} else {
			down.Text, down.UnmodifiedText = text, text
		}
		for _, m := range mods {
			down.Modifiers |= m
			up.Modifiers |= m
		}
		if err := chromedp.Run(e.tab, down, up); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *browserEnv) click(sel string) {
	e.t.Helper()
	if err := chromedp.Run(e.tab, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		e.t.Fatal(err)
	}
}

// elsewhere does what another window would: a plain POST.
func (e *browserEnv) elsewhere(path string, form url.Values) {
	e.t.Helper()
	resp, err := http.PostForm(e.base+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.t.Fatalf("POST %s = %d", path, resp.StatusCode)
	}
}

// arrive files a new QSO in the Inbox the way the UDP feed does.
func (e *browserEnv) arrive(call, date string) string {
	e.t.Helper()
	k := addQueued(e.t, e.st, call, date)
	e.srv.broker.Publish(events.QueueChanged(k, "queued"))
	return k
}

// decided puts a QSO on the Desk.
func (e *browserEnv) decided(call, date string) string {
	e.t.Helper()
	k := addQueued(e.t, e.st, call, date)
	if err := e.st.QueueAccept(k); err != nil {
		e.t.Fatal(err)
	}
	return k
}

// waitStatus waits for the request a key or click sent.
func (e *browserEnv) waitStatus(key, want string) *store.QueueItem {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		it := status(e.t, e.st, key)
		if it.Status == want {
			return it
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s: status %q, want %q", key, it.Status, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func js(s string) string { return strconv.Quote(s) } // a JavaScript string literal (ASCII)

// Expressions for conditions; each in parentheses, so "x === y" compares
// the whole value.
const (
	decideKey    = `((document.querySelector('#decide') || {dataset: {}}).dataset.qslkey || '')`
	deskCardKeys = `((document.querySelector('#workcard') || {dataset: {}}).dataset.keys || '')`
	mdRows       = `(Array.from(document.querySelectorAll('#md-list tr.md-row')).map(function (r) { return r.dataset.key; }).join(','))`
	mdSel        = `((document.querySelector('#md-list tr.md-row.sel') || {dataset: {}}).dataset.key || '')`
)

// --- keys.js ---

// TestKeysInboxDecide: y (or j) says yes, n no card, each showing the next
// card; the arrows browse without deciding.
func TestKeysInboxDecide(t *testing.T) {
	e := newBrowserEnv(t)
	k1 := addQueued(t, e.st, "DL1AAA", "20240101")
	addQueued(t, e.st, "DL2BBB", "20240102")
	addQueued(t, e.st, "DL3CCC", "20240103")
	e.open("/decide?key=" + url.QueryEscape(k1))

	e.key(kb.ArrowRight)
	e.waitFor("the next card", decideKey+` !== `+js(k1)+` && `+decideKey+` !== ''`)
	e.key(kb.ArrowLeft)
	e.waitFor("the card back", decideKey+` === `+js(k1))
	if it := status(t, e.st, k1); it.Status != "queued" {
		t.Fatalf("browsing decided the card: %+v", it)
	}

	e.key("y")
	e.waitStatus(k1, "decided")
	e.waitFor("the next card after yes", decideKey+` !== `+js(k1)+` && `+decideKey+` !== ''`)
	second := e.str(decideKey)
	e.key("j")
	e.waitStatus(second, "decided")
	e.waitFor("the last card", decideKey+` !== `+js(second)+` && `+decideKey+` !== ''`)
	third := e.str(decideKey)
	e.key("n")
	e.waitStatus(third, "skipped")
	e.waitFor("the empty Inbox", decideKey+` === ''`)
}

// TestKeysInboxWrittenNow: w arms "written now" and b/d picks the route; any
// other key only disarms (w then n is no "no card"), so a slip decides
// nothing. Modifier keys are left to the browser.
func TestKeysInboxWrittenNow(t *testing.T) {
	e := newBrowserEnv(t)
	k := addQueued(t, e.st, "DL1AAA", "20240101")
	e.open("/decide?key=" + url.QueryEscape(k))
	armed := `document.querySelector('#decide .written-now.armed')`

	e.key("n", input.ModifierCtrl)
	e.key("y", input.ModifierMeta)
	e.key("w")
	e.waitFor("w to arm", armed)
	e.key("n")
	e.waitFor("n to disarm", `!`+armed)
	e.key("b") // not armed any more: b alone does nothing on the Inbox card
	e.still("an untouched card", decideKey+` === `+js(k))
	if it := status(t, e.st, k); it.Status != "queued" {
		t.Fatalf("modifiers, a disarmed w or a lone b decided the card: %+v", it)
	}

	e.key("w")
	e.waitFor("w to arm", armed)
	e.key("d")
	it := e.waitStatus(k, "sent")
	if it.Note != "written now" || it.DesiredMethod != "D" {
		t.Errorf("w d = %+v, want written now, direct", it)
	}
}

// TestKeysDeskRoute: b/d/m/v pick the route; m focuses the empty manager
// field, where typing is text (the B in K2ABC picks nothing); Escape leaves
// the field and w records the hand-written card.
func TestKeysDeskRoute(t *testing.T) {
	e := newBrowserEnv(t)
	k := e.decided("DL4DDD", "20240104")
	e.open("/work/card?key=" + url.QueryEscape(k))
	checked := func(v string) string { return `document.querySelector('#route-pick input[value="` + v + `"]').checked` }

	e.key("d")
	e.waitFor("route D", checked("D")+` && document.querySelector('#workcard').dataset.route === 'D'`)
	e.key("m")
	e.waitFor("route MD with the manager field focused", checked("MD")+` && document.activeElement.classList.contains('mgr-in')`)
	e.key("K2ABC")
	e.waitFor("the typed manager", `document.querySelector('#route-pick .mgr-in').value === 'K2ABC'`)
	if e.str(`String(`+checked("MD")+`)`) != "true" {
		t.Fatal("typing in the manager field changed the route")
	}
	e.key(kb.Escape)
	e.waitFor("Escape to leave the field", `!document.activeElement.classList.contains('mgr-in')`)
	e.key("w")
	it := e.waitStatus(k, "sent")
	if it.DesiredMethod != "M" || it.Manager != "K2ABC" || it.SendVia != "D" {
		t.Errorf("written card = %+v, want via manager K2ABC, direct", it)
	}
}

// TestKeysDeskRequest: r opens the request with the note focused (typing it
// triggers no keys) and Enter records it.
func TestKeysDeskRequest(t *testing.T) {
	e := newBrowserEnv(t)
	k := e.decided("DL4DDD", "20240104")
	e.open("/work/card?key=" + url.QueryEscape(k))
	e.key("r")
	e.waitFor("the request form", `!document.getElementById('req').hidden && document.activeElement.name === 'note'`)
	e.key("2 USD, ref 4711" + kb.Enter) // u, d, n, r would be Desk keys
	it := e.waitStatus(k, "requested")
	if it.Note != "2 USD, ref 4711" || it.Channel == "" {
		t.Errorf("request = %+v", it)
	}
}

// --- live.js ---

// TestLiveNavBadges: the nav counts follow a card moved in another window.
func TestLiveNavBadges(t *testing.T) {
	e := newBrowserEnv(t)
	k := addQueued(t, e.st, "DL1AAA", "20240101")
	e.open("/queue")
	badge := func(area string) string {
		return `((document.querySelector('header.site nav a[data-area=` + area + `] .cnt') || {}).textContent)`
	}
	e.waitFor("one new QSO", badge("inbox")+` === '1'`)
	e.elsewhere("/queue/yes", url.Values{"key": {k}})
	e.waitFor("the badges to follow", `!`+badge("inbox")+` && `+badge("desk")+` === '1'`)
}

// TestLiveCompactList: in the compact list rows appear (newest on top) and
// vanish as QSOs come and go; the count and the empty note follow.
func TestLiveCompactList(t *testing.T) {
	e := newBrowserEnv(t)
	k1 := addQueued(t, e.st, "DL1AAA", "20240101")
	e.open("/queue?compact=1")
	count := `document.getElementById('queue-count').textContent`
	row := func(k string) string { return `document.getElementById('row-' + ` + js(k) + `)` }

	k2 := e.arrive("DL2BBB", "20240102")
	e.waitFor("the new row on top", row(k2)+` && document.querySelector('#queue-body tr') === `+row(k2)+` && `+count+` === '2'`)
	e.elsewhere("/queue/none", url.Values{"key": {k1}})
	e.waitFor("the handled row to go", `!`+row(k1)+` && `+count+` === '1'`)
	e.elsewhere("/queue/yes", url.Values{"key": {k2}})
	e.waitFor("the empty list", count+` === '0' && !document.getElementById('empty-msg').hidden`)
}

// TestLiveDecideFollows: the card view loads a card when one arrives while
// it is empty, keeps its card while others arrive, and moves on when its
// card is handled in another window.
func TestLiveDecideFollows(t *testing.T) {
	e := newBrowserEnv(t)
	e.open("/decide")
	if k := e.str(decideKey); k != "" {
		t.Fatalf("empty Inbox shows %q", k)
	}
	k1 := e.arrive("DL1AAA", "20240101")
	e.waitFor("the arriving card", decideKey+` === `+js(k1))
	k2 := e.arrive("DL2BBB", "20240102")
	e.still("the card being decided", decideKey+` === `+js(k1))
	e.elsewhere("/queue/yes", url.Values{"key": {k1}})
	e.waitFor("the next card", decideKey+` === `+js(k2))
}

// TestLiveStationUpdated: the card reloads when the station's QRZ data lands.
func TestLiveStationUpdated(t *testing.T) {
	e := newBrowserEnv(t)
	k := addQueued(t, e.st, "DL1AAA", "20240101")
	e.open("/decide?key=" + url.QueryEscape(k))
	if err := e.st.PutStation(&store.StationInfo{Callsign: "DL1AAA", Name: "Hans", Addr1: "Musterweg 7", Country: "Germany",
		FetchedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	e.srv.broker.Publish(events.Event{Type: "station_updated", Data: "DL1AAA"})
	e.waitFor("the QRZ address on the card", `document.getElementById('decide').textContent.indexOf('Musterweg 7') >= 0`)
}

// TestLiveInboxMasterDetail: arrows and clicks select in the list and the
// pane follows; a decision shows the card below as the list shows it now
// (not as it was when the card was drawn); a card handled in another window
// gives way to the row taking its place.
func TestLiveInboxMasterDetail(t *testing.T) {
	e := newBrowserEnv(t)
	for i, call := range []string{"DL1AAA", "DL2BBB", "DL3CCC", "DL4DDD"} {
		addQueued(t, e.st, call, "2024010"+strconv.Itoa(i+1))
	}
	e.open("/queue")
	e.waitFor("the first card selected", mdSel+` !== '' && `+mdSel+` === `+decideKey)
	rows := strings.Split(e.str(mdRows), ",")
	if len(rows) != 4 || rows[0] != e.str(decideKey) {
		t.Fatalf("rows %v, pane %q", rows, e.str(decideKey))
	}

	e.key(kb.ArrowDown)
	e.waitFor("the second row and its card", mdSel+` === `+js(rows[1])+` && `+decideKey+` === `+js(rows[1]))
	if !strings.Contains(e.str(`location.search`), url.QueryEscape(rows[1])) {
		t.Errorf("URL %q does not name the selected card", e.str(`location.search`))
	}

	// The card below goes in another window: the list reloads, the pane
	// keeps its card - and y must move on to the new card below.
	e.elsewhere("/queue/none", url.Values{"key": {rows[2]}})
	e.waitFor("the list without it", mdRows+` === `+js(rows[0]+","+rows[1]+","+rows[3])+` && `+decideKey+` === `+js(rows[1]))
	e.key("y")
	e.waitStatus(rows[1], "decided")
	e.waitFor("the card now below, selected", decideKey+` === `+js(rows[3])+` && `+mdSel+` === `+js(rows[3])+
		` && `+mdRows+` === `+js(rows[0]+","+rows[3]))

	e.elsewhere("/queue/none", url.Values{"key": {rows[3]}})
	e.waitFor("the remaining row and its card", mdRows+` === `+js(rows[0])+` && `+decideKey+` === `+js(rows[0])+` && `+mdSel+` === `+js(rows[0]))

	k4 := e.arrive("DL4DDD", "20240104")
	e.waitFor("the arriving row", `document.getElementById('row-' + `+js(k4)+`)`)
	if e.str(decideKey) != rows[0] {
		t.Fatal("an arriving QSO replaced the card being decided")
	}
	e.click(`#md-list tr[data-key=` + js(k4) + `] td`)
	e.waitFor("the clicked card", decideKey+` === `+js(k4)+` && `+mdSel+` === `+js(k4))
}

// TestLiveDeskCardGrows: another QSO with the station joining the Desk
// reloads the card with it, keeping the route the operator picked.
func TestLiveDeskCardGrows(t *testing.T) {
	e := newBrowserEnv(t)
	a := e.decided("DL5EEE", "20240105")
	e.open("/work/card?key=" + url.QueryEscape(a))
	e.key("d")
	e.waitFor("route D", `document.querySelector('#route-pick input[value="D"]').checked`)

	b := addQueued(t, e.st, "DL5EEE", "20240106")
	e.elsewhere("/queue/yes", url.Values{"key": {b}})
	e.waitFor("both QSOs on the card", deskCardKeys+`.indexOf(`+js(a)+`) >= 0 && `+deskCardKeys+`.indexOf(`+js(b)+`) >= 0`)
	if e.str(`String(document.querySelector('#route-pick input[value="D"]').checked)`) != "true" {
		t.Error("the reload dropped the route the operator picked")
	}
}

// TestLiveDeskListKeepsChoice: a card arriving at the Desk reloads the list
// but leaves the shown card, and its unsaved route, alone.
func TestLiveDeskListKeepsChoice(t *testing.T) {
	e := newBrowserEnv(t)
	e.decided("DL6AAA", "20240101")
	e.decided("DL6BBB", "20240102")
	e.open("/work")
	e.waitFor("a card in the pane", deskCardKeys+` !== ''`)
	shown := e.str(deskCardKeys)
	e.key("d")
	e.waitFor("route D", `document.querySelector('#route-pick input[value="D"]').checked`)

	k := addQueued(t, e.st, "DL6CCC", "20240103")
	e.elsewhere("/queue/yes", url.Values{"key": {k}})
	e.waitFor("the new row", `document.querySelectorAll('#md-list tr.md-row').length === 3`)
	e.still("the shown card with its route", deskCardKeys+` === `+js(shown)+
		` && document.querySelector('#route-pick input[value="D"]').checked`)
}

// TestLiveCurrentContact: the "QSO in progress" box follows the logger.
func TestLiveCurrentContact(t *testing.T) {
	var tracker *contact.Tracker
	e := newBrowserEnv(t, func(srv *Server) {
		tracker = contact.NewTracker(srv.store, srv.broker, nil)
		srv.Contacts = tracker
	})
	e.open("/queue")
	call := `((document.getElementById('current') || {dataset: {}}).dataset.call)`
	e.waitFor("an idle box", call+` === ''`)
	tracker.Set(contact.Contact{Call: "DL7XYZ", Band: "20m"})
	e.waitFor("the station being worked", call+` === 'DL7XYZ'`)
	tracker.Clear()
	e.waitFor("the box to clear", call+` === ''`)
}
