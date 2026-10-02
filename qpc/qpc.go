// Package qpc is the QSL preference classifier: it reads a station's QRZ
// record and bio and asks a language model, through any OpenAI-compatible
// chat-completions endpoint (Ollama, llama.cpp, LM Studio, ...), whether and
// how a paper QSL card can go out.
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

// Status says whether a paper card can go out. With Paper, Result.Routes lists
// how; the other statuses have no routes.
type Status string

const (
	Paper   Status = "paper"
	NoPaper Status = "no-paper"
	Unknown Status = "unknown"
	Unclear Status = "unclear" // paper wanted, but no usable route (via without route, direct without address)
)

// Statuses lists every status in a stable order.
var Statuses = []Status{Paper, NoPaper, Unknown, Unclear}

// Valid reports whether s is one of Statuses.
func (s Status) Valid() bool {
	for _, x := range Statuses {
		if s == x {
			return true
		}
	}
	return false
}

// Route is a way a paper card travels. Who it goes via (a QSL manager, the
// operator's home call) is separate: Result.Via.
type Route string

const (
	Bureau Route = "bureau"
	Direct Route = "direct"
	OQRS   Route = "oqrs"
)

// AllRoutes lists every route in a stable order; route sets are kept in it.
var AllRoutes = []Route{Bureau, Direct, OQRS}

// Valid reports whether r is one of AllRoutes.
func (r Route) Valid() bool {
	for _, x := range AllRoutes {
		if r == x {
			return true
		}
	}
	return false
}

// SortRoutes returns the valid routes of rs, deduplicated, in AllRoutes order.
func SortRoutes(rs []Route) []Route {
	var out []Route
	for _, x := range AllRoutes {
		if HasRoute(rs, x) {
			out = append(out, x)
		}
	}
	return out
}

// HasRoute reports whether rs contains r.
func HasRoute(rs []Route, r Route) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// Contribution says whether the station asks for something in return for a
// card: return postage (SAE/SASE, IRC, green stamps), money (USD, EUR), PayPal,
// an OQRS fee or a donation. Required, explicitly not needed, or "" when the
// record does not say. Kind and amount stay in the note.
type Contribution string

const (
	ContributionRequired  Contribution = "required"
	ContributionNotNeeded Contribution = "not-needed"
)

// Valid reports whether p is a known value ("" = not stated).
func (p Contribution) Valid() bool {
	return p == "" || p == ContributionRequired || p == ContributionNotNeeded
}

// LegacyLabels is the single-label scheme of prompts v1 and v2: one route or
// one status, the cheapest route when several were accepted.
var LegacyLabels = []string{"bureau", "direct", "oqrs", "unclear", "no-paper", "unknown"}

// FromLegacyLabel maps a single label of the old scheme to status and routes.
func FromLegacyLabel(label string) (Status, []Route) {
	switch r := Route(label); r {
	case Bureau, Direct, OQRS:
		return Paper, []Route{r}
	}
	if s := Status(label); s.Valid() && s != Paper {
		return s, nil
	}
	return "", nil
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

// Result is one classification. Status is empty when the model's answer could
// not be read (ParseError says why); a transport failure is an error instead.
type Result struct {
	Call         string       `json:"call"`
	Status       Status       `json:"status"`
	Routes       []Route      `json:"routes,omitempty"`       // with Status paper: every route the station accepts
	Preferred    Route        `json:"preferred,omitempty"`    // the route the station says it prefers, if any
	Via          string       `json:"via,omitempty"`          // callsign the card goes via, empty if none
	Note         string       `json:"note,omitempty"`         // the station's preferences, conditions, requirements (English)
	Contribution Contribution `json:"contribution,omitempty"` // something asked in return for a card (prompts from v7)
	// Label is the answer of a single-label prompt (v1, v2); Status and
	// Routes are derived from it.
	Label            string `json:"label,omitempty"`
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
	Guard            string `json:"guard,omitempty"` // a code check changed the model's answer (Variant.AddressGuard)
	Raw              string `json:"raw,omitempty"`   // the model's answer as received
}

// Normalize fills Status and Routes of a result written by a single-label
// prompt (v1, v2) before they existed.
func (r *Result) Normalize() {
	if r.Status == "" && r.Label != "" {
		r.Status, r.Routes = FromLegacyLabel(r.Label)
	}
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }
