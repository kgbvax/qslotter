// Package printer renders QSL cards to PDF and sends them to the system printer.
//
// The PDF rendering is platform-independent (go-pdf/fpdf). The actual print
// dispatch is platform-specific and selected by build tags:
//   - printer_unix.go   (//go:build darwin || linux)   -> lp / lpstat
//   - printer_windows.go (//go:build windows)          -> SumatraPDF.exe
package printer

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-pdf/fpdf"
)

// QSOFields is the data passed to the template for substitution.
type QSOFields struct {
	Call     string
	Name     string
	QSODate  string
	TimeOn   string
	Band     string
	Mode     string
	RSTSent  string
	RSTRcvd  string
	MyCall   string
	MyName   string
	QTH      string
	Freq     string
	QSLMsg   string
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

// RenderPDF renders one card to a PDF file at path using the given template
// and QSO field values. Returns the path written.
func RenderPDF(path string, tmpl *template.Template, fields QSOFields) error {
	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "mm",
		SizeStr:        "",
		Size:           fpdf.SizeType{Wd: tmpl.WidthMM, Ht: tmpl.HeightMM},
	})
	pdf.AddPage()
	pdf.SetAutoPageBreak(false, 0)
	for _, f := range tmpl.Fields {
		val := f.Text
		if val == "" {
			val = fieldFor(f.Name, fields)
		}
		fontStyle := ""
		pdf.SetFont(f.Font, fontStyle, f.FontSize)
		align := "L"
		switch strings.ToUpper(f.Align) {
		case "C":
			align = "C"
		case "R":
			align = "R"
		}
		pdf.SetXY(f.X, f.Y)
		pdf.Cell(0, 0, val)
		_ = align
	}
	return pdf.OutputFileAndClose(path)
}

func fieldFor(name string, f QSOFields) string {
	switch strings.ToLower(name) {
	case "call":
		return strings.ToUpper(f.Call)
	case "name":
		return f.Name
	case "qso_date":
		return formatDate(f.QSODate)
	case "time_on":
		return formatTime(f.TimeOn)
	case "band":
		return f.Band
	case "mode":
		return f.Mode
	case "rst_sent":
		return f.RSTSent
	case "rst_rcvd":
		return f.RSTRcvd
	case "my_call":
		return strings.ToUpper(f.MyCall)
	case "my_name":
		return f.MyName
	case "qth":
		return f.QTH
	case "freq":
		return f.Freq
	case "qslmsg":
		return f.QSLMsg
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

// TempPDFPath returns a temp path for the next PDF.
func TempPDFPath() string {
	return os.TempDir() + fmt.Sprintf("/qslotter_%s.pdf", time.Now().UTC().Format("20060102_150405"))
}

// run is a small helper to execute a command and capture its output.
func run(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w (%s)", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}