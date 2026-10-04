package llmqsl

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
)

//go:embed default_prompt.txt
var defaultPromptTemplate string

// DefaultPrompt returns the embedded default prompt renderer.
func DefaultPrompt() (PromptRenderer, error) {
	return NewPrompt(defaultPromptTemplate)
}

// NewPrompt builds a PromptRenderer from a Go text/template string.
// It injects helpers upper, coalesce, and formatCallsign.
func NewPrompt(src string) (PromptRenderer, error) {
	t, err := template.New("qsl").Funcs(template.FuncMap{
		"upper":          strings.ToUpper,
		"lower":          strings.ToLower,
		"coalesce":       coalesce,
		"formatCallsign": formatCallsign,
	}).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse prompt template: %w", err)
	}
	return &tmplRenderer{t: t}, nil
}

type tmplRenderer struct {
	t *template.Template
}

func (r *tmplRenderer) Render(input EvalInput) (string, error) {
	var b strings.Builder
	if err := r.t.Execute(&b, makeTmplData(input)); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}
	return b.String(), nil
}

// promptData exposes a flat, prompt-friendly view of the input.
// It is intentionally conservative: only fields that help the LLM decide.
type promptData struct {
	Callsign        string
	QRZ             *qrz.Callsign
	Bio             string
	HasQRZ          bool
	HeuristicMethod string
	HeuristicReason string
	HeuristicMapped Method
}

func makeTmplData(input EvalInput) promptData {
	return promptData{
		Callsign:        strings.ToUpper(strings.TrimSpace(input.Callsign)),
		QRZ:             input.QRZ,
		Bio:             input.Bio,
		HasQRZ:          input.QRZ != nil,
		HeuristicMethod: coalesce(input.Heuristic.Suggest(), "(nothing stated)"),
		HeuristicReason: heuristicReason(input.Heuristic),
		HeuristicMapped: MapHeuristic(input.Heuristic),
	}
}

// heuristicReason is the classifier's own reason.
func heuristicReason(a qsldetermine.Result) string { return a.Reason }

// coalesce returns the first non-empty string argument.
func coalesce(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// formatCallsign is a defensive formatter for manager callsigns.
func formatCallsign(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
