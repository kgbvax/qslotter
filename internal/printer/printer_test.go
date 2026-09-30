package printer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dl9et/qslotter/internal/template"
)

func TestRenderPDF(t *testing.T) {
	tmpl := template.Default()
	out := filepath.Join(t.TempDir(), "card.pdf")
	fields := QSOFields{
		Call: "DL1ABC", Name: "Hans", QSODate: "20240101", TimeOn: "120000",
		Band: "20m", Mode: "SSB", RSTSent: "59", MyCall: "DL9ET",
	}
	if err := RenderPDF(out, tmpl, fields); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 500 {
		t.Fatalf("PDF is %d bytes, too small", info.Size())
	}
	// Sanity: %PDF- header
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr := make([]byte, 5)
	if _, err := f.Read(hdr); err != nil {
		t.Fatal(err)
	}
	if string(hdr) != "%PDF-" {
		t.Fatalf("PDF header = %q, want %%PDF-", string(hdr))
	}
}