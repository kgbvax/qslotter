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

// TestSettingsPullButton: the Clublog section offers a pull with its status.
func TestSettingsPullButton(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := get(t, srv.Routes(), "/settings").Body.String()
	for _, want := range []string{`hx-post="/sync/pull"`, `id="settings-sync"`, "never pulled", `class="muted build-info">qslotter`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
}
