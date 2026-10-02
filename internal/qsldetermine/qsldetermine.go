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
}

// Determine applies the decision ladder to a QRZ lookup result + bio text.
// The outcome is a suggestion for the operator, never a decision.
func Determine(c *qrz.Callsign, bio string) Result {
	if c == nil {
		return Result{Reason: "no station info"}
	}
	r := determineRoute(c, bio)
	r.Contribution = contribution(c.QSLMgr, bio)
	return r
}

func determineRoute(c *qrz.Callsign, bio string) Result {

	// 1. Structured qslmgr field. A callsign there is a manager (highest
	// confidence). Anything else is free text people type into the field
	// ("VIA BUREAU", "ONLY DIRECT ( SAE + $6 )"): read it for its meaning
	// instead of mistaking its first word for a callsign.
	mgr := strings.TrimSpace(c.QSLMgr)
	if mgr != "" && !strings.EqualFold(mgr, "NONE") {
		if call, ok := managerCall(mgr); ok {
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
	if m := viaManagerRe.FindStringSubmatch(bio); m != nil && LooksLikeCallsign(m[1]) {
		return Result{Method: "M", Manager: strings.ToUpper(m[1]),
			Confidence: "medium", Reason: "bio: QSL via " + m[1], RefusePaper: refusePaper}
	}
	if r, ok := sig.result(false); ok {
		r.Reason = "bio: " + r.Reason
		if refusePaper && !r.RefusePaper {
			r.RefusePaper = true
		}
		return r
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
	// noContributionRe: the same, waived ("no SASE needed", "IRC not
	// necessary", "free of charge"). Matched spans are removed before
	// contributionRe looks, so "no green stamps, IRC only" still asks for one.
	noContributionRe = regexp.MustCompile(`(no|without|don'?t (send|need)|no need (for|of|to send)) (\w+ )?(s\.?a\.?s?\.?e\b|irc'?s?|green ?stamps?|postage|money|dollars?|contribution|donation|fee)( (is|are))?( (needed|necessary|required))?|(sase|sae|irc'?s?|green ?stamps?|postage|money) (is |are )?not (needed|necessary|required)|free of charge`)
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
		if noContributionRe.MatchString(p) {
			waived = true
			p = noContributionRe.ReplaceAllString(p, " ")
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

// managerCall extracts a manager callsign from a qslmgr value such as
// "K2ABC", "via K2ABC" or "K2ABC (bureau only)". Free text yields false.
func managerCall(s string) (string, bool) {
	fields := strings.Fields(s)
	if len(fields) > 1 && strings.EqualFold(strings.Trim(fields[0], ",;:."), "via") {
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
	bureau, direct             bool // route mentioned as accepted
	onlyBureau, onlyDirect     bool // "direct only", "bureau only"
	noBureau, noDirect         bool // "no bureau", "not via direct"
	noPaper                    bool // "no QSL", "no paper cards"
	electronic, electronicOnly bool // eQSL / LoTW / Clublog mentioned; "eQSL only"
	bureauWord, directWord     bool
}

var (
	bureauWordRe     = regexp.MustCompile(`bureau|buro|b\x{fc}ro|bur\x{f3}`)
	directWordRe     = regexp.MustCompile(`direct|direkt|directo`)
	electronicRe     = regexp.MustCompile(`e-?\.?qsl|lotw|logbook of the world|clublog`)
	electronicOnlyRe = regexp.MustCompile(`(e-?\.?qsl|lotw|electronic)[^.]{0,20}\bonly\b|\bonly (e-?\.?qsl|lotw|electronic)`)
	onlyDirectRe     = regexp.MustCompile(`only direct|direct(ly)? only|direct qsl only|via direct only|direct or nothing|(direct|direkt) (\+|plus) sae`)
	onlyBureauRe     = regexp.MustCompile(`only (via )?(the )?(bureau|buro)|(bureau|buro) only|via (the )?(bureau|buro) only`)
	noBureauRe       = regexp.MustCompile(`no (qsl )?(via )?(the )?(bureau|buro)|not (via )?(the )?(bureau|buro)|(bureau|buro) (is )?(not|no)\b|without (the )?(bureau|buro)`)
	noDirectRe       = regexp.MustCompile(`no (qsl )?(via )?direct|not (via )?direct|direct (is )?(not|no)\b|no direkt`)
	noPaperRe        = regexp.MustCompile(`\b(no|not|don'?t need|do not need) (any )?(paper )?(qsl|cards?)\b|qsl (not needed|not wanted)|paper (qsl )?(not|no)\b|no paper`)
	viaManagerRe     = regexp.MustCompile(`(?i)qsl\s+via\s+([a-z0-9/]{3,10})\b`)
)

// readSignals scans free text for route hints. Negations are read first so
// "no bureau" never counts as a bureau mention.
func readSignals(text string) signals {
	lc := strings.ToLower(text)
	var s signals
	s.bureauWord = bureauWordRe.MatchString(lc)
	s.directWord = directWordRe.MatchString(lc)
	s.electronic = electronicRe.MatchString(lc)
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
// no route information at all. electronicHint controls whether merely
// mentioning eQSL/LoTW counts as "no paper": right for a short qslmgr field,
// wrong for a free-form bio ("I upload to LoTW weekly"), where only an explicit
// "eQSL only" does.
func (s signals) result(electronicHint bool) (Result, bool) {
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
	case s.noBureau:
		return Result{Method: "D", Confidence: "low", Reason: "no bureau, so direct"}, true
	case s.noPaper:
		return Result{RefusePaper: true, Confidence: "medium", Reason: "no paper QSL"}, true
	case s.electronicOnly:
		return Result{RefusePaper: true, Confidence: "medium", Reason: "electronic only (eQSL/LoTW) - no paper card"}, true
	case electronicHint && s.electronic:
		return Result{RefusePaper: true, Confidence: "low", Reason: "only electronic confirmations (eQSL/LoTW) mentioned - no paper card"}, true
	}
	return Result{}, false
}

// classify reads a short free-text value (a qslmgr field).
func classify(text string) (Result, bool) {
	return readSignals(text).result(true)
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
