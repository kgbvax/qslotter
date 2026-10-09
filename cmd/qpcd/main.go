// Command qpcd is the QSL preference classifier backend (package qpc): an
// HTTP service that classifies a station's QRZ record with a language model
// behind any OpenAI-compatible endpoint. It is experimental and independent of
// the qslotter app (see qpc/LABELS.md and cmd/qpc-lab).
//
//	qpcd serve -config qpcd.yaml [-addr 127.0.0.1:8474]
//	qpcd classify -config qpcd.yaml [-show-prompt] < station.json
//
// API:
//
//	POST /v1/classify  {"station": {"call": "EA8/DL1ABC", "qslmgr": "", "mqsl": "1", "bio": "..."}}
//	                   -> qpc.Result as JSON (502 if the model cannot be reached)
//	GET  /healthz      -> {"model": ..., "prompt": ...}
//
// The config file is a qpc.Variant in YAML; see qpc/qpcd.example.yaml.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dl9et/qslotter/qpc"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "classify":
		classify(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: qpcd serve|classify -config qpcd.yaml [flags]")
	os.Exit(2)
}

func load(fs *flag.FlagSet, args []string) *qpc.Classifier {
	cfg := fs.String("config", "qpcd.yaml", "classifier config (a qpc.Variant in YAML)")
	fs.Parse(args)
	v, err := qpc.LoadVariant(*cfg)
	if err != nil {
		log.Fatal(err)
	}
	c, err := qpc.New(v)
	if err != nil {
		log.Fatal(err)
	}
	return c
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8474", "listen address")
	c := load(fs, args)
	srv := &http.Server{Addr: *addr, Handler: newHandler(c), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("qpcd: %s with prompt %s via %s, listening on %s", c.Variant().Model, c.PromptID(), c.Variant().BaseURL, *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type classifyRequest struct {
	Station qpc.Station `json:"station"`
}

func newHandler(c *qpc.Classifier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"model": c.Variant().Model, "prompt": c.PromptID()})
	})
	mux.HandleFunc("POST /v1/classify", func(w http.ResponseWriter, r *http.Request) {
		var req classifyRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body: " + err.Error()})
			return
		}
		if strings.TrimSpace(req.Station.Call) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "station.call is required"})
			return
		}
		res, err := c.Classify(r.Context(), req.Station)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func classify(args []string) {
	fs := flag.NewFlagSet("classify", flag.ExitOnError)
	show := fs.Bool("show-prompt", false, "print the rendered prompt instead of calling the model")
	c := load(fs, args)
	var st qpc.Station
	if err := json.NewDecoder(os.Stdin).Decode(&st); err != nil {
		log.Fatalf("read station JSON from stdin: %v", err)
	}
	if *show {
		system, user, _, err := c.Messages(st)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("=== system ===\n%s\n\n=== user ===\n%s\n", system, user)
		return
	}
	res, err := c.Classify(context.Background(), st)
	if err != nil {
		log.Fatal(err)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
}
