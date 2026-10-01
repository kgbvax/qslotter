// Command ocr-eval measures how well OCR output from photographed QSL cards
// can be matched to QSOs in the local log. It is a calibration tool for the
// received-card photo intake (docs/VISION.md, roadmap v2) and is not part of
// the app.
//
// Local-only working layout (never committed; card photos show other
// stations' addresses, /eval/ is in .gitignore):
//
//	eval/cards/*.heic|jpg   photos of received cards
//	eval/expected.csv       ground truth, one row per photo
//	eval/ocr.jsonl          output of tools/ocr-dump.swift
//	eval/qslotter.db(+-wal) a COPY of the real database (Open creates the
//	                        schema and bumps meta, so never point -db at the
//	                        live file)
//
// expected.csv has a header and the columns
//
//	file,call,qso_date,band,call_style
//	IMG_0012.heic,DL1ABC,20240301,20m,handwritten
//
// call is the sender's callsign; qso_date (YYYYMMDD) and band are optional
// and pin the exact QSO; call_style is printed or handwritten and splits the
// totals so the handwriting share is visible.
//
// Workflow:
//
//	swift tools/ocr-dump.swift eval/cards/* > eval/ocr.jsonl
//	go run ./cmd/ocr-eval -db eval/qslotter.db -mycall DL9ET
//
// Each photo ends in one outcome:
//
//	auto-ok     matcher would pre-select the right QSO
//	auto-wrong  matcher would pre-select a wrong QSO (the dangerous case)
//	pick-ok     the right QSO is on the pick list
//	pick-wrong  a pick list without the right QSO
//	miss        no callsign on the card matched the log
//	not-in-pool the expected call has no candidate QSO in the pool (for
//	            -pool unreceived: the card was already marked received);
//	            excluded from the rates
//
// "usable" = (auto-ok + pick-ok) / labeled photos in the pool. VISION's
// decision rule: roughly 90% or more means on-device Apple Vision is
// sufficient.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dl9et/qslotter/internal/intake"
	"github.com/dl9et/qslotter/internal/store"
)

var (
	dbFlag       = flag.String("db", "eval/qslotter.db", "copy of the qslotter SQLite database")
	ocrFlag      = flag.String("ocr", "eval/ocr.jsonl", "OCR output (JSON Lines) from tools/ocr-dump.swift")
	expectedFlag = flag.String("expected", "eval/expected.csv", "ground-truth CSV")
	myCallFlag   = flag.String("mycall", "", "own callsign, excluded from matching (printed on every card as the addressee)")
	poolFlag     = flag.String("pool", "unreceived", "which QSOs a card may confirm: unreceived or all")
	formatFlag   = flag.String("format", "text", "output format: text or jsonl")
)

type expected struct {
	Call  string
	Date  string
	Band  string
	Style string
}

// Outcomes.
const (
	outAutoOK      = "auto-ok"
	outAutoWrong   = "auto-wrong"
	outPickOK      = "pick-ok"
	outPickWrong   = "pick-wrong"
	outMiss        = "miss"
	outNotInPool   = "not-in-pool"
	outUnlabeled   = "unlabeled"
	styleUnknown   = "unknown"
	poolAll        = "all"
	poolUnreceived = "unreceived"
)

