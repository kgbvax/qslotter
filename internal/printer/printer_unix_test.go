//go:build darwin || linux

package printer

import (
	"slices"
	"testing"
)

// TestLPArgsOneSided checks that a multi-card PDF never prints duplex.
func TestLPArgsOneSided(t *testing.T) {
	args := lpArgs("/tmp/card.pdf", "Card_Printer", Options{PaperWMM: 100, PaperHMM: 74, Copies: 2})
	want := []string{"-d", "Card_Printer", "-o", "media=Custom.100x74mm", "-o", "sides=one-sided",
		"-n", "2", "/tmp/card.pdf"}
	if !slices.Equal(args, want) {
		t.Fatalf("lpArgs = %q, want %q", args, want)
	}
}

func TestParseDefault(t *testing.T) {
	names := []string{"Brother_MFC_J6930DW", "Card_Printer"}
	for out, want := range map[string]string{
		"system default destination: Brother_MFC_J6930DW\n":  "Brother_MFC_J6930DW",
		"System-Standardzielort: Brother_MFC_J6930DW\n":      "Brother_MFC_J6930DW", // German macOS
		"destination par défaut du système : Card_Printer\n": "Card_Printer",
		"no system default destination\n":                    "",
		"system default destination: Gone\n":                 "", // not a printer
		"":                                                   "",
	} {
		if got := parseDefault(out, names); got != want {
			t.Errorf("parseDefault(%q) = %q, want %q", out, got, want)
		}
	}
}
