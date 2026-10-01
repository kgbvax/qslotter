package intake

import (
	"sort"
	"strings"
)

// Token is a callsign-like string cut out of the OCR output.
type Token struct {
	Text string
	Line int     // index of the source line in Page.Lines
	Rank int     // 0 = engine's best reading of that line
	Conf float64 // line confidence
}

// CallTokens splits every reading of every line into alphanumeric tokens
// (keeping '/') of 3..12 characters. Duplicates keep the best occurrence:
// lowest rank, then highest confidence.
func CallTokens(p Page) []Token {
	idx := map[string]int{}
	var out []Token
	for li, l := range p.Lines {
		for ri, cand := range l.Cands {
			for _, s := range splitTokens(cand) {
				t := Token{Text: s, Line: li, Rank: ri, Conf: l.Conf}
				if i, ok := idx[s]; ok {
					if betterToken(t, out[i]) {
						out[i] = t
					}
					continue
				}
				idx[s] = len(out)
				out = append(out, t)
			}
		}
	}
	return out
}

func betterToken(a, b Token) bool {
	if a.Rank != b.Rank {
		return a.Rank < b.Rank
	}
	return a.Conf > b.Conf
}

func splitTokens(s string) []string {
	f := func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/')
	}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToUpper(s), f) {
		w = strings.Trim(w, "/")
		if len(w) >= 3 && len(w) <= 12 {
			out = append(out, w)
		}
	}
	return out
}

// skeletonMap folds characters that OCR confuses (especially in handwriting)
// into one representative. The set is deliberately small and explicit.
var skeletonMap = map[byte]byte{
	'O': '0', 'Q': '0',
	'I': '1', 'L': '1',
	'S': '5',
	'B': '8',
	'Z': '2',
	'G': '6',
}

// Skeleton folds OCR-confusable characters: {0,O,Q} {1,I,L} {5,S} {8,B}
// {2,Z} {6,G}. Input is expected upper-case ASCII.
func Skeleton(s string) string {
	b := []byte(s)
	for i, c := range b {
		if m, ok := skeletonMap[c]; ok {
			b[i] = m
		}
	}
	return string(b)
}

// portable-operation suffixes that are not callsigns of their own.
var suffixParts = map[string]bool{
	"QRP": true, "MM": true, "AM": true, "LGT": true,
}

// variants returns the string itself plus, for portable forms such as
// DL1ABC/P or EA8/DL1ABC, each part long enough to be a callsign.
func variants(s string) []string {
	out := []string{s}
	if !strings.Contains(s, "/") {
		return out
	}
	for _, part := range strings.Split(s, "/") {
		if len(part) >= 3 && !suffixParts[part] {
			out = append(out, part)
		}
	}
	return out
}

func baseCall(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if i := strings.Index(s, "/"); i > 0 {
		// DL9ET/P -> DL9ET; EA8/DL9ET keeps the longer part.
		parts := strings.Split(s, "/")
		best := parts[0]
		for _, p := range parts[1:] {
			if len(p) > len(best) {
				best = p
			}
		}
		return best
	}
	return s
}

// Tier says how closely a token matched a log callsign.
type Tier int

const (
	TierNear     Tier = 1 // one insert/delete/substitute away after folding
	TierSkeleton Tier = 2 // equal after folding confusable characters
	TierExact    Tier = 3 // identical
)

func (t Tier) String() string {
	switch t {
	case TierExact:
		return "exact"
	case TierSkeleton:
		return "skeleton"
	case TierNear:
		return "near"
	}
	return "none"
}

// CallHit is a log callsign found on the card.
type CallHit struct {
	Call    string // as stored in the log
	Tier    Tier
	Token   Token  // evidence
	Variant string // token variant that matched (e.g. without /P)
}

func betterHit(a, b CallHit) bool {
	if a.Tier != b.Tier {
		return a.Tier > b.Tier
	}
	return betterToken(a.Token, b.Token)
}

type poolEntry struct {
	call  string
	skels []string
}

// Pool indexes the log's callsigns for matching.
type Pool struct {
	entries []poolEntry
	exact   map[string][]int
	skel    map[string][]int
}

// NewPool indexes the given callsigns (any case, duplicates allowed).
func NewPool(calls []string) *Pool {
	p := &Pool{exact: map[string][]int{}, skel: map[string][]int{}}
	seen := map[string]bool{}
	for _, c := range calls {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		i := len(p.entries)
		e := poolEntry{call: c}
		for _, v := range variants(c) {
			sv := Skeleton(v)
			e.skels = append(e.skels, sv)
			p.exact[v] = append(p.exact[v], i)
			p.skel[sv] = append(p.skel[sv], i)
		}
		p.entries = append(p.entries, e)
	}
	return p
}

// Len is the number of distinct callsigns in the pool.
func (p *Pool) Len() int { return len(p.entries) }

// Match returns the log callsigns found among the tokens, best first: tier,
// then token rank and confidence, then callsign. myCall (the station's own
// call, printed on every card as the addressee) is never matched, nor is
// anything that folds to it.
func (p *Pool) Match(tokens []Token, myCall string) []CallHit {
	mySkel := ""
	if myCall != "" {
		mySkel = Skeleton(baseCall(myCall))
	}
	best := map[string]CallHit{}
	consider := func(i int, tier Tier, tk Token, v string) {
		e := p.entries[i]
		if mySkel != "" && Skeleton(baseCall(e.call)) == mySkel {
			return
		}
		h := CallHit{Call: e.call, Tier: tier, Token: tk, Variant: v}
		if cur, ok := best[e.call]; !ok || betterHit(h, cur) {
			best[e.call] = h
		}
	}
	for _, tk := range tokens {
		for _, v := range variants(tk.Text) {
			sv := Skeleton(v)
			if mySkel != "" && sv == mySkel {
				continue
			}
			for _, i := range p.exact[v] {
				consider(i, TierExact, tk, v)
			}
			for _, i := range p.skel[sv] {
				consider(i, TierSkeleton, tk, v)
			}
			if len(sv) < 4 {
				continue
			}
			for i, e := range p.entries {
				for _, es := range e.skels {
					if len(es) >= 4 && es != sv && within1(sv, es) {
						consider(i, TierNear, tk, v)
						break
					}
				}
			}
		}
	}
	hits := make([]CallHit, 0, len(best))
	for _, h := range best {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if betterHit(hits[i], hits[j]) != betterHit(hits[j], hits[i]) {
			return betterHit(hits[i], hits[j])
		}
		return hits[i].Call < hits[j].Call
	})
	return hits
}

// within1 reports whether a and b differ by at most one insertion,
// deletion or substitution.
func within1(a, b string) bool {
	la, lb := len(a), len(b)
	if la-lb > 1 || lb-la > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < la && j < lb {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		switch {
		case la > lb:
			i++
		case lb > la:
			j++
		default:
			i++
			j++
		}
	}
	edits += (la - i) + (lb - j)
	return edits <= 1
}
