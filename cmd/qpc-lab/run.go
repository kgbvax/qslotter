package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/qpc"
)

// experiment is a YAML file of classifier variants to compare.
type experiment struct {
	Defaults qpc.Variant   `yaml:"defaults"`
	Variants []qpc.Variant `yaml:"variants"`
}

// runInfo identifies what produced a results file, so results from an edited
// prompt or another dataset are never mixed under one variant name.
type runInfo struct {
	Variant  qpc.Variant `json:"variant"`
	PromptID string      `json:"prompt_id,omitempty"`
	Dataset  string      `json:"dataset_hash"`
	Started  string      `json:"started"`
}

func (a runInfo) sameAs(b runInfo) error {
	switch {
	case a.Dataset != b.Dataset:
		return fmt.Errorf("dataset changed (%s -> %s)", a.Dataset, b.Dataset)
	case a.PromptID != b.PromptID:
		return fmt.Errorf("prompt changed (%s -> %s)", a.PromptID, b.PromptID)
	}
	va, _ := json.Marshal(a.Variant)
	vb, _ := json.Marshal(b.Variant)
	if !bytes.Equal(va, vb) {
		return fmt.Errorf("variant settings changed (%s -> %s)", va, vb)
	}
	return nil
}

func cmdRun(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	expPath := fl.String("experiment", "", "experiment YAML (defaults + variants)")
	only := fl.String("only", "", "comma-separated variant names to run (default all)")
	limit := fl.Int("limit", 0, "classify only the first N stations (smoke test)")
	force := fl.Bool("force", false, "discard earlier results of the variants run")
	fl.Parse(args)
	if *expPath == "" {
		return errors.New("-experiment is required")
	}
	var exp experiment
	if err := qpc.LoadYAML(*expPath, &exp); err != nil {
		return err
	}
	dsPath := filepath.Join(*dir, "dataset.jsonl")
	items, err := loadDataset(dsPath)
	if err != nil {
		return err
	}
	dsHash, err := fileHash(dsPath)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(items) {
		items = items[:*limit]
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}

	var variants []qpc.Variant
	seen := map[string]bool{}
	for _, v := range exp.Variants {
		v = v.Over(exp.Defaults).WithDefaults()
		if err := v.Check(); err != nil {
			return err
		}
		if !validName.MatchString(v.Name) {
			return fmt.Errorf("variant name %q: use letters, digits, '.', '_' and '-'", v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("variant %q appears twice", v.Name)
		}
		seen[v.Name] = true
		if len(want) == 0 || want[v.Name] {
			variants = append(variants, v)
		}
	}
	if len(variants) == 0 {
		return errors.New("no variants selected")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for _, v := range variants {
		if err := runVariant(ctx, v, items, dsHash, filepath.Join(*dir, "runs", v.Name), *force); err != nil {
			if ctx.Err() != nil {
				return errors.New("interrupted; rerun to continue where it stopped")
			}
			log.Printf("[%s] stopped: %v", v.Name, err)
		}
	}
	return nil
}

var validName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// classifyFunc classifies one station.
type classifyFunc func(context.Context, qpc.Station) (qpc.Result, error)

func runVariant(ctx context.Context, v qpc.Variant, items []item, dsHash, out string, force bool) error {
	info := runInfo{Variant: v, Dataset: dsHash, Started: time.Now().UTC().Format(time.RFC3339)}
	var classify classifyFunc
	if v.Kind == "heuristic" {
		classify = heuristic
	} else {
		c, err := qpc.New(v)
		if err != nil {
			return err
		}
		info.PromptID = c.PromptID()
		classify = c.Classify
	}

	resultsPath := filepath.Join(out, "results.jsonl")
	infoPath := filepath.Join(out, "run.json")
	if force {
		os.RemoveAll(out)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	var prev runInfo
	switch err := readJSON(infoPath, &prev); {
	case err == nil:
		if err := prev.sameAs(info); err != nil {
			return fmt.Errorf("%v since %s was written; use a new variant name or -force", err, out)
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := writeJSON(infoPath, info); err != nil {
			return err
		}
	default:
		return err
	}
	done, err := lastResults(resultsPath)
	if err != nil {
		return err
	}

	failures := 0
	for i, it := range items {
		if _, ok := done[it.Call]; ok {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r, err := classify(ctx, it.Station)
		if err != nil {
			// Not written: the station is retried on the next run.
			failures++
			log.Printf("[%s] %3d/%d %-12s ERROR %v", v.Name, i+1, len(items), it.Call, err)
			if failures >= 3 {
				return fmt.Errorf("%d errors in a row", failures)
			}
			continue
		}
		failures = 0
		if err := appendJSONL(resultsPath, r); err != nil {
			return err
		}
		label := string(r.Label)
		if label == "" {
			label = "(unreadable: " + r.ParseError + ")"
		}
		if r.Via != "" {
			label += " via " + r.Via
		}
		log.Printf("[%s] %3d/%d %-12s %s (%.1fs)", v.Name, i+1, len(items), it.Call, label, float64(r.LatencyMS)/1000)
	}
	return nil
}

// heuristic runs qslotter's rule-based determination (internal/qsldetermine)
// and maps it to qpc labels, as the baseline the LLM has to beat. It never
// answers oqrs; a manager becomes the via, and the route is read from the
// reason text (the qslmgr field can say "K2ABC (bureau only)").
func heuristic(_ context.Context, st qpc.Station) (qpc.Result, error) {
	start := time.Now()
	c := &qrz.Callsign{Call: st.Call, Country: st.Country, DXCC: st.DXCC,
		QSLMgr: st.QSLMgr, MQSL: st.MQSL, EQSL: st.EQSL, LoTW: st.LoTW}
	h := qsldetermine.Determine(c, st.Bio)
	r := qpc.Result{Call: strings.ToUpper(st.Call), Model: "heuristic", Confidence: h.Confidence, Evidence: h.Reason}
	r.Label, r.Via = mapHeuristic(h)
	r.LatencyMS = time.Since(start).Milliseconds()
	return r, nil
}

var (
	bureauWordRe = regexp.MustCompile(`(?i)bureau|buro|b\x{fc}ro`)
	directWordRe = regexp.MustCompile(`(?i)direct|direkt`)
)

func mapHeuristic(h qsldetermine.Result) (qpc.Label, string) {
	switch h.Method {
	case "B":
		return qpc.Bureau, ""
	case "D":
		return qpc.Direct, ""
	case "M":
		// The reason quotes the qslmgr field or the bio match; the manager
		// call itself never contains these words.
		rest := strings.Replace(h.Reason, h.Manager, "", 1)
		b, d := bureauWordRe.MatchString(rest), directWordRe.MatchString(rest)
		switch {
		case b:
			return qpc.Bureau, h.Manager // cheapest accepted route
		case d:
			return qpc.Direct, h.Manager
		}
		return qpc.Unclear, h.Manager
	}
	if h.RefusePaper {
		return qpc.NoPaper, ""
	}
	return qpc.Unknown, ""
}
