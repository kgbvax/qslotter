package web

// The card layout editor (Settings > Card layout): the operator places the
// fields on a picture of the pre-printed card instead of editing YAML. The
// layouts stay YAML templates (VISION §6), kept in cards/ next to the config
// file; card.template points at the active one, empty = the built-in
// layout. The browser never sets text itself: every change is laid out by
// printer.Preview - the same code that prints - and drawn from its result.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/template"
	"gopkg.in/yaml.v3"
)

// Layout IDs that are not a file in cards/: the built-in layout and a
// card.template file outside cards/ (both read-only in the editor).
const (
	builtinLayout    = ":builtin"
	configuredLayout = ":configured"
)

const (
	maxLayoutJSON  = 1 << 20 // a layout posted by the editor
	maxLayoutImage = 8 << 20 // a card scan
	maxOffsetMM    = 20.0    // printer offset either way
)

// layoutMu serialises the editor's file operations (two windows).
var layoutMu sync.Mutex

var layoutNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,39}$`)

// validLayoutName: letters, digits, space, dot, dash, underscore; no path
// parts, no trailing dot or space and no reserved device name (Windows).
func validLayoutName(n string) bool {
	if !layoutNameRe.MatchString(n) || strings.HasSuffix(n, ".") || strings.HasSuffix(n, " ") {
		return false
	}
	base := strings.ToUpper(strings.TrimSpace(strings.SplitN(n, ".", 2)[0]))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return false
	}
	return true
}

// cardsDir is where the editor keeps its layouts ("" without a config file).
func (s *Server) cardsDir() string {
	if s.cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.cfgPath), "cards")
}

func (s *Server) layoutPath(name string) string {
	return filepath.Join(s.cardsDir(), name+".yaml")
}

// LayoutEntry is one layout in the editor's list.
type LayoutEntry struct {
	ID       string // a name in cards/, builtinLayout or configuredLayout
	Label    string
	Active   bool // prints the cards
	Editable bool // a file in cards/ (and a config file to keep it with)
}

// layouts lists the layouts - built-in, the configured file when it lies
// outside cards/, the files in cards/ - and which one prints, with a note
// when card.template names a file that does not exist (the built-in prints).
func (s *Server) layouts(r *http.Request) (list []LayoutEntry, active string, note string) {
	cfg := s.config()
	dir := s.cardsDir()
	active = builtinLayout
	if p := cfg.Card.Template; p != "" {
		_, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			note = s.tr(r, "The configured layout file %s does not exist: cards print with the built-in layout.", p)
		case dir != "" && filepath.Clean(filepath.Dir(p)) == filepath.Clean(dir) && strings.HasSuffix(p, ".yaml") &&
			validLayoutName(strings.TrimSuffix(filepath.Base(p), ".yaml")):
			active = strings.TrimSuffix(filepath.Base(p), ".yaml")
		default:
			active = configuredLayout
		}
	}
	list = append(list, LayoutEntry{ID: builtinLayout, Label: s.tr(r, "Built-in layout")})
	if active == configuredLayout {
		list = append(list, LayoutEntry{ID: configuredLayout, Label: s.tr(r, "%s (configured file)", filepath.Base(cfg.Card.Template))})
	}
	var names []string
	if dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			n := strings.TrimSuffix(e.Name(), ".yaml")
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") && validLayoutName(n) {
				names = append(names, n)
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	for _, n := range names {
		list = append(list, LayoutEntry{ID: n, Label: n, Editable: true})
	}
	for i := range list {
		list[i].Active = list[i].ID == active
	}
	return list, active, note
}

// errNoLayout: the layout asked for does not exist.
var errNoLayout = errors.New("no such layout")

// loadLayout returns the layout with this ID and the file it lives in (""
// for the built-in one).
func (s *Server) loadLayout(id string) (*template.Template, string, error) {
	var path string
	switch id {
	case builtinLayout:
		return template.Default(), "", nil
	case configuredLayout:
		path = s.config().Card.Template
		if path == "" {
			return nil, "", errNoLayout
		}
	default:
		if !validLayoutName(id) || s.cardsDir() == "" {
			return nil, "", errNoLayout
		}
		path = s.layoutPath(id)
	}
	t, err := template.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", errNoLayout
	}
	if err != nil {
		return nil, "", err
	}
	return t, path, nil
}

// layoutImagePath is the card scan of a layout file ("" when it has none).
// Only a file right next to the layout counts.
func layoutImagePath(t *template.Template, path string) string {
	if t == nil || path == "" || t.PreviewImage == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), filepath.Base(t.PreviewImage))
}

// catField is one data field in the editor's menu.
type catField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// fieldLabels are the English names of the data fields.
var fieldLabels = map[string]string{
	"call": "Callsign", "name": "Name", "qth": "QTH", "my_call": "My callsign", "my_name": "My name",
	"my_qth": "My QTH", "via": "via manager", "qslmsg": "QSL message",
	"qso_date": "Date", "time_on": "Time (UTC)", "band": "Band", "mode": "Mode", "rst_sent": "RST sent",
	"rst_rcvd": "RST rcvd", "freq": "Frequency", "sat_name": "Satellite", "freq_rx": "RX frequency",
}

// fieldLabel names a template field for the operator: its data field, its
// text in quotes, or line / box.
func (s *Server) fieldLabel(r *http.Request, f template.Field) string {
	switch strings.ToLower(f.Kind) {
	case template.KindLine:
		return s.tr(r, "Line")
	case template.KindRect:
		return s.tr(r, "Box")
	}
	if f.Text != "" {
		return "“" + f.Text + "”"
	}
	if l, ok := fieldLabels[strings.ToLower(f.Name)]; ok {
		return s.tr(r, l)
	}
	return f.Name
}

// layoutJSStrings are the editor script's texts, handed over translated.
var layoutJSStrings = []string{
	"Fixed text", "Text", "Line", "Box", "per QSO", "no value on this sample",
	"Data field", "X (mm)", "Y (mm)", "Width (mm)", "Height (mm)",
	"Line width (mm)", "Font", "Size (pt)", "Bold", "Align", "left", "centre", "right",
	"Duplicate", "Delete", "Click an element on the card, or pick one here.",
	"Network error - is the qslotter server running?", "Error", "Discard the unsaved changes?",
	"X is where the text starts (left), its middle (centre) or where it ends (right).",
	"The printer is set up for %s x %s mm paper (printer.paper_size_mm in the config file), this card is %s x %s mm.",
	"Only on satellite cards", "satellite cards", "Lines",
	"Width 0: one line up to the card margin. With a width, a longer text wraps onto up to Lines lines, then gets smaller.",
	"For a satellite column and its heading: an HF card leaves them out.",
}

// layoutPageData is what the editor script gets.
type layoutPageData struct {
	ID       string             `json:"id"`
	Editable bool               `json:"editable"`
	Model    *template.Template `json:"model"`
	Card     []catField         `json:"card"`
	Row      []catField         `json:"row"`
	Labels   map[string]string  `json:"labels"`  // field name -> label
	Strings  map[string]string  `json:"strings"` // English -> translated
	Fonts    []string           `json:"fonts"`
	Image    string             `json:"image,omitempty"` // URL of the card scan
	Margin   float64            `json:"margin"`          // printer.FitMarginMM
	Paper    [2]float64         `json:"paper"`           // printer.paper_size_mm
	CanSave  bool               `json:"can_save"`        // a config file to keep layouts with
}

// sampleChoice is one entry of the sample select.
type sampleChoice struct{ Value, Label string }

// pageCards renders the layout editor for ?name= (default: the active one).
func (s *Server) pageCards(w http.ResponseWriter, r *http.Request) {
	list, active, note := s.layouts(r)
	id := r.URL.Query().Get("name")
	if id == "" {
		id = active
	}
	tmpl, path, err := s.loadLayout(id)
	var loadErr string
	if errors.Is(err, errNoLayout) {
		id = active
		tmpl, path, err = s.loadLayout(id)
	}
	if err != nil {
		loadErr = s.tr(r, "The layout %s does not load: %s", id, err.Error())
		tmpl, path = nil, ""
	}
	var entry LayoutEntry
	for _, e := range list {
		if e.ID == id {
			entry = e
		}
	}
	cfg := s.config()
	data := layoutPageData{ID: id, Editable: entry.Editable && s.cfgPath != "", Model: tmpl,
		Labels: map[string]string{}, Strings: map[string]string{}, Fonts: template.Fonts, Margin: printer.FitMarginMM,
		Paper: cfg.Printer.PaperSizeMM, CanSave: s.cfgPath != ""}
	for _, n := range template.CardFieldNames() {
		data.Card = append(data.Card, catField{n, s.tr(r, fieldLabels[n])})
	}
	for _, n := range template.RowFieldNames() {
		data.Row = append(data.Row, catField{n, s.tr(r, fieldLabels[n])})
	}
	for n, l := range fieldLabels {
		data.Labels[n] = s.tr(r, l)
	}
	for _, k := range layoutJSStrings {
		data.Strings[k] = s.tr(r, k)
	}
	if layoutImagePath(tmpl, path) != "" {
		if _, err := os.Stat(layoutImagePath(tmpl, path)); err == nil {
			data.Image = "/settings/cards/image?name=" + url.QueryEscape(id)
		}
	}
	s.render(w, r, "cards.html", map[string]any{
		"Layouts":  list,
		"Entry":    entry,
		"Active":   active,
		"Note":     note,
		"LoadErr":  loadErr,
		"NoConfig": s.cfgPath == "",
		"Data":     data,
		"Samples":  s.sampleChoices(r),
		"OffsetX":  cfg.Printer.OffsetMM[0],
		"OffsetY":  cfg.Printer.OffsetMM[1],
		"Printer":  cfg.Printer.Name,
		"HasImage": data.Image != "",
		"Created":  r.URL.Query().Get("created") == "1" && entry.Editable,
	})
}

// sampleChoices are the cards the editor can lay out: two made-up ones and
// the cards waiting at the Desk.
func (s *Server) sampleChoices(r *http.Request) []sampleChoice {
	out := []sampleChoice{
		{"long", s.tr(r, "Sample: long names, a full card, via manager, satellite")},
		{"short", s.tr(r, "Sample: one short QSO")},
		{"sat", s.tr(r, "Sample: a satellite QSO")},
	}
	cards, err := s.deskCards(false)
	if err != nil {
		return out
	}
	for i, c := range cards {
		if i == 20 {
			break
		}
		out = append(out, sampleChoice{c.Lead.Item.QSLKey, s.tr(r, "Desk: %s (%d QSOs)", c.Call, len(c.Rows))})
	}
	return out
}

// sampleCard is the card the editor lays out for ?sample=: "short", "sat", a Desk
// card's lead key, else "long" - the longest values a card realistically
// gets, as many QSOs as tmpl holds.
func (s *Server) sampleCard(sample string, tmpl *template.Template) printer.CardFields {
	cfg := s.config()
	myCall := cmpOr(cfg.Clublog.Call, "DL9ET")
	switch sample {
	case "short":
		return printer.CardFields{Call: "DL1ABC", Name: "Hans", QTH: "Berlin", MyCall: myCall, MyName: cfg.Station.Name,
			MyQTH: cfg.Station.QTH, Rows: []printer.QSORow{{QSODate: "20240101", TimeOn: "1200", Band: "20m", Mode: "SSB",
				RSTSent: "59", RSTRcvd: "57", Freq: "14.250"}}}
	case "sat":
		return printer.CardFields{Call: "EA4XYZ", Name: "Carlos", QTH: "Madrid", MyCall: myCall, MyName: cfg.Station.Name, QSLMsg: "Tnx for my first RS-44 QSO!",
			MyQTH: cfg.Station.QTH, Rows: []printer.QSORow{{QSODate: "20240615", TimeOn: "1842", Band: "70cm", Mode: "FM",
				RSTSent: "59", RSTRcvd: "59", Freq: "145.850", SatName: "RS-44", FreqRX: "435.640"}}}
	case "long", "":
	default:
		if cards, err := s.deskCards(false); err == nil {
			for _, c := range cards {
				if c.Lead.Item.QSLKey != sample {
					continue
				}
				var qsos []*store.QSO
				for _, row := range c.Rows {
					qsos = append(qsos, row.QSO)
				}
				via := ""
				if strings.HasPrefix(c.Route, "M") {
					via = c.MgrPrefill
				}
				return cardFieldsFor(cfg, qsos, via)
			}
		}
	}
	card := printer.CardFields{Call: "VP2V/DL9ET", Name: "Hans-Joachim Müller-Lüdenscheidt", QTH: "Garmisch-Partenkirchen",
		MyCall: myCall, MyName: cmpOr(cfg.Station.Name, "Ingomar Otter"), MyQTH: cmpOr(cfg.Station.QTH, "Bad Tölz, JN57"),
		Via: "KC4AAA", QSLMsg: "Thanks for the nice QSO on 2190 m - hope to work you again, 73!"}
	long := []printer.QSORow{
		{QSODate: "20241231", TimeOn: "235959", Band: "2190m", Mode: "OLIVIA", RSTSent: "59+20", RSTRcvd: "599", Freq: "0.1375"},
		{QSODate: "20240615", TimeOn: "000000", Band: "70cm", Mode: "SSB", RSTSent: "59", RSTRcvd: "59", Freq: "435.645",
			SatName: "RS-44", FreqRX: "145.851"},
		{QSODate: "20240101", TimeOn: "120000", Band: "13cm", Mode: "PSK31", RSTSent: "579", RSTRcvd: "599", Freq: "2400.250",
			SatName: "QO-100", FreqRX: "10489.750"},
	}
	for i := range max(1, printer.MaxRows(tmpl)) {
		card.Rows = append(card.Rows, long[i%len(long)])
	}
	return card
}

// readLayout decodes the layout the editor posts, with defaults applied and
// validated.
func readLayout(w http.ResponseWriter, r *http.Request) (*template.Template, error) {
	var t template.Template
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLayoutJSON))
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("layout: %w", err)
	}
	t.ApplyDefaults()
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// previewAnswer is the editor's view of one card.
type previewAnswer struct {
	Ops      []printer.Op `json:"ops"`
	Pages    int          `json:"pages"`
	Warnings []string     `json:"warnings"`
	Error    string       `json:"error,omitempty"`
}

// htmxCardsPreview lays out the posted layout with a sample card
// (?sample=) as the printer would and answers the elements as JSON. A
// layout that does not validate answers its error (status 200: the editor
// shows it next to the card while the operator is still typing).
func (s *Server) htmxCardsPreview(w http.ResponseWriter, r *http.Request) {
	var ans previewAnswer
	tmpl, err := readLayout(w, r)
	if err != nil {
		ans.Error = err.Error()
		writeJSON(w, ans)
		return
	}
	ops, pages, err := printer.Preview(tmpl, s.sampleCard(r.URL.Query().Get("sample"), tmpl))
	if err != nil {
		ans.Error = err.Error()
		writeJSON(w, ans)
		return
	}
	ans.Ops, ans.Pages, ans.Warnings = ops, pages, []string{}
	fitted, outside := map[int]bool{}, map[int]bool{}
	for _, op := range ops {
		if op.Fitted && !fitted[op.Field] {
			fitted[op.Field] = true
			ans.Warnings = append(ans.Warnings, s.tr(r, "%s: too long for its place on this card - printed smaller or cut.", s.fieldLabel(r, tmpl.Fields[op.Field])))
		}
		if op.Outside && !outside[op.Field] {
			outside[op.Field] = true
			ans.Warnings = append(ans.Warnings, s.tr(r, "%s: partly off the card.", s.fieldLabel(r, tmpl.Fields[op.Field])))
		}
	}
	pairs, rowClash := overlaps(ops)
	for _, pair := range pairs {
		ans.Warnings = append(ans.Warnings, s.tr(r, "%s and %s print on top of each other.",
			s.fieldLabel(r, tmpl.Fields[pair[0]]), s.fieldLabel(r, tmpl.Fields[pair[1]])))
	}
	if len(rowClash) > 0 {
		ans.Warnings = append(ans.Warnings, s.tr(r, "The rows of %s run into each other: make the row spacing larger or the text smaller.",
			s.fieldLabel(r, tmpl.Fields[rowClash[0]])))
	}
	if pages > 1 {
		ans.Warnings = append(ans.Warnings, s.tr(r, "These QSOs fill %d cards: the layout holds %d per card.", pages, printer.MaxRows(tmpl)))
	}
	writeJSON(w, ans)
}

// overlaps returns the pairs of fields (indexes, each pair once, in field
// order) whose texts overlap on the card - from the top of the capitals to
// the bottom of the descenders, as TestDefaultTemplateFits measures - and
// the row fields whose rows run into each other. Lines and boxes are left
// out: a text inside a box is the point of the box.
func overlaps(ops []printer.Op) (pairs [][2]int, rowClash []int) {
	type box struct {
		field      int
		l, r, t, b float64
	}
	var boxes []box
	for _, op := range ops {
		if op.Kind != template.KindText {
			continue
		}
		boxes = append(boxes, box{op.Field, op.X, op.X + op.W, op.Baseline - 0.718*op.H, op.Baseline + 0.207*op.H})
	}
	seen, clash := map[[2]int]bool{}, map[int]bool{}
	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			if !(a.l < b.r && b.l < a.r && a.t < b.b && b.t < a.b) {
				continue
			}
			if a.field == b.field { // two rows of one row field
				if !clash[a.field] {
					clash[a.field] = true
					rowClash = append(rowClash, a.field)
				}
				continue
			}
			k := [2]int{min(a.field, b.field), max(a.field, b.field)}
			if !seen[k] {
				seen[k] = true
				pairs = append(pairs, k)
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	sort.Ints(rowClash)
	return pairs, rowClash
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("layout: writing JSON: %v", err)
	}
}

// canEditLayouts answers 503 when there is no config file to keep the
// layouts with.
func (s *Server) canEditLayouts(w http.ResponseWriter, r *http.Request) bool {
	if s.cfgPath == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "layout editing is disabled (no config path given at startup)")
		return false
	}
	return true
}

// layoutName reads a layout name parameter and answers 400 when it is not
// a valid one.
func (s *Server) layoutName(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	n := strings.TrimSpace(r.FormValue(param))
	if !validLayoutName(n) {
		s.fail(w, r, http.StatusBadRequest, "%q is not a usable layout name: up to 40 letters, digits, spaces, dots, dashes or underscores.", n)
		return "", false
	}
	return n, true
}

// htmxCardsSave stores the posted layout as cards/<name>.yaml. With
// create=1 it makes a new layout (409 when the name is taken), copying the
// card scan of the layout ?from= along; otherwise the layout must exist and
// keeps its scan. With auto=1 (and create=1) the server names the new layout
// ("My card", "My card 2", ...) and, when ?from= is the layout that prints,
// makes the new one print instead: editing the built-in layout needs no copy
// step. A new layout answers {"name", "location": its editor URL}.
func (s *Server) htmxCardsSave(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	create := r.FormValue("create") == "1"
	auto := create && r.FormValue("auto") == "1"
	var name string
	if !auto {
		var ok bool
		if name, ok = s.layoutName(w, r, "name"); !ok {
			return
		}
	}
	tmpl, err := readLayout(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	from := r.FormValue("from")
	_, active, _ := s.layouts(r)
	if auto {
		name = s.freeLayoutName(r)
	}
	path := s.layoutPath(name)
	_, statErr := os.Stat(path)
	tmpl.Name = name
	tmpl.PreviewImage = ""
	if create {
		if statErr == nil {
			s.fail(w, r, http.StatusConflict, "A layout named %s exists already.", name)
			return
		}
		if src, srcPath, err := s.loadLayout(from); err == nil {
			if img := layoutImagePath(src, srcPath); img != "" {
				ext := strings.ToLower(filepath.Ext(img))
				if err := copyFile(img, filepath.Join(s.cardsDir(), name+ext)); err == nil {
					tmpl.PreviewImage = name + ext
				} else {
					log.Printf("layout: copying the card picture %s: %v", img, err)
				}
			}
		}
	} else {
		if errors.Is(statErr, fs.ErrNotExist) {
			s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
			return
		}
		if old, err := template.Load(path); err == nil {
			tmpl.PreviewImage = old.PreviewImage
		}
	}
	if err := tmpl.Save(path); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving the layout: %s", err.Error())
		return
	}
	if auto && from != "" && from == active {
		if err := s.setActiveLayout(name); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
			return
		}
	}
	s.notice(w, r, "Layout %s saved.", name)
	if create {
		writeJSON(w, map[string]string{"name": name,
			"location": "/settings/cards?created=1&name=" + url.QueryEscape(name)})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// freeLayoutName is the first unused name of "My card", "My card 2", ...
// (in the request's language).
func (s *Server) freeLayoutName(r *http.Request) string {
	base := s.tr(r, "My card")
	if !validLayoutName(base) {
		base = "My card"
	}
	name := base
	for i := 2; ; i++ {
		if _, err := os.Stat(s.layoutPath(name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s %d", base, i)
	}
}

// copyFile copies src to dst (a new file).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// activeLayoutIs reports whether card.template is the cards/ file name.
func (s *Server) activeLayoutIs(name string) bool {
	p := s.config().Card.Template
	return p != "" && filepath.Clean(p) == filepath.Clean(s.layoutPath(name))
}

// setActiveLayout points card.template at cards/<name>.yaml (name "" =
// the built-in layout) and makes it the live config.
func (s *Server) setActiveLayout(name string) error {
	val := ""
	if name != "" {
		val = "cards/" + name + ".yaml" // relative: config.Load resolves it next to the config file
	}
	if err := updateConfigFile(s.cfgPath, map[string]string{"card.template": val}); err != nil {
		return err
	}
	_, err := s.reloadConfig()
	return err
}

func (s *Server) redirectCards(w http.ResponseWriter, r *http.Request, id string) {
	target := "/settings/cards"
	if id != "" {
		target += "?name=" + url.QueryEscape(id)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// postCardsActivate makes the layout name= the one that prints (the
// built-in one for name=:builtin).
func (s *Server) postCardsActivate(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	id := r.FormValue("name")
	switch id {
	case builtinLayout:
		if err := s.setActiveLayout(""); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
			return
		}
	case configuredLayout:
	default:
		name, ok := s.layoutName(w, r, "name")
		if !ok {
			return
		}
		if _, err := template.Load(s.layoutPath(name)); err != nil {
			s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
			return
		}
		if err := s.setActiveLayout(name); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
			return
		}
	}
	s.redirectCards(w, r, id)
}

// postCardsRename renames the layout name= to to= (with its card scan, and
// card.template when it is the active one).
func (s *Server) postCardsRename(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	name, ok := s.layoutName(w, r, "name")
	if !ok {
		return
	}
	to, ok := s.layoutName(w, r, "to")
	if !ok {
		return
	}
	if to == name {
		s.redirectCards(w, r, name)
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	from := s.layoutPath(name)
	tmpl, err := template.Load(from)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
		return
	}
	dst := s.layoutPath(to)
	// A change of case only is the same file on macOS and Windows.
	if _, err := os.Stat(dst); err == nil && !strings.EqualFold(name, to) {
		s.fail(w, r, http.StatusConflict, "A layout named %s exists already.", to)
		return
	}
	wasActive := s.activeLayoutIs(name)
	if err := os.Rename(from, dst); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "renaming the layout: %s", err.Error())
		return
	}
	if img := layoutImagePath(tmpl, from); img != "" {
		ext := strings.ToLower(filepath.Ext(img))
		if err := os.Rename(img, filepath.Join(s.cardsDir(), to+ext)); err == nil {
			tmpl.PreviewImage = to + ext
		} else if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("layout: renaming the card picture %s: %v", img, err)
		}
	}
	tmpl.Name = to
	if err := tmpl.Save(dst); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving the layout: %s", err.Error())
		return
	}
	if wasActive {
		if err := s.setActiveLayout(to); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
			return
		}
	}
	s.redirectCards(w, r, to)
}

// postCardsDelete deletes the layout name= and its card scan; the active
// layout cannot be deleted (409).
func (s *Server) postCardsDelete(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	name, ok := s.layoutName(w, r, "name")
	if !ok {
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	if s.activeLayoutIs(name) {
		s.fail(w, r, http.StatusConflict, "%s prints the cards: make another layout active first.", name)
		return
	}
	path := s.layoutPath(name)
	tmpl, err := template.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
		return
	}
	if img := layoutImagePath(tmpl, path); img != "" {
		if err := os.Remove(img); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("layout: removing the card picture %s: %v", img, err)
		}
	}
	if err := os.Remove(path); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "deleting the layout: %s", err.Error())
		return
	}
	s.redirectCards(w, r, "")
}

// imageExt is the file extension of a PNG or JPEG ("" for anything else).
func imageExt(head []byte) string {
	switch http.DetectContentType(head) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	}
	return ""
}

// postCardsImage stores the uploaded card scan (form file "image", PNG or
// JPEG, at most 8 MB) as cards/<name>.<ext> and records it in the layout.
func (s *Server) postCardsImage(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	// The limit goes first: reading any form value parses the whole upload.
	r.Body = http.MaxBytesReader(w, r.Body, maxLayoutImage+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.fail(w, r, http.StatusBadRequest, "The picture could not be read (at most 8 MB): %s", err.Error())
		return
	}
	name, ok := s.layoutName(w, r, "name")
	if !ok {
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "The picture could not be read (at most 8 MB): %s", err.Error())
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxLayoutImage+1))
	if err != nil || len(raw) > maxLayoutImage {
		s.fail(w, r, http.StatusBadRequest, "The picture could not be read (at most 8 MB): %s", fmt.Sprint(err))
		return
	}
	ext := imageExt(raw)
	if ext == "" {
		s.fail(w, r, http.StatusBadRequest, "The picture must be a PNG or JPEG file.")
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	path := s.layoutPath(name)
	tmpl, err := template.Load(path)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
		return
	}
	dst := filepath.Join(s.cardsDir(), name+ext)
	if old := layoutImagePath(tmpl, path); old != "" && filepath.Clean(old) != filepath.Clean(dst) {
		if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("layout: removing the old card picture %s: %v", old, err)
		}
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving the picture: %s", err.Error())
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		s.fail(w, r, http.StatusInternalServerError, "saving the picture: %s", err.Error())
		return
	}
	tmpl.PreviewImage = name + ext
	if err := tmpl.Save(path); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving the layout: %s", err.Error())
		return
	}
	s.notice(w, r, "Card picture saved.")
	writeJSON(w, map[string]string{"image": "/settings/cards/image?name=" + url.QueryEscape(name)})
}

// postCardsImageDelete removes the layout's card scan.
func (s *Server) postCardsImageDelete(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	name, ok := s.layoutName(w, r, "name")
	if !ok {
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	path := s.layoutPath(name)
	tmpl, err := template.Load(path)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "There is no layout named %s.", name)
		return
	}
	if img := layoutImagePath(tmpl, path); img != "" {
		if err := os.Remove(img); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.fail(w, r, http.StatusInternalServerError, "deleting the picture: %s", err.Error())
			return
		}
	}
	tmpl.PreviewImage = ""
	if err := tmpl.Save(path); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving the layout: %s", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getCardsImage serves a layout's card scan (PNG or JPEG only).
func (s *Server) getCardsImage(w http.ResponseWriter, r *http.Request) {
	tmpl, path, err := s.loadLayout(r.URL.Query().Get("name"))
	img := layoutImagePath(tmpl, path)
	if err != nil || img == "" {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(img)
	if err != nil || imageExt(raw) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(raw))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

// offsetParam reads an offset in mm (a decimal comma is fine); ok is false
// for anything that is not a number within maxOffsetMM.
func offsetParam(v string) (float64, bool) {
	v = strings.ReplaceAll(strings.TrimSpace(v), ",", ".")
	if v == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < -maxOffsetMM || f > maxOffsetMM {
		return 0, false
	}
	return f, true
}

// offsets reads ?ox= and ?oy=, defaulting to the configured printer offset.
func (s *Server) offsets(w http.ResponseWriter, r *http.Request, xParam, yParam string) (x, y float64, ok bool) {
	cfg := s.config()
	x, y = cfg.Printer.OffsetMM[0], cfg.Printer.OffsetMM[1]
	if v := r.FormValue(xParam); v != "" {
		if x, ok = offsetParam(v); !ok {
			s.fail(w, r, http.StatusBadRequest, "The printer offset must be a number of millimetres between -%g and %g.", maxOffsetMM, maxOffsetMM)
			return 0, 0, false
		}
	}
	if v := r.FormValue(yParam); v != "" {
		if y, ok = offsetParam(v); !ok {
			s.fail(w, r, http.StatusBadRequest, "The printer offset must be a number of millimetres between -%g and %g.", maxOffsetMM, maxOffsetMM)
			return 0, 0, false
		}
	}
	return x, y, true
}

// postCardsOffset stores the printer offset (x=, y= in mm) as
// printer.offset_mm.
func (s *Server) postCardsOffset(w http.ResponseWriter, r *http.Request) {
	if !s.canEditLayouts(w, r) {
		return
	}
	x, ok := offsetParam(r.FormValue("x"))
	y, ok2 := offsetParam(r.FormValue("y"))
	if !ok || !ok2 {
		s.fail(w, r, http.StatusBadRequest, "The printer offset must be a number of millimetres between -%g and %g.", maxOffsetMM, maxOffsetMM)
		return
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	if err := updateConfigNodes(s.cfgPath, map[string]*yaml.Node{"printer.offset_mm": floatPair(x, y)}); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
		return
	}
	if _, err := s.reloadConfig(); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "config saved, but reloading it failed: %s", err.Error())
		return
	}
	s.notice(w, r, "Printer offset saved: X %s mm, Y %s mm.", fmtMM(x), fmtMM(y))
	w.WriteHeader(http.StatusNoContent)
}

func fmtMM(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// testCardLabel is printed small on a test card.
func (s *Server) testCardLabel(r *http.Request, name string, x, y float64) string {
	return s.tr(r, "qslotter test card - %s - offset X %s / Y %s mm", name, fmtMM(x), fmtMM(y))
}

// htmxCardsTest prints the posted layout with the sample ?sample=, the mm
// ruler and a label, shifted by ?ox= / ?oy= (default: the configured
// offset) - a test card to check the layout and measure the offset.
func (s *Server) htmxCardsTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	x, y, ok := s.offsets(w, r, "ox", "oy")
	if !ok {
		return
	}
	tmpl, err := readLayout(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := s.config()
	pdfPath := printer.TempPDFPath()
	defer func() {
		if err := os.Remove(pdfPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("test card: removing %s: %v", pdfPath, err)
		}
	}()
	name := cmpOr(r.URL.Query().Get("name"), tmpl.Name)
	if name == builtinLayout {
		name = s.tr(r, "Built-in layout")
	}
	card := s.sampleCard(r.URL.Query().Get("sample"), tmpl)
	card.Rows = card.Rows[:min(len(card.Rows), printer.MaxRows(tmpl))] // one card
	if err := printer.Render(pdfPath, tmpl, card, printer.RenderOptions{
		OffsetXMM: x, OffsetYMM: y, Ruler: true, Label: s.testCardLabel(r, name, x, y),
	}); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "rendering the test card: %s", err.Error())
		return
	}
	if _, err := s.printer.PrintPDF(pdfPath, cfg.Printer.Name, printer.Options{
		PaperWMM: cfg.Printer.PaperSizeMM[0], PaperHMM: cfg.Printer.PaperSizeMM[1], Copies: 1,
	}); err != nil {
		s.fail(w, r, http.StatusBadGateway, "printing the test card: %s", err.Error())
		return
	}
	s.notice(w, r, "Test card sent to the printer.")
	w.WriteHeader(http.StatusNoContent)
}

// htmxCardsPDF answers the posted layout with the sample ?sample= as a PDF
// (inline), shifted by ?ox= / ?oy=, with the ruler when ?ruler=1.
func (s *Server) htmxCardsPDF(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	x, y, ok := s.offsets(w, r, "ox", "oy")
	if !ok {
		return
	}
	tmpl, err := readLayout(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	opts := printer.RenderOptions{OffsetXMM: x, OffsetYMM: y}
	if r.URL.Query().Get("ruler") == "1" {
		opts.Ruler = true
		opts.Label = s.testCardLabel(r, cmpOr(tmpl.Name, "layout"), x, y)
	}
	raw, err := printer.RenderBytes(tmpl, s.sampleCard(r.URL.Query().Get("sample"), tmpl), opts)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "rendering the card: %s", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="card.pdf"`)
	_, _ = w.Write(raw)
}
