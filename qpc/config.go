package qpc

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Variant is one classifier setup: endpoint, model, prompt and request knobs.
// cmd/qpcd reads a single Variant as its config file; an experiment file in
// cmd/qpc-lab holds defaults plus a list of Variants to compare.
type Variant struct {
	Name string `yaml:"name" json:"name,omitempty"`
	// Kind is "llm" (default, chat completions), "decision" (a Jev-style
	// decision model behind Ollama's /v1/systemone; Prompt names a decision
	// spec) or "heuristic" (qslotter's rule-based baseline, only in
	// cmd/qpc-lab).
	Kind    string `yaml:"kind" json:"kind,omitempty"`
	BaseURL string `yaml:"base_url" json:"base_url,omitempty"` // OpenAI-compatible, e.g. http://localhost:11434/v1
	APIKey  string `yaml:"api_key" json:"-"`                   // sent as a bearer token if set; ${ENV} is expanded
	Model   string `yaml:"model" json:"model,omitempty"`
	// Prompt is a built-in prompt name ("v1") or a path to a template file;
	// for a decision variant a decision spec name ("d1-split") or YAML path.
	Prompt      string   `yaml:"prompt" json:"prompt,omitempty"`
	Temperature *float64 `yaml:"temperature" json:"temperature,omitempty"`
	Seed        *int     `yaml:"seed" json:"seed,omitempty"`
	MaxTokens   int      `yaml:"max_tokens" json:"max_tokens,omitempty"`
	// BioMaxChars caps the bio sent to the model; the rest is cut.
	BioMaxChars int `yaml:"bio_max_chars" json:"bio_max_chars,omitempty"`
	// BioFocus reduces a bio longer than BioMaxChars to its sentences about
	// QSL cards before cutting it (FocusBio), for models with a small context.
	BioFocus bool `yaml:"bio_focus" json:"bio_focus,omitempty"`
	// Format is how the answer is constrained: "schema" (JSON schema via
	// response_format), "json" (any JSON object) or "none" (prompt only).
	Format string `yaml:"format" json:"format,omitempty"`
	// Extra is merged into the request body as is, for server-specific knobs
	// such as Ollama's think:false or reasoning_effort. A null value removes a
	// key set by the defaults.
	Extra   map[string]any `yaml:"extra" json:"extra,omitempty"`
	Timeout time.Duration  `yaml:"timeout" json:"timeout,omitempty"`
	// AddressGuard checks the model's answer in code: direct without a full
	// QRZ postal address becomes unclear. It cannot see an address written in
	// the bio or other routes the record accepts.
	AddressGuard bool `yaml:"address_guard" json:"address_guard,omitempty"`
	// FlagRule applies LABELS.md rule 6 in code when the model answers
	// "unknown" (the text says nothing about paper cards): mqsl 0 = no-paper,
	// a full postal address with mqsl 1 or empty = direct, mqsl 1 = bureau.
	// Prompts that hide the flags from the model (v11) declare it themselves.
	FlagRule bool `yaml:"flag_rule" json:"flag_rule,omitempty"`
}

// Defaults applied by WithDefaults.
const (
	DefaultBaseURL  = "http://localhost:11434/v1"
	DefaultPrompt   = "v13"
	DefaultDecision = "d1-split"
	// DefaultDecisionBioMaxChars fits Tev1's context of about 2,000 tokens.
	DefaultDecisionBioMaxChars = 1500
	DefaultMaxTokens           = 512
	DefaultBioMaxChars         = 6000
	DefaultTimeout             = 2 * time.Minute
)

// Over returns v with every unset field taken from def. Extra is merged key by
// key, v winning; a nil value in v deletes the key.
func (v Variant) Over(def Variant) Variant {
	out := def
	if v.Name != "" {
		out.Name = v.Name
	}
	if v.Kind != "" {
		out.Kind = v.Kind
	}
	if v.BaseURL != "" {
		out.BaseURL = v.BaseURL
	}
	if v.APIKey != "" {
		out.APIKey = v.APIKey
	}
	if v.Model != "" {
		out.Model = v.Model
	}
	if v.Prompt != "" {
		out.Prompt = v.Prompt
	}
	if v.Temperature != nil {
		out.Temperature = v.Temperature
	}
	if v.Seed != nil {
		out.Seed = v.Seed
	}
	if v.MaxTokens != 0 {
		out.MaxTokens = v.MaxTokens
	}
	if v.BioMaxChars != 0 {
		out.BioMaxChars = v.BioMaxChars
	}
	if v.Format != "" {
		out.Format = v.Format
	}
	if v.Timeout != 0 {
		out.Timeout = v.Timeout
	}
	out.AddressGuard = def.AddressGuard || v.AddressGuard
	out.FlagRule = def.FlagRule || v.FlagRule
	out.BioFocus = def.BioFocus || v.BioFocus
	if len(def.Extra) > 0 || len(v.Extra) > 0 {
		out.Extra = map[string]any{}
		for k, x := range def.Extra {
			out.Extra[k] = x
		}
		for k, x := range v.Extra {
			if x == nil {
				delete(out.Extra, k)
				continue
			}
			out.Extra[k] = x
		}
	}
	return out
}

// WithDefaults fills the fields every LLM variant needs.
func (v Variant) WithDefaults() Variant {
	if v.Kind == "" {
		v.Kind = "llm"
	}
	if v.BaseURL == "" {
		v.BaseURL = DefaultBaseURL
	}
	if v.Timeout == 0 {
		v.Timeout = DefaultTimeout
	}
	if v.Name == "" {
		v.Name = v.Model
	}
	if v.Kind == "decision" {
		// No sampling knobs and no answer format: the endpoint scores options.
		if v.Prompt == "" {
			v.Prompt = DefaultDecision
		}
		if v.BioMaxChars == 0 {
			v.BioMaxChars = DefaultDecisionBioMaxChars
		}
		v.Temperature, v.Seed, v.MaxTokens, v.Format = nil, nil, 0, ""
		return v
	}
	if v.Prompt == "" {
		v.Prompt = DefaultPrompt
	}
	if v.Temperature == nil {
		zero := 0.0
		v.Temperature = &zero
	}
	if v.MaxTokens == 0 {
		v.MaxTokens = DefaultMaxTokens
	}
	if v.BioMaxChars == 0 {
		v.BioMaxChars = DefaultBioMaxChars
	}
	if v.Format == "" {
		v.Format = "schema"
	}
	return v
}

// Check reports a variant that cannot run.
func (v Variant) Check() error {
	switch v.Kind {
	case "llm":
	case "heuristic":
		return nil
	case "decision":
		if v.Model == "" {
			return fmt.Errorf("variant %q: no model", v.Name)
		}
		return nil
	default:
		return fmt.Errorf("variant %q: unknown kind %q", v.Name, v.Kind)
	}
	if v.Model == "" {
		return fmt.Errorf("variant %q: no model", v.Name)
	}
	switch v.Format {
	case "schema", "json", "none":
	default:
		return fmt.Errorf("variant %q: format %q (want schema, json or none)", v.Name, v.Format)
	}
	return nil
}

// LoadVariant reads a single variant from a YAML file (the cmd/qpcd config).
func LoadVariant(path string) (Variant, error) {
	var v Variant
	if err := LoadYAML(path, &v); err != nil {
		return v, err
	}
	return v.WithDefaults(), nil
}

// LoadYAML reads a YAML file into out after expanding ${VAR} references from
// the environment, so secrets need not live in the file.
func LoadYAML(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal([]byte(expandEnv(string(raw))), out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

func expandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(envRe.FindStringSubmatch(m)[1])
	})
}
