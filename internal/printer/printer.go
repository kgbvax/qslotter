// Package printer renders QSL cards to PDF and sends them to the system printer.
//
// The PDF rendering is platform-independent (go-pdf/fpdf). The actual print
// dispatch is platform-specific and selected by build tags:
//   - printer_unix.go   (//go:build darwin || linux)   -> lp / lpstat
//   - printer_windows.go (//go:build windows)          -> SumatraPDF.exe
package printer

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-pdf/fpdf"
)

// QSORow is one confirmed QSO on a card.
type QSORow struct {
	QSODate, TimeOn, Band, Mode, RSTSent, RSTRcvd, Freq string
}

// CardFields is everything printed on one card (or one card per page when
// Rows exceed the template).
type CardFields struct {
	Call, Name, QTH, MyCall, MyName, QSLMsg string
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
	PrintPDF(path, printerName string, opts Options) error
}

type Options struct {
	PaperWMM float64
	PaperHMM float64
	Copies   int
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
	return RenderCards(path, tmpl, []CardFields{card})
}

// RenderCards renders several cards into one PDF at path, in order (one print
// job for a whole print run). Each card is laid out as RenderCard does.
func RenderCards(path string, tmpl *template.Template, cards []CardFields) error {
	pdf, err := buildPDF(tmpl, cards)
	if err != nil {
		return err
	}
	return pdf.OutputFileAndClose(path)
}

// MaxRows reports how many QSO rows one card of this template holds (>= 1).
func MaxRows(tmpl *template.Template) int {
	if tmpl == nil {
		return 1
	}
	return tmpl.MaxRows()
}

// buildPDF lays out the whole document, one page per MaxRows(tmpl) rows of
// each card, without writing it.
func buildPDF(tmpl *template.Template, cards []CardFields) (*fpdf.Fpdf, error) {
	if tmpl == nil {
		return nil, errors.New("printer: no card template")
	}
	if len(cards) == 0 {
		return nil, errors.New("printer: no cards")
	}
	for _, card := range cards {
		if len(card.Rows) == 0 {
			return nil, fmt.Errorf("printer: card for %s has no QSO rows", card.Call)
		}
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
	measure := func(font string, size float64, s string) float64 {
		pdf.SetFont(font, "", size)
		return pdf.GetStringWidth(tr(s))
	}
	for _, card := range cards {
		for _, rows := range chunkRows(card.Rows, MaxRows(tmpl)) {
			pdf.AddPage()
			for _, op := range layout(tmpl, card, rows, measure) {
				pdf.SetFont(op.Font, "", op.FontSize)
				pdf.SetXY(op.X, op.Y)
				pdf.CellFormat(op.W, 0, tr(op.Text), "", 0, "L", false, 0, "")
			}
		}
	}
	if err := pdf.Error(); err != nil {
		return nil, err
	}
	return pdf, nil
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

// drawOp is one text placed on a card page. X is the left edge of the text
// (the field's alignment already applied, kept on the card), Y the line
// position from the template (row fields shifted to their row), W the text
// width. Text and FontSize are what is printed: the field value made
// printable in cp1252 and, when it was too long, shrunk and cut to fit.
type drawOp struct {
	X, Y, W  float64
	Text     string
	Font     string
	FontSize float64
}

// measureFunc returns the width in mm of s set in font at size points.
type measureFunc func(font string, size float64, s string) float64

// Shrink-to-fit: a text that would come closer than fitMarginMM to the card
// edge it grows towards is set smaller, fontStepPt at a time down to
// minFontPt; if it still does not fit, it is cut and ends in ellipsis.
const (
	fitMarginMM = 4.0
	fontStepPt  = 0.5
	minFontPt   = 6.0
	ellipsis    = "..."
)

// layout places the fields of tmpl for one card page holding rows (at most
// MaxRows(tmpl)): card fields once, row fields once per row, row i shifted
// down by i*Rows.PitchMM. Fields with an empty value are left out. Texts
// too long for the card are shrunk to fit (see fitMarginMM).
func layout(tmpl *template.Template, card CardFields, rows []QSORow, measure measureFunc) []drawOp {
	width := tmpl.WidthMM
	if width <= 0 {
		width = 100 // template.Load's default
	}
	var ops []drawOp
	place := func(f template.Field, y float64, text string) {
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
		room := roomFor(f.Align, f.X, width)
		w := measure(font, size, text)
		for w > room && size > minFontPt {
			size = math.Max(size-fontStepPt, minFontPt)
			w = measure(font, size, text)
		}
		if w > room {
			text, w = cutToFit(text, room, func(s string) float64 { return measure(font, size, s) })
		}
		x := clampX(anchor(f.Align, f.X, w), w, width)
		ops = append(ops, drawOp{X: x, Y: y, W: w, Text: text, Font: font, FontSize: size})
	}
	for _, f := range tmpl.Fields {
		if template.IsRowField(f.Name) {
			for i, r := range rows {
				val := f.Text
				if val == "" {
					val = rowValue(f.Name, r)
				}
				place(f, f.Y+float64(i)*tmpl.Rows.PitchMM, val)
			}
			continue
		}
		val := f.Text
		if val == "" {
			val = cardValue(f.Name, card)
		}
		place(f, f.Y, val)
	}
	return ops
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
