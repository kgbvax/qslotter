// Package adif implements a minimal ADIF reader/writer sufficient for
// Clublog round-tripping. It handles the subset of fields Clublog stores
// (QSO_DATE, TIME_ON, CALL, BAND, MODE, FREQ, RST_SENT, RST_RCVD, QSL_SENT,
// QSL_RCVD, QSLSDATE, QSLRDATE, LOTW_QSL_RCVD, DXCC, PROP_MODE, NOTES,
// GRIDSQUARE, OPERATOR) plus any extra fields encountered, preserved verbatim.
package adif

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Record is one ADIF QSO. Field names are upper-case. Multi-value preservation
// is not supported; only the last value per field name is kept, matching ADIF.
type Record map[string]string

// Reader parses ADIF records from a stream.
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

// Read returns the next record, or io.EOF when the stream is exhausted.
// It tolerates a leading header (anything before the first <FIELD:...>) by
// skipping it.
func (r *Reader) Read() (Record, error) {
	rec := Record{}
	gotAny := false
	for {
		// Skip until '<'
		for {
			b, err := r.r.ReadByte()
			if err != nil {
				if gotAny {
					return rec, nil
				}
				return nil, err
			}
			if b == '<' {
				r.r.UnreadByte()
				break
			}
		}
		// Read a tag <NAME:LEN[:TYPE]>
		tag, err := r.readTag()
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(tag.name, "EOR") {
			if gotAny {
				return rec, nil
			}
			continue
		}
		if tag.len == 0 {
			// Tag with no value (e.g. <EOR> handled above, or empty field).
			rec[strings.ToUpper(tag.name)] = ""
			gotAny = true
			continue
		}
		val := make([]byte, tag.len)
		if _, err := io.ReadFull(r.r, val); err != nil {
			return nil, fmt.Errorf("adif: short value for %s: %w", tag.name, err)
		}
		rec[strings.ToUpper(tag.name)] = string(val)
		gotAny = true
	}
}

type adifTag struct {
	name string
	len  int
	typ  string
}

func (r *Reader) readTag() (adifTag, error) {
	// Consume '<'
	if _, err := r.r.ReadByte(); err != nil {
		return adifTag{}, err
	}
	var sb strings.Builder
	for {
		b, err := r.r.ReadByte()
		if err != nil {
			return adifTag{}, err
		}
		if b == '>' {
			break
		}
		sb.WriteByte(b)
	}
	parts := strings.SplitN(sb.String(), ":", 3)
	t := adifTag{name: parts[0]}
	// Tags like <EOR> have no length component.
	if len(parts) == 1 {
		return t, nil
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return t, fmt.Errorf("adif: bad length in <%s>: %w", sb.String(), err)
	}
	t.len = n
	if len(parts) == 3 {
		t.typ = parts[2]
	}
	return t, nil
}

// ReadAll reads all remaining records.
func (r *Reader) ReadAll() ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Read()
		if err == io.EOF {
			if rec != nil {
				out = append(out, rec)
			}
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
}

// Writer emits ADIF records.
type Writer struct {
	w io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write emits one record followed by <EOR>.
func (w *Writer) Write(rec Record) error {
	for name, val := range rec {
		if strings.EqualFold(name, "EOR") {
			continue
		}
		if _, err := fmt.Fprintf(w.w, "<%s:%d>%s", strings.ToUpper(name), len(val), val); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w.w, "<EOR>\n")
	return err
}

// WriteAll writes all records.
func (w *Writer) WriteAll(recs []Record) error {
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			return err
		}
	}
	return nil
}

// --- convenience accessors ---

func (r Record) Get(field string) string { return r[strings.ToUpper(field)] }

func (r Record) Set(field, value string) { r[strings.ToUpper(field)] = value }

func (r Record) Call() string { return strings.ToUpper(r.Get("CALL")) }

// QSOTime returns the QSO start as a UTC time.Time derived from QSO_DATE
// (YYYYMMDD) and TIME_ON (HHMMSS or HHMM).
func (r Record) QSOTime() (time.Time, error) {
	d := r.Get("QSO_DATE")
	t := r.Get("TIME_ON")
	if t == "" {
		t = r.Get("TIME_OFF")
	}
	if len(d) != 8 {
		return time.Time{}, fmt.Errorf("adif: bad QSO_DATE %q", d)
	}
	if len(t) == 4 {
		t += "00"
	}
	if len(t) != 6 {
		return time.Time{}, fmt.Errorf("adif: bad TIME_ON %q", t)
	}
	return time.Parse("20060102 150405", d+" "+t)
}

// QSLKey returns the composite key used by Clublog to dedup QSOs:
// CALL | YYYYMMDD | HHMMSS | BAND, with all fields upper-cased and zero-padded.
// It is stable across round-trips.
func (r Record) QSLKey() (string, error) {
	t, err := r.QSOTime()
	if err != nil {
		return "", err
	}
	call := strings.ToUpper(r.Get("CALL"))
	band := strings.ToUpper(r.Get("BAND"))
	return fmt.Sprintf("%s|%s|%s", call, t.Format("20060102|150405"), band), nil
}

// ParseQSODate parses an ADIF QSO_DATE (YYYYMMDD) or date-time field.
func ParseQSODate(s string) (time.Time, bool) {
	if len(s) == 8 {
		t, err := time.Parse("20060102", s)
		return t, err == nil
	}
	if len(s) == 14 {
		t, err := time.Parse("20060102150405", s)
		return t, err == nil
	}
	return time.Time{}, false
}