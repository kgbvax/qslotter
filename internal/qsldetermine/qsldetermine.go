// Package qsldetermine implements the "smart QSL method determination" ladder:
// given a QRZ Callsign + bio text, decide what QSL method the DX prefers.
//
// Vocabulary (ADIF QSL_VIA): B = bureau, D = direct, E = electronic, M = manager.
// "No paper" is modelled separately as RefusePaper (derived from bio "NO QSL"
// or mqsl=N with no route in the text), since ADIF QSL_VIA has no N value.
package qsldetermine

import (
	"regexp"
	"strings"

	"github.com/dl9et/qslotter/internal/qrz"
)

// Result is the determination outcome.
type Result struct {
	Method      string // "B","D","M" or "" if skipped (E is not a paper-card decision)
	Manager     string // manager callsign if Method=="M", else ""
	RefusePaper bool   // explicit "NO QSL", eQSL/LoTW-only, etc.
	Confidence  string // "high" (structured field), "medium" (explicit bio), "low" (fallback)
	Reason      string
	// Contribution: "required" when the station asks for something in
	// return for a card (SAE/SASE, IRC, green stamps, money, PayPal, a fee or
	// donation), "not-needed" when it waives that explicitly, "" otherwise.
	Contribution string
	// OQRS: the station takes card requests through an online QSL request
	// service (Club Log OQRS), next to Method or as the only route (Method
	// empty, RefusePaper false). Computed, not yet stored or shown.
	OQRS bool
}

// Determine applies the decision ladder to a QRZ lookup result + bio text.
// The outcome is a suggestion for the operator, never a decision.
func Determine(c *qrz.Callsign, bio string) Result {
	if c == nil {
		return Result{Reason: "no station info"}
	}
	oqrs := offersOQRS(c.QSLMgr, bio)
	r := determineRoute(c, bio, oqrs)
	r.Contribution = contribution(c.QSLMgr, bio)
	if oqrs {
		r.OQRS = true
		if r.RefusePaper {
			// "No cards needed! If you need one, pse use Clublog OQRS"
			r.RefusePaper, r.Method, r.Manager = false, "", ""
			r.Reason += "; but OQRS offered"
		}
	}
	return r
}

var (
	oqrsRe = regexp.MustCompile(`\boqrs\b|club ?log(\.org)? (qsl )?requests?\b|requests? (your |a )?(qsl )?(via|through|on|at) club ?log`)
	// noOQRSRe removes "no OQRS" before oqrsRe looks.
	noOQRSRe = regexp.MustCompile(`\b(no|not|without)( via| through)? (club ?log )?oqrs\b|oqrs (is )?(not|closed)`)
)

// offersOQRS reports whether qslmgr or the bio offers card requests through
// an OQRS.
func offersOQRS(qslmgr, bio string) bool {
	lc := noOQRSRe.ReplaceAllString(strings.ToLower(qslmgr+"\n"+bio), " ")
	return oqrsRe.MatchString(lc)
}

func determineRoute(c *qrz.Callsign, bio string, oqrs bool) Result {

	// 1. Structured qslmgr field. A callsign there is a manager (highest
	// confidence). Anything else is free text people type into the field
	// ("VIA BUREAU", "ONLY DIRECT ( SAE + $6 )"): read it for its meaning
	// instead of mistaking its first word for a callsign.
	mgr := strings.TrimSpace(c.QSLMgr)
	if mgr != "" && !strings.EqualFold(mgr, "NONE") {
		// The station's own call is not a manager ("QSL via PD3JWB (bureau)"
		// on PD3JWB's record); its home call for a portable call is.
		if call, ok := managerCall(mgr); ok && !strings.EqualFold(call, strings.TrimSpace(c.Call)) {
			return Result{Method: "M", Manager: call, Confidence: "high",
				Reason: "qslmgr field: " + mgr}
		}
		if r, ok := classify(mgr); ok {
			r.Reason = "qslmgr text \"" + mgr + "\": " + r.Reason
			return r
		}
	}

	// 2. Bio text.
	sig := readSignals(bio)
	refusePaper := sig.noPaper
	if m := viaManagerRe.FindStringSubmatch(bio); m != nil && LooksLikeCallsign(m[1]) && !strings.EqualFold(m[1], strings.TrimSpace(c.Call)) {
		return Result{Method: "M", Manager: strings.ToUpper(m[1]),
			Confidence: "medium", Reason: "bio: QSL via " + m[1], RefusePaper: refusePaper}
	}
	if r, ok := sig.result(); ok {
		r.Reason = "bio: " + r.Reason
		if refusePaper && !r.RefusePaper {
			r.RefusePaper = true
		}
		return r
	}

	// An OQRS named in the text is a route: the flags and the address do not
	// decide then.
	if oqrs {
		return Result{Confidence: "medium", Reason: "OQRS"}
	}

	// 3. Nothing in qslmgr or the bio: the mqsl flag (QRZ delivers 1/0) and
	// the postal address decide; eqsl/lotw do not (operator's rule,
	// 2026-10-02). A published street address invites a direct card.
	mqslYes, mqslNo := flag(c.MQSL)
	fullAddress := strings.TrimSpace(c.Addr1) != "" && strings.TrimSpace(c.Addr2) != ""
	switch {
	case mqslNo:
		return Result{RefusePaper: true, Confidence: "low",
			Reason: "mqsl=no and no route in the text - no paper card"}
	case fullAddress:
		return Result{Method: "D", Confidence: "low",
			Reason: "postal address on QRZ and no other instructions - direct"}
	case mqslYes:
		return Result{Method: "B", Confidence: "low",
			Reason: "mqsl=yes but no full postal address - bureau"}
	}

	// 4. No signal.
	return Result{Confidence: "low", Reason: "no QSL preference signal"}
}

var (
	// contributionRe: something asked in return for a card.
	contributionRe = regexp.MustCompile(`\bs\.?a\.?s?\.?e\b|self[- ]addressed|\birc'?s?\b|green ?stamps?|\bgs\b|return postage|postage|\busd\b|us ?\$|\$ ?\d|\d ?\$|\d ?(eur|euro|€)|€ ?\d|\beuros?\b|dollars?|paypal|donation|contribution|\bfee\b`)
	// noContributionRes: a contribution waived ("no SASE needed", "IRC not
	// necessary", "free of charge"). Matched spans are removed before
	// contributionRe looks, so "no green stamps, IRC only" still asks for one.
	noContributionRes = []*regexp.Regexp{
		regexp.MustCompile(`(\bno|without|no need (for|of|to send)) (\w+ )?(s\.?a\.?s?\.?e\b|irc'?s?|green ?stamps?|postage|money|dollars?|contributions?|donations?|fees?)( (is|are))?( (needed|necessary|required))?`),
		regexp.MustCompile(`(sase|sae|irc'?s?|green ?stamps?|postage|money) (is |are )?not (needed|necessary|required)|free of charge`),
	}
	// noContributionSpans: clauses that waive whatever they name; they count
	// only when they name a contribution ("no bureau needed" does not).
	noContributionSpans = []*regexp.Regexp{
		regexp.MustCompile(`\bno\b[^.,;!\n]{0,30}?\b(needed|necessary|required)\b`),
		regexp.MustCompile(`(do not|don'?t|does not|doesn'?t|never) (need|want|require|ask for|expect)[^.;!\n]{0,60}`),
		regexp.MustCompile(`(do not|don'?t|never|please no) (send|include|add|enclose)[^.;!\n]{0,40}`),
	}
	// ignoreContributionRe: mentions that neither ask nor waive ("I will not
	// answer paper QSL even with green stamps").
	ignoreContributionRe = regexp.MustCompile(`even with [^.;!\n]{0,40}`)
	// qslContextRe marks the sentences of a bio that talk about cards;
	// money elsewhere ("my book costs $5") is not a contribution.
	qslContextRe = regexp.MustCompile(`qsl|card|direct|postage|return|envelope|oqrs|bureau|buro|mail|sase|\bsae\b|\birc\b`)
	sentenceRe   = regexp.MustCompile(`\n|\. |; |! `)
)

// contribution reads the qslmgr field and the card-related sentences of the
// bio for something asked in return for a card.
func contribution(qslmgr, bio string) string {
	parts := []string{strings.ToLower(qslmgr)}
	for _, s := range sentenceRe.Split(strings.ToLower(bio), -1) {
		if qslContextRe.MatchString(s) {
			parts = append(parts, s)
		}
	}
	waived := false
	for _, p := range parts {
		p = ignoreContributionRe.ReplaceAllString(p, " ")
		for _, re := range noContributionSpans {
			p = re.ReplaceAllStringFunc(p, func(m string) string {
				if contributionRe.MatchString(m) {
					waived = true
					return " "
				}
				return m // about something else ("no bureau needed")
			})
		}
		for _, re := range noContributionRes {
			if re.MatchString(p) {
				waived = true
				p = re.ReplaceAllString(p, " ")
			}
		}
		if contributionRe.MatchString(p) {
			return "required"
		}
	}
	if waived {
		return "not-needed"
	}
	return ""
}

// asked returns lc without the parts that waive or merely mention a
// contribution ("no SASE needed", "even with green stamps"), so what is left
// is asked for.
func asked(lc string) string {
	lc = ignoreContributionRe.ReplaceAllString(lc, " ")
	for _, re := range noContributionSpans {
		lc = re.ReplaceAllStringFunc(lc, func(m string) string {
			if contributionRe.MatchString(m) {
				return " "
			}
			return m
		})
	}
	for _, re := range noContributionRes {
		lc = re.ReplaceAllString(lc, " ")
	}
	return lc
}

// flag normalizes a QRZ yes/no flag: QRZ sends 1/0, older data and tests Y/N.
func flag(v string) (yes, no bool) {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "1", "Y", "YES", "TRUE":
		return true, false
	case "0", "N", "NO", "FALSE":
		return false, true
	}
	return false, false
}

var managerLeadIn = map[string]bool{"via": true, "qsl": true, "mgr": true, "manager": true, "pse": true, "please": true, "only": true}

// managerCall extracts a manager callsign from a qslmgr value such as
// "K2ABC", "via K2ABC", "QSL MGR K2ABC" or "K2ABC (bureau only)". Free text
// yields false.
func managerCall(s string) (string, bool) {
	fields := strings.Fields(s)
	// Lead-in words: "QSL via K2ABC", "QSL MGR K2ABC", "QSL Manager: K2ABC".
	for len(fields) > 1 && managerLeadIn[strings.ToLower(strings.Trim(fields[0], ",;:.-"))] {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", false
	}
	first := strings.Trim(fields[0], ",;:.()[]")
	if !LooksLikeCallsign(first) {
		return "", false
	}
	return strings.ToUpper(first), true
}

// signals are the QSL-route hints found in a piece of free text.
type signals struct {
	bureau, direct         bool // route mentioned as accepted
	onlyBureau, onlyDirect bool // "direct only", "bureau only"
	noBureau, noDirect     bool // "no bureau", "not via direct"
	noPaper                bool // "no QSL", "no paper cards"
	electronicOnly         bool // "eQSL only", "only QRZ and LoTW"
	bureauWord, directWord bool
}

var (
	bureauWordRe = regexp.MustCompile(`bureau|buro|b\x{fc}ro|bur\x{f3}`)
	directWordRe = regexp.MustCompile(`\bdirect(o|a|ly|ement)?([^a-z]|$)|\bdirekt`) // "Direct3$", not "direction"
	// returnPostageRe: asking for return postage means a card by post
	// ("LOTW or SASE"); see asked for waivers.
	returnPostageRe = regexp.MustCompile(`\bs\.?a\.?s?\.?e\b|self[- ]addressed|\birc'?s?\b|green ?stamps?`)
	// electronicOnlyRe: confirmations stated to be electronic only. A mere
	// list ("LoTW, eQSL, Club Log") is not a refusal: mqsl and the address
	// decide then (operator, 2026-10-02). HamAward is digital only.
	electronicOnlyRe = regexp.MustCompile(`(e-?\.?qsl|lotw|electronic|hamaward|qrz|club ?log)[^.]{0,20}\bonly\b|\bonly (via )?(e-?\.?qsl|lotw|electronic|hamaward|qrz|club ?log)`)
	onlyDirectRe     = regexp.MustCompile(`only direct|direct(ly)? only|direct qsl only|via direct only|direct or nothing|(direct|direkt) (\+|plus) sae`)
	onlyBureauRe     = regexp.MustCompile(`only (via )?(the )?(bureau|buro)|(bureau|buro) only|via (the )?(bureau|buro) only`)
	noBureauRe       = regexp.MustCompile(`no (qsl )?(via )?(the )?(bureau|buro)|not (via )?(the )?(bureau|buro)|(bureau|buro) (is )?(not|no)\b|without (the )?(bureau|buro)|(bureau|buro)[^.]{0,30}no longer`)
	noDirectRe       = regexp.MustCompile(`no (qsl )?(paper )?(via )?direct|not (via )?direct|direct (is )?(not|no)\b|no direkt`)
	noPaperRe        = regexp.MustCompile(`\b(no|not|don'?t need|do not need) (any )?(paper )?(qsl|cards?)\b|qsl (not needed|not wanted)|paper (qsl )?(not|no)\b|no paper|(don'?t|do not|won'?t|will not|no longer) (answer|accept|reply to|return)( any)? (paper )?(qsl|cards?)`)
	viaManagerRe     = regexp.MustCompile(`(?i)qsl\s+via\s+([a-z0-9/]{3,10})\b`)
)

