package llmqsl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
)

func TestIsValidMethod(t *testing.T) {
	if !IsValidMethod(MethodDirect) {
		t.Fatal("expected direct to be valid")
	}
	if IsValidMethod(Method("bogus")) {
		t.Fatal("expected bogus to be invalid")
	}
}

func TestMapHeuristic(t *testing.T) {
	cases := []struct {
		name string
		in   qsldetermine.Result
		want Method
	}{
		{"bureau", qsldetermine.Result{Method: "B"}, MethodBuero},
		{"direct", qsldetermine.Result{Method: "D"}, MethodDirect},
		{"electronic", qsldetermine.Result{Method: "E"}, MethodNone},
		{"manager direct", qsldetermine.Result{Method: "M", Manager: "XX1ABC", Reason: "QSL via manager"}, MethodManagerDirect},
		{"manager buero", qsldetermine.Result{Method: "M", Manager: "XX1ABC", Reason: "QSL via bureau"}, MethodManagerBuero},
		{"manager no reason", qsldetermine.Result{Method: "M", Manager: "XX1ABC"}, MethodManagerDirect},
		{"no method refuse", qsldetermine.Result{RefusePaper: true}, MethodNone},
		{"no method no signal", qsldetermine.Result{}, MethodNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MapHeuristic(c.in)
			if got != c.want {
				t.Fatalf("MapHeuristic(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseResponse(t *testing.T) {
	cases := []struct {
		name           string
		raw            string
		wantMethod     Method
		wantConfidence string
		wantManager    string
		wantError      string
	}{
		{
			name:           "valid json",
			raw:            `{"method":"direct","confidence":"high","reasoning":"bio says direct only","manager_call":""}`,
			wantMethod:     MethodDirect,
			wantConfidence: "high",
		},
		{
			name:           "json in prose",
			raw:            "The answer is direct.\n```json\n{\"method\":\"direct\",\"confidence\":\"medium\",\"reasoning\":\"via bio\",\"manager_call\":\"\"}\n```",
			wantMethod:     MethodDirect,
			wantConfidence: "medium",
		},
		{
			name:           "manager uppercase",
			raw:            `{"method":"manager-buero","confidence":"high","reasoning":"QSL via XX1ABC","manager_call":"xx1abc"}`,
			wantMethod:     MethodManagerBuero,
			wantConfidence: "high",
			wantManager:    "XX1ABC",
		},
		{
			name:           "invalid method",
			raw:            `{"method":"maybe","confidence":"high","reasoning":"unsure"}`,
			wantMethod:     Method("maybe"),
			wantConfidence: "low",
			wantError:      "invalid method",
		},
		{
			name:           "missing manager call",
			raw:            `{"method":"manager-direct","confidence":"high","reasoning":"via manager"}`,
			wantMethod:     MethodManagerDirect,
			wantConfidence: "low",
			wantError:      "requires manager_call",
		},
		{
			name:           "bad manager call",
			raw:            `{"method":"manager-direct","confidence":"high","reasoning":"via manager","manager_call":"not a callsign"}`,
			wantMethod:     MethodManagerDirect,
			wantConfidence: "low",
			wantError:      "does not look like a callsign",
		},
		{
			name:           "malformed json",
			raw:            `this is not json`,
			wantConfidence: "",
			wantError:      "no JSON object found",
		},
		{
			name:           "method cleared for non manager",
			raw:            `{"method":"direct","confidence":"high","reasoning":"direct","manager_call":"XX1ABC"}`,
			wantMethod:     MethodDirect,
			wantConfidence: "high",
			wantManager:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseResponse(c.raw)
			if got.Method != c.wantMethod {
				t.Fatalf("Method = %q, want %q", got.Method, c.wantMethod)
			}
			if got.Confidence != c.wantConfidence {
				t.Fatalf("Confidence = %q, want %q", got.Confidence, c.wantConfidence)
			}
			if got.ManagerCall != c.wantManager {
				t.Fatalf("ManagerCall = %q, want %q", got.ManagerCall, c.wantManager)
			}
			if c.wantError != "" && !strings.Contains(got.ParseError, c.wantError) {
				t.Fatalf("ParseError = %q, want containing %q", got.ParseError, c.wantError)
			}
		})
	}
}

type fakeLLM struct {
	response string
	err      error
}

func (f *fakeLLM) Generate(ctx context.Context, prompt string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

func TestEvaluatorEvaluate(t *testing.T) {
	renderer, err := DefaultPrompt()
	if err != nil {
		t.Fatalf("default prompt: %v", err)
	}
	e := &Evaluator{
		Client: &fakeLLM{response: `{"method":"buero","confidence":"medium","reasoning":"mqsl=Y"}`},
		Prompt: renderer,
	}
	res, err := e.Evaluate(context.Background(), EvalInput{
		Callsign: "DL1ABC",
		QRZ: &qrz.Callsign{
			Call:    "DL1ABC",
			Country: "Germany",
			MQSL:    "Y",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Method != MethodBuero {
		t.Fatalf("Method = %q, want buero", res.Method)
	}
	if res.Duration < 0 {
		t.Fatal("duration should be non-negative")
	}
}

func TestEvaluatorEvaluateGenerateError(t *testing.T) {
	renderer, err := DefaultPrompt()
	if err != nil {
		t.Fatalf("default prompt: %v", err)
	}
	e := &Evaluator{
		Client: &fakeLLM{err: errors.New("ollama down")},
		Prompt: renderer,
	}
	_, err = e.Evaluate(context.Background(), EvalInput{Callsign: "DL1ABC"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDefaultPromptRender(t *testing.T) {
	renderer, err := DefaultPrompt()
	if err != nil {
		t.Fatalf("default prompt: %v", err)
	}
	prompt, err := renderer.Render(EvalInput{
		Callsign: "DL1ABC",
		QRZ: &qrz.Callsign{
			Call:    "DL1ABC",
			Country: "Germany",
			MQSL:    "Y",
			QSLMgr:  "XX1ABC",
		},
		Bio: "QSL via bureau.",
		HeuristicResult: qsldetermine.Result{
			Method: "B",
			Reason: "mqsl=Y",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"DL1ABC", "manager-buero", "QSL via bureau", "mqsl=Y"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDefaultPromptRenderNoQRZ(t *testing.T) {
	renderer, err := DefaultPrompt()
	if err != nil {
		t.Fatalf("default prompt: %v", err)
	}
	prompt, err := renderer.Render(EvalInput{Callsign: "DL1ABC"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(prompt, "(no QRZ data available)") {
		t.Fatalf("expected nil QRZ branch in prompt:\n%s", prompt)
	}
}

func TestCoalesce(t *testing.T) {
	if got := coalesce("", "b", "c"); got != "b" {
		t.Fatalf("coalesce = %q, want b", got)
	}
	if got := coalesce("", "  ", "c"); got != "c" {
		t.Fatalf("coalesce = %q, want c", got)
	}
	if got := coalesce("", " "); got != "" {
		t.Fatalf("coalesce = %q, want empty", got)
	}
}

func TestNormalizeConfidence(t *testing.T) {
	if got := normalizeConfidence("HIGH"); got != "high" {
		t.Fatalf("normalizeConfidence(HIGH) = %q", got)
	}
	if got := normalizeConfidence(""); got != "low" {
		t.Fatalf("normalizeConfidence() = %q", got)
	}
}

func TestOllamaClientDefaults(t *testing.T) {
	c := NewOllamaClient("", "")
	if c.BaseURL != "http://localhost:11434" {
		t.Fatalf("BaseURL = %q", c.BaseURL)
	}
	if c.Model != "ministral-3b" {
		t.Fatalf("Model = %q", c.Model)
	}
	if c.Temperature != 0 {
		t.Fatalf("Temperature = %v", c.Temperature)
	}
	if c.HTTP.Timeout != 2*time.Minute {
		t.Fatalf("Timeout = %v", c.HTTP.Timeout)
	}
}
