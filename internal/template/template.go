// Package template loads QSL card layout templates (YAML field coordinates).
package template

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Template describes how to render QSO data onto a card. All coordinates are
// millimetres from the top-left corner of the card. Fields are substituted by
// name from a QSO record (see FieldFor).
type Template struct {
	Name     string  `yaml:"name"`
	WidthMM  float64 `yaml:"width_mm"`
	HeightMM float64 `yaml:"height_mm"`
	Fields   []Field `yaml:"fields"`
}

type Field struct {
	// Name is the placeholder to substitute from the QSO. Supported built-ins:
	//   call, name, qso_date, time_on, band, mode, rst_sent, rst_rcvd,
	//   my_call, my_name, qth, freq, qslmsg
	Name     string  `yaml:"name"`
	Text     string  `yaml:"text"` // literal text; if set, used verbatim (e.g. "QSL 73")
	X        float64 `yaml:"x_mm"`
	Y        float64 `yaml:"y_mm"`
	FontSize float64 `yaml:"font_size"` // points
	Align    string  `yaml:"align"`     // "L" (default), "C", "R"
	Font     string  `yaml:"font"`      // "Helvetica" (default), "Times", "Courier"
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

// Default returns a sane built-in template for DL9ET's standard 100x74mm card.
func Default() *Template {
	t := &Template{
		Name: "default", WidthMM: 100, HeightMM: 74,
		Fields: []Field{
			{Name: "text", Text: "QSL CARD", X: 50, Y: 6, FontSize: 16, Align: "C", Font: "Helvetica"},
			{Name: "my_call", X: 4, Y: 22, FontSize: 18, Font: "Helvetica"},
			{Name: "my_name", X: 4, Y: 29, FontSize: 10, Font: "Helvetica"},
			{Name: "text", Text: "Confirming 2-way QSO with:", X: 4, Y: 32, FontSize: 10, Font: "Helvetica"},
			{Name: "call", X: 4, Y: 40, FontSize: 16, Font: "Helvetica"},
			{Name: "name", X: 60, Y: 40, FontSize: 12, Font: "Helvetica"},
			{Name: "qso_date", X: 4, Y: 50, FontSize: 11, Font: "Helvetica"},
			{Name: "time_on", X: 38, Y: 50, FontSize: 11, Font: "Helvetica"},
			{Name: "band", X: 60, Y: 50, FontSize: 11, Font: "Helvetica"},
			{Name: "mode", X: 78, Y: 50, FontSize: 11, Font: "Helvetica"},
			{Name: "rst_sent", X: 4, Y: 56, FontSize: 11, Font: "Helvetica"},
			{Name: "text", Text: "73 de DL9ET", X: 50, Y: 68, FontSize: 10, Align: "C", Font: "Helvetica"},
		},
	}
	return t
}
