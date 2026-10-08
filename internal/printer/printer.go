// Package printer renders QSL cards to PDF and sends them to the system printer.
//
// The PDF rendering is platform-independent (go-pdf/fpdf). The actual print
// dispatch is platform-specific and selected by build tags:
//   - printer_unix.go   (//go:build darwin || linux)   -> lp / lpstat
//   - printer_windows.go (//go:build windows)          -> SumatraPDF.exe
package printer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-pdf/fpdf"
)

// QSORow is one confirmed QSO on a card.
type QSORow struct {
	QSODate, TimeOn, Band, Mode, RSTSent, RSTRcvd, Freq string
	// SatName and FreqRX are the satellite name (ADIF SAT_NAME) and the
	// receive frequency (FREQ_RX) of a satellite or split QSO.
	SatName, FreqRX string
}

// CardFields is everything printed on one card (or one card per page when
// Rows exceed the template).
type CardFields struct {
	Call, Name, QTH, MyCall, MyName, QSLMsg string
	MyQTH                                   string // station.qth (field my_qth)
	// Via is the manager callsign for a manager card ("" otherwise); the
	// template field "via" prints "via <CALL>" when set, nothing otherwise.
	Via  string
	Rows []QSORow
}

// QSOFields is the data of a one-QSO card (see RenderPDF).
type QSOFields struct {
	Call    string
	Name    string
	QSODate string
	TimeOn  string
	Band    string
	Mode    string
	RSTSent string
	RSTRcvd string
	MyCall  string
	MyName  string
	QTH     string
	Freq    string
	QSLMsg  string
}

// Printer is the platform-agnostic interface.
type Printer interface {
	List() ([]string, error)
	Default() (string, error)
	// PrintPDF hands the PDF to the print system and returns the job; a
	// nil error means queued, not printed (see Watcher).
	PrintPDF(path, printerName string, opts Options) (Job, error)
}

// Job is a submitted print job. ID 0: the platform cannot follow it
// (Windows/SumatraPDF), so handing it over is all there is to know.
type Job struct {
	Printer string
	ID      int
}

// Watcher follows submitted jobs (CUPS on macOS and Linux): whether a job
// really printed, failed, or waits on a stopped printer.
type Watcher interface {
	JobStatus(ctx context.Context, job Job) (JobStatus, error)
	CancelJob(ctx context.Context, job Job) error
}

type Options struct {
	PaperWMM float64
	PaperHMM float64
	Copies   int
}

// RenderOptions adjust a rendered card beyond its template.
type RenderOptions struct {
	// OffsetXMM and OffsetYMM shift everything printed (printer.offset_mm):
	// they make up for a printer that feeds the card a little off.
	OffsetXMM, OffsetYMM float64
	// Ruler adds millimetre ticks along the top and left edge (a test card:
	// measure where the 10 mm tick lands to find the offset).
	Ruler bool
	// Label is printed small in the bottom right corner (a test card).
	Label string
}

// New returns the platform-specific Printer. Defined in printer_unix.go
// (//go:build darwin || linux) and printer_windows.go.

// RenderPDF renders a one-QSO card to a PDF file at path using the given
// template and QSO field values.
func RenderPDF(path string, tmpl *template.Template, fields QSOFields) error {
	return RenderCard(path, tmpl, CardFields{
		Call: fields.Call, Name: fields.Name, QTH: fields.QTH,
		MyCall: fields.MyCall, MyName: fields.MyName, QSLMsg: fields.QSLMsg,
		Rows: []QSORow{{
			QSODate: fields.QSODate, TimeOn: fields.TimeOn,
			Band: fields.Band, Mode: fields.Mode,
			RSTSent: fields.RSTSent, RSTRcvd: fields.RSTRcvd, Freq: fields.Freq,
		}},
	})
}

// RenderCard renders the card to a PDF at path. Rows beyond MaxRows(tmpl)
// continue on further pages (one page = one physical card; the shared fields
// repeat on every page). Zero rows is an error.
func RenderCard(path string, tmpl *template.Template, card CardFields) error {
	return Render(path, tmpl, card, RenderOptions{})
}

// Render is RenderCard with options: a printer offset, a test card's ruler
// and label.
func Render(path string, tmpl *template.Template, card CardFields, opts RenderOptions) error {
	pdf, err := buildPDF(tmpl, card, opts)
	if err != nil {
		return err
	}
	return pdf.OutputFileAndClose(path)
}

