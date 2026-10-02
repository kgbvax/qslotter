// Package qsldetermine reads what a QRZ entry STATES about QSL cards.
//
// It does not guess. Assess returns the signals it found (each with its source
// and the quoted words), and a suggestion only when those signals state a route
// or a refusal: a manager callsign, "bureau", "direct", "only direct", "no
// paper QSL", "eQSL only". Flags (mQSL/eQSL/LoTW) and mere mentions of eQSL or
// LoTW are facts to display, never a reason to suggest anything: in real QRZ
// data they are almost always 1 and say nothing about how to send a card.
//
// Vocabulary: B = bureau, D = direct, M = via a manager, N = no paper card.
// The result is computed on read from the stored raw fields, so a change here
// applies to every cached station at once.
package qsldetermine

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is what a signal says.
type Kind string

const (
	KindBureau       Kind = "bureau"        // bureau accepted
	KindDirect       Kind = "direct"        // direct accepted
	KindOnlyBureau   Kind = "only-bureau"   // "bureau only"
	KindOnlyDirect   Kind = "only-direct"   // "direct only"
	KindNoBureau     Kind = "no-bureau"     // "no bureau"
	KindNoDirect     Kind = "no-direct"     // "no direct"
	KindManager      Kind = "manager"       // a manager callsign (Value)
	KindRefusesPaper Kind = "refuses-paper" // "no QSL", "eQSL only"
	KindAcceptsPaper Kind = "accepts-paper" // "QSL card", "paper", "SAE"
	KindOQRS         Kind = "oqrs"          // online QSL request service
	KindElectronic   Kind = "electronic"    // eQSL / LoTW / Clublog / ... (Value = service)
	KindFlag         Kind = "flag"          // mQSL / eQSL / LoTW as QRZ delivers them (Value = yes/no)
)

// Sources of a signal.
const (
	SourceQSLMgr = "qslmgr"
	SourceBio    = "bio"
	SourceFlag   = "flag"
)

// Signal is one thing QRZ says, with where and in which words.
type Signal struct {
	Kind     Kind   `json:"kind"`
	Source   string `json:"source"`
	Quote    string `json:"quote,omitempty"`
	Value    string `json:"value,omitempty"`
	Scoped   bool   `json:"scoped,omitempty"`   // limited to a case ("no paper QSL for FT8"): shown, never decisive
	Decisive bool   `json:"decisive,omitempty"` // the suggestion rests on it
}

// Note says why there is no suggestion.
type Note string

const (
	NoteNothing        Note = "nothing"         // QRZ states nothing about QSL
	NoteElectronicOnly Note = "electronic-only" // only eQSL/LoTW & co. are listed
	NotePaperNoRoute   Note = "paper-no-route"  // paper is welcome, no route is given
	NoteConflict       Note = "conflict"        // qslmgr and bio contradict each other
	NoteScopedRefusal  Note = "scoped-refusal"  // a refusal limited to a case
)

// Input is the raw material: the QRZ fields as stored and the bio text.
type Input struct {
	Call                     string // the station itself (its own call is not its manager)
	QSLMgr, MQSL, EQSL, LoTW string
	Bio                      string
}

// Assessment is the result.
type Assessment struct {
	Signals    []Signal `json:"signals"`
	Suggest    string   `json:"suggest"`               // "B", "D", "M", "N" or "" (nothing stated)
	Manager    string   `json:"manager,omitempty"`     // callsign when Suggest == "M"
	ManagerVia string   `json:"manager_via,omitempty"` // "B" or "D" when the manager's way is stated
	Both       bool     `json:"both,omitempty"`        // bureau AND direct stated: bureau is the cheaper pick
	Note       Note     `json:"note,omitempty"`        // why Suggest is empty
}

// Decisive returns the signals the suggestion rests on.
func (a Assessment) Decisive() []Signal {
	var out []Signal
	for _, s := range a.Signals {
		if s.Decisive {
			out = append(out, s)
		}
	}
	return out
}

const (
	maxBio   = 32 << 10
	maxQuote = 120
)

