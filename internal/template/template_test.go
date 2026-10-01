package template

import (
	"os"
	"path/filepath"
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
	if got := Default().MaxRows(); got != 3 {
		t.Errorf("Default: MaxRows = %d, want 3", got)
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
	if d.WidthMM != 100 || d.HeightMM != 74 {
		t.Fatalf("size = %vx%v, want 100x74", d.WidthMM, d.HeightMM)
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
