package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dl9et/qslotter/qpc"
)

// fit: does a decision layer fitted on labelled answers beat the hand cuts
// of a decision spec? A decision model's probabilities are features; for
// every noul question a logistic regression over all the probabilities and
// the code facts (full address, mqsl, DCL) replaces "p >= cut". The rest of
// the composition (address guard, flag rule, DCL rule, preference rule) is
// the spec's own, through qpc.Decision.Compose, so the two score on the
// same footing. Leave-one-sample-out: fitted on all other samples, scored
// on the held-out one; the held-out numbers are the ones that count.
//
// -via-from <run> splices another run's via callsign and "unclear" verdict
// into both (a decision model cannot extract a callsign): how much of the
// gap is extraction.

type fitStation struct {
	dir   string
	item  item
	gold  goldLabel
	res   qpc.Result
	x     []float64 // features
	nouls map[string]float64
	via   *qpc.Result // the -via-from run's answer
}

var fitNouls = []string{"refused", "bureau", "direct", "oqrs"}

func cmdFit(args []string) error {
	fl := flag.NewFlagSet("fit", flag.ExitOnError)
	dirs := fl.String("dirs", "", "comma-separated labelled working directories that have the run")
	runName := fl.String("run", "", "decision run to fit (its results carry the raw answers)")
	viaFrom := fl.String("via-from", "", "run whose via callsign and unclear verdict are spliced in (e.g. heuristic)")
	out := fl.String("out", "", "Markdown report (default <first dir>/fit-<run>.md)")
	fl.Parse(args)
	if *dirs == "" || *runName == "" {
		return fmt.Errorf("fit: -dirs and -run are required")
	}
	dirList := strings.Split(*dirs, ",")
	if *out == "" {
		*out = filepath.Join(dirList[0], "fit-"+*runName+".md")
	}
	var (
		all  []fitStation
		spec *qpc.Decision
	)
	for _, dir := range dirList {
		dir = strings.TrimSpace(dir)
		dsPath := filepath.Join(dir, "dataset.jsonl")
		items, err := loadDataset(dsPath)
		if err != nil {
			return err
		}
		dsHash, _ := fileHash(dsPath)
		gold, err := loadGold(filepath.Join(dir, "gold.jsonl"))
		if err != nil {
			return err
		}
		only := *runName
		if *viaFrom != "" {
			only += "," + *viaFrom
		}
		runs, err := loadRuns(filepath.Join(dir, "runs"), only, dsHash)
		if err != nil {
			return err
		}
		var r, v *run
		for _, x := range runs {
			switch x.Name {
			case *runName:
				r = x
			case *viaFrom:
				v = x
			}
		}
		if r == nil {
			return fmt.Errorf("%s: no run %s", dir, *runName)
		}
		if *viaFrom != "" && v == nil {
			return fmt.Errorf("%s: no run %s", dir, *viaFrom)
		}
		if spec == nil {
			name, _, _ := strings.Cut(r.Info.PromptID, "@")
			if spec, err = qpc.LoadDecision(name); err != nil {
				return err
			}
			if spec.ID() != r.Info.PromptID {
				fmt.Fprintf(os.Stderr, "note: the run used %s, the binary has %s\n", r.Info.PromptID, spec.ID())
			}
		}
		for _, it := range items {
			g, ok := gold[it.Call]
			res, okR := r.Results[it.Call]
			if !ok || g.Unsure || g.Legacy || !okR || res.Raw == "" {
				continue
			}
			nouls, choices, err := spec.Probabilities([]byte(res.Raw))
			if err != nil {
				return fmt.Errorf("%s %s: %w", dir, it.Call, err)
			}
			st := fitStation{dir: dir, item: it, gold: g, res: res, nouls: nouls, x: features(it.Station, nouls, choices)}
			if v != nil {
				if vr, ok := v.Results[it.Call]; ok {
					st.via = &vr
				}
			}
			all = append(all, st)
		}
	}
	if len(all) == 0 {
		return fmt.Errorf("no labelled stations with raw answers")
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := io.MultiWriter(f, os.Stdout)
	fmt.Fprintf(w, "# Fitted decision layer for %s (spec %s)\n\n", *runName, spec.ID())
	fmt.Fprintf(w, "%d labelled stations from %s. Features: %s.\n\n", len(all), strings.Join(dirList, ", "), strings.Join(featureNames, ", "))
	fmt.Fprintln(w, "\"all\" = status, routes, preferred route and via right. \"spec cuts\" = the stored answers composed with the spec's thresholds (what the run reported); \"fitted\" = the same answers with a logistic layer fitted on the other samples replacing every noul cut.")
	if *viaFrom != "" {
		fmt.Fprintf(w, " \"+via\" = with the via callsign and unclear verdict of run %s spliced in.", *viaFrom)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "\n| held out | n | spec cuts | fitted |"+map[bool]string{true: " spec cuts +via | fitted +via |", false: ""}[*viaFrom != ""])
	fmt.Fprintln(w, "|---|---:|---:|---:|"+map[bool]string{true: "---:|---:|", false: ""}[*viaFrom != ""])
	var tot [4]int
	for _, dir := range dirList {
		var train, test []fitStation
		for _, s := range all {
			if s.dir == dir {
				test = append(test, s)
			} else {
				train = append(train, s)
			}
		}
		if len(test) == 0 {
			continue
		}
		models := map[string][]float64{}
		for _, q := range fitNouls {
			models[q] = logistic(train, q)
		}
		var ok [4]int
		for _, s := range test {
			base, err := spec.Compose([]byte(s.res.Raw), s.item.Station, true, nil)
			if err != nil {
				return err
			}
			fitted, err := spec.WithThresholds(nil).Compose([]byte(s.res.Raw), s.item.Station, true, predict(models, s.x))
			if err != nil {
				return err
			}
			g := answerT{s.gold.Status, s.gold.Routes, s.gold.Preferred, s.gold.Via, s.gold.Note, s.gold.Contribution}
			for i, r := range []qpc.Result{base, fitted, splice(base, s.via), splice(fitted, s.via)} {
				if (pair{Gold: g, Pred: answerOf(r)}).allOK() {
					ok[i]++
					tot[i]++
				}
			}
		}
		row := fmt.Sprintf("| %s | %d | %s | %s |", filepath.Base(dir), len(test), pct(ratio(ok[0], len(test))), pct(ratio(ok[1], len(test))))
		if *viaFrom != "" {
			row += fmt.Sprintf(" %s | %s |", pct(ratio(ok[2], len(test))), pct(ratio(ok[3], len(test))))
		}
		fmt.Fprintln(w, row)
	}
	row := fmt.Sprintf("| **all held out** | %d | **%s** | **%s** |", len(all), pct(ratio(tot[0], len(all))), pct(ratio(tot[1], len(all))))
	if *viaFrom != "" {
		row += fmt.Sprintf(" **%s** | **%s** |", pct(ratio(tot[2], len(all))), pct(ratio(tot[3], len(all))))
	}
	fmt.Fprintln(w, row)

	// The weights fitted on everything: what the layer looks at.
	fmt.Fprint(w, "\n## Weights (fitted on all stations)\n\n")
	fmt.Fprintln(w, "| question | "+strings.Join(featureNames, " | ")+" |")
	fmt.Fprintln(w, "|---|"+strings.Repeat("---:|", len(featureNames)))
	for _, q := range fitNouls {
		wts := logistic(all, q)
		cells := make([]string, len(wts))
		for i, v := range wts {
			cells[i] = fmt.Sprintf("%.1f", v)
		}
		fmt.Fprintf(w, "| %s | %s |\n", q, strings.Join(cells, " | "))
	}
	fmt.Fprintf(os.Stderr, "\nfull report: %s\n", *out)
	return nil
}

var featureNames = []string{"bias", "refused", "bureau", "direct", "oqrs", "pref none", "pref bureau", "pref direct", "pref oqrs", "contrib required", "full address", "mqsl 1", "mqsl 0", "dcl"}

var fitDCL = regexp.MustCompile(`(?i)\bdcl\b|darc community log`)

// features is the input of the fitted layer: every probability the model
// gave plus the facts code knows.
func features(st qpc.Station, nouls map[string]float64, choices map[string]map[string]float64) []float64 {
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	return []float64{1,
		nouls["refused"], nouls["bureau"], nouls["direct"], nouls["oqrs"],
		choices["preferred"]["none"], choices["preferred"]["bureau"], choices["preferred"]["direct"], choices["preferred"]["oqrs"],
		choices["contribution"]["required"],
		b(st.HasFullAddress()), b(strings.TrimSpace(st.MQSL) == "1"), b(strings.TrimSpace(st.MQSL) == "0"),
		b(fitDCL.MatchString(st.QSLMgr + " " + st.Bio)),
	}
}

// target is what the gold label says for noul question q.
func target(g goldLabel, q string) float64 {
	if q == "refused" {
		if g.Status == qpc.NoPaper {
			return 1
		}
		return 0
	}
	if qpc.HasRoute(g.Routes, qpc.Route(q)) {
		return 1
	}
	return 0
}

// logistic fits a logistic regression of target q on the features by
// gradient descent with a little L2 (deterministic, no dependencies; the
// problem is tiny).
func logistic(train []fitStation, q string) []float64 {
	n := len(train[0].x)
	w := make([]float64, n)
	const (
		iters = 3000
		lr    = 0.3
		l2    = 1e-3
	)
	grad := make([]float64, n)
	for it := 0; it < iters; it++ {
		for i := range grad {
			grad[i] = l2 * w[i]
		}
		for _, s := range train {
			e := sigmoid(dot(w, s.x)) - target(s.gold, q)
			for i, x := range s.x {
				grad[i] += e * x / float64(len(train))
			}
		}
		for i := range w {
			w[i] -= lr * grad[i]
		}
	}
	return w
}

func predict(models map[string][]float64, x []float64) map[string]float64 {
	out := map[string]float64{}
	keys := make([]string, 0, len(models))
	for q := range models {
		keys = append(keys, q)
	}
	sort.Strings(keys)
	for _, q := range keys {
		out[q] = sigmoid(dot(models[q], x))
	}
	return out
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// splice puts the via callsign and the unclear verdict of another run's
// answer into r: what a decision model cannot extract.
func splice(r qpc.Result, via *qpc.Result) qpc.Result {
	if via == nil {
		return r
	}
	if via.Via != "" {
		r.Via = via.Via
	}
	if via.Status == qpc.Unclear {
		r.Status, r.Routes, r.Preferred = qpc.Unclear, nil, ""
	}
	return r
}
