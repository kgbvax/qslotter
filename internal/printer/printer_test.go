package printer

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-pdf/fpdf"
)

func TestRenderPDF(t *testing.T) {
	tmpl := template.Default()
	out := filepath.Join(t.TempDir(), "card.pdf")
	fields := QSOFields{
		Call: "DL1ABC", Name: "Hans", QSODate: "20240101", TimeOn: "120000",
		Band: "20m", Mode: "SSB", RSTSent: "59", MyCall: "DL9ET",
	}
	if err := RenderPDF(out, tmpl, fields); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 500 {
		t.Fatalf("PDF is %d bytes, too small", info.Size())
	}
	// Sanity: %PDF- header
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr := make([]byte, 5)
	if _, err := f.Read(hdr); err != nil {
		t.Fatal(err)
	}
	if string(hdr) != "%PDF-" {
		t.Fatalf("PDF header = %q, want %%PDF-", string(hdr))
	}
}

func TestMaxRows(t *testing.T) {
	cases := []struct {
		name string
		tmpl *template.Template
		want int
	}{
		{"default", template.Default(), 1},
		{"nil template", nil, 1},
		{"no rows block", &template.Template{}, 1},
		{"explicit", &template.Template{Rows: template.RowsCfg{Max: 5, PitchMM: 4}}, 5},
		{"max zero", &template.Template{Rows: template.RowsCfg{Max: 0, PitchMM: 4}}, 1},
		{"no pitch", &template.Template{Rows: template.RowsCfg{Max: 3}}, 1},
	}
	for _, c := range cases {
		if got := MaxRows(c.tmpl); got != c.want {
			t.Errorf("%s: MaxRows = %d, want %d", c.name, got, c.want)
		}
	}
}

// testRows returns n distinct QSO rows (bands 1m, 2m, ...).
func testRows(n int) []QSORow {
	rows := make([]QSORow, n)
	for i := range rows {
		rows[i] = QSORow{
			QSODate: "20240101", TimeOn: "1200", Band: string(rune('1'+i)) + "m",
			Mode: "SSB", RSTSent: "59",
		}
	}
	return rows
}

// pageCount counts the page objects in a written PDF file.
func pageCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.HasPrefix(s, "%PDF-") {
		t.Fatalf("%s is not a PDF", path)
	}
	return strings.Count(s, "/Type /Page") - strings.Count(s, "/Type /Pages")
}

func TestRenderCardPages(t *testing.T) {
	tmpl := template.Default()
	max := MaxRows(tmpl)
	for _, c := range []struct{ rows, pages int }{
		{1, 1}, {max, 1}, {max + 1, 2}, {2*max + 1, 3},
	} {
		card := CardFields{
			Call: "DL1ABC", Name: "Jürgen Müller", MyCall: "DL9ET", Via: "K2ABC",
			Rows: testRows(c.rows),
		}
		pdf, err := buildPDF(tmpl, []CardFields{card}, RenderOptions{})
		if err != nil {
			t.Fatalf("%d rows: %v", c.rows, err)
		}
		if got := pdf.PageCount(); got != c.pages {
			t.Errorf("%d rows: %d pages, want %d", c.rows, got, c.pages)
		}
		out := filepath.Join(t.TempDir(), "card.pdf")
		if err := RenderCard(out, tmpl, card); err != nil {
			t.Fatalf("%d rows: %v", c.rows, err)
		}
		if got := pageCount(t, out); got != c.pages {
			t.Errorf("%d rows: file has %d pages, want %d", c.rows, got, c.pages)
		}
	}
}

