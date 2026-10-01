package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/store"
)

// TestInboxMasterDetail: /queue is the list plus the selected QSO's card; the
// list only selects (no decision buttons in it), ?key= selects, and a
// decision answers with the QSO below the decided one (VISION A6).
func TestInboxMasterDetail(t *testing.T) {
	srv, st, oldest := newTestServer(t) // 2024-01-01
	h := srv.Routes()
	mid := addQueued(t, st, "DL2ZZZ", "20240103")
	newest := addQueued(t, st, "DL3YYY", "20240104")

	page := get(t, h, "/queue").Body.String()
	list := page[strings.Index(page, `id="md-list"`):strings.Index(page, `class="md-detail"`)]
	for _, k := range []string{newest, mid, oldest} {
		if !strings.Contains(list, `data-key="`+k+`"`) {
			t.Fatalf("list misses %s:\n%s", k, list)
		}
	}
	if strings.Contains(list, "hx-post") {
		t.Fatalf("the list must only select:\n%s", list)
	}
	if !strings.Contains(page, `data-live="md-inbox"`) || !strings.Contains(page, `id="decide" data-qslkey="`+newest) {
		t.Fatalf("detail must show the newest QSO first:\n%s", page)
	}
	if strings.Contains(page, `data-key="arrowleft"`) {
		t.Fatal("no browse buttons in the master-detail pane - the list navigates")
	}
	// Selecting the middle one; its actions carry the neighbours.
	sel := get(t, h, "/queue?key="+url.QueryEscape(mid)).Body.String()
	if !strings.Contains(sel, `id="decide" data-qslkey="`+mid) || !strings.Contains(sel, "next="+url.QueryEscape(oldest)) || !strings.Contains(sel, "prev="+url.QueryEscape(newest)) {
		t.Fatalf("selected card / neighbours wrong:\n%s", sel)
	}
	// Deciding it moves to the one below (not back to the newest).
	r := postForm(t, h, "/queue/yes?work=1&md=1&key="+url.QueryEscape(mid)+"&next="+url.QueryEscape(oldest)+"&prev="+url.QueryEscape(newest), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `data-qslkey="`+oldest) {
		t.Fatalf("after a decision the next QSO down must follow:\n%s", r.Body)
	}
	// The last one: nothing below, the one above follows.
	r = postForm(t, h, "/queue/none?work=1&md=1&key="+url.QueryEscape(oldest)+"&prev="+url.QueryEscape(newest), nil)
	if !strings.Contains(r.Body.String(), `data-qslkey="`+newest) {
		t.Fatalf("after the last one the one above must follow:\n%s", r.Body)
	}
	if l := get(t, h, "/queue/list").Body.String(); !strings.Contains(l, `data-key="`+newest+`"`) || strings.Contains(l, mid) {
		t.Fatalf("/queue/list:\n%s", l)
	}
	// The compact list stays (yes / no per row).
	if c := get(t, h, "/queue?compact=1").Body.String(); !strings.Contains(c, "/queue/yes?key=") {
		t.Fatal("compact list lost its yes/no buttons")
	}
}

// TestDecideCardByCardMovesDown: the card-by-card view also moves to the QSO
// below after a decision.
func TestDecideCardByCardMovesDown(t *testing.T) {
	srv, st, oldest := newTestServer(t)
	h := srv.Routes()
	newest := addQueued(t, st, "DL3YYY", "20240104")
	mid := addQueued(t, st, "DL2ZZZ", "20240103")
	_ = newest
	body := getHX(t, h, "/decide?key="+url.QueryEscape(mid)).Body.String()
	if !strings.Contains(body, "next="+url.QueryEscape(oldest)) {
		t.Fatalf("decide actions must carry the QSO below:\n%s", body)
	}
	r := postForm(t, h, "/queue/yes?work=1&key="+url.QueryEscape(mid)+"&next="+url.QueryEscape(oldest), nil)
	if !strings.Contains(r.Body.String(), `data-qslkey="`+oldest) {
		t.Fatalf("card-by-card must move down:\n%s", r.Body)
	}
}

// TestDeskMasterDetail: /work is the grouped list plus the selected card;
// finishing a card answers with the card below in the list's order; batch
// fields carry each card's preselected route (VISION B2).
func TestDeskMasterDetail(t *testing.T) {
	srv, st, key := newTestServer(t) // DL1ABC: no QRZ -> route open
	h := srv.Routes()
	kd := addQueued(t, st, "DL2ZZZ", "20240103")
	kb := addQueued(t, st, "DL4WWW", "20240105")
	for call, m := range map[string]string{"DL2ZZZ": "D", "DL4WWW": "B"} {
		if err := st.PutStation(&store.StationInfo{Callsign: call, QSLMethod: m}); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{key, kd, kb} {
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
	}
	page := get(t, h, "/work").Body.String()
	// List order = groups: Route open (DL1ABC), Direct (DL2ZZZ), Bureau (DL4WWW).
	iO, iD, iB := strings.Index(page, `data-key="`+key+`"`), strings.Index(page, `data-key="`+kd+`"`), strings.Index(page, `data-key="`+kb+`"`)
	if iO < 0 || iD < iO || iB < iD {
		t.Fatalf("list order must follow the groups:\n%s", page)
	}
	if !strings.Contains(page, `data-live="md-desk"`) || !strings.Contains(page, `id="workcard" data-filter="" data-qslkey="`+key) {
		t.Fatalf("detail must show the first card of the list:\n%s", page)
	}
	if !strings.Contains(page, `name="route:`+kd+`" form="batch" value="D"`) {
		t.Fatalf("batch fields must carry the preselected route:\n%s", page)
	}
	// Selecting the Direct card, writing it: the Bureau card below follows.
	sel := getHX(t, h, "/work/card?md=1&key="+url.QueryEscape(kd)).Body.String()
	if !strings.Contains(sel, "next="+url.QueryEscape(kb)) || !strings.Contains(sel, "prev="+url.QueryEscape(key)) {
		t.Fatalf("Desk detail neighbours wrong:\n%s", sel)
	}
	r := postForm(t, h, "/work/written?view=work&md=1&next="+url.QueryEscape(kb)+"&prev="+url.QueryEscape(key), url.Values{"key": {kd}, "route": {"D"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), `data-qslkey="`+kb) {
		t.Fatalf("after finishing a card the one below must follow:\n%s", r.Body)
	}
	if l := get(t, h, "/work/list").Body.String(); strings.Contains(l, kd) || !strings.Contains(l, "Bureau <small>") {
		t.Fatalf("/work/list:\n%s", l)
	}
}
