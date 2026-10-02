// Package qpc is the QSL preference classifier: it reads a station's QRZ
// record and bio and asks a language model, through any OpenAI-compatible
// chat-completions endpoint (Ollama, llama.cpp, LM Studio, ...), which route a
// paper QSL card should take.
//
// Experimental (docs/VISION.md E2): its answers do not reach qslotter until it
// beats the heuristic in internal/qsldetermine on a labelled sample
// (cmd/qpc-lab). It imports nothing from qslotter's internal packages, so it
// can be built and deployed on its own (cmd/qpcd) or moved out of the
// repository.
package qpc

import (
	_ "embed"
	"strings"
	"time"
)

// Label is the route a paper card should take. Who the card goes via (a QSL
// manager, the operator's home call) is separate: Result.Via. LABELS.md
// defines each label and the rules for unclear cases.
type Label string

const (
	Bureau  Label = "bureau"
	Direct  Label = "direct"
	OQRS    Label = "oqrs"
	Unclear Label = "unclear" // a via callsign is named, the route is not
	NoPaper Label = "no-paper"
	Unknown Label = "unknown"
)

// Labels lists every label in a stable order (reports, the labelling page).
var Labels = []Label{Bureau, Direct, OQRS, Unclear, NoPaper, Unknown}

// Valid reports whether l is one of Labels.
func (l Label) Valid() bool {
	for _, x := range Labels {
		if l == x {
			return true
		}
	}
	return false
}

// Rules is the labelling protocol (LABELS.md). The same text is shown to the
// person labelling and, through the {{.Rules}} template field, to the model.
//
//go:embed LABELS.md
var Rules string

// Station is the classifier's input: the parts of a QRZ record that bear on
// the QSL route. The postal address is part of it (no address, no direct
// card); name and e-mail are not.
type Station struct {
	Call    string `json:"call"`
	Country string `json:"country,omitempty"`
	DXCC    string `json:"dxcc,omitempty"`
	QSLMgr  string `json:"qslmgr,omitempty"`
	MQSL    string `json:"mqsl,omitempty"`  // "will return paper QSL": 1, 0 or empty
	EQSL    string `json:"eqsl,omitempty"`  // accepts eQSL: 1, 0 or empty
	LoTW    string `json:"lotw,omitempty"`  // uses LoTW: 1, 0 or empty
	Addr1   string `json:"addr1,omitempty"` // street
	Addr2   string `json:"addr2,omitempty"` // city
	State   string `json:"state,omitempty"`
	Zip     string `json:"zip,omitempty"`
	Bio     string `json:"bio,omitempty"` // QRZ bio page as plain text
}

// HasFullAddress reports whether the QRZ postal address has at least a street
// and a city, enough to send a card direct. An address written only in the bio
// does not count here.
func (s Station) HasFullAddress() bool {
	return strings.TrimSpace(s.Addr1) != "" && strings.TrimSpace(s.Addr2) != ""
}

// Result is one classification. Label is empty when the model's answer could
// not be read (ParseError says why); a transport failure is an error instead.
type Result struct {
	Call             string `json:"call"`
	Label            Label  `json:"label"`
	Via              string `json:"via,omitempty"`        // callsign the card goes via, empty if none
	Confidence       string `json:"confidence,omitempty"` // high, medium or low
	Evidence         string `json:"evidence,omitempty"`   // the model's quote from the record
	Model            string `json:"model,omitempty"`
	Prompt           string `json:"prompt,omitempty"` // prompt ID, name@hash
	LatencyMS        int64  `json:"latency_ms"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"` // "length" = the answer was cut off
	Truncated        bool   `json:"truncated,omitempty"`     // the bio was cut to BioMaxChars
	ParseError       string `json:"parse_error,omitempty"`
	Guard            string `json:"guard,omitempty"` // a code check changed the model's label (Variant.AddressGuard)
	Raw              string `json:"raw,omitempty"`   // the model's answer as received
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }
