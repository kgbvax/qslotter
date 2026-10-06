//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// New returns the platform-specific Printer.
func New() Printer { return &windowsPrinter{} }

type windowsPrinter struct {
	binPath string // discovered path to SumatraPDF.exe
}

func (p *windowsPrinter) findSumatra() (string, error) {
	if p.binPath != "" {
		return p.binPath, nil
	}
	// 1. Bundled copy in third_party/sumatrapdf/ next to the executable.
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "third_party", "sumatrapdf", "SumatraPDF.exe")
		if _, err := os.Stat(cand); err == nil {
			p.binPath = cand
			return cand, nil
		}
	}
	// 2. Common install locations.
	for _, dir := range []string{
		`C:\Program Files\SumatraPDF\SumatraPDF.exe`,
		`C:\Program Files (x86)\SumatraPDF\SumatraPDF.exe`,
	} {
		if _, err := os.Stat(dir); err == nil {
			p.binPath = dir
			return dir, nil
		}
	}
	// 3. PATH.
	if path, err := exec.LookPath("SumatraPDF.exe"); err == nil {
		p.binPath = path
		return path, nil
	}
	return "", fmt.Errorf("SumatraPDF not found - install it or bundle third_party/sumatrapdf/SumatraPDF.exe")
}

// defaultPrinterName resolves the system default printer via winspool
// directly. (SumatraPDF -list-printers hangs on some setups, so it is only
// used for the explicit List() debug helper, never on the print path.)
func defaultPrinterName() (string, error) {
	winspool := windows.NewLazySystemDLL("winspool.drv")
	proc := winspool.NewProc("GetDefaultPrinterW")
	var n uint32
	r1, _, err := proc.Call(0, uintptr(unsafe.Pointer(&n)))
	if n == 0 {
		if r1 == 0 {
			return "", fmt.Errorf("no default printer on this system (%v)", err)
		}
		return "", errors.New("GetDefaultPrinterW returned no buffer size")
	}
	buf := make([]uint16, n)
	r1, _, err = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r1 == 0 {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

func (p *windowsPrinter) List() ([]string, error) {
	bin, err := p.findSumatra()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(bin, "-list-printers").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sumatra -list-printers: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "Printer") {
			continue
		}
		// Lines are "printer name : <status info>"
		if i := strings.Index(s, ":"); i > 0 {
			names = append(names, strings.TrimSpace(s[:i]))
		} else {
			names = append(names, s)
		}
	}
	return names, nil
}

func (p *windowsPrinter) Default() (string, error) {
	names, err := p.List()
	if err != nil {
		return "", err
	}
	if len(names) > 0 {
		return names[0], nil
	}
	return "", nil
}

// PrintPDF prints through SumatraPDF, which returns once the job is
// spooled: Windows jobs cannot be followed (Job.ID 0).
func (p *windowsPrinter) PrintPDF(path, printerName string, opts Options) (Job, error) {
	bin, err := p.findSumatra()
	if err != nil {
		return Job{}, err
	}
	if printerName == "" {
		d, err := defaultPrinterName()
		if err != nil {
			return Job{}, fmt.Errorf("no printer configured (set printer.name in config) and no system default: %w", err)
		}
		printerName = d
	}
	args := []string{"-print-to", printerName, "-silent", "-exit-when-done",
		"-print-settings", sumatraSettings(opts), path}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Job{}, fmt.Errorf("sumatra print: timed out after 2m (printer busy or offline?)")
		}
		return Job{}, fmt.Errorf("sumatra print: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return Job{Printer: printerName}, nil
}

// sumatraSettings returns the SumatraPDF -print-settings value for opts.
func sumatraSettings(opts Options) string {
	// One PDF page is one physical card: "simplex" keeps a duplex printer
	// from putting the next card on the back of this one.
	settings := []string{"noscale", "disable-auto-rotation", "simplex"}
	if opts.PaperWMM > 0 && opts.PaperHMM > 0 {
		// SumatraPDF uses "paper=Wmm x Hmm" (spaces around x are required).
		settings = append(settings, fmt.Sprintf("paper=%.0fmm x %.0fmm", opts.PaperWMM, opts.PaperHMM))
	}
	if opts.Copies > 1 {
		settings = append(settings, fmt.Sprintf("%dx", opts.Copies))
	}
	return strings.Join(settings, ",")
}
