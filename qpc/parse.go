package qpc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// answer is the JSON object the prompt asks for.
type answer struct {
	Evidence   string `json:"evidence"`
	Label      string `json:"label"`
	Via        string `json:"via"`
	Confidence string `json:"confidence"`
}

var (
	thinkRe  = regexp.MustCompile(`(?is)<think>.*?</think>`)
	fenceRe  = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")
	viaRe    = regexp.MustCompile(`^[A-Z0-9]{1,4}(/[A-Z0-9]{1,8}){0,2}$|^[A-Z0-9]{3,10}$`)
	digitRe  = regexp.MustCompile(`[0-9]`)
	letterRe = regexp.MustCompile(`[A-Z]`)
)

// parseAnswer reads the model's answer into r. It tolerates thinking blocks,
// code fences and prose around the object; anything it cannot use is noted in
// r.ParseError, and r.Label stays empty when no valid label was found.
func parseAnswer(raw string, r *Result) {
	r.Raw = raw
	s := thinkRe.ReplaceAllString(raw, "")
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = strings.TrimSpace(s)
	var a answer
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
		if i < 0 || j < i {
			r.ParseError = "no JSON object in the answer"
			return
		}
		if err := json.Unmarshal([]byte(s[i:j+1]), &a); err != nil {
			r.ParseError = fmt.Sprintf("unreadable JSON: %v", err)
			return
		}
	}
	r.Evidence = strings.TrimSpace(a.Evidence)
	if len(r.Evidence) > 400 {
		r.Evidence = r.Evidence[:400] + "..."
	}
	r.Confidence = normalizeConfidence(a.Confidence)
	l := Label(strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(strings.TrimSpace(a.Label))))
	if !l.Valid() {
		r.ParseError = fmt.Sprintf("invalid label %q", a.Label)
		return
	}
	r.Label = l
	via, ok := normalizeVia(a.Via)
	if !ok {
		r.ParseError = fmt.Sprintf("via %q is not a callsign", a.Via)
	}
	r.Via = via
}

// normalizeVia returns the callsign in a via answer ("" for none). ok is false
// when the value is neither empty nor callsign-shaped; via is then "".
func normalizeVia(s string) (via string, ok bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "VIA "))
	s = strings.Trim(s, ".,;:()[]\"'")
	switch s {
	case "", "NONE", "N/A", "NA", "-", "NULL", "EMPTY":
		return "", true
	}
	if viaRe.MatchString(s) && digitRe.MatchString(s) && letterRe.MatchString(s) {
		return s, true
	}
	return "", false
}

func normalizeConfidence(s string) string {
	switch c := strings.ToLower(strings.TrimSpace(s)); c {
	case "high", "medium", "low":
		return c
	}
	return ""
}
