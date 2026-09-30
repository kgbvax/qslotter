// Package llmqsl uses a local LLM (via Ollama) to determine the preferred
// paper QSL method for a station, using QRZ data as context.
//
// It is designed for calibration: it records the exact prompt, raw response,
// and parsed result so users can iterate on prompts before wiring the evaluator
// into the live qslotter pipeline.
package llmqsl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
)

// Method is the LLM-facing vocabulary for paper QSL preference.
type Method string

const (
	MethodNone          Method = "none"
	MethodDirect        Method = "direct"
	MethodBuero         Method = "buero"
	MethodManagerBuero  Method = "manager-buero"
	MethodManagerDirect Method = "manager-direct"
)

var allMethods = []Method{
	MethodNone,
	MethodDirect,
	MethodBuero,
	MethodManagerBuero,
	MethodManagerDirect,
}

// IsValid reports whether m is one of the allowed method values.
func IsValidMethod(m Method) bool {
	switch m {
	case MethodNone, MethodDirect, MethodBuero, MethodManagerBuero, MethodManagerDirect:
		return true
	}
	return false
}

// EvalInput is everything the prompt template needs to render a query.
type EvalInput struct {
	Callsign        string
	QRZ             *qrz.Callsign
	Bio             string
	HeuristicResult qsldetermine.Result
}

// EvalResult is the parsed LLM determination.
type EvalResult struct {
	Method      Method
	Confidence  string // high | medium | low
	Reasoning   string
	ManagerCall string // populated when Method is manager-*
	RawResponse string
	Duration    time.Duration
	ParseError  string // set if the response could not be parsed cleanly
}

// LLMClient abstracts the underlying text-generation backend.
type LLMClient interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// PromptRenderer renders an EvalInput into the prompt string sent to the LLM.
type PromptRenderer interface {
	Render(input EvalInput) (string, error)
}

// Determiner computes the heuristic baseline for a station.
type Determiner interface {
	Determine(callsign string, qrz *qrz.Callsign, bio string) qsldetermine.Result
}

// Evaluator ties together prompt rendering, LLM querying, and response parsing.
type Evaluator struct {
	Client LLMClient
	Prompt PromptRenderer
}

// Evaluate renders the prompt, sends it to the LLM, and parses the response.
func (e *Evaluator) Evaluate(ctx context.Context, input EvalInput) (EvalResult, error) {
	prompt, err := e.Prompt.Render(input)
	if err != nil {
		return EvalResult{}, fmt.Errorf("render prompt: %w", err)
	}

	start := time.Now()
	raw, err := e.Client.Generate(ctx, prompt)
	dur := time.Since(start)
	if err != nil {
		return EvalResult{RawResponse: raw, Duration: dur}, fmt.Errorf("llm generate: %w", err)
	}

	res := ParseResponse(raw)
	res.Duration = dur
	return res, nil
}

// MapHeuristic maps the existing qsldetermine.Result to the LLM-facing Method
// vocabulary. The raw heuristic result is preserved in the output for comparison.
func MapHeuristic(r qsldetermine.Result) Method {
	switch r.Method {
	case "B":
		return MethodBuero
	case "D":
		return MethodDirect
	case "E":
		return MethodNone
	case "M":
		if r.Manager == "" {
			return MethodNone
		}
		lc := strings.ToLower(r.Reason)
		if strings.Contains(lc, "bureau") || strings.Contains(lc, "buro") {
			return MethodManagerBuero
		}
		return MethodManagerDirect
	}
	// Method is empty.
	if r.RefusePaper {
		return MethodNone
	}
	return MethodNone
}