// Assess reads the QRZ entry. The qslmgr field is the operator's own statement
// of how to QSL and wins over the bio; the bio is read only when the field
// states nothing. Every signal found stays in the result.
func Assess(in Input) Assessment {
	q := scanSource(SourceQSLMgr, in.QSLMgr, in.Call)
	b := scanSource(SourceBio, in.Bio, in.Call)

	var a Assessment
	flags := flagSignals(in)
	a.Signals = append(a.Signals, flags...)
	a.Signals = append(a.Signals, q.sigs...)
	nq := len(q.sigs)
	a.Signals = append(a.Signals, b.sigs...)
	mark := func(o outcome, base int) {
		for _, i := range o.decisive {
			a.Signals[len(flags)+base+i].Decisive = true
		}
	}

	switch {
	case q.suggest != "":
		// the field states something; a contradiction in the bio stops the suggestion
		if conflicts(q, b) {
			a.Note = NoteConflict
			break
		}
		a.Suggest, a.Manager, a.ManagerVia, a.Both = q.suggest, q.manager, q.via, q.both
		mark(q, 0)
	case b.suggest != "":
		if q.paper && b.suggest == "N" {
			a.Note = NoteConflict
			break
		}
		a.Suggest, a.Manager, a.ManagerVia, a.Both = b.suggest, b.manager, b.via, b.both
		mark(b, nq)
	}
	if a.Suggest == "" && a.Note == "" {
		switch {
		case q.scopedRefusal || b.scopedRefusal:
			a.Note = NoteScopedRefusal
		case q.paper || b.paper:
			a.Note = NotePaperNoRoute
		case q.electronic:
			a.Note = NoteElectronicOnly
		default:
			a.Note = NoteNothing
		}
	}
	return a
}

// conflicts reports a route/refusal contradiction between field and bio.
func conflicts(q, b outcome) bool {
	switch q.suggest {
	case "B", "D", "M":
		return b.refusal && b.suggest == "N"
	case "N":
		return b.suggest == "B" || b.suggest == "D" || b.suggest == "M"
	}
	return false
}

// flagSignals lists the three QRZ flags as facts (QRZ sends 1/0).
func flagSignals(in Input) []Signal {
	var out []Signal
	for _, f := range []struct{ name, v string }{{"mQSL", in.MQSL}, {"eQSL", in.EQSL}, {"LoTW", in.LoTW}} {
		yes, no := flag(f.v)
		switch {
		case yes:
			out = append(out, Signal{Kind: KindFlag, Source: SourceFlag, Quote: f.name, Value: "yes"})
		case no:
			out = append(out, Signal{Kind: KindFlag, Source: SourceFlag, Quote: f.name, Value: "no"})
		}
	}
	return out
}

// outcome is what one source states.
type outcome struct {
	sigs          []Signal
	decisive      []int // indexes into sigs the suggestion rests on
	suggest       string
	manager, via  string
	both          bool
	refusal       bool // a refusal that is not limited to a case
	scopedRefusal bool
	paper         bool // paper is accepted or welcome
	electronic    bool // an electronic service is listed
}

func (o *outcome) add(s Signal) int {
	o.sigs = append(o.sigs, s)
	return len(o.sigs) - 1
}

