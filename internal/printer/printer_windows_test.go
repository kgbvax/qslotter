//go:build windows

package printer

import "testing"

// TestSumatraSettingsSimplex checks that a multi-card PDF never prints duplex.
func TestSumatraSettingsSimplex(t *testing.T) {
	got := sumatraSettings(Options{PaperWMM: 100, PaperHMM: 74, Copies: 2})
	want := "noscale,disable-auto-rotation,simplex,paper=100mm x 74mm,2x"
	if got != want {
		t.Fatalf("sumatraSettings = %q, want %q", got, want)
	}
}
