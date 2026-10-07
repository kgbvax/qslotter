package printer

import "testing"

func TestPickPaper(t *testing.T) {
	papers := []Media{
		{Name: "A4", ID: 9, WMM: 210, HMM: 297},
		{Name: "A6", ID: 70, WMM: 105, HMM: 148},
		{Name: "QSL 140x90", ID: 260, WMM: 90.2, HMM: 139.6},
	}
	for _, c := range []struct {
		want   string
		w, h   float64
		wantID int
		ok     bool
	}{
		{"", 140, 90, 260, true},             // by size, turned, within 1 mm
		{"", 148, 105, 70, true},             // A6 by size
		{"", 150, 100, 0, false},             // no such paper
		{"a4", 140, 90, 9, true},             // by name, any case
		{" QSL 140x90 ", 140, 90, 260, true}, // trimmed
		{"70", 140, 90, 70, true},            // by number
		{"Letter", 140, 90, 0, false},        // named, missing: no fallback
	} {
		got, ok := PickPaper(papers, c.want, c.w, c.h)
		if ok != c.ok || got.ID != c.wantID {
			t.Errorf("PickPaper(%q, %g x %g) = %v %v, want ID %d %v", c.want, c.w, c.h, got, ok, c.wantID, c.ok)
		}
	}
}

func TestPickTray(t *testing.T) {
	trays := []Media{{Name: "Automatisch", ID: 7}, {Name: "Manueller Einzug", ID: 4}}
	if m, ok := PickTray(trays, "manueller einzug"); !ok || m.ID != 4 {
		t.Errorf("by name: %v %v", m, ok)
	}
	if m, ok := PickTray(trays, "7"); !ok || m.ID != 7 {
		t.Errorf("by number: %v %v", m, ok)
	}
	if _, ok := PickTray(trays, ""); ok {
		t.Error("empty name picked a tray")
	}
}

func TestCrosswise(t *testing.T) {
	if !Crosswise(Media{WMM: 90, HMM: 140}, 140, 90) {
		t.Error("wide card on tall paper is not crosswise")
	}
	if Crosswise(Media{WMM: 140, HMM: 90}, 140, 90) || Crosswise(Media{WMM: 100, HMM: 100}, 140, 90) {
		t.Error("crosswise on matching or square paper")
	}
}
