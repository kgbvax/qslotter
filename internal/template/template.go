// Package template loads QSL card layout templates (YAML field coordinates).
package template

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Template describes how to render QSO data onto a card. All coordinates are
// millimetres from the top-left corner of the card. Fields are substituted by
// name (see Field.Name).
//
// One card can confirm several QSOs with the same station. The optional rows
// block sets how many QSO rows a card holds and how far apart they are:
//
//	rows:
//	  max: 3        # QSO rows per card; missing or < 1 = 1
//	  pitch_mm: 5.5 # distance from one row to the next; must be > 0 for max > 1
//
// Row fields (see IsRowField) are drawn once per QSO, row i (0-based) at
// y_mm + i*pitch_mm; all other fields are drawn once per card. More QSOs
// than max continue on further cards.
type Template struct {
	Name     string  `yaml:"name"`
	WidthMM  float64 `yaml:"width_mm"`
	HeightMM float64 `yaml:"height_mm"`
	Rows     RowsCfg `yaml:"rows"`
	Fields   []Field `yaml:"fields"`
}

// RowsCfg is the template's rows block: how many QSO rows one card holds
// (Max) and the vertical distance between them in millimetres (PitchMM).
type RowsCfg struct {
	Max     int     `yaml:"max"`
	PitchMM float64 `yaml:"pitch_mm"`
}

type Field struct {
	// Name is the placeholder to substitute. Once per card:
	//   call, name, qth, my_call, my_name, qslmsg,
	//   via (prints "via <manager call>" on a manager card, nothing otherwise)
	// Once per QSO row (see Template.Rows):
	//   qso_date, time_on, band, mode, rst_sent, rst_rcvd, freq
	// Any other name (e.g. "text") prints only its Text.
	Name string  `yaml:"name"`
	Text string  `yaml:"text"` // literal text; if set, used verbatim (e.g. "QSL 73")
	X    float64 `yaml:"x_mm"` // anchor of the text, see Align
	// Y is the vertical middle of the text line; for a row field it is the
	// line of the first row.
	Y float64 `yaml:"y_mm"`
	// FontSize is in points. A text that would come closer than 4 mm to
	// the card edge it grows towards (see Align) is printed smaller, down
	// to 6 pt, and cut with "..." if even that is too wide.
	FontSize float64 `yaml:"font_size"`
	// Align places the text relative to X: "L" (default) starts it at X,
	// "C" centres it on X, "R" ends it at X.
	Align string `yaml:"align"`
	Font  string `yaml:"font"` // "Helvetica" (default), "Times", "Courier"
}

// rowFields are the field names drawn once per QSO row.
var rowFields = map[string]bool{
	"qso_date": true, "time_on": true, "band": true, "mode": true,
	"rst_sent": true, "rst_rcvd": true, "freq": true,
}

// IsRowField reports whether a field with this name is drawn once per QSO
// row (qso_date, time_on, band, mode, rst_sent, rst_rcvd, freq) rather than
// once per card.
func IsRowField(name string) bool {
	return rowFields[strings.ToLower(name)]
}

// MaxRows reports how many QSO rows one card holds: Rows.Max, or 1 when the
// rows block is missing, max < 1, or pitch_mm is not positive (the rows
// would print on top of each other).
func (t *Template) MaxRows() int {
	if t.Rows.Max < 1 || t.Rows.PitchMM <= 0 {
		return 1
	}
	return t.Rows.Max
}

// Load reads a YAML template file.
func Load(path string) (*Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Template
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	if t.WidthMM == 0 {
		t.WidthMM = 100
	}
	if t.HeightMM == 0 {
		t.HeightMM = 74
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		if f.FontSize == 0 {
			f.FontSize = 12
		}
		if f.Align == "" {
			f.Align = "L"
		}
		if f.Font == "" {
			f.Font = "Helvetica"
		}
	}
	return &t, nil
}

// Default returns a sane built-in template for DL9ET's standard 100x74mm
// card: up to three QSOs, one row each under small column labels.
//
// Vertical budget (Y is the middle of a line; Helvetica caps reach about
// 0.42*size above it, descenders 0.51*size below, size in mm = pt*0.353):
// title 3.6-8.9, my_call 13.3-19.2, my_name 21.0-24.3, "Confirming" 26.5-29.8,
// call/name 32.6-37.9, via 40.0-43.3, labels 46.0-48.3, rows 49.9-64.5,
// 73 line 67.5-70.8.
func Default() *Template {
	t := &Template{
		Name: "default", WidthMM: 100, HeightMM: 74,
		Rows: RowsCfg{Max: 3, PitchMM: 5.5},
		Fields: []Field{
			{Name: "text", Text: "QSL CARD", X: 50, Y: 6, FontSize: 16, Align: "C", Font: "Helvetica"},
			{Name: "my_call", X: 4, Y: 16, FontSize: 18, Align: "L", Font: "Helvetica"},
			{Name: "my_name", X: 4, Y: 22.5, FontSize: 10, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Confirming 2-way QSO with:", X: 4, Y: 28, FontSize: 10, Align: "L", Font: "Helvetica"},
			{Name: "call", X: 4, Y: 35, FontSize: 16, Align: "L", Font: "Helvetica"},
			{Name: "name", X: 60, Y: 35, FontSize: 12, Align: "L", Font: "Helvetica"},
			{Name: "via", X: 4, Y: 41.5, FontSize: 10, Align: "L", Font: "Helvetica"},
			// Column labels over the QSO rows.
			{Name: "text", Text: "Date", X: 4, Y: 47, FontSize: 7, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "UTC", X: 30, Y: 47, FontSize: 7, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Band", X: 46, Y: 47, FontSize: 7, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Mode", X: 62, Y: 47, FontSize: 7, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "RST", X: 82, Y: 47, FontSize: 7, Align: "L", Font: "Helvetica"},
			// One row per QSO, the first at Y 51.5, then every 5.5 mm.
			{Name: "qso_date", X: 4, Y: 51.5, FontSize: 11, Align: "L", Font: "Helvetica"},
			{Name: "time_on", X: 30, Y: 51.5, FontSize: 11, Align: "L", Font: "Helvetica"},
			{Name: "band", X: 46, Y: 51.5, FontSize: 11, Align: "L", Font: "Helvetica"},
			{Name: "mode", X: 62, Y: 51.5, FontSize: 11, Align: "L", Font: "Helvetica"},
			{Name: "rst_sent", X: 82, Y: 51.5, FontSize: 11, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "73 de DL9ET", X: 50, Y: 69, FontSize: 10, Align: "C", Font: "Helvetica"},
		},
	}
	return t
}
