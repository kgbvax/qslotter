package qsldetermine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type corpusRec struct {
	ID          string `json:"id"`
	QSLMgr      string `json:"qslmgr"`
	MQSL        string `json:"mqsl"`
	EQSL        string `json:"eqsl"`
	LoTW        string `json:"lotw"`
	Bio         string `json:"bio"`
	WantSuggest string `json:"want_suggest"`
	WantManager string `json:"want_manager"`
	WantNote    Note   `json:"want_note"`
}

// TestAssessCorpus runs the records of real QRZ entries (callsigns and personal
// text removed; only what they say about QSL) through Assess.
func TestAssessCorpus(t *testing.T) {
	b, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var recs []corpusRec
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		a := Assess(Input{Call: "XX1XX", QSLMgr: r.QSLMgr, MQSL: r.MQSL, EQSL: r.EQSL, LoTW: r.LoTW, Bio: r.Bio})
		if a.Suggest != r.WantSuggest || a.Manager != r.WantManager || (r.WantNote != "" && a.Note != r.WantNote) {
			t.Errorf("%s (%q): got suggest=%q manager=%q note=%q, want suggest=%q manager=%q note=%q\n%+v",
				r.ID, r.QSLMgr, a.Suggest, a.Manager, a.Note, r.WantSuggest, r.WantManager, r.WantNote, a.Signals)
		}
		if a.Suggest != "" && len(a.Decisive()) == 0 {
			t.Errorf("%s: a suggestion without decisive signals", r.ID)
		}
	}
}

func kinds(a Assessment) string {
	var ks []string
	for _, s := range a.Signals {
		if s.Kind != KindFlag {
			ks = append(ks, string(s.Kind)+"@"+s.Source)
		}
	}
	return strings.Join(ks, " ")
}

func TestAssessCases(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string // suggest, then ":" manager, then "/" via
		note Note
	}{
		{"screenshot: eQSL/LoTW listed next to QSL Card", Input{QSLMgr: "LOTW, QRZ, EQSL, QSL Card", MQSL: "1", EQSL: "1", LoTW: "1"}, "", NotePaperNoRoute},
		{"paper word beats the electronic list", Input{QSLMgr: "LOTW, paper QSL welcome"}, "", NotePaperNoRoute},
		{"eQSL only is a stated refusal", Input{QSLMgr: "eQSL only"}, "N", ""},
		{"LoTW only is a stated refusal", Input{QSLMgr: "LoTW ONLY"}, "N", ""},
		{"electronic list alone states nothing", Input{QSLMgr: "LoTW - eQSL", MQSL: "0"}, "", NoteElectronicOnly},
		{"flags are never a route", Input{MQSL: "1", EQSL: "1", LoTW: "1"}, "", NoteNothing},
		{"mqsl 0 is not a refusal", Input{MQSL: "0", EQSL: "1", LoTW: "1"}, "", NoteNothing},
		{"directional is not direct", Input{Bio: "I run a directional beam and a direct conversion receiver. QSL cards welcome."}, "", NotePaperNoRoute},
		{"bureaucracy is not bureau", Input{QSLMgr: "bureaucracy"}, "", NoteNothing},
		{"refusal with a stated route in the bio: route wins", Input{Bio: "No QSL needed for eQSL. Paper via bureau welcome."}, "B", ""},
		{"scoped refusal never decides", Input{Bio: "No paper QSL for FT8."}, "", NoteScopedRefusal},
		{"plain refusal in the bio", Input{Bio: "Please no paper QSL."}, "N", ""},
		{"German refusal", Input{Bio: "Keine QSL-Karten bitte."}, "N", ""},
		{"manager callsign", Input{QSLMgr: "K2ABC"}, "M:K2ABC", ""},
		{"via manager", Input{QSLMgr: "via K2ABC"}, "M:K2ABC", ""},
		{"QSL via manager in the field", Input{QSLMgr: "QSL via K2ABC"}, "M:K2ABC", ""},
		{"Manager: field", Input{QSLMgr: "Manager: K2ABC"}, "M:K2ABC", ""},
		{"manager with bureau only", Input{QSLMgr: "K2ABC (bureau only)"}, "M:K2ABC/B", ""},
		{"manager with direct", Input{QSLMgr: "K2ABC direct only"}, "M:K2ABC/D", ""},
		{"portable manager", Input{QSLMgr: "DL1ABC/P,"}, "M:DL1ABC/P", ""},
		{"own call is not a manager", Input{Call: "K2ABC", QSLMgr: "K2ABC"}, "", ""},
		{"negated manager", Input{QSLMgr: "not via K2ABC"}, "", ""},
		{"bio manager", Input{Bio: "QSL via K9XYZ."}, "M:K9XYZ", ""},
		{"bio manager for one call only", Input{Bio: "QSL via DL1XYZ for TX0AT."}, "", ""},
		{"bio manager naming another call", Input{Call: "TX0AT", Bio: "QSL via DL1XYZ for TX0AT only"}, "", ""},
		{"lone no-bureau suggests nothing", Input{QSLMgr: "no bureau"}, "", ""},
		{"direct with no bureau", Input{QSLMgr: "direct, no bureau"}, "D", ""},
		{"no direct, bureau", Input{QSLMgr: "bureau, no direct"}, "B", ""},
		{"no QSL via bureau is a bureau refusal, not a paper refusal", Input{QSLMgr: "no QSL via bureau, direct"}, "D", ""},
		{"no bureau and no direct", Input{QSLMgr: "no bureau, no direct"}, "N", ""},
		{"NO MONEY does not negate direct", Input{Bio: "Direct QSL: Please, NO MONEY - NO IRC - NO $$$"}, "D", ""},
		{"direct is not accepted", Input{QSLMgr: "direct is not accepted, bureau"}, "B", ""},
		{"both stated: bureau, shown as a tie-break", Input{QSLMgr: "BUREAU / DIRECT"}, "B", ""},
		{"bureau only", Input{QSLMgr: "Bureau only"}, "B", ""},
		{"German direct", Input{QSLMgr: "nur direkt"}, "D", ""},
		{"German bureau", Input{QSLMgr: "QSL über Büro"}, "B", ""},
		{"Spanish direct", Input{QSLMgr: "solo directo"}, "D", ""},
		{"Spanish bureau accent", Input{QSLMgr: "vía buró"}, "B", ""},
		{"Italian direct", Input{QSLMgr: "diretto"}, "D", ""},
		{"French", Input{QSLMgr: "pas de QSL"}, "N", ""},
		{"Convention Buro is not a QSL statement", Input{Bio: "Convention Buro at the fair."}, "", NoteNothing},
		{"award sentence is not an electronic-only statement", Input{Bio: "The award is handled exclusively via the DCL system."}, "", NoteNothing},
		{"bio electronic only needs QSL context", Input{Bio: "I log with LoTW only."}, "", NoteNothing},
		{"bio eQSL only QSL", Input{Bio: "Confirmations: eQSL only."}, "N", ""},
		{"qslmgr beats bio", Input{QSLMgr: "BUREAU", Bio: "QSL direct only."}, "B", ""},
		{"bio is read when the field is silent", Input{QSLMgr: "LoTW", Bio: "QSL via bureau."}, "B", ""},
		{"route in the field, refusal in the bio: conflict", Input{QSLMgr: "direct", Bio: "No paper QSL."}, "", NoteConflict},
		{"refusal in the field, route in the bio: conflict", Input{QSLMgr: "no QSL", Bio: "QSL via bureau."}, "", NoteConflict},
		{"CSS leftovers are ignored", Input{Bio: "#biodata {\n  direction: rtl;\n}\n.x { direct: 1 }\nQSL via bureau."}, "B", ""},
		{"entities", Input{Bio: "QSL &amp; eQSL: bureau OK"}, "B", ""},
		{"NONE is empty", Input{QSLMgr: "NONE"}, "", NoteNothing},
		{"OQRS is a fact, not a route", Input{QSLMgr: "OQRS"}, "", NoteNothing},
	}
	for _, c := range cases {
		a := Assess(c.in)
		got := a.Suggest
		if a.Manager != "" {
			got += ":" + a.Manager
		}
		if a.ManagerVia != "" {
			got += "/" + a.ManagerVia
		}
		if got != c.want || (c.note != "" && a.Note != c.note) {
			t.Errorf("%s: got %q (note %q), want %q (note %q)\nsignals: %s", c.name, got, a.Note, c.want, c.note, kinds(a))
		}
	}
}