// RenderBytes is Render into memory (the layout editor's PDF view).
func RenderBytes(tmpl *template.Template, card CardFields, opts RenderOptions) ([]byte, error) {
	pdf, err := buildPDF(tmpl, card, opts)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MaxRows reports how many QSO rows one card of this template holds (>= 1).
func MaxRows(tmpl *template.Template) int {
	if tmpl == nil {
		return 1
	}
	return tmpl.MaxRows()
}

// ptToMM converts a font size in points to millimetres.
func ptToMM(pt float64) float64 { return pt * 25.4 / 72 }

// buildPDF lays out the whole card document, one page per MaxRows(tmpl)
// rows, without writing it.
func buildPDF(tmpl *template.Template, card CardFields, opts RenderOptions) (*fpdf.Fpdf, error) {
	if tmpl == nil {
		return nil, errors.New("printer: no card template")
	}
	if len(card.Rows) == 0 {
		return nil, errors.New("printer: card has no QSO rows")
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "mm",
		Size:           fpdf.SizeType{Wd: tmpl.WidthMM, Ht: tmpl.HeightMM},
	})
	pdf.SetAutoPageBreak(false, 0)
	// No inner cell padding: a field's X is the exact edge of its text.
	pdf.SetCellMargin(0)
	// The core fonts are cp1252; without this, umlauts in names and QTHs
	// would print as two garbage characters each. layout has already
	// replaced what cp1252 cannot hold (see cp1252Text).
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	measure := func(font, style string, size float64, s string) float64 {
		pdf.SetFont(font, style, size)
		return pdf.GetStringWidth(tr(s))
	}
	ox, oy := opts.OffsetXMM, opts.OffsetYMM
	for _, rows := range chunkRows(card.Rows, MaxRows(tmpl)) {
		pdf.AddPage()
		for _, op := range layout(tmpl, card, rows, measure) {
			switch op.Kind {
			case template.KindLine:
				pdf.SetLineWidth(op.StrokeMM)
				pdf.Line(op.X+ox, op.Y+oy, op.X+op.W+ox, op.Y+op.H+oy)
			case template.KindRect:
				pdf.SetLineWidth(op.StrokeMM)
				pdf.Rect(op.X+ox, op.Y+oy, op.W, op.H, "D")
			default:
				// Y is the middle of the line: the baseline sits 0.3 of the
				// font size below it (fpdf's CellFormat with h=0 does the
				// same). Text takes the position as is - SetXY would read a
				// negative coordinate (an offset past the edge) as measured
				// from the other edge.
				pdf.SetFont(op.Font, op.Style, op.FontSize)
				pdf.Text(op.X+ox, op.Y+oy+0.3*ptToMM(op.FontSize), tr(op.Text))
			}
		}
		if opts.Ruler {
			drawRuler(pdf, tmpl.WidthMM, tmpl.HeightMM, ox, oy)
		}
		if opts.Label != "" {
			pdf.SetFont("Helvetica", "", 5)
			s := tr(cp1252Text(opts.Label))
			pdf.Text(tmpl.WidthMM-2-pdf.GetStringWidth(s)+ox, tmpl.HeightMM-2+oy, s)
		}
	}
	if err := pdf.Error(); err != nil {
		return nil, err
	}
	return pdf, nil
}

// drawRuler draws millimetre ticks along the top and the left edge of a
// width x height card, shifted by the offset like everything else: 1 mm
// ticks short, every 5 mm longer, every 10 mm long with its number. On the
// printed card, the distance from the card edge to the tick marked 10 tells
// the offset (11.5 mm: the printer shifts by +1.5, set -1.5).
func drawRuler(pdf *fpdf.Fpdf, width, height, ox, oy float64) {
	pdf.SetLineWidth(0.1)
	pdf.SetFont("Helvetica", "", 4.5)
	tick := func(i int) float64 {
		switch {
		case i%10 == 0:
			return 3.5
		case i%5 == 0:
			return 2.2
		}
		return 1.2
	}
	for i := 1; float64(i) < width; i++ {
		x, l := float64(i)+ox, tick(i)
		pdf.Line(x, oy, x, oy+l)
		if i%10 == 0 {
			s := strconv.Itoa(i)
			pdf.Text(x-pdf.GetStringWidth(s)/2, oy+l+1.8, s)
		}
	}
	for i := 1; float64(i) < height; i++ {
		y, l := float64(i)+oy, tick(i)
		pdf.Line(ox, y, ox+l, y)
		if i%10 == 0 {
			pdf.Text(ox+l+0.6, y+0.55, strconv.Itoa(i))
		}
	}
}

