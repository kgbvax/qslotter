package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/template"
)

const layoutTestConfig = `# qslotter test config
clublog:
  call: DL9ET
station:
  name: Ingo
  qth: Bad Tölz
printer:
  paper_size_mm: [100, 74]   # card stock
card:
  template: ""   # active layout
`

// newLayoutServer is a server with a config file in a temp directory (the
// layout editor keeps its layouts next to it) and a fake printer.
func newLayoutServer(t *testing.T) (*Server, http.Handler, store.Store, string) {
	t.Helper()
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgFile, []byte(layoutTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(cfg, st, events.New(), cfgFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.printer = &fakePrinter{}
	return srv, srv.Routes(), st, cfgFile
}

func sendJSON(t *testing.T, h http.Handler, path string, body any, lang string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return sendJSON(t, h, path, body, "")
}

func previewOf(t *testing.T, h http.Handler, sample string, tmpl any) previewAnswer {
	t.Helper()
	r := postJSON(t, h, "/settings/cards/preview?sample="+url.QueryEscape(sample), tmpl)
	if r.Code != 200 {
		t.Fatalf("preview = %d: %s", r.Code, r.Body)
	}
	var a previewAnswer
	if err := json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func hasWarning(a previewAnswer, part string) bool {
	for _, w := range a.Warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 15))
	img.Set(3, 3, color.RGBA{200, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func upload(t *testing.T, h http.Handler, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("image", "scan.png")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/settings/cards/image?name="+url.QueryEscape(name), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestValidLayoutName(t *testing.T) {
	for _, ok := range []string{"stock", "Stock 2026", "Karte grün", "a.b-c_d", "K"} {
		if !validLayoutName(ok) {
			t.Errorf("validLayoutName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", " lead", "trail ", "dot.", "../x", "a/b", `a\b`, ":builtin", "con", "COM1", "lpt9.yaml",
		strings.Repeat("x", 41)} {
		if validLayoutName(bad) {
			t.Errorf("validLayoutName(%q) = true", bad)
		}
	}
}

// TestLayoutPageBuiltin: without layouts the built-in one prints; the page
// carries it for the script, and the Desk cards as samples.
func TestLayoutPageBuiltin(t *testing.T) {
	srv, h, st, _ := newLayoutServer(t)
	key := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	r := get(t, h, "/settings/cards")
	if r.Code != 200 {
		t.Fatalf("page = %d: %s", r.Code, r.Body)
	}
	body := r.Body.String()
	for _, want := range []string{"Built-in layout", "This layout prints the cards.", "window.qslLayout", "QSL CARD",
		"Desk: DL2ZZZ (1 QSOs)", "/static/layout.js", "Save as new layout", `"paper":[100,74]`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="lay-save"`) {
		t.Error("the built-in layout offers Save")
	}
	// Settings links to it.
	if !strings.Contains(get(t, h, "/settings").Body.String(), `href="/settings/cards"`) {
		t.Error("settings page does not link the card layout")
	}
	_ = srv
}

// TestLayoutLifecycle: save as new, save, activate (the Desk prints with
// it), rename (the config follows), delete.
func TestLayoutLifecycle(t *testing.T) {
	srv, h, st, cfgFile := newLayoutServer(t)
	tmpl := template.Default()

	r := postJSON(t, h, "/settings/cards/save?create=1&name=Stock&from=:builtin", tmpl)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"/settings/cards?name=Stock"`) {
		t.Fatalf("save as = %d: %s", r.Code, r.Body)
	}
	if r := postJSON(t, h, "/settings/cards/save?create=1&name=Stock", tmpl); r.Code != http.StatusConflict {
		t.Fatalf("second save as = %d, want 409", r.Code)
	}
	tmpl.Fields[4].X = 10 // call
	tmpl.Fields = append(tmpl.Fields, template.Field{Kind: template.KindLine, X: 4, Y: 45, W: 92})
	if r := postJSON(t, h, "/settings/cards/save?name=Stock", tmpl); r.Code != http.StatusNoContent || !strings.Contains(r.Header().Get("HX-Trigger"), "Stock saved") {
		t.Fatalf("save = %d (%s): %s", r.Code, r.Header().Get("HX-Trigger"), r.Body)
	}
	path := filepath.Join(filepath.Dir(cfgFile), "cards", "Stock.yaml")
	saved, err := template.Load(path)
	if err != nil || saved.Fields[4].X != 10 || saved.Name != "Stock" || len(saved.Fields) != len(tmpl.Fields) {
		t.Fatalf("saved layout = %+v, %v", saved, err)
	}
	for _, c := range []struct {
		path string
		body any
		code int
	}{
		{"/settings/cards/save?name=Nope", tmpl, http.StatusNotFound},
		{"/settings/cards/save?name=..%2Fx", tmpl, http.StatusBadRequest},
		{"/settings/cards/save?name=Stock", `{"fields":[{"name":"call","font_size":2}]}`, http.StatusBadRequest},
		{"/settings/cards/save?name=Stock", `{not json`, http.StatusBadRequest},
	} {
		if r := postJSON(t, h, c.path, c.body); r.Code != c.code {
			t.Errorf("%s = %d, want %d: %s", c.path, r.Code, c.code, r.Body)
		}
	}

	// Activate: card.template is written relative, the comment stays, the
	// Desk prints with the layout.
	if r := postForm(t, h, "/settings/cards/activate", url.Values{"name": {"Stock"}}); r.Code != http.StatusSeeOther {
		t.Fatalf("activate = %d: %s", r.Code, r.Body)
	}
	disk, _ := os.ReadFile(cfgFile)
	if !strings.Contains(string(disk), `template: "cards/Stock.yaml" # active layout`) || !strings.Contains(string(disk), "# card stock") {
		t.Fatalf("config after activate:\n%s", disk)
	}
	if got, err := srv.cardTemplate(); err != nil || got.Name != "Stock" {
		t.Fatalf("cardTemplate = %+v, %v", got, err)
	}
	key := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	if r := postForm(t, h, "/work/print", url.Values{"key": {key}, "route": {"B"}}); r.Code != 200 {
		t.Fatalf("print = %d: %s", r.Code, r.Body)
	}
	if n := len(srv.printer.(*fakePrinter).printed); n != 1 {
		t.Fatalf("printed %d cards", n)
	}
	body := get(t, h, "/settings/cards").Body.String()
	if !strings.Contains(body, `id="lay-save"`) || !strings.Contains(body, "This layout prints the cards.") || strings.Contains(body, "Delete layout") {
		t.Error("the active layout's page: want Save, the active note and no Delete")
	}

	if r := postForm(t, h, "/settings/cards/delete", url.Values{"name": {"Stock"}}); r.Code != http.StatusConflict {
		t.Fatalf("deleting the active layout = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/settings/cards/rename", url.Values{"name": {"Stock"}, "to": {"Stock 2026"}}); r.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d: %s", r.Code, r.Body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old file after rename: %v", err)
	}
	if got, err := srv.cardTemplate(); err != nil || got.Name != "Stock 2026" {
		t.Fatalf("cardTemplate after rename = %+v, %v", got, err)
	}
	postJSON(t, h, "/settings/cards/save?create=1&name=Other", tmpl)
	if r := postForm(t, h, "/settings/cards/rename", url.Values{"name": {"Other"}, "to": {"Stock 2026"}}); r.Code != http.StatusConflict {
		t.Fatalf("rename onto an existing layout = %d, want 409", r.Code)
	}
	if r := postForm(t, h, "/settings/cards/activate", url.Values{"name": {builtinLayout}}); r.Code != http.StatusSeeOther {
		t.Fatalf("activate built-in = %d", r.Code)
	}
	if srv.config().Card.Template != "" {
		t.Fatalf("card.template = %q, want empty", srv.config().Card.Template)
	}
	if r := postForm(t, h, "/settings/cards/delete", url.Values{"name": {"Stock 2026"}}); r.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", r.Code, r.Body)
	}
	list, active, _ := srv.layouts(httptest.NewRequest("GET", "/", nil))
	if active != builtinLayout || len(list) != 2 || list[1].ID != "Other" {
		t.Fatalf("layouts = %+v (active %s)", list, active)
	}
}

// TestLayoutImage: the card scan is stored next to the layout, served back,
// copied by "save as", moved by rename and removed with the layout.
func TestLayoutImage(t *testing.T) {
	_, h, _, cfgFile := newLayoutServer(t)
	dir := filepath.Join(filepath.Dir(cfgFile), "cards")
	postJSON(t, h, "/settings/cards/save?create=1&name=A", template.Default())
	img := pngBytes(t)
	if r := upload(t, h, "A", img); r.Code != 200 || !strings.Contains(r.Body.String(), "/settings/cards/image?name=A") {
		t.Fatalf("upload = %d: %s", r.Code, r.Body)
	}
	if tmpl, _ := template.Load(filepath.Join(dir, "A.yaml")); tmpl.PreviewImage != "A.png" {
		t.Fatalf("preview_image = %q", tmpl.PreviewImage)
	}
	r := get(t, h, "/settings/cards/image?name=A")
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(r.Body.Bytes(), img) {
		t.Fatalf("image = %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	if r := upload(t, h, "A", []byte("<html>not a picture</html>")); r.Code != http.StatusBadRequest {
		t.Fatalf("text upload = %d, want 400", r.Code)
	}
	big := append(append([]byte{}, img...), make([]byte, maxLayoutImage)...)
	if r := upload(t, h, "A", big); r.Code != http.StatusBadRequest {
		t.Fatalf("oversized upload = %d, want 400", r.Code)
	}
	// Saving keeps the picture, whatever the editor sends.
	postJSON(t, h, "/settings/cards/save?name=A", template.Default())
	if tmpl, _ := template.Load(filepath.Join(dir, "A.yaml")); tmpl.PreviewImage != "A.png" {
		t.Fatalf("preview_image after save = %q", tmpl.PreviewImage)
	}
	postJSON(t, h, "/settings/cards/save?create=1&name=B&from=A", template.Default())
	if get(t, h, "/settings/cards/image?name=B").Code != 200 {
		t.Fatal("save as did not copy the picture")
	}
	postForm(t, h, "/settings/cards/rename", url.Values{"name": {"B"}, "to": {"C"}})
	if get(t, h, "/settings/cards/image?name=C").Code != 200 {
		t.Fatal("rename lost the picture")
	}
	if _, err := os.Stat(filepath.Join(dir, "B.png")); !os.IsNotExist(err) {
		t.Fatal("rename left the old picture")
	}
	if r := postForm(t, h, "/settings/cards/image/delete?name=C", nil); r.Code != http.StatusNoContent {
		t.Fatalf("image delete = %d", r.Code)
	}
	if get(t, h, "/settings/cards/image?name=C").Code != 404 {
		t.Fatal("picture still served after delete")
	}
	postForm(t, h, "/settings/cards/delete", url.Values{"name": {"A"}})
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "C.yaml" {
		t.Fatalf("cards/ = %v, want only C.yaml", names)
	}
	if get(t, h, "/settings/cards/image?name=%3Abuiltin").Code != 404 || get(t, h, "/settings/cards/image?name=..%2Fconfig").Code != 404 {
		t.Fatal("image of a layout without one")
	}
}

// TestLayoutPreview: the card as the printer lays it out, with warnings.
func TestLayoutPreview(t *testing.T) {
	_, h, st, _ := newLayoutServer(t)
	a := previewOf(t, h, "long", template.Default())
	if a.Error != "" || a.Pages != 1 || len(a.Ops) < 20 {
		t.Fatalf("default preview = %+v", a)
	}
	if !hasWarning(a, "Name: too long") || hasWarning(a, "on top of each other") || hasWarning(a, "off the card") {
		t.Errorf("default layout warnings = %v", a.Warnings)
	}
	rows := 0
	for _, op := range a.Ops {
		if op.Kind == "text" && template.IsRowField(template.Default().Fields[op.Field].Name) {
			rows = max(rows, op.Row+1)
		}
	}
	if rows != 3 {
		t.Errorf("long sample fills %d rows, want 3", rows)
	}

	bad := template.Default()
	bad.Fields[5].X, bad.Fields[5].Y = 8, bad.Fields[4].Y // name onto the call
	bad.Rows.PitchMM = 2
	bad.Fields = append(bad.Fields, template.Field{Kind: template.KindRect, X: 50, Y: 50, W: 100, H: 10})
	bad.Rows.Max = 2 // 3 QSOs on the Desk card below: two cards
	a = previewOf(t, h, "long", bad)
	for _, want := range []string{"Callsign and Name print on top of each other", "The rows of Date run into each other", "Box: partly off the card"} {
		if !hasWarning(a, want) {
			t.Errorf("warnings %v lack %q", a.Warnings, want)
		}
	}

	if a := previewOf(t, h, "short", template.Default()); a.Pages != 1 || !opText(a.Ops, "DL1ABC") || opText(a.Ops, "via KC4AAA") {
		t.Errorf("short sample = %+v", a)
	}
	var keys []string
	for _, d := range []string{"20240103", "20240104", "20240105"} {
		k := addQueued(t, st, "DL2ZZZ", d)
		postForm(t, h, "/queue/yes", url.Values{"key": {k}})
		keys = append(keys, k)
	}
	a = previewOf(t, h, keys[2], bad)
	if !opText(a.Ops, "DL2ZZZ") || a.Pages != 2 || !hasWarning(a, "fill 2 cards") {
		t.Errorf("Desk sample = %+v", a)
	}
	if a := previewOf(t, h, "long", `{"fields":[{"name":"call","x_mm":4,"y_mm":30,"font_size":200}]}`); a.Error == "" || len(a.Ops) != 0 {
		t.Errorf("invalid layout = %+v, want an error", a)
	}
}

func opText(ops []printer.Op, text string) bool {
	for _, op := range ops {
		if op.Text == text {
			return true
		}
	}
	return false
}

// TestLayoutTestCardAndPDF: a test card goes to the printer, a PDF comes
// back; the offset from the query is checked.
func TestLayoutTestCardAndPDF(t *testing.T) {
	srv, h, _, _ := newLayoutServer(t)
	r := postJSON(t, h, "/settings/cards/test?name=%3Abuiltin&sample=short&ox=1.5&oy=-0,5", template.Default())
	if r.Code != http.StatusNoContent || !strings.Contains(r.Header().Get("HX-Trigger"), "Test card sent") {
		t.Fatalf("test card = %d (%s): %s", r.Code, r.Header().Get("HX-Trigger"), r.Body)
	}
	fp := srv.printer.(*fakePrinter)
	if len(fp.printed) != 1 {
		t.Fatalf("printed %d", len(fp.printed))
	}
	if r := postJSON(t, h, "/settings/cards/test?ox=25", template.Default()); r.Code != http.StatusBadRequest {
		t.Fatalf("offset 25 mm = %d, want 400", r.Code)
	}
	fp.err = os.ErrDeadlineExceeded
	if r := postJSON(t, h, "/settings/cards/test", template.Default()); r.Code != http.StatusBadGateway {
		t.Fatalf("printer error = %d, want 502", r.Code)
	}
	r = postJSON(t, h, "/settings/cards/pdf?sample=long&ruler=1", template.Default())
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(r.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("pdf = %d %q", r.Code, r.Header().Get("Content-Type"))
	}
}

// TestLayoutOffset: the printer offset lands in the config as numbers and
// shifts the Desk's prints.
func TestLayoutOffset(t *testing.T) {
	srv, h, _, cfgFile := newLayoutServer(t)
	if r := postForm(t, h, "/settings/cards/offset", url.Values{"x": {"1,5"}, "y": {"-0.5"}}); r.Code != http.StatusNoContent {
		t.Fatalf("offset = %d: %s", r.Code, r.Body)
	}
	disk, _ := os.ReadFile(cfgFile)
	if !strings.Contains(string(disk), "offset_mm: [1.5, -0.5]") || !strings.Contains(string(disk), "# card stock") {
		t.Fatalf("config:\n%s", disk)
	}
	if got := srv.config().Printer.OffsetMM; got != [2]float64{1.5, -0.5} {
		t.Fatalf("live offset = %v", got)
	}
	if r := postForm(t, h, "/settings/cards/offset", url.Values{"x": {"30"}, "y": {"0"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("offset 30 = %d, want 400", r.Code)
	}
	if r := postForm(t, h, "/settings/cards/offset", url.Values{"x": {"abc"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("offset abc = %d, want 400", r.Code)
	}
	// Twice: the existing value is replaced, not appended.
	postForm(t, h, "/settings/cards/offset", url.Values{"x": {"0"}, "y": {"2"}})
	disk, _ = os.ReadFile(cfgFile)
	if strings.Count(string(disk), "offset_mm") != 1 || !strings.Contains(string(disk), "offset_mm: [0, 2]") {
		t.Fatalf("config after the second save:\n%s", disk)
	}
}

// TestLayoutNoConfig: without a config file the editor shows layouts and
// prints test cards, but saves nothing.
func TestLayoutNoConfig(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	r := get(t, h, "/settings/cards")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Layouts cannot be saved") || strings.Contains(r.Body.String(), "Copy, rename, delete") {
		t.Fatalf("page = %d", r.Code)
	}
	for _, p := range []string{"/settings/cards/save?name=A&create=1", "/settings/cards/offset"} {
		if r := postJSON(t, h, p, template.Default()); r.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503", p, r.Code)
		}
	}
	if a := previewOf(t, h, "long", template.Default()); a.Error != "" || len(a.Ops) == 0 {
		t.Errorf("preview without config = %+v", a)
	}
}

// TestLayoutMissingFile: card.template naming a missing file prints the
// built-in layout, and the editor says so.
func TestLayoutMissingFile(t *testing.T) {
	srv, h, _, _ := newLayoutServer(t)
	srv.cfg.Card.Template = filepath.Join(t.TempDir(), "gone.yaml")
	body := get(t, h, "/settings/cards").Body.String()
	if !strings.Contains(body, "does not exist: cards print with the built-in layout") {
		t.Fatal("no note about the missing layout file")
	}
	// A configured file outside cards/ is listed, read-only.
	other := filepath.Join(t.TempDir(), "mine.yaml")
	if err := template.Default().Save(other); err != nil {
		t.Fatal(err)
	}
	srv.cfg.Card.Template = other
	body = get(t, h, "/settings/cards").Body.String()
	if !strings.Contains(body, "mine.yaml (configured file)") || !strings.Contains(body, "outside the editor") || strings.Contains(body, `id="lay-save"`) {
		t.Fatal("configured file outside cards/ not shown read-only")
	}
}

// TestLayoutGerman renders the editor and its messages in German and fails
// on any text the German catalogs lack.
func TestLayoutGerman(t *testing.T) {
	missing := map[string]bool{}
	i18n.Default.OnMissing = func(lang, text string) { missing[text] = true }
	defer func() { i18n.Default.OnMissing = nil }()
	const de = "de-DE,de;q=0.9"

	srv, h, st, _ := newLayoutServer(t)
	key := addQueued(t, st, "DL2ZZZ", "20240103")
	postForm(t, h, "/queue/yes", url.Values{"key": {key}})
	sendJSON(t, h, "/settings/cards/save?create=1&name=A", template.Default(), de)
	sendJSON(t, h, "/settings/cards/save?create=1&name=A", template.Default(), de) // exists
	sendJSON(t, h, "/settings/cards/save?name=Nope", template.Default(), de)
	sendJSON(t, h, "/settings/cards/save?name=..", template.Default(), de)
	sendJSON(t, h, "/settings/cards/save?name=A", template.Default(), de)
	upload(t, h, "A", pngBytes(t))
	requestDE(t, h, http.MethodPost, "/settings/cards/activate", url.Values{"name": {"A"}})
	requestDE(t, h, http.MethodPost, "/settings/cards/delete", url.Values{"name": {"A"}})
	requestDE(t, h, http.MethodPost, "/settings/cards/offset", url.Values{"x": {"1"}, "y": {"2"}})
	requestDE(t, h, http.MethodPost, "/settings/cards/offset", url.Values{"x": {"99"}})
	sendJSON(t, h, "/settings/cards/test?sample=short", template.Default(), de)
	bad := template.Default()
	bad.Fields[5].X, bad.Fields[5].Y, bad.Rows.PitchMM, bad.Rows.Max = 8, bad.Fields[4].Y, 2, 2
	bad.Fields = append(bad.Fields, template.Field{Kind: template.KindRect, X: 50, Y: 50, W: 100, H: 10},
		template.Field{Kind: template.KindLine, X: 50, Y: 89, W: 60, H: 5})
	sendJSON(t, h, "/settings/cards/preview?sample=long", bad, de)
	for _, p := range []string{"/settings/cards", "/settings/cards?name=A", "/settings/cards?name=%3Abuiltin", "/settings"} {
		if r := requestDE(t, h, http.MethodGet, p, nil); r.Code != 200 {
			t.Errorf("%s = %d", p, r.Code)
		}
	}
	srv.cfg.Card.Template = filepath.Join(t.TempDir(), "gone.yaml")
	requestDE(t, h, http.MethodGet, "/settings/cards", nil)
	other := filepath.Join(t.TempDir(), "mine.yaml")
	template.Default().Save(other)
	srv.cfg.Card.Template = other
	requestDE(t, h, http.MethodGet, "/settings/cards?name=%3Aconfigured", nil)
	srv.printer.(*fakePrinter).err = os.ErrDeadlineExceeded
	sendJSON(t, h, "/settings/cards/test", template.Default(), de)

	nc, _, _ := newTestServer(t)
	requestDE(t, nc.Routes(), http.MethodGet, "/settings/cards", nil)
	requestDE(t, nc.Routes(), http.MethodPost, "/settings/cards/offset", url.Values{"x": {"1"}})

	if len(missing) > 0 {
		var list []string
		for m := range missing {
			list = append(list, m)
		}
		sort.Strings(list)
		t.Fatalf("%d text(s) missing from the German catalogs (internal/i18n/locales/de):\n%s", len(list), strings.Join(list, "\n"))
	}
}

// TestLayoutBrokenFile: a layout file that does not load shows its error,
// keeps the picker working and can be deleted.
func TestLayoutBrokenFile(t *testing.T) {
	_, h, _, cfgFile := newLayoutServer(t)
	dir := filepath.Join(filepath.Dir(cfgFile), "cards")
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "Bad.yaml"), []byte("fields: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/settings/cards?name=Bad").Body.String()
	for _, want := range []string{"The layout Bad does not load", "/static/layout.js", "Delete layout", `"model":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="lay-svg"`) {
		t.Error("broken layout shows the editor")
	}
	if r := postForm(t, h, "/settings/cards/delete", url.Values{"name": {"Bad"}}); r.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", r.Code, r.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "Bad.yaml")); !os.IsNotExist(err) {
		t.Fatal("broken layout not deleted")
	}
}
