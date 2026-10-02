package qpc

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		name, raw  string
		label      Label
		via, error string
	}{
		{"plain", `{"evidence":"QSL via buro","label":"bureau","via":"","confidence":"high"}`, Bureau, "", ""},
		{"fenced", "Sure:\n```json\n{\"label\":\"direct\",\"via\":\"none\"}\n```", Direct, "", ""},
		{"think block", `<think>{"label":"oqrs"}</think>{"label":"no-paper","via":""}`, NoPaper, "", ""},
		{"prose around", `The answer is {"label":"Unclear","via":"via ea5gl"} done.`, Unclear, "EA5GL", ""},
		{"portable via", `{"label":"bureau","via":"VK9/DL1ABC"}`, Bureau, "VK9/DL1ABC", ""},
		{"label spelling", `{"label":"No Paper","via":""}`, NoPaper, "", ""},
		{"invalid label", `{"label":"manager","via":"EA5GL"}`, "", "", `invalid label "manager"`},
		{"via not a call", `{"label":"bureau","via":"see website"}`, Bureau, "", `via "see website" is not a callsign`},
		{"no json", `I think bureau.`, "", "", "no JSON object in the answer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r Result
			parseAnswer(c.raw, &r)
			if r.Label != c.label || r.Via != c.via || r.ParseError != c.error {
				t.Errorf("got label=%q via=%q err=%q, want %q %q %q", r.Label, r.Via, r.ParseError, c.label, c.via, c.error)
			}
			if r.Raw != c.raw {
				t.Errorf("raw not kept")
			}
		})
	}
}

func TestPrepareBio(t *testing.T) {
	got, cut := PrepareBio("  Hello \t world  \n\n\n\n  QSL  via   buro ", 0)
	if got != "Hello world\n\nQSL via buro" || cut {
		t.Errorf("got %q cut=%v", got, cut)
	}
	got, cut = PrepareBio("one two three four", 10)
	if got != "one two [...]" || !cut {
		t.Errorf("got %q cut=%v", got, cut)
	}
	got, cut = PrepareBio("ÄÖÜäöü", 3) // runes, not bytes; no space to cut at
	if got != "ÄÖÜ [...]" || !cut {
		t.Errorf("got %q cut=%v", got, cut)
	}
}

func TestBuiltinPromptRenders(t *testing.T) {
	c, err := New(Variant{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	sys, user, _, err := c.Messages(Station{Call: "EA8/DL1ABC", MQSL: "1", Bio: "QSL via DL1ABC"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Route labels:", "bureau|direct|oqrs|unclear|no-paper|unknown"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"Station: EA8/DL1ABC", "mqsl: 1", "qslmgr: (empty)", "QSL via DL1ABC"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt lacks %q:\n%s", want, user)
		}
	}
	if !strings.HasPrefix(c.PromptID(), "v1@") {
		t.Errorf("prompt ID %q", c.PromptID())
	}
}

func TestPromptFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mine.tmpl")
	os.WriteFile(p, []byte(`{{define "system"}}S{{end}}{{define "user"}}U {{.Call}}{{end}}`), 0o644)
	pr, err := LoadPrompt(p)
	if err != nil {
		t.Fatal(err)
	}
	s, u, err := pr.Render(PromptData{Station: Station{Call: "K1A"}})
	if err != nil || s != "S" || u != "U K1A" || pr.Name != "mine" {
		t.Errorf("got %q %q %v name=%q", s, u, err, pr.Name)
	}
	os.WriteFile(p, []byte(`{{define "system"}}S{{end}}`), 0o644)
	if _, err := LoadPrompt(p); err == nil {
		t.Error("prompt without a user block accepted")
	}
}

func TestVariantOver(t *testing.T) {
	half := 0.5
	def := Variant{BaseURL: "http://a/v1", Model: "m1", Extra: map[string]any{"think": false, "keep": 1}}
	v := Variant{Name: "x", Model: "m2", Temperature: &half, Extra: map[string]any{"think": nil, "new": "y"}}.Over(def).WithDefaults()
	if v.BaseURL != "http://a/v1" || v.Model != "m2" || *v.Temperature != 0.5 || v.Prompt != "v1" {
		t.Errorf("merge: %+v", v)
	}
	if _, ok := v.Extra["think"]; ok || v.Extra["keep"] != 1 || v.Extra["new"] != "y" {
		t.Errorf("extra merge: %v", v.Extra)
	}
	if def.Extra["think"] != false {
		t.Error("defaults modified")
	}
}

func TestClassifyRequestAndResult(t *testing.T) {
	var got map[string]json.RawMessage
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"evidence\":\"QSL via EA5GL direct\",\"label\":\"direct\",\"via\":\"ea5gl\",\"confidence\":\"high\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":812,"completion_tokens":31}}`)
	}))
	defer srv.Close()
	t.Setenv("QPC_TEST_KEY", "secret")
	cfg := filepath.Join(t.TempDir(), "qpc.yaml")
	os.WriteFile(cfg, []byte("base_url: "+srv.URL+"/v1\napi_key: ${QPC_TEST_KEY}\nmodel: qwen3.5:4b\nseed: 7\nextra:\n  think: false\n"), 0o644)
	v, err := LoadVariant(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(v)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Classify(context.Background(), Station{Call: "ea8/dl1abc", Bio: "QSL via EA5GL direct"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Label != Direct || r.Via != "EA5GL" || r.Confidence != "high" || r.PromptTokens != 812 || r.Call != "EA8/DL1ABC" || r.FinishReason != "stop" {
		t.Errorf("result %+v", r)
	}
	if auth != "Bearer secret" {
		t.Errorf("auth header %q", auth)
	}
	if string(got["think"]) != "false" || string(got["seed"]) != "7" || string(got["temperature"]) != "0" || string(got["model"]) != `"qwen3.5:4b"` {
		t.Errorf("request body: think=%s seed=%s temp=%s model=%s", got["think"], got["seed"], got["temperature"], got["model"])
	}
	// The schema keeps evidence before label (the order the model writes).
	rf := string(got["response_format"])
	if i, j := strings.Index(rf, `"evidence"`), strings.Index(rf, `"label"`); i < 0 || j < i {
		t.Errorf("schema property order: %s", rf)
	}
}

func TestClassifyTimeoutAndHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Slow") == "" && strings.Contains(r.URL.Path, "slow") {
			time.Sleep(300 * time.Millisecond)
		}
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"model \"x\" not found"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{}"}}]}`)
	}))
	defer srv.Close()

	c, _ := New(Variant{BaseURL: srv.URL + "/slow", Model: "x", Timeout: 50 * time.Millisecond})
	if _, err := c.Classify(context.Background(), Station{Call: "K1A"}); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("timeout not honoured: %v", err)
	}
	c, _ = New(Variant{BaseURL: srv.URL + "/missing", Model: "x"})
	if _, err := c.Classify(context.Background(), Station{Call: "K1A"}); err == nil || !strings.Contains(err.Error(), `HTTP 404: model "x" not found`) {
		t.Errorf("error: %v", err)
	}
}

// qpc must stay deployable on its own: no qslotter internals.
func TestNoInternalImports(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		pf, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range pf.Imports {
			if strings.Contains(im.Path.Value, "github.com/dl9et/qslotter/") {
				t.Errorf("%s imports %s", f, im.Path.Value)
			}
		}
	}
}
