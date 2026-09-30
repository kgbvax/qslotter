package llmqsl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// rawResult is the JSON shape we ask the model to emit.
type rawResult struct {
	Method      string `json:"method"`
	Confidence  string `json:"confidence"`
	Reasoning   string `json:"reasoning"`
	ManagerCall string `json:"manager_call"`
}

var (
	// jsonObjectRe extracts the first {...} block from text, tolerant of surrounding prose.
	jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)
	// callsignRe is a permissive callsign sanity check (alphanumeric, at least one digit).
	callsignRe = regexp.MustCompile(`^[A-Z0-9]{3,6}$`)
)

// ParseResponse turns the LLM's raw text into an EvalResult.
// It tries a direct JSON unmarshal first, then falls back to extracting the
// first JSON object from the text. Method values are normalized and validated.
func ParseResponse(raw string) EvalResult {
	res := EvalResult{RawResponse: raw}

	var rr rawResult
	if err := json.Unmarshal([]byte(raw), &rr); err != nil {
		// Try to find a JSON object embedded in prose or a markdown fence.
		m := jsonObjectRe.FindString(raw)
		if m == "" {
			res.ParseError = fmt.Sprintf("no JSON object found: %v", err)
			return res
		}
		if err := json.Unmarshal([]byte(m), &rr); err != nil {
			res.ParseError = fmt.Sprintf("found JSON-like text but could not parse: %v", err)
			return res
		}
	}

	res.Method = Method(strings.ToLower(strings.TrimSpace(rr.Method)))
	if !IsValidMethod(res.Method) {
		res.ParseError = fmt.Sprintf("invalid method %q", rr.Method)
		res.Confidence = "low"
	} else {
		res.Confidence = normalizeConfidence(rr.Confidence)
	}

	res.Reasoning = strings.TrimSpace(rr.Reasoning)
	res.ManagerCall = strings.ToUpper(strings.TrimSpace(rr.ManagerCall))
	if res.ManagerCall != "" && !callsignRe.MatchString(res.ManagerCall) {
		// Doesn't look like a callsign; clear it and note the issue.
		res.ParseError = fmt.Sprintf("manager_call %q does not look like a callsign", res.ManagerCall)
		res.ManagerCall = ""
		if res.Confidence != "low" {
			res.Confidence = "low"
		}
	}

	// Manager call is only meaningful for manager-* methods.
	if res.Method != MethodManagerBuero && res.Method != MethodManagerDirect {
		res.ManagerCall = ""
	} else if res.ManagerCall == "" && res.ParseError == "" {
		res.ParseError = fmt.Sprintf("method %q requires manager_call", res.Method)
		res.Confidence = "low"
	}

	return res
}

func normalizeConfidence(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}
