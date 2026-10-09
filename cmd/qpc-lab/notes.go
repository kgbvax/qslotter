package main

import (
	_ "embed"
	"encoding/json"
	"errors"
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

//go:embed notes.html
var notesPage []byte

// cmdNotes serves a review page for the station notes: every station where
// the manual label or a run's answer has a note, both side by side, so the
// labeller can accept, edit or reject the model's note. Not blind (the model's
// note is shown): it completes the note part of the gold labels after the
// blind pass, the rest of the label stays as it was.
func cmdNotes(args []string) error {
	fl := flag.NewFlagSet("notes", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	runName := fl.String("run", "", "the run whose notes to review (a directory under <dir>/runs)")
	addr := fl.String("addr", "127.0.0.1:8476", "listen address")
	fl.Parse(args)
	if *runName == "" {
		return errors.New("-run is required")
	}
	items, err := loadDataset(filepath.Join(*dir, "dataset.jsonl"))
	if err != nil {
		return err
	}
	results, err := lastResults(filepath.Join(*dir, "runs", *runName, "results.jsonl"))
	if err != nil {
		return err
	}
	h, n, err := newNotesHandler(items, results, filepath.Join(*dir, "gold.jsonl"))
	if err != nil {
		return err
	}
	log.Printf("reviewing %d notes (yours and %s) at http://%s/", n, *runName, *addr)
	return http.ListenAndServe(*addr, h)
}

type noteItem struct {
	Station   qpc.Station `json:"station"`
	Gold      string      `json:"gold"`  // your note
	Model     string      `json:"model"` // the run's note
	Answer    string      `json:"answer"`
	Reviewed  bool        `json:"reviewed"`
	Evidence  string      `json:"evidence"`
	goldLabel goldLabel
}

func newNotesHandler(items []item, results map[string]qpc.Result, goldPath string) (http.Handler, int, error) {
	gold, err := loadGold(goldPath)
	if err != nil {
		return nil, 0, err
	}
	var list []*noteItem
	byCall := map[string]*noteItem{}
	for _, it := range items {
		g, ok := gold[it.Call]
		if !ok || g.Legacy {
			continue // not labelled in the routes scheme
		}
		r := results[it.Call]
		if g.Note == "" && r.Note == "" {
			continue
		}
		ni := &noteItem{Station: it.Station, Gold: g.Note, Model: r.Note, Reviewed: g.NoteReviewed,
			Answer: answerT{Status: g.Status, Routes: g.Routes, Preferred: g.Preferred, Via: g.Via}.String(), Evidence: r.Evidence, goldLabel: g}
		list = append(list, ni)
		byCall[it.Call] = ni
	}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(notesPage)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": list})
	})
	mux.HandleFunc("POST /api/note", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Call, Note string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ni, ok := byCall[strings.ToUpper(strings.TrimSpace(in.Call))]
		if !ok {
			http.Error(w, fmt.Sprintf("%s is not under review", in.Call), http.StatusBadRequest)
			return
		}
		// A new record with only the note changed; the label stays as it was.
		g := ni.goldLabel
		g.Note, g.NoteReviewed, g.At = strings.TrimSpace(in.Note), true, time.Now().UTC().Format(time.RFC3339)
		if err := appendJSONL(goldPath, g); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ni.goldLabel, ni.Gold, ni.Reviewed = g, g.Note, true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ni)
	})
	return mux, len(list), nil
}
