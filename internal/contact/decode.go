// Package contact handles the QSO in progress (VISION A1b): the logger's
// "current contact" broadcast over UDP tells qslotter which station the
// operator is working before the QSO is logged, so the Inbox can show it with
// its research, and a card written during the QSO can be booked the moment
// the QSO is saved.
//
// Two broadcast forms are accepted on the same UDP port as the ADIF feed:
//   - Log4OM's CALLSIGN outbound service: the bare callsign as plain text
//     ("VU2ATN"; pinned by live capture in logger-spot-bridge, 2026-09-15);
//   - the N1MM-family <lookupinfo> XML (N1MM Logger+, DXLog): sent when the
//     operator enters a call, before the QSO is logged.
//
// Other N1MM XML (contactinfo, RadioInfo, spot) is ignored. The decoder is a
// port of logger-spot-bridge's internal/log4om and internal/n1mm.
package contact

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

// Kind classifies a UDP datagram.
type Kind int

const (
	KindUnknown Kind = iota // garbage: neither ADIF nor a contact
	KindQSO                 // an ADIF record: a logged QSO
	KindContact             // the current contact (callsign or lookupinfo)
	KindClear               // the logger's entry field was cleared
	KindIgnore              // other N1MM XML (contactinfo, RadioInfo, spot ...)
	KindPartial             // a callsign still being typed ("V", "VU2"): nothing to do
)

// Contact is the station in the logger's entry field.
type Contact struct {
	Call   string
	Band   string // as the logger sent it ("20m", "14"), "" when unknown
	Mode   string
	FreqHz int64
	Source string // "callsign" (Log4OM) or "lookupinfo" (N1MM family)
}

// Classify tells a datagram apart and decodes a contact.
func Classify(data []byte) (Kind, Contact) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return KindClear, Contact{}
	}
	if bytes.Contains(bytes.ToUpper(trimmed), []byte("<EOR>")) {
		return KindQSO, Contact{}
	}
	if trimmed[0] == '<' {
		switch strings.ToLower(rootName(trimmed)) {
		case "lookupinfo":
			c, ok := decodeLookupInfo(trimmed)
			if !ok {
				if c.Call != "" {
					return KindPartial, Contact{}
				}
				return KindUnknown, Contact{}
			}
			if c.Call == "" {
				return KindClear, Contact{}
			}
			return KindContact, c
		case "":
			// not XML: an ADIF record without <EOR> is still a QSO
			if bytes.Contains(bytes.ToUpper(trimmed), []byte("<CALL:")) {
				return KindQSO, Contact{}
			}
			return KindUnknown, Contact{}
		default:
			return KindIgnore, Contact{}
		}
	}
	call := strings.ToUpper(string(trimmed))
	if !looksLikeCallsign(call) {
		if callChars(call) {
			return KindPartial, Contact{}
		}
		return KindUnknown, Contact{}
	}
	return KindContact, Contact{Call: call, Source: "callsign"}
}

// looksLikeCallsign: 3..14 characters of [A-Z0-9/-] with at least one letter
// and a digit (or three letters: special-event calls like RAEM), so stray
// text, frequencies and partial input like "DL" do not pass.
func looksLikeCallsign(s string) bool {
	if len(s) < 3 || len(s) > 14 {
		return false
	}
	letters, digits := 0, 0
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
		case r >= '0' && r <= '9':
			digits++
		case r == '-' || r == '/':
		default:
			return false
		}
	}
	return letters > 0 && (digits > 0 || letters >= 3)
}

// callChars: only characters a callsign has (a fragment being typed).
func callChars(s string) bool {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '-') {
			return false
		}
	}
	return s != ""
}

// rootName returns the XML root element name ("" when not XML). ADIF looks
// like markup ("<CALL:5>...") but its tags never parse as an element.
func rootName(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != "" { // "<CALL:5>" parses as namespace CALL, local 5
				return ""
			}
			return t.Name.Local
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return ""
			}
		}
	}
}

// wireLookupInfo is the superset of N1MM's and DXLog's lookupinfo; unknown
// fields are ignored, missing ones stay empty.
type wireLookupInfo struct {
	Call   string `xml:"call"`
	TxFreq string `xml:"txfreq"`
	RxFreq string `xml:"rxfreq"`
	Band   string `xml:"band"`
	Mode   string `xml:"mode"`
}

func decodeLookupInfo(data []byte) (Contact, bool) {
	var w wireLookupInfo
	if err := xml.Unmarshal(data, &w); err != nil {
		return Contact{}, false
	}
	c := Contact{
		Call:   strings.ToUpper(strings.TrimSpace(w.Call)),
		Band:   strings.TrimSpace(w.Band),
		Mode:   strings.TrimSpace(w.Mode),
		Source: "lookupinfo",
	}
	for _, f := range []string{w.TxFreq, w.RxFreq} {
		// N1MM-family frequencies are in 10 Hz units
		if n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && n > 0 {
			c.FreqHz = n * 10
			break
		}
	}
	if c.Call != "" && !looksLikeCallsign(c.Call) {
		return Contact{Call: c.Call}, false // a call still being typed
	}
	return c, true
}
