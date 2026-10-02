// Command qsl-eval is a calibration CLI for LLM-based QSL method determination.
//
// It reads an ADIF file (or a single callsign / callsign list), gathers QRZ
// station data via the QRZ XML API, prompts a local Ollama LLM to classify the
// preferred paper QSL method, and writes a JSONL report suitable for prompt
// iteration and evaluation.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/llmqsl"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
)

const version = "0.1.0"

var (
	adifFlag        = flag.String("adif", "", "input ADIF file (mutually exclusive with -call/-calls-file)")
	callFlag        = flag.String("call", "", "single callsign to evaluate")
	callsFlag       = flag.String("calls", "", "comma-separated callsign whitelist")
	callsFileFlag   = flag.String("calls-file", "", "path to file with one callsign per line")
	configFlag      = flag.String("config", "config.yaml", "path to qslotter config.yaml")
	outFlag         = flag.String("out", "qsl-eval.jsonl", "output JSONL file")
	ollamaURLFlag   = flag.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	modelFlag       = flag.String("model", "ministral-3b", "Ollama model name")
	promptFlag      = flag.String("prompt", "", "path to custom prompt template file")
	qrzWorkersFlag  = flag.Int("qrz-workers", 1, "concurrent QRZ lookups (must be 1 because qrz.Client is not goroutine-safe)")
	llmWorkersFlag  = flag.Int("llm-workers", 1, "concurrent LLM evaluations")
	qrzSleepFlag    = flag.Duration("qrz-sleep", 1*time.Second, "delay between QRZ requests")
	qrzTimeoutFlag  = flag.Duration("qrz-timeout", 30*time.Second, "per-QRZ-lookup timeout")
	timeoutFlag     = flag.Duration("timeout", 2*time.Minute, "per-LLM-request timeout")
	temperatureFlag = flag.Float64("temperature", 0, "LLM sampling temperature")
	skipCompareFlag = flag.Bool("skip-compare", false, "omit heuristic baseline")
	sampleFlag      = flag.Int("sample", 0, "limit to first N unique callsigns")
	formatFlag      = flag.String("format", "jsonl", "output format: jsonl, pretty, or text")
	dryRunFlag      = flag.Bool("dry-run", false, "fetch QRZ but skip Ollama calls")
	redactFlag      = flag.Bool("redact", false, "remove PII fields from output")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		log.Fatalf("qsl-eval: %v", err)
	}
}

