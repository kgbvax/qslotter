package intake

import (
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/store"
)

// page builds a Page where each argument is one line's best reading.
func page(lines ...string) Page {
	p := Page{File: "t.jpg", Engine: "test"}
	for _, l := range lines {
		p.Lines = append(p.Lines, Line{Cands: []string{l}, Conf: 0.9})
	}
	return p
}

func qso(call, date, timeOn, band, mode string) *store.QSO {
	return &store.QSO{
		QSLKey: call + "|" + date + "|" + timeOn + "|" + band,
		Call:   call, QSODate: date, TimeOn: timeOn, Band: band, Mode: mode,
	}
}

func world(qs ...*store.QSO) (*Pool, map[string][]*store.QSO) {
	m := map[string][]*store.QSO{}
	var calls []string
	for _, q := range qs {
		m[q.Call] = append(m[q.Call], q)
		calls = append(calls, q.Call)
	}
	return NewPool(calls), m
}

func TestParseOCR(t *testing.T) {
	in := `{"file":"a.jpg","engine":"e","lines":[{"cands":["DL1ABC","DLIABC"],"conf":0.8,"box":[0.1,0.2,0.3,0.4]}]}

{"file":"b.jpg","engine":"e","lines":[]}
`
	pages, err := ParseOCR(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Lines[0].Cands[1] != "DLIABC" || pages[0].Lines[0].Box[2] != 0.3 {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	if _, err := ParseOCR(strings.NewReader("not json\n")); err == nil {
		t.Fatal("want error for bad json")
	}
}

func TestSkeletonPinned(t *testing.T) {
	// The confusable classes are part of the contract: pin them.
	pairs := [][2]string{
		{"0", "O"}, {"0", "Q"}, {"1", "I"}, {"1", "L"}, {"5", "S"},
		{"8", "B"}, {"2", "Z"}, {"6", "G"},
	}
	for _, p := range pairs {
		if Skeleton(p[0]) != Skeleton(p[1]) {
			t.Errorf("%s and %s must fold together", p[0], p[1])
		}
	}
	if Skeleton("DL1ABC") != Skeleton("DLIA8C") {
		t.Error("DL1ABC / DLIA8C should fold equal")
	}
	if Skeleton("DL1ABC") == Skeleton("DL1ABD") {
		t.Error("D and C must stay distinct")
	}
}

func TestWithin1(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"DL1ABC", "DL1ABC", true},
		{"DL1ABC", "DL1AB", true},
		{"DL1AB", "DL1ABC", true},
		{"DL1ABC", "DL1ABD", true},
		{"DL1ABC", "DL1ADD", false},
		{"DL1ABC", "DL1A", false},
		{"DL1ABC", "XL1ABD", false},
	}
	for _, c := range cases {
		if got := within1(c.a, c.b); got != c.want {
			t.Errorf("within1(%s,%s)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveCallTiers(t *testing.T) {
	pool, m := world(qso("DL1ABC", "20240301", "143000", "20M", "SSB"))
	cases := []struct {
		name  string
		lines []string
		class Class
		tier  Tier
	}{
		{"exact", []string{"To Radio DL9ET", "DL1ABC"}, ClassAuto, TierExact},
		{"I for 1", []string{"DLIABC"}, ClassAuto, TierSkeleton},
		{"8 for B", []string{"DL1A8C"}, ClassAuto, TierSkeleton},
		{"O for 0 in a call with zero", []string{"DL1ABC"}, ClassAuto, TierExact},
		{"missing char", []string{"DL1AB"}, ClassPick, TierNear},
		{"extra char", []string{"DL1ABCX"}, ClassPick, TierNear},
		{"portable suffix", []string{"DL1ABC/P"}, ClassAuto, TierExact},
		{"prefix form", []string{"EA8/DL1ABC"}, ClassAuto, TierExact},
		{"words only", []string{"CONFIRMING", "RADIO", "QSO", "73 de"}, ClassMiss, 0},
		{"unrelated call", []string{"W1AW"}, ClassMiss, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Resolve(page(c.lines...), pool, m, "DL9ET")
			if r.Class != c.class {
				t.Fatalf("class=%s want %s (hits=%+v)", r.Class, c.class, r.Hits)
			}
			if c.class != ClassMiss && r.Hits[0].Tier != c.tier {
				t.Errorf("tier=%s want %s", r.Hits[0].Tier, c.tier)
			}
		})
	}
}

func TestResolveHandwrittenZeroAndO(t *testing.T) {
	pool, m := world(qso("DL0XYZ", "20240301", "100000", "40M", "CW"))
	r := Resolve(page("DLOXYZ"), pool, m, "DL9ET")
	if r.Class != ClassAuto || r.Hits[0].Call != "DL0XYZ" {
		t.Fatalf("got %+v", r)
	}
}

func TestResolveExcludesOwnCall(t *testing.T) {
	// DL9ET is in the log (a QSO with oneself is nonsense, but own call must
	// never match) and DL9EY is one character away from the own call.
	pool, m := world(
		qso("DL9ET", "20240301", "100000", "40M", "CW"),
		qso("DL9EY", "20240301", "100000", "40M", "CW"),
	)
	r := Resolve(page("To Radio DL9ET"), pool, m, "DL9ET")
	if r.Class != ClassMiss {
		t.Fatalf("own call must not match: %+v", r.Hits)
	}
	// A card that really is from DL9EY still matches it.
	r = Resolve(page("DL9ET", "DL9EY"), pool, m, "DL9ET")
	if r.Class != ClassAuto || r.Hits[0].Call != "DL9EY" {
		t.Fatalf("got %+v", r)
	}
}

func TestResolveMultipleQSOs(t *testing.T) {
	a := qso("DL1ABC", "20240301", "143000", "20M", "SSB")
	b := qso("DL1ABC", "20230715", "090000", "40M", "CW")
	pool, m := world(a, b)

	r := Resolve(page("DL1ABC"), pool, m, "DL9ET")
	if r.Class != ClassPick || len(r.Candidates) != 2 {
		t.Fatalf("no date: want pick with 2 candidates, got %s/%d", r.Class, len(r.Candidates))
	}

	r = Resolve(page("DL1ABC", "01.03.2024", "20m SSB"), pool, m, "DL9ET")
	if r.Class != ClassAuto || r.Candidates[0].QSO != a {
		t.Fatalf("date given: want auto on a, got %s %+v", r.Class, r.Candidates)
	}

	r = Resolve(page("DL1ABC", "15 JUL 2023", "40m CW"), pool, m, "DL9ET")
	if r.Class != ClassAuto || r.Candidates[0].QSO != b {
		t.Fatalf("other date: want auto on b, got %s", r.Class)
	}

	// Date matches nothing: stays a pick list.
	r = Resolve(page("DL1ABC", "01.01.2020"), pool, m, "DL9ET")
	if r.Class != ClassPick {
		t.Fatalf("unmatched date must not auto-resolve, got %s", r.Class)
	}
}

func TestResolveTwoCallsOnCardIsPick(t *testing.T) {
	// Sender plus a QSL manager that is also in the log.
	pool, m := world(
		qso("DL1ABC", "20240301", "143000", "20M", "SSB"),
		qso("DL2XYZ", "20230101", "120000", "40M", "CW"),
	)
	r := Resolve(page("DL1ABC", "QSL via DL2XYZ"), pool, m, "DL9ET")
	if r.Class != ClassPick || len(r.Hits) != 2 {
		t.Fatalf("got %s with %d hits", r.Class, len(r.Hits))
	}
}

func TestResolveBestTierWins(t *testing.T) {
	// DL1ABD is one character from DL1ABC; the exact hit must dominate.
	pool, m := world(
		qso("DL1ABC", "20240301", "143000", "20M", "SSB"),
		qso("DL1ABD", "20240301", "143000", "20M", "SSB"),
	)
	r := Resolve(page("DL1ABC"), pool, m, "DL9ET")
	if r.Class != ClassAuto || r.Candidates[0].QSO.Call != "DL1ABC" || len(r.Candidates) != 1 {
		t.Fatalf("got %s %+v", r.Class, r.Candidates)
	}
}

func TestCallTokensKeepBestOccurrence(t *testing.T) {
	find := func(p Page, text string) Token {
		for _, tk := range CallTokens(p) {
			if tk.Text == text {
				return tk
			}
		}
		t.Fatalf("token %s not found", text)
		return Token{}
	}
	// Same rank: the more confident line wins.
	p := Page{Lines: []Line{
		{Cands: []string{"DL1ABC"}, Conf: 0.7},
		{Cands: []string{"x DL1ABC y"}, Conf: 0.9},
	}}
	if tk := find(p, "DL1ABC"); tk.Line != 1 || tk.Rank != 0 {
		t.Errorf("same rank: got %+v", tk)
	}
	// Lower rank beats higher confidence.
	p = Page{Lines: []Line{
		{Cands: []string{"XX", "DL1ABC"}, Conf: 0.99},
		{Cands: []string{"DL1ABC"}, Conf: 0.5},
	}}
	if tk := find(p, "DL1ABC"); tk.Line != 1 || tk.Rank != 0 {
		t.Errorf("rank beats conf: got %+v", tk)
	}
}

func ymd(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func hasDate(f Fields, d time.Time) bool {
	for _, x := range f.Dates {
		if x.Equal(d) {
			return true
		}
	}
	return false
}

func TestFieldsDates(t *testing.T) {
	want := ymd(2024, 3, 1)
	for _, s := range []string{
		"01.03.2024", "01.03.24", "1/3/24", "2024-03-01", "2024/03/01",
		"01 MAR 2024", "1-Mar-24", "01.MRZ.2024", "Date: 01 03 2024",
	} {
		if f := ExtractFields(page(s)); !hasDate(f, want) {
			t.Errorf("%q: dates=%v", s, f.Dates)
		}
	}
	// US reading offered as an alternative when day and month are both <= 12.
	if f := ExtractFields(page("03/01/2024")); !hasDate(f, want) || !hasDate(f, ymd(2024, 3, 1)) {
		t.Errorf("03/01/2024 should offer 1 Mar and 3 Jan: %v", f.Dates)
	}
	// Impossible dates are dropped.
	if f := ExtractFields(page("31.02.2024")); len(f.Dates) != 0 {
		t.Errorf("31.02.2024 must not parse: %v", f.Dates)
	}
	// Day, month and year in separate table cells.
	if f := ExtractFields(page("DAY", "01", "03", "2024")); !hasDate(f, want) {
		t.Errorf("cells: %v", f.Dates)
	}
}

func TestFieldsTimes(t *testing.T) {
	f := ExtractFields(page("14:32", "0915Z", "1432"))
	if len(f.Times) != 2 || f.Times[0] != 14*60+32 || f.Times[1] != 9*60+15 {
		t.Fatalf("times=%v (bare 1432 must be ignored)", f.Times)
	}
}

func TestFieldsBands(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = no band expected
	}{
		{"20m", "20M"}, {"20 M", "20M"}, {"160M", "160M"}, {"2m", "2M"},
		{"70cm", "70CM"}, {"14 MHz", "20M"}, {"14.250 MHz", "20M"},
		{"7,050 MHz", "40M"}, {"14.250", "20M"}, {"28.480", "10M"},
		{"14.07.24", ""}, {"14.07.2024", ""}, {"3.14", ""},
	}
	for _, c := range cases {
		f := ExtractFields(page(c.in))
		got := ""
		if len(f.Bands) > 0 {
			got = f.Bands[0]
		}
		if got != c.want {
			t.Errorf("%q: bands=%v want %q", c.in, f.Bands, c.want)
		}
	}
}

func TestFieldsModes(t *testing.T) {
	f := ExtractFields(page("Mode: USB", "PSK31", "cw", "I am happy", "FM"))
	got := strings.Join(f.Modes, ",")
	if got != "SSB,PSK,CW,FM" {
		t.Fatalf("modes=%s", got)
	}
	if !modeMatches("DIGI", "FT8") || modeMatches("DIGI", "SSB") || !modeMatches("USB", "SSB") {
		t.Error("modeMatches wrong")
	}
}

func TestScoreQSO(t *testing.T) {
	q := qso("DL1ABC", "20240301", "143000", "20M", "SSB")
	f := Fields{
		Dates: []time.Time{ymd(2024, 3, 2)}, // one day off: UTC vs local
		Times: []int{14*60 + 50},
		Bands: []string{"20M"},
		Modes: []string{"SSB"},
	}
	if got := ScoreQSO(q, f).Score; got != 2+1+1+1 {
		t.Errorf("score=%d want 5", got)
	}
	f.Dates = []time.Time{ymd(2024, 3, 1)}
	f.Times = []int{23*60 + 59}
	if got := ScoreQSO(q, f).Score; got != 3+1+1 {
		t.Errorf("score=%d want 5", got)
	}
}
