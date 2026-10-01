// Package i18n translates the user interface (VISION D3). Messages are keyed
// by their English text, so English needs no catalog and a missing
// translation falls back to English. A language is a directory under
// locales/ with any number of JSON files ({"English": "translation"}); they
// are merged, so each UI area keeps its own file. Adding a language = adding
// a directory.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed locales
var locales embed.FS

// Msg is a translatable message with format arguments: Go code builds it,
// the template translates it for the request's language. Args may themselves
// be Msgs (translated too).
type Msg struct {
	Text string
	Args []any
}

// M builds a Msg.
func M(text string, args ...any) Msg { return Msg{Text: text, Args: args} }

// IsZero reports an empty message.
func (m Msg) IsZero() bool { return m.Text == "" }

// String renders the message in English.
func (m Msg) String() string { return Default.T("en", m.Text, m.Args...) }

// Bundle holds the catalogs of all languages.
type Bundle struct {
	catalogs map[string]map[string]string // lang -> English -> translation
	names    map[string]string            // lang -> its own name ("Deutsch")
	// OnMissing, when set, is told about every message a catalog lacks (tests).
	OnMissing func(lang, text string)
}

// Default is the bundle of the embedded catalogs.
var Default = mustLoad()

func mustLoad() *Bundle {
	b, err := Load(locales)
	if err != nil {
		panic(err)
	}
	return b
}

// Load reads every locales/<lang>/*.json of fsys.
func Load(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{catalogs: map[string]map[string]string{}, names: map[string]string{"en": "English"}}
	dirs, err := fs.ReadDir(fsys, "locales")
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		lang := d.Name()
		cat := map[string]string{}
		files, err := fs.Glob(fsys, path.Join("locales", lang, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			raw, err := fs.ReadFile(fsys, f)
			if err != nil {
				return nil, err
			}
			var m map[string]string
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			for k, v := range m {
				if prev, ok := cat[k]; ok && prev != v {
					return nil, fmt.Errorf("%s: %q translated twice (%q, %q)", f, k, prev, v)
				}
				cat[k] = v
			}
		}
		b.catalogs[lang] = cat
		b.names[lang] = cat["English"] // a catalog names its language: {"English": "Deutsch"}
		if b.names[lang] == "" {
			b.names[lang] = lang
		}
	}
	return b, nil
}

// Languages lists the available languages (codes), English first.
func (b *Bundle) Languages() []string {
	out := []string{"en"}
	for l := range b.catalogs {
		if l != "en" {
			out = append(out, l)
		}
	}
	sort.Strings(out[1:])
	return out
}

// Name is a language's own name ("Deutsch").
func (b *Bundle) Name(lang string) string { return b.names[lang] }

// T translates text for lang and formats args (Msg args are translated).
func (b *Bundle) T(lang, text string, args ...any) string {
	out := text
	if lang != "en" && text != "" {
		if tr, ok := b.catalogs[lang][text]; ok && tr != "" {
			out = tr
		} else if b.OnMissing != nil {
			b.OnMissing(lang, text)
		}
	}
	if len(args) == 0 {
		return out
	}
	conv := make([]any, len(args)) // never touch the caller's Msg args
	for i, a := range args {
		if m, ok := a.(Msg); ok {
			a = b.T(lang, m.Text, m.Args...)
		}
		conv[i] = a
	}
	return fmt.Sprintf(out, conv...)
}

// Match picks the UI language: the setting when it names an available
// language, else the best match in an Accept-Language header, else English.
func (b *Bundle) Match(setting, acceptLanguage string) string {
	if setting = strings.ToLower(strings.TrimSpace(setting)); setting == "en" || b.catalogs[setting] != nil {
		return setting
	}
	type pref struct {
		lang string
		q    float64
	}
	var prefs []pref
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, q := strings.TrimSpace(part), 1.0
		if i := strings.Index(tag, ";"); i >= 0 {
			if _, err := fmt.Sscanf(strings.TrimSpace(tag[i+1:]), "q=%g", &q); err != nil {
				q = 0
			}
			tag = strings.TrimSpace(tag[:i])
		}
		if tag == "" {
			continue
		}
		base := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		prefs = append(prefs, pref{base, q})
	}
	sort.SliceStable(prefs, func(i, j int) bool { return prefs[i].q > prefs[j].q })
	for _, p := range prefs {
		if p.q > 0 && (p.lang == "en" || b.catalogs[p.lang] != nil) {
			return p.lang
		}
	}
	return "en"
}
