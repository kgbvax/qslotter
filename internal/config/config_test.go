package config

import "testing"

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
