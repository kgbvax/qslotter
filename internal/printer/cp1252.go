package printer

import (
	"strings"
	"unicode"
)

// The PDF core fonts (Helvetica, Times, Courier) hold only cp1252, and fpdf's
// translator prints every other rune as '.' ("Łukasz" -> ".ukasz").
// cp1252Text replaces those runes first.

// cp1252High are the characters cp1252 holds at 0x80-0x9F; 0xA0-0xFF are the
// Latin-1 characters with the same Unicode code points.
var cp1252High = map[rune]bool{
	'€': true, '‚': true, 'ƒ': true, '„': true, '…': true, '†': true, '‡': true,
	'ˆ': true, '‰': true, 'Š': true, '‹': true, 'Œ': true, 'Ž': true,
	'‘': true, '’': true, '“': true, '”': true, '•': true, '–': true, '—': true,
	'˜': true, '™': true, 'š': true, '›': true, 'œ': true, 'ž': true, 'Ÿ': true,
}

// inCP1252 reports whether cp1252 holds r.
func inCP1252(r rune) bool {
	return r < 0x80 || (r >= 0xA0 && r <= 0xFF) || cp1252High[r]
}

// cp1252Fold are stand-ins for characters cp1252 does not hold: the letters
// of Latin Extended-A (Polish, Czech, Slovak, Hungarian, Croatian, Serbian
// and Slovenian Latin, Romanian, Turkish, Baltic, Maltese, Esperanto, ...)
// without their diacritic, Romanian comma-below letters, and a few
// typographic dashes and spaces. Hungarian ő/ű become ö/ü, which cp1252
// holds and which read closer than o/u.
var cp1252Fold = map[rune]string{
	'Ā': "A", 'ā': "a", 'Ă': "A", 'ă': "a", 'Ą': "A", 'ą': "a",
	'Ć': "C", 'ć': "c", 'Ĉ': "C", 'ĉ': "c", 'Ċ': "C", 'ċ': "c", 'Č': "C", 'č': "c",
	'Ď': "D", 'ď': "d", 'Đ': "D", 'đ': "d",
	'Ē': "E", 'ē': "e", 'Ĕ': "E", 'ĕ': "e", 'Ė': "E", 'ė': "e", 'Ę': "E", 'ę': "e", 'Ě': "E", 'ě': "e",
	'Ĝ': "G", 'ĝ': "g", 'Ğ': "G", 'ğ': "g", 'Ġ': "G", 'ġ': "g", 'Ģ': "G", 'ģ': "g",
	'Ĥ': "H", 'ĥ': "h", 'Ħ': "H", 'ħ': "h",
	'Ĩ': "I", 'ĩ': "i", 'Ī': "I", 'ī': "i", 'Ĭ': "I", 'ĭ': "i", 'Į': "I", 'į': "i", 'İ': "I", 'ı': "i",
	'Ĳ': "IJ", 'ĳ': "ij", 'Ĵ': "J", 'ĵ': "j", 'Ķ': "K", 'ķ': "k", 'ĸ': "k",
	'Ĺ': "L", 'ĺ': "l", 'Ļ': "L", 'ļ': "l", 'Ľ': "L", 'ľ': "l", 'Ŀ': "L", 'ŀ': "l", 'Ł': "L", 'ł': "l",
	'Ń': "N", 'ń': "n", 'Ņ': "N", 'ņ': "n", 'Ň': "N", 'ň': "n", 'ŉ': "'n", 'Ŋ': "N", 'ŋ': "n",
	'Ō': "O", 'ō': "o", 'Ŏ': "O", 'ŏ': "o", 'Ő': "Ö", 'ő': "ö",
	'Ŕ': "R", 'ŕ': "r", 'Ŗ': "R", 'ŗ': "r", 'Ř': "R", 'ř': "r",
	'Ś': "S", 'ś': "s", 'Ŝ': "S", 'ŝ': "s", 'Ş': "S", 'ş': "s", 'Ș': "S", 'ș': "s", 'ſ': "s",
	'Ţ': "T", 'ţ': "t", 'Ť': "T", 'ť': "t", 'Ŧ': "T", 'ŧ': "t", 'Ț': "T", 'ț': "t",
	'Ũ': "U", 'ũ': "u", 'Ū': "U", 'ū': "u", 'Ŭ': "U", 'ŭ': "u", 'Ů': "U", 'ů': "u",
	'Ű': "Ü", 'ű': "ü", 'Ų': "U", 'ų': "u",
	'Ŵ': "W", 'ŵ': "w", 'Ŷ': "Y", 'ŷ': "y",
	'Ź': "Z", 'ź': "z", 'Ż': "Z", 'ż': "z",
	'\u2010': "-", '\u2011': "-", '\u2212': "-", // hyphen, non-breaking hyphen, minus
	'\u2009': " ", '\u200a': " ", '\u202f': " ", // thin, hair, narrow no-break space
	'\u2032': "'", '\u2033': "\"", // prime, double prime
}

// cp1252Text returns s with every rune cp1252 cannot hold replaced: by its
// stand-in from cp1252Fold, combining marks (decomposed accents) and
// invisible format characters (byte order mark, zero-width and bidi marks)
// dropped, anything else by '?'.
func cp1252Text(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return !inCP1252(r) }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case inCP1252(r):
			b.WriteRune(r)
		case unicode.In(r, unicode.Mn, unicode.Cf):
		default:
			if rep, ok := cp1252Fold[r]; ok {
				b.WriteString(rep)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}
