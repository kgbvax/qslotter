package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/store"
)

// TestSettingsSaveRoundTrip: the settings form edits the config file in
// place (comments preserved), keeps secrets when the field is left empty,
// and swaps the live config.
func TestSettingsSaveRoundTrip(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	src := "# qslotter test config\nserver:\n  addr: 127.0.0.1:8473\nqrz:\n  username: old\n  password: oldsecret # keep me\nclublog:\n  email: a@b.c\n  app_password: \"12345\" # numeric on purpose\n  call: DL9ET\n  api_key: k1\nstation:\n  name: Ingo\n  qth: Bavaria\n"
	if err := os.WriteFile(cfgFile, []byte(src), 0o600); err != nil {
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
	defer st.Close()
	srv, err := New(cfg, st, events.New(), cfgFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Stub the credential checks: no network from tests.
	srv.validateFn = func(*config.Config) (i18n.Msg, i18n.Msg) { return i18n.M("OK - stub"), i18n.M("OK - stub") }
	h := srv.Routes()

	// Save: new username, empty password (keep old), numeric-looking app
	// password (must stay a string on reload), new station name.
	form := url.Values{
		"qrz_username":         {"newcall"},
		"qrz_password":         {""},
		"clublog_email":        {"a@b.c"},
		"clublog_app_password": {"12345"},
		"clublog_call":         {"DL9ET"},
		"clublog_api_key":      {""},
		"station_name":         {"Ingo M."},
		"station_qth":          {"Bavaria"},
	}
	r := postForm(t, h, "/settings/save", form)
	if r.Code != 200 {
		t.Fatalf("save = %d: %s", r.Code, r.Body)
	}

	// File: username updated, password (now double-quoted) + comment kept.
	onDisk, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	disk := string(onDisk)
	if !strings.Contains(disk, "newcall") || !strings.Contains(disk, "oldsecret") || !strings.Contains(disk, "# keep me") {
		t.Fatalf("config file not edited as expected:\n%s", disk)
	}
	reloaded, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if reloaded.QRZ.Username != "newcall" || reloaded.QRZ.Password != "oldsecret" {
		t.Fatalf("qrz after save: user=%q pass=%q", reloaded.QRZ.Username, reloaded.QRZ.Password)
	}
	if reloaded.Clublog.AppPassword != "12345" {
		t.Fatalf("numeric app password mangled: %q", reloaded.Clublog.AppPassword)
	}
	if reloaded.Station.Name != "Ingo M." {
		t.Fatalf("station name = %q", reloaded.Station.Name)
	}

	// Live config swapped: the server now serves the new values.
	if srv.config().QRZ.Username != "newcall" {
		t.Fatalf("live config not swapped: %q", srv.config().QRZ.Username)
	}

	// The settings page renders the (masked) form.
	r = get(t, h, "/settings")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "newcall") {
		t.Fatalf("/settings = %d", r.Code)
	}
}

// TestSettingsFilter: the New QSOs matrix is written as qualify.ask,
// switch the shared rules at once and queue the FT8 QSO skipped so far -
// even with FT8 in an old config's exclude_modes.
func TestSettingsFilter(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	src := "qualify:\n  exclude_modes: [\"FT4\", \"FT8\", \"SSTV\"]\n  since: all\n"
	if err := os.WriteFile(cfgFile, []byte(src), 0o600); err != nil {
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
	defer st.Close()
	ft8 := &store.QSO{QSLKey: "DL1AB|20260101|120000|20M", Call: "DL1AB", QSODate: "20260101", TimeOn: "120000", Band: "20M", Mode: "FT8", Hash: "h1"}
	if _, _, err := st.UpsertQSO(ft8); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, st, events.New(), cfgFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Routes()
	if srv.Rules().Filter() != qualify.DefaultFilter {
		t.Fatalf("filter = %+v, want the default", srv.Rules().Filter())
	}
	body := get(t, h, "/settings/general").Body.String()
	if !strings.Contains(body, `name="qualify_ask_cw_repeat" value="1" checked`) || strings.Contains(body, `name="qualify_ask_digital_new" value="1" checked`) {
		t.Fatal("the default filter is not shown")
	}

	// FT8 ticked for new stations, CW only for new stations.
	form := url.Values{"tab": {"general"}, "qualify_ask_digital_new": {"1"}, "qualify_ask_cw_new": {"1"}}
	for _, row := range []string{"phone", "keyboard", "satellite"} {
		for _, col := range qualify.Cols {
			form.Set("qualify_ask_"+row+"_"+col, "1")
		}
	}
	if r := postForm(t, h, "/settings/save", form); r.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", r.Code, r.Body)
	}
	onDisk, _ := os.ReadFile(cfgFile)
	for _, want := range []string{"cw: [new]", "digital: [new]", "phone: [new, band, repeat]"} {
		if !strings.Contains(string(onDisk), want) {
			t.Fatalf("config lacks %q:\n%s", want, onDisk)
		}
	}
	want := qualify.DefaultFilter
	want[1] = [3]bool{true, false, false}
	want[3] = [3]bool{true, false, false}
	if reloaded, err := config.Load(cfgFile); err != nil || qualify.FilterFrom(reloaded.Qualify) != want {
		t.Fatalf("filter after reload = %v (%v)", qualify.FilterFrom(reloaded.Qualify), err)
	}
	if srv.Rules().Filter() != want {
		t.Fatalf("rules not switched live: %v", srv.Rules().Filter())
	}
	if it, err := st.QueueGet(ft8.QSLKey); err != nil || it == nil || it.Status != "queued" {
		t.Fatalf("FT8 QSO not queued after switching on: %+v %v", it, err)
	}
}

// TestSettingsTabs: three tabs, each with its own sections; the Clublog
// section shows the sync state and leaves pulling to the Log page.
func TestSettingsTabs(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()
	for path, want := range map[string][]string{
		"/settings":          {`name="qrz_username"`, `name="clublog_api_key"`, "never pulled", `href="/log"`, `<input type="hidden" name="tab" value="connections">`},
		"/settings/printing": {"Prints the cards", `href="/settings/cards?name=%3abuiltin"`, `<svg class="cardsvg"`, `action="/settings/printer"`, `hx-post="/settings/testcard"`, `name="station_name"`},
		"/settings/general":  {`name="ui_language"`, `name="qualify_ask_cw_repeat"`, `name="qualify_ask_satellite_band"`, `id="build"`, "Version"},
	} {
		r := get(t, h, path)
		if r.Code != 200 {
			t.Fatalf("%s = %d", path, r.Code)
		}
		for _, w := range want {
			if !strings.Contains(r.Body.String(), w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}
		if strings.Contains(r.Body.String(), `hx-post="/sync/pull"`) {
			t.Errorf("%s offers a pull: that is the Log page's", path)
		}
	}
}

// TestSettingsSaveTab: a tab's form writes only its own fields - saving the
// language leaves the station and the credentials alone.
func TestSettingsSaveTab(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	src := "qrz:\n  username: keepme\nstation:\n  name: Ingo\n  qth: Bavaria\n"
	if err := os.WriteFile(cfgFile, []byte(src), 0o600); err != nil {
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
	defer st.Close()
	srv, err := New(cfg, st, events.New(), cfgFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.validateFn = func(*config.Config) (i18n.Msg, i18n.Msg) {
		t.Fatal("credentials checked on the General tab")
		return i18n.Msg{}, i18n.Msg{}
	}
	h := srv.Routes()
	r := postForm(t, h, "/settings/save", url.Values{"tab": {"general"}, "ui_language": {"de"}, "qualify_ask_phone_new": {"1"}})
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/settings/general?saved=1" {
		t.Fatalf("save = %d %q", r.Code, r.Header().Get("Location"))
	}
	got := srv.config()
	if got.UI.Language != "de" || got.QRZ.Username != "keepme" || got.Station.Name != "Ingo" {
		t.Fatalf("after saving General: %+v %+v %+v", got.UI, got.QRZ, got.Station)
	}
	r = postForm(t, h, "/settings/save", url.Values{"tab": {"printing"}, "station_name": {"Ingomar"}, "station_qth": {"Lengerich"}})
	if r.Code != http.StatusSeeOther || srv.config().Station.QTH != "Lengerich" || srv.config().UI.Language != "de" || srv.config().QRZ.Username != "keepme" {
		t.Fatalf("after saving the station: %d %+v", r.Code, srv.config())
	}
}
