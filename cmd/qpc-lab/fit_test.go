package main

import (
	"testing"

	"github.com/dl9et/qslotter/qpc"
)

// The fitted layer learns a cut that the raw probability hides: here
// "direct" is only right when the probability is above 0.8.
func TestLogisticLearnsCut(t *testing.T) {
	var train []fitStation
	for i := 0; i < 40; i++ {
		p := float64(i) / 40
		g := goldLabel{}
		if p >= 0.8 {
			g.Routes = []qpc.Route{qpc.Direct}
		}
		train = append(train, fitStation{gold: g, x: features(qpc.Station{}, map[string]float64{"direct": p}, nil)})
	}
	w := logistic(train, "direct")
	for p, want := range map[float64]bool{0.2: false, 0.7: false, 0.9: true} {
		got := predict(map[string][]float64{"direct": w}, features(qpc.Station{}, map[string]float64{"direct": p}, nil))["direct"] >= 0.5
		if got != want {
			t.Errorf("p=%.1f: direct %v, want %v", p, got, want)
		}
	}
}

func TestSplice(t *testing.T) {
	r := qpc.Result{Status: qpc.Paper, Routes: []qpc.Route{qpc.Bureau}, Preferred: qpc.Bureau}
	if s := splice(r, nil); s.Via != "" || s.Status != qpc.Paper {
		t.Errorf("nil: %+v", s)
	}
	if s := splice(r, &qpc.Result{Status: qpc.Paper, Via: "DL1ABC"}); s.Via != "DL1ABC" || len(s.Routes) != 1 {
		t.Errorf("via: %+v", s)
	}
	if s := splice(r, &qpc.Result{Status: qpc.Unclear}); s.Status != qpc.Unclear || s.Routes != nil || s.Preferred != "" {
		t.Errorf("unclear: %+v", s)
	}
}