// A print run is one PDF: every card's pages in order, a card with more QSOs
// than the template holds still continues on its own further pages.
func TestRenderCardsOneJob(t *testing.T) {
	tmpl := template.Default()
	max := MaxRows(tmpl)
	cards := []CardFields{
		{Call: "DL1ABC", MyCall: "DL9ET", QSLMsg: "Tnx for the QSO", Rows: testRows(1)},
		{Call: "K1A", MyCall: "DL9ET", Rows: testRows(max + 1)},
		{Call: "JA1XYZ", MyCall: "DL9ET", Rows: testRows(max)},
	}
	out := filepath.Join(t.TempDir(), "run.pdf")
	if err := RenderCards(out, tmpl, cards, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := pageCount(t, out); got != 4 {
		t.Errorf("run has %d pages, want 4", got)
	}
	if err := RenderCards(out, tmpl, append(cards, CardFields{Call: "EMPTY"}), RenderOptions{}); err == nil {
		t.Error("a card without rows in the run succeeded, want error")
	}
}

func TestRenderCardNoRows(t *testing.T) {
	out := filepath.Join(t.TempDir(), "card.pdf")
	if err := RenderCard(out, template.Default(), CardFields{Call: "DL1ABC"}); err == nil {
		t.Fatal("RenderCard with zero rows succeeded, want error")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("RenderCard with zero rows wrote %s (stat err %v)", out, err)
	}
	if err := RenderCard(out, nil, CardFields{Rows: testRows(1)}); err == nil {
		t.Fatal("RenderCard without a template succeeded, want error")
	}
}

func TestChunkRows(t *testing.T) {
	var sizes []int
	for _, c := range chunkRows(testRows(7), 3) {
		sizes = append(sizes, len(c))
	}
	if len(sizes) != 3 || sizes[0] != 3 || sizes[1] != 3 || sizes[2] != 1 {
		t.Fatalf("chunkRows(7, 3) sizes = %v, want [3 3 1]", sizes)
	}
	if got := chunkRows(testRows(3), 3); len(got) != 1 {
		t.Fatalf("chunkRows(3, 3) = %d cards, want 1", len(got))
	}
}

// fixedWidth measures every rune as 2 mm, so alignment offsets are exact.
func fixedWidth(_, _ string, _ float64, s string) float64 {
	return 2 * float64(len([]rune(s)))
}

// findOps returns the draw ops with the given text.
func findOps(ops []drawOp, text string) []drawOp {
	var found []drawOp
	for _, op := range ops {
		if op.Text == text {
			found = append(found, op)
		}
	}
	return found
}

func TestLayoutRows(t *testing.T) {
	tmpl := &template.Template{
		WidthMM: 100, HeightMM: 74,
		Rows: template.RowsCfg{Max: 3, PitchMM: 5.5},
		Fields: []template.Field{
			{Name: "call", X: 4, Y: 30},
			{Name: "band", X: 40, Y: 50},
			{Name: "qso_date", X: 4, Y: 50},
			{Name: "text", Text: "Band", X: 40, Y: 45},
		},
	}
	card := CardFields{Call: "dl1abc", Rows: testRows(3)}
	ops := layout(tmpl, card, card.Rows, fixedWidth)

	if got := findOps(ops, "DL1ABC"); len(got) != 1 || got[0].Y != 30 {
		t.Fatalf("call ops = %+v, want one at Y 30", got)
	}
	if got := findOps(ops, "Band"); len(got) != 1 || got[0].Y != 45 {
		t.Fatalf("label ops = %+v, want one at Y 45", got)
	}
	if got := findOps(ops, "2024-01-01"); len(got) != 3 {
		t.Fatalf("date ops = %+v, want one per row", got)
	}
	for i, r := range card.Rows {
		got := findOps(ops, r.Band)
		want := 50 + float64(i)*5.5
		if len(got) != 1 || got[0].X != 40 || math.Abs(got[0].Y-want) > 1e-9 {
			t.Errorf("row %d band ops = %+v, want one at (40, %.1f)", i, got, want)
		}
	}

	// A page with fewer rows than the template holds leaves the rest empty.
	ops = layout(tmpl, card, card.Rows[:1], fixedWidth)
	if got := findOps(ops, "2024-01-01"); len(got) != 1 || got[0].Y != 50 {
		t.Fatalf("one-row date ops = %+v, want one at Y 50", got)
	}
}

func TestLayoutAlign(t *testing.T) {
	tmpl := &template.Template{Fields: []template.Field{
		{Name: "text", Text: "LEFT", X: 50, Y: 10, Align: "L"},
		{Name: "text", Text: "CENTRE", X: 50, Y: 20, Align: "C"},
		{Name: "text", Text: "RIGHT", X: 50, Y: 30, Align: "r"},
		{Name: "text", Text: "NONE", X: 50, Y: 40},
	}}
	ops := layout(tmpl, CardFields{}, testRows(1), fixedWidth)
	want := map[string]float64{
		"LEFT":   50,          // starts at X
		"CENTRE": 50 - 12.0/2, // 6 runes = 12 mm, centred on X
		"RIGHT":  50 - 10,     // 5 runes = 10 mm, ends at X
		"NONE":   50,          // default L
	}
	if len(ops) != len(want) {
		t.Fatalf("got %d ops, want %d: %+v", len(ops), len(want), ops)
	}
	for _, op := range ops {
		if op.X != want[op.Text] {
			t.Errorf("%s: X = %v, want %v", op.Text, op.X, want[op.Text])
		}
	}
}

func TestLayoutVia(t *testing.T) {
	tmpl := &template.Template{Fields: []template.Field{{Name: "via", X: 4, Y: 40}}}
	ops := layout(tmpl, CardFields{Via: "k2abc"}, testRows(1), fixedWidth)
	if len(ops) != 1 || ops[0].Text != "via K2ABC" {
		t.Fatalf("manager card ops = %+v, want one \"via K2ABC\"", ops)
	}
	ops = layout(tmpl, CardFields{}, testRows(1), fixedWidth)
	if len(ops) != 0 {
		t.Fatalf("card without manager ops = %+v, want none", ops)
	}
}

// fpdfWidth measures with the real core-font metrics, in cp1252 like
// buildPDF.
func fpdfWidth() measureFunc {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	return func(font, style string, size float64, s string) float64 {
		pdf.SetFont(font, style, size)
		return pdf.GetStringWidth(tr(s))
	}
}

// TestDefaultTemplateFits lays out full default cards with long but
// realistic values and checks that no two texts overlap and everything stays
// on the card, at least fitMarginMM from its left and right edge.
func TestDefaultTemplateFits(t *testing.T) {
	tmpl := template.Default()
	var rows []QSORow
	for _, r := range []QSORow{
		{QSODate: "20240101", TimeOn: "235959", Band: "2190m", Mode: "DOMINO", RSTSent: "599"},
		{QSODate: "20241231", TimeOn: "000000", Band: "160m", Mode: "OLIVIA", RSTSent: "59+20"},
		{QSODate: "20240615", TimeOn: "120000", Band: "70cm", Mode: "PSK31", RSTSent: "579", SatName: "TEVEL-12"},
	} {
		rows = append(rows, r)
	}
	for _, name := range []string{
		"Hans Mustermann",
		"Hans-Joachim Müller",
		"Jean-Pierre Lefebvre-Dubois",
		"Hans-Joachim Müller-Lüdenscheidt",
		"Łukasz Wiśniewski-Grzegorzewski",
	} {
		card := CardFields{
			Call: "VP2V/DL9ET", Name: name, MyCall: "DL9ET",
			MyName: "Ingomar Otter-Hohenzollern-Sigmaringen", Via: "KC4AAA",
			Rows: rows,
		}
		ops := layout(tmpl, card, card.Rows, fpdfWidth())

		type box struct{ l, r, t, b float64 }
		boxes := make([]box, len(ops))
		for i, op := range ops {
			// CellFormat with h=0 puts the baseline 0.3*size below Y; Helvetica
			// caps rise 0.718*size above it, descenders drop 0.207*size below.
			size := op.FontSize * 25.4 / 72
			base := op.Y + 0.3*size
			boxes[i] = box{op.X, op.X + op.W, base - 0.718*size, base + 0.207*size}
			b := boxes[i]
			if b.l < fitMarginMM-1e-9 || b.r > tmpl.WidthMM-fitMarginMM+1e-9 || b.t < 0 || b.b > tmpl.HeightMM {
				t.Errorf("%q (%.1f-%.1f x %.1f-%.1f) leaves the %vx%v card less %v mm margin",
					op.Text, b.l, b.r, b.t, b.b, tmpl.WidthMM, tmpl.HeightMM, fitMarginMM)
			}
			if op.FontSize < minFontPt {
				t.Errorf("%q set at %v pt, below %v pt", op.Text, op.FontSize, minFontPt)
			}
		}
		for i := range boxes {
			for j := i + 1; j < len(boxes); j++ {
				a, b := boxes[i], boxes[j]
				if a.l < b.r && b.l < a.r && a.t < b.b && b.t < a.b {
					t.Errorf("%q overlaps %q", ops[i].Text, ops[j].Text)
				}
			}
		}

		// Centred fields are centred on their X (the old renderer ignored Align).
		for _, text := range []string{"QSL CARD", "73 de DL9ET"} {
			got := findOps(ops, text)
			if len(got) != 1 || math.Abs(got[0].X+got[0].W/2-tmpl.WidthMM/2) > 1e-9 {
				t.Errorf("%q ops = %+v, want one centred on X %v", text, got, tmpl.WidthMM/2)
			}
		}
	}
}

// TestLayoutShrinkToFit checks the steps of fitting a long text: first a
// smaller font, then, at minFontPt, cutting it.
func TestLayoutShrinkToFit(t *testing.T) {
	// 0.2 mm per rune and point: 20 runes at 12 pt = 48 mm, at 9 pt = 36 mm.
	scaled := func(_, _ string, size float64, s string) float64 {
		return 0.2 * size * float64(len([]rune(s)))
	}
	tmpl := &template.Template{WidthMM: 100, Fields: []template.Field{
		{Name: "name", X: 60, Y: 35, FontSize: 12}, // room 100-4-60 = 36 mm
	}}
	long := "ABCDEFGHIJ KLMNOPQRS" // 20 runes

	ops := layout(tmpl, CardFields{Name: long}, testRows(1), scaled)
	if len(ops) != 1 || ops[0].Text != long || ops[0].FontSize != 9 || ops[0].W > 36 {
		t.Fatalf("shrunk ops = %+v, want %q at 9 pt within 36 mm", ops, long)
	}
	ops = layout(tmpl, CardFields{Name: "Short"}, testRows(1), scaled)
	if len(ops) != 1 || ops[0].FontSize != 12 {
		t.Fatalf("short ops = %+v, want the template's 12 pt", ops)
	}

	// fixedWidth ignores the size, so only cutting helps: 36 mm hold 18
	// runes, 15 of the text plus "...".
	ops = layout(tmpl, CardFields{Name: "ABCDEFGHIJ KLMNOPQRSTUVWXYZ"}, testRows(1), fixedWidth)
	if len(ops) != 1 || ops[0].Text != "ABCDEFGHIJ KLMN..." || ops[0].FontSize != minFontPt || ops[0].W > 36 {
		t.Fatalf("cut ops = %+v, want \"ABCDEFGHIJ KLMN...\" at %v pt within 36 mm", ops, minFontPt)
	}
	// A space right before the cut is dropped.
	ops = layout(tmpl, CardFields{Name: "ABCDEFGHIJKLMN OPQRSTUV"}, testRows(1), fixedWidth)
	if len(ops) != 1 || ops[0].Text != "ABCDEFGHIJKLMN..." {
		t.Fatalf("cut at a space ops = %+v, want \"ABCDEFGHIJKLMN...\"", ops)
	}
}

func TestRoomFor(t *testing.T) {
	for _, c := range []struct {
		align string
		x     float64
		want  float64
	}{
		{"L", 60, 36}, {"", 4, 92}, {"R", 60, 56}, {"r", 2, -2},
		{"C", 50, 92}, {"C", 20, 32}, {"C", 90, 12},
	} {
		if got := roomFor(c.align, c.x, 100); got != c.want {
			t.Errorf("roomFor(%q, %v, 100) = %v, want %v", c.align, c.x, got, c.want)
		}
	}
}

// TestLayoutClamp checks that a text never starts left of the card (fpdf
// reads a negative X from the right edge) and, when it fits on the card,
// never ends right of it, even when its anchor leaves no room.
func TestLayoutClamp(t *testing.T) {
	tmpl := &template.Template{WidthMM: 100, Fields: []template.Field{
		{Name: "text", Text: "A very long centred text near the edge", X: 1, Y: 10, Align: "C"},
		{Name: "text", Text: "Right-aligned at the left edge", X: 1, Y: 20, Align: "R"},
		{Name: "text", Text: "Left of the card", X: -5, Y: 30, Align: "L"},
		{Name: "text", Text: "A very long centred text near the right edge", X: 99, Y: 40, Align: "C"},
	}}
	for _, m := range []struct {
		name    string
		measure measureFunc
	}{{"fixed", fixedWidth}, {"fpdf", fpdfWidth()}} {
		ops := layout(tmpl, CardFields{}, testRows(1), m.measure)
		if len(ops) != len(tmpl.Fields) {
			t.Fatalf("%s: got %d ops, want %d: %+v", m.name, len(ops), len(tmpl.Fields), ops)
		}
		for _, op := range ops {
			if op.X < 0 || op.X+op.W > tmpl.WidthMM {
				t.Errorf("%s: %q at X %v, %v mm wide, leaves the card", m.name, op.Text, op.X, op.W)
			}
		}
	}

	for _, c := range []struct{ left, w, want float64 }{
		{-3, 6, 0},     // negative: to the left edge
		{10, 20, 10},   // inside: unchanged
		{96, 6, 94},    // past the right edge: back onto the card
		{-10, 120, 0},  // wider than the card: starts at the left edge
		{-0.5, 100, 0}, // exactly the card width
	} {
		if got := clampX(c.left, c.w, 100); got != c.want {
			t.Errorf("clampX(%v, %v, 100) = %v, want %v", c.left, c.w, got, c.want)
		}
	}
}

func TestTempPDFPathUnique(t *testing.T) {
	a, b := TempPDFPath(), TempPDFPath()
	t.Cleanup(func() { os.Remove(a); os.Remove(b) })
	if a == b {
		t.Fatalf("TempPDFPath returned %q twice", a)
	}
	for _, p := range []string{a, b} {
		if filepath.Ext(p) != ".pdf" || !strings.HasPrefix(filepath.Base(p), "qslotter_") {
			t.Errorf("TempPDFPath = %q, want qslotter_*.pdf", p)
		}
	}

	// Without a usable temp directory the fallback names are unique too.
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	a, b = TempPDFPath(), TempPDFPath()
	if a == b {
		t.Fatalf("fallback TempPDFPath returned %q twice", a)
	}
}

// TestLayoutShapesBoldSat: shapes come out as they are, once per card;
// bold text is measured bold; the satellite fields print per row.
func TestLayoutShapesBoldSat(t *testing.T) {
	tmpl := &template.Template{WidthMM: 100, HeightMM: 74, Rows: template.RowsCfg{Max: 2, PitchMM: 5},
		Fields: []template.Field{
			{Kind: template.KindLine, X: 4, Y: 45, W: 92},
			{Kind: template.KindRect, X: 2, Y: 2, W: 96, H: 70, StrokeMM: 0.5},
			{Name: "call", X: 4, Y: 30, FontSize: 16, Style: "B"},
			{Name: "call", X: 4, Y: 40, FontSize: 16},
			{Name: "sat_name", X: 4, Y: 50, FontSize: 10},
			{Name: "freq_rx", X: 40, Y: 50, FontSize: 10},
		}}
	card := CardFields{Call: "DL1ABC", Rows: []QSORow{{SatName: "RS-44", FreqRX: "435.645"}, {SatName: "QO-100", FreqRX: "10489.750"}}}
	ops := layout(tmpl, card, card.Rows, fpdfWidth())
	var line, rect []drawOp
	for _, op := range ops {
		switch op.Kind {
		case template.KindLine:
			line = append(line, op)
		case template.KindRect:
			rect = append(rect, op)
		}
	}
	if len(line) != 1 || line[0].W != 92 || line[0].StrokeMM != template.DefaultStrokeMM || line[0].Row != -1 {
		t.Fatalf("line ops = %+v", line)
	}
	if len(rect) != 1 || rect[0].H != 70 || rect[0].StrokeMM != 0.5 || rect[0].Field != 1 {
		t.Fatalf("rect ops = %+v", rect)
	}
	calls := findOps(ops, "DL1ABC")
	if len(calls) != 2 || calls[0].Style != "B" || calls[1].Style != "" || calls[0].W <= calls[1].W {
		t.Fatalf("call ops = %+v, want the bold one wider", calls)
	}
	for i, want := range []string{"RS-44", "QO-100"} {
		got := findOps(ops, want)
		if len(got) != 1 || got[0].Row != i || got[0].Field != 4 || math.Abs(got[0].Y-(50+5*float64(i))) > 1e-9 {
			t.Errorf("sat ops %q = %+v", want, got)
		}
	}
	if got := findOps(ops, "10489.750"); len(got) != 1 || got[0].Row != 1 {
		t.Errorf("freq_rx ops = %+v", got)
	}
}

// TestRenderOptions: offset, ruler and label render, with bold text and
// shapes, as a valid PDF; the label lands on the page.
func TestRenderOptions(t *testing.T) {
	tmpl := template.Default()
	tmpl.Rows.Max = 3
	tmpl.Fields = append(tmpl.Fields, template.Field{Kind: template.KindRect, X: 1, Y: 1, W: 98, H: 72},
		template.Field{Name: "sat_name", X: 4, Y: 66, FontSize: 8, Style: "B"})
	card := CardFields{Call: "DL1ABC", Rows: testRows(4)}
	card.Rows[0].SatName = "RS-44"
	pdf, err := buildPDF(tmpl, []CardFields{card}, RenderOptions{OffsetXMM: -1.5, OffsetYMM: 0.5, Ruler: true, Label: "test card"})
	if err != nil {
		t.Fatal(err)
	}
	pdf.SetCompression(false)
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.HasPrefix(s, "%PDF-") || !strings.Contains(s, "(test card)") || !strings.Contains(s, "(RS-44)") || !strings.Contains(s, "(90)") {
		t.Fatal("PDF lacks the label, the satellite or the ruler numbers")
	}
	if pdf.PageCount() != 2 {
		t.Fatalf("pages = %d, want 2", pdf.PageCount())
	}
	out := filepath.Join(t.TempDir(), "test.pdf")
	if err := Render(out, tmpl, card, RenderOptions{Ruler: true}); err != nil {
		t.Fatal(err)
	}
	if got := pageCount(t, out); got != 2 {
		t.Fatalf("file pages = %d, want 2", got)
	}
	raw, err := RenderBytes(tmpl, card, RenderOptions{})
	if err != nil || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		t.Fatalf("RenderBytes = %d bytes, %v", len(raw), err)
	}
}

