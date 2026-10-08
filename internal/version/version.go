// Package version tells which build of qslotter is running: the git commit
// (stamped by the Go toolchain into every build from a checkout) and when it
// was built (set by scripts/release.sh; a plain go build shows the commit's
// time instead).
package version

import (
	"runtime/debug"
	"strings"
	"time"
)

// Built is the build time, RFC 3339, set at link time:
//
//	go build -ldflags "-X github.com/dl9et/qslotter/internal/version.Built=2026-10-06T12:00:00Z"
var Built string

// Info is the running build.
type Info struct {
	Commit   string    // short git commit, "" when unknown
	Modified bool      // built from a checkout with uncommitted changes
	Time     time.Time // when it was built, else the commit's time (zero when unknown)
	IsBuild  bool      // Time is the build time (not the commit's)
}

// Get reads the running build's information.
func Get() Info {
	var in Info
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				in.Commit = s.Value
				if len(in.Commit) > 7 {
					in.Commit = in.Commit[:7]
				}
			case "vcs.modified":
				in.Modified = s.Value == "true"
			case "vcs.time":
				in.Time, _ = time.Parse(time.RFC3339, s.Value)
			}
		}
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(Built)); err == nil {
		in.Time, in.IsBuild = t, true
	}
	return in
}

// String is a one-line description for the log, e.g.
// "97f66c3 (modified), built 2026-10-06 14:12 UTC".
func (in Info) String() string {
	s := in.Commit
	if s == "" {
		s = "unknown commit"
	}
	if in.Modified {
		s += " (modified)"
	}
	if !in.Time.IsZero() {
		if in.IsBuild {
			s += ", built "
		} else {
			s += ", committed "
		}
		s += in.Time.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	return s
}
