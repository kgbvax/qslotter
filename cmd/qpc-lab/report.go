package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/qpc"
)

// run is one variant's results, as read back for the report.
type run struct {
	Name    string
	Info    runInfo
	Results map[string]qpc.Result
	Pairs   []pair
	Missing int
	M       metrics
}

func cmdReport(args []string) error {
	fl := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	only := fl.String("only", "", "comma-separated variant names (default all runs)")
	out := fl.String("out", "", "Markdown report (default <dir>/report.md)")
	fl.Parse(args)
	if *out == "" {
		*out = filepath.Join(*dir, "report.md")
	}
	dsPath := filepath.Join(*dir, "dataset.jsonl")
	items, err := loadDataset(dsPath)
	if err != nil {
		return err
	}
	dsHash, _ := fileHash(dsPath)
	gold, err := loadGold(filepath.Join(*dir, "gold.jsonl"))
	if err != nil {
		return err
	}
	var eval []item
	unlabelled, unsure := 0, 0
	for _, it := range items {
		g, ok := gold[it.Call]
		switch {
		case !ok:
			unlabelled++
		case g.Unsure:
			unsure++
		default:
			eval = append(eval, it)
		}
	}
	if len(eval) == 0 {
		return errors.New("no labelled stations yet (qpc-lab label)")
	}

	runs, err := loadRuns(filepath.Join(*dir, "runs"), *only, dsHash)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return errors.New("no runs yet (qpc-lab run)")
	}
	for _, r := range runs {
		for _, it := range eval {
			res, ok := r.Results[it.Call]
			if !ok {
				r.Missing++
				continue
			}
			g := gold[it.Call]
			p := pair{Call: it.Call, Gold: g.Label, GoldVia: g.Via, Pred: res.Label, PredVia: res.Via,
				Conf: res.Confidence, Evidence: res.Evidence, LatencyMS: res.LatencyMS,
				Tokens: res.PromptTokens, Truncated: res.Truncated, CutOff: res.FinishReason == "length"}
			if p.Pred == "" {
				p.Pred = unreadable
			}
			r.Pairs = append(r.Pairs, p)
		}
		r.M = computeMetrics(r.Pairs)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].M.Both > runs[j].M.Both })

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	head := fmt.Sprintf("dataset %s (%d stations): %d labelled, %d unsure (left out), %d unlabelled; evaluated on %d.",
		dsHash, len(items), len(items)-unlabelled, unsure, unlabelled, len(eval))
	fmt.Fprintf(f, "# qpc report\n\nGenerated %s. %s\n\n", time.Now().Format("2006-01-02 15:04"), head)
	writeGoldDistribution(f, eval, gold)
	fmt.Fprint(f, "## Summary\n\n")
	writeSummary(f, runs)
	fmt.Fprintln(f, `
"label" = route label right; "via" = via callsign right (both empty counts as right); "both" = the whole answer right.
"high conf" = share of stations the model answered with high confidence, and its label accuracy there.`)
	for _, r := range runs {
		writeRunDetail(f, r)
	}
	writeDisagreements(f, runs, eval, gold)

	fmt.Println(head)
	writeSummary(os.Stdout, runs)
	fmt.Printf("\nfull report: %s\n", *out)
	return nil
}