// TestPreview: one card's elements as the editor draws them - every card
// field once, row fields per row of the first card, the fitted and the
// outside ones marked.
func TestPreview(t *testing.T) {
	tmpl := template.Default()
	tmpl.Rows.Max = 3
	tmpl.Fields = append(tmpl.Fields,
		template.Field{Kind: template.KindLine, X: 100, Y: 70, W: 60},                                     // runs off the card
		template.Field{Name: "text", Text: "low", X: 4, Y: 89.5, FontSize: 12, Align: "L", Font: "Times"}) // below the edge
	card := CardFields{Call: "DL1ABC", Name: "Hans-Joachim Müller-Lüdenscheidt von und zu Hohenzollern", MyCall: "DL9ET",
		Rows: testRows(5)}
	ops, pages, err := Preview(tmpl, card)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Errorf("pages = %d, want 2", pages)
	}
	count := map[int]int{}
	for _, op := range ops {
		count[op.Field]++
		if op.Kind == "text" && (op.Baseline <= op.Y || op.H <= 0 || op.Font == "") {
			t.Errorf("text op %+v lacks its metrics", op)
		}
	}
	for i, f := range tmpl.Fields {
		want := 1
		switch {
		case f.When == template.WhenSat || f.Name == "sat_name":
			want = 0 // no satellite QSO on this card
		case template.IsRowField(f.Name):
			want = 3
		case f.Name == "via" || f.Name == "my_name" || f.Name == "qslmsg":
			want = 0 // no value on this card
		}
		if count[i] != want {
			t.Errorf("field %d (%s %q): %d ops, want %d", i, f.Name, f.Text, count[i], want)
		}
	}
	var name, line, low *Op
	for i := range ops {
		switch {
		case ops[i].Kind == "line":
			line = &ops[i]
		case strings.HasPrefix(ops[i].Text, "Hans"):
			name = &ops[i]
		case ops[i].Text == "low":
			low = &ops[i]
		}
	}
	if name == nil || !name.Fitted || name.Outside {
		t.Errorf("long name op = %+v, want fitted, on the card", name)
	}
	if line == nil || !line.Outside || low == nil || !low.Outside {
		t.Errorf("line %+v / low text %+v, want both outside", line, low)
	}
	if _, _, err := Preview(nil, card); err == nil {
		t.Error("Preview without a template succeeded")
	}
	// No QSOs (an empty sample): the card fields still show.
	if ops, pages, err := Preview(tmpl, CardFields{Call: "X1X"}); err != nil || pages != 1 || len(ops) == 0 {
		t.Errorf("empty-row preview = %d ops, %d pages, %v", len(ops), pages, err)
	}
}

