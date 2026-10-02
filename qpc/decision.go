package qpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Decision models (Variant.Kind "decision") answer named questions about a
// piece of text through Ollama's /v1/systemone endpoint (TypeSafe's Jev API):
// each question is a choice from listed options, a true/false probability
// ("noul") or a level on a rubric, and the answer comes back as
// probabilities. They cannot write text, so there is no note and no via
// callsign; the station goes in as a JSON "state" and the rules live in the
// question instructions of a decision spec (qpc/decisions/*.yaml).
//
// A spec names its questions by what they answer:
//
//	answer        choice: the whole answer in one; options are a status
//	              (no-paper, unknown, unclear) or routes joined by "+"
//	              ("bureau+direct")                         - layout "single"
//	status        choice over the statuses, with            - layout "split"
//	bureau, direct, oqrs  noul each: is the route accepted?
//	preferred     choice: none or a route (optional)
//	contribution  choice: required, not-needed, not-stated (optional)

//go:embed decisions/*.yaml
var builtinDecisions embed.FS

// routeThreshold is the probability from which a route counts as accepted.
const routeThreshold = 0.5

// Decision is a parsed decision spec.
type Decision struct {
	Name   string // file name without extension
	Hash   string // first 8 hex digits of the SHA-256 of the file
	Layout string // "single" or "split"
	// questions is the "questions" object as JSON, in the file's order (the
	// order the model sees them in).
	questions json.RawMessage
	options   map[string][]string // choice question -> option names
}

// ID identifies the exact spec a result came from.
func (d *Decision) ID() string { return d.Name + "@" + d.Hash }