type cardResult struct {
	Type     string `json:"type"` // card
	File     string `json:"file"`
	Engine   string `json:"engine"`
	Style    string `json:"style"`
	Expected string `json:"expected"`
	Class    string `json:"class"`
	Outcome  string `json:"outcome"`
	Call     string `json:"call,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Key      string `json:"key,omitempty"`
	Cands    int    `json:"candidates"`
}

type totals struct {
	Type    string `json:"type"` // total
	Engine  string `json:"engine"`
	Style   string `json:"style"` // "all" for the engine-wide row
	N       int    `json:"n"`     // labeled photos in the pool
	Counts  map[string]int
	Usable  float64 `json:"usable_pct"`
	Skipped int     `json:"skipped"` // not-in-pool + unlabeled
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		log.Fatalf("ocr-eval: %v", err)
	}
}

func run() error {
	if *poolFlag != poolAll && *poolFlag != poolUnreceived {
		return fmt.Errorf("-pool must be %q or %q", poolUnreceived, poolAll)
	}
	if *formatFlag != "text" && *formatFlag != "jsonl" {
		return fmt.Errorf("-format must be text or jsonl")
	}
	if _, err := os.Stat(*dbFlag); err != nil {
		return fmt.Errorf("database %s: %w (store.Open would create an empty one)", *dbFlag, err)
	}
	exp, err := loadExpected(*expectedFlag)
	if err != nil {
		return err
	}
	f, err := os.Open(*ocrFlag)
	if err != nil {
		return err
	}
	defer f.Close()
	pages, err := intake.ParseOCR(f)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("%s holds no pages", *ocrFlag)
	}

	st, err := store.Open(*dbFlag)
	if err != nil {
		return err
	}
	defer st.Close()
	all, err := st.AllQSOs()
	if err != nil {
		return err
	}
	byCall := poolQSOs(all, *poolFlag)
	calls := make([]string, 0, len(byCall))
	for c := range byCall {
		calls = append(calls, c)
	}
	pool := intake.NewPool(calls)
	if *formatFlag == "text" {
		fmt.Printf("pool=%s: %d callsigns from %d QSOs (database holds %d)\n\n",
			*poolFlag, pool.Len(), countQSOs(byCall), len(all))
	}

	var results []cardResult
	for _, p := range pages {
		e, labeled := exp[filepath.Base(p.File)]
		r := intake.Resolve(p, pool, byCall, *myCallFlag)
		results = append(results, judge(p, r, e, labeled, byCall))
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Engine != results[j].Engine {
			return results[i].Engine < results[j].Engine
		}
		return results[i].File < results[j].File
	})
	tot := summarize(results)
	if *formatFlag == "jsonl" {
		return writeJSONL(os.Stdout, results, tot)
	}
	writeText(os.Stdout, results, tot)
	return nil
}

func loadExpected(path string) (map[string]expected, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"file", "call"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("%s: header lacks column %q", path, need)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	out := map[string]expected{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		file := get(rec, "file")
		if file == "" {
			continue
		}
		style := strings.ToLower(get(rec, "call_style"))
		if style == "" {
			style = styleUnknown
		}
		out[filepath.Base(file)] = expected{
			Call:  strings.ToUpper(get(rec, "call")),
			Date:  strings.ReplaceAll(get(rec, "qso_date"), "-", ""),
			Band:  strings.ToUpper(get(rec, "band")),
			Style: style,
		}
	}
	return out, nil
}

// poolQSOs groups the QSOs a card may confirm by callsign.
func poolQSOs(all []*store.QSO, mode string) map[string][]*store.QSO {
	m := map[string][]*store.QSO{}
	for _, q := range all {
		if mode == poolUnreceived && (q.QSLRcvd == "Y" || q.QSLRcvdLocal.String == "Y") {
			continue
		}
		m[strings.ToUpper(q.Call)] = append(m[strings.ToUpper(q.Call)], q)
	}
	return m
}

func countQSOs(m map[string][]*store.QSO) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

func (e expected) matches(q *store.QSO) bool {
	if !strings.EqualFold(q.Call, e.Call) {
		return false
	}
	if e.Date != "" && strings.ReplaceAll(q.QSODate, "-", "") != e.Date {
		return false
	}
	if e.Band != "" && !strings.EqualFold(q.Band, e.Band) {
		return false
	}
	return true
}

func judge(p intake.Page, r intake.Result, e expected, labeled bool, byCall map[string][]*store.QSO) cardResult {
	cr := cardResult{
		Type: "card", File: filepath.Base(p.File), Engine: p.Engine,
		Style: e.Style, Expected: e.Call, Class: string(r.Class), Cands: len(r.Candidates),
	}
	if cr.Style == "" {
		cr.Style = styleUnknown
	}
	if len(r.Hits) > 0 {
		cr.Call, cr.Tier, cr.Evidence = r.Hits[0].Call, r.Hits[0].Tier.String(), r.Hits[0].Variant
	}
	if len(r.Candidates) > 0 {
		cr.Key = r.Candidates[0].QSO.QSLKey
	}
	switch {
	case !labeled:
		cr.Outcome = outUnlabeled
		return cr
	case !inPool(e, byCall):
		cr.Outcome = outNotInPool
		return cr
	}
	switch r.Class {
	case intake.ClassMiss:
		cr.Outcome = outMiss
	case intake.ClassAuto:
		cr.Outcome = outAutoWrong
		if e.matches(r.Candidates[0].QSO) {
			cr.Outcome = outAutoOK
		}
	default:
		cr.Outcome = outPickWrong
		for _, c := range r.Candidates {
			if e.matches(c.QSO) {
				cr.Outcome = outPickOK
				break
			}
		}
	}
	return cr
}

func inPool(e expected, byCall map[string][]*store.QSO) bool {
	for _, q := range byCall[e.Call] {
		if e.matches(q) {
			return true
		}
	}
	return false
}

var outcomeOrder = []string{outAutoOK, outAutoWrong, outPickOK, outPickWrong, outMiss}

func summarize(results []cardResult) []totals {
	type key struct{ engine, style string }
	acc := map[key]*totals{}
	add := func(k key, outcome string) {
		t := acc[k]
		if t == nil {
			t = &totals{Type: "total", Engine: k.engine, Style: k.style, Counts: map[string]int{}}
			acc[k] = t
		}
		if outcome == outNotInPool || outcome == outUnlabeled {
			t.Skipped++
			return
		}
		t.N++
		t.Counts[outcome]++
	}
	for _, r := range results {
		add(key{r.Engine, "all"}, r.Outcome)
		add(key{r.Engine, r.Style}, r.Outcome)
	}
	var out []totals
	for _, t := range acc {
		if t.N > 0 {
			t.Usable = 100 * float64(t.Counts[outAutoOK]+t.Counts[outPickOK]) / float64(t.N)
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Engine != out[j].Engine {
			return out[i].Engine < out[j].Engine
		}
		if (out[i].Style == "all") != (out[j].Style == "all") {
			return out[i].Style == "all"
		}
		return out[i].Style < out[j].Style
	})
	return out
}

func writeJSONL(w io.Writer, results []cardResult, tot []totals) error {
	enc := json.NewEncoder(w)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	for _, t := range tot {
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return nil
}

func writeText(w io.Writer, results []cardResult, tot []totals) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tENGINE\tSTYLE\tEXPECTED\tCLASS\tOUTCOME\tMATCH\tCANDS\tFIRST KEY")
	for _, r := range results {
		match := "-"
		if r.Call != "" {
			match = fmt.Sprintf("%s (%s %q)", r.Call, r.Tier, r.Evidence)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			r.File, r.Engine, r.Style, dash(r.Expected), r.Class, r.Outcome, match, r.Cands, dash(r.Key))
	}
	tw.Flush()

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ENGINE\tSTYLE\tN\tAUTO-OK\tAUTO-WRONG\tPICK-OK\tPICK-WRONG\tMISS\tUSABLE\tSKIPPED")
	for _, t := range tot {
		fmt.Fprintf(tw, "%s\t%s\t%d", t.Engine, t.Style, t.N)
		for _, o := range outcomeOrder {
			fmt.Fprintf(tw, "\t%s", frac(t.Counts[o], t.N))
		}
		fmt.Fprintf(tw, "\t%.0f%%\t%d\n", t.Usable, t.Skipped)
	}
	tw.Flush()
}

func frac(n, of int) string {
	if of == 0 {
		return "0"
	}
	return fmt.Sprintf("%d (%.0f%%)", n, 100*float64(n)/float64(of))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
