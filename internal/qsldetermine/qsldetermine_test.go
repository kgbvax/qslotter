package qsldetermine

import (
	"strings"
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
	cs := &qrz.Callsign{Call: "DL5DIR", MQSL: "Y", Addr1: "Street 1", Addr2: "Town"}
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
		{"eQSL & LoTW", "", false}, // a list: mqsl decides,
		{"direct", "D", false},
		{"VIA BUREAU, DIRECT. LotW. SWL welcome.", "B", false},
		{"VIA BUREAU", "B", false},
		{"QRZ,EQSL,LOTW,BUREAU", "B", false},
		{"ONLY DIRECT ( SAE + $6 or 5 euro )", "D", false},
		{"LoTW - eQSL", "", false}, // a list: mqsl decides,
		{"LOTW- E-qsl also via BUREAU is OK", "B", false},
		{"LOTW, DIRECT", "D", false},
		{"LOTW and ClubLog.", "", false}, // a list: mqsl decides,
		{"DIRECT,e.QSL", "D", false},
		{"DIRECT - BUREAU - LoTW", "B", false},
		{"Bureau, eQSL", "B", false},
		{"BURO - LOTW - DIRECT -eQSL", "B", false},
		{"BUREAU- LOTW - DIRECT", "B", false},
		{"All paper QSL cards received will be 100% replied.", "", false},
		// "no bureau" means direct only while nothing refuses paper.
		{"QRZ Logbook Only (No Buro, No Direct, No QSL Manager)", "", true},
		{"LoTW, QRZ&Clublog (NO BUREAU, NO DIRECT, NO E-MAIL)", "", true},
		{"QRZ - HRDLOG - LOTW - EQSL - CLUBLOG - NO Paper NO Bureau", "", true},
		{"LoTW, QRZ.com, eQSL, Clublog. No paper or cards and no Bureau.", "", true},
		{"Only eQSL / No QSL Paper Direct or Office please", "", true},
		{"QSL VIA HAMAWARD ONLY", "", true},
		{"only QRZ and LoTW", "", true},
		{"Via LoTW, eQSL and QRZ (Only Please, )", "", true},
		{"LOTW or SASE", "D", false},
		{"Direct3$", "D", false},
		{"QSL directa", "D", false},
		{"Bureau or IRC", "B", false}, // both accepted, bureau is cheaper
		{"QSL VIA BUREAU, no SASE needed", "B", false},
		{"ONLY LoTW. No paper QSL even with green stamps", "", true},
		{"No bureau", "D", false},
		{"No bureau, SASE please", "D", false},
	}
	for _, c := range cases {
		st := &qrz.Callsign{Call: "XX1XX", QSLMgr: c.mgr}
		if c.method == "D" { // direct needs a full address (rule 5)
			st.Addr1, st.Addr2 = "Street 1", "Town"
		}
		r := Determine(st, "")
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

// A qslmgr field listing only electronic services is no refusal: mqsl and
// the postal address decide (operator, 2026-10-02).
func TestElectronicList(t *testing.T) {
	for _, c := range []struct {
		mqsl, method string
		addr, refuse bool
	}{
		{"1", "D", true, false},
		{"1", "B", false, false},
		{"0", "", true, true},
		{"", "D", true, false},
		{"", "", false, false},
	} {
		st := &qrz.Callsign{Call: "XX1XX", QSLMgr: "LoTW, eQSL, Club Log", MQSL: c.mqsl}
		if c.addr {
			st.Addr1, st.Addr2 = "Street 1", "Town"
		}
		if r := Determine(st, ""); r.Method != c.method || r.RefusePaper != c.refuse {
			t.Errorf("mqsl %q address %v: %+v", c.mqsl, c.addr, r)
		}
	}
}

func TestOQRS(t *testing.T) {
	addr := func(c *qrz.Callsign) *qrz.Callsign { c.Addr1, c.Addr2 = "Street 1", "Town"; return c }
	for _, c := range []struct {
		st     *qrz.Callsign
		bio    string
		method string
		oqrs   bool
	}{
		{&qrz.Callsign{QSLMgr: "DIRECT/OQRS/LOTW/eQSL"}, "", "D", true},
		{&qrz.Callsign{QSLMgr: "BURO, LOTW, EQSL.CC, CLUBLOG.ORG QSL REQUEST"}, "", "B", true},
		{&qrz.Callsign{QSLMgr: "clublog request / LOTW"}, "", "", true},
		// The text names a route: the address does not make it direct.
		{addr(&qrz.Callsign{MQSL: "1"}), "QSL via Club Log OQRS only.", "", true},
		// A refusal with an OQRS offered is an OQRS card.
		{&qrz.Callsign{MQSL: "0"}, "No cards needed! If you need one, pse use Clublog OQRS.", "", true},
		{&qrz.Callsign{QSLMgr: "VIA SQ2RAD OQRS CLUBLOG"}, "", "M", true},
		{&qrz.Callsign{QSLMgr: "Direct only, no OQRS"}, "", "D", false},
		{&qrz.Callsign{}, "I upload to LoTW, Club Log and QRZ. Please do not send me any email QSL requests.", "", false},
	} {
		if c.method == "D" {
			c.st.Addr1, c.st.Addr2 = "Street 1", "Town"
		}
		r := Determine(c.st, c.bio)
		if r.Method != c.method || r.OQRS != c.oqrs || r.RefusePaper {
			t.Errorf("%q / %q: %+v", c.st.QSLMgr, c.bio, r)
		}
	}
}

func TestPostalDOKAndInlineManager(t *testing.T) {
	for _, c := range []struct {
		call, mgr, bio  string
		method, manager string
		bureau, direct  bool
	}{
		{"KP4AF", "VIA EQSL ,LOTW , QRZ.COM ,BURO,IQSL, VIA MAIL.", "", "B", "", true, true},
		{"M0LOW", "VIA THE BUREAU OR TO THE ABOVE ADDRESS.", "", "B", "", true, true},
		{"LZ591MK", "via Bureau or P.O.Box 36", "", "B", "", true, true},
		{"DM1SV", "no QSL cards via Mail please", "", "", "", false, false},
		{"XX1XX", "Direct only or via e-mail", "", "D", "", false, true},
		{"DN9DPA", "O 52", "", "B", "", true, false},
		{"DL0AH", "Bureau / eQSL / DOK F01", "", "B", "", true, false},
		{"S79VU", "ALL QSL's via N4GNR Direct Only", "", "M", "N4GNR", false, true},
		{"DL0DC", "VIA BUREAU OR VIA DH3UN ADDRESS, PSE NO E-QSL", "", "M", "DH3UN", true, true},
		{"UP0L", "via bureau DL8KAC", "", "M", "DL8KAC", true, false},
		{"HA7NB", "Only Email-QSL please! NO need more paper QSL ! Tnx!", "", "", "", false, false},
		{"CN3A", "", "CN3A is qsling via BUREAU - LOTW - and direct. BURO and Direct QSL VIA IK2OHG see qrz", "M", "IK2OHG", true, true},
		{"NE1C", "kx1x", "QSL Info: logs are uploaded to LoTW. BURO OK. Direct QSLs must have SASE or will not be returned.", "M", "KX1X", true, true},
		{"IZ2ABM", "", "conferma QSO solo tramite e-qsl", "", "", false, false},
		{"JR6IQI", "JARL", "", "B", "", true, false},
		{"TM40REF", "eQSL & awards via REF server, QRZ.com 3 days, LOTW", "", "", "", false, false},
		{"TM17FFF", "F4GFE,REF BUREAU or Direct +2$", "", "M", "F4GFE", true, true},
		{"IQ5AAR", "by bureau, lotw, qrz (DIRECT through IZ5UGE)", "", "M", "IZ5UGE", true, true},
		{"DL7MDX", "LoTW, QRZ, (Bureau / direct only when other ways impossible)", "", "B", "", true, true},
		{"EW1ACG", "", "I don't use paper QSL cards", "", "", false, false},
		{"PD8D", "", "I do not send out physical QSL cards. Please don't send any QSL cards to bureau since I'm not a member.", "", "", false, false},
		{"DG1NPM", "LOTW, DCL(DARC community log, Email request", "", "B", "", true, true},
		{"RK3AW", "", "For more information please apply to e-mail. QSL is OK via burea", "B", "", true, false},
		// Routes are read only from the bio's sentences about cards.
		{"II6IARU", "", "The rules will be available directly on the HamAward website.", "", "", false, false},
		{"F8DGY", "", "Pse QSL via LOTW EQSL only OR ( exceptionally direct with self envelope for return with stamps )", "D", "", false, true},
	} {
		st := &qrz.Callsign{Call: c.call, QSLMgr: c.mgr}
		if c.direct {
			st.Addr1, st.Addr2 = "Street 1", "Town"
		}
		r := Determine(st, c.bio)
		if r.Method != c.method || r.Manager != c.manager || r.Bureau != c.bureau || r.Direct != c.direct {
			t.Errorf("%s %q %q: %+v", c.call, c.mgr, c.bio, r)
		}
	}
}

// The operator's rules of 2026-10-03.
func TestRules20261003(t *testing.T) {
	full := func(c qrz.Callsign) *qrz.Callsign { c.Addr1, c.Addr2 = "Street 1", "Town"; return &c }
	for _, c := range []struct {
		name           string
		st             *qrz.Callsign
		bio            string
		method, pref   string
		bureau, direct bool
		oqrs, unclear  bool
		refuse         bool
	}{
		// Rule 5: direct needs a full address on QRZ or in the bio.
		{"city only", &qrz.Callsign{QSLMgr: "Direct only or via e-mail", MQSL: "1", Addr2: "Hradec Kralove"}, "", "", "", false, false, false, true, false},
		{"city only, bureau too", &qrz.Callsign{QSLMgr: "Direct or Bureau", Addr2: "Afragola"}, "", "B", "", true, false, false, false, false},
		{"address in the bio", &qrz.Callsign{QSLMgr: "Direct"}, "QSL direct to the address below: Postfach 12, 1122 Vienna", "D", "", false, true, false, false, false},
		{"manager: not checked", &qrz.Callsign{QSLMgr: "QSL via EA5GL direct"}, "", "M", "", false, true, false, false, false},
		// A card sent only on an OQRS request is OQRS.
		{"no bureau, clublog request", &qrz.Callsign{QSLMgr: "clublog request / LOTW"}, "* Please do not send QSL via bureau * Please use clublog request for bureau or direct QSLs", "", "", false, false, true, false, false},
		{"direct via OQRS", full(qrz.Callsign{QSLMgr: "LoTW, Direct via OQRS, NO eQSL"}), "", "", "", false, false, true, false, false},
		{"own card or OQRS", full(qrz.Callsign{QSLMgr: "LOTW, OQRS"}), "If you want paper QSL, you can send own QSL direct, via Bureau or order via OQRS (prefer).", "B", "O", true, true, true, false, false},
		// Preferred only with two or more routes.
		{"preferred, two routes", full(qrz.Callsign{QSLMgr: "VIA BUREAU PREFERRED OR DIRECT"}), "", "B", "B", true, true, false, false, false},
		{"preferred, one route", full(qrz.Callsign{QSLMgr: "Direct preferred. Will answer any QSL card."}), "", "D", "", false, true, false, false, false},
		// "No bureau" alone with mqsl 0 names no route.
		{"no bureau, mqsl 0", full(qrz.Callsign{QSLMgr: "No Buro. Cfm qso e-QSL, LotW.", MQSL: "0"}), "", "", "", false, false, false, false, true},
		{"no bureau, mqsl 0, direct in bio", full(qrz.Callsign{QSLMgr: "LOTW,EQSL,QRZ. NO BUREAU,I AM NOT MEMBER", MQSL: "0"}), "please no send your qsl via buro, i update my log on LOTW, EQSL, CLUBLOG, last way is direct mode.", "D", "", false, true, false, false, false},
		{"no bureau, mqsl 1", full(qrz.Callsign{QSLMgr: "LOTW, eQSL, NO BUREAU", MQSL: "1"}), "", "D", "", false, true, false, false, false},
	} {
		r := Determine(c.st, c.bio)
		if r.Method != c.method || r.Preferred != c.pref || r.Bureau != c.bureau || r.Direct != c.direct || r.OQRS != c.oqrs || r.Unclear != c.unclear || r.RefusePaper != c.refuse {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

func TestManagerWithRoute(t *testing.T) {
	// A lead-in word is not a route: "QSL MGR EA5GL" names no route.
	r := Determine(&qrz.Callsign{QSLMgr: "QSL MGR EA5GL", Addr1: "Street 1", Addr2: "Town"}, "")
	if r.Method != "M" || r.Manager != "EA5GL" || strings.Contains(strings.ToLower(strings.TrimPrefix(r.Reason, "qslmgr field: ")), "direct") {
		t.Errorf("manager without route: %+v", r)
	}
	// The station's own call is not a manager; the home call of a portable call is.
	if r := Determine(&qrz.Callsign{Call: "PD3JWB", QSLMgr: "QSL via PD3JWB (bureau)"}, ""); r.Method != "B" || r.Manager != "" {
		t.Errorf("own call as manager: %+v", r)
	}
	if r := Determine(&qrz.Callsign{Call: "EA8/DL1ABC"}, "QSL via DL1ABC"); r.Method != "M" || r.Manager != "DL1ABC" {
		t.Errorf("home call: %+v", r)
	}
	// "QSL via bureau" is a route, not a manager.
	if r := Determine(&qrz.Callsign{QSLMgr: "QSL VIA BUREAU"}, ""); r.Method != "B" {
		t.Errorf("QSL VIA BUREAU: %+v", r)
	}
}

func TestQSLMgrCallsignForms(t *testing.T) {
	for mgr, want := range map[string]string{
		"K2ABC": "K2ABC", "via K2ABC": "K2ABC", "k2abc": "K2ABC",
		"K2ABC (bureau only)": "K2ABC", "DL1ABC/P,": "DL1ABC/P",
		"QSL MGR EA5GL": "EA5GL", "QSL VIA EC1DD": "EC1DD", "QSL Manager: EA7FTR": "EA7FTR",
		"PSE QSL via K2ABC direct": "K2ABC",
		"ONLY VIA EB7DX":           "EB7DX",
		"MANAGER : IZ8CLM":         "IZ8CLM",
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
		{"The direction was always 270 degrees", ""}, // not "direct"
		{"I don´t answer Paper QSL Cards anymore. Cards via Bureau will no longer be possible", ""},
	} {
		st := &qrz.Callsign{Call: "XX1XX"}
		if c.method == "D" {
			st.Addr1, st.Addr2 = "Street 1", "Town"
		}
		r := Determine(st, c.bio)
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
	// HamAward confirmations are digital only.
	if r := Determine(&qrz.Callsign{Call: "XX1XX"}, "QSL only via HAMAWARD"); !r.RefusePaper || r.Method != "" {
		t.Errorf("HamAward only must refuse paper: %+v", r)
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

func TestContribution(t *testing.T) {
	cases := []struct{ qslmgr, bio, want string }{
		{"DIRECT ONLY ( SAE + $6 )", "", "required"},
		{"", "QSL direct with SASE and 2 green stamps please.", "required"},
		{"", "Paper QSL on request: 5 EUR via PayPal for postage", "required"},
		{"", "Direct: no SASE needed, I return all cards.", "not-needed"},
		{"", "QSL via bureau or direct, IRC not necessary.", "not-needed"},
		{"", "No green stamps please, IRC only for direct QSL.", "required"},
		{"", "My new book costs $5.\nQSL via bureau.", ""},
		{"VIA BUREAU", "", ""},
		{"", "QSL free of charge via the bureau", "not-needed"},
		{"", "still love analog qsl cards...no $ or sase needed", "not-needed"},
		{"", "I do not need any kind of fees or contributions to return QSL", "not-needed"},
		{"", "Please do not send me dollars for QSL", "not-needed"},
		{"", "I will not answer to paper QSL even with green stamps", ""},
		{"", "No bureau needed, direct QSL with SASE only", "required"},
		{"", "Direct QSL: SASE required. No IRC needed.", "required"},
	}
	for _, c := range cases {
		if got := contribution(c.qslmgr, c.bio); got != c.want {
			t.Errorf("%q / %q: got %q, want %q", c.qslmgr, c.bio, got, c.want)
		}
	}
	if r := Determine(&qrz.Callsign{QSLMgr: "Direct SASE", Addr1: "Street 1", Addr2: "Town"}, ""); r.Contribution != "required" || r.Method != "D" {
		t.Errorf("Determine: %+v", r)
	}
}