func run() error {
	if err := validateFlags(); err != nil {
		return err
	}

	cfg, err := config.Load(*configFlag)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.QRZ.Username == "" || cfg.QRZ.Password == "" {
		return fmt.Errorf("QRZ username and password must be set in %s", *configFlag)
	}

	calls, err := buildCallsignList()
	if err != nil {
		return err
	}
	if len(calls) == 0 {
		return fmt.Errorf("no callsigns to evaluate")
	}
	if *sampleFlag > 0 && *sampleFlag < len(calls) {
		calls = calls[:*sampleFlag]
	}

	// Warn the user about QRZ quota before making any network calls.
	fmt.Fprintf(os.Stderr, "qsl-eval: %d unique callsign(s) to evaluate.\n", len(calls))
	fmt.Fprintf(os.Stderr, "WARNING: each QRZ lookup consumes your XML subscription quota.\n")

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("open store %q: %w", cfg.Store.Path, err)
	}
	defer st.Close()

	qrzClient := qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
	qrzClient.HTTP.Timeout = *qrzTimeoutFlag
	refresher := station.New(st, qrzClient, nil, cfg.QRZ.CacheTTL)

	promptRenderer, err := loadPrompt()
	if err != nil {
		return err
	}

	var llmClient llmqsl.LLMClient = &noopLLM{}
	if !*dryRunFlag {
		oc := llmqsl.NewOllamaClient(*ollamaURLFlag, *modelFlag)
		oc.Timeout = *timeoutFlag
		oc.Temperature = *temperatureFlag
		oc.JSONMode = true
		llmClient = oc
	}

	evaluator := &llmqsl.Evaluator{
		Client: llmClient,
		Prompt: promptRenderer,
	}

	out, err := os.Create(*outFlag)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// Signal handling: flush and close cleanly on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nqsl-eval: caught signal, flushing output...")
		out.Sync()
		out.Close()
		os.Exit(1)
	}()

	promptHash := "embedded"
	if *promptFlag != "" {
		promptHash = hashFilePath(*promptFlag)
	}
	meta := runMeta{
		Version:    version,
		Model:      *modelFlag,
		OllamaURL:  *ollamaURLFlag,
		PromptHash: promptHash,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		DryRun:     *dryRunFlag,
		Redacted:   *redactFlag,
	}

	writer := bufio.NewWriter(out)
	defer writer.Flush()

	// Stage 1: gather QRZ data serially (qrz.Client is not goroutine-safe).
	// Stage 2: evaluate with the LLM with controlled concurrency.
	enrichedCh := make(chan enriched, len(calls))

	go func() {
		defer close(enrichedCh)
		ctx := context.Background()
		for i, call := range calls {
			select {
			case <-ctx.Done():
				return
			default:
			}

			e := enriched{call: call}
			si, err := refresher.Get(ctx, call)
			if err != nil {
				e.qrzErr = err.Error()
			} else if si != nil {
				e.info = si
				// Reconstruct a qrz.Callsign from the cached store record for the prompt.
				e.cs = stationInfoToCallsign(si)
				e.bio = si.BioText
			}
			if !*skipCompareFlag {
				e.heuristic = qsldetermine.Assess(qsldetermine.Input{Call: e.call, QSLMgr: e.cs.QSLMgr, MQSL: e.cs.MQSL, EQSL: e.cs.EQSL, LoTW: e.cs.LoTW, Bio: e.bio})
			}
			enrichedCh <- e
			if i < len(calls)-1 {
				time.Sleep(*qrzSleepFlag)
			}
		}
	}()

	var wg sync.WaitGroup
	written := 0
	var mu sync.Mutex
	llmSem := make(chan struct{}, *llmWorkersFlag)

	for e := range enrichedCh {
		e := e
		wg.Add(1)
		llmSem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-llmSem }()

			record := buildRecord(e, evaluator, meta)

			var b []byte
			var writeErr error
			switch *formatFlag {
			case "text":
				b = []byte(formatText(record))
			case "pretty":
				var indented bytes.Buffer
				if err := json.Indent(&indented, mustJSON(record), "", "  "); err == nil {
					b = indented.Bytes()
				} else {
					b = mustJSON(record)
				}
			default: // jsonl
				b = mustJSON(record)
			}

			mu.Lock()
			_, werr1 := writer.Write(b)
			_, werr2 := writer.WriteString("\n")
			if *formatFlag == "pretty" || *formatFlag == "text" {
				writer.Flush()
			}
			if werr1 == nil && werr2 == nil {
				written++
			}
			mu.Unlock()
			if werr1 != nil {
				log.Printf("write record for %s: %v", e.call, werr1)
			}
			if werr2 != nil {
				log.Printf("write newline for %s: %v", e.call, werr2)
			}
			_ = writeErr
		}()
	}
	wg.Wait()
	writer.Flush()

	fmt.Fprintf(os.Stderr, "qsl-eval: wrote %d record(s) to %s\n", written, *outFlag)
	return nil
}

func validateFlags() error {
	sources := 0
	if *adifFlag != "" {
		sources++
	}
	if *callFlag != "" {
		sources++
	}
	if *callsFileFlag != "" {
		sources++
	}
	if *callsFlag != "" {
		sources++
	}
	if sources == 0 {
		return fmt.Errorf("provide one of -adif, -call, -calls, or -calls-file")
	}
	if sources > 1 {
		return fmt.Errorf("-adif, -call, -calls, and -calls-file are mutually exclusive")
	}
	if *formatFlag != "jsonl" && *formatFlag != "pretty" && *formatFlag != "text" {
		return fmt.Errorf("-format must be jsonl, pretty, or text")
	}
	if *qrzWorkersFlag != 1 {
		return fmt.Errorf("-qrz-workers must be 1 because qrz.Client is not goroutine-safe")
	}
	return nil
}

