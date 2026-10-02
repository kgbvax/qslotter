package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/qpc"
)

func TestMetricsToyGold(t *testing.T) {
	ps := []pair{
		{Call: "A", Gold: qpc.Bureau, Pred: qpc.Bureau, Conf: "high"},
		{Call: "B", Gold: qpc.Bureau, Pred: qpc.Direct, Conf: "medium"},
		{Call: "C", Gold: qpc.Unclear, GoldVia: "EA5GL", Pred: qpc.Unclear, PredVia: "EA5GL", Conf: "high"},
		{Call: "D", Gold: qpc.Unknown, Pred: unreadable},
	}
	m := computeMetrics(ps)
	if m.LabelAcc != 0.5 || m.ViaAcc != 1 || m.Both != 0.5 || m.Unreadable != 1 {
		t.Errorf("acc %v via %v both %v unreadable %d", m.LabelAcc, m.ViaAcc, m.Both, m.Unreadable)
	}
	if m.HighCov != 0.5 || m.HighAcc != 1 || m.HighMedCov != 0.75 || m.HighMedAcc != 2.0/3 {
		t.Errorf("confidence: %v %v %v %v", m.HighCov, m.HighAcc, m.HighMedCov, m.HighMedAcc)
	}
	// Per class: bureau P=1 R=.5 F1=2/3; direct P=0 (F1 0); unclear 1; unknown R=0.
	want := map[qpc.Label]float64{qpc.Bureau: 2.0 / 3, qpc.Direct: 0, qpc.Unclear: 1, qpc.Unknown: 0}
	for _, c := range m.PerClass {
		if math.Abs(c.F1-want[c.Label]) > 1e-9 {
			t.Errorf("%s F1 = %v, want %v", c.Label, c.F1, want[c.Label])
		}
	}
	if math.Abs(m.MacroF1-(2.0/3+1)/4) > 1e-9 {
		t.Errorf("macro-F1 %v", m.MacroF1)
	}
	// kappa: po=.5; gold B:2 U:1 K:1, pred B:1 D:1 U:1 X:1 -> pe=(2*1+1*1)/16
	pe := 3.0 / 16
	if math.Abs(m.Kappa-(0.5-pe)/(1-pe)) > 1e-9 {
		t.Errorf("kappa %v", m.Kappa)
	}
	if m.Confusion[qpc.Bureau][qpc.Direct] != 1 || m.Confusion[qpc.Unknown][unreadable] != 1 {
		t.Errorf("confusion %v", m.Confusion)
	}
}

func TestMapHeuristic(t *testing.T) {
	cases := []struct {
		r     qsldetermine.Result
		label qpc.Label
		via   string
	}{
		{qsldetermine.Result{Method: "B"}, qpc.Bureau, ""},
		{qsldetermine.Result{Method: "D"}, qpc.Direct, ""},
		{qsldetermine.Result{Method: "M", Manager: "EA5GL", Reason: "qslmgr field: EA5GL"}, qpc.Unclear, "EA5GL"},
		{qsldetermine.Result{Method: "M", Manager: "K2ABC", Reason: "qslmgr field: K2ABC (bureau only)"}, qpc.Bureau, "K2ABC"},
		{qsldetermine.Result{Method: "M", Manager: "IK2DUW", Reason: "bio: QSL via IK2DUW direct"}, qpc.Direct, "IK2DUW"},
		{qsldetermine.Result{RefusePaper: true}, qpc.NoPaper, ""},
		{qsldetermine.Result{}, qpc.Unknown, ""},
	}
	for _, c := range cases {
		l, v := mapHeuristic(c.r)
		if l != c.label || v != c.via {
			t.Errorf("%+v -> %s %q, want %s %q", c.r, l, v, c.label, c.via)
		}
	}
}

func TestDrawOrderDistinctAndSeeded(t *testing.T) {
	var qsos []*store.QSO
	for _, c := range []string{"DL1ABC", "DL1ABC/P", "EA8/DL1ABC", "K1A", "K1A", "F4XYZ", "G0AAA"} {
		qsos = append(qsos, &store.QSO{Call: c})
	}
	a, b := drawOrder(qsos, 42), drawOrder(qsos, 42)
	if len(a) != 4 {
		t.Fatalf("want 4 distinct stations, got %d", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("same seed, different order")
		}
	}
}

