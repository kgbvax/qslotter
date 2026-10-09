package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dl9et/qslotter/qpc"
)

// unreadable stands for an answer without a valid status in the confusion
// matrix.
const unreadable = qpc.Status("(unreadable)")

// answerT is the comparable part of an answer: manual label or prediction.
type answerT struct {
	Status    qpc.Status
	Routes    []qpc.Route
	Preferred qpc.Route
	Via       string
	Note      string
	// Contribution is scored on its own, not as part of "all": only prompts
	// from v7 answer it.
	Contribution qpc.Contribution
}

// String is the compact form used in tables: "bureau+direct*" (* = preferred),
// a status otherwise, plus " via CALL".
func (a answerT) String() string {
	s := string(a.Status)
	if a.Status == qpc.Paper {
		var parts []string
		for _, r := range a.Routes {
			p := string(r)
			if r == a.Preferred {
				p += "*"
			}
			parts = append(parts, p)
		}
		s = strings.Join(parts, "+")
	}
	if a.Via != "" {
		s += " via " + a.Via
	}
	return s
}

// pair is one evaluated station: the manual label against a prediction.
type pair struct {
	Call       string
	Gold, Pred answerT
	Conf       string
	Evidence   string
	LatencyMS  int64
	Tokens     int
	Truncated  bool
	CutOff     bool // finish_reason "length"
	Guarded    bool // the address guard changed the model's answer
}

func sameRoutes(a, b []qpc.Route) bool {
	return fmt.Sprint(qpc.SortRoutes(a)) == fmt.Sprint(qpc.SortRoutes(b))
}

func (p pair) statusOK() bool    { return p.Gold.Status == p.Pred.Status }
func (p pair) routesOK() bool    { return sameRoutes(p.Gold.Routes, p.Pred.Routes) }
func (p pair) preferredOK() bool { return p.Gold.Preferred == p.Pred.Preferred }
func (p pair) viaOK() bool       { return p.Gold.Via == p.Pred.Via }
func (p pair) allOK() bool       { return p.statusOK() && p.routesOK() && p.preferredOK() && p.viaOK() }
func (p pair) noteAgrees() bool  { return (p.Gold.Note != "") == (p.Pred.Note != "") }
func (p pair) contribOK() bool   { return p.Gold.Contribution == p.Pred.Contribution }

// classStats is precision and recall for one status or route; NaN =
// undefined (never predicted / never in gold).
type classStats struct {
	Name                  string
	Support, Predicted    int
	TP                    int
	Precision, Recall, F1 float64
}

func newClassStats(name string, support, predicted, tp int) classStats {
	c := classStats{Name: name, Support: support, Predicted: predicted, TP: tp,
		Precision: ratio(tp, predicted), Recall: ratio(tp, support)}
	if tp > 0 {
		c.F1 = 2 * c.Precision * c.Recall / (c.Precision + c.Recall)
	}
	return c
}

