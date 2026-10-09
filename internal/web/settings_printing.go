package web

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/printer"
	cardtmpl "github.com/dl9et/qslotter/internal/template"
)

// layoutThumb is one layout on the Cards & printer tab: a small picture of
// the card as it prints (the short sample over the card scan).
type layoutThumb struct {
	LayoutEntry
	SVG  template.HTML // "" when the layout does not load
	Size string        // "140 × 90 mm"
	Err  string
}

// layoutThumbs draws every layout of list, the active one first.
func (s *Server) layoutThumbs(list []LayoutEntry) []layoutThumb {
	var out []layoutThumb
	for _, e := range list {
		t := layoutThumb{LayoutEntry: e}
		tmpl, path, err := s.loadLayout(e.ID)
		if err != nil {
			t.Err = err.Error()
		} else {
			t.Size = fmtMM(tmpl.WidthMM) + " × " + fmtMM(tmpl.HeightMM) + " mm"
			img := ""
			if p := layoutImagePath(tmpl, path); p != "" {
				if _, err := os.Stat(p); err == nil {
					img = "/settings/cards/image?name=" + url.QueryEscape(e.ID)
				}
			}
			t.SVG = s.cardSVG(tmpl, img)
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(a, b layoutThumb) int {
		switch {
		case a.Active == b.Active:
			return 0
		case a.Active:
			return -1
		}
		return 1
	})
	return out
}

// svgFonts maps the PDF core fonts to what the browser draws them with (as
// in layout.js).
var svgFonts = map[string]string{
	"Helvetica": "Helvetica, Arial, sans-serif",
	"Times":     "'Times New Roman', Times, serif",
	"Courier":   "'Courier New', Courier, monospace",
}

// cardSVG draws tmpl with the short sample card as an SVG in millimetres,
// over the card scan img when there is one. It is only a picture: the
// editor is where the layout changes.
func (s *Server) cardSVG(tmpl *cardtmpl.Template, img string) template.HTML {
	ops, _, err := printer.Preview(tmpl, s.sampleCard("short", tmpl))
	if err != nil {
		return ""
	}
	n := func(v float64) string { return fmtMM(math.Round(v*100) / 100) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="cardsvg" viewBox="0 0 %s %s" role="img" aria-hidden="true">`, n(tmpl.WidthMM), n(tmpl.HeightMM))
	fmt.Fprintf(&b, `<rect width="%s" height="%s" fill="#fff"/>`, n(tmpl.WidthMM), n(tmpl.HeightMM))
	if img != "" {
		fmt.Fprintf(&b, `<image href="%s" width="%s" height="%s" preserveAspectRatio="none"/>`, html.EscapeString(img), n(tmpl.WidthMM), n(tmpl.HeightMM))
	}
	for _, op := range ops {
		switch op.Kind {
		case "line":
			fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="#1D2733" stroke-width="%s"/>`,
				n(op.X), n(op.Y), n(op.X+op.W), n(op.Y+op.H), n(max(op.Stroke, 0.2)))
		case "rect":
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="none" stroke="#1D2733" stroke-width="%s"/>`,
				n(op.X), n(op.Y), n(op.W), n(op.H), n(max(op.Stroke, 0.2)))
		default:
			if op.Text == "" {
				continue
			}
			weight := ""
			if op.Style == "B" {
				weight = ` font-weight="bold"`
			}
			fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="%s" font-family="%s"%s textLength="%s" lengthAdjust="spacingAndGlyphs">%s</text>`,
				n(op.X), n(op.Baseline), n(op.H), html.EscapeString(cmpOr(svgFonts[op.Font], svgFonts["Helvetica"])), weight, n(op.W), html.EscapeString(op.Text))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) // every value above is a number or escaped
}

// printerChoice is one entry of the printer select.
type printerChoice struct {
	Name     string
	Selected bool
}

// printerChoices lists the printers to choose from, the configured one
// selected (kept in the list when the system does not know it).
func (s *Server) printerChoices(cfg *config.Config) ([]printerChoice, string) {
	ch := make(chan []string, 1)
	errc := make(chan error, 1)
	go func() {
		names, err := s.printer.List()
		if err != nil {
			errc <- err
			return
		}
		ch <- names
	}()
	var names []string
	select {
	case names = <-ch:
	case err := <-errc:
		return s.keepConfigured(nil, cfg), err.Error()
	case <-time.After(mediaTimeout):
		return s.keepConfigured(nil, cfg), "the print system did not answer"
	}
	return s.keepConfigured(names, cfg), ""
}

func (s *Server) keepConfigured(names []string, cfg *config.Config) []printerChoice {
	var out []printerChoice
	for _, n := range names {
		out = append(out, printerChoice{n, n == cfg.Printer.Name})
	}
	if cfg.Printer.Name != "" && !slices.Contains(names, cfg.Printer.Name) {
		out = append(out, printerChoice{cfg.Printer.Name, true})
	}
	return out
}

// postSettingsPrinter stores the printer (name=; empty = the system's
// default) as printer.name. Another printer has other papers and trays, so
// a change resets printer.paper and printer.tray to automatic.
func (s *Server) postSettingsPrinter(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	vals := map[string]any{"printer.name": name}
	if name != s.config().Printer.Name {
		vals["printer.paper"], vals["printer.tray"] = "", ""
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	if err := updateConfigFile(s.cfgPath, vals); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
		return
	}
	if _, err := s.reloadConfig(); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "config saved, but reloading it failed: %s", err.Error())
		return
	}
	log.Printf("settings: printer.name = %q", name)
	http.Redirect(w, r, settingsTabPath(tabPrinting)+"?saved=1#printer", http.StatusSeeOther)
}

// postSettingsTestCard prints the active layout with the short sample, the
// mm ruler and a label, shifted by x= / y= (default: the saved offset) - the
// card to measure the printer offset from.
func (s *Server) postSettingsTestCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	x, y, ok := s.offsets(w, r, "x", "y")
	if !ok {
		return
	}
	tmpl, err := s.cardTemplate()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "loading the card layout: %s", err.Error())
		return
	}
	_, active, _ := s.layouts(r)
	name := active
	switch active {
	case builtinLayout:
		name = s.tr(r, "Built-in layout")
	case configuredLayout:
		name = cmpOr(tmpl.Name, "layout")
	}
	s.printTestCard(w, r, tmpl, name, "short", x, y)
}

// printTestCard renders tmpl with the sample, the ruler and a label, shifted
// by x, y, and sends it to the configured printer.
func (s *Server) printTestCard(w http.ResponseWriter, r *http.Request, tmpl *cardtmpl.Template, name, sample string, x, y float64) {
	cfg := s.config()
	pdfPath := printer.TempPDFPath()
	defer func() {
		if err := os.Remove(pdfPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("test card: removing %s: %v", pdfPath, err)
		}
	}()
	card := s.sampleCard(sample, tmpl)
	card.Rows = card.Rows[:min(len(card.Rows), printer.MaxRows(tmpl))] // one card
	if err := printer.Render(pdfPath, tmpl, card, printer.RenderOptions{
		OffsetXMM: x, OffsetYMM: y, Ruler: true, Label: s.testCardLabel(r, name, x, y),
	}); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "rendering the test card: %s", err.Error())
		return
	}
	if _, err := s.printer.PrintPDF(pdfPath, cfg.Printer.Name, printOptions(cfg)); err != nil {
		s.fail(w, r, http.StatusBadGateway, "printing the test card: %s", err.Error())
		return
	}
	s.notice(w, r, "Test card sent to the printer.")
	w.WriteHeader(http.StatusNoContent)
}
