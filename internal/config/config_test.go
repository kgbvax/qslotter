package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerLocalURLAndWildcard(t *testing.T) {
	for _, c := range []struct {
		addr     string
		url      string
		wildcard bool
	}{
		{"127.0.0.1:8473", "http://127.0.0.1:8473", false},
		{"192.168.1.197:8473", "http://192.168.1.197:8473", false},
		{"localhost:8473", "http://localhost:8473", false},
		{"0.0.0.0:8473", "http://127.0.0.1:8473", true},
		{":8473", "http://127.0.0.1:8473", true},
		{"[::]:8473", "http://127.0.0.1:8473", true},
		{"not-an-addr", "http://not-an-addr", false},
	} {
		s := ServerCfg{Addr: c.addr}
		if got := s.LocalURL(); got != c.url {
			t.Errorf("LocalURL(%q) = %q, want %q", c.addr, got, c.url)
		}
		if got := s.Wildcard(); got != c.wildcard {
			t.Errorf("Wildcard(%q) = %v, want %v", c.addr, got, c.wildcard)
		}
	}
}

func TestRelativePathsFollowTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("store:\n  path: data/q.db\ncard:\n  template: cards/t.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Store.Path != filepath.Join(dir, "data", "q.db") || cfg.Card.Template != filepath.Join(dir, "cards", "t.yaml") {
		t.Fatalf("paths = %q / %q", cfg.Store.Path, cfg.Card.Template)
	}
	abs := filepath.Join(dir, "elsewhere.db")
	if err := os.WriteFile(p, []byte("store:\n  path: "+abs+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := Load(p); cfg.Store.Path != abs {
		t.Fatalf("absolute path changed: %q", cfg.Store.Path)
	}
}

func TestWriteDefaultLoadsAndNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := WriteDefault(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:8473" || cfg.UI.Mode != "window" || cfg.Store.Path != filepath.Join(filepath.Dir(p), "qslotter.db") || cfg.Qualify.FirstContactOnly {
		t.Fatalf("default config = %+v", cfg)
	}
	if err := WriteDefault(p); err == nil {
		t.Fatal("WriteDefault overwrote an existing config")
	}
}

// The shipped example must load as-is.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
	if cfg.Receive.OverdueWeeks != 12 {
		t.Fatalf("receive.overdue_weeks = %d, want 12", cfg.Receive.OverdueWeeks)
	}
}

func TestReceiveOverdueDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("store:\n  path: x.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Receive.OverdueWeeks != 12 {
		t.Fatalf("default overdue weeks = %d, want 12", cfg.Receive.OverdueWeeks)
	}
}