// scanSource finds the signals of one source and resolves what they state.
func scanSource(source, text, self string) outcome {
	var o outcome
	field := source == SourceQSLMgr
	if field {
		text = strings.TrimSpace(text)
		if emptyFieldValues[strings.ToLower(text)] {
			return o
		}
	} else {
		text = cleanBio(text)
	}
	if text == "" {
		return o
	}

	// manager: a callsign after a cue (or first in the field)
	mgrIdx := -1
	rest := text
	if field {
		if call, r, ok := findManager(text, self, false, true); ok {
			mgrIdx = o.add(Signal{Kind: KindManager, Source: source, Quote: quote(text), Value: call})
			o.manager, rest = call, r
		}
	} else {
		for _, sent := range splitSentences(text) {
			call, r, ok := findManager(sent, self, true, false)
			if !ok {
				continue
			}
			scoped := scopedSentence(sent, call, self)
			i := o.add(Signal{Kind: KindManager, Source: source, Quote: quote(sent), Value: call, Scoped: scoped})
			if !scoped && o.manager == "" {
				mgrIdx, o.manager, rest = i, call, r
			}
		}
	}

	// the manager's own way: route words in what is left of the field/sentence
	var routeText = text
	if o.manager != "" {
		routeText = rest
	}
	rs := scanClauses(source, routeText, o.manager != "")
	routeIdx := make([]int, len(rs))
	for i, s := range rs {
		routeIdx[i] = o.add(s)
	}

	// resolve
	var has = map[Kind]int{} // first non-scoped signal of each kind
	for i, s := range o.sigs {
		if _, ok := has[s.Kind]; !ok && !s.Scoped && s.Kind != KindManager {
			has[s.Kind] = i
		}
		switch s.Kind {
		case KindAcceptsPaper:
			o.paper = true
		case KindElectronic:
			if s.Source == SourceQSLMgr {
				o.electronic = true
			}
		case KindRefusesPaper:
			if s.Scoped {
				o.scopedRefusal = true
			} else {
				o.refusal = true
			}
		}
	}
	idx := func(ks ...Kind) []int {
		var out []int
		for _, k := range ks {
			if i, ok := has[k]; ok {
				out = append(out, i)
			}
		}
		return out
	}
	B := len(idx(KindBureau, KindOnlyBureau)) > 0
	D := len(idx(KindDirect, KindOnlyDirect)) > 0
	_, ob := has[KindOnlyBureau]
	_, od := has[KindOnlyDirect]
	_, nb := has[KindNoBureau]
	_, nd := has[KindNoDirect]

	route, routeSigs, both := "", []int(nil), false
	switch {
	case od || (D && nb && !B):
		route, routeSigs = "D", idx(KindOnlyDirect, KindDirect, KindNoBureau)
	case ob || (B && nd && !D):
		route, routeSigs = "B", idx(KindOnlyBureau, KindBureau, KindNoDirect)
	case B && D:
		route, routeSigs, both = "B", idx(KindBureau, KindDirect), true
	case B:
		route, routeSigs = "B", idx(KindBureau)
	case D:
		route, routeSigs = "D", idx(KindDirect)
	case nb && nd:
		route, routeSigs = "N", idx(KindNoBureau, KindNoDirect)
	}

	if o.manager != "" {
		// a manager: the route words say how to reach them
		o.suggest = "M"
		o.decisive = []int{mgrIdx}
		switch route {
		case "B", "D":
			if !both {
				o.via = route
			}
			o.decisive = append(o.decisive, routeSigs...)
		}
		return o
	}
	if route != "" {
		o.suggest, o.both, o.decisive = route, both, routeSigs
		return o
	}
	if i, ok := has[KindRefusesPaper]; ok {
		o.suggest, o.decisive = "N", []int{i}
	}
	return o
}

