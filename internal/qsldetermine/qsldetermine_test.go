package qsldetermine

import (
	"testing"

	"github.com/dl9et/qslotter/internal/qrz"
)

func TestDetermineQSLMgr(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL1AB", QSLMgr: "K2ABC"}
	r := Determine(cs, "")
	if r.Method != "M" || r.Manager != "K2ABC" {
		t.Fatalf("got %+v, want M/K2ABC", r)
	}
}

func TestDetermineQSLMgrNone(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL1AB", QSLMgr: "NONE"}
	r := Determine(cs, "")
	if r.Method != "" {
		t.Fatalf("qslmgr=NONE should not give method M; got %+v", r)
	}
}

func TestDetermineBioViaBureau(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL2CD", MQSL: "Y"}
	r := Determine(cs, "QSL via bureau please. 73!")
	if r.Method != "B" {
		t.Fatalf("got Method=%q, want B", r.Method)
	}
}

func TestDetermineBioViaManager(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL3EF", MQSL: "Y"}
	r := Determine(cs, "QSL via K9XYZ please")
	if r.Method != "M" || r.Manager != "K9XYZ" {
		t.Fatalf("got %+v, want M/K9XYZ", r)
	}
}

func TestDetermineBioNoPaper(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL4NO", EQSL: "Y", MQSL: "N", LoTW: "Y"}
	r := Determine(cs, "NO PAPER QSL PLEASE. eQSL only.")
	if !r.RefusePaper {
		t.Fatal("RefusePaper should be true for 'NO PAPER QSL'")
	}
	// E is not a paper-card decision: eQSL-only stations suggest "none".
	if r.Method != "" {
		t.Fatalf("Method = %q, want empty (eQSL only maps to no paper)", r.Method)
	}
}

func TestDetermineBioDirectOnly(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL5DIR", MQSL: "Y"}
	r := Determine(cs, "QSL direct only, no bureau")
	if r.Method != "D" {
		t.Fatalf("Method = %q, want D", r.Method)
	}
}

func TestDetermineFallbackMQSL(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL6FB", MQSL: "Y"}
	r := Determine(cs, "")
	if r.Method != "B" {
		t.Fatalf("fallback for mqsl=Y should be B; got %+v", r)
	}
}

func TestDetermineNoSignal(t *testing.T) {
	cs := &qrz.Callsign{Call: "DL7NS"}
	r := Determine(cs, "")
	if r.Method != "" {
		t.Fatalf("no signal should give empty method; got %+v", r)
	}
}
func TestLooksLikeCallsign(t *testing.T) {
	for _, ok := range []string{"DL1ABC", "dl2xyz", "W1AW", "4X4AA", "K1ABC/P", "VK9/DL1ABC", "EA8/DL1ABC/P", "OK1XYZ"} {
		if !LooksLikeCallsign(ok) {
			t.Errorf("%q should look like a callsign", ok)
		}
	}
	for _, bad := range []string{"", "VIA", "LOTW", "ONLY", "DIRECT", "BUREAU", "eQSL", "ABC", "12345", "DL1ABC VIA", "via bureau"} {
		if LooksLikeCallsign(bad) {
			t.Errorf("%q should not look like a callsign", bad)
		}
	}
}

// TestQSLMgrFreeText: real qslmgr values from QRZ (qsl-eval.jsonl). Free text
// in the manager field must be read for its meaning - its first word is not a
// manager callsign.
func TestQSLMgrFreeText(t *testing.T) {
	cases := []struct {
		mgr    string
		method string
		refuse bool
	}{
		{"via eQSL, Bureau,LoTW, QRZ.com Log", "B", false},
		{"via BUREAU / eQSL/ QRZ.com / Direct / LotW / DCL /Clublog /", "B", false},
		{"eQSL LoTW QRZ. COM * DIREKT ADRES", "D", false},
		{"eQSL & LoTW", "", true},
		{"direct", "D", false},
		{"VIA BUREAU, DIRECT. LotW. SWL welcome.", "B", false},
		{"VIA BUREAU", "B", false},
		{"QRZ,EQSL,LOTW,BUREAU", "B", false},
		{"ONLY DIRECT ( SAE + $6 or 5 euro )", "D", false},
		{"LoTW - eQSL", "", true},
		{"LOTW- E-qsl also via BUREAU is OK", "B", false},
		{"LOTW, DIRECT", "D", false},
		{"LOTW and ClubLog.", "", true},
		{"DIRECT,e.QSL", "D", false},
		{"DIRECT - BUREAU - LoTW", "B", false},
		{"Bureau, eQSL", "B", false},
		{"BURO - LOTW - DIRECT -eQSL", "B", false},
		{"BUREAU- LOTW - DIRECT", "B", false},
		{"All paper QSL cards received will be 100% replied.", "", false},
	}
	for _, c := range cases {
		r := Determine(&qrz.Callsign{Call: "XX1XX", QSLMgr: c.mgr}, "")
		if r.Method == "M" {
			t.Errorf("qslmgr %q: free text became a manager: %+v", c.mgr, r)
			continue
		}
		if r.Method != c.method || r.RefusePaper != c.refuse {
			t.Errorf("qslmgr %q: got method=%q refuse=%v (%s), want %q/%v", c.mgr, r.Method, r.RefusePaper, r.Reason, c.method, c.refuse)
		}
		if r.Confidence == "high" {
			t.Errorf("qslmgr %q: free text must not be high confidence: %+v", c.mgr, r)
		}
	}
}

