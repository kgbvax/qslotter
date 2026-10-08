package adif

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<FREQ:5>14.20<RST_SENT:2>59<QSL_SENT:1>N<EOR>\n"
	rec, err := NewReader(bytes.NewReader([]byte(in))).Read()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Call() != "DL1AB" {
		t.Fatalf("Call = %q", rec.Call())
	}
	tm, err := rec.QSOTime()
	if err != nil {
		t.Fatal(err)
	}
	if tm.Format("20060102") != "20240101" || tm.Format("150405") != "120000" {
		t.Fatalf("QSOTime = %v", tm)
	}
	key, err := rec.QSLKey()
	if err != nil {
		t.Fatal(err)
	}
	want := "DL1AB|20240101|120000|20M"
	if key != want {
		t.Fatalf("QSLKey = %q, want %q", key, want)
	}

	// Write it back and verify the fields survive
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(rec); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, expect := range []string{"<CALL:5>DL1AB", "<QSO_DATE:8>20240101", "<BAND:3>20m"} {
		if !bytes.Contains(buf.Bytes(), []byte(expect)) {
			t.Fatalf("write missing %q in %q", expect, out)
		}
	}
}

func TestReadAllSkipsHeader(t *testing.T) {
	in := "ADIF export header\n<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:3>AB1<BAND:3>20m<MODE:3>SSB<EOR>\n<QSO_DATE:8>20240102<TIME_ON:6>130000<CALL:3>AB2<BAND:3>40m<MODE:3>CW<EOR>\n"
	recs, err := NewReader(bytes.NewReader([]byte(in))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("ReadAll = %d recs, want 2", len(recs))
	}
	if recs[0].Call() != "AB1" || recs[1].Call() != "AB2" {
		t.Fatalf("calls = %q, %q", recs[0].Call(), recs[1].Call())
	}
}

// TestWriteFieldsOrdered: header and records keep the given field order and
// leave empty values out.
func TestWriteFieldsOrdered(t *testing.T) {
	var b strings.Builder
	w := NewWriter(&b)
	if err := w.WriteHeader("export", Field{"ADIF_VER", "3.1.4"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFields(Field{"CALL", "DL1ABC"}, Field{"FREQ", ""}, Field{"BAND", "20m"}); err != nil {
		t.Fatal(err)
	}
	want := "export\n<ADIF_VER:5>3.1.4 <EOH>\n<CALL:6>DL1ABC <BAND:3>20m <EOR>\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
	recs, err := NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil || len(recs) != 1 || recs[0].Get("BAND") != "20m" {
		t.Fatalf("read back: %v %v", recs, err)
	}
}
