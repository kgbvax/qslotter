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
