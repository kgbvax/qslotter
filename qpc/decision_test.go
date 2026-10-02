package qpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuiltinDecisionsLoad(t *testing.T) {
	for name, layout := range map[string]string{"d1-split": "split", "d1-single": "single"} {
		d, err := LoadDecision(name)
		if err != nil {
			t.Fatal(err)
		}
		if d.Layout != layout || len(d.Hash) != 8 {
			t.Errorf("%s: %+v", name, d)
		}
		// The questions keep the file's order, and so do the options.
		var keys []string
		dec := json.NewDecoder(strings.NewReader(string(d.questions)))
		dec.Token()
		for dec.More() {
			tok, _ := dec.Token()
			keys = append(keys, tok.(string))
			var skip json.RawMessage
			dec.Decode(&skip)
		}
		if keys[0] != map[string]string{"split": "status", "single": "answer"}[layout] || keys[len(keys)-1] != "contribution" {
			t.Errorf("%s: question order %v", name, keys)
		}
	}
	if !strings.Contains(string(mustDecision(t, "d1-single").questions), `"bureau":"Via the QSL bureau`) {
		t.Error("option descriptions lost")
	}
}

func mustDecision(t *testing.T, name string) *Decision {
	t.Helper()
	d, err := LoadDecision(name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDecisionSpecErrors(t *testing.T) {
	for src, want := range map[string]string{
		"questions:\n  answer: {type: choice, criteria: {bureau: x, maybe: y}}":                                       `unknown option "maybe"`,
		"questions:\n  status: {type: choice, criteria: {paper: x}}":                                                  `no "bureau" question`,
		"questions:\n  answer: {type: choice, criteria: {bureau: x}}\n  status: {type: choice, criteria: {paper: x}}": "either",
		"questions:\n  note: {type: choice, criteria: {a: b}}":                                                        `"note": unknown`,
		"questions:\n  answer: {type: choice, criteria: {bureau: x}}\n  contribution: {type: noul}":                   "must be a choice",
	} {
		if _, err := parseDecision([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", src, err, want)
		}
	}
}

func p(x float64) *float64 { return &x }

func TestDecisionApply(t *testing.T) {
	split, single := mustDecision(t, "d1-split"), mustDecision(t, "d1-single")
	cases := []struct {
		d       *Decision
		ans     map[string]decisionAnswer
		want    string
		problem string
	}{
		{split, map[string]decisionAnswer{
			"status": {Choice: "paper", Confidence: 0.9}, "bureau": {Noul: p(0.8)}, "direct": {Noul: p(0.6)}, "oqrs": {Noul: p(0.1)},
			"preferred": {Choice: "direct"}, "contribution": {Choice: "required"},
		}, "paper|[bureau direct]|direct|required|high", ""},
		{split, map[string]decisionAnswer{ // paper, but no route at 0.5: the likeliest
			"status": {Choice: "paper", Confidence: 0.5}, "bureau": {Noul: p(0.3)}, "direct": {Noul: p(0.4)}, "oqrs": {Noul: p(0.1)},
		}, "paper|[direct]|||medium", "took the likeliest"},
		{split, map[string]decisionAnswer{ // the status decides
			"status": {Choice: "no-paper", Confidence: 0.2}, "bureau": {Noul: p(0.9)}, "direct": {Noul: p(0.1)}, "oqrs": {Noul: p(0.1)},
			"preferred": {Choice: "bureau"}, "contribution": {Choice: "not-stated"},
		}, "no-paper|[]|||low", `preferred "bureau" is not among the routes`},
		{single, map[string]decisionAnswer{"answer": {Choice: "bureau+direct", Confidence: 0.85}, "preferred": {Choice: "none"}, "contribution": {Choice: "not-needed"}},
			"paper|[bureau direct]||not-needed|high", ""},
		{single, map[string]decisionAnswer{"answer": {Choice: "unclear"}}, "unclear|[]|||low", ""},
		{single, map[string]decisionAnswer{}, "|[]|||", "no answer in the answer"},
	}
	for i, c := range cases {
		var r Result
		c.d.apply(c.ans, &r)
		got := strings.Join([]string{string(r.Status), fmtRoutes(r.Routes), string(r.Preferred), string(r.Contribution), r.Confidence}, "|")
		if got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
		if (c.problem == "") != (r.ParseError == "") || !strings.Contains(r.ParseError, c.problem) {
			t.Errorf("case %d: problem %q, want %q", i, r.ParseError, c.problem)
		}
	}
}

func fmtRoutes(rs []Route) string {
	var s []string
	for _, r := range rs {
		s = append(s, string(r))
	}
	return "[" + strings.Join(s, " ") + "]"
}

func TestFocusBio(t *testing.T) {
	short := "QSL via bureau."
	if got, cut := FocusBio(short, 100); got != short || cut {
		t.Errorf("short bio changed: %q", got)
	}
	long := strings.Repeat("My antenna is a dipole. ", 20) + "QSL direct only, SASE please. " + strings.Repeat("I like CW. ", 20)
	got, cut := FocusBio(long, 200)
	if !cut || !strings.Contains(got, "QSL direct only, SASE please") || strings.Contains(got, "dipole") {
		t.Errorf("focus: %q", got)
	}
	if got, _ := FocusBio(strings.Repeat("My antenna is a dipole. ", 20), 100); !strings.Contains(got, "no sentence") {
		t.Errorf("nothing about QSL: %q", got)
	}
}

func TestDecisionRequestAndResult(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		io.WriteString(w, `{"model":"tev1","answers":{"answer":{"type":"choice","choice":"direct","probabilities":{"direct":0.9,"bureau":0.1},"confidence":0.7},
			"contribution":{"type":"choice","choice":"required","probabilities":{"required":0.95},"confidence":0.9}},"usage":{"input_tokens":812,"output_tokens":3}}`)
	}))
	defer srv.Close()
	c, err := New(Variant{Kind: "decision", BaseURL: srv.URL + "/v1", Model: "tev1", Prompt: "d1-single", BioFocus: true, AddressGuard: true,
		Extra: map[string]any{"keep_alive": "10m"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Variant().BioMaxChars != DefaultDecisionBioMaxChars || c.Variant().Format != "" || c.Variant().Temperature != nil {
		t.Errorf("decision defaults: %+v", c.Variant())
	}
	r, err := c.Classify(context.Background(), Station{Call: "zz1aa", MQSL: "1", Addr1: "Street 1", Addr2: "Town", Bio: "QSL direct, 2 USD"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Paper || fmtRoutes(r.Routes) != "[direct]" || r.Contribution != ContributionRequired || r.Confidence != "medium" ||
		r.PromptTokens != 812 || r.Prompt != c.PromptID() || !strings.Contains(r.Evidence, "answer direct 0.90 bureau 0.10") || r.Guard != "" {
		t.Errorf("result: %+v", r)
	}
	for k, want := range map[string]string{
		"model":      `"tev1"`,
		"keep_alive": `"10m"`,
		"state":      `{"station":"ZZ1AA","country":"not stated","qslmgr (QSL manager or QSL instructions)":"(empty)","mqsl (will return paper QSL)":"yes","eqsl (accepts eQSL)":"not stated","lotw (uses LoTW)":"not stated","QRZ postal address":"full (street and city)","bio":"QSL direct, 2 USD"}`,
	} {
		if string(body[k]) != want {
			t.Errorf("request %s = %s, want %s", k, body[k], want)
		}
	}
	if !strings.HasPrefix(string(body["questions"]), `{"answer":{"type":"choice"`) {
		t.Errorf("questions: %.80s", body["questions"])
	}
	// The address guard also applies: direct without a full address.
	r, _ = c.Classify(context.Background(), Station{Call: "ZZ2BB", Bio: "QSL direct"})
	if r.Status != Unclear || r.Guard == "" {
		t.Errorf("guard: %+v", r)
	}
}

func TestDecisionHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"model \"tev9\" not found"}`)
	}))
	defer srv.Close()
	c, _ := New(Variant{Kind: "decision", BaseURL: srv.URL, Model: "tev9"})
	if _, err := c.Classify(context.Background(), Station{Call: "ZZ1AA"}); err == nil || err.Error() != `HTTP 404: model "tev9" not found` {
		t.Errorf("error: %v", err)
	}
}
