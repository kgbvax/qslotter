package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	bd := []qpc.Route{qpc.Bureau, qpc.Direct}
	ps := []pair{
		// entirely right
		{Call: "A", Gold: answerT{Status: qpc.Paper, Routes: bd}, Pred: answerT{Status: qpc.Paper, Routes: bd}, Conf: "high"},
		// status right, one route missing, a note only the gold has
		{Call: "B", Gold: answerT{Status: qpc.Paper, Routes: bd, Preferred: qpc.Direct, Note: "direct preferred"},
			Pred: answerT{Status: qpc.Paper, Routes: []qpc.Route{qpc.Bureau}}, Conf: "medium"},
		{Call: "C", Gold: answerT{Status: qpc.Unclear, Via: "EA5GL"}, Pred: answerT{Status: qpc.Unclear, Via: "EA5GL"}, Conf: "high"},
		{Call: "D", Gold: answerT{Status: qpc.Unknown}, Pred: answerT{Status: unreadable}},
	}
	m := computeMetrics(ps)
	if m.StatusAcc != 0.75 || m.RoutesAcc != 0.75 || m.PreferredAcc != 0.75 || m.ViaAcc != 1 || m.AllAcc != 0.5 || m.NoteAgree != 0.75 {
		t.Errorf("status %v routes %v pref %v via %v all %v notes %v", m.StatusAcc, m.RoutesAcc, m.PreferredAcc, m.ViaAcc, m.AllAcc, m.NoteAgree)
	}
	if m.Unreadable != 1 || m.HighCov != 0.5 || m.HighAcc != 1 || m.HighMedAcc != 2.0/3 {
		t.Errorf("unreadable %d high %v/%v highmed %v", m.Unreadable, m.HighCov, m.HighAcc, m.HighMedAcc)
	}
	// Route detection: bureau 2/2 found, direct found in 1 of 2 stations.
	for _, c := range m.PerRoute {
		switch c.Name {
		case "bureau":
			if c.Recall != 1 || c.Precision != 1 {
				t.Errorf("bureau %+v", c)
			}
		case "direct":
			if c.Recall != 0.5 || c.Precision != 1 {
				t.Errorf("direct %+v", c)
			}
		}
	}
	// kappa on status: po=.75; gold P:2 U:1 K:1, pred P:2 U:1 X:1 -> pe=(4+1)/16
	pe := 5.0 / 16
	if math.Abs(m.Kappa-(0.75-pe)/(1-pe)) > 1e-9 {
		t.Errorf("kappa %v", m.Kappa)
	}
	if m.Confusion[qpc.Unknown][unreadable] != 1 {
		t.Errorf("confusion %v", m.Confusion)
	}
	if got := (answerT{Status: qpc.Paper, Routes: bd, Preferred: qpc.Direct, Via: "EA5GL"}).String(); got != "bureau+direct* via EA5GL" {
		t.Errorf("answer string %q", got)
	}
}

