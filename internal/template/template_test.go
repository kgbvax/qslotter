package template

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadYAML writes body to a temp file and loads it as a template.
func loadYAML(t *testing.T, body string) *Template {
	t.Helper()
	path := filepath.Join(t.TempDir(), "card.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func TestLoadRows(t *testing.T) {
	tmpl := loadYAML(t, `
name: multi
rows:
  max: 4
  pitch_mm: 6.5
fields:
  - name: band
    x_mm: 40
    y_mm: 50
`)
	if tmpl.Rows.Max != 4 || tmpl.Rows.PitchMM != 6.5 {
		t.Fatalf("Rows = %+v, want {Max:4 PitchMM:6.5}", tmpl.Rows)
	}
	if got := tmpl.MaxRows(); got != 4 {
		t.Fatalf("MaxRows = %d, want 4", got)
	}
	// Load still fills the field defaults.
	if f := tmpl.Fields[0]; f.FontSize != 12 || f.Align != "L" || f.Font != "Helvetica" {
		t.Fatalf("field defaults = %+v", f)
	}
}

func TestMaxRows(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want int
	}{
		{"missing rows block", "name: plain\n", 1},
		{"max zero", "rows: {max: 0, pitch_mm: 5}\n", 1},
		{"negative max", "rows: {max: -2, pitch_mm: 5}\n", 1},
		{"no pitch", "rows: {max: 3}\n", 1},
		{"explicit", "rows: {max: 2, pitch_mm: 5}\n", 2},
	}
	for _, c := range cases {
		if got := loadYAML(t, c.yaml).MaxRows(); got != c.want {
			t.Errorf("%s: MaxRows = %d, want %d", c.name, got, c.want)
		}
	}
	if got := Default().MaxRows(); got != 1 {
		t.Errorf("Default: MaxRows = %d, want 1 (one QSO per card)", got)
	}
}

func TestIsRowField(t *testing.T) {
	for _, name := range []string{"qso_date", "time_on", "band", "mode", "rst_sent", "rst_rcvd", "freq", "BAND"} {
		if !IsRowField(name) {
			t.Errorf("IsRowField(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"call", "name", "qth", "my_call", "my_name", "qslmsg", "via", "text", ""} {
		if IsRowField(name) {
			t.Errorf("IsRowField(%q) = true, want false", name)
		}
	}
}

// TestDefaultLayout pins the parts of the default card the web layer relies
// on: a via field and one field per row column.
func TestDefaultLayout(t *testing.T) {
	d := Default()
	if d.WidthMM != 140 || d.HeightMM != 90 {
		t.Fatalf("size = %vx%v, want 140x90", d.WidthMM, d.HeightMM)
	}
	have := map[string]bool{}
	for _, f := range d.Fields {
		have[f.Name] = true
		if f.Y <= 0 || f.Y >= d.HeightMM {
			t.Errorf("%s %q at Y %v is off the card", f.Name, f.Text, f.Y)
		}
		if IsRowField(f.Name) && f.Y+float64(d.MaxRows()-1)*d.Rows.PitchMM >= d.HeightMM {
			t.Errorf("last row of %s is off the card", f.Name)
		}
	}
	for _, name := range []string{"my_call", "call", "name", "via", "qso_date", "time_on", "band", "mode", "rst_sent"} {
		if !have[name] {
			t.Errorf("default template has no %q field", name)
		}
	}
}

func TestIsRowFieldSatellite(t *testing.T) {
	for _, name := range []string{"sat_name", "freq_rx"} {
		if !IsRowField(name) {
			t.Errorf("IsRowField(%q) = false, want true", name)
		}
	}
	if len(RowFieldNames()) != 9 || len(CardFieldNames()) != 8 {
		t.Fatalf("field catalogs = %v / %v", RowFieldNames(), CardFieldNames())
	}
}

// TestLoadShapesAndStyle: the new kinds load with their sizes, bold stays,
// shapes get no text defaults, and an explicit "text" kind is the default.
func TestLoadShapesAndStyle(t *testing.T) {
	tmpl := loadYAML(t, `
fields:
  - name: call
    x_mm: 4
    y_mm: 30
    style: B
  - kind: line
    x_mm: 4
    y_mm: 45
    w_mm: 92
  - kind: RECT
    x_mm: 2
    y_mm: 2
    w_mm: 96
    h_mm: 70
    stroke_mm: 0.5
  - kind: text
    name: text
    text: hello
    x_mm: 1
    y_mm: 1
`)
	if err := tmpl.Validate(); err != nil {
		t.Fatal(err)
	}
	call, line, rect, txt := tmpl.Fields[0], tmpl.Fields[1], tmpl.Fields[2], tmpl.Fields[3]
	if !call.IsText() || call.Style != "B" || call.FontSize != 12 {
		t.Errorf("call = %+v", call)
	}
	if !line.IsShape() || line.W != 92 || line.H != 0 || line.Stroke() != DefaultStrokeMM || line.FontSize != 0 || line.Font != "" {
		t.Errorf("line = %+v", line)
	}
	if rect.Kind != KindRect || rect.Stroke() != 0.5 {
		t.Errorf("rect = %+v", rect)
	}
	if txt.Kind != "" || !txt.IsText() {
		t.Errorf("explicit text kind = %+v, want the default kind", txt)
	}
}

func TestValidate(t *testing.T) {
	ok := Default()
	if err := ok.Validate(); err != nil {
		t.Fatalf("default template: %v", err)
	}
	for name, mod := range map[string]func(*Template){
		"no width":         func(t *Template) { t.WidthMM = 0 },
		"rows no pitch":    func(t *Template) { t.Rows.Max, t.Rows.PitchMM = 3, 0 },
		"unknown kind":     func(t *Template) { t.Fields[0].Kind = "circle" },
		"unknown font":     func(t *Template) { t.Fields[0].Font = "Comic Sans" },
		"unknown align":    func(t *Template) { t.Fields[0].Align = "X" },
		"italic":           func(t *Template) { t.Fields[0].Style = "I" },
		"unknown when":     func(t *Template) { t.Fields[0].When = "hf" },
		"font too small":   func(t *Template) { t.Fields[0].FontSize = 2 },
		"unknown field":    func(t *Template) { t.Fields[1].Name = "callsign" },
		"empty text":       func(t *Template) { t.Fields[0].Text = "" },
		"shape no size":    func(t *Template) { t.Fields = append(t.Fields, Field{Kind: KindLine}) },
		"shape negative":   func(t *Template) { t.Fields = append(t.Fields, Field{Kind: KindRect, W: -1, H: 3}) },
		"stroke too thick": func(t *Template) { t.Fields = append(t.Fields, Field{Kind: KindRect, W: 1, H: 3, StrokeMM: 20}) },
	} {
		bad := Default()
		mod(bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: Validate = nil, want an error", name)
		}
	}
}

// TestSaveRoundTrip: what Save writes loads back the same.
func TestSaveRoundTrip(t *testing.T) {
	src := Default()
	src.Name = "stock 2026"
	src.PreviewImage = "stock 2026.jpg"
	src.Fields = append(src.Fields,
		Field{Kind: KindLine, X: 4, Y: 45, W: 92, StrokeMM: 0.2},
		Field{Name: "sat_name", X: 4, Y: 60, FontSize: 9, Align: "L", Font: "Courier", Style: "B"})
	path := filepath.Join(t.TempDir(), "cards", "stock.yaml")
	if err := src.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# qslotter card layout") || strings.Contains(string(raw), "kind: text") {
		t.Fatalf("file:\n%s", raw)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, src) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, src)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("Save left %d files behind", len(entries))
	}
}
