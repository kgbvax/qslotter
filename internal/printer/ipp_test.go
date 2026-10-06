package printer

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ippAnswer encodes an IPP response: status, then job or printer
// attributes (integers/enums as 4 bytes, the rest as strings).
func ippAnswer(status uint16, group byte, attrs ...any) []byte {
	var b bytes.Buffer
	b.Write([]byte{2, 0})
	binary.Write(&b, binary.BigEndian, status)
	binary.Write(&b, binary.BigEndian, uint32(1))
	b.WriteByte(tagOperation)
	b.WriteByte(group)
	for i := 0; i+2 < len(attrs)+1; i += 3 {
		tag, name := attrs[i].(byte), attrs[i+1].(string)
		var val []byte
		switch v := attrs[i+2].(type) {
		case int:
			val = make([]byte, 4)
			binary.BigEndian.PutUint32(val, uint32(v))
		case string:
			val = []byte(v)
		}
		b.WriteByte(tag)
		binary.Write(&b, binary.BigEndian, uint16(len(name)))
		b.WriteString(name)
		binary.Write(&b, binary.BigEndian, uint16(len(val)))
		b.Write(val)
	}
	b.WriteByte(tagEnd)
	return b.Bytes()
}

// fakeCUPS answers Get-Job-Attributes with job and Get-Printer-Attributes
// with printer, and records the operations it saw.
func fakeCUPS(t *testing.T, job, prn []byte) (*httptest.Server, *[]uint16) {
	var ops []uint16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/ipp" || len(body) < 8 {
			t.Errorf("bad IPP request")
		}
		op := binary.BigEndian.Uint16(body[2:4])
		ops = append(ops, op)
		switch op {
		case opGetJobAttributes:
			w.Write(job)
		case opGetPrinterAttribs:
			w.Write(prn)
		default:
			w.Write(ippAnswer(0, tagJob))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &ops
}

func TestIPPJobStatus(t *testing.T) {
	ctx := context.Background()
	idle := ippAnswer(0, tagPrinter, byte(tagEnum), "printer-state", 3)

	// Completed: done, not failed; the printer is not asked.
	srv, ops := fakeCUPS(t, ippAnswer(0, tagJob, byte(tagEnum), "job-state", 9,
		byte(tagKeyword), "job-state-reasons", "job-completed-successfully"), idle)
	st, err := newIPPClient(srv.URL).JobStatus(ctx, "Brother", 77)
	if err != nil || st.State != JobCompleted || st.Failed() || len(*ops) != 1 {
		t.Fatalf("completed: %+v %v ops %v", st, err, *ops)
	}

	// Aborted with a message: failed, the message is the reason.
	srv, _ = fakeCUPS(t, ippAnswer(0, tagJob, byte(tagEnum), "job-state", 8,
		byte(tagKeyword), "job-state-reasons", "aborted-by-system", byte(tagKeyword), "", "printer-stopped",
		byte(tagText), "job-printer-state-message", "Media jam"), idle)
	st, err = newIPPClient(srv.URL).JobStatus(ctx, "Brother", 78)
	if err != nil || !st.Failed() || st.Reason() != "Media jam" || len(st.Reasons) != 2 {
		t.Fatalf("aborted: %+v %v", st, err)
	}

	// Pending on a stopped queue: not failed yet, but the printer says why.
	srv, ops = fakeCUPS(t, ippAnswer(0, tagJob, byte(tagEnum), "job-state", 3),
		ippAnswer(0, tagPrinter, byte(tagEnum), "printer-state", 5, byte(tagText), "printer-state-message", "Printer is offline"))
	st, err = newIPPClient(srv.URL).JobStatus(ctx, "Brother", 79)
	if err != nil || st.Failed() || !st.PrinterStopped || st.Reason() != "Printer is offline" || len(*ops) != 2 {
		t.Fatalf("stopped: %+v %v ops %v", st, err, *ops)
	}

	// Unknown job: the IPP status is the error.
	srv, _ = fakeCUPS(t, ippAnswer(0x0406, tagJob), idle)
	if _, err := newIPPClient(srv.URL).JobStatus(ctx, "Brother", 1); err == nil || err.Error() != "ipp: status 0x0406" {
		t.Fatalf("unknown job: %v", err)
	}
	if err := newIPPClient(srv.URL).CancelJob(ctx, "Brother", 1); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestJobIDFrom(t *testing.T) {
	for out, want := range map[string]int{
		"request id is Brother_MFC_J6930DW-123 (1 file(s))\n":             123,
		"Anfrage-ID ist Brother_MFC_J6930DW-7 (1 Datei(en))\n":            7,
		"l'identifiant de requête est Brother_MFC_J6930DW-42 (1 fichier)": 42,
		"something else": 0,
	} {
		if got := jobIDFrom(out, "Brother_MFC_J6930DW"); got != want {
			t.Errorf("jobIDFrom(%q) = %d, want %d", out, got, want)
		}
	}
}
