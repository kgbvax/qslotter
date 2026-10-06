package web

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/store"
)

// The print queue (the Desk's third view, /work/printq; docs/STATES.md): cards sent to printing
// wait there (toprint) with their route and note; "Print" sends all of them
// as one job and opens the print run (printing) - or, for stations whose
// cards a QSL print service produces (e.g. DARC), "Export ADIF" writes them
// into one ADIF file for the service instead. The run is confirmed as a
// whole - only then are the cards sent -, or single cards are printed or
// exported again, or taken back to the print queue first. One run is open
// at a time.

// PrintCard is one card in the print queue or the open print run: the QSOs
// with one callsign sent to printing together (same route and note).
type PrintCard struct {
	Lead    string // first key: checkbox value, element id
	Call    string
	Name    string
	Keys    []string
	QSOs    []*store.QSO // oldest first
	Route   string       // B, D, MD, MB
	Manager string
	Note    string
	Prints  int // physical cards (QSO rows per card from the template)
}

// printMu serialises print jobs: starting a run, printing it and recording a
// failure, or a reprint, must not interleave with another window's.
var printMu sync.Mutex

// printCards lists the cards in one print status (toprint or printing) in
// print order: direct, via manager, bureau (by call, for the parcel).
func (s *Server) printCards(status string) ([]*PrintCard, error) {
	items, err := s.store.QueueList(status)
	if err != nil {
		return nil, err
	}
	perCard := 1
	if t, err := s.cardTemplate(); err == nil {
		perCard = printer.MaxRows(t)
	}
	var cards []*PrintCard
	byCard := map[string]*PrintCard{}
	for _, it := range items {
		q, err := s.store.GetQSO(it.QSLKey)
		if err != nil {
			return nil, err
		}
		if q == nil {
			continue
		}
		code := routeCode(it.DesiredMethod, it.SendVia)
		id := strings.ToUpper(q.Call) + "|" + code + "|" + it.Manager + "|" + it.CardNote
		c := byCard[id]
		if c == nil {
			c = &PrintCard{Call: strings.ToUpper(q.Call), Route: code, Manager: it.Manager, Note: it.CardNote}
			byCard[id] = c
			cards = append(cards, c)
		}
		c.QSOs = append(c.QSOs, q)
	}
	for _, c := range cards {
		sort.SliceStable(c.QSOs, func(i, j int) bool {
			if c.QSOs[i].QSODate != c.QSOs[j].QSODate {
				return c.QSOs[i].QSODate < c.QSOs[j].QSODate
			}
			return c.QSOs[i].TimeOn < c.QSOs[j].TimeOn
		})
		for _, q := range c.QSOs {
			c.Keys = append(c.Keys, q.QSLKey)
			c.Name = cmpOr(q.Name, c.Name) // newest non-empty wins, as on the Desk
		}
		c.Lead = c.Keys[0]
		c.Prints = (len(c.QSOs) + perCard - 1) / perCard
	}
	rank := map[string]int{"D": 0, "M": 1, "B": 2}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := rank[routeGroup(cards[i].Route)], rank[routeGroup(cards[j].Route)]
		if a != b {
			return a < b
		}
		return cards[i].Call < cards[j].Call
	})
	return cards, nil
}

// cardFields is what is printed on one card.
func (s *Server) cardFields(c *PrintCard) printer.CardFields {
	cfg := s.config()
	card := printer.CardFields{Call: c.QSOs[0].Call, MyCall: cfg.Clublog.Call, MyName: cfg.Station.Name, QSLMsg: c.Note}
	if strings.HasPrefix(c.Route, "M") {
		card.Via = c.Manager
	}
	for _, q := range c.QSOs {
		card.Name = cmpOr(q.Name, card.Name)
		card.QTH = cmpOr(q.QTH, card.QTH)
		card.Rows = append(card.Rows, printer.QSORow{QSODate: q.QSODate, TimeOn: q.TimeOn, Band: q.Band,
			Mode: q.Mode, RSTSent: q.RSTSent, RSTRcvd: q.RSTRcvd, Freq: q.Freq})
	}
	return card
}