func TestQSLMgrCallsignForms(t *testing.T) {
	for mgr, want := range map[string]string{
		"K2ABC": "K2ABC", "via K2ABC": "K2ABC", "k2abc": "K2ABC",
		"K2ABC (bureau only)": "K2ABC", "DL1ABC/P,": "DL1ABC/P",
	} {
		r := Determine(&qrz.Callsign{QSLMgr: mgr}, "")
		if r.Method != "M" || r.Manager != want || r.Confidence != "high" {
			t.Errorf("qslmgr %q: got %+v, want M/%s/high", mgr, r, want)
		}
	}
}

func TestBioNegationsAndKeywords(t *testing.T) {
	for _, c := range []struct{ bio, method string }{
		{"qsl no bureau", "D"},      // used to read "bureau" and answer B
		{"QSL via the bureau", "B"}, // used to read "the" as a manager callsign
		{"Please QSL via bureau only", "B"},
		{"No bureau cards please, direct with SAE", "D"},
		{"73 and see you on the bands", ""},
	} {
		r := Determine(&qrz.Callsign{Call: "XX1XX"}, c.bio)
		if r.Method != c.method || r.Method == "M" {
			t.Errorf("bio %q: got %+v, want method %q", c.bio, r, c.method)
		}
	}
	// A passing mention of LoTW in a bio is not "no paper".
	if r := Determine(&qrz.Callsign{Call: "XX1XX"}, "I upload to LoTW every week. QSL via bureau is fine."); r.Method != "B" || r.RefusePaper {
		t.Errorf("bio with LoTW mention: %+v", r)
	}
	if r := Determine(&qrz.Callsign{Call: "XX1XX"}, "eQSL only, sorry"); !r.RefusePaper {
		t.Errorf("explicit eQSL only must refuse paper: %+v", r)
	}
}

// QRZ delivers mqsl/eqsl/lotw as 1/0.
func TestFlagsOneZero(t *testing.T) {
	if r := Determine(&qrz.Callsign{MQSL: "1"}, ""); r.Method != "B" {
		t.Errorf("mqsl=1: %+v", r)
	}
	if r := Determine(&qrz.Callsign{MQSL: "0", EQSL: "?", LoTW: "1"}, ""); !r.RefusePaper {
		t.Errorf("mqsl=0 lotw=1: %+v", r)
	}
	if r := Determine(&qrz.Callsign{MQSL: "0"}, ""); !r.RefusePaper || r.Method != "" {
		t.Errorf("mqsl=0 with nothing else is no paper: %+v", r)
	}
}

// The operator's rule for a record with nothing in qslmgr or the bio: a full
// postal address means direct (mqsl 1 or empty), mqsl 0 means no paper,
// without an address mqsl 1 means bureau; eqsl/lotw do not matter.
func TestAddressOnlyRecords(t *testing.T) {
	addr := func(c qrz.Callsign) *qrz.Callsign { c.Addr1, c.Addr2 = "Main St 1", "12345 Town"; return &c }
	cases := []struct {
		name   string
		cs     *qrz.Callsign
		bio    string
		method string
		refuse bool
	}{
		{"address, mqsl empty", addr(qrz.Callsign{}), "", "D", false},
		{"address, mqsl 1", addr(qrz.Callsign{MQSL: "1"}), "", "D", false},
		{"address, mqsl empty, lotw 1", addr(qrz.Callsign{LoTW: "1", EQSL: "1"}), "", "D", false},
		{"address, mqsl 0", addr(qrz.Callsign{MQSL: "0"}), "", "", true},
		{"city only, mqsl 1", &qrz.Callsign{MQSL: "1", Addr2: "Town"}, "", "B", false},
		{"city only, mqsl empty", &qrz.Callsign{Addr2: "Town"}, "", "", false},
		{"address, but the bio says bureau", addr(qrz.Callsign{MQSL: "0"}), "QSL via bureau please", "B", false},
		{"address, bio without QSL words", addr(qrz.Callsign{}), "I like CW and antennas.", "D", false},
	}
	for _, c := range cases {
		r := Determine(c.cs, c.bio)
		if r.Method != c.method || r.RefusePaper != c.refuse {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}
