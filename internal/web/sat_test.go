package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/store"
)

// A satellite QSO shows the satellite and both frequencies on the New QSOs
// card and on the Desk card.
func TestSatelliteQSOShowsSatAndFrequencies(t *testing.T) {
	srv, st, _ := newTestServer(t)
	h := srv.Routes()
	q := &store.QSO{QSLKey: "DM9EE|20261004|125349|2M", Call: "DM9EE", QSODate: "20261004", TimeOn: "125349",
		Band: "2M", Mode: "SSB", Freq: "145.8510", FreqRX: "435.6450", PropMode: "SAT", SatName: "RS-44", Hash: "hs"}
	if _, _, err := st.UpsertQSO(q); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetQSO(q.QSLKey)
	if err != nil || got.SatName != "RS-44" || got.FreqRX != "435.6450" {
		t.Fatalf("stored QSO = %+v, %v", got, err)
	}
	want := "Satellite <b>RS-44</b> &middot; TX 145.8510 &middot; RX 435.6450 MHz"
	if b := get(t, h, "/decide?key="+url.QueryEscape(q.QSLKey)).Body.String(); !strings.Contains(b, want) {
		t.Fatalf("New QSOs card without the satellite line:\n%s", b)
	}
	postForm(t, h, "/queue/yes", url.Values{"key": {q.QSLKey}})
	if b := get(t, h, "/work/card?key="+url.QueryEscape(q.QSLKey)).Body.String(); !strings.Contains(b, want) {
		t.Fatalf("Desk card without the satellite line:\n%s", b)
	}
	// Loggers leave PROP_MODE SAT on HF QSOs: without a satellite name or an
	// RX frequency the QSO is no satellite QSO.
	hf := &store.QSO{QSLKey: "KD2PLR|20261001|193300|20M", Call: "KD2PLR", QSODate: "20261001", TimeOn: "193300",
		Band: "20M", Mode: "SSB", Freq: "14.2680", PropMode: "SAT", Hash: "hh"}
	if _, _, err := st.UpsertQSO(hf); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(&store.QueueItem{QSLKey: hf.QSLKey, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if b := get(t, h, "/decide?key="+url.QueryEscape(hf.QSLKey)).Body.String(); strings.Contains(b, "Satellite") {
		t.Fatalf("an HF QSO with a stale PROP_MODE shows a satellite line:\n%s", b)
	}
}
