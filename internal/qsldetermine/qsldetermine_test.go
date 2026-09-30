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