func TestAssessBothIsMarked(t *testing.T) {
	a := Assess(Input{QSLMgr: "BUREAU, DIRECT"})
	if a.Suggest != "B" || !a.Both || len(a.Decisive()) != 2 {
		t.Fatalf("both stated: %+v", a)
	}
	if a := Assess(Input{QSLMgr: "BUREAU"}); a.Both {
		t.Fatalf("one route is not a tie-break: %+v", a)
	}
}

func TestAssessSignalsStayVisible(t *testing.T) {
	a := Assess(Input{QSLMgr: "LOTW, QRZ, EQSL, QSL Card", MQSL: "1", EQSL: "1", LoTW: "1"})
	var el, paper, flags int
	for _, s := range a.Signals {
		switch s.Kind {
		case KindElectronic:
			el++
		case KindAcceptsPaper:
			paper++
		case KindFlag:
			flags++
		}
		if s.Decisive {
			t.Errorf("nothing is decisive here: %+v", s)
		}
	}
	if el != 3 || paper != 1 || flags != 3 {
		t.Fatalf("electronic=%d paper=%d flags=%d: %s", el, paper, flags, kinds(a))
	}
}

// Assess must survive whatever a bio contains.
func FuzzAssess(f *testing.F) {
	for _, s := range []string{"", "no", "no no no", "via", "via via", "QSL via", "(((", "e.qsl e-qsl e qsl", "\x00\xff", "only only only direct"} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, field, bio string) {
		_ = Assess(Input{Call: "DL1ABC", QSLMgr: field, Bio: bio})
	})
}

func BenchmarkAssess(b *testing.B) {
	// a typical bio: mostly about antennas, one QSL sentence
	bio := strings.Repeat("I enjoy ragchewing on 40m with a dipole and a tuner on the roof in all weather. ", 120) + "QSL via bureau welcome, direct with SAE also fine."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Assess(Input{Call: "DL1ABC", QSLMgr: "LOTW, QRZ, EQSL", MQSL: "1", Bio: bio})
	}
}
