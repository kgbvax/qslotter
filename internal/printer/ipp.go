package printer

// A minimal IPP client (RFC 8010/8011) for asking the local CUPS scheduler
// how a print job is doing. lp only reports that a job was queued; whether
// the card really printed - or the printer was off, jammed, out of paper -
// shows only in the job's state. IPP answers in keywords and enums, the same
// in every system language (lpstat's text is translated on macOS).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/user"
	"strconv"
	"strings"
	"sync/atomic"
)

// JobState is an IPP job-state.
type JobState int

const (
	JobPending    JobState = 3
	JobHeld       JobState = 4
	JobProcessing JobState = 5
	JobStopped    JobState = 6
	JobCanceled   JobState = 7
	JobAborted    JobState = 8
	JobCompleted  JobState = 9
)

// Done reports a final state (canceled, aborted, completed).
func (s JobState) Done() bool { return s >= JobCanceled }

func (s JobState) String() string {
	switch s {
	case JobPending:
		return "pending"
	case JobHeld:
		return "held"
	case JobProcessing:
		return "processing"
	case JobStopped:
		return "stopped"
	case JobCanceled:
		return "canceled"
	case JobAborted:
		return "aborted"
	case JobCompleted:
		return "completed"
	}
	return "unknown"
}

// JobStatus is what CUPS says about a job and its printer.
type JobStatus struct {
	State   JobState
	Reasons []string // job-state-reasons, e.g. "job-completed-successfully", "printer-stopped"
	Message string   // job-printer-state-message or printer-state-message ("Media jam", ...)
	// PrinterStopped: the queue is paused (CUPS stops a queue whose printer
	// failed); the job would wait until someone resumes it.
	PrinterStopped bool
}

// Failed reports a job that ended without printing: canceled or aborted,
// or completed with a reason that says it did not finish.
func (st JobStatus) Failed() bool {
	if st.State == JobCanceled || st.State == JobAborted {
		return true
	}
	if st.State == JobCompleted {
		for _, r := range st.Reasons {
			if strings.HasPrefix(r, "job-completed-with-errors") || strings.Contains(r, "aborted") || strings.Contains(r, "canceled") {
				return true
			}
		}
	}
	return false
}

// Reason is the most telling text for an operator: the printer's message,
// else the reasons, else the state.
func (st JobStatus) Reason() string {
	if st.Message != "" {
		return st.Message
	}
	var rs []string
	for _, r := range st.Reasons {
		if r != "none" && r != "job-completed-successfully" {
			rs = append(rs, r)
		}
	}
	if st.PrinterStopped {
		rs = append(rs, "printer stopped")
	}
	if len(rs) > 0 {
		return strings.Join(rs, ", ")
	}
	return "job " + st.State.String()
}

// IPP operations and tags used here.
const (
	opCancelJob         = 0x0008
	opGetJobAttributes  = 0x0009
	opGetPrinterAttribs = 0x000B

	tagOperation = 0x01
	tagJob       = 0x02
	tagEnd       = 0x03
	tagPrinter   = 0x04

	tagInteger  = 0x21
	tagEnum     = 0x23
	tagText     = 0x41
	tagName     = 0x42
	tagKeyword  = 0x44
	tagURI      = 0x45
	tagCharset  = 0x47
	tagLanguage = 0x48
)

// ippClient talks IPP to CUPS at base (http://localhost:631).
type ippClient struct {
	base string
	http *http.Client
	user string
	seq  atomic.Uint32
}

func newIPPClient(base string) *ippClient {
	u := "qslotter"
	if cur, err := user.Current(); err == nil && cur.Username != "" {
		u = cur.Username
	}
	return &ippClient{base: strings.TrimRight(base, "/"), http: http.DefaultClient, user: u}
}

// ippRequest builds an IPP request.
type ippRequest struct{ buf bytes.Buffer }

func (c *ippClient) newRequest(op uint16) *ippRequest {
	r := &ippRequest{}
	r.buf.Write([]byte{2, 0}) // IPP/2.0
	_ = binary.Write(&r.buf, binary.BigEndian, op)
	_ = binary.Write(&r.buf, binary.BigEndian, c.seq.Add(1))
	r.buf.WriteByte(tagOperation)
	r.attr(tagCharset, "attributes-charset", []byte("utf-8"))
	r.attr(tagLanguage, "attributes-natural-language", []byte("en"))
	return r
}

func (r *ippRequest) attr(tag byte, name string, val []byte) {
	r.buf.WriteByte(tag)
	_ = binary.Write(&r.buf, binary.BigEndian, uint16(len(name)))
	r.buf.WriteString(name)
	_ = binary.Write(&r.buf, binary.BigEndian, uint16(len(val)))
	r.buf.Write(val)
}

func (r *ippRequest) str(tag byte, name, val string) { r.attr(tag, name, []byte(val)) }

func (r *ippRequest) int(name string, v int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(int32(v)))
	r.attr(tagInteger, name, b)
}

// keywords adds a multi-valued keyword attribute.
func (r *ippRequest) keywords(name string, vals ...string) {
	for i, v := range vals {
		n := name
		if i > 0 {
			n = "" // an additional value of the same attribute
		}
		r.str(tagKeyword, n, v)
	}
}

