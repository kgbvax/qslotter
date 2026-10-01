// Package config loads qslotter's YAML configuration with ${ENV} expansion.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerCfg  `yaml:"server"`
	Station StationCfg `yaml:"station"`
	Clublog ClublogCfg `yaml:"clublog"`
	QRZ     QRZCfg     `yaml:"qrz"`
	Printer PrinterCfg `yaml:"printer"`
	Card    CardCfg    `yaml:"card"`
	UI      UICfg      `yaml:"ui"`
	Qualify QualifyCfg `yaml:"qualify"`
	Receive ReceiveCfg `yaml:"receive"`
	Store   StoreCfg   `yaml:"store"`
	UDP     UDPCfg     `yaml:"udp"`
}

// LocalURL is the base URL for windows opened on this machine (tray menu,
// compact window). A wildcard bind address (0.0.0.0, ::, or no host at all,
// as in ":8473") is fine to listen on but not a destination a browser opens, so
// it maps to loopback.
func (s ServerCfg) LocalURL() string {
	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return "http://" + s.Addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Wildcard reports whether the server listens on every interface, i.e. is
// reachable from other machines on the network.
func (s ServerCfg) Wildcard() bool {
	host, _, err := net.SplitHostPort(s.Addr)
	return err == nil && (host == "" || host == "0.0.0.0" || host == "::")
}

type ServerCfg struct {
	Addr string `yaml:"addr"`
	// Tray is ignored since the desktop app (2026-10): the tray icon is
	// always there on macOS and Windows. Kept so old configs still load.
	Tray bool `yaml:"tray"`
	// OpenCompact also opens the compact decision window on startup.
	OpenCompact bool `yaml:"open_compact"`
}

// StationCfg is the operator's own station identity, printed on cards
// (my_name) and used in the handwriting view.
type StationCfg struct {
	Name string `yaml:"name"`
	QTH  string `yaml:"qth"`
}

type UDPCfg struct {
	Listen string `yaml:"listen"` // e.g. "127.0.0.1:1273"
}

type ClublogCfg struct {
	Email        string        `yaml:"email"`
	AppPassword  string        `yaml:"app_password"`
	Call         string        `yaml:"call"`
	APIKey       string        `yaml:"api_key"`
	PullInterval time.Duration `yaml:"pull_interval"` // background reconciliation pull; 0 = off
	PushInterval time.Duration `yaml:"push_interval"` // background push-back; 0 = off (default)
}

type QRZCfg struct {
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	Agent    string        `yaml:"agent"`
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

type PrinterCfg struct {
	Name        string     `yaml:"name"`
	Command     string     `yaml:"command"`
	PaperSizeMM [2]float64 `yaml:"paper_size_mm"`
}

type CardCfg struct {
	Template string `yaml:"template"`
}

// ReceiveCfg configures Incoming QSLs.
type ReceiveCfg struct {
	// OverdueWeeks marks a card requested via OQRS & co. (VISION C5) as
	// overdue when it has not arrived after this many weeks. Default 12.
	OverdueWeeks int `yaml:"overdue_weeks"`
}

type QualifyCfg struct {
	ExcludeModes []string `yaml:"exclude_modes"`
	// FirstContactOnly keeps repeat contacts out of the decision queue. Off by
	// default: repeat contacts are shown with their history instead, and the
	// operator decides.
	FirstContactOnly bool   `yaml:"first_contact_only"`
	OverrideMarker   string `yaml:"override_marker"`
	// Since limits the decision queue to QSOs on or after this date
	// (YYYY-MM-DD) so the first Clublog pull does not flood it with years of
	// history. Empty = the day qslotter first ran; "all" = no cutoff (import
	// the whole backlog).
	Since string `yaml:"since"`
}

// UICfg selects the user interface: "window" (own app window + tray,
// default), "browser" (tray + browser app windows) or "headless" (server
// only). The -ui flag overrides it.
type UICfg struct {
	Mode string `yaml:"mode"`
}

type StoreCfg struct {
	Driver string `yaml:"driver"` // "sqlite" (default, only v1 option) | "couchdb" (v2)
	Path   string `yaml:"path"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := expandEnv(string(raw))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:8473"
	}
	if cfg.QRZ.Agent == "" {
		cfg.QRZ.Agent = "qslotter/1.0"
	}
	if cfg.Receive.OverdueWeeks <= 0 {
		cfg.Receive.OverdueWeeks = 12
	}
	if cfg.Qualify.OverrideMarker == "" {
		cfg.Qualify.OverrideMarker = "QSL!"
	}
	if cfg.Printer.PaperSizeMM == [2]float64{0, 0} {
		cfg.Printer.PaperSizeMM = [2]float64{100, 74}
	}
	if cfg.UDP.Listen == "" {
		cfg.UDP.Listen = "127.0.0.1:1273"
	}
	if cfg.Store.Driver == "" {
		cfg.Store.Driver = "sqlite"
	}
	if cfg.Store.Path == "" {
		cfg.Store.Path = "qslotter.db"
	}
	// Relative paths are relative to the config file, not to the working
	// directory: an app started by double-click has an arbitrary one.
	dir := filepath.Dir(path)
	if cfg.Store.Path != ":memory:" && !filepath.IsAbs(cfg.Store.Path) {
		cfg.Store.Path = filepath.Join(dir, cfg.Store.Path)
	}
	if cfg.Card.Template != "" && !filepath.IsAbs(cfg.Card.Template) {
		cfg.Card.Template = filepath.Join(dir, cfg.Card.Template)
	}
	// Clublog.PullInterval / PushInterval: 0 (or omitted) disables the
	// respective background loop; the log-page buttons always work.
	return &cfg, nil
}

var envRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

func expandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		name := envRe.FindStringSubmatch(m)[1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return ""
	})
}

// DefaultPath is where qslotter keeps its config when none is given:
// <user config dir>/qslotter/config.yaml (%AppData% on Windows,
// ~/Library/Application Support on macOS, ~/.config on Linux).
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "qslotter", "config.yaml"), nil
}

// defaultConfig is written on the first start. Credentials stay empty: they
// are entered under Settings, which edits this file.
const defaultConfig = `# qslotter configuration - created on the first start.
# Credentials are easiest to enter in the app under Settings.
server:
    addr: 127.0.0.1:8473      # 0.0.0.0:8473 = reachable from the LAN (no login!)
    open_compact: false       # also open the compact decision window at start
station:
    name: ""                  # your name, printed on cards
    qth: ""
udp:
    listen: 127.0.0.1:1273    # Log4OM UDP: ADIF (QSO logged) and current call
clublog:
    email: ""
    app_password: ""
    call: ""
    api_key: ""
    pull_interval: 1h         # background reconciliation pull; 0s = off
    push_interval: 0s         # background push-back; 0s = off
qrz:
    username: ""
    password: ""
    cache_ttl: 168h
printer:
    name: ""                  # empty = system default
    paper_size_mm: [100, 74]
qualify:
    exclude_modes: ["FT4", "FT8", "FST4", "JS8", "WSPR", "MSK144"]
    first_contact_only: false
    override_marker: "QSL!"
    since: ""                 # "" = from the first start on, "all" = whole log
store:
    driver: sqlite
    path: qslotter.db         # relative to this file
ui:
    mode: window              # window | browser | headless
`

// WriteDefault creates a starter config at path (and its directory). It
// refuses to overwrite an existing file.
func WriteDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(defaultConfig); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
