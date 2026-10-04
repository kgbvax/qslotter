package web

import (
	"strconv"
	"strings"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
)

// classifyFor reads a cached station's QRZ record with the QSL classifier
// (qsldetermine, the operator's labelling rules in qpc/LABELS.md): status,
// accepted routes, preferred route, via, contribution. It is computed from
// the stored raw fields, so a change of the rules shows up at once; the
// result is memoised per (call, fetch time, fields) because every row of
// every list asks for it. nil without station info.
func (s *Server) classifyFor(info *store.StationInfo) *qsldetermine.Result {
	if info == nil || info.NotFound {
		return nil
	}
	key := strings.Join([]string{info.Callsign, info.FetchedAt, info.QSLMgr, info.MQSL, info.EQSL, info.LoTW,
		info.Addr1, info.Addr2, strconv.Itoa(len(info.BioText))}, "|")
	s.assessMu.Lock()
	defer s.assessMu.Unlock()
	if r, ok := s.assessMemo[key]; ok {
		return r
	}
	r := qsldetermine.Determine(&qrz.Callsign{Call: info.Callsign, QSLMgr: info.QSLMgr, MQSL: info.MQSL,
		EQSL: info.EQSL, LoTW: info.LoTW, Addr1: info.Addr1, Addr2: info.Addr2, State: info.State,
		Zip: info.Zip, Country: info.Country, DXCC: info.DXCC}, info.BioText)
	if s.assessMemo == nil || len(s.assessMemo) > 2000 {
		s.assessMemo = map[string]*qsldetermine.Result{}
	}
	s.assessMemo[key] = &r
	return &r
}

// SigChip is one part of the classification in the strip.
type SigChip struct {
	Kind     string // css class suffix
	Text     i18n.Msg
	Title    i18n.Msg
	Decisive bool // the preferred route, or the status when it decides alone
}

// SigView is what the research panel (Inbox, QSO in progress, Desk card)
// shows about a station's QSL wishes: the classification as chips - status,
// accepted routes (preferred marked), via, contribution - and the route the
// Desk preselects with the classifier's reason.
type SigView struct {
	Status  string // paper, no-paper, unclear, unknown
	Chips   []SigChip
	Suggest string   // "B", "D", "M", "N" or ""
	Manager string   // callsign for "M"
	Why     i18n.Msg // the classifier's reason
	Note    i18n.Msg // why nothing is preselected
}

// sigViewFor renders a classification (nil for no station info).
func sigViewFor(r *qsldetermine.Result) *SigView {
	if r == nil {
		return nil
	}
	v := &SigView{Status: r.Status(), Suggest: r.Suggest(), Manager: r.Manager}
	why := i18n.M("%s", r.Reason)
	st := SigChip{Kind: "status-" + v.Status, Text: statusText(v.Status), Title: why, Decisive: v.Status != "paper"}
	v.Chips = append(v.Chips, st)
	pref := map[string]string{"B": "bureau", "D": "direct", "O": "oqrs"}[r.Preferred]
	for _, rt := range r.Routes() {
		c := SigChip{Kind: rt, Text: routeText(rt), Title: why, Decisive: rt == pref}
		if rt == pref {
			c.Text = map[string]i18n.Msg{"bureau": i18n.M("Bureau (preferred)"), "direct": i18n.M("Direct (preferred)"), "oqrs": i18n.M("OQRS (preferred)")}[rt]
			c.Title = i18n.M("The station prefers this route")
		}
		v.Chips = append(v.Chips, c)
	}
	if r.Manager != "" {
		v.Chips = append(v.Chips, SigChip{Kind: "manager", Text: i18n.M("via %s", r.Manager), Title: i18n.M("QSL manager or home call")})
	}
	switch r.Contribution {
	case "required":
		v.Chips = append(v.Chips, SigChip{Kind: "contribution", Text: i18n.M("asks for return postage"),
			Title: i18n.M("SAE/SASE, IRC, green stamps, money, PayPal or a fee - see the bio")})
	case "not-needed":
		v.Chips = append(v.Chips, SigChip{Kind: "no-contribution", Text: i18n.M("no return postage needed")})
	}
	v.Why = why
	switch v.Status {
	case "unknown":
		v.Note = i18n.M("QRZ says nothing about QSL cards - nothing preselected.")
	case "unclear":
		if r.Manager != "" {
			v.Note = i18n.M("Manager %s named, but no route for it.", r.Manager)
		} else {
			v.Note = i18n.M("Paper cards wanted, but no usable route (direct without a full postal address).")
		}
	case "paper":
		if v.Suggest == "" {
			v.Note = i18n.M("Cards only through OQRS - request one instead of sending a card.")
		}
	}
	return v
}

func statusText(s string) i18n.Msg {
	switch s {
	case "paper":
		return i18n.M("Paper QSL")
	case "no-paper":
		return i18n.M("No paper QSL")
	case "unclear":
		return i18n.M("Unclear")
	}
	return i18n.M("Nothing stated")
}

func routeText(r string) i18n.Msg {
	switch r {
	case "bureau":
		return i18n.M("Bureau")
	case "direct":
		return i18n.M("Direct")
	}
	return i18n.M("OQRS")
}

// routeFromAssessment is the route the Desk (and a reply to a received card)
// offers first: B and D as classified (the preferred one when stated), a
// manager as via manager - bureau when only that is named for it, else
// direct. Refusals, OQRS-only and silence preselect nothing.
func routeFromAssessment(row *QueueRow) (code, manager string) {
	manager = row.MgrPrefill
	switch row.Suggested {
	case "B", "D":
		return row.Suggested, manager
	case "M":
		if manager == "" {
			return "", manager
		}
		if row.MgrVia == "B" {
			return "MB", manager
		}
		return "MD", manager
	}
	return "", manager
}
