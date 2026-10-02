package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dl9et/qslotter/qpc"
)

// item is one sampled station.
type item struct {
	qpc.Station
	QRZCall   string `json:"qrz_call,omitempty"` // the record QRZ returned, if it differs from the logged call
	QSOKey    string `json:"qso_key"`
	FetchedAt string `json:"fetched_at"`
}

// goldLabel is one manual label. Records from the first labelling pass have
// only Label (the single-label scheme) and used Note for the labeller's own
// remark; normalize converts them and marks them Legacy.
type goldLabel struct {
	Call      string      `json:"call"`
	Status    qpc.Status  `json:"status,omitempty"`
	Routes    []qpc.Route `json:"routes,omitempty"`
	Preferred qpc.Route   `json:"preferred,omitempty"`
	Via       string      `json:"via,omitempty"`
	Note      string      `json:"note,omitempty"`    // the station's preferences, conditions, requirements
	Comment   string      `json:"comment,omitempty"` // the labeller's own remark
	Unsure    bool        `json:"unsure,omitempty"`
	Label     string      `json:"label,omitempty"` // single-label scheme (first pass)
	Legacy    bool        `json:"-"`
	At        string      `json:"at"`
}

func (g *goldLabel) normalize() {
	if g.Status == "" && g.Label != "" {
		g.Status, g.Routes = qpc.FromLegacyLabel(g.Label)
		g.Comment, g.Note = g.Note, ""
		g.Legacy = true
	}
}

func dirFlagDefault() string { return filepath.Join("eval", "qpc") }

// readJSONL decodes every line of path into a T. A missing file is empty.
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// appendJSONL appends v as one line to path.
func appendJSONL(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func loadDataset(path string) ([]item, error) {
	items, err := readJSONL[item](path)
	if err == nil && len(items) == 0 {
		err = fmt.Errorf("%s: no stations (run qpc-lab sample first)", path)
	}
	return items, err
}

// loadGold returns the last label per call.
func loadGold(path string) (map[string]goldLabel, error) {
	all, err := readJSONL[goldLabel](path)
	if err != nil {
		return nil, err
	}
	m := map[string]goldLabel{}
	for _, g := range all {
		g.normalize()
		m[g.Call] = g
	}
	return m, nil
}

// lastResults returns the last result per call.
func lastResults(path string) (map[string]qpc.Result, error) {
	all, err := readJSONL[qpc.Result](path)
	if err != nil {
		return nil, err
	}
	m := map[string]qpc.Result{}
	for _, r := range all {
		r.Normalize()
		m[r.Call] = r
	}
	return m, nil
}

func fileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12], nil
}
