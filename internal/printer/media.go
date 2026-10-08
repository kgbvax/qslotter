package printer

import (
	"math"
	"strconv"
	"strings"
)

// Media is a paper (form) or a tray (paper source) as a printer's driver
// names it. ID is the driver's number for it (DMPAPER_* / DMBIN_*); a
// paper has its size, a tray none.
type Media struct {
	Name     string
	ID       int
	WMM, HMM float64
}

// MediaLister lists the papers and trays a printer's driver offers
// (Windows). printerName "" = the system default.
type MediaLister interface {
	Media(printerName string) (papers, trays []Media, err error)
}

// paperToleranceMM is how far a paper may differ from the card per side
// and still be its paper (SumatraPDF matches within 1 mm too).
const paperToleranceMM = 1.0

// PickPaper is the paper to print a w x h mm card on: the one named want
// (its name, case-insensitive, or its number), else for want "" the one
// of the card's size in either orientation - the first, drivers list the
// standard size before a copy defined as a form. ok is false when there
// is none.
func PickPaper(papers []Media, want string, w, h float64) (Media, bool) {
	if want = strings.TrimSpace(want); want != "" {
		return pickByName(papers, want)
	}
	for _, p := range papers {
		if sameSize(p.WMM, p.HMM, w, h) || sameSize(p.HMM, p.WMM, w, h) {
			return p, true
		}
	}
	return Media{}, false
}

// PickTray is the tray named want (its name, case-insensitive, or its
// number).
func PickTray(trays []Media, want string) (Media, bool) {
	return pickByName(trays, strings.TrimSpace(want))
}

func pickByName(list []Media, want string) (Media, bool) {
	if want == "" {
		return Media{}, false
	}
	for _, m := range list {
		if strings.EqualFold(strings.TrimSpace(m.Name), want) {
			return m, true
		}
	}
	if id, err := strconv.Atoi(want); err == nil {
		for _, m := range list {
			if m.ID == id {
				return m, true
			}
		}
	}
	return Media{}, false
}

func sameSize(w1, h1, w2, h2 float64) bool {
	return math.Abs(w1-w2) <= paperToleranceMM && math.Abs(h1-h2) <= paperToleranceMM
}

// Crosswise reports whether a w x h card lies across paper p (one wide,
// the other tall): it has to be turned to fit.
func Crosswise(p Media, w, h float64) bool {
	if p.WMM == p.HMM || w == h || p.WMM <= 0 || w <= 0 {
		return false
	}
	return (p.WMM > p.HMM) != (w > h)
}

// MediaNames lists the names of list, for a message.
func MediaNames(list []Media) string {
	names := make([]string, len(list))
	for i, m := range list {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}