func buildCallsignList() ([]string, error) {
	m := make(map[string]struct{})
	switch {
	case *adifFlag != "":
		f, err := os.Open(*adifFlag)
		if err != nil {
			return nil, fmt.Errorf("open adif: %w", err)
		}
		defer f.Close()
		recs, err := adif.NewReader(f).ReadAll()
		if err != nil {
			return nil, fmt.Errorf("read adif: %w", err)
		}
		for _, r := range recs {
			call := r.Call()
			if call != "" {
				m[call] = struct{}{}
			}
		}
	case *callFlag != "":
		m[strings.ToUpper(strings.TrimSpace(*callFlag))] = struct{}{}
	case *callsFlag != "":
		for _, c := range strings.Split(*callsFlag, ",") {
			c = strings.ToUpper(strings.TrimSpace(c))
			if c != "" {
				m[c] = struct{}{}
			}
		}
	case *callsFileFlag != "":
		f, err := os.Open(*callsFileFlag)
		if err != nil {
			return nil, fmt.Errorf("open calls file: %w", err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			c := strings.ToUpper(strings.TrimSpace(sc.Text()))
			if c != "" && !strings.HasPrefix(c, "#") {
				m[c] = struct{}{}
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read calls file: %w", err)
		}
	}
	var out []string
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}

func loadPrompt() (llmqsl.PromptRenderer, error) {
	if *promptFlag == "" {
		return llmqsl.DefaultPrompt()
	}
	raw, err := os.ReadFile(*promptFlag)
	if err != nil {
		return nil, fmt.Errorf("read prompt file: %w", err)
	}
	return llmqsl.NewPrompt(string(raw))
}

func stationInfoToCallsign(si *store.StationInfo) *qrz.Callsign {
	return &qrz.Callsign{
		Call:    si.Callsign,
		QSLMgr:  si.QSLMgr,
		EQSL:    si.EQSL,
		MQSL:    si.MQSL,
		LoTW:    si.LoTW,
		Email:   si.Email,
		Addr1:   si.Addr1,
		Addr2:   si.Addr2,
		State:   si.State,
		Zip:     si.Zip,
		Country: si.Country,
		DXCC:    si.DXCC,
	}
}

type runMeta struct {
	Version    string `json:"version"`
	Model      string `json:"model"`
	OllamaURL  string `json:"ollama_url"`
	PromptHash string `json:"prompt_hash"`
	Timestamp  string `json:"timestamp"`
	DryRun     bool   `json:"dry_run"`
	Redacted   bool   `json:"redacted"`
}

type recordError struct {
	Step    string `json:"step"`
	Message string `json:"message"`
}

type outputRecord struct {
	Meta      runMeta            `json:"meta"`
	Callsign  string             `json:"callsign"`
	QRZData   *qrz.Callsign      `json:"qrz_data,omitempty"`
	BioText   string             `json:"bio_text,omitempty"`
	Heuristic *heuristicRecord   `json:"heuristic,omitempty"`
	Prompt    string             `json:"prompt"`
	LLM       *llmqsl.EvalResult `json:"llm,omitempty"`
	Error     *recordError       `json:"error,omitempty"`
}

type heuristicRecord struct {
	Raw    qsldetermine.Assessment `json:"raw"`
	Mapped llmqsl.Method           `json:"mapped"`
}

func buildRecord(e enriched, evaluator *llmqsl.Evaluator, meta runMeta) outputRecord {
	rec := outputRecord{
		Meta:     meta,
		Callsign: e.call,
	}
	if e.cs != nil {
		cs := *e.cs
		if meta.Redacted {
			cs.Email = ""
			cs.Addr1 = ""
			cs.Addr2 = ""
			cs.Zip = ""
		}
		rec.QRZData = &cs
		rec.BioText = e.bio
	}
	if e.qrzErr != "" {
		rec.Error = &recordError{Step: "qrz", Message: e.qrzErr}
	}
	if !*skipCompareFlag {
		rec.Heuristic = &heuristicRecord{Raw: e.heuristic, Mapped: llmqsl.MapHeuristic(e.heuristic)}
	}

	input := llmqsl.EvalInput{
		Callsign:  e.call,
		QRZ:       e.cs,
		Bio:       e.bio,
		Heuristic: e.heuristic,
	}
	prompt, err := evaluator.Prompt.Render(input)
	if err != nil {
		if rec.Error == nil {
			rec.Error = &recordError{Step: "prompt", Message: err.Error()}
		}
		return rec
	}
	rec.Prompt = prompt

	if !meta.DryRun {
		llmRes, err := evaluator.Evaluate(context.Background(), input)
		if err != nil {
			if rec.Error == nil {
				rec.Error = &recordError{Step: "llm", Message: err.Error()}
			}
		}
		rec.LLM = &llmRes
	}
	return rec
}

func hashFilePath(p string) string {
	return filepath.Base(p)
}

// mustJSON marshals v and panics only on programmer error; it is used after
// buildRecord, which contains only JSON-marshalable types.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Fatalf("marshal record: %v", err)
	}
	return b
}

