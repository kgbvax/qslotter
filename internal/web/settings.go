package web

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/buildinfo"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/sync"
	"gopkg.in/yaml.v3"
)

// The settings tabs: Connections (QRZ, Clublog), Cards & printer (layouts,
// station identity, printer) and General (Inbox, language, version).
const (
	tabConnections = "connections"
	tabPrinting    = "printing"
	tabGeneral     = "general"
)

// settingsTabPath is the page of a tab.
func settingsTabPath(tab string) string {
	if tab == tabConnections {
		return "/settings"
	}
	return "/settings/" + tab
}

// settingsData is what every settings tab renders with.
func (s *Server) settingsData(r *http.Request, tab string, cfg *config.Config) map[string]any {
	lastPull, _ := s.store.MetaGet("clublog_last_pull_at")
	lastPush, _ := s.store.MetaGet("clublog_last_push_at")
	d := map[string]any{"Tab": tab, "Cfg": cfg, "NoConfig": s.cfgPath == "", "Saved": r.URL.Query().Get("saved") == "1"}
	switch tab {
	case tabConnections:
		d["ClublogPaused"], d["LastPull"], d["LastPush"] = s.clublogPausedAt(), lastPull, lastPush
	case tabPrinting:
		list, _, note := s.layouts(r)
		d["Layouts"], d["LayoutNote"] = s.layoutThumbs(list), note
		d["Printers"], d["PrintersErr"] = s.printerChoices(cfg)
		d["Media"] = s.printerMedia(cfg)
		d["OffsetX"], d["OffsetY"] = fmtMM(cfg.Printer.OffsetMM[0]), fmtMM(cfg.Printer.OffsetMM[1])
	case tabGeneral:
		d["Langs"], d["Build"], d["CanQuit"] = s.langChoices(), buildinfo.Get(), s.Quit != nil
		d["Matrix"] = filterMatrix(qualify.FilterFrom(cfg.Qualify))
	}
	return d
}

// pageSettings renders the Connections tab (QRZ and Clublog credentials).
func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings.html", s.settingsData(r, tabConnections, s.config()))
}

// pageSettingsPrinting renders the Cards & printer tab.
func (s *Server) pageSettingsPrinting(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings.html", s.settingsData(r, tabPrinting, s.config()))
}

// pageSettingsGeneral renders the General tab.
func (s *Server) pageSettingsGeneral(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings.html", s.settingsData(r, tabGeneral, s.config()))
}

// matrixRow is one row of the New QSOs filter on the General tab.
type matrixRow struct {
	Label, Hint string // English, translated in the template
	Cells       []matrixCell
}

type matrixCell struct {
	Name string // form field
	On   bool
}

// filterRowText labels the rows of the filter.
var filterRowText = map[string][2]string{
	qualify.GroupPhone:    {"Phone", "SSB, FM, AM, digital voice"},
	qualify.GroupCW:       {"CW", ""},
	qualify.GroupKeyboard: {"RTTY and other digital modes", "PSK, Olivia, SSTV, ..."},
	qualify.GroupDigital:  {"FT8, FT4 and similar", "FT2, JS8, FST4, MSK144, WSPR"},
	qualify.RowSatellite:  {"Satellite", "any mode; new band = new satellite"},
}

func filterMatrix(f qualify.Filter) []matrixRow {
	var out []matrixRow
	for ri, row := range qualify.Rows {
		mr := matrixRow{Label: filterRowText[row][0], Hint: filterRowText[row][1]}
		for ci, col := range qualify.Cols {
			mr.Cells = append(mr.Cells, matrixCell{"qualify_ask_" + row + "_" + col, f[ri][ci]})
		}
		out = append(out, mr)
	}
	return out
}

// askNode is the filter as the YAML of qualify.ask: one row per line, the
// columns that ask as a flow sequence ("phone: [new, band, repeat]").
func askNode(f qualify.Filter) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	ask := f.Ask()
	for _, row := range qualify.Rows {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, col := range ask[row] {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: col})
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: row}, seq)
	}
	return m
}