// chunkRows splits rows into cards of at most n rows each.
func chunkRows(rows []QSORow, n int) [][]QSORow {
	var cards [][]QSORow
	for len(rows) > n {
		cards = append(cards, rows[:n])
		rows = rows[n:]
	}
	return append(cards, rows)
}

// drawOp is one element placed on a card page. For a text, X is the left
// edge of the text (the field's alignment already applied, kept on the
// card), Y the line position from the template (row fields shifted to their
// row), W the text width; Text, Style and FontSize are what is printed: the
// field value made printable in cp1252 and, when it was too long, shrunk and
// cut to fit (Fitted). For a line or rectangle (Kind), X/Y/W/H come from the
// template as they are. Field is the index of the template field, Row the
// QSO row (-1 for a once-per-card element).
type drawOp struct {
	Kind     string // "" text, template.KindLine, template.KindRect
	X, Y, W  float64
	H        float64
	Text     string
	Font     string
	Style    string
	FontSize float64
	StrokeMM float64
	Field    int
	Row      int
	Fitted   bool
}

// measureFunc returns the width in mm of s set in font and style ("" or
// "B") at size points.
type measureFunc func(font, style string, size float64, s string) float64

// Shrink-to-fit: a text that would come closer than fitMarginMM to the card
// edge it grows towards is set smaller, fontStepPt at a time down to
// minFontPt; if it still does not fit, it is cut and ends in ellipsis.
const (
	fitMarginMM = 4.0
	fontStepPt  = 0.5
	minFontPt   = 6.0
	ellipsis    = "..."
)

// FitMarginMM is how close shrink-to-fit lets a text come to the card edge
// it grows towards (the layout editor draws it as a guide).
const FitMarginMM = fitMarginMM

// layout places the fields of tmpl for one card page holding rows (at most
// MaxRows(tmpl)): card fields once, row fields once per row, row i shifted
// down by i*Rows.PitchMM, lines and rectangles once. Fields with an empty
// value are left out, and fields marked when: sat unless a QSO on this card
// has a satellite name. Texts too long for the card are shrunk to fit (see
// fitMarginMM).
func layout(tmpl *template.Template, card CardFields, rows []QSORow, measure measureFunc) []drawOp {
	width := tmpl.WidthMM
	if width <= 0 {
		width = template.DefaultWidthMM // template.Load's default
	}
	var ops []drawOp
	sat := false // a satellite QSO on this card: fields with when: sat print
	for _, r := range rows {
		sat = sat || strings.TrimSpace(r.SatName) != ""
	}
	place := func(idx, row int, f template.Field, y float64, text string) {
		text = cp1252Text(text)
		if text == "" {
			return
		}
		font, size := f.Font, f.FontSize
		if font == "" {
			font = "Helvetica"
		}
		if size == 0 {
			size = 12
		}
		style := ""
		if strings.EqualFold(f.Style, "B") {
			style = "B"
		}
		fitted := false
		room := roomFor(f.Align, f.X, width)
		if f.W > 0 {
			room = math.Min(room, f.W)
		}
		mw := func(s string) float64 { return measure(font, style, size, s) }
		maxLines := f.MaxLines()
		tooBig := func(ls []string) bool {
			if len(ls) > maxLines {
				return true
			}
			for _, l := range ls {
				if mw(l) > room {
					return true
				}
			}
			return false
		}
		lines := wrapText(text, room, maxLines, mw)
		for tooBig(lines) && size > minFontPt {
			size = math.Max(size-fontStepPt, minFontPt)
			lines = wrapText(text, room, maxLines, mw)
			fitted = true
		}
		if len(lines) > maxLines {
			// Even the smallest size needs more lines: cut in the last one.
			rest := strings.Join(lines[maxLines-1:], " ")
			lines = append(lines[:maxLines-1], rest)
			fitted = true
		}
		step := template.LineSpacing * ptToMM(size)
		for i, line := range lines {
			w := mw(line)
			if w > room {
				line, w = cutToFit(line, room, mw)
				fitted = true
			}
			x := clampX(anchor(f.Align, f.X, w), w, width)
			ops = append(ops, drawOp{X: x, Y: y + float64(i)*step, W: w, Text: line, Font: font, Style: style,
				FontSize: size, Field: idx, Row: row, Fitted: fitted})
		}
	}
	for i, f := range tmpl.Fields {
		if strings.EqualFold(f.When, template.WhenSat) && !sat {
			continue
		}
		if f.IsShape() {
			ops = append(ops, drawOp{Kind: strings.ToLower(f.Kind), X: f.X, Y: f.Y, W: f.W, H: f.H,
				StrokeMM: f.Stroke(), Field: i, Row: -1})
			continue
		}
		if !f.IsText() {
			continue // an unknown kind (Validate rejects it) prints nothing
		}
		if template.IsRowField(f.Name) {
			for r, qso := range rows {
				val := f.Text
				if val == "" {
					val = rowValue(f.Name, qso)
				}
				place(i, r, f, f.Y+float64(r)*tmpl.Rows.PitchMM, val)
			}
			continue
		}
		val := f.Text
		if val == "" {
			val = cardValue(f.Name, card)
		}
		place(i, -1, f, f.Y, val)
	}
	return ops
}

