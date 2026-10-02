package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/qpc"
)

// cmdCompare sets two runs side by side on the same stations, without manual
// labels: how each answers, how often they agree and where they differ. With
// -calibrate it first measures, on labelled samples where both runs exist,
// how often agreement and disagreement went with right answers, which says
// what the unlabelled differences are likely worth.
func cmdCompare(args []string) error {
	fl := flag.NewFlagSet("compare", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	aName := fl.String("a", "heuristic", "first run (a directory under <dir>/runs)")
	bName := fl.String("b", "qwen3.5-4b-v7", "second run")
	calib := fl.String("calibrate", "", "comma-separated labelled working directories that have both runs")
	out := fl.String("out", "", "Markdown report (default <dir>/compare.md)")
	fl.Parse(args)
	if *out == "" {
		*out = filepath.Join(*dir, "compare.md")
	}
	dsPath := filepath.Join(*dir, "dataset.jsonl")
	items, err := loadDataset(dsPath)
	if err != nil {
		return err
	}
	dsHash, _ := fileHash(dsPath)
	a, b, err := loadTwoRuns(*dir, dsHash, *aName, *bName)
	if err != nil {
		return err
	}
	// pair.Gold is run a here, pair.Pred run b: the "OK" methods mean agree.
	var ps []pair
	byCall := map[string]item{}
	missing := 0
	for _, it := range items {
		ra, okA := a.Results[it.Call]
		rb, okB := b.Results[it.Call]
		if !okA || !okB {
			missing++
			continue
		}
		byCall[it.Call] = it
		ps = append(ps, pair{Call: it.Call, Gold: answerOf(ra), Pred: answerOf(rb),
			Conf: rb.Confidence, Evidence: rb.Evidence, LatencyMS: rb.LatencyMS, Tokens: rb.PromptTokens})
	}
	if len(ps) == 0 {
		return errors.New("no station answered by both runs")
	}
	var cal []calibration
	for _, d := range strings.Split(*calib, ",") {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		c, err := calibrate(d, *aName, *bName)
		if err != nil {
			return fmt.Errorf("calibrate %s: %w", d, err)
		}
		cal = append(cal, c)
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	head := fmt.Sprintf("dataset %s (%d stations), %s against %s on %d", dsHash, len(items), a.Name, b.Name, len(ps))
	if missing > 0 {
		head += fmt.Sprintf(" (%d without an answer from both)", missing)
	}
	head += ". No manual labels: agreement, not accuracy."
	fmt.Fprintf(f, "# qpc comparison\n\nGenerated %s. %s\n\n", time.Now().Format("2006-01-02 15:04"), head)
	fmt.Fprint(f, "## Agreement\n\n")
	writeAgreement(f, ps)
	if len(cal) > 0 {
		fmt.Fprint(f, "\n## What the labelled samples say\n\n")
		writeCalibration(f, cal, a.Name, b.Name)
	}
	fmt.Fprint(f, "\n## How each answers\n\n")
	writeDistributions(f, ps, a.Name, b.Name)
	writeDifferences(f, ps, byCall, a, b)

	fmt.Println(head)
	writeAgreement(os.Stdout, ps)
	if len(cal) > 0 {
		fmt.Println()
		writeCalibration(os.Stdout, cal, a.Name, b.Name)
	}
	fmt.Printf("\nfull report: %s\n", *out)
	return nil
}

func loadTwoRuns(dir, dsHash, aName, bName string) (a, b *run, err error) {
	runs, err := loadRuns(filepath.Join(dir, "runs"), aName+","+bName, dsHash)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range runs {
		switch r.Name {
		case aName:
			a = r
		case bName:
			b = r
		}
	}
	if a == nil || b == nil {
		return nil, nil, fmt.Errorf("%s: need runs %s and %s on this dataset (qpc-lab run)", dir, aName, bName)
	}
	return a, b, nil
}

func answerOf(r qpc.Result) answerT {
	a := answerT{r.Status, r.Routes, r.Preferred, r.Via, r.Note, r.Contribution}
	if a.Status == "" {
		a.Status = unreadable
	}
	return a
}

// mainOK is status, routes and via: the part of the answer both classifiers
// give (the heuristic never names a preferred route).
func (p pair) mainOK() bool { return p.statusOK() && p.routesOK() && p.viaOK() }

func writeAgreement(w io.Writer, ps []pair) {
	var status, routes, via, main, contrib int
	for _, p := range ps {
		for _, c := range []struct {
			ok bool
			n  *int
		}{{p.statusOK(), &status}, {p.routesOK(), &routes}, {p.viaOK(), &via}, {p.mainOK(), &main}, {p.contribOK(), &contrib}} {
			if c.ok {
				*c.n++
			}
		}
	}
	n := len(ps)
	fmt.Fprintln(w, "| same | stations | share |\n|---|---:|---:|")
	for _, r := range []struct {
		name string
		k    int
	}{{"status", status}, {"routes", routes}, {"via", via}, {"**status + routes + via**", main}, {"contribution", contrib}} {
		fmt.Fprintf(w, "| %s | %d of %d | %s |\n", r.name, r.k, n, pct(ratio(r.k, n)))
	}
	fmt.Fprintf(w, "\nStatus κ between the two: %s. The preferred route and the note are left out: the heuristic gives neither.\n", num(cohenKappa(ps)))
}

// calibration is one labelled sample: how the two runs fared against the
// manual labels when they agreed and when they did not. Right = status,
// routes and via as labelled.
type calibration struct {
	Dir                         string
	N                           int
	ARight, BRight              int
	Agree, AgreeRight           int
	Differ, DiffA, DiffB, DiffN int // differ: only a right, only b right, neither
	// The same for the contribution flag.
	CAgree, CAgreeRight             int
	CDiffer, CDiffA, CDiffB, CDiffN int
}

func calibrate(dir, aName, bName string) (calibration, error) {
	c := calibration{Dir: dir}
	dsPath := filepath.Join(dir, "dataset.jsonl")
	items, err := loadDataset(dsPath)
	if err != nil {
		return c, err
	}
	dsHash, _ := fileHash(dsPath)
	a, b, err := loadTwoRuns(dir, dsHash, aName, bName)
	if err != nil {
		return c, err
	}
	gold, err := loadGold(filepath.Join(dir, "gold.jsonl"))
	if err != nil {
		return c, err
	}
	for _, it := range items {
		g, ok := gold[it.Call]
		ra, okA := a.Results[it.Call]
		rb, okB := b.Results[it.Call]
		if !ok || g.Unsure || g.Legacy || !okA || !okB {
			continue
		}
		ga := answerT{g.Status, g.Routes, g.Preferred, g.Via, g.Note, g.Contribution}
		pa, pb := pair{Gold: ga, Pred: answerOf(ra)}, pair{Gold: ga, Pred: answerOf(rb)}
		ab := pair{Gold: pa.Pred, Pred: pb.Pred}
		c.N++
		okA, okB = pa.mainOK(), pb.mainOK()
		if okA {
			c.ARight++
		}
		if okB {
			c.BRight++
		}
		tally(ab.mainOK(), okA, okB, &c.Agree, &c.AgreeRight, &c.Differ, &c.DiffA, &c.DiffB, &c.DiffN)
		tally(ab.contribOK(), pa.contribOK(), pb.contribOK(), &c.CAgree, &c.CAgreeRight, &c.CDiffer, &c.CDiffA, &c.CDiffB, &c.CDiffN)
	}
	return c, nil
}

func tally(agree, okA, okB bool, nAgree, agreeRight, nDiffer, diffA, diffB, diffN *int) {
	switch {
	case agree:
		*nAgree++
		if okA {
			*agreeRight++
		}
	case okA:
		*nDiffer++
		*diffA++
	case okB:
		*nDiffer++
		*diffB++
	default:
		*nDiffer++
		*diffN++
	}
}

func writeCalibration(w io.Writer, cal []calibration, aName, bName string) {
	var t calibration
	t.Dir = "**together**"
	for _, c := range cal {
		t.N += c.N
		t.ARight, t.BRight = t.ARight+c.ARight, t.BRight+c.BRight
		t.Agree, t.AgreeRight, t.Differ = t.Agree+c.Agree, t.AgreeRight+c.AgreeRight, t.Differ+c.Differ
		t.DiffA, t.DiffB, t.DiffN = t.DiffA+c.DiffA, t.DiffB+c.DiffB, t.DiffN+c.DiffN
		t.CAgree, t.CAgreeRight, t.CDiffer = t.CAgree+c.CAgree, t.CAgreeRight+c.CAgreeRight, t.CDiffer+c.CDiffer
		t.CDiffA, t.CDiffB, t.CDiffN = t.CDiffA+c.CDiffA, t.CDiffB+c.CDiffB, t.CDiffN+c.CDiffN
	}
	rows := cal
	if len(cal) > 1 {
		rows = append(append([]calibration{}, cal...), t)
	}
	fmt.Fprintf(w, "Status, routes and via against your labels, on the samples you labelled (preferred route left out):\n\n")
	fmt.Fprintf(w, "| sample | stations | %s right | %s right | they agree | of which right | they differ | %s right | %s right | neither |\n", aName, bName, aName, bName)
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, c := range rows {
		fmt.Fprintf(w, "| %s | %d | %s | %s | %d | %s | %d | %d | %d | %d |\n", c.Dir, c.N,
			pct(ratio(c.ARight, c.N)), pct(ratio(c.BRight, c.N)), c.Agree, pct(ratio(c.AgreeRight, c.Agree)),
			c.Differ, c.DiffA, c.DiffB, c.DiffN)
	}
	fmt.Fprintf(w, "\nContribution flag: they agree on %d of %d, right on %s of those; they differ on %d: %s right %d, %s right %d, neither %d.\n",
		t.CAgree, t.N, pct(ratio(t.CAgreeRight, t.CAgree)), t.CDiffer, aName, t.CDiffA, bName, t.CDiffB, t.CDiffN)
}

func writeDistributions(w io.Writer, ps []pair, aName, bName string) {
	type side struct {
		status          map[qpc.Status]int
		route           map[qpc.Route]int
		combo           map[string]int
		contrib         map[qpc.Contribution]int
		via, pref, note int
	}
	count := func(get func(pair) answerT) side {
		s := side{status: map[qpc.Status]int{}, route: map[qpc.Route]int{}, combo: map[string]int{}, contrib: map[qpc.Contribution]int{}}
		for _, p := range ps {
			x := get(p)
			s.status[x.Status]++
			for _, r := range x.Routes {
				s.route[r]++
			}
			if x.Status == qpc.Paper {
				s.combo[strings.Join(routeNames(x.Routes), "+")]++
			}
			s.contrib[x.Contribution]++
			if x.Via != "" {
				s.via++
			}
			if x.Preferred != "" {
				s.pref++
			}
			if x.Note != "" {
				s.note++
			}
		}
		return s
	}
	sa := count(func(p pair) answerT { return p.Gold })
	sb := count(func(p pair) answerT { return p.Pred })
	fmt.Fprintf(w, "| | %s | %s |\n|---|---:|---:|\n", aName, bName)
	for _, s := range append(append([]qpc.Status{}, qpc.Statuses...), unreadable) {
		if sa.status[s]+sb.status[s] > 0 {
			fmt.Fprintf(w, "| status %s | %d | %d |\n", s, sa.status[s], sb.status[s])
		}
	}
	for _, r := range qpc.AllRoutes {
		fmt.Fprintf(w, "| accepts %s | %d | %d |\n", r, sa.route[r], sb.route[r])
	}
	fmt.Fprintf(w, "| via a callsign | %d | %d |\n| preferred route named | %d | %d |\n", sa.via, sb.via, sa.pref, sb.pref)
	fmt.Fprintf(w, "| contribution required | %d | %d |\n| contribution not needed | %d | %d |\n| with a note | %d | %d |\n",
		sa.contrib[qpc.ContributionRequired], sb.contrib[qpc.ContributionRequired],
		sa.contrib[qpc.ContributionNotNeeded], sb.contrib[qpc.ContributionNotNeeded], sa.note, sb.note)
	combos := map[string]bool{}
	for k := range sa.combo {
		combos[k] = true
	}
	for k := range sb.combo {
		combos[k] = true
	}
	keys := make([]string, 0, len(combos))
	for k := range combos {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if x, y := sa.combo[keys[i]]+sb.combo[keys[i]], sa.combo[keys[j]]+sb.combo[keys[j]]; x != y {
			return x > y
		}
		return keys[i] < keys[j]
	})
	fmt.Fprintf(w, "\n| paper routes | %s | %s |\n|---|---:|---:|\n", aName, bName)
	for _, k := range keys {
		fmt.Fprintf(w, "| %s | %d | %d |\n", k, sa.combo[k], sb.combo[k])
	}
	// Status cross-table.
	var cols []qpc.Status
	for _, s := range append(append([]qpc.Status{}, qpc.Statuses...), unreadable) {
		if sa.status[s]+sb.status[s] > 0 {
			cols = append(cols, s)
		}
	}
	cross := map[qpc.Status]map[qpc.Status]int{}
	for _, p := range ps {
		if cross[p.Gold.Status] == nil {
			cross[p.Gold.Status] = map[qpc.Status]int{}
		}
		cross[p.Gold.Status][p.Pred.Status]++
	}
	fmt.Fprintf(w, "\nStatus, rows = %s, columns = %s:\n\n| |", aName, bName)
	for _, c := range cols {
		fmt.Fprintf(w, " %s |", c)
	}
	fmt.Fprint(w, "\n|---|")
	for range cols {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	for _, r := range cols {
		fmt.Fprintf(w, "| **%s** |", r)
		for _, c := range cols {
			switch n := cross[r][c]; {
			case n > 0 && r == c:
				fmt.Fprintf(w, " **%d** |", n)
			case n > 0:
				fmt.Fprintf(w, " %d |", n)
			default:
				fmt.Fprint(w, " · |")
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "\n| route | both | only %s | only %s |\n|---|---:|---:|---:|\n", aName, bName)
	for _, r := range qpc.AllRoutes {
		var both, onlyA, onlyB int
		for _, p := range ps {
			switch x, y := qpc.HasRoute(p.Gold.Routes, r), qpc.HasRoute(p.Pred.Routes, r); {
			case x && y:
				both++
			case x:
				onlyA++
			case y:
				onlyB++
			}
		}
		fmt.Fprintf(w, "| %s | %d | %d | %d |\n", r, both, onlyA, onlyB)
	}
}

func routeNames(rs []qpc.Route) []string {
	var out []string
	for _, r := range qpc.SortRoutes(rs) {
		out = append(out, string(r))
	}
	return out
}

// diffKind groups a difference by what differs first: status, then routes,
// then via.
func diffKind(p pair) string {
	switch {
	case !p.statusOK():
		return fmt.Sprintf("status %s → %s", p.Gold.Status, p.Pred.Status)
	case !p.routesOK():
		return fmt.Sprintf("routes %s → %s", strings.Join(routeNames(p.Gold.Routes), "+"), strings.Join(routeNames(p.Pred.Routes), "+"))
	default:
		return "via differs"
	}
}

func writeDifferences(w io.Writer, ps []pair, byCall map[string]item, a, b *run) {
	groups := map[string][]pair{}
	var diff []string
	for _, p := range ps {
		if !p.mainOK() {
			k := diffKind(p)
			groups[k] = append(groups[k], p)
			diff = append(diff, p.Call)
		}
	}
	kinds := make([]string, 0, len(groups))
	for k := range groups {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if x, y := len(groups[kinds[i]]), len(groups[kinds[j]]); x != y {
			return x > y
		}
		return kinds[i] < kinds[j]
	})
	fmt.Fprintf(w, "\n## Where they differ\n\n%d stations differ in status, routes or via (%s → %s; * = preferred route).\n\n", len(diff), a.Name, b.Name)
	fmt.Fprintln(w, "| difference | stations |\n|---|---:|")
	for _, k := range kinds {
		fmt.Fprintf(w, "| %s | %d |\n", k, len(groups[k]))
	}
	for _, k := range kinds {
		fmt.Fprintf(w, "\n### %s (%d)\n\n", k, len(groups[k]))
		fmt.Fprintf(w, "| station | %s | %s | qslmgr | mqsl/eqsl/lotw | address | %s quoted | %s reason |\n|---|---|---|---|---|---|---|---|\n", a.Name, b.Name, b.Name, a.Name)
		for _, p := range groups[k] {
			st := byCall[p.Call].Station
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", p.Call, p.Gold, p.Pred,
				orDash(cell(st.QSLMgr)), flags(st), address(st), orDash(cell(evidenceOf(b.Results[p.Call]))), orDash(cell(a.Results[p.Call].Evidence)))
		}
	}

	var cdiff []pair
	for _, p := range ps {
		if !p.contribOK() {
			cdiff = append(cdiff, p)
		}
	}
	fmt.Fprintf(w, "\n## Contribution flag differs (%d)\n\n", len(cdiff))
	if len(cdiff) > 0 {
		fmt.Fprintf(w, "| station | %s | %s | %s note | qslmgr |\n|---|---|---|---|---|\n", a.Name, b.Name, b.Name)
		for _, p := range cdiff {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", p.Call, contribWord(p.Gold.Contribution), contribWord(p.Pred.Contribution),
				orDash(cell(p.Pred.Note)), orDash(cell(byCall[p.Call].QSLMgr)))
		}
	}

	if len(diff) > 0 {
		fmt.Fprintf(w, "\nTo label only the differing stations:\n\n    qpc-lab label -dir <dir> -calls %s\n", strings.Join(diff, ","))
	}
}

func evidenceOf(r qpc.Result) string {
	if r.ParseError != "" && r.Status == "" {
		return r.ParseError
	}
	return r.Evidence
}

func flags(st qpc.Station) string {
	f := func(s string) string {
		if s == "" {
			return "–"
		}
		return s
	}
	return f(st.MQSL) + "/" + f(st.EQSL) + "/" + f(st.LoTW)
}

func address(st qpc.Station) string {
	switch {
	case st.HasFullAddress():
		return "full"
	case st.Addr1 != "" || st.Addr2 != "" || st.Zip != "":
		return "partial"
	}
	return "none"
}

func contribWord(c qpc.Contribution) string {
	if c == "" {
		return "not stated"
	}
	return string(c)
}
