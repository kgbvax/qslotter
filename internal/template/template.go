// Package template loads QSL card layout templates (YAML field coordinates).
package template

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
//
// The JSON tags mirror the YAML names: the layout editor (internal/web,
// /settings/cards) exchanges the same structure with the browser.
type Template struct {
	Name     string  `yaml:"name" json:"name"`
	WidthMM  float64 `yaml:"width_mm" json:"width_mm"`
	HeightMM float64 `yaml:"height_mm" json:"height_mm"`
	// PreviewImage is a scan or photo of the pre-printed card shown behind
	// the fields in the layout editor, a file next to the template. It is
	// never printed.
	PreviewImage string  `yaml:"preview_image,omitempty" json:"preview_image,omitempty"`
	Rows         RowsCfg `yaml:"rows" json:"rows"`
	Fields       []Field `yaml:"fields" json:"fields"`
}

// RowsCfg is the template's rows block: how many QSO rows one card holds
// (Max) and the vertical distance between them in millimetres (PitchMM).
type RowsCfg struct {
	Max     int     `yaml:"max" json:"max"`
	PitchMM float64 `yaml:"pitch_mm" json:"pitch_mm"`
}

// Field kinds: a text (the default), a line from (X, Y) to (X+W, Y+H) or a
// rectangle W x H with its top-left corner at (X, Y). Shapes are drawn once
// per card.
const (
	KindText = "text"
	KindLine = "line"
	KindRect = "rect"
)

type Field struct {
	// Kind is "text" (also when empty), "line" or "rect".
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Name is the placeholder to substitute. Once per card:
	//   call, name, qth, my_call, my_name, my_qth (station.qth), qslmsg,
	//   via (prints "via <manager call>" on a manager card, nothing otherwise),
	//   address (the station's postal address, one line under the other
	//   from y_mm, only on a card sent direct - not via a manager),
	//   route (D, B, MD or MB: how the card is sent, to sort the cards)
	// Once per QSO row (see Template.Rows):
	//   qso_date, time_on, band, mode, rst_sent, rst_rcvd, freq, sat_name, freq_rx
	// Any other name (e.g. "text") prints only its Text.
	Name string  `yaml:"name,omitempty" json:"name,omitempty"`
	Text string  `yaml:"text,omitempty" json:"text,omitempty"` // literal text; if set, used verbatim (e.g. "QSL 73")
	X    float64 `yaml:"x_mm" json:"x_mm"`                     // anchor of the text, see Align
	// Y is the vertical middle of the text line; for a row field it is the
	// line of the first row.
	Y float64 `yaml:"y_mm" json:"y_mm"`
	// W and H are the extent of a line or rectangle (unused for text).
	W float64 `yaml:"w_mm,omitempty" json:"w_mm,omitempty"`
	H float64 `yaml:"h_mm,omitempty" json:"h_mm,omitempty"`
	// StrokeMM is the line width of a line or rectangle; 0 = DefaultStrokeMM.
	StrokeMM float64 `yaml:"stroke_mm,omitempty" json:"stroke_mm,omitempty"`
	// FontSize is in points. A text that would come closer than 4 mm to
	// the card edge it grows towards (see Align) is printed smaller, down
	// to 6 pt, and cut with "..." if even that is too wide.
	FontSize float64 `yaml:"font_size,omitempty" json:"font_size,omitempty"`
	// Align places the text relative to X: "L" (default) starts it at X,
	// "C" centres it on X, "R" ends it at X.
	Align string `yaml:"align,omitempty" json:"align,omitempty"`
	Font  string `yaml:"font,omitempty" json:"font,omitempty"` // "Helvetica" (default), "Times", "Courier"
	// Style is "" (regular) or "B" (bold).
	Style string `yaml:"style,omitempty" json:"style,omitempty"`
	// When limits the field to some cards: "" = every card, "sat" = only a
	// card with a satellite QSO on it (e.g. the heading of a satellite
	// column, which an HF card would carry empty).
	When string `yaml:"when,omitempty" json:"when,omitempty"`
}

// WhenSat is Field.When for a field printed only on satellite cards.
const WhenSat = "sat"

// The card size of a template that names none: DL9ET's 140x90 mm cards.
const (
	DefaultWidthMM  = 140.0
	DefaultHeightMM = 90.0
)

// DefaultStrokeMM is the line width of a shape whose stroke_mm is not set.
const DefaultStrokeMM = 0.3

// Limits of a text's font size in points (Validate).
const (
	MinFontPt = 4.0
	MaxFontPt = 72.0
)

// cardFieldNames are the field names drawn once per card, in menu order.
var cardFieldNames = []string{"call", "name", "qth", "my_call", "my_name", "my_qth", "via", "address", "route", "qslmsg"}