func TestLabelHandler(t *testing.T) {
	gold := filepath.Join(t.TempDir(), "gold.jsonl")
	items := []item{{Station: qpc.Station{Call: "K1A", Bio: "QSL via buro"}}}
	h, err := newLabelHandler(items, gold)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/label", strings.NewReader(body)))
		return rec.Code
	}
	if c := post(`{"call":"k1a","label":"unclear","via":" ea5gl "}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if c := post(`{"call":"K1A","label":"bureau"}`); c != 200 {
		t.Fatalf("relabel: %d", c)
	}
	if c := post(`{"call":"K1A","label":"manager"}`); c != 400 {
		t.Errorf("invalid label: %d", c)
	}
	if c := post(`{"call":"ZZ9ZZ","label":"bureau"}`); c != 400 {
		t.Errorf("unknown station: %d", c)
	}
	g, _ := loadGold(gold)
	if g["K1A"].Label != qpc.Bureau || len(g) != 1 {
		t.Errorf("last label must win: %+v", g)
	}
	all, _ := readJSONL[goldLabel](gold)
	if len(all) != 2 || all[0].Via != "EA5GL" {
		t.Errorf("append-only log: %+v", all)
	}

	// The page state never carries predictions.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var st map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &st)
	for k := range st {
		if k != "stations" && k != "gold" && k != "labels" && k != "rules" {
			t.Errorf("unexpected state key %q", k)
		}
	}
}

func TestRunResumesAndGuardsChanges(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"evidence\":\"\",\"label\":\"unknown\",\"via\":\"\",\"confidence\":\"low\"}"}}]}`)
	}))
	defer srv.Close()
	items := []item{{Station: qpc.Station{Call: "K1A"}}, {Station: qpc.Station{Call: "K2B"}}, {Station: qpc.Station{Call: "K3C"}}}
	out := filepath.Join(t.TempDir(), "runs", "m")
	v := qpc.Variant{Name: "m", BaseURL: srv.URL, Model: "m"}.WithDefaults()
	ctx := context.Background()

	if err := runVariant(ctx, v, items[:2], "ds1", out, false); err != nil {
		t.Fatal(err)
	}
	if err := runVariant(ctx, v, items, "ds1", out, false); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("resume: %d model calls, want 3", n)
	}
	res, _ := lastResults(filepath.Join(out, "results.jsonl"))
	if len(res) != 3 {
		t.Errorf("results: %d", len(res))
	}

	changed := v
	changed.BioMaxChars = 100
	if err := runVariant(ctx, changed, items, "ds1", out, false); err == nil || !strings.Contains(err.Error(), "settings changed") {
		t.Errorf("changed settings accepted: %v", err)
	}
	if err := runVariant(ctx, v, items, "ds2", out, false); err == nil || !strings.Contains(err.Error(), "dataset changed") {
		t.Errorf("changed dataset accepted: %v", err)
	}
	if err := runVariant(ctx, changed, items, "ds1", out, true); err != nil {
		t.Errorf("-force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "run.json")); err != nil {
		t.Error(err)
	}
}

func TestReportSmoke(t *testing.T) {
	dir := t.TempDir()
	for _, it := range []item{{Station: qpc.Station{Call: "K1A"}}, {Station: qpc.Station{Call: "K2B"}}, {Station: qpc.Station{Call: "K3C"}}} {
		appendJSONL(filepath.Join(dir, "dataset.jsonl"), it)
	}
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K1A", Label: qpc.Bureau})
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K2B", Label: qpc.Unclear, Via: "EA5GL"})
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K3C", Label: qpc.Unknown, Unsure: true})
	items, _ := loadDataset(filepath.Join(dir, "dataset.jsonl"))
	ds, _ := fileHash(filepath.Join(dir, "dataset.jsonl"))
	if err := runVariant(context.Background(), qpc.Variant{Name: "heuristic", Kind: "heuristic"}, items, ds, filepath.Join(dir, "runs", "heuristic"), false); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "report.md")
	if err := cmdReport([]string{"-dir", dir, "-out", out}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	for _, want := range []string{"evaluated on 2", "| heuristic | (rules) |", "| K2B | unclear via EA5GL | ✗ unknown |"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report lacks %q:\n%s", want, b)
		}
	}
}