// scanClauses finds the route, refusal, paper and electronic signals of a text,
// clause by clause. bio texts need a QSL context before their words count.
func scanClauses(source, text string, hasManager bool) []Signal {
	var out []Signal
	seen := map[string]bool{}
	add := func(s Signal) {
		k := string(s.Kind) + "|" + s.Value + "|" + boolStr(s.Scoped)
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	field := source == SourceQSLMgr
	for _, c := range splitClauses(text) {
		toks := tokenize(c)
		if len(toks) == 0 {
			continue
		}
		q := quote(c)
		scoped := hasScope(toks)
		ctx := field || anyIn(toks, routeContext)
		qctx := field || anyIn(toks, qslContext)

		for i, t := range toks {
			switch {
			case bureauWords[t] || hasSuffixWord(t, "bureau", "buero", "büro"):
				if !ctx {
					continue
				}
				add(Signal{Kind: routeKind(toks, i, KindBureau, KindNoBureau, KindOnlyBureau), Source: source, Quote: q})
			case directWords[t]:
				if !ctx {
					continue
				}
				add(Signal{Kind: routeKind(toks, i, KindDirect, KindNoDirect, KindOnlyDirect), Source: source, Quote: q})
			case t == "oqrs":
				add(Signal{Kind: KindOQRS, Source: source, Quote: q})
			case field && electronicFieldWords[t] != "":
				add(Signal{Kind: KindElectronic, Source: source, Quote: q, Value: electronicFieldWords[t]})
			}
		}

		// "eQSL only", "LoTW only", "electronic only"
		if qctx {
			for i, t := range toks {
				if (electronicWords[t] != "" || electronicGeneric[t]) && nearOnly(toks, i) && !negatedBefore(toks, i) {
					add(Signal{Kind: KindRefusesPaper, Source: source, Quote: q, Value: "electronic-only", Scoped: scoped})
					break
				}
			}
		}

		// "no QSL", "no paper QSL", "keine Karten", "QSL not needed"
		for i, t := range toks {
			if negators[t] {
				if j := refusalObject(toks, i+1); j >= 0 && !routeFollow[at(toks, j+1)] {
					add(Signal{Kind: KindRefusesPaper, Source: source, Quote: q, Value: "no-paper", Scoped: scoped || hasScope(toks[j:])})
				}
				continue
			}
			if qslWords[t] && trailingNegator(toks, i) && !routeFollow[at(toks, i+1)] {
				add(Signal{Kind: KindRefusesPaper, Source: source, Quote: q, Value: "no-paper", Scoped: scoped})
			}
		}

		// paper welcome: "QSL card", "paper", SAE, ... (in a bio only with an accepting verb)
		okPaper := field || anyIn(toks, acceptVerbs)
		if okPaper {
			for i, t := range toks {
				isPaper := (t == "paper" || t == "papier" || t == "cartacea") ||
					((t == "card" || t == "cards") && i > 0 && (toks[i-1] == "qsl" || toks[i-1] == "qsls")) ||
					costWords[t] || t == "karte" || t == "karten"
				if !isPaper || negatedBefore(toks, i) || trailingNegator(toks, i) {
					continue
				}
				if (t == "karte" || t == "karten" || t == "paper" || t == "papier") && !field && !anyIn(toks, qslContext) {
					continue
				}
				add(Signal{Kind: KindAcceptsPaper, Source: source, Quote: q})
				break
			}
		}
	}
	return out
}

// routeKind classifies a route word: negated, "only", or plain.
func routeKind(toks []string, i int, plain, negated, only Kind) Kind {
	if negatedBefore(toks, i) || trailingNegator(toks, i) {
		return negated
	}
	if nearOnly(toks, i) {
		return only
	}
	return plain
}

// negatedBefore reports a negator in front of toks[i], looking back through
// fillers: "no QSL via bureau", "no direct or bureau".
func negatedBefore(toks []string, i int) bool {
	for j, steps := i-1, 0; j >= 0 && steps < 5; j, steps = j-1, steps+1 {
		switch {
		case negators[toks[j]]:
			return true
		case negFillers[toks[j]] || bureauWords[toks[j]] || directWords[toks[j]]:
			continue
		default:
			return false
		}
	}
	return false
}

// trailingNegator reports a negator after toks[i]: "direct is not accepted",
// or a closing "no" ("bureau: no"). A negator that begins the next thought
// ("direct QSL ... NO MONEY") sits in another clause and does not count.
func trailingNegator(toks []string, i int) bool {
	aux := false
	for j := i + 1; j < len(toks) && j <= i+3; j++ {
		switch {
		case negators[toks[j]]:
			return aux || j == len(toks)-1
		case auxWords[toks[j]]:
			aux = true
		default:
			return false
		}
	}
	return false
}

// nearOnly reports an "only" word within three tokens of toks[i].
func nearOnly(toks []string, i int) bool {
	for j := i - 3; j <= i+3; j++ {
		if j >= 0 && j < len(toks) && j != i && onlyWords[toks[j]] {
			return true
		}
	}
	return false
}

// refusalObject returns the index of the QSL word a negator refers to
// ("no [paper] QSL"), or -1.
func refusalObject(toks []string, from int) int {
	for j := from; j < len(toks) && j <= from+2; j++ {
		if qslWords[toks[j]] {
			return j
		}
		if !refusalSkip[toks[j]] {
			return -1
		}
	}
	return -1
}

func hasScope(toks []string) bool {
	for _, t := range toks {
		if scopeWords[t] {
			return true
		}
	}
	return false
}

func anyIn(toks []string, s map[string]bool) bool {
	for _, t := range toks {
		if s[t] {
			return true
		}
	}
	return false
}

func at(toks []string, i int) string {
	if i >= 0 && i < len(toks) {
		return toks[i]
	}
	return ""
}

func hasSuffixWord(t string, suffixes ...string) bool {
	for _, s := range suffixes {
		if len(t) > len(s) && strings.HasSuffix(t, s) && !strings.Contains(t, "bureaucra") {
			return true
		}
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ---- text handling

var (
	clauseRe   = regexp.MustCompile("[\n;,!?|()\\[\\]*•/]|\\.(?:\\s|$)|\\s[-–—]\\s")
	sentenceRe = regexp.MustCompile("[\n!?]|\\.(?:\\s|$)")
	cssDeclRe  = regexp.MustCompile(`^[\w\-\s]+:[^:]*;\s*$`)
	ntRe       = regexp.MustCompile(`(?i)n['’]t\b`)
)

func splitClauses(s string) []string { return clauseRe.Split(s, -1) }

func splitSentences(s string) []string { return sentenceRe.Split(s, -1) }

// cleanBio decodes entities and drops lines that are stylesheet leftovers
// (bios cached by older versions still carry CSS).
func cleanBio(s string) string {
	s = html.UnescapeString(s)
	if len(s) > maxBio {
		s = s[:maxBio]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
	}
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.ContainsAny(l, "{}") || strings.Contains(l, "/*") || strings.Contains(l, "*/") ||
			strings.Contains(strings.ToLower(l), "progid") || cssDeclRe.MatchString(l) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(relevantSentences(strings.Join(keep, "\n")), "\n")
}

// qslHints are the substrings that make a bio sentence worth reading at all.
var qslHints = []string{"qsl", "card", "bureau", "buro", "büro", "buró", "direct", "direkt", "diret", "manager", "mgr", "paper",
	"papier", "lotw", "eqsl", "e-qsl", "e.qsl", "clublog", "oqrs", "karte", "tarjeta", "cartol", "sae", "irc", "via ", "über"}

// relevantSentences keeps the sentences that mention anything QSL-related:
// most of a bio is about antennas and the weather, and every list page
// assesses many stations.
func relevantSentences(s string) []string {
	var out []string
	for _, sent := range splitSentences(s) {
		lc := strings.ToLower(sent)
		for _, h := range qslHints {
			if strings.Contains(lc, h) {
				out = append(out, sent)
				break
			}
		}
	}
	return out
}

// tokenize lower-cases a clause and splits it into letter/digit words;
// "e-qsl" and "e.qsl" become "eqsl", "don't" becomes "do not".
func tokenize(c string) []string {
	c = ntRe.ReplaceAllString(strings.ToLower(c), " not")
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			toks = append(toks, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range c {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	out := toks[:0]
	for i := 0; i < len(toks); i++ {
		if toks[i] == "e" && i+1 < len(toks) && (toks[i+1] == "qsl" || toks[i+1] == "qsls") {
			out = append(out, "eqsl")
			i++
			continue
		}
		out = append(out, toks[i])
	}
	return out
}

// quote shortens a clause to something that fits in a tooltip.
func quote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxQuote {
		r := []rune(s)
		s = string(r[:maxQuote]) + "..."
	}
	return s
}

// ---- managers

// findManager looks for a manager callsign: after a cue ("via K2ABC",
// "QSL via K2ABC", "manager K2ABC", "mgr: K2ABC", "c/o K2ABC"), or - in the
// qslmgr field - as its first word. It returns the callsign and the text
// without the cue and the callsign. A negated cue ("not via K2ABC") and the
// station's own call do not count.
func findManager(text, self string, needQSLContext, allowFirst bool) (call, rest string, ok bool) {
	fields := strings.Fields(text)
	clean := func(f string) string { return strings.Trim(f, ",;:.()[]\"'") }
	for i, f := range fields {
		lc := strings.ToLower(clean(f))
		if i == 0 && allowFirst && LooksLikeCallsign(clean(f)) && !isSelf(clean(f), self) {
			return strings.ToUpper(clean(f)), strings.Join(fields[1:], " "), true
		}
		cue := lc == "via" || lc == "manager" || lc == "mgr" || lc == "mgr." || lc == "c/o" || lc == "co"
		if !cue || i+1 >= len(fields) {
			continue
		}
		cand := clean(fields[i+1])
		if !LooksLikeCallsign(cand) || isSelf(cand, self) {
			continue
		}
		if lc == "via" && needQSLContext && !nearField(fields, i, 4, qslContext) {
			continue
		}
		neg := false
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if negators[strings.ToLower(clean(fields[j]))] {
				neg = true
			}
		}
		if neg {
			continue
		}
		restFields := append(append([]string{}, fields[:i]...), fields[i+2:]...)
		return strings.ToUpper(cand), strings.Join(restFields, " "), true
	}
	return "", text, false
}

func nearField(fields []string, i, n int, s map[string]bool) bool {
	for j := i - n; j <= i+n; j++ {
		if j >= 0 && j < len(fields) && s[strings.ToLower(strings.Trim(fields[j], ",;:.()[]\"'"))] {
			return true
		}
	}
	return false
}

// scopedSentence reports a sentence limited to a case: a scope word, or a
// second callsign ("QSL via DL1XYZ for TX0AT").
func scopedSentence(sent, manager, self string) bool {
	toks := tokenize(sent)
	if hasScope(toks) {
		return true
	}
	for _, f := range strings.Fields(sent) {
		c := strings.Trim(f, ",;:.()[]\"'")
		if LooksLikeCallsign(c) && !strings.EqualFold(c, manager) && !isSelf(c, self) {
			return true
		}
	}
	return false
}

// isSelf reports whether call is the station's own call (or a portable form).
func isSelf(call, self string) bool {
	if self == "" {
		return false
	}
	a, b := strings.ToUpper(call), strings.ToUpper(self)
	if a == b {
		return true
	}
	for _, x := range strings.Split(a, "/") {
		for _, y := range strings.Split(b, "/") {
			if len(x) >= 3 && x == y {
				return true
			}
		}
	}
	return false
}