// printJob renders the cards into one PDF and sends it to the printer as one
// job. Callers hold printMu.
func (s *Server) printJob(cards []*PrintCard) error {
	tmpl, err := s.cardTemplate()
	if err != nil {
		return err
	}
	var fields []printer.CardFields
	for _, c := range cards {
		fields = append(fields, s.cardFields(c))
	}
	pdfPath := printer.TempPDFPath()
	defer func() {
		if err := os.Remove(pdfPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("print: removing %s: %v", pdfPath, err)
		}
	}()
	cfg := s.config()
	if err := printer.RenderCards(pdfPath, tmpl, fields, printer.RenderOptions{
		OffsetXMM: cfg.Printer.OffsetMM[0], OffsetYMM: cfg.Printer.OffsetMM[1],
	}); err != nil {
		return err
	}
	// The job is not followed (printer.Watcher): the run is checked by the
	// operator and confirmed as a whole.
	_, err = s.printer.PrintPDF(pdfPath, cfg.Printer.Name, printer.Options{
		PaperWMM: cfg.Printer.PaperSizeMM[0],
		PaperHMM: cfg.Printer.PaperSizeMM[1],
		Copies:   1,
	})
	return err
}

// templateHasNote reports whether the card template prints the card note.
func (s *Server) templateHasNote() bool {
	t, err := s.cardTemplate()
	return err != nil || t.HasField("qslmsg") // a broken template says so when printing
}

// printQueueData is the print queue section: the cards waiting and the open
// run.
func (s *Server) printQueueData() (map[string]any, error) {
	queue, err := s.printCards("toprint")
	if err != nil {
		return nil, err
	}
	run, err := s.printCards("printing")
	if err != nil {
		return nil, err
	}
	n := 0
	for _, c := range queue {
		n += c.Prints
	}
	export, _ := s.store.MetaGet(metaRunExport)
	return map[string]any{"Queue": queue, "Run": run, "QueuePrints": n, "NoteField": s.templateHasNote(),
		"Export": export, "ExportPath": filepath.Join(s.exportDir(), export),
		"PQ": PrintBadge{Queued: len(queue), RunOpen: len(run) > 0, On: true}}, nil
}

// PrintBadge is the "Print queue" view button in the Desk band: how many
// cards wait, whether a run waits for its check, and whether it is the page.
type PrintBadge struct {
	Queued  int
	RunOpen bool
	On      bool
}

// printBadge counts for the band of the Desk pages (list, card by card).
func (s *Server) printBadge() PrintBadge {
	q, _ := s.store.QueueList("toprint")
	run, _ := s.store.QueueList("printing")
	return PrintBadge{Queued: len(q), RunOpen: len(run) > 0}
}

func (s *Server) renderPrintQueue(w http.ResponseWriter, r *http.Request) {
	data, err := s.printQueueData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "print_queue", data)
}

// pagePrintQueue is the Desk's Print queue view: the full page, or - for
// htmx and live.js - the section alone; ?badge=1 answers the band's button
// (its count follows every move on the other Desk pages).
func (s *Server) pagePrintQueue(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("badge") == "1" {
		s.render(w, r, "printq_badge", s.printBadge())
		return
	}
	if isFragmentRequest(r) {
		s.renderPrintQueue(w, r)
		return
	}
	data, err := s.printQueueData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "printq.html", data)
}

// runKeys returns the QSOs of the ticked cards of the run: each checkbox
// carries a card's lead key, its QSOs come as card:<lead> fields.
func runKeys(r *http.Request) []string {
	_ = r.ParseForm()
	var keys []string
	seen := map[string]bool{}
	for _, lead := range r.Form["lead"] {
		ks := r.Form["card:"+lead]
		if len(ks) == 0 {
			ks = []string{lead}
		}
		for _, k := range ks {
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

func (s *Server) printErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrRunOpen):
		s.fail(w, r, http.StatusConflict, "The last print run is not confirmed yet - check the printed cards first.")
	case errors.Is(err, store.ErrNothingToPrint):
		s.fail(w, r, http.StatusConflict, "Nothing waits to be printed.")
	default:
		s.queueErr(w, r, err)
	}
}

