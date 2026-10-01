package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/internal/intake"
	"github.com/dl9et/qslotter/internal/store"
)

func q(call, date, band string, rcvd bool) *store.QSO {
	x := &store.QSO{QSLKey: call + "|" + date + "|120000|" + band, Call: call, QSODate: date, TimeOn: "120000", Band: band, Mode: "SSB"}
	if rcvd {
		x.QSLRcvdLocal = sql.NullString{String: "Y", Valid: true}
	}
	return x
}

func TestPoolQSOs(t *testing.T) {
	all := []*store.QSO{
		q("DL1ABC", "20240301", "20M", false),
		q("DL2XYZ", "20240302", "40M", true),
		{QSLKey: "k", Call: "dl3foo", QSODate: "20240303", Band: "15M", QSLRcvd: "Y"},
	}
	if got := poolQSOs(all, poolUnreceived); len(got) != 1 || len(got["DL1ABC"]) != 1 {
		t.Errorf("unreceived pool: %v", got)
	}
	if got := poolQSOs(all, poolAll); len(got) != 3 || len(got["DL3FOO"]) != 1 {
		t.Errorf("all pool must upper-case calls: %v", got)
	}
}

func TestLoadExpected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expected.csv")
	csvText := "file,call,qso_date,band,call_style\n" +
		"eval/cards/IMG_1.heic, dl1abc ,2024-03-01,20m,Handwritten\n" +
		"IMG_2.heic,DL2XYZ,,,\n"
	if err := os.WriteFile(path, []byte(csvText), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := loadExpected(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := e["IMG_1.heic"]; got != (expected{Call: "DL1ABC", Date: "20240301", Band: "20M", Style: "handwritten"}) {
		t.Errorf("IMG_1: %+v", got)
	}
	if got := e["IMG_2.heic"]; got.Style != styleUnknown || got.Call != "DL2XYZ" {
		t.Errorf("IMG_2: %+v", got)
	}
	bad := filepath.Join(t.TempDir(), "bad.csv")
	_ = os.WriteFile(bad, []byte("name,callsign\nx,y\n"), 0o600)
	if _, err := loadExpected(bad); err == nil {
		t.Error("missing columns must fail")
	}
}

func page(file, engine string, lines ...string) intake.Page {
	p := intake.Page{File: file, Engine: engine}
	for _, l := range lines {
		p.Lines = append(p.Lines, intake.Line{Cands: []string{l}, Conf: 0.9})
	}
	return p
}

func TestJudgeAndSummarize(t *testing.T) {
	a := q("DL1ABC", "20240301", "20M", false)
	b := q("DL1ABC", "20230715", "40M", false)
	c := q("DL2XYZ", "20240302", "15M", false)
	byCall := poolQSOs([]*store.QSO{a, b, c}, poolAll)
	pool := intake.NewPool([]string{"DL1ABC", "DL2XYZ"})

	run := func(p intake.Page, e expected, labeled bool) cardResult {
		return judge(p, intake.Resolve(p, pool, byCall, "DL9ET"), e, labeled, byCall)
	}
	cases := []struct {
		name    string
		p       intake.Page
		e       expected
		labeled bool
		want    string
	}{
		{"auto ok", page("a.jpg", "v", "DL2XYZ"), expected{Call: "DL2XYZ", Style: "printed"}, true, outAutoOK},
		{"auto ok via confusable", page("b.jpg", "v", "DL2XY2"), expected{Call: "DL2XYZ", Style: "handwritten"}, true, outAutoOK},
		{"auto wrong", page("c.jpg", "v", "DL2XYZ"), expected{Call: "DL1ABC", Style: "printed"}, true, outAutoWrong},
		{"pick ok", page("d.jpg", "v", "DL1ABC"), expected{Call: "DL1ABC", Date: "20230715", Style: "printed"}, true, outPickOK},
		{"pick wrong: near match to another call, expected QSO not offered", page("e.jpg", "v", "DL2XY"), expected{Call: "DL1ABC", Style: "printed"}, true, outPickWrong},
		{"miss", page("f.jpg", "v", "W1AW"), expected{Call: "DL1ABC", Style: "handwritten"}, true, outMiss},
		{"not in pool", page("g.jpg", "v", "DL1ABC"), expected{Call: "DL7NOP", Style: "printed"}, true, outNotInPool},
		{"unlabeled", page("h.jpg", "v", "DL1ABC"), expected{}, false, outUnlabeled},
	}
	var results []cardResult
	for _, tc := range cases {
		r := run(tc.p, tc.e, tc.labeled)
		if r.Outcome != tc.want {
			t.Errorf("%s: outcome=%s want %s (class=%s)", tc.name, r.Outcome, tc.want, r.Class)
		}
		results = append(results, r)
	}

	tot := summarize(results)
	var all *totals
	for i := range tot {
		if tot[i].Style == "all" {
			all = &tot[i]
		}
	}
	if all == nil || all.N != 6 || all.Skipped != 2 {
		t.Fatalf("totals: %+v", tot)
	}
	// 2 auto-ok + 1 pick-ok out of 6 labeled in-pool photos.
	if all.Usable != 50 {
		t.Errorf("usable=%.1f want 50", all.Usable)
	}

	var sb strings.Builder
	writeText(&sb, results, tot)
	for _, want := range []string{"FILE", "USABLE", "handwritten", "auto-wrong"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, sb.String())
		}
	}
	sb.Reset()
	if err := writeJSONL(&sb, results, tot); err != nil || !strings.Contains(sb.String(), `"type":"total"`) {
		t.Errorf("jsonl: %v\n%s", err, sb.String())
	}
}
