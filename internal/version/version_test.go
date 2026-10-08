package version

import (
	"testing"
	"time"
)

func TestString(t *testing.T) {
	in := Info{Commit: "97f66c3", Modified: true, Time: time.Date(2026, 10, 6, 14, 12, 0, 0, time.UTC), IsBuild: true}
	if got := in.String(); got != "97f66c3 (modified), built 2026-10-06 14:12 UTC" {
		t.Errorf("String = %q", got)
	}
	in.IsBuild, in.Modified = false, false
	if got := in.String(); got != "97f66c3, committed 2026-10-06 14:12 UTC" {
		t.Errorf("String = %q", got)
	}
	if got := (Info{}).String(); got != "unknown commit" {
		t.Errorf("empty String = %q", got)
	}
}

func TestBuiltOverridesCommitTime(t *testing.T) {
	old := Built
	defer func() { Built = old }()
	Built = "2026-10-06T12:00:00Z"
	if in := Get(); !in.IsBuild || !in.Time.Equal(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("Get = %+v", in)
	}
}