// rowFieldNames are the field names drawn once per QSO row, in menu order.
var rowFieldNames = []string{"qso_date", "time_on", "band", "mode", "rst_sent", "rst_rcvd", "freq", "sat_name", "freq_rx"}

// rowFields are the field names drawn once per QSO row.
var rowFields = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range rowFieldNames {
		m[n] = true
	}
	return m
}()

var cardFields = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range cardFieldNames {
		m[n] = true
	}
	return m
}()

// CardFieldNames lists the once-per-card field names (call, name, ...).
func CardFieldNames() []string { return append([]string(nil), cardFieldNames...) }

// RowFieldNames lists the once-per-QSO-row field names (qso_date, band, ...).
func RowFieldNames() []string { return append([]string(nil), rowFieldNames...) }

// IsRowField reports whether a field with this name is drawn once per QSO
// row (qso_date, time_on, band, mode, rst_sent, rst_rcvd, freq, sat_name,
// freq_rx) rather than once per card.
func IsRowField(name string) bool {
	return rowFields[strings.ToLower(name)]
}

// IsCardField reports whether name is a once-per-card data field (call,
// name, qth, my_call, my_name, my_qth, via, address, route, qslmsg).
func IsCardField(name string) bool {
	return cardFields[strings.ToLower(name)]
}

// Fonts are the PDF core fonts a field may use.
var Fonts = []string{"Helvetica", "Times", "Courier"}

// IsText reports whether the field is a text (kind empty or "text").
func (f *Field) IsText() bool {
	k := strings.ToLower(f.Kind)
	return k == "" || k == KindText
}

// IsShape reports whether the field is a line or rectangle.
func (f *Field) IsShape() bool {
	k := strings.ToLower(f.Kind)
	return k == KindLine || k == KindRect
}

// Stroke is the shape's line width in mm (StrokeMM or DefaultStrokeMM).
func (f *Field) Stroke() float64 {
	if f.StrokeMM > 0 {
		return f.StrokeMM
	}
	return DefaultStrokeMM
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

// HasField reports whether the template draws a field with this name (e.g.
// "qslmsg": without it a card note is not printed).
func (t *Template) HasField(name string) bool {
	for _, f := range t.Fields {
		if strings.EqualFold(f.Name, name) {
			return true
		}
	}
	return false
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
	t.ApplyDefaults()
	return &t, nil
}

// ApplyDefaults fills in what a file may leave out: the 140x90 mm card and,
// per text field, 12 pt Helvetica aligned left.
func (t *Template) ApplyDefaults() {
	if t.WidthMM == 0 {
		t.WidthMM = DefaultWidthMM
	}
	if t.HeightMM == 0 {
		t.HeightMM = DefaultHeightMM
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		f.Kind = strings.ToLower(f.Kind)
		if f.Kind == KindText {
			f.Kind = "" // the default; keeps the file as it always was
		}
		if !f.IsText() {
			continue
		}
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
}

// Validate reports the first thing wrong with the template: a card without
// a size, several rows without a pitch, or a field with an unknown kind,
// font, alignment or style, a text field with neither a known data name nor
// a literal text, or a font size outside MinFontPt..MaxFontPt. Call it on a
// template with defaults applied (Load does).
func (t *Template) Validate() error {
	if t.WidthMM <= 0 || t.HeightMM <= 0 {
		return fmt.Errorf("card size %gx%g mm: width and height must be positive", t.WidthMM, t.HeightMM)
	}
	if t.WidthMM > 1000 || t.HeightMM > 1000 {
		return fmt.Errorf("card size %gx%g mm: larger than 1000 mm", t.WidthMM, t.HeightMM)
	}
	if t.Rows.Max > 1 && t.Rows.PitchMM <= 0 {
		return errors.New("rows: pitch_mm must be positive for more than one row")
	}
	if t.Rows.Max > 50 {
		return fmt.Errorf("rows: max %d is more than 50", t.Rows.Max)
	}
	for i := range t.Fields {
		if err := t.Fields[i].validate(); err != nil {
			return fmt.Errorf("field %d: %w", i+1, err)
		}
	}
	return nil
}

func (f *Field) validate() error {
	switch strings.ToLower(f.When) {
	case "", WhenSat:
	default:
		return fmt.Errorf("when %q: must be empty or sat", f.When)
	}
	switch strings.ToLower(f.Kind) {
	case "", KindText:
		name := strings.ToLower(f.Name)
		if f.Text == "" && !IsCardField(name) && !IsRowField(name) {
			return fmt.Errorf("%q is not a data field and has no text", f.Name)
		}
		if f.FontSize < MinFontPt || f.FontSize > MaxFontPt {
			return fmt.Errorf("font size %g pt: must be %g to %g", f.FontSize, MinFontPt, MaxFontPt)
		}
		switch strings.ToUpper(f.Align) {
		case "", "L", "C", "R":
		default:
			return fmt.Errorf("align %q: must be L, C or R", f.Align)
		}
		if f.Font != "" && !knownFont(f.Font) {
			return fmt.Errorf("font %q: must be one of %s", f.Font, strings.Join(Fonts, ", "))
		}
		switch strings.ToUpper(f.Style) {
		case "", "B":
		default:
			return fmt.Errorf("style %q: must be empty or B", f.Style)
		}
	case KindLine, KindRect:
		if f.W < 0 || f.H < 0 {
			return fmt.Errorf("%s with negative size %gx%g mm", f.Kind, f.W, f.H)
		}
		if f.W == 0 && f.H == 0 {
			return fmt.Errorf("%s without a size", f.Kind)
		}
		if f.StrokeMM < 0 || f.StrokeMM > 10 {
			return fmt.Errorf("stroke %g mm: must be 0 to 10", f.StrokeMM)
		}
	default:
		return fmt.Errorf("kind %q: must be text, line or rect", f.Kind)
	}
	return nil
}

func knownFont(name string) bool {
	for _, f := range Fonts {
		if strings.EqualFold(f, name) {
			return true
		}
	}
	return false
}

// header opens every template file the editor writes.
const header = "# qslotter card layout - edited under Settings > Card layout.\n# Coordinates in millimetres from the card's top-left corner; font sizes in points.\n"

// Marshal returns the template as a YAML document (with a short header).
func (t *Template) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(t)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), body...), nil
}

