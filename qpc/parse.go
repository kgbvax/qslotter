package qpc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// answer is the JSON object the prompts ask for: Status, Routes, Preferred and
// Note for a routes prompt, Label for a single-label prompt (v1, v2).
type answer struct {
	Evidence     string   `json:"evidence"`
	Status       string   `json:"status"`
	Routes       []string `json:"routes"`
	Preferred    string   `json:"preferred"`
	Label        string   `json:"label"`
	Via          string   `json:"via"`
	Contribution string   `json:"contribution"`
	Note         string   `json:"note"`
	Confidence   string   `json:"confidence"`
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
// r.ParseError, and r.Status stays empty when no valid answer was found.
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
	r.Evidence = clip(strings.TrimSpace(a.Evidence), 400)
	r.Note = clip(strings.TrimSpace(a.Note), 300)
	r.Confidence = normalizeConfidence(a.Confidence)
	var problems []string
	via, ok := normalizeVia(a.Via)
	if !ok {
		problems = append(problems, fmt.Sprintf("via %q is not a callsign", a.Via))
	}
	r.Via = via
	if p := Contribution(word(a.Contribution)); p.Valid() {
		r.Contribution = p
	} else {
		problems = append(problems, fmt.Sprintf("invalid contribution %q", a.Contribution))
	}

	if a.Status == "" && a.Label != "" {
		l := word(a.Label)
		r.Label = l
		if r.Status, r.Routes = FromLegacyLabel(l); r.Status == "" {
			problems = append(problems, fmt.Sprintf("invalid label %q", a.Label))
		}
		r.ParseError = strings.Join(problems, "; ")
		return
	}

	var routes []Route
	for _, x := range a.Routes {
		if rt := Route(word(x)); rt.Valid() {
			routes = append(routes, rt)
		} else {
			problems = append(problems, fmt.Sprintf("invalid route %q", x))
		}
	}
	r.Routes = SortRoutes(routes)
	st := Status(word(a.Status))
	switch {
	case len(r.Routes) > 0 && st != Paper:
		// Routes decide: a station with a usable route takes paper.
		if st != "" {
			problems = append(problems, fmt.Sprintf("status %q with routes, taken as paper", a.Status))
		}
		st = Paper
	case st == Paper && len(r.Routes) == 0:
		problems = append(problems, "status paper without routes")
		st = ""
	case !st.Valid():
		problems = append(problems, fmt.Sprintf("invalid status %q", a.Status))
		st = ""
	}
	r.Status = st
	if p := Route(word(a.Preferred)); p != "" {
		if HasRoute(r.Routes, p) {
			r.Preferred = p
		} else {
			problems = append(problems, fmt.Sprintf("preferred %q is not among the routes", a.Preferred))
		}
	}
	r.ParseError = strings.Join(problems, "; ")
}

// word normalizes an enum value: lowercase, "No Paper" -> "no-paper".
func word(s string) string {
	return strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
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