// LoadDecision returns a built-in decision spec by name ("d1-split") or reads
// a YAML file when nameOrPath names one.
func LoadDecision(nameOrPath string) (*Decision, error) {
	var (
		src  []byte
		err  error
		name = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(nameOrPath), ".yaml"), ".yml")
	)
	if strings.ContainsAny(nameOrPath, `/\`) || strings.HasSuffix(nameOrPath, ".yaml") || strings.HasSuffix(nameOrPath, ".yml") {
		src, err = os.ReadFile(nameOrPath)
	} else {
		src, err = builtinDecisions.ReadFile("decisions/" + nameOrPath + ".yaml")
	}
	if err != nil {
		return nil, fmt.Errorf("decision spec %q: %w", nameOrPath, err)
	}
	d, err := parseDecision(src)
	if err != nil {
		return nil, fmt.Errorf("decision spec %q: %w", nameOrPath, err)
	}
	sum := sha256.Sum256(src)
	d.Name, d.Hash = name, hex.EncodeToString(sum[:])[:8]
	return d, nil
}

func parseDecision(src []byte) (*Decision, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("want a mapping with a questions key")
	}
	qs := mapValue(doc.Content[0], "questions")
	if qs == nil || qs.Kind != yaml.MappingNode || len(qs.Content) == 0 {
		return nil, fmt.Errorf("no questions")
	}
	d := &Decision{options: map[string][]string{}}
	types := map[string]string{}
	for i := 0; i+1 < len(qs.Content); i += 2 {
		name, q := qs.Content[i].Value, qs.Content[i+1]
		t := mapValue(q, "type")
		if t == nil {
			return nil, fmt.Errorf("question %q: no type", name)
		}
		types[name] = t.Value
		if t.Value == "choice" {
			c := mapValue(q, "criteria")
			if c == nil || c.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("question %q: a choice needs criteria (option: description)", name)
			}
			for j := 0; j < len(c.Content); j += 2 {
				d.options[name] = append(d.options[name], c.Content[j].Value)
			}
		}
	}
	want := func(name, typ string, allowed func(string) bool) error {
		if t, ok := types[name]; ok {
			if t != typ {
				return fmt.Errorf("question %q must be a %s", name, typ)
			}
			for _, o := range d.options[name] {
				if !allowed(o) {
					return fmt.Errorf("question %q: unknown option %q", name, o)
				}
			}
		}
		return nil
	}
	for name := range types {
		switch name {
		case "answer", "status", "bureau", "direct", "oqrs", "preferred", "contribution":
		default:
			return nil, fmt.Errorf("question %q: unknown (want answer, status, bureau, direct, oqrs, preferred, contribution)", name)
		}
	}
	_, single := types["answer"]
	_, split := types["status"]
	switch {
	case single && !split:
		d.Layout = "single"
	case split && !single:
		d.Layout = "split"
		for _, r := range AllRoutes {
			if _, ok := types[string(r)]; !ok {
				return nil, fmt.Errorf("layout split: no %q question", r)
			}
		}
	default:
		return nil, fmt.Errorf("want either an answer question or a status question")
	}
	for _, err := range []error{
		want("answer", "choice", func(o string) bool { s, _ := parseAnswerOption(o); return s != "" }),
		want("status", "choice", func(o string) bool { return Status(o).Valid() }),
		want("bureau", "noul", nil),
		want("direct", "noul", nil),
		want("oqrs", "noul", nil),
		want("preferred", "choice", func(o string) bool { return o == "none" || Route(o).Valid() }),
		want("contribution", "choice", func(o string) bool {
			return o == "not-stated" || o == string(ContributionRequired) || o == string(ContributionNotNeeded)
		}),
	} {
		if err != nil {
			return nil, err
		}
	}
	raw, err := nodeJSON(qs)
	if err != nil {
		return nil, err
	}
	d.questions = raw
	return d, nil
}

// parseAnswerOption reads an option of the answer question: a status other
// than paper, or routes joined by "+".
func parseAnswerOption(o string) (Status, []Route) {
	if s := Status(o); s.Valid() && s != Paper {
		return s, nil
	}
	var routes []Route
	for _, p := range strings.Split(o, "+") {
		r := Route(strings.TrimSpace(p))
		if !r.Valid() {
			return "", nil
		}
		routes = append(routes, r)
	}
	return Paper, SortRoutes(routes)
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// nodeJSON converts a YAML node to JSON, keeping the order of mapping keys.
func nodeJSON(n *yaml.Node) (json.RawMessage, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		return nodeJSON(n.Content[0])
	case yaml.AliasNode:
		return nodeJSON(n.Alias)
	case yaml.MappingNode:
		var b bytes.Buffer
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(n.Content[i].Value)
			b.Write(k)
			b.WriteByte(':')
			v, err := nodeJSON(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			b.Write(v)
		}
		b.WriteByte('}')
		return b.Bytes(), nil
	case yaml.SequenceNode:
		var b bytes.Buffer
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			v, err := nodeJSON(c)
			if err != nil {
				return nil, err
			}
			b.Write(v)
		}
		b.WriteByte(']')
		return b.Bytes(), nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// decisionState is the station as the decision model sees it: plain-language
// keys, since there is no system prompt to explain the QRZ fields.
func decisionState(st Station, bio string) json.RawMessage {
	flag := func(v string) string {
		switch strings.TrimSpace(v) {
		case "1":
			return "yes"
		case "0":
			return "no"
		}
		return "not stated"
	}
	addr := "none"
	switch {
	case st.HasFullAddress():
		addr = "full (street and city)"
	case strings.TrimSpace(st.Addr1+st.Addr2+st.Zip) != "":
		addr = "partial (street or city missing)"
	}
	or := func(s, empty string) string {
		if strings.TrimSpace(s) == "" {
			return empty
		}
		return s
	}
	fields := [][2]string{
		{"station", st.Call},
		{"country", or(st.Country, "not stated")},
		{"qslmgr (QSL manager or QSL instructions)", or(st.QSLMgr, "(empty)")},
		{"mqsl (will return paper QSL)", flag(st.MQSL)},
		{"eqsl (accepts eQSL)", flag(st.EQSL)},
		{"lotw (uses LoTW)", flag(st.LoTW)},
		{"QRZ postal address", addr},
		{"bio", or(bio, "(no bio)")},
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f[0])
		v, _ := json.Marshal(f[1])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// decisionRequest returns the /v1/systemone request body for st.
func (d *Decision) request(model string, st Station, bio string, extra map[string]any) ([]byte, error) {
	body := map[string]json.RawMessage{"state": decisionState(st, bio), "questions": d.questions}
	m, _ := json.Marshal(model)
	body["model"] = m
	for k, v := range extra {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", k, err)
		}
		body[k] = raw
	}
	return json.Marshal(body)
}

type decisionAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Noul          *float64           `json:"noul"`
}

type decisionResponse struct {
	Answers map[string]decisionAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// decide posts one request to {baseURL}/systemone.
func decide(ctx context.Context, hc *http.Client, baseURL, apiKey string, body []byte) (decisionResponse, string, error) {
	var dr decisionResponse
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/systemone", bytes.NewReader(body))
	if err != nil {
		return dr, "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		hr.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := hc.Do(hr)
	if err != nil {
		return dr, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return dr, "", err
	}
	jsonErr := json.Unmarshal(raw, &dr)
	if resp.StatusCode != http.StatusOK {
		if jsonErr == nil && len(dr.Error) > 0 {
			var msg string
			var obj struct{ Message string }
			if json.Unmarshal(dr.Error, &msg) != nil && json.Unmarshal(dr.Error, &obj) == nil {
				msg = obj.Message
			}
			if msg != "" {
				return dr, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
			}
		}
		return dr, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(string(raw)))
	}
	if jsonErr != nil {
		return dr, "", fmt.Errorf("decode response: %w", jsonErr)
	}
	return dr, string(raw), nil
}

// apply turns the answers into r: status, routes, preferred, contribution,
// a confidence word and the probabilities as evidence. Problems go to
// r.ParseError; r.Status stays empty when the main answer is missing.
func (d *Decision) apply(ans map[string]decisionAnswer, r *Result) {
	var problems, ev []string
	main := "status"
	if d.Layout == "single" {
		main = "answer"
	}
	a, ok := ans[main]
	if !ok || a.Choice == "" {
		r.ParseError = "no " + main + " in the answer"
		return
	}
	ev = append(ev, fmt.Sprintf("%s %s", main, probs(a.Probabilities)))
	switch d.Layout {
	case "single":
		r.Status, r.Routes = parseAnswerOption(a.Choice)
		if r.Status == "" {
			problems = append(problems, fmt.Sprintf("unknown answer %q", a.Choice))
		}
	case "split":
		st := Status(a.Choice)
		if !st.Valid() {
			r.ParseError = fmt.Sprintf("unknown status %q", a.Choice)
			return
		}
		var parts []string
		best, bestP := Route(""), -1.0
		for _, rt := range AllRoutes {
			x, ok := ans[string(rt)]
			if !ok || x.Noul == nil {
				problems = append(problems, fmt.Sprintf("no %s answer", rt))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %.2f", rt, *x.Noul))
			if *x.Noul >= routeThreshold {
				r.Routes = append(r.Routes, rt)
			}
			if *x.Noul > bestP {
				best, bestP = rt, *x.Noul
			}
		}
		ev = append(ev, strings.Join(parts, " "))
		r.Status = st
		switch {
		case st != Paper:
			r.Routes = nil // the status decides; route answers only count for paper
		case len(r.Routes) == 0 && best != "":
			r.Routes = []Route{best}
			problems = append(problems, fmt.Sprintf("paper but no route at %.1f, took the likeliest (%s)", routeThreshold, best))
		case len(r.Routes) == 0:
			r.Status = ""
			problems = append(problems, "paper without routes")
		}
	}
	r.Confidence = confidenceWord(a.Confidence)
	if p, ok := ans["preferred"]; ok {
		ev = append(ev, "preferred "+probs(p.Probabilities))
		if rt := Route(p.Choice); rt.Valid() {
			if HasRoute(r.Routes, rt) {
				r.Preferred = rt
			} else {
				problems = append(problems, fmt.Sprintf("preferred %q is not among the routes", p.Choice))
			}
		}
	}
	if c, ok := ans["contribution"]; ok {
		ev = append(ev, "contribution "+probs(c.Probabilities))
		switch Contribution(c.Choice) {
		case ContributionRequired, ContributionNotNeeded:
			r.Contribution = Contribution(c.Choice)
		}
	}
	r.Evidence = strings.Join(ev, "; ")
	r.ParseError = strings.Join(problems, "; ")
}

// confidenceWord maps the API's confidence (how concentrated the
// probabilities are, not the chance of being right) to the words the report
// uses. The cut-offs are arbitrary.
func confidenceWord(c float64) string {
	switch {
	case c >= 0.8:
		return "high"
	case c >= 0.4:
		return "medium"
	}
	return "low"
}

// probs renders choice probabilities, likeliest first: "paper .93 no-paper .05".
func probs(p map[string]float64) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if p[keys[i]] != p[keys[j]] {
			return p[keys[i]] > p[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		if p[k] >= 0.01 || len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("%s %.2f", k, math.Round(p[k]*100)/100))
		}
	}
	return strings.Join(parts, " ")
}