// Save writes the template to path: to a temporary file first, then renamed
// into place, so a crash never leaves a half-written layout.
func (t *Template) Save(path string) error {
	raw, err := t.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".card-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Clone returns a deep copy.
func (t *Template) Clone() *Template {
	c := *t
	c.Fields = append([]Field(nil), t.Fields...)
	return &c
}

// Default returns a sane built-in template for DL9ET's standard 140x90mm
// card: one QSO per card (rows.max 1 - a station with several open QSOs
// gets one card each; raise it in the layout editor for several rows), under
// small column labels; the satellite column and its heading print only on a
// card with a satellite QSO.
//
// Vertical budget (Y is the middle of a line; the baseline sits 0.3*size
// below it, Helvetica caps rise 0.72*size above the baseline, descenders
// drop 0.21*size below it, size in mm = pt*0.353): title 4.6-11.1,
// my_call 16.3-23.4, my_name 25.2-29.2, "Confirming" 32.2-36.2,
// call/name 39.5-46.1, via 48.2-52.2, labels 55.3-57.9, rows 60.1-77.3,
// 73 line 82.2-86.2. Columns (13 pt, widest realistic value): date 6-29.5,
// UTC 36-47.5, band 52-66, mode 70-85, RST 90-103, satellite 108-136.
func Default() *Template {
	t := &Template{
		Name: "default", WidthMM: 140, HeightMM: 90,
		Rows: RowsCfg{Max: 1, PitchMM: 6.5},
		Fields: []Field{
			{Name: "text", Text: "QSL CARD", X: 70, Y: 7.5, FontSize: 20, Align: "C", Font: "Helvetica"},
			{Name: "my_call", X: 6, Y: 19.5, FontSize: 22, Align: "L", Font: "Helvetica"},
			{Name: "my_name", X: 6, Y: 27, FontSize: 12, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Confirming 2-way QSO with:", X: 6, Y: 34, FontSize: 12, Align: "L", Font: "Helvetica"},
			{Name: "call", X: 6, Y: 42.5, FontSize: 20, Align: "L", Font: "Helvetica"},
			{Name: "name", X: 84, Y: 42.5, FontSize: 14, Align: "L", Font: "Helvetica"},
			{Name: "via", X: 6, Y: 50, FontSize: 12, Align: "L", Font: "Helvetica"},
			// The card note (set when the card is sent to printing), right of "via".
			{Name: "qslmsg", X: 44, Y: 50, FontSize: 10, Align: "L", Font: "Helvetica"},
			// Column labels over the QSO rows.
			{Name: "text", Text: "Date", X: 6, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "UTC", X: 36, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Band", X: 52, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Mode", X: 70, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "RST", X: 90, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "Satellite", X: 108, Y: 56.5, FontSize: 8, Align: "L", Font: "Helvetica", When: WhenSat},
			// The QSO row at Y 62 (with rows.max > 1, further rows every 6.5 mm).
			{Name: "qso_date", X: 6, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "time_on", X: 36, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "band", X: 52, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "mode", X: 70, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "rst_sent", X: 90, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "sat_name", X: 108, Y: 62, FontSize: 13, Align: "L", Font: "Helvetica"},
			{Name: "text", Text: "73 de DL9ET", X: 70, Y: 84, FontSize: 12, Align: "C", Font: "Helvetica"},
		},
	}
	t.ApplyDefaults()
	return t
}
