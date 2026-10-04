package printer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-pdf/fpdf"
)

func TestCP1252Text(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Łukasz Wiśniewski", "Lukasz Wisniewski"},
		{"Jiří Novák", "Jirí Novák"}, // í and á are in cp1252, ř is not
		{"Müller", "Müller"},
		{"Straße", "Straße"},
		{"Šimun Žagar", "Šimun Žagar"}, // Š/Ž are in cp1252
		{"Čačak", "Cacak"},
		{"Erdős Pál", "Erdös Pál"},
		{"Ştefan Ţurcanu, Țepeș", "Stefan Turcanu, Tepes"},
		{"İzmir, Kadıköy", "Izmir, Kadiköy"},
		{"Kaļķis, Ģirts, Jānis", "Kalkis, Girts, Janis"},
		{"Jir\u030ci", "Jiri"},                                     // decomposed ř: the combining caron is dropped
		{"\ufeffAnna\u200b \u200eKowalska\u200f", "Anna Kowalska"}, // BOM, zero-width and bidi marks are dropped
		{"Москва", "??????"},                                       // no stand-in: '?', never '.'
		{"DL1ABC", "DL1ABC"},
		{"", ""},
	} {
		if got := cp1252Text(c.in); got != c.want {
			t.Errorf("cp1252Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCP1252MatchesFpdf pins inCP1252 to fpdf's own cp1252 map and checks
// that every stand-in is printable and replaces only unprintable runes.
func TestCP1252MatchesFpdf(t *testing.T) {
	tr := fpdf.New("P", "mm", "A4", "").UnicodeTranslatorFromDescriptor("")
	for r := rune(0); r < 0x3000; r++ {
		if r == '.' {
			continue
		}
		if fpdfHas := tr(string(r)) != "."; inCP1252(r) != fpdfHas {
			t.Errorf("inCP1252(%U %q) = %v, fpdf's cp1252 map says %v", r, r, inCP1252(r), fpdfHas)
		}
	}
	for r, rep := range cp1252Fold {
		if inCP1252(r) {
			t.Errorf("cp1252Fold has %U %q, which cp1252 holds", r, r)
		}
		if strings.ContainsFunc(rep, func(r rune) bool { return !inCP1252(r) }) {
			t.Errorf("cp1252Fold[%q] = %q is not cp1252", r, rep)
		}
	}
}

// TestRenderCardCP1252 checks the printed bytes: letters cp1252 does not
// hold come out as their stand-ins (not '.'), cp1252 letters as themselves.
func TestRenderCardCP1252(t *testing.T) {
	tmpl := &template.Template{WidthMM: 100, HeightMM: 74, Fields: []template.Field{
		{Name: "name", X: 4, Y: 20, FontSize: 12, Font: "Helvetica"},
	}}
	for _, c := range []struct{ name, want string }{
		{"Łukasz Wiśniewski", "(Lukasz Wisniewski)"},
		{"Jiří Novák", "(Jir\xed Nov\xe1k)"},
		{"Müller", "(M\xfcller)"},
	} {
		pdf, err := buildPDF(tmpl, CardFields{Name: c.name, Rows: testRows(1)}, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pdf.SetCompression(false)
		var buf bytes.Buffer
		if err := pdf.Output(&buf); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(buf.Bytes(), []byte(c.want)) {
			t.Errorf("%q: PDF does not contain %q", c.name, c.want)
		}
	}
}
