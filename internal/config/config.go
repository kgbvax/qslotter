// Package config loads qslotter's YAML configuration with ${ENV} expansion.
package config

import (
	"fmt"
	"os"
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
	Qualify QualifyCfg `yaml:"qualify"`
	Store   StoreCfg   `yaml:"store"`
	UDP     UDPCfg     `yaml:"udp"`
}

type ServerCfg struct {
	Addr string `yaml:"addr"`
	// Tray shows a system-tray icon (Windows) whose menu opens the compact
	// queue / log / receive windows. Tray-less platforms ignore it.
	Tray bool `yaml:"tray"`
	// OpenCompact opens the compact queue window on startup.
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

type QualifyCfg struct {
	ExcludeModes     []string `yaml:"exclude_modes"`
	FirstContactOnly bool     `yaml:"first_contact_only"`
	OverrideMarker   string   `yaml:"override_marker"`
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