// settingsSections are the config keys each tab's form writes (tab= in the
// form); a form without a tab writes them all.
var settingsSections = map[string][]string{
	tabConnections: {"qrz.", "clublog."},
	tabPrinting:    {"station."},
	tabGeneral:     {"ui.", "qualify."},
}

// saveSettings writes the form values of one tab into the config file on
// disk (a YAML-node edit that preserves comments and unknown keys), reloads
// it as the live config and swaps the QRZ client in the shared refresher.
// The Connections tab validates both credential sets at once so the outcome
// shows up in the page (and in the log); the other tabs go back to their
// page. Sync intervals still need a restart.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfgPath == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "settings editing is disabled (no config path given at startup)")
		return
	}
	cur := s.config()
	vals := map[string]any{
		"qrz.username":         strings.TrimSpace(r.FormValue("qrz_username")),
		"qrz.password":         r.FormValue("qrz_password"),
		"clublog.email":        strings.TrimSpace(r.FormValue("clublog_email")),
		"clublog.app_password": r.FormValue("clublog_app_password"),
		"clublog.call":         strings.ToUpper(strings.TrimSpace(r.FormValue("clublog_call"))),
		"clublog.api_key":      strings.TrimSpace(r.FormValue("clublog_api_key")),
		"station.name":         strings.TrimSpace(r.FormValue("station_name")),
		"station.qth":          strings.TrimSpace(r.FormValue("station_qth")),
		"ui.language":          "",
	}
	// The New QSOs filter: one checkbox per cell of the matrix.
	var filter qualify.Filter
	for ri, row := range qualify.Rows {
		for ci, col := range qualify.Cols {
			filter[ri][ci] = r.FormValue("qualify_ask_"+row+"_"+col) == "1"
		}
	}
	vals["qualify.ask"] = askNode(filter)
	// The UI language: one with a catalog, or empty = the browser's.
	for _, l := range s.i18n.Languages() {
		if r.FormValue("ui_language") == l {
			vals["ui.language"] = l
		}
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
	tab := r.FormValue("tab")
	if prefixes, ok := settingsSections[tab]; ok {
		for k := range vals {
			if !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
				delete(vals, k)
			}
		}
	}

	if err := updateConfigFile(s.cfgPath, vals); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "saving config: %s", err.Error())
		return
	}
	newCfg, err := s.reloadConfig()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "config saved, but reloading it failed: %s", err.Error())
		return
	}

	// The New QSOs filter applies at once (UDP feed, sync and Recompute
	// share these rules). Waiting QSOs stay; a scan queues the QSOs since the
	// cutoff that the new filter lets in.
	if f := qualify.FilterFrom(newCfg.Qualify); s.rules != nil && s.rules.Filter() != f {
		s.rules.SetFilter(f)
		log.Printf("settings: qualify filter = %+v", f)
		keys, err := s.rules.EnqueueAllKeys(s.store)
		for _, k := range keys {
			s.publishQueueChanged(k, "queued")
		}
		if err != nil {
			log.Printf("settings: queue scan after a filter change: %v", err)
		}
	}

	// Live-swap the QRZ client: the shared refresher serves both the web UI
	// and the UDP listener's lookups.
	if s.refresher != nil {
		var qc *qrz.Client // nil (lookups are no-ops) until a username is set
		if newCfg.QRZ.Username != "" {
			qc = qrz.New(newCfg.QRZ.Username, newCfg.QRZ.Password, newCfg.QRZ.Agent)
		}
		s.refresher.SetClient(qc)
	}

	if tab == tabPrinting || tab == tabGeneral {
		http.Redirect(w, r, settingsTabPath(tab)+"?saved=1", http.StatusSeeOther)
		return
	}
	qrzStatus, clublogStatus := s.validateFn(newCfg)
	d := s.settingsData(r, tabConnections, newCfg)
	d["Checked"], d["QrzStatus"], d["ClublogStatus"] = true, qrzStatus, clublogStatus
	s.render(w, r, "settings.html", d)
}

// LangChoice is one entry of the language select.
type LangChoice struct{ Code, Name string }

func (s *Server) langChoices() []LangChoice {
	var out []LangChoice
	for _, l := range s.i18n.Languages() {
		out = append(out, LangChoice{Code: l, Name: s.i18n.Name(l)})
	}
	return out
}

