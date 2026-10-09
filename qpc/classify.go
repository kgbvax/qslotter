package qpc

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Classifier classifies stations with one Variant.
type Classifier struct {
	v      Variant
	prompt *Prompt
	client *chatClient
}

// New prepares a classifier for an LLM variant (defaults applied).
func New(v Variant) (*Classifier, error) {
	v = v.WithDefaults()
	if err := v.Check(); err != nil {
		return nil, err
	}
	c := &Classifier{v: v, client: &chatClient{baseURL: v.BaseURL, apiKey: v.APIKey, http: &http.Client{}}}
	var err error
	if v.Kind != "llm" {
		return nil, fmt.Errorf("variant %q: kind %q is not a model classifier", v.Name, v.Kind)
	}
	if c.prompt, err = LoadPrompt(v.Prompt); err != nil {
		return nil, err
	}
	return c, nil
}

// Variant returns the resolved variant.
func (c *Classifier) Variant() Variant { return c.v }

// PromptID identifies the prompt text (name@hash).
func (c *Classifier) PromptID() string { return c.prompt.ID() }

// Messages renders the prompt for st without calling the model.
func (c *Classifier) Messages(st Station) (system, user string, truncated bool, err error) {
	bio, truncated := PrepareBio(st.Bio, c.v.BioMaxChars)
	system, user, err = c.prompt.Render(PromptData{
		Station: st, Bio: bio, Truncated: truncated, Rules: Rules,
		Labels: LegacyLabels, Statuses: Statuses, Routes: AllRoutes,
	})
	return system, user, truncated, err
}

// Classify asks the model about st. An unreadable answer is not an error: the
// Result then has an empty Label and a ParseError.
func (c *Classifier) Classify(ctx context.Context, st Station) (Result, error) {
	st.Call = strings.ToUpper(strings.TrimSpace(st.Call))
	r := Result{Call: st.Call, Model: c.v.Model, Prompt: c.prompt.ID()}
	system, user, truncated, err := c.Messages(st)
	if err != nil {
		return r, fmt.Errorf("render prompt: %w", err)
	}
	r.Truncated = truncated
	req := chatRequest{
		Model:       c.v.Model,
		Messages:    []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: c.v.Temperature,
		Seed:        c.v.Seed,
		MaxTokens:   c.v.MaxTokens,
	}
	switch c.v.Format {
	case "schema":
		req.ResponseFormat = c.prompt.schema()
	case "json":
		req.ResponseFormat = jsonObjectFormat
	}
	ctx, cancel := context.WithTimeout(ctx, c.v.Timeout)
	defer cancel()
	start := time.Now()
	ans, err := c.client.complete(ctx, req, c.v.Extra)
	r.LatencyMS = msSince(start)
	if err != nil {
		return r, err
	}
	r.PromptTokens, r.CompletionTokens, r.FinishReason = ans.PromptTokens, ans.CompletionTokens, ans.FinishReason
	parseAnswer(ans.Content, &r)
	if c.v.FlagRule || c.prompt.FlagRule {
		flagRule(st, &r)
		dclRule(st, &r)
	}
	if c.v.AddressGuard {
		addressGuard(st, &r)
	}
	return r, nil
}

// flagRule applies LABELS.md rule 6 when the model found nothing about paper
// cards in the text: the mqsl flag and the postal address decide.
func flagRule(st Station, r *Result) {
	if r.Status != Unknown {
		return
	}
	switch strings.TrimSpace(st.MQSL) {
	case "0":
		r.Status, r.Guard = NoPaper, "flag rule: mqsl 0, nothing in the text"
	case "1", "":
		switch {
		case st.HasFullAddress():
			r.Status, r.Routes, r.Guard = Paper, []Route{Direct}, "flag rule: full postal address, nothing in the text"
		case st.MQSL == "1":
			r.Status, r.Routes, r.Guard = Paper, []Route{Bureau}, "flag rule: mqsl 1, no full postal address"
		}
	}
}

var dclRe = regexp.MustCompile(`(?i)\bdcl\b|darc community log`)

// dclRule applies LABELS.md rule 6a in code: a station that mentions DCL
// (DARC Community Logbook) is a DARC member, so the bureau is a route too,
// unless the answer refuses paper or the text refuses the bureau.
func dclRule(st Station, r *Result) {
	if r.Status == NoPaper || HasRoute(r.Routes, Bureau) || !dclRe.MatchString(st.QSLMgr+" "+st.Bio) || noBureauTextRe.MatchString(st.QSLMgr+" "+st.Bio) {
		return
	}
	r.Status, r.Routes = Paper, SortRoutes(append(r.Routes, Bureau))
	r.Guard = strings.TrimPrefix(r.Guard+"; dcl rule: DARC member, bureau too", "; ")
}

var noBureauTextRe = regexp.MustCompile(`(?i)\bno[- ](qsl )?(via )?(the )?(bureau|buro)|not (a )?member of (the |a )?(bureau|buro)`)

// addressGuard applies rule 5 in code: without a full QRZ postal address,
// direct is not a usable route (unless the card goes via another call). It
// cannot see an address written in the bio.
func addressGuard(st Station, r *Result) {
	if r.Via != "" || st.HasFullAddress() || !HasRoute(r.Routes, Direct) {
		return
	}
	var keep []Route
	for _, x := range r.Routes {
		if x != Direct {
			keep = append(keep, x)
		}
	}
	r.Routes = keep
	if r.Preferred == Direct {
		r.Preferred = ""
	}
	r.Guard = "direct dropped: no full QRZ postal address"
	if len(keep) == 0 {
		r.Status = Unclear
		r.Guard = "direct without a full QRZ postal address -> unclear"
	}
}
