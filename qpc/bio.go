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