// validateCredentials checks both services right now and returns human-ready
// status lines for the settings page. Uses short-timeout clients so the form
// round-trip stays snappy; the outcome also lands in the log. A Clublog 403
// pauses the automatic sync, a success ends a pause (sync.NoteLogin).
func (s *Server) validateCredentials(cfg *config.Config) (qrzStatus, clublogStatus i18n.Msg) {
	if cfg.QRZ.Username != "" {
		q := qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
		q.HTTP = &http.Client{Timeout: 12 * time.Second}
		if err := q.CheckCredentials(); err != nil {
			qrzStatus = i18n.M("ERROR: %s", err.Error())
			log.Printf("settings: QRZ credential check failed: %v", err)
		} else {
			qrzStatus = i18n.M("OK - logged in as %s", cfg.QRZ.Username)
			log.Printf("settings: QRZ credential check OK (%s)", cfg.QRZ.Username)
		}
	} else {
		qrzStatus = i18n.M("not configured (station info disabled)")
	}
	if cfg.Clublog.Email != "" && cfg.Clublog.APIKey != "" {
		c := s.clublogFn(cfg.Clublog)
		c.HTTP = &http.Client{Timeout: 15 * time.Second, Transport: c.HTTP.Transport}
		err := c.CheckCredentials()
		sync.NoteLogin(s.store, c, err)
		if err != nil {
			clublogStatus = i18n.M("ERROR: %s", err.Error())
			log.Printf("settings: Clublog credential check failed: %v", err)
		} else {
			clublogStatus = i18n.M("OK (%s)", cfg.Clublog.Call)
			log.Printf("settings: Clublog credential check OK (%s)", cfg.Clublog.Call)
		}
	} else {
		clublogStatus = i18n.M("not configured (pull/push disabled)")
	}
	return qrzStatus, clublogStatus
}

// reloadConfig loads the config file again and makes it the live config.
func (s *Server) reloadConfig() (*config.Config, error) {
	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, err
	}
	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	return newCfg, nil
}

// floatPair is a YAML flow sequence of two numbers ("[1.5, -0.5]"), for
// settings such as printer.offset_mm.
func floatPair(a, b float64) *yaml.Node {
	num := func(v float64) *yaml.Node {
		tag := "!!float" // a whole number reads as !!int: tagged as float, it would print the tag
		if v == math.Trunc(v) {
			tag = "!!int"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: strconv.FormatFloat(v, 'f', -1, 64)}
	}
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: []*yaml.Node{num(a), num(b)}}
}

// updateConfigNodes is updateConfigFile for values given as YAML nodes
// (e.g. floatPair).
func updateConfigNodes(path string, vals map[string]*yaml.Node) error {
	m := map[string]any{}
	for k, v := range vals {
		m[k] = v
	}
	return updateConfigFile(path, m)
}

// updateConfigFile edits section.key = value pairs in a YAML config file
// in place. It re-marshals the parsed node tree, so comments and unknown
// keys survive; string values are double-quoted so passwords that look
// like numbers or booleans stay strings on reload; bools are written as
// true/false.
func updateConfigFile(path string, vals map[string]any) error {
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

// configNode is the YAML node for a settings value: strings double-quoted
// (a password that looks like a number stays a string), bools as true/false,
// a *yaml.Node (floatPair) as given.
func configNode(section, key string, val any) (*yaml.Node, error) {
	switch v := val.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case *yaml.Node:
		return v, nil
	}
	return nil, fmt.Errorf("config value %s.%s: unsupported type %T", section, key, val)
}

// setConfigValue sets section.key to val, creating the section and the key
// when missing. The existing value node is replaced in place, so a comment on
// its line survives.
func setConfigValue(root *yaml.Node, section, key string, val any) error {
	node, err := configNode(section, key, val)
	if err != nil {
		return err
	}
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
		sec.Content = append(sec.Content, keyNode, node)
		return nil
	}
	node.LineComment, node.HeadComment, node.FootComment = valueNode.LineComment, valueNode.HeadComment, valueNode.FootComment
	*valueNode = *node
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
