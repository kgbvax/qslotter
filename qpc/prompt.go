package qpc

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed prompts/*.tmpl
var builtinPrompts embed.FS

// Prompt is a parsed prompt template. A template file defines two blocks,
// {{define "system"}} and {{define "user"}}, rendered with PromptData, and
// may define {{define "answer"}}routes{{end}} for the routes answer (status,
// routes, preferred, via, note); without it the answer is a single label
// (prompts v1, v2).
type Prompt struct {
	Name   string // file name without extension
	Hash   string // first 8 hex digits of the SHA-256 of the file (and Rules, if used)
	Answer string // "routes-contribution", "routes" or "label"
	tmpl   *template.Template
}

// ID identifies the exact prompt text a result came from.
func (p *Prompt) ID() string { return p.Name + "@" + p.Hash }

// PromptData is what a prompt template sees.
type PromptData struct {
	Station            // .Call, .Country, .QSLMgr, .MQSL, .EQSL, .LoTW, ...
	Bio       string   // the bio after preparation (whitespace, length cap)
	Truncated bool     // Bio was cut
	Rules     string   // LABELS.md
	Labels    []string // the single-label scheme (v1, v2)
	Statuses  []Status
	Routes    []Route
}

// LoadPrompt returns a built-in prompt by name ("v1") or reads a template
// file when nameOrPath names one.
func LoadPrompt(nameOrPath string) (*Prompt, error) {
	var (
		src  []byte
		err  error
		name = strings.TrimSuffix(filepath.Base(nameOrPath), ".tmpl")
	)
	if strings.ContainsAny(nameOrPath, `/\`) || strings.HasSuffix(nameOrPath, ".tmpl") {
		src, err = os.ReadFile(nameOrPath)
	} else {
		src, err = builtinPrompts.ReadFile("prompts/" + nameOrPath + ".tmpl")
	}
	if err != nil {
		return nil, fmt.Errorf("prompt %q: %w", nameOrPath, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("prompt %q: %w", nameOrPath, err)
	}
	for _, block := range []string{"system", "user"} {
		if t.Lookup(block) == nil {
			return nil, fmt.Errorf("prompt %q: no {{define %q}} block", nameOrPath, block)
		}
	}
	// A template using {{.Rules}} includes LABELS.md; hashing it too means an
	// edit there yields a new prompt ID.
	hashed := src
	if bytes.Contains(src, []byte(".Rules")) {
		hashed = append(append(append([]byte{}, src...), 0), Rules...)
	}
	sum := sha256.Sum256(hashed)
	p := &Prompt{Name: name, Hash: hex.EncodeToString(sum[:])[:8], Answer: "label", tmpl: t}
	if t.Lookup("answer") != nil {
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, "answer", nil); err != nil {
			return nil, fmt.Errorf("prompt %q: %w", nameOrPath, err)
		}
		switch a := strings.TrimSpace(b.String()); a {
		case "routes-contribution", "routes", "label":
			p.Answer = a
		default:
			return nil, fmt.Errorf("prompt %q: answer %q (want routes or label)", nameOrPath, a)
		}
	}
	return p, nil
}

// schema returns the response_format for Format "schema".
func (p *Prompt) schema() json.RawMessage {
	switch p.Answer {
	case "routes-contribution":
		return routesContributionSchema
	case "routes":
		return routesSchema
	}
	return labelSchema
}

// Render returns the system and user messages for d.
func (p *Prompt) Render(d PromptData) (system, user string, err error) {
	var sb, ub bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&sb, "system", d); err != nil {
		return "", "", err
	}
	if err := p.tmpl.ExecuteTemplate(&ub, "user", d); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(sb.String()), strings.TrimSpace(ub.String()), nil
}

// The JSON schemas for Format "schema". The property order is the order the
// model writes: quoting the evidence before deciding gives a small model a
// moment of reasoning even under constrained decoding.
var routesSchema = func() json.RawMessage {
	statuses, _ := json.Marshal(Statuses)
	routes, _ := json.Marshal(AllRoutes)
	preferred, _ := json.Marshal(append([]Route{""}, AllRoutes...))
	return json.RawMessage(`{"type":"json_schema","json_schema":{"name":"qsl_preference","strict":true,"schema":{` +
		`"type":"object","properties":{` +
		`"evidence":{"type":"string"},` +
		`"status":{"type":"string","enum":` + string(statuses) + `},` +
		`"routes":{"type":"array","items":{"type":"string","enum":` + string(routes) + `}},` +
		`"preferred":{"type":"string","enum":` + string(preferred) + `},` +
		`"via":{"type":"string"},` +
		`"note":{"type":"string"},` +
		`"confidence":{"type":"string","enum":["high","medium","low"]}` +
		`},"required":["evidence","status","routes","preferred","via","note","confidence"],"additionalProperties":false}}}`)
}()

// routesContributionSchema is routesSchema plus the contribution flag (prompts from v7).
var routesContributionSchema = func() json.RawMessage {
	statuses, _ := json.Marshal(Statuses)
	routes, _ := json.Marshal(AllRoutes)
	preferred, _ := json.Marshal(append([]Route{""}, AllRoutes...))
	return json.RawMessage(`{"type":"json_schema","json_schema":{"name":"qsl_preference","strict":true,"schema":{` +
		`"type":"object","properties":{` +
		`"evidence":{"type":"string"},` +
		`"status":{"type":"string","enum":` + string(statuses) + `},` +
		`"routes":{"type":"array","items":{"type":"string","enum":` + string(routes) + `}},` +
		`"preferred":{"type":"string","enum":` + string(preferred) + `},` +
		`"via":{"type":"string"},` +
		`"contribution":{"type":"string","enum":["","required","not-needed"]},` +
		`"note":{"type":"string"},` +
		`"confidence":{"type":"string","enum":["high","medium","low"]}` +
		`},"required":["evidence","status","routes","preferred","via","contribution","note","confidence"],"additionalProperties":false}}}`)
}()

var labelSchema = func() json.RawMessage {
	labels, _ := json.Marshal(LegacyLabels)
	return json.RawMessage(`{"type":"json_schema","json_schema":{"name":"qsl_preference","strict":true,"schema":{` +
		`"type":"object","properties":{` +
		`"evidence":{"type":"string"},` +
		`"label":{"type":"string","enum":` + string(labels) + `},` +
		`"via":{"type":"string"},` +
		`"confidence":{"type":"string","enum":["high","medium","low"]}` +
		`},"required":["evidence","label","via","confidence"],"additionalProperties":false}}}`)
}()

var jsonObjectFormat = json.RawMessage(`{"type":"json_object"}`)