func (r *ippRequest) bytes() []byte {
	r.buf.WriteByte(tagEnd)
	return r.buf.Bytes()
}

// ippResponse is a parsed IPP response: the status code and the attributes
// (all groups merged; each name to its values).
type ippResponse struct {
	status uint16
	attrs  map[string][]ippValue
}

type ippValue struct {
	tag byte
	raw []byte
}

func (v ippValue) int() int {
	if len(v.raw) != 4 {
		return 0
	}
	return int(int32(binary.BigEndian.Uint32(v.raw)))
}

func (r *ippResponse) first(name string) (ippValue, bool) {
	vs := r.attrs[name]
	if len(vs) == 0 {
		return ippValue{}, false
	}
	return vs[0], true
}

func (r *ippResponse) strs(name string) []string {
	var out []string
	for _, v := range r.attrs[name] {
		out = append(out, string(v.raw))
	}
	return out
}

func parseIPP(data []byte) (*ippResponse, error) {
	if len(data) < 8 {
		return nil, errors.New("ipp: short response")
	}
	resp := &ippResponse{status: binary.BigEndian.Uint16(data[2:4]), attrs: map[string][]ippValue{}}
	p := 8
	last := ""
	for p < len(data) {
		tag := data[p]
		p++
		if tag == tagEnd {
			break
		}
		if tag < 0x10 { // the start of another attribute group
			continue
		}
		if p+2 > len(data) {
			return nil, errors.New("ipp: truncated name length")
		}
		nl := int(binary.BigEndian.Uint16(data[p:]))
		p += 2
		if p+nl+2 > len(data) {
			return nil, errors.New("ipp: truncated name")
		}
		name := string(data[p : p+nl])
		p += nl
		vl := int(binary.BigEndian.Uint16(data[p:]))
		p += 2
		if p+vl > len(data) {
			return nil, errors.New("ipp: truncated value")
		}
		if name == "" {
			name = last // an additional value
		}
		last = name
		resp.attrs[name] = append(resp.attrs[name], ippValue{tag, data[p : p+vl]})
		p += vl
	}
	return resp, nil
}

// do posts an IPP request to path and parses the answer. A status of
// 0x0400 and above is an error.
func (c *ippClient) do(ctx context.Context, path string, req *ippRequest) (*ippResponse, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(req.bytes()))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/ipp")
	res, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("ipp: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("ipp: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ipp: HTTP %d", res.StatusCode)
	}
	resp, err := parseIPP(data)
	if err != nil {
		return nil, err
	}
	if resp.status >= 0x0400 {
		return resp, fmt.Errorf("ipp: status 0x%04x", resp.status)
	}
	return resp, nil
}

func (c *ippClient) printerURI(name string) string {
	return "ipp://localhost/printers/" + url.PathEscape(name)
}

// JobStatus asks CUPS for the state of job id on printer name (and whether
// the printer's queue is stopped).
func (c *ippClient) JobStatus(ctx context.Context, name string, id int) (JobStatus, error) {
	req := c.newRequest(opGetJobAttributes)
	req.str(tagURI, "printer-uri", c.printerURI(name))
	req.int("job-id", id)
	req.str(tagName, "requesting-user-name", c.user)
	req.keywords("requested-attributes", "job-state", "job-state-reasons", "job-printer-state-message")
	resp, err := c.do(ctx, "/jobs/", req)
	if err != nil {
		return JobStatus{}, err
	}
	st := JobStatus{Reasons: resp.strs("job-state-reasons")}
	if v, ok := resp.first("job-state"); ok {
		st.State = JobState(v.int())
	}
	if m := resp.strs("job-printer-state-message"); len(m) > 0 {
		st.Message = strings.TrimSpace(m[0])
	}
	if !st.State.Done() {
		// A job waits forever in a stopped queue: say so.
		preq := c.newRequest(opGetPrinterAttribs)
		preq.str(tagURI, "printer-uri", c.printerURI(name))
		preq.keywords("requested-attributes", "printer-state", "printer-state-message")
		if pr, err := c.do(ctx, "/printers/"+url.PathEscape(name), preq); err == nil {
			if v, ok := pr.first("printer-state"); ok && v.int() == 5 { // stopped
				st.PrinterStopped = true
				if m := pr.strs("printer-state-message"); len(m) > 0 && st.Message == "" {
					st.Message = strings.TrimSpace(m[0])
				}
			}
		}
	}
	return st, nil
}

// CancelJob cancels job id on printer name.
func (c *ippClient) CancelJob(ctx context.Context, name string, id int) error {
	req := c.newRequest(opCancelJob)
	req.str(tagURI, "printer-uri", c.printerURI(name))
	req.int("job-id", id)
	req.str(tagName, "requesting-user-name", c.user)
	_, err := c.do(ctx, "/jobs/", req)
	return err
}

// jobIDFrom finds the job number in lp's answer ("request id is
// Brother-123 (1 file(s))", in any language): the number after the printer
// name and a dash. 0 when there is none.
func jobIDFrom(out, printerName string) int {
	tok := printerName + "-"
	i := strings.LastIndex(out, tok)
	if i < 0 {
		return 0
	}
	rest := out[i+len(tok):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0
	}
	return n
}
