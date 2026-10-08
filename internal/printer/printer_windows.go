//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"
	"log"
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
// directly (SumatraPDF -list-printers hangs on some setups, so it is not
// used at all).
func defaultPrinterName() (string, error) {
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

// List enumerates the local and connected printers via winspool
// (EnumPrintersW, level 4: names only, no driver queries, so a printer that
// is offline does not hold it up).
func (p *windowsPrinter) List() ([]string, error) {
	const flags = 0x2 | 0x4 // PRINTER_ENUM_LOCAL | PRINTER_ENUM_CONNECTIONS
	type printerInfo4 struct {
		PrinterName *uint16
		ServerName  *uint16
		Attributes  uint32
	}
	var needed, returned uint32
	procEnumPrintersW.Call(flags, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil, nil
	}
	buf := make([]byte, needed)
	r1, _, err := procEnumPrintersW.Call(flags, 0, 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r1 == 0 {
		return nil, fmt.Errorf("EnumPrintersW: %w", err)
	}
	infos := unsafe.Slice((*printerInfo4)(unsafe.Pointer(&buf[0])), returned)
	names := make([]string, 0, returned)
	for _, in := range infos {
		names = append(names, windows.UTF16PtrToString(in.PrinterName))
	}
	return names, nil
}

func (p *windowsPrinter) Default() (string, error) { return defaultPrinterName() }

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
	paper, tray, err := p.pickMedia(printerName, opts)
	if err != nil {
		return Job{}, err
	}
	args := []string{"-print-to", printerName, "-silent", "-exit-when-done",
		"-print-settings", sumatraSettings(opts, paper, tray, landscapeDefault(printerName)), path}
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

// pickMedia is the paper and tray a card prints on (nil: left to
// SumatraPDF and the driver). A paper or tray named in the config that the
// printer does not have is an error - printing on whatever the driver
// picks instead is how cards end up fed as A4.
func (p *windowsPrinter) pickMedia(printerName string, opts Options) (paper, tray *Media, err error) {
	papers, trays, err := p.Media(printerName)
	if err != nil {
		if opts.Paper != "" || opts.Tray != "" {
			return nil, nil, fmt.Errorf("reading the papers and trays of %s: %w", printerName, err)
		}
		log.Printf("print: reading the papers of %s: %v", printerName, err)
		return nil, nil, nil
	}
	if m, ok := PickPaper(papers, opts.Paper, opts.PaperWMM, opts.PaperHMM); ok {
		paper = &m
	} else if opts.Paper != "" {
		return nil, nil, fmt.Errorf("printer %s has no paper %q (it has: %s)", printerName, opts.Paper, MediaNames(papers))
	} else if opts.PaperWMM > 0 && opts.PaperHMM > 0 {
		log.Printf("print: %s has no %gx%g mm paper, the driver may feed its default (A4) instead - "+
			"add a form of that size (Print server properties > Forms) and choose it under Settings > Card layout", printerName, opts.PaperWMM, opts.PaperHMM)
	}
	if opts.Tray != "" {
		m, ok := PickTray(trays, opts.Tray)
		if !ok {
			return nil, nil, fmt.Errorf("printer %s has no tray %q (it has: %s)", printerName, opts.Tray, MediaNames(trays))
		}
		tray = &m
	}
	return paper, tray, nil
}

// sumatraSettings returns the SumatraPDF -print-settings value for opts on
// paper and tray (nil: not set). landscape: the driver's default
// orientation is landscape (it turns the paper).
//
// SumatraPDF 3.5 takes "paper=" only as a name from the driver's list (no
// "140mm x 90mm": that is silently dropped and the driver falls back to
// its default, A4), so the paper and tray go by the driver's number
// (paperkind=, bin=), which also keeps a comma in a name from splitting
// the settings.
func sumatraSettings(opts Options, paper, tray *Media, landscape bool) string {
	// One PDF page is one physical card: "simplex" keeps a duplex printer
	// from putting the next card on the back of this one.
	settings := []string{"noscale", "simplex"}
	// With auto-rotation off, "portrait" prints the page as it is and
	// "landscape" turns it by 90 degrees; with it on, "portrait" turns a
	// wide page by 270 degrees to stand on the paper.
	switch {
	case opts.Rotate == 90:
		settings = append(settings, "disable-auto-rotation", "landscape")
	case opts.Rotate == 270:
		settings = append(settings, "portrait")
	default:
		settings = append(settings, "disable-auto-rotation")
		if paper != nil {
			// Turn the card when it lies across the paper as the driver feeds it.
			if Crosswise(*paper, opts.PaperWMM, opts.PaperHMM) != landscape {
				settings = append(settings, "landscape")
			} else {
				settings = append(settings, "portrait")
			}
		}
	}
	if paper != nil {
		settings = append(settings, fmt.Sprintf("paperkind=%d", paper.ID))
	}
	if tray != nil {
		settings = append(settings, fmt.Sprintf("bin=%d", tray.ID))
	}
	if opts.Copies > 1 {
		settings = append(settings, fmt.Sprintf("%dx", opts.Copies))
	}
	return strings.Join(settings, ",")
}

var (
	winspool                = windows.NewLazySystemDLL("winspool.drv")
	procDeviceCapabilitiesW = winspool.NewProc("DeviceCapabilitiesW")
	procEnumPrintersW       = winspool.NewProc("EnumPrintersW")
	procOpenPrinterW        = winspool.NewProc("OpenPrinterW")
	procClosePrinter        = winspool.NewProc("ClosePrinter")
	procDocumentPropertiesW = winspool.NewProc("DocumentPropertiesW")
)

// DeviceCapabilities values (wingdi.h).
const (
	dcPapers     = 2
	dcPaperSize  = 3
	dcBins       = 6
	dcBinNames   = 12
	dcPaperNames = 16

	paperNameLen = 64 // WCHARs per DC_PAPERNAMES entry
	binNameLen   = 24 // WCHARs per DC_BINNAMES entry
)

// deviceCaps calls DeviceCapabilitiesW; out nil asks for the count.
func deviceCaps(device *uint16, capability int, out unsafe.Pointer) (int, error) {
	r, _, err := procDeviceCapabilitiesW.Call(uintptr(unsafe.Pointer(device)), 0, uintptr(capability), uintptr(out), 0)
	n := int(int32(r))
	if n < 0 {
		return 0, fmt.Errorf("DeviceCapabilities(%d): %v", capability, err)
	}
	return n, nil
}

// Media lists the papers (with their size) and trays printerName's driver
// offers, as its print dialog does.
func (p *windowsPrinter) Media(printerName string) (papers, trays []Media, err error) {
	if printerName == "" {
		if printerName, err = defaultPrinterName(); err != nil {
			return nil, nil, err
		}
	}
	dev, err := windows.UTF16PtrFromString(printerName)
	if err != nil {
		return nil, nil, err
	}
	n, err := deviceCaps(dev, dcPapers, nil)
	if err != nil {
		return nil, nil, err
	}
	if n > 0 {
		ids := make([]uint16, n)
		names := make([]uint16, n*paperNameLen)
		sizes := make([]int32, 2*n) // POINT: tenths of a millimetre
		for _, c := range []struct {
			capability int
			out        unsafe.Pointer
		}{{dcPapers, unsafe.Pointer(&ids[0])}, {dcPaperNames, unsafe.Pointer(&names[0])}, {dcPaperSize, unsafe.Pointer(&sizes[0])}} {
			if got, err := deviceCaps(dev, c.capability, c.out); err != nil {
				return nil, nil, err
			} else if got != n {
				return nil, nil, fmt.Errorf("DeviceCapabilities(%d): %d entries, %d papers", c.capability, got, n)
			}
		}
		for i := range n {
			papers = append(papers, Media{Name: windows.UTF16ToString(names[i*paperNameLen : (i+1)*paperNameLen]),
				ID: int(ids[i]), WMM: float64(sizes[2*i]) / 10, HMM: float64(sizes[2*i+1]) / 10})
		}
	}
	if n, err = deviceCaps(dev, dcBins, nil); err != nil {
		return nil, nil, err
	}
	if n > 0 {
		ids := make([]uint16, n)
		names := make([]uint16, n*binNameLen)
		if _, err := deviceCaps(dev, dcBins, unsafe.Pointer(&ids[0])); err != nil {
			return nil, nil, err
		}
		if _, err := deviceCaps(dev, dcBinNames, unsafe.Pointer(&names[0])); err != nil {
			return nil, nil, err
		}
		for i := range n {
			trays = append(trays, Media{Name: windows.UTF16ToString(names[i*binNameLen : (i+1)*binNameLen]), ID: int(ids[i])})
		}
	}
	return papers, trays, nil
}

// DEVMODEW: dmFields at byte 72, dmOrientation at 76.
const (
	devModeFields      = 72
	devModeOrientation = 76
	dmOrientationField = 0x1
	dmOrientLandscape  = 2
	dmOutBuffer        = 2
)

// landscapeDefault reports whether printerName's default settings are
// landscape (the driver turns the paper; SumatraPDF goes by it). False
// when that cannot be read.
func landscapeDefault(printerName string) bool {
	name, err := windows.UTF16PtrFromString(printerName)
	if err != nil {
		return false
	}
	var h windows.Handle
	if r, _, _ := procOpenPrinterW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0); r == 0 {
		return false
	}
	defer procClosePrinter.Call(uintptr(h))
	size, _, _ := procDocumentPropertiesW.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)), 0, 0, 0)
	if int32(size) < devModeOrientation+2 {
		return false
	}
	buf := make([]byte, int32(size))
	if r, _, _ := procDocumentPropertiesW.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&buf[0])), 0, dmOutBuffer); int32(r) < 0 {
		return false
	}
	fields := *(*uint32)(unsafe.Pointer(&buf[devModeFields]))
	orient := *(*int16)(unsafe.Pointer(&buf[devModeOrientation]))
	return fields&dmOrientationField != 0 && orient == dmOrientLandscape
}
