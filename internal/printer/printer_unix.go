//go:build darwin || linux

package printer

import (
	"fmt"
	"os/exec"
	"strings"
)

// New returns the platform-specific Printer.
func New() Printer { return &unixPrinter{} }

type unixPrinter struct{}

func (p *unixPrinter) List() ([]string, error) {
	out, err := exec.Command("lpstat", "-p").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lpstat: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		// Format: "printer <name> is idle. ..."
		if strings.HasPrefix(line, "printer ") {
			rest := strings.TrimPrefix(line, "printer ")
			name := strings.Fields(rest)
			if len(name) > 0 {
				names = append(names, name[0])
			}
		}
	}
	return names, nil
}

func (p *unixPrinter) Default() (string, error) {
	out, err := exec.Command("lpstat", "-d").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lpstat -d: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "system default destination:") {
			f := strings.Fields(line)
			if len(f) > 0 {
				return f[len(f)-1], nil
			}
		}
	}
	return "", nil
}

func (p *unixPrinter) PrintPDF(path, printerName string, opts Options) error {
	if printerName == "" {
		d, err := p.Default()
		if err != nil {
			return err
		}
		printerName = d
	}
	_, err := run(exec.Command("lp", lpArgs(path, printerName, opts)...))
	return err
}

// lpArgs returns the lp arguments that print the PDF at path on printerName.
func lpArgs(path, printerName string, opts Options) []string {
	media := fmt.Sprintf("Custom.%.0fx%.0fmm", opts.PaperWMM, opts.PaperHMM)
	// One PDF page is one physical card: never let a duplex printer put the
	// next card on the back of this one.
	args := []string{"-d", printerName, "-o", "media=" + media, "-o", "sides=one-sided"}
	if opts.Copies > 1 {
		args = append(args, "-n", fmt.Sprintf("%d", opts.Copies))
	}
	return append(args, path)
}
