package qpc

import (
	"regexp"
	"strings"
)

var (
	bioSpacesRe = regexp.MustCompile(`[ \t\r\f\v\x{a0}]+`)
	bioBlankRe  = regexp.MustCompile(`\n{3,}`)
)

// PrepareBio tidies a bio for the prompt: runs of spaces collapse, lines are
// trimmed, at most one blank line in a row survives, and the text is cut to
// max characters (at a word boundary when one is near). truncated reports a
// cut; max <= 0 means no cap.
func PrepareBio(bio string, max int) (text string, truncated bool) {
	lines := strings.Split(bio, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(bioSpacesRe.ReplaceAllString(ln, " "))
	}
	text = strings.TrimSpace(bioBlankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	r := []rune(text)
	if max <= 0 || len(r) <= max {
		return text, false
	}
	cut := max
	for i := max; i > max-200 && i > 0; i-- {
		if r[i] == ' ' || r[i] == '\n' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(r[:cut])) + " [...]", true
}

// qslSentenceRe marks the sentences of a bio that may bear on QSL cards, in
// the languages seen on QRZ: cards, routes, managers, return postage,
// electronic confirmations and postal addresses.
var qslSentenceRe = regexp.MustCompile(`(?i)qsl|card|karte|tarjeta|carte|cartolin|kaart|kartk|bureau|buro|b[üu]ro|bur[óo]|direct|direkt|diret|manager|\bvia\b|s\.?a\.?s?\.?e\b|\birc\b|green ?stamp|postage|porto|oqrs|lotw|e-?qsl|club ?log|hamaward|paypal|\$|\busd\b|\beur|€|address|adress|indirizzo|direcci|p\.? ?o\.? ?box|postfach`)

var sentenceEndRe = regexp.MustCompile(`\n+|[.!?]\s+`)

// FocusBio is PrepareBio for a small context: a bio within max characters
// stays whole; a longer one is reduced to its sentences about QSL cards
// (qslSentenceRe), then cut to max. focused reports the reduction.
func FocusBio(bio string, max int) (text string, focused bool) {
	text, _ = PrepareBio(bio, 0)
	if max <= 0 || len([]rune(text)) <= max {
		return text, false
	}
	var keep []string
	for _, s := range sentenceEndRe.Split(text, -1) {
		if s = strings.TrimSpace(s); s != "" && qslSentenceRe.MatchString(s) {
			keep = append(keep, s)
		}
	}
	if len(keep) == 0 {
		return "[a long bio; no sentence in it mentions QSL cards]", true
	}
	text, _ = PrepareBio("[only the sentences about QSL cards from a longer bio] "+strings.Join(keep, ". "), max)
	return text, true
}
