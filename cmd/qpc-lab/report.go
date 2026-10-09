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
	legacy := fl.Bool("legacy", false, "also evaluate first-pass labels (single label, no also-routes or note)")
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
	unlabelled, unsure, firstPass := 0, 0, 0
	for _, it := range items {
		g, ok := gold[it.Call]
		switch {
		case !ok:
			unlabelled++
		case g.Unsure:
			unsure++
		case g.Legacy && !*legacy:
			firstPass++
		default:
			eval = append(eval, it)
		}
	}
	if len(eval) == 0 {
		return errors.New("no stations labelled in the routes scheme yet (qpc-lab label; -legacy uses first-pass labels)")
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
			p := pair{Call: it.Call,
				Gold: answerT{g.Status, g.Routes, g.Preferred, g.Via, g.Note, g.Contribution},
				Pred: answerT{res.Status, res.Routes, res.Preferred, res.Via, res.Note, res.Contribution},
				Conf: res.Confidence, Evidence: res.Evidence, LatencyMS: res.LatencyMS,
				Tokens: res.PromptTokens, Truncated: res.Truncated, CutOff: res.FinishReason == "length",
				Guarded: res.Guard != ""}
			if p.Pred.Status == "" {
				p.Pred.Status = unreadable
			}
			r.Pairs = append(r.Pairs, p)
		}
		r.M = computeMetrics(r.Pairs)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].M.AllAcc > runs[j].M.AllAcc })

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	head := fmt.Sprintf("dataset %s (%d stations): %d unlabelled, %d unsure, %d first-pass only (left out); evaluated on %d.",
		dsHash, len(items), unlabelled, unsure, firstPass, len(eval))
	if *legacy {
		head += " First-pass labels included: they have one route at most and no note."
	}
	fmt.Fprintf(f, "# qpc report\n\nGenerated %s. %s\n\n", time.Now().Format("2006-01-02 15:04"), head)
	writeGoldDistribution(f, eval, gold)
	fmt.Fprint(f, "## Summary\n\n")
	writeSummary(f, runs)
	fmt.Fprintln(f, `
"status" = paper / no-paper / unknown / unclear right; "routes" = the whole set of accepted routes right; "pref" = preferred route right (both empty counts as right); "via" likewise; "all" = status, routes, pref and via all right.
"notes" = both or neither have a note (the wording is compared by reading, below). "contrib" = the contribution flag right (required / not-needed / not stated), with precision/recall for "required"; not part of "all". "high conf" = share answered with high confidence, and how many of those are entirely right.
Single-label prompts (v1, v2) give one route at most and no note.`)
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
	statuses, routes, combos := map[qpc.Status]int{}, map[qpc.Route]int{}, map[string]int{}
	var pref, vias, notes int
	for _, it := range eval {
		g := gold[it.Call]
		statuses[g.Status]++
		for _, r := range g.Routes {
			routes[r]++
		}
		if g.Status == qpc.Paper {
			combos[answerT{Status: g.Status, Routes: g.Routes}.String()]++
		}
		if g.Preferred != "" {
			pref++
		}
		if g.Via != "" {
			vias++
		}
		if g.Note != "" {
			notes++
		}
	}
	fmt.Fprintln(w, "## Manual labels\n\n| status | stations |\n|---|---:|")
	for _, s := range qpc.Statuses {
		note := ""
		if c := statuses[s]; c > 0 && c < 5 {
			note = " (few: per-class numbers are anecdotal)"
		}
		fmt.Fprintf(w, "| %s | %d%s |\n", s, statuses[s], note)
	}
	fmt.Fprintln(w, "\n| paper routes | stations |\n|---|---:|")
	keys := make([]string, 0, len(combos))
	for k := range combos {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return combos[keys[i]] > combos[keys[j]] })
	for _, k := range keys {
		fmt.Fprintf(w, "| %s | %d |\n", k, combos[k])
	}
	contrib := map[qpc.Contribution]int{}
	for _, it := range eval {
		contrib[gold[it.Call].Contribution]++
	}
	fmt.Fprintf(w, "\nWith a preferred route: %d. With a via callsign: %d. With a note: %d. Contribution required: %d, not needed: %d.\n\n",
		pref, vias, notes, contrib[qpc.ContributionRequired], contrib[qpc.ContributionNotNeeded])
}