var apostrophes = strings.NewReplacer("´", "'", "’", "'", "`", "'")

// readSignals scans free text for route hints. Negations are read first so
// "no bureau" never counts as a bureau mention.
func readSignals(text string) signals {
	lc := apostrophes.Replace(strings.ToLower(text))
	var s signals
	s.bureauWord = bureauWordRe.MatchString(lc)
	s.directWord = directWordRe.MatchString(lc) || returnPostageRe.MatchString(asked(lc))
	s.electronicOnly = electronicOnlyRe.MatchString(lc)
	s.onlyDirect = onlyDirectRe.MatchString(lc)
	s.onlyBureau = onlyBureauRe.MatchString(lc)
	s.noBureau = noBureauRe.MatchString(lc)
	s.noDirect = noDirectRe.MatchString(lc)
	// "no QSL via bureau" is a bureau refusal, not a blanket "no cards".
	for _, loc := range noPaperRe.FindAllStringIndex(lc, -1) {
		rest := strings.TrimSpace(lc[loc[1]:])
		if strings.HasPrefix(rest, "via ") || strings.HasPrefix(rest, "by ") {
			continue
		}
		s.noPaper = true
	}
	s.bureau = s.bureauWord && !s.noBureau
	s.direct = s.directWord && !s.noDirect
	return s
}

// result turns the signals into a suggestion. ok is false when the text held
// no route information at all. Mentioning eQSL/LoTW is none, in qslmgr too
// ("LoTW, eQSL"): only an explicit "eQSL only" refuses paper.
func (s signals) result() (Result, bool) {
	switch {
	case s.onlyDirect || (s.direct && s.noBureau && !s.bureau):
		return Result{Method: "D", Confidence: "medium", Reason: "direct only"}, true
	case s.onlyBureau || (s.bureau && s.noDirect && !s.direct):
		return Result{Method: "B", Confidence: "medium", Reason: "bureau only"}, true
	case s.bureau && s.direct:
		return Result{Method: "B", Confidence: "medium", Reason: "bureau and direct both accepted, bureau is cheaper"}, true
	case s.bureau:
		return Result{Method: "B", Confidence: "medium", Reason: "QSL via bureau"}, true
	case s.direct:
		return Result{Method: "D", Confidence: "medium", Reason: "QSL direct"}, true
	case s.noBureau && s.noDirect:
		return Result{RefusePaper: true, Confidence: "medium", Reason: "no bureau and no direct - no paper card"}, true
	case s.noPaper:
		return Result{RefusePaper: true, Confidence: "medium", Reason: "no paper QSL"}, true
	case s.electronicOnly:
		return Result{RefusePaper: true, Confidence: "medium", Reason: "electronic only (eQSL/LoTW) - no paper card"}, true
	case s.noBureau:
		// Only when nothing refuses paper: "NO Paper NO Bureau" is not direct.
		return Result{Method: "D", Confidence: "low", Reason: "no bureau, so direct"}, true
	}
	return Result{}, false
}

// classify reads a short free-text value (a qslmgr field).
func classify(text string) (Result, bool) {
	return readSignals(text).result()
}

// callsignRe matches an amateur callsign, optionally with a country prefix
// ("VK9/DL1ABC") and/or a portable suffix ("DL1ABC/P"): 1-3 prefix chars, one
// digit, 0-3 more chars, a final letter.
var callsignRe = regexp.MustCompile(`^([A-Z0-9]{1,3}/)?[A-Z0-9]{1,3}[0-9][A-Z0-9]{0,3}[A-Z](/[A-Z0-9]{1,4})?$`)

// LooksLikeCallsign reports whether s (case-insensitive) has the shape of an
// amateur callsign. Free text such as "VIA BUREAU" or "LOTW" does not.
func LooksLikeCallsign(s string) bool {
	return callsignRe.MatchString(strings.ToUpper(strings.TrimSpace(s)))
}
