package web

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/qrz"
	"gopkg.in/yaml.v3"
)

// pageSettings renders the service-configuration form (QRZ/Clublog
// credentials, station identity).
func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, "settings.html", map[string]any{"Cfg": s.config()})
}

// saveSettings writes the form values into the config file on disk (a
// YAML-node edit that preserves comments and unknown keys), reloads it as the
// live config, swaps the QRZ client in the shared refresher, and validates
// both credential sets immediately so the outcome shows up in the page (and
// in the log). Sync intervals still need a restart.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfgPath == "" {
		http.Error(w, "settings editing is disabled (no config path given at startup)", http.StatusServiceUnavailable)
		return
	}
	cur := s.config()
	vals := map[string]string{
		"qrz.username":         strings.TrimSpace(r.FormValue("qrz_username")),
		"qrz.password":         r.FormValue("qrz_password"),
		"clublog.email":        strings.TrimSpace(r.FormValue("clublog_email")),
		"clublog.app_password": r.FormValue("clublog_app_password"),
		"clublog.call":         strings.ToUpper(strings.TrimSpace(r.FormValue("clublog_call"))),
		"clublog.api_key":      strings.TrimSpace(r.FormValue("clublog_api_key")),
		"station.name":         strings.TrimSpace(r.FormValue("station_name")),
		"station.qth":          strings.TrimSpace(r.FormValue("station_qth")),
	}
	// Empty secret fields mean "keep the stored value".
	if vals["qrz.password"] == "" {
		vals["qrz.password"] = cur.QRZ.Password
	}
	if vals["clublog.app_password"] == "" {
		vals["clublog.app_password"] = cur.Clublog.AppPassword
	}
	if vals["clublog.api_key"] == "" {
		vals["clublog.api_key"] = cur.Clublog.APIKey
	}

	if err := updateConfigFile(s.cfgPath, vals); err != nil {
		http.Error(w, "saving config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, "config saved, but reloading it failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()

	// Live-swap the QRZ client: the shared refresher serves both the web UI
	// and the UDP listener's lookups.
	if s.refresher != nil {
		var qc *qrz.Client // nil (lookups are no-ops) until a username is set
		if newCfg.QRZ.Username != "" {
			qc = qrz.New(newCfg.QRZ.Username, newCfg.QRZ.Password, newCfg.QRZ.Agent)
		}
		s.refresher.SetClient(qc)
	}

	qrzStatus, clublogStatus := s.validateFn(newCfg)
	s.render(w, "settings.html", map[string]any{
		"Cfg":           newCfg,
		"Saved":         true,
		"QrzStatus":     qrzStatus,
		"ClublogStatus": clublogStatus,
	})
}

// validateCredentials checks both services right now and returns human-ready
// status lines for the settings page. Uses short-timeout clients so the form
// round-trip stays snappy; the outcome also lands in the log.
func validateCredentials(cfg *config.Config) (qrzStatus, clublogStatus string) {
	if cfg.QRZ.Username != "" {
		q := qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
		q.HTTP = &http.Client{Timeout: 12 * time.Second}
		if err := q.CheckCredentials(); err != nil {
			qrzStatus = "ERROR: " + err.Error()
			log.Printf("settings: QRZ credential check failed: %v", err)
		} else {
			qrzStatus = "OK - logged in as " + cfg.QRZ.Username
			log.Printf("settings: QRZ credential check OK (%s)", cfg.QRZ.Username)
		}
	} else {
		qrzStatus = "not configured (station info disabled)"
	}
	if cfg.Clublog.Email != "" && cfg.Clublog.APIKey != "" {
		c := clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword, cfg.Clublog.Call, cfg.Clublog.APIKey)
		c.HTTP = &http.Client{Timeout: 15 * time.Second}
		if err := c.CheckCredentials(); err != nil {
			clublogStatus = "ERROR: " + err.Error()
			log.Printf("settings: Clublog credential check failed: %v", err)
		} else {
			clublogStatus = "OK (" + cfg.Clublog.Call + ")"
			log.Printf("settings: Clublog credential check OK (%s)", cfg.Clublog.Call)
		}
	} else {
		clublogStatus = "not configured (pull/push disabled)"
	}
	return qrzStatus, clublogStatus
}

// updateConfigFile edits section.key = value pairs in a YAML config file
// in place. It re-marshals the parsed node tree, so comments and unknown
// keys survive; written values are double-quoted so passwords that look
// like numbers or booleans stay strings on reload.
func updateConfigFile(path string, vals map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("config file is empty or not a mapping")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config root is not a mapping")
	}
	// Deterministic order for newly created sections.
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.SplitN(k, ".", 2)
		if err := setConfigValue(root, parts[0], parts[1], vals[k]); err != nil {
			return err
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

func setConfigValue(root *yaml.Node, section, key, val string) error {
	sec := mappingValue(root, section)
	if sec == nil {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}
		sec = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, keyNode, sec)
	}
	if sec.Kind != yaml.MappingNode {
		return fmt.Errorf("config section %q is not a mapping", section)
	}
	valueNode := mappingValue(sec, key)
	if valueNode == nil {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		valueNode = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
		sec.Content = append(sec.Content, keyNode, valueNode)
	}
	valueNode.SetString(val)
	valueNode.Style = yaml.DoubleQuotedStyle
	return nil
}

// mappingValue returns the value node for a mapping key, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