// Op is one element of a card as the layout editor draws it: the same
// placement the printed card gets (layout), in millimetres from the card's
// top-left corner.
type Op struct {
	Kind  string  `json:"kind"`  // "text", "line" or "rect"
	Field int     `json:"field"` // index of the template field
	Row   int     `json:"row"`   // QSO row, -1 for a once-per-card element
	X     float64 `json:"x"`     // text: left edge; shape: as in the template
	Y     float64 `json:"y"`     // text: middle of the line; shape: as in the template
	W     float64 `json:"w"`
	H     float64 `json:"h"` // text: the font size in mm
	// Baseline is where the text sits (Y + 0.3 * the font size in mm).
	Baseline float64 `json:"baseline,omitempty"`
	Text     string  `json:"text,omitempty"`
	Font     string  `json:"font,omitempty"`
	Style    string  `json:"style,omitempty"`
	FontSize float64 `json:"font_size,omitempty"` // points, after shrink-to-fit
	Stroke   float64 `json:"stroke,omitempty"`
	// Fitted: the text was too long and printed smaller or cut.
	Fitted bool `json:"fitted,omitempty"`
	// Outside: part of the element is off the card.
	Outside bool `json:"outside,omitempty"`
}

// Preview lays out the first card of card with tmpl as the printer would
// and returns its elements, plus how many cards the QSOs fill.
func Preview(tmpl *template.Template, card CardFields) ([]Op, int, error) {
	if tmpl == nil {
		return nil, 0, errors.New("printer: no card template")
	}
	rows := card.Rows
	if len(rows) == 0 {
		rows = []QSORow{{}}
	}
	chunks := chunkRows(rows, MaxRows(tmpl))
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	measure := func(font, style string, size float64, s string) float64 {
		pdf.SetFont(font, style, size)
		return pdf.GetStringWidth(tr(s))
	}
	w, h := tmpl.WidthMM, tmpl.HeightMM
	const eps = 1e-9
	var out []Op
	for _, d := range layout(tmpl, card, chunks[0], measure) {
		op := Op{Kind: d.Kind, Field: d.Field, Row: d.Row, X: d.X, Y: d.Y, W: d.W, H: d.H,
			Stroke: d.StrokeMM, Fitted: d.Fitted}
		if d.Kind == "" {
			sz := ptToMM(d.FontSize)
			op.Kind = template.KindText
			op.H = sz
			op.Baseline = d.Y + 0.3*sz
			op.Text, op.Font, op.Style, op.FontSize = d.Text, d.Font, d.Style, d.FontSize
			// Caps rise about 0.72 of the size above the baseline,
			// descenders drop about 0.21 below it.
			op.Outside = d.X < -eps || d.X+d.W > w+eps || op.Baseline-0.72*sz < -eps || op.Baseline+0.21*sz > h+eps
		} else {
			op.Outside = d.X < -eps || d.Y < -eps || d.X+d.W > w+eps || d.Y+d.H > h+eps
		}
		out = append(out, op)
	}
	if pdf.Error() != nil {
		return nil, 0, pdf.Error()
	}
	return out, len(chunks), nil
}

