// Package qsldetermine implements the "smart QSL method determination" ladder:
// given a QRZ Callsign + bio text, decide what QSL method the DX prefers.
//
// Vocabulary (ADIF QSL_VIA): B = bureau, D = direct, E = electronic, M = manager.
// "No paper" is modelled separately as RefusePaper (derived from bio "NO QSL"
// or mqsl=N+eqsl=N+lotw=N), since ADIF QSL_VIA has no N value.
package qsldetermine

import (
	"regexp"
	"strings"

	"github.com/dl9et/qslotter/internal/qrz"
)

// Result is the determination outcome.
type Result struct {
	Method       string // "B","D","M" or "" if skipped (E is not a paper-card decision)
	Manager      string // manager callsign if Method=="M", else ""
	RefusePaper  bool   // explicit "NO QSL", eQSL/LoTW-only, etc.
	Confidence   string // "high" (structured field), "medium" (explicit bio), "low" (fallback)
	Reason       string
}

// Determine applies the decision ladder to a QRZ lookup result + bio text.
func Determine(c *qrz.Callsign, bio string) Result {
	if c == nil {
		return Result{Reason: "no station info"}
	}

	// 1. Structured qslmgr field - highest confidence.
	mgr := strings.TrimSpace(c.QSLMgr)
	if mgr != "" && !strings.EqualFold(mgr, "NONE") {
		// "CALL via route" -> take the first token as manager.
		mgrCall := strings.Fields(mgr)[0]
		return Result{Method: "M", Manager: mgrCall, Confidence: "high",
			Reason: "qslmgr field: " + mgr}
	}

	// 2. Bio heuristics.
	lc := strings.ToLower(bio)
	refusePaper := false
	if noQSLRe.MatchString(lc) {
		refusePaper = true
		// Don't return yet: "NO PAPER QSL ... eQSL only" should set Method=E too.
	}
	if m := viaManagerRe.FindStringSubmatch(bio); m != nil {
		cand := strings.ToLower(m[1])
		// Exclude routing keywords that aren't callsigns.
		if cand != "bureau" && cand != "buro" && cand != "direct" &&
			cand != "manage" && cand != "manager" && cand != "electron" &&
			cand != "electronic" && cand != "eqsl" && cand != "lotw" {
			return Result{Method: "M", Manager: strings.ToUpper(m[1]),
				Confidence: "medium", Reason: "bio: QSL via " + m[1], RefusePaper: refusePaper}
		}
	}
	if viaBureauRe.MatchString(lc) {
		return Result{Method: "B", Confidence: "medium",
			Reason: "bio: QSL via bureau", RefusePaper: refusePaper}
	}
	if directOnlyRe.MatchString(lc) {
		return Result{Method: "D", Confidence: "medium",
			Reason: "bio: QSL direct only", RefusePaper: refusePaper}
	}
	if eQSLOnlyRe.MatchString(lc) {
		return Result{RefusePaper: true, Confidence: "medium",
			Reason: "bio: eQSL only - no paper card"}
	}

	// If bio said "NO QSL" but none of the method-specific regexes matched,
	// return RefusePaper with no method.
	if refusePaper {
		return Result{RefusePaper: true, Confidence: "high", Reason: "bio says NO QSL"}
	}

	// 3. Structured mqsl/eqsl/lotw fallback.
	if c.MQSL == "Y" {
		return Result{Method: "B", Confidence: "low",
			Reason: "mqsl=Y, defaulting to bureau"}
	}
	if c.MQSL == "N" && (c.EQSL == "Y" || c.LoTW == "Y") {
		return Result{RefusePaper: true, Confidence: "low",
			Reason: "mqsl=N, eqsl/lotw only - no paper card"}
	}

	// 4. No signal.
	return Result{Confidence: "low", Reason: "no QSL preference signal"}
}

var (
	noQSLRe       = regexp.MustCompile(`no (qsl|paper|card)|qsl (not needed|via .{0,3}only|not wanted)`)
	// viaManagerRe matches "QSL via <CALL>" where CALL looks like a callsign
	// (3-6 alphanumeric chars) but is NOT a routing keyword (bureau/buro/
	// direct/manager/electronic). The negative-lookahead is approximated by
	// excluding known keywords in the captured group via a follow-up check.
	viaManagerRe  = regexp.MustCompile(`(?i)qsl\s+via\s+([a-z0-9]{3,6})\b`)
	viaBureauRe   = regexp.MustCompile(`(?i)qsl .{0,8}(bureau|buro)|via bureau|via buro`)
	directOnlyRe  = regexp.MustCompile(`(?i)direct (only|qsl)|qsl direct|no bureau`)
	eQSLOnlyRe    = regexp.MustCompile(`(?i)eqsl only|lotw only|e-qsl only`)
)