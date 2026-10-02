package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/qpc"
)

//go:embed label.html
var labelPage []byte

// cmdLabel serves the blind labelling page. It reads only the dataset and the
// gold file: no prediction ever reaches the page.
func cmdLabel(args []string) error {
	fl := flag.NewFlagSet("label", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	addr := fl.String("addr", "127.0.0.1:8475", "listen address")
	calls := fl.String("calls", "", "comma-separated calls: show only these stations (re-checking)")
	fl.Parse(args)
	ds := filepath.Join(*dir, "dataset.jsonl")
	items, err := loadDataset(ds)
	if err != nil {
		return err
	}
	if *calls != "" {
		if items, err = onlyCalls(items, *calls); err != nil {
			return err
		}
	}
	h, err := newLabelHandler(items, filepath.Join(*dir, "gold.jsonl"))
	if err != nil {
		return err
	}
	log.Printf("labelling %d stations from %s at http://%s/", len(items), ds, *addr)
	return http.ListenAndServe(*addr, h)
}

// onlyCalls keeps the stations named in a comma-separated list, in its order.
func onlyCalls(items []item, list string) ([]item, error) {
	byCall := map[string]item{}
	for _, it := range items {
		byCall[it.Call] = it
	}
	var out []item
	for _, c := range strings.Split(list, ",") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		it, ok := byCall[c]
		if !ok {
			return nil, fmt.Errorf("%s is not in the dataset", c)
		}
		out = append(out, it)
	}
	return out, nil
}

func newLabelHandler(items []item, goldPath string) (http.Handler, error) {
	gold, err := loadGold(goldPath)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, it := range items {
		known[it.Call] = true
	}
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(labelPage)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		stations := make([]qpc.Station, len(items))
		for i, it := range items {
			stations[i] = it.Station
		}
		type goldView struct {
			goldLabel
			Legacy bool `json:"legacy"` // first-pass label, not yet in the routes scheme
		}
		view := map[string]goldView{}
		for c, g := range gold {
			view[c] = goldView{g, g.Legacy}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"stations": stations, "gold": view, "statuses": qpc.Statuses, "routes": qpc.AllRoutes, "rules": qpc.Rules,
		})
	})
	mux.HandleFunc("POST /api/label", func(w http.ResponseWriter, r *http.Request) {
		var g goldLabel
		if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := g.clean(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !known[g.Call] {
			http.Error(w, "unknown station "+g.Call, http.StatusBadRequest)
			return
		}
		g.At = time.Now().UTC().Format(time.RFC3339)
		mu.Lock()
		defer mu.Unlock()
		if err := appendJSONL(goldPath, g); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		gold[g.Call] = g
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(g)
	})
	return mux, nil
}

// clean normalizes a label posted by the page and rejects inconsistent ones:
// routes mean status paper, paper needs routes, preferred must be one of them.
func (g *goldLabel) clean() error {
	g.Call = strings.ToUpper(strings.TrimSpace(g.Call))
	g.Via = strings.ToUpper(strings.TrimSpace(g.Via))
	g.Note, g.Comment, g.Label = strings.TrimSpace(g.Note), strings.TrimSpace(g.Comment), ""
	for _, r := range g.Routes {
		if !r.Valid() {
			return fmt.Errorf("invalid route %q", r)
		}
	}
	g.Routes = qpc.SortRoutes(g.Routes)
	switch {
	case len(g.Routes) > 0:
		g.Status = qpc.Paper
	case g.Status == qpc.Paper:
		return fmt.Errorf("status paper needs at least one route")
	case !g.Status.Valid():
		return fmt.Errorf("pick routes or a status")
	}
	if g.Preferred != "" && !qpc.HasRoute(g.Routes, g.Preferred) {
		return fmt.Errorf("preferred %q is not among the routes", g.Preferred)
	}
	return nil
}
