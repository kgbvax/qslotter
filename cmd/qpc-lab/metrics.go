package main

import (
	"math"
	"sort"

	"github.com/dl9et/qslotter/qpc"
)

// unreadable stands for a prediction without a valid label in the confusion
// matrix.
const unreadable = qpc.Label("(unreadable)")

// pair is one evaluated station: the manual label against a prediction.
type pair struct {
	Call      string
	Gold      qpc.Label
	GoldVia   string
	Pred      qpc.Label // unreadable if the model's answer had no valid label
	PredVia   string
	Conf      string
	Evidence  string
	LatencyMS int64
	Tokens    int
	Truncated bool
	CutOff    bool // finish_reason "length"
	Guarded   bool // the address guard changed the model's label
}

func (p pair) labelOK() bool { return p.Gold == p.Pred }
func (p pair) viaOK() bool   { return p.GoldVia == p.PredVia }

// classStats is one label's precision and recall; NaN = undefined (the label
// was never predicted / never in gold).
type classStats struct {
	Label                 qpc.Label
	Support, Predicted    int
	TP                    int
	Precision, Recall, F1 float64
}

type metrics struct {
	N                      int
	LabelAcc, ViaAcc, Both float64
	MacroF1, Kappa         float64
	Unreadable, Truncated  int
	CutOff                 int
	Guarded, GuardedOK     int
	PerClass               []classStats
	Confusion              map[qpc.Label]map[qpc.Label]int // gold -> pred -> count
	// Confidence: share of stations answered with that confidence or higher,
	// and the label accuracy on them.
	HighCov, HighAcc        float64
	HighMedCov, HighMedAcc  float64
	LatencyMean, LatencyP95 float64 // seconds
	TokensMax, TokensMean   int
}

func ratio(a, b int) float64 {
	if b == 0 {
		return math.NaN()
	}
	return float64(a) / float64(b)
}

func computeMetrics(ps []pair) metrics {
	m := metrics{N: len(ps), Confusion: map[qpc.Label]map[qpc.Label]int{}}
	if m.N == 0 {
		return m
	}
	var labelOK, viaOK, both, high, highOK, hm, hmOK, tokSum int
	var lat []float64
	for _, p := range ps {
		if p.labelOK() {
			labelOK++
		}
		if p.viaOK() {
			viaOK++
		}
		if p.labelOK() && p.viaOK() {
			both++
		}
		if p.Pred == unreadable {
			m.Unreadable++
		}
		if p.Truncated {
			m.Truncated++
		}
		if p.CutOff {
			m.CutOff++
		}
		if p.Guarded {
			m.Guarded++
			if p.labelOK() {
				m.GuardedOK++
			}
		}
		if p.Conf == "high" {
			high++
			if p.labelOK() {
				highOK++
			}
		}
		if p.Conf == "high" || p.Conf == "medium" {
			hm++
			if p.labelOK() {
				hmOK++
			}
		}
		if m.Confusion[p.Gold] == nil {
			m.Confusion[p.Gold] = map[qpc.Label]int{}
		}
		m.Confusion[p.Gold][p.Pred]++
		lat = append(lat, float64(p.LatencyMS)/1000)
		tokSum += p.Tokens
		if p.Tokens > m.TokensMax {
			m.TokensMax = p.Tokens
		}
	}
	m.LabelAcc, m.ViaAcc, m.Both = ratio(labelOK, m.N), ratio(viaOK, m.N), ratio(both, m.N)
	m.HighCov, m.HighAcc = ratio(high, m.N), ratio(highOK, high)
	m.HighMedCov, m.HighMedAcc = ratio(hm, m.N), ratio(hmOK, hm)
	m.TokensMean = tokSum / m.N
	sort.Float64s(lat)
	var sum float64
	for _, x := range lat {
		sum += x
	}
	m.LatencyMean = sum / float64(len(lat))
	m.LatencyP95 = lat[int(math.Ceil(0.95*float64(len(lat))))-1]

	// Per class and macro-F1 over the labels that occur in gold or prediction.
	var f1Sum float64
	var f1N int
	for _, l := range qpc.Labels {
		cs := classStats{Label: l}
		for _, p := range ps {
			if p.Gold == l {
				cs.Support++
			}
			if p.Pred == l {
				cs.Predicted++
			}
			if p.Gold == l && p.Pred == l {
				cs.TP++
			}
		}
		if cs.Support == 0 && cs.Predicted == 0 {
			continue
		}
		cs.Precision, cs.Recall = ratio(cs.TP, cs.Predicted), ratio(cs.TP, cs.Support)
		if cs.TP > 0 {
			cs.F1 = 2 * cs.Precision * cs.Recall / (cs.Precision + cs.Recall)
		}
		f1Sum += cs.F1
		f1N++
		m.PerClass = append(m.PerClass, cs)
	}
	m.MacroF1 = f1Sum / float64(f1N)
	m.Kappa = cohenKappa(ps)
	return m
}

// cohenKappa is the agreement between gold and prediction beyond chance.
func cohenKappa(ps []pair) float64 {
	n := float64(len(ps))
	gold, pred := map[qpc.Label]float64{}, map[qpc.Label]float64{}
	var agree float64
	for _, p := range ps {
		gold[p.Gold]++
		pred[p.Pred]++
		if p.labelOK() {
			agree++
		}
	}
	po := agree / n
	var pe float64
	for l, g := range gold {
		pe += (g / n) * (pred[l] / n)
	}
	if pe == 1 {
		return math.NaN()
	}
	return (po - pe) / (1 - pe)
}
