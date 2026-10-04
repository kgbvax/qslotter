package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/store"
)

func putStation(t *testing.T, st store.Store, in *store.StationInfo) {
	t.Helper()
	in.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	if err := st.PutStation(in); err != nil {
		t.Fatal(err)
	}
}

// The research panel shows the classification: status, every accepted route
// (the preferred one marked), the contribution flag, and the route the Desk
// preselects with the classifier's reason.
func TestResearchShowsClassification(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "VIA BUREAU OR DIRECT, DIRECT PREFERRED, SASE",
		MQSL: "1", Addr1: "Hauptstr. 1", Addr2: "Berlin"})
	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{`chip sig-status-paper"`, `chip sig-bureau"`, `chip sig-direct decisive`, "Direct (preferred)",
		"asks for return postage", `Suggests <b class="tentative">Direct</b>`, "stamp yes suggested"} {
		if !strings.Contains(body, want) {
			t.Errorf("research panel misses %q:\n%s", want, body)
		}
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if card := get(t, h, "/work/card").Body.String(); !strings.Contains(card, `value="D" data-key="d" checked`) {
		t.Fatalf("the Desk must preselect the preferred route:\n%s", card)
	}
}

// Nothing about cards in the text: mqsl and the postal address decide (the
// operator's rule, LABELS.md 6); direct needs a full address (rule 5).
func TestFlagsAndAddressDecideWhenNothingIsStated(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	for _, c := range []struct {
		info *store.StationInfo
		want string
	}{
		{&store.StationInfo{Callsign: "DL1ABC", MQSL: "1", Addr1: "Hauptstr. 1", Addr2: "Berlin"}, `Suggests <b class="tentative">Direct</b>`},
		{&store.StationInfo{Callsign: "DL1ABC", MQSL: "1"}, `Suggests <b class="tentative">Bureau</b>`},
		{&store.StationInfo{Callsign: "DL1ABC", MQSL: "0", Addr1: "Hauptstr. 1", Addr2: "Berlin"}, `Suggests <b class="tentative">No card</b>`},
		{&store.StationInfo{Callsign: "DL1ABC"}, "QRZ says nothing about QSL cards - nothing preselected."},
		{&store.StationInfo{Callsign: "DL1ABC", QSLMgr: "Direct only", Addr2: "Berlin"}, `chip sig-status-unclear`},
	} {
		putStation(t, st, c.info)
		if b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String(); !strings.Contains(b, c.want) {
			t.Errorf("%+v: want %q:\n%s", c.info, c.want, b)
		}
	}
}

func TestStatedRouteIsSuggestedWithItsWords(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "VIA BUREAU / eQSL", MQSL: "0"})
	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{`Suggests <b class="tentative">Bureau</b>`, "VIA BUREAU / eQSL", "stamp yes suggested", `chip sig-bureau"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "stamp none suggested") {
		t.Errorf("a stated route must not hint \"no card\":\n%s", body)
	}
}

func TestStatedRefusalHintsNoCard(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "eQSL only", MQSL: "0"})
	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	if !strings.Contains(body, "stamp none suggested") || !strings.Contains(body, `Suggests <b class="tentative">No card</b>`) {
		t.Fatalf("an explicit refusal is a stated signal:\n%s", body)
	}
}

func TestManagerWayDecidesTheDeskRoute(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "K2ABC (bureau only)"})
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	card := get(t, h, "/work/card").Body.String()
	if !strings.Contains(card, `value="MB" data-key="v" checked`) || !strings.Contains(card, `value="K2ABC"`) {
		t.Fatalf("via manager, bureau with K2ABC expected:\n%s", card)
	}
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "K2ABC"})
	card = get(t, h, "/work/card?key="+url.QueryEscape(key)).Body.String()
	if !strings.Contains(card, `value="MD" data-key="m" checked`) {
		t.Fatalf("a manager without a stated way keeps via manager, direct:\n%s", card)
	}
}

// A change of the reading rules applies to cached rows at once.
func TestAssessmentIsNotStale(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "direct", Addr1: "Hauptstr. 1", Addr2: "Berlin"})
	if b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String(); !strings.Contains(b, `tentative">Direct`) {
		t.Fatalf("direct expected:\n%s", b)
	}
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "bureau"})
	if b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String(); !strings.Contains(b, `tentative">Bureau`) {
		t.Fatalf("the memo must follow the stored fields:\n%s", b)
	}
}
