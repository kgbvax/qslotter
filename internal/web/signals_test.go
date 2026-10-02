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

// The case that started the redesign: eQSL/LoTW listed next to "QSL Card" with
// mQSL=yes. The stored legacy verdict ("no card") must not matter any more.
func TestResearchStatesFactsNotAGuess(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "LOTW, QRZ, EQSL, QSL Card", MQSL: "1", EQSL: "1", LoTW: "1"})

	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{"Paper QSL accepted, no route stated.", `class="chip sig-accepts-paper`, `class="chip sig-electronic`, "LoTW"} {
		if !strings.Contains(body, want) {
			t.Errorf("research panel misses %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"Suggests", "confidence", "stamp none suggested", "stamp yes suggested"} {
		if strings.Contains(body, bad) {
			t.Errorf("research panel must not say %q:\n%s", bad, body)
		}
	}
	// Yes sends it to the Desk, which preselects nothing: QRZ stated no route.
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	card := get(t, h, "/work/card").Body.String()
	if !strings.Contains(card, `data-route=""`) || strings.Contains(card, "suggested: by QRZ") || strings.Contains(card, " checked") {
		t.Fatalf("the Desk must preselect nothing:\n%s", card)
	}
}

func TestStatedRouteIsSuggestedWithItsWords(t *testing.T) {
	srv, st, key := newTestServer(t)
	h := srv.Routes()
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "VIA BUREAU / eQSL", MQSL: "0"})
	body := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String()
	for _, want := range []string{`Suggests <b class="tentative">Bureau</b>`, "VIA BUREAU / eQSL", "stamp yes suggested", `chip sig-bureau decisive`} {
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
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "direct"})
	if b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String(); !strings.Contains(b, `tentative">Direct`) {
		t.Fatalf("direct expected:\n%s", b)
	}
	putStation(t, st, &store.StationInfo{Callsign: "DL1ABC", QSLMgr: "bureau"})
	if b := get(t, h, "/decide?key="+url.QueryEscape(key)).Body.String(); !strings.Contains(b, `tentative">Bureau`) {
		t.Fatalf("the memo must follow the stored fields:\n%s", b)
	}
}