// htmxPrintRun prints every card of the print queue as one job and opens the
// run. A job that fails puts the cards back into the print queue.
func (s *Server) htmxPrintRun(w http.ResponseWriter, r *http.Request) {
	s.startRun(w, r, func(cards []*PrintCard) error {
		if err := s.printJob(cards); err != nil {
			return err
		}
		return s.store.MetaSet(metaRunExport, "")
	})
}

// htmxExportRun writes every card of the print queue into one ADIF file for
// a QSL print service and opens the run, like printing.
func (s *Server) htmxExportRun(w http.ResponseWriter, r *http.Request) {
	s.startRun(w, r, func(cards []*PrintCard) error {
		name, err := s.exportADIF(cards)
		if err != nil {
			return err
		}
		s.notice(w, r, "ADIF file for the print service written: %s", filepath.Join(s.exportDir(), name))
		return s.store.MetaSet(metaRunExport, name)
	})
}

// startRun opens the run with every card of the print queue and produces it
// with out (print job or ADIF file); when that fails the run goes back.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request, out func([]*PrintCard) error) {
	printMu.Lock()
	defer printMu.Unlock()
	keys, err := s.store.QueueStartRun()
	if err != nil {
		s.printErr(w, r, err)
		return
	}
	cards, err := s.printCards("printing")
	if err == nil {
		err = out(cards)
	}
	if err != nil {
		if e := s.store.QueueRunFailed(keys...); e != nil {
			log.Printf("print: putting the failed run back: %v", e)
		}
		w.Header().Del("HX-Trigger")
		s.fail(w, r, http.StatusBadGateway, "Printing failed (%s) - the cards wait in the print queue.", err.Error())
		return
	}
	for _, k := range keys {
		s.publishQueueChanged(k, "printing")
	}
	s.renderPrintQueue(w, r)
}

// htmxPrintConfirm confirms the open run: every printed card is sent.
func (s *Server) htmxPrintConfirm(w http.ResponseWriter, r *http.Request) {
	printMu.Lock()
	keys, err := s.store.QueueConfirmRun()
	if err == nil {
		_ = s.store.MetaSet(metaRunExport, "")
	}
	printMu.Unlock()
	if err != nil {
		s.printErr(w, r, err)
		return
	}
	for _, k := range keys {
		s.publishQueueChanged(k, "sent")
	}
	s.renderPrintQueue(w, r)
}

// htmxReprint prints the ticked cards of the open run again (one job), or
// with how=export writes them into a new ADIF file; they stay in the run.
func (s *Server) htmxReprint(w http.ResponseWriter, r *http.Request) {
	keys := runKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Tick the cards to print again.")
		return
	}
	printMu.Lock()
	defer printMu.Unlock()
	want := map[string]bool{}
	for _, k := range keys {
		it, err := s.store.QueueGet(k)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if it == nil || it.Status != "printing" {
			s.queueErr(w, r, store.ErrConflict)
			return
		}
		want[k] = true
	}
	run, err := s.printCards("printing")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var cards []*PrintCard
	for _, c := range run {
		for _, k := range c.Keys {
			if want[k] {
				cards = append(cards, c)
				break
			}
		}
	}
	if r.FormValue("how") == "export" {
		name, err := s.exportADIF(cards)
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "Printing failed (%s).", err.Error())
			return
		}
		_ = s.store.MetaSet(metaRunExport, name)
		s.notice(w, r, "ADIF file for the print service written: %s", filepath.Join(s.exportDir(), name))
	} else if err := s.printJob(cards); err != nil {
		s.fail(w, r, http.StatusBadGateway, "Printing failed (%s).", err.Error())
		return
	}
	if err := s.store.QueueReprint(keys...); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.renderPrintQueue(w, r)
}

// htmxPrintBack takes the ticked cards of the open run back to the print
// queue (a misprint to correct at the Desk, or nothing came out).
func (s *Server) htmxPrintBack(w http.ResponseWriter, r *http.Request) {
	keys := runKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Tick the cards to take back.")
		return
	}
	printMu.Lock()
	err := s.store.QueueRunBack(keys...)
	printMu.Unlock()
	if err != nil {
		s.queueErr(w, r, err)
		return
	}
	for _, k := range keys {
		s.publishQueueChanged(k, "toprint")
	}
	s.renderPrintQueue(w, r)
}