// TestSatelliteColumn: the default card carries the satellite column and its
// heading only when a QSO on that card was made via a satellite.
func TestSatelliteColumn(t *testing.T) {
	tmpl := template.Default()
	hf := CardFields{Call: "DL1ABC", Rows: []QSORow{{QSODate: "20240101", TimeOn: "1200", Band: "20m", Mode: "SSB", RSTSent: "59"}}}
	if ops := layout(tmpl, hf, hf.Rows, fpdfWidth()); len(findOps(ops, "Satellite")) != 0 {
		t.Error("an HF card carries the satellite heading")
	}
	sat := CardFields{Call: "DL1ABC", Rows: []QSORow{
		{QSODate: "20240101", TimeOn: "1200", Band: "20m", Mode: "SSB", RSTSent: "59"},
		{QSODate: "20240102", TimeOn: "1300", Band: "70cm", Mode: "FM", RSTSent: "59", SatName: "SO-50", FreqRX: "436.795"},
	}}
	ops := layout(tmpl, sat, sat.Rows, fpdfWidth())
	if len(findOps(ops, "Satellite")) != 1 || len(findOps(ops, "SO-50")) != 1 {
		t.Fatalf("satellite card ops lack the heading or the satellite: %+v", ops)
	}
	// Only the card (page) holding the satellite QSO: with one row per card
	// the HF card stays without the column.
	one := tmpl.Clone()
	one.Rows = template.RowsCfg{Max: 1, PitchMM: 6.5}
	if ops := layout(one, sat, sat.Rows[:1], fpdfWidth()); len(findOps(ops, "Satellite")) != 0 {
		t.Error("the HF page of a split card carries the satellite heading")
	}
}
