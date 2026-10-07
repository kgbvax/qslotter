//go:build darwin || linux

package printer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cups runs a CUPS command. It asks for the C locale, which Linux honours;
// macOS answers in the system language regardless ("System-Standardzielort:
// ..." on a German Mac), so nothing below depends on the wording.
func cups(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	return cmd
}

// errNoPrinter: no printer configured and none set as the system default.
var errNoPrinter = errors.New("no printer: none chosen (printer.name in the config file) and no system default printer")

// New returns the platform-specific Printer.
func New() Printer { return &unixPrinter{ipp: newIPPClient("http://localhost:631")} }

// JobStatus asks CUPS how the job is doing.
func (p *unixPrinter) JobStatus(ctx context.Context, job Job) (JobStatus, error) {
	return p.ipp.JobStatus(ctx, job.Printer, job.ID)
}

// CancelJob cancels the job in CUPS.
func (p *unixPrinter) CancelJob(ctx context.Context, job Job) error {
	return p.ipp.CancelJob(ctx, job.Printer, job.ID)
}

type unixPrinter struct {
	ipp *ippClient // the local CUPS scheduler
}

// List returns the printer names: lpstat -e prints one bare name per line,
// in every language.
func (p *unixPrinter) List() ([]string, error) {
	out, err := cups("lpstat", "-e").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lpstat -e: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.Fields(string(out)), nil
}

// Default returns the system default printer ("" when there is none).
func (p *unixPrinter) Default() (string, error) {
	names, err := p.List()
	if err != nil {
		return "", err
	}
	out, _ := cups("lpstat", "-d").CombinedOutput() // exits 1 without a default on some systems
	return parseDefault(string(out), names), nil
}

// parseDefault finds the default printer in lpstat -d output, whatever its
// language ("system default destination: X", "System-Standardzielort: X"):
// the known printer name after the colon. "" when there is none.
func parseDefault(out string, names []string) string {
	for _, line := range strings.Split(out, "\n") {
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		cand := strings.TrimSpace(line[i+1:])
		for _, n := range names {
			if n == cand {
				return n
			}
		}
	}
	return ""
}

func (p *unixPrinter) PrintPDF(path, printerName string, opts Options) (Job, error) {
	if printerName == "" {
		d, err := p.Default()
		if err != nil {
			return Job{}, err
		}
		if d == "" {
			return Job{}, errNoPrinter
		}
		printerName = d
	}
	out, err := run(cups("lp", lpArgs(path, printerName, opts)...))
	if err != nil {
		return Job{}, err
	}
	return Job{Printer: printerName, ID: jobIDFrom(string(out), printerName)}, nil
}

// lpArgs returns the lp arguments that print the PDF at path on printerName.
func lpArgs(path, printerName string, opts Options) []string {
	media := fmt.Sprintf("Custom.%.0fx%.0fmm", opts.PaperWMM, opts.PaperHMM)
	if opts.Paper != "" {
		media = opts.Paper // a CUPS media name, e.g. a PPD's custom size
	}
	// One PDF page is one physical card: never let a duplex printer put the
	// next card on the back of this one.
	args := []string{"-d", printerName, "-o", "media=" + media, "-o", "sides=one-sided"}
	if opts.Tray != "" {
		args = append(args, "-o", "InputSlot="+opts.Tray) // the PPD's name, e.g. Manual
	}
	if opts.Copies > 1 {
		args = append(args, "-n", fmt.Sprintf("%d", opts.Copies))
	}
	return append(args, path)
}