// htmxUnprint takes a card out of the print queue back to the Desk, where
// its route and note can be changed.
func (s *Server) htmxUnprint(w http.ResponseWriter, r *http.Request) {
	keys := deskKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, errNoKeys.Error())
		return
	}
	printMu.Lock()
	err := s.store.QueueUnprint(keys...)
	printMu.Unlock()
	if err != nil {
		s.queueErr(w, r, err)
		return
	}
	for _, k := range keys {
		s.publishQueueChanged(k, "decided")
	}
	s.renderPrintQueue(w, r)
}

// metaRunExport names the ADIF file of the open run ("" = the run was
// printed).
const metaRunExport = "print_run_export"

// exportDir is where ADIF files for the print service go: card.export_dir,
// else "exports" next to the database.
func (s *Server) exportDir() string {
	cfg := s.config()
	if cfg.Card.ExportDir != "" {
		return cfg.Card.ExportDir
	}
	return filepath.Join(filepath.Dir(cfg.Store.Path), "exports")
}

// exportADIF writes the cards into a new ADIF file in the export directory,
// one record per QSO in print order, with what a print service needs for
// the card: the QSO, the note (QSLMSG), the route (QSL_SENT_VIA, QSL_VIA)
// and the own call. Returns the file name.
func (s *Server) exportADIF(cards []*PrintCard) (string, error) {
	dir := s.exportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	now := s.now().UTC()
	name := "qsl-" + now.Format("20060102-150405") + ".adi"
	for i := 2; ; i++ { // two exports within a second get their own files
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("qsl-%s-%d.adi", now.Format("20060102-150405"), i)
	}
	var b strings.Builder
	aw := adif.NewWriter(&b)
	n := 0
	for _, c := range cards {
		n += len(c.QSOs)
	}
	if err := aw.WriteHeader(fmt.Sprintf("qslotter: %d QSL card(s), %d QSO(s) for the QSL print service", len(cards), n),
		adif.Field{Name: "ADIF_VER", Value: "3.1.4"},
		adif.Field{Name: "PROGRAMID", Value: "qslotter"},
		adif.Field{Name: "CREATED_TIMESTAMP", Value: now.Format("20060102 150405")}); err != nil {
		return "", err
	}
	myCall := strings.ToUpper(s.config().Clublog.Call)
	for _, c := range cards {
		via, mgr := c.Route, ""
		if strings.HasPrefix(c.Route, "M") {
			via, mgr = c.Route[1:], c.Manager
		}
		for _, q := range c.QSOs {
			if err := aw.WriteFields(
				adif.Field{Name: "CALL", Value: q.Call},
				adif.Field{Name: "QSO_DATE", Value: q.QSODate},
				adif.Field{Name: "TIME_ON", Value: q.TimeOn},
				adif.Field{Name: "BAND", Value: q.Band},
				adif.Field{Name: "FREQ", Value: q.Freq},
				adif.Field{Name: "FREQ_RX", Value: q.FreqRX},
				adif.Field{Name: "MODE", Value: q.Mode},
				adif.Field{Name: "PROP_MODE", Value: q.PropMode},
				adif.Field{Name: "SAT_NAME", Value: q.SatName},
				adif.Field{Name: "RST_SENT", Value: q.RSTSent},
				adif.Field{Name: "RST_RCVD", Value: q.RSTRcvd},
				adif.Field{Name: "NAME", Value: q.Name},
				adif.Field{Name: "QSL_SENT_VIA", Value: via},
				adif.Field{Name: "QSL_VIA", Value: mgr},
				adif.Field{Name: "QSLMSG", Value: c.Note},
				adif.Field{Name: "STATION_CALLSIGN", Value: myCall},
			); err != nil {
				return "", err
			}
		}
	}
	// Written whole or not at all: a half file must not reach the service.
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// htmxExportFile serves an exported ADIF file (?name=, a bare file name from
// the export directory) as a download.
func (s *Server) htmxExportFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".adi") || strings.HasPrefix(name, ".") {
		s.fail(w, r, http.StatusBadRequest, "no such export")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, filepath.Join(s.exportDir(), name))
}