// wrapText breaks text into lines at most room wide (by width) at spaces.
// With max 1 it returns the text as one line (shrinking and cutting are the
// caller's). A word wider than room is split. It may return more than max
// lines: the caller sets the text smaller then.
func wrapText(text string, room float64, max int, width func(string) float64) []string {
	if max <= 1 || width(text) <= room {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(text) {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if width(try) <= room {
			cur = try
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
		}
		// A word longer than a whole line: split it.
		for width(word) > room {
			r := []rune(word)
			n := len(r) - 1
			for n > 1 && width(string(r[:n])) > room {
				n--
			}
			lines = append(lines, string(r[:n]))
			word = string(r[n:])
		}
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// anchor returns the left edge of a text w mm wide whose template anchor is
// x: "L" (default) starts the text at x, "C" centres it on x, "R" ends it at x.
func anchor(align string, x, w float64) float64 {
	switch strings.ToUpper(align) {
	case "C":
		return x - w/2
	case "R":
		return x - w
	}
	return x
}

// roomFor returns how wide a text anchored at x may be so that it keeps
// fitMarginMM from the card edges it grows towards on a card width mm wide:
// "L" grows right, "R" grows left, "C" both ways. It can be <= 0 for an
// anchor inside the margin.
func roomFor(align string, x, width float64) float64 {
	switch strings.ToUpper(align) {
	case "C":
		return 2 * math.Min(x-fitMarginMM, width-fitMarginMM-x)
	case "R":
		return x - fitMarginMM
	}
	return width - fitMarginMM - x
}

// cutToFit drops runes from the end of text and appends ellipsis until the
// result is at most room wide (by width); when not even one rune fits it
// returns the ellipsis alone. It returns the text and its width.
func cutToFit(text string, room float64, width func(string) float64) (string, float64) {
	r := []rune(text)
	for len(r) > 0 {
		r = r[:len(r)-1]
		s := strings.TrimRight(string(r), " ") + ellipsis
		if w := width(s); w <= room {
			return s, w
		}
	}
	return ellipsis, width(ellipsis)
}

// clampX keeps a text w mm wide whose left edge is left on a card width mm
// wide: never past the right edge when it fits on the card, and never left
// of the left edge (fpdf reads a negative X as "from the right edge", which
// would move the text off the card).
func clampX(left, w, width float64) float64 {
	if w <= width && left+w > width {
		left = width - w
	}
	return math.Max(left, 0)
}

// cardValue is the value of a once-per-card field.
func cardValue(name string, c CardFields) string {
	switch strings.ToLower(name) {
	case "call":
		return strings.ToUpper(c.Call)
	case "name":
		return c.Name
	case "qth":
		return c.QTH
	case "my_call":
		return strings.ToUpper(c.MyCall)
	case "my_name":
		return c.MyName
	case "my_qth":
		return c.MyQTH
	case "qslmsg":
		return c.QSLMsg
	case "via":
		if v := strings.TrimSpace(c.Via); v != "" {
			return "via " + strings.ToUpper(v)
		}
	}
	return ""
}

// rowValue is the value of a once-per-QSO field.
func rowValue(name string, r QSORow) string {
	switch strings.ToLower(name) {
	case "qso_date":
		return formatDate(r.QSODate)
	case "time_on":
		return formatTime(r.TimeOn)
	case "band":
		return r.Band
	case "mode":
		return r.Mode
	case "rst_sent":
		return r.RSTSent
	case "rst_rcvd":
		return r.RSTRcvd
	case "freq":
		return r.Freq
	case "sat_name":
		return r.SatName
	case "freq_rx":
		return r.FreqRX
	}
	return ""
}

// formatDate turns an ADIF YYYYMMDD into "YYYY-MM-DD".
func formatDate(s string) string {
	if len(s) == 8 {
		return s[0:4] + "-" + s[4:6] + "-" + s[6:8]
	}
	return s
}

// formatTime turns an ADIF HHMMSS into "HH:MM".
func formatTime(s string) string {
	if len(s) == 6 {
		return s[0:2] + ":" + s[2:4]
	}
	if len(s) == 4 {
		return s[0:2] + ":" + s[2:4]
	}
	return s
}

// tempSeq makes TempPDFPath's fallback names unique within the process.
var tempSeq atomic.Uint64

// TempPDFPath returns a new path for a card PDF in the system temp directory,
// unique per call so two prints never share (and overwrite) one file. The
// file normally exists, empty, when TempPDFPath returns; RenderCard
// overwrites it. The caller removes the file after printing.
func TempPDFPath() string {
	f, err := os.CreateTemp("", "qslotter_*.pdf")
	if err == nil {
		name := f.Name()
		f.Close()
		return name
	}
	return filepath.Join(os.TempDir(),
		fmt.Sprintf("qslotter_%d_%d.pdf", time.Now().UnixNano(), tempSeq.Add(1)))
}

// run is a small helper to execute a command and capture its output.
func run(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w (%s)", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
