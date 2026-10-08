// Package buildinfo tells which build is running: version and build time,
// stamped by scripts/release.sh via -ldflags -X; a plain `go build` falls
// back to the VCS revision Go records and the executable's file time.
package buildinfo

import (
	"os"
	"runtime/debug"
	"time"
)

// Set by scripts/release.sh:
//
//	-X github.com/dl9et/qslotter/internal/buildinfo.Version=<git describe>
//	-X github.com/dl9et/qslotter/internal/buildinfo.Date=<RFC 3339, UTC>
var (
	Version string
	Date    string
)

// Info is the running build.
type Info struct {
	Version string    // git describe, else the short VCS revision (+"-dirty"), else "dev"
	Built   time.Time // zero when unknown
}

// Get returns the running build's version and build time.
func Get() Info {
	in := Info{Version: Version}
	if t, err := time.Parse(time.RFC3339, Date); err == nil {
		in.Built = t
	}
	if in.Version == "" {
		in.Version = "dev"
		if bi, ok := debug.ReadBuildInfo(); ok {
			rev, dirty := "", false
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					rev = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if len(rev) > 7 {
				rev = rev[:7]
			}
			if rev != "" {
				in.Version = rev
				if dirty {
					in.Version += "-dirty"
				}
			}
		}
	}
	if in.Built.IsZero() {
		if exe, err := os.Executable(); err == nil {
			if st, err := os.Stat(exe); err == nil {
				in.Built = st.ModTime()
			}
		}
	}
	return in
}