// formatText returns a human-readable text summary of a record for -format text.
func formatText(r outputRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "== %s ==\n", r.Callsign)
	if r.Error != nil {
		fmt.Fprintf(&sb, "ERROR [%s]: %s\n", r.Error.Step, r.Error.Message)
	}
	if r.QRZData != nil {
		fmt.Fprintf(&sb, "Country: %s\n", r.QRZData.Country)
		if r.QRZData.QSLMgr != "" {
			fmt.Fprintf(&sb, "QSL Manager: %s\n", r.QRZData.QSLMgr)
		}
		fmt.Fprintf(&sb, "MQSL: %s  EQSL: %s  LOTW: %s\n",
			coalesceStr(r.QRZData.MQSL, "?"),
			coalesceStr(r.QRZData.EQSL, "?"),
			coalesceStr(r.QRZData.LoTW, "?"))
	}
	if r.BioText != "" {
		fmt.Fprintf(&sb, "Bio:\n%s\n", strings.TrimSpace(r.BioText))
	}
	if r.Heuristic != nil {
		fmt.Fprintf(&sb, "Heuristic: %s (mapped: %s)\n", r.Heuristic.Raw.Suggest, r.Heuristic.Mapped)
	}
	fmt.Fprintf(&sb, "Prompt:\n%s\n", r.Prompt)
	if r.LLM != nil {
		fmt.Fprintf(&sb, "LLM: method=%s confidence=%s manager=%s reasoning=%q\n",
			r.LLM.Method, r.LLM.Confidence, r.LLM.ManagerCall, r.LLM.Reasoning)
		if r.LLM.ParseError != "" {
			fmt.Fprintf(&sb, "Parse error: %s\n", r.LLM.ParseError)
		}
		fmt.Fprintf(&sb, "Raw response:\n%s\n", r.LLM.RawResponse)
	} else if r.Meta.DryRun {
		fmt.Fprintln(&sb, "LLM: (dry run)")
	}
	return sb.String()
}

func coalesceStr(a, fallback string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return fallback
}

// noopLLM is a stand-in for dry-run mode.
type noopLLM struct{}

func (n *noopLLM) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}

type enriched struct {
	call      string
	info      *store.StationInfo
	cs        *qrz.Callsign
	bio       string
	heuristic qsldetermine.Assessment
	qrzErr    string
}