func writeSummary(w io.Writer, runs []*run) {
	fmt.Fprintln(w, "| variant | model | prompt | done | status | routes | pref | via | **all** | κ | notes | contrib (P/R) | high conf (cov/all) | unreadable | s mean/p95 |")
	fmt.Fprintln(w, "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range runs {
		m := r.M
		model := r.Info.Variant.Model
		if r.Info.Variant.Kind == "heuristic" {
			model = "(rules)"
		}
		fmt.Fprintf(w, "| %s | %s | %s | %d/%d | %s | %s | %s | %s | **%s** | %s | %s | %s (%s/%s) | %s / %s | %d | %.1f / %.1f |\n",
			r.Name, model, orDash(r.Info.PromptID), m.N, m.N+r.Missing, pct(m.StatusAcc), pct(m.RoutesAcc),
			pct(m.PreferredAcc), pct(m.ViaAcc), pct(m.AllAcc), num(m.Kappa), pct(m.NoteAgree),
			pct(m.ContribAcc), pct(m.ContribRequired.Precision), pct(m.ContribRequired.Recall),
			pct(m.HighCov), pct(m.HighAcc), m.Unreadable, m.LatencyMean, m.LatencyP95)
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
	switch v.Kind {
	case "heuristic":
		fmt.Fprintln(w, "internal/qsldetermine mapped to qpc answers (no note).")
	default:
		temp := 0.0
		if v.Temperature != nil {
			temp = *v.Temperature
		}
		fmt.Fprintf(w, "`%s` at %s, prompt `%s`, format %s, temperature %g, bio cap %d chars, extra %v.\n",
			v.Model, v.BaseURL, r.Info.PromptID, v.Format, temp, v.BioMaxChars, v.Extra)
		fmt.Fprintf(w, "High+medium confidence: %s of stations, %s of them entirely right. Bio cut: %d. Answer cut off (max_tokens): %d. Prompt tokens mean/max: %d/%d.\n",
			pct(m.HighMedCov), pct(m.HighMedAcc), m.Truncated, m.CutOff, m.TokensMean, m.TokensMax)
		if v.AddressGuard {
			fmt.Fprintf(w, "Address guard changed %d answers; %d of them are now entirely right.\n", m.Guarded, m.GuardedOK)
		}
	}
	if r.Missing > 0 {
		fmt.Fprintf(w, "\n**%d labelled stations have no result yet** (rerun `qpc-lab run`).\n", r.Missing)
	}
	fmt.Fprintf(w, "\nStatus macro-F1 %s.\n\n| status | gold | predicted | precision | recall | F1 |\n|---|---:|---:|---:|---:|---:|\n", num(m.StatusF1))
	for _, c := range m.PerStatus {
		fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s |\n", c.Name, c.Support, c.Predicted, pct(c.Precision), pct(c.Recall), num(c.F1))
	}
	fmt.Fprintln(w, "\n| route found | gold | predicted | precision | recall | F1 |\n|---|---:|---:|---:|---:|---:|")
	for _, c := range m.PerRoute {
		fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s |\n", c.Name, c.Support, c.Predicted, pct(c.Precision), pct(c.Recall), num(c.F1))
	}
	// Status confusion over the statuses that occur.
	var cols []qpc.Status
	for _, s := range append(append([]qpc.Status{}, qpc.Statuses...), unreadable) {
		for _, row := range m.Confusion {
			if row[s] > 0 || m.Confusion[s] != nil {
				cols = append(cols, s)
				break
			}
		}
	}
	fmt.Fprint(w, "\nStatus confusion (rows = your label, columns = prediction):\n\n| |")
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
			switch n := row[c]; {
			case n > 0 && c == g:
				fmt.Fprintf(w, " **%d** |", n)
			case n > 0:
				fmt.Fprintf(w, " %d |", n)
			default:
				fmt.Fprint(w, " · |")
			}
		}
		fmt.Fprintln(w)
	}
	if v.Kind != "heuristic" && (m.NotesGold > 0 || m.NotesPred > 0) {
		fmt.Fprintf(w, "\nNotes: yours %d, the model's %d, both %d.\n\n| station | your note | model's note |\n|---|---|---|\n", m.NotesGold, m.NotesPred, m.NotesBoth)
		for _, p := range r.Pairs {
			if p.Gold.Note != "" || p.Pred.Note != "" {
				fmt.Fprintf(w, "| %s | %s | %s |\n", p.Call, orDash(cell(p.Gold.Note)), orDash(cell(p.Pred.Note)))
			}
		}
	}
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// writeDisagreements lists every station some variant got wrong (status,
// routes, preferred or via), with all answers side by side, then the
// evidence each wrong variant quoted.
func writeDisagreements(w io.Writer, runs []*run, eval []item, gold map[string]goldLabel) {
	fmt.Fprint(w, "\n## Disagreements\n\n✗ = differs from your label (status, routes, preferred or via; * = preferred). Only stations at least one variant got wrong.\n\n")
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
		ga := answerT{g.Status, g.Routes, g.Preferred, g.Via, g.Note, g.Contribution}
		var cells []string
		any := false
		for _, r := range runs {
			var p *pair
			for i := range r.Pairs {
				if r.Pairs[i].Call == it.Call {
					p = &r.Pairs[i]
				}
			}
			if p == nil {
				cells = append(cells, "–")
				continue
			}
			a := p.Pred.String()
			if !p.allOK() {
				any = true
				a = "✗ " + a
				res := r.Results[it.Call]
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
		fmt.Fprintf(w, "| %s | %s |", it.Call, ga)
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
		fmt.Fprintf(w, "\n**%s** — yours: %s", call, answerT{g.Status, g.Routes, g.Preferred, g.Via, g.Note, g.Contribution})
		if g.Note != "" {
			fmt.Fprintf(w, " (note: %s)", cell(g.Note))
		}
		if g.Comment != "" {
			fmt.Fprintf(w, " (comment: %s)", cell(g.Comment))
		}
		fmt.Fprintln(w)
		for _, d := range details[call] {
			fmt.Fprintf(w, "- %s: %s — “%s”\n", d.run, d.ans, cell(d.evidence))
		}
	}
}