func TestMapHeuristic(t *testing.T) {
	cases := []struct {
		r      qsldetermine.Result
		status qpc.Status
		routes string
		via    string
	}{
		{qsldetermine.Result{Method: "B", Reason: "QSL via bureau"}, qpc.Paper, "[bureau]", ""},
		{qsldetermine.Result{Method: "B", Reason: "bio: bureau and direct both accepted, bureau is cheaper"}, qpc.Paper, "[bureau direct]", ""},
		{qsldetermine.Result{Method: "D"}, qpc.Paper, "[direct]", ""},
		{qsldetermine.Result{Method: "M", Manager: "EA5GL", Reason: "qslmgr field: EA5GL"}, qpc.Unclear, "[]", "EA5GL"},
		{qsldetermine.Result{Method: "M", Manager: "K2ABC", Reason: "qslmgr field: K2ABC (bureau only)"}, qpc.Paper, "[bureau]", "K2ABC"},
		{qsldetermine.Result{Method: "M", Manager: "IQ3BM", Reason: "qslmgr field: IQ3BM via bureau or direct"}, qpc.Paper, "[bureau direct]", "IQ3BM"},
		{qsldetermine.Result{RefusePaper: true}, qpc.NoPaper, "[]", ""},
		{qsldetermine.Result{}, qpc.Unknown, "[]", ""},
	}
	for _, c := range cases {
		st, routes, via := mapHeuristic(c.r)
		if st != c.status || fmt.Sprint(routes) != c.routes || via != c.via {
			t.Errorf("%+v -> %s %v %q", c.r, st, routes, via)
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
	// A first-pass record (single label, note = the labeller's comment).
	appendJSONL(gold, map[string]any{"call": "K1A", "label": "bureau", "note": "check later", "at": "x"})
	items := []item{{Station: qpc.Station{Call: "K1A", Bio: "QSL via buro"}}, {Station: qpc.Station{Call: "K2B"}}}
	h, err := newLabelHandler(items, gold)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := loadGold(gold)
	if k := g["K1A"]; !k.Legacy || k.Status != qpc.Paper || fmt.Sprint(k.Routes) != "[bureau]" || k.Comment != "check later" || k.Note != "" {
		t.Errorf("first-pass record: %+v", k)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/label", strings.NewReader(body)))
		return rec.Code
	}
	if c := post(`{"call":"k1a","routes":["direct","bureau"],"preferred":"direct","note":" direct preferred ","via":" ea5gl "}`); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if c := post(`{"call":"K2B","status":"unclear"}`); c != 200 {
		t.Fatalf("status only: %d", c)
	}
	for body, why := range map[string]string{
		`{"call":"K2B","status":"paper"}`:                         "paper without routes",
		`{"call":"K2B"}`:                                          "nothing picked",
		`{"call":"K2B","routes":["bureau"],"preferred":"direct"}`: "preferred not among routes",
		`{"call":"K2B","routes":["email"]}`:                       "invalid route",
		`{"call":"ZZ9ZZ","status":"unknown"}`:                     "unknown station",
	} {
		if c := post(body); c != 400 {
			t.Errorf("%s: %d", why, c)
		}
	}
	g, _ = loadGold(gold)
	k := g["K1A"]
	if k.Legacy || k.Status != qpc.Paper || fmt.Sprint(k.Routes) != "[bureau direct]" || k.Preferred != qpc.Direct || k.Via != "EA5GL" || k.Note != "direct preferred" {
		t.Errorf("new label: %+v", k)
	}
	all, _ := readJSONL[goldLabel](gold)
	if len(all) != 3 {
		t.Errorf("append-only log: %d records", len(all))
	}

	// The page state never carries predictions.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var st map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &st)
	for k := range st {
		switch k {
		case "stations", "gold", "statuses", "routes", "rules":
		default:
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
	for _, it := range []item{{Station: qpc.Station{Call: "K1A"}}, {Station: qpc.Station{Call: "K2B"}}, {Station: qpc.Station{Call: "K3C"}}, {Station: qpc.Station{Call: "K4D"}}} {
		appendJSONL(filepath.Join(dir, "dataset.jsonl"), it)
	}
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K1A", Status: qpc.Paper, Routes: []qpc.Route{qpc.Bureau}})
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K2B", Status: qpc.Unclear, Via: "EA5GL", Note: "manager only"})
	appendJSONL(filepath.Join(dir, "gold.jsonl"), goldLabel{Call: "K3C", Status: qpc.Unknown, Unsure: true})
	appendJSONL(filepath.Join(dir, "gold.jsonl"), map[string]any{"call": "K4D", "label": "direct", "at": "x"}) // first pass
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
	for _, want := range []string{"1 first-pass only (left out); evaluated on 2", "| heuristic | (rules) |", "| K2B | unclear via EA5GL | ✗ unknown |", "With a note: 1."} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report lacks %q:\n%s", want, b)
		}
	}
	if err := cmdReport([]string{"-dir", dir, "-out", out, "-legacy"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "evaluated on 3") {
		t.Errorf("-legacy: %s", b)
	}
}

func TestNotesReview(t *testing.T) {
	gold := filepath.Join(t.TempDir(), "gold.jsonl")
	appendJSONL(gold, goldLabel{Call: "K1A", Status: qpc.Paper, Routes: []qpc.Route{qpc.Direct}, Comment: "mine", At: "x"})
	appendJSONL(gold, goldLabel{Call: "K2B", Status: qpc.Unknown, At: "x"})
	appendJSONL(gold, map[string]any{"call": "K3C", "label": "bureau", "at": "x"}) // first pass: not reviewed
	items := []item{{Station: qpc.Station{Call: "K1A"}}, {Station: qpc.Station{Call: "K2B"}}, {Station: qpc.Station{Call: "K3C"}}}
	results := map[string]qpc.Result{"K1A": {Call: "K1A", Note: "SAE + 2 USD"}, "K3C": {Call: "K3C", Note: "x"}}
	h, n, err := newNotesHandler(items, results, gold)
	if err != nil || n != 1 {
		t.Fatalf("items %d, %v (want only K1A: K2B has no note, K3C is first-pass)", n, err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/note", strings.NewReader(`{"call":"k1a","note":" SAE + 2 USD "}`)))
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	g, _ := loadGold(gold)
	k := g["K1A"]
	if k.Note != "SAE + 2 USD" || !k.NoteReviewed || k.Status != qpc.Paper || fmt.Sprint(k.Routes) != "[direct]" || k.Comment != "mine" {
		t.Errorf("reviewed label: %+v", k)
	}
}
