package i18n

import (
	"testing"
	"testing/fstest"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load(fstest.MapFS{
		"locales/de/a.json": {Data: []byte(`{"English": "Deutsch", "Inbox": "Eingang", "card %d of %d": "Karte %d von %d", "via %s": "über %s", "Bureau": "Büro"}`)},
		"locales/de/b.json": {Data: []byte(`{"Desk": "Schreibtisch"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTranslate(t *testing.T) {
	b := testBundle(t)
	if got := b.T("de", "Inbox"); got != "Eingang" {
		t.Fatalf("de Inbox = %q", got)
	}
	if got := b.T("de", "Desk"); got != "Schreibtisch" {
		t.Fatalf("catalog files are merged: %q", got)
	}
	if got := b.T("de", "card %d of %d", 2, 5); got != "Karte 2 von 5" {
		t.Fatalf("format = %q", got)
	}
	if got := b.T("de", "via %s", M("Bureau")); got != "über Büro" {
		t.Fatalf("nested Msg = %q", got)
	}
	var missed []string
	b.OnMissing = func(lang, text string) { missed = append(missed, text) }
	if got := b.T("de", "Unknown"); got != "Unknown" || len(missed) != 1 {
		t.Fatalf("fallback = %q, missed %v", got, missed)
	}
	if got := b.T("en", "Inbox"); got != "Inbox" {
		t.Fatalf("en = %q", got)
	}
	if b.Name("de") != "Deutsch" || len(b.Languages()) != 2 || b.Languages()[0] != "en" {
		t.Fatalf("languages %v / %q", b.Languages(), b.Name("de"))
	}
}

func TestMatch(t *testing.T) {
	b := testBundle(t)
	for _, c := range []struct{ setting, accept, want string }{
		{"", "de-DE,de;q=0.9,en;q=0.8", "de"},
		{"", "fr-FR,fr;q=0.9,de;q=0.5", "de"},
		{"", "fr-FR,fr;q=0.9", "en"},
		{"", "en-US,de;q=0.9", "en"},
		{"en", "de-DE", "en"},
		{"de", "en-US", "de"},
		{"xx", "de", "de"},
		{"", "", "en"},
		{"", "de;q=0", "en"},
	} {
		if got := b.Match(c.setting, c.accept); got != c.want {
			t.Errorf("Match(%q, %q) = %q, want %q", c.setting, c.accept, got, c.want)
		}
	}
}

func TestEmbeddedCatalogsLoad(t *testing.T) {
	if Default.Name("de") != "Deutsch" {
		t.Fatalf("embedded de catalog: %q", Default.Name("de"))
	}
}

func TestTranslateKeepsArgs(t *testing.T) {
	b := testBundle(t)
	m := M("via %s", M("Bureau"))
	_ = b.T("de", m.Text, m.Args...)
	if got := b.T("en", m.Text, m.Args...); got != "via Bureau" {
		t.Fatalf("a translation changed the message's args: %q", got)
	}
}
