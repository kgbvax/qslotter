package web

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
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

// TestSettingsIncludeDigital: the digital-mode checkbox is written as a YAML
// bool, drops digital entries from exclude_modes (they would keep FT8 out),
// switches the shared rules at once and queues the skipped FT8 QSO.
func TestSettingsIncludeDigital(t *testing.T) {
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
	srv.validateFn = func(*config.Config) (i18n.Msg, i18n.Msg) { return i18n.M("OK - stub"), i18n.M("OK - stub") }
	h := srv.Routes()
	if srv.Rules().IncludeDigital() {
		t.Fatal("include_digital on by default")
	}

	if r := postForm(t, h, "/settings/save", url.Values{"qualify_include_digital": {"1"}}); r.Code != 200 {
		t.Fatalf("save = %d: %s", r.Code, r.Body)
	}
	onDisk, _ := os.ReadFile(cfgFile)
	if !strings.Contains(string(onDisk), "include_digital: true") {
		t.Fatalf("include_digital not written as a bool:\n%s", onDisk)
	}
	reloaded, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Qualify.IncludeDigital || strings.Join(reloaded.Qualify.ExcludeModes, ",") != "SSTV" {
		t.Fatalf("after save: include_digital=%v exclude_modes=%v", reloaded.Qualify.IncludeDigital, reloaded.Qualify.ExcludeModes)
	}
	if !srv.Rules().IncludeDigital() {
		t.Fatal("rules not switched live")
	}
	if it, err := st.QueueGet(ft8.QSLKey); err != nil || it == nil || it.Status != "queued" {
		t.Fatalf("FT8 QSO not queued after switching on: %+v %v", it, err)
	}
	r := get(t, h, "/settings")
	if !strings.Contains(r.Body.String(), `name="qualify_include_digital" value="1" checked`) {
		t.Fatal("checkbox not shown as checked")
	}

	// Unticked: off again, on disk and live.
	if r := postForm(t, h, "/settings/save", url.Values{}); r.Code != 200 {
		t.Fatalf("save = %d", r.Code)
	}
	if srv.Rules().IncludeDigital() {
		t.Fatal("rules still include digital after unticking")
	}
}