type metrics struct {
	N                                  int
	StatusAcc, RoutesAcc, PreferredAcc float64
	ViaAcc, AllAcc, NoteAgree          float64
	StatusF1, Kappa                    float64 // over status
	Unreadable, Truncated, CutOff      int
	Guarded, GuardedOK                 int
	NotesGold, NotesPred, NotesBoth    int
	ContribAcc                         float64
	ContribRequired                    classStats // "required" found where the station asks for it
	PerStatus, PerRoute                []classStats
	Confusion                          map[qpc.Status]map[qpc.Status]int // gold -> pred -> count
	// Share of stations the model answered with that confidence or higher,
	// and how many of those answers are entirely right.
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
	m := metrics{N: len(ps), Confusion: map[qpc.Status]map[qpc.Status]int{}}
	if m.N == 0 {
		return m
	}
	var status, routes, pref, via, all, notes, contrib, high, highOK, hm, hmOK, tokSum int
	var crSup, crPred, crTP int
	var lat []float64
	count := func(ok bool, n *int) {
		if ok {
			*n++
		}
	}
	for _, p := range ps {
		count(p.statusOK(), &status)
		count(p.routesOK(), &routes)
		count(p.preferredOK(), &pref)
		count(p.viaOK(), &via)
		count(p.allOK(), &all)
		count(p.noteAgrees(), &notes)
		count(p.contribOK(), &contrib)
		count(p.Gold.Contribution == qpc.ContributionRequired, &crSup)
		count(p.Pred.Contribution == qpc.ContributionRequired, &crPred)
		count(p.Gold.Contribution == qpc.ContributionRequired && p.Pred.Contribution == qpc.ContributionRequired, &crTP)
		count(p.Pred.Status == unreadable, &m.Unreadable)
		count(p.Truncated, &m.Truncated)
		count(p.CutOff, &m.CutOff)
		count(p.Guarded, &m.Guarded)
		count(p.Guarded && p.allOK(), &m.GuardedOK)
		count(p.Gold.Note != "", &m.NotesGold)
		count(p.Pred.Note != "", &m.NotesPred)
		count(p.Gold.Note != "" && p.Pred.Note != "", &m.NotesBoth)
		if p.Conf == "high" {
			high++
			count(p.allOK(), &highOK)
		}
		if p.Conf == "high" || p.Conf == "medium" {
			hm++
			count(p.allOK(), &hmOK)
		}
		if m.Confusion[p.Gold.Status] == nil {
			m.Confusion[p.Gold.Status] = map[qpc.Status]int{}
		}
		m.Confusion[p.Gold.Status][p.Pred.Status]++
		lat = append(lat, float64(p.LatencyMS)/1000)
		tokSum += p.Tokens
		if p.Tokens > m.TokensMax {
			m.TokensMax = p.Tokens
		}
	}
	m.StatusAcc, m.RoutesAcc, m.PreferredAcc = ratio(status, m.N), ratio(routes, m.N), ratio(pref, m.N)
	m.ViaAcc, m.AllAcc, m.NoteAgree = ratio(via, m.N), ratio(all, m.N), ratio(notes, m.N)
	m.ContribAcc = ratio(contrib, m.N)
	m.ContribRequired = newClassStats("required", crSup, crPred, crTP)
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

	// Status: per class and macro-F1 over the statuses that occur.
	var f1Sum float64
	for _, s := range qpc.Statuses {
		var sup, pred, tp int
		for _, p := range ps {
			count(p.Gold.Status == s, &sup)
			count(p.Pred.Status == s, &pred)
			count(p.Gold.Status == s && p.Pred.Status == s, &tp)
		}
		if sup == 0 && pred == 0 {
			continue
		}
		c := newClassStats(string(s), sup, pred, tp)
		f1Sum += c.F1
		m.PerStatus = append(m.PerStatus, c)
	}
	m.StatusF1 = f1Sum / float64(len(m.PerStatus))
	// Routes: is each route found where the station accepts it?
	for _, r := range qpc.AllRoutes {
		var sup, pred, tp int
		for _, p := range ps {
			g, q := qpc.HasRoute(p.Gold.Routes, r), qpc.HasRoute(p.Pred.Routes, r)
			count(g, &sup)
			count(q, &pred)
			count(g && q, &tp)
		}
		m.PerRoute = append(m.PerRoute, newClassStats(string(r), sup, pred, tp))
	}
	m.Kappa = cohenKappa(ps)
	return m
}

// cohenKappa is the agreement on status between gold and prediction beyond
// chance.
func cohenKappa(ps []pair) float64 {
	n := float64(len(ps))
	gold, pred := map[qpc.Status]float64{}, map[qpc.Status]float64{}
	var agree float64
	for _, p := range ps {
		gold[p.Gold.Status]++
		pred[p.Pred.Status]++
		if p.statusOK() {
			agree++
		}
	}
	po := agree / n
	var pe float64
	for s, g := range gold {
		pe += (g / n) * (pred[s] / n)
	}
	if pe == 1 {
		return math.NaN()
	}
	return (po - pe) / (1 - pe)
}