func loadRuns(root, only, dsHash string) ([]*run, error) {
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []*run
	for _, e := range entries {
		if !e.IsDir() || (len(want) > 0 && !want[e.Name()]) {
			continue
		}
		r := &run{Name: e.Name()}
		if err := readJSON(filepath.Join(root, e.Name(), "run.json"), &r.Info); err != nil {
			return nil, fmt.Errorf("run %s: %w", e.Name(), err)
		}
		if r.Info.Dataset != dsHash {
			fmt.Fprintf(os.Stderr, "skipping run %s: made on another dataset (%s)\n", e.Name(), r.Info.Dataset)
			continue
		}
		if r.Results, err = lastResults(filepath.Join(root, e.Name(), "results.jsonl")); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func pct(x float64) string {
	if math.IsNaN(x) {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", 100*x)
}

func num(x float64) string {
	if math.IsNaN(x) {
		return "–"
	}
	return fmt.Sprintf("%.2f", x)
}

func writeGoldDistribution(w io.Writer, eval []item, gold map[string]goldLabel) {
	counts := map[qpc.Label]int{}
	vias := 0
	for _, it := range eval {
		g := gold[it.Call]
		counts[g.Label]++
		if g.Via != "" {
			vias++
		}
	}
	fmt.Fprintln(w, "## Manual labels\n\n| label | stations |\n|---|---:|")
	for _, l := range qpc.Labels {
		note := ""
		if c := counts[l]; c > 0 && c < 5 {
			note = " (few: per-class numbers are anecdotal)"
		}
		fmt.Fprintf(w, "| %s | %d%s |\n", l, counts[l], note)
	}
	fmt.Fprintf(w, "| *with a via callsign* | %d |\n\n", vias)
}

func writeSummary(w io.Writer, runs []*run) {
	fmt.Fprintln(w, "| variant | model | prompt | done | label | via | both | macro-F1 | κ | high conf (cov/acc) | unreadable | s mean/p95 | max tokens |")
	fmt.Fprintln(w, "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range runs {
		m := r.M
		model := r.Info.Variant.Model
		if r.Info.Variant.Kind == "heuristic" {
			model = "(rules)"
		}
		fmt.Fprintf(w, "| %s | %s | %s | %d/%d | %s | %s | %s | %s | %s | %s / %s | %d | %.1f / %.1f | %d |\n",
			r.Name, model, orDash(r.Info.PromptID), m.N, m.N+r.Missing, pct(m.LabelAcc), pct(m.ViaAcc), pct(m.Both),
			num(m.MacroF1), num(m.Kappa), pct(m.HighCov), pct(m.HighAcc), m.Unreadable,
			m.LatencyMean, m.LatencyP95, m.TokensMax)
	}
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func writeRunDetail(w io.Writer, r *run) {
	m := r.M
	v := r.Info.Variant
	fmt.Fprintf(w, "\n## %s\n\n", r.Name)
	if v.Kind == "heuristic" {
		fmt.Fprintln(w, "internal/qsldetermine mapped to qpc labels (never answers oqrs).")
	} else {
		temp := 0.0
		if v.Temperature != nil {
			temp = *v.Temperature
		}
		fmt.Fprintf(w, "`%s` at %s, prompt `%s`, format %s, temperature %g, bio cap %d chars, extra %v.\n",
			v.Model, v.BaseURL, r.Info.PromptID, v.Format, temp, v.BioMaxChars, v.Extra)
		fmt.Fprintf(w, "High+medium confidence: %s of stations, label accuracy %s. Bio cut: %d. Answer cut off (max_tokens): %d. Mean prompt tokens: %d.\n",
			pct(m.HighMedCov), pct(m.HighMedAcc), m.Truncated, m.CutOff, m.TokensMean)
	}
	if r.Missing > 0 {
		fmt.Fprintf(w, "\n**%d labelled stations have no result yet** (rerun `qpc-lab run`).\n", r.Missing)
	}
	fmt.Fprintln(w, "\n| label | gold | predicted | precision | recall | F1 |\n|---|---:|---:|---:|---:|---:|")
	for _, c := range m.PerClass {
		fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s |\n", c.Label, c.Support, c.Predicted, pct(c.Precision), pct(c.Recall), num(c.F1))
	}
	// Confusion matrix over the labels that occur.
	var cols []qpc.Label
	for _, l := range append(append([]qpc.Label{}, qpc.Labels...), unreadable) {
		for _, row := range m.Confusion {
			if row[l] > 0 || m.Confusion[l] != nil {
				cols = append(cols, l)
				break
			}
		}
	}
	fmt.Fprint(w, "\nConfusion (rows = your label, columns = prediction):\n\n| |")
	for _, c := range cols {
		fmt.Fprintf(w, " %s |", c)
	}
	fmt.Fprint(w, "\n|---|")
	for range cols {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	for _, g := range cols {
		row := m.Confusion[g]
		if row == nil {
			continue
		}
		fmt.Fprintf(w, "| **%s** |", g)
		for _, c := range cols {
			if n := row[c]; n > 0 {
				if c == g {
					fmt.Fprintf(w, " **%d** |", n)
				} else {
					fmt.Fprintf(w, " %d |", n)
				}
			} else {
				fmt.Fprint(w, " · |")
			}
		}
		fmt.Fprintln(w)
	}
}

func answer(l qpc.Label, via string) string {
	if via != "" {
		return string(l) + " via " + via
	}
	return string(l)
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// writeDisagreements lists every station some variant got wrong, with all
// answers side by side, then the evidence each wrong variant quoted.
func writeDisagreements(w io.Writer, runs []*run, eval []item, gold map[string]goldLabel) {
	fmt.Fprint(w, "\n## Disagreements\n\n✗ = differs from your label. Only stations at least one variant got wrong.\n\n")
	fmt.Fprint(w, "| station | your label |")
	for _, r := range runs {
		fmt.Fprintf(w, " %s |", r.Name)
	}
	fmt.Fprint(w, "\n|---|---|")
	for range runs {
		fmt.Fprint(w, "---|")
	}
	fmt.Fprintln(w)
	type wrong struct{ run, ans, evidence string }
	details := map[string][]wrong{}
	var order []string
	for _, it := range eval {
		g := gold[it.Call]
		var cells []string
		any := false
		for _, r := range runs {
			res, ok := r.Results[it.Call]
			if !ok {
				cells = append(cells, "–")
				continue
			}
			lbl := res.Label
			if lbl == "" {
				lbl = unreadable
			}
			a := answer(lbl, res.Via)
			if lbl != g.Label || res.Via != g.Via {
				any = true
				a = "✗ " + a
				ev := res.Evidence
				if res.ParseError != "" {
					ev = res.ParseError + ": " + res.Raw
				}
				details[it.Call] = append(details[it.Call], wrong{r.Name, a, ev})
			}
			cells = append(cells, a)
		}
		if !any {
			continue
		}
		order = append(order, it.Call)
		fmt.Fprintf(w, "| %s | %s |", it.Call, answer(g.Label, g.Via))
		for _, c := range cells {
			fmt.Fprintf(w, " %s |", cell(c))
		}
		fmt.Fprintln(w)
	}
	if len(order) == 0 {
		fmt.Fprintln(w, "\nNone.")
		return
	}
	fmt.Fprintln(w, "\n### Evidence quoted by the wrong answers")
	for _, call := range order {
		g := gold[call]
		fmt.Fprintf(w, "\n**%s** — yours: %s", call, answer(g.Label, g.Via))
		if g.Note != "" {
			fmt.Fprintf(w, " (note: %s)", cell(g.Note))
		}
		fmt.Fprintln(w)
		for _, d := range details[call] {
			fmt.Fprintf(w, "- %s: %s — “%s”\n", d.run, d.ans, cell(d.evidence))
		}
	}
}
