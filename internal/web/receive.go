package web

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/store"
)

// Incoming QSLs (VISION 2.3): book a received card with as few keys as
// possible (C1), see whether it needs a reply (C2), answer it right there -
// written now, printed, or later via the Desk (C3) - and keep track of the
// cards requested via OQRS & co. (C5).

// rcvdLimit bounds the station's QSOs listed for booking (newest first); the
// page says when there are more.
const rcvdLimit = 300

// ExpectedCard is one request made at the Desk (B4b) whose card has not
// arrived: the QSOs requested together, with when and how.
type ExpectedCard struct {
	Call        string
	QSOs        []*store.QSO // oldest first
	RequestedAt time.Time
	Channel     string
	Note        string
	Overdue     bool
}

// expectedCards lists the open requests, oldest request first. Requests are
// told apart by call, time, channel and note (one Desk action stamps all QSOs
// of its card alike), so a second request to a station never hides an
// overdue first one.
func (s *Server) expectedCards() ([]*ExpectedCard, error) {
	items, err := s.store.QueueList("requested")
	if err != nil {
		return nil, err
	}
	overdue := time.Duration(s.config().Receive.OverdueWeeks) * 7 * 24 * time.Hour
	var out []*ExpectedCard
	byRequest := map[string]*ExpectedCard{}
	for _, it := range items {
		q, err := s.store.GetQSO(it.QSLKey)
		if err != nil || q == nil {
			continue
		}
		if rcvd, _ := q.EffectiveRcvd(); rcvd {
			continue // arrived
		}
		call := strings.ToUpper(q.Call)
		id := call + "|" + it.SentAt.String + "|" + it.Channel + "|" + it.Note
		e := byRequest[id]
		if e == nil {
			e = &ExpectedCard{Call: call, Channel: it.Channel, Note: it.Note}
			if it.SentAt.Valid {
				e.RequestedAt, _ = time.Parse(time.RFC3339, it.SentAt.String)
			}
			byRequest[id] = e
			out = append(out, e)
		}
		e.QSOs = append([]*store.QSO{q}, e.QSOs...)
	}
	for _, e := range out {
		e.Overdue = overdue > 0 && !e.RequestedAt.IsZero() && s.now().Sub(e.RequestedAt) > overdue
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out, nil
}

// RcvdLine is one QSO with the looked-up station and where both cards stand.
type RcvdLine struct {
	QSO      *store.QSO
	Received bool     // their card is on record
	RcvdDate string   // YYYYMMDD
	Status   string   // queue status ("" = never queued)
	Yours    i18n.Msg // where your card stands, spelled out
	Reply    i18n.Msg // zero = a reply is due (or the QSO is at the Desk); else why none is needed
	AtDesk   bool     // a card for it is already at the Desk: it goes on the reply card
	Checked  bool     // preselected for booking
}

// rcvdLines describes the station's QSOs for booking (base-call aware: a
// portable call finds the home call's QSOs and vice versa, C1).
func (s *Server) rcvdLines(call string) (lines []*RcvdLine, truncated bool, err error) {
	hist, err := s.store.CallHistory(call, rcvdLimit)
	if err != nil {
		return nil, false, err
	}
	overdue := time.Duration(s.config().Receive.OverdueWeeks) * 7 * 24 * time.Hour
	for _, h := range hist {
		l := &RcvdLine{QSO: h.QSO, Status: h.QueueStatus}
		l.Received, l.RcvdDate = h.QSO.EffectiveRcvd()
		sent, method, date := h.QSO.EffectiveSent()
		switch {
		case h.QueueStatus == "requested":
			it, _ := s.store.QueueGet(h.QSO.QSLKey)
			l.Yours = i18n.M("their card requested")
			if it != nil {
				l.Yours = i18n.M("their card requested via %s", i18n.M(it.Channel))
				if it.SentAt.Valid {
					if t, err := time.Parse(time.RFC3339, it.SentAt.String); err == nil {
						l.Yours = i18n.M("their card requested via %s on %s", i18n.M(it.Channel), t.Format("2006-01-02"))
						if overdue > 0 && s.now().Sub(t) > overdue && !l.Received {
							l.Yours = i18n.M("their card requested via %s on %s - overdue", i18n.M(it.Channel), t.Format("2006-01-02"))
						}
					}
				}
			}
			l.Reply = i18n.M("%s - nothing to send back", l.Yours)
		case sent || h.QueueStatus == "sent":
			if !sent { // pushed, and Clublog's state changed since: the queue knows
				if it, _ := s.store.QueueGet(h.QSO.QSLKey); it != nil {
					method = it.SendVia
					if method == "" && it.DesiredMethod != "W" {
						method = it.DesiredMethod
					}
					if it.SentAt.Valid && len(it.SentAt.String) >= 10 {
						date = strings.ReplaceAll(it.SentAt.String[:10], "-", "")
					}
				}
			}
			l.Yours = sentMsg(date, method, h.DesiredMethod, h.Manager)
			l.Reply = i18n.M("your card went out (%s)", l.Yours)
		case h.QueueStatus == "decided":
			l.Yours, l.AtDesk = i18n.M("at the Desk"), true
		case h.QueueStatus == "queued":
			l.Yours = i18n.M("in New QSOs")
		case h.QueueStatus == "skipped":
			l.Yours = i18n.M("no card decided")
		}
		lines = append(lines, l)
	}
	preselect(lines, call)
	return lines, len(hist) == rcvdLimit, nil
}

// preselect ticks what the card most likely confirms, never another
// station's QSO: an open request with the typed call (an expected card
// arrived), else open requests that all share one call, else - when nothing
// was requested - the only open QSO of the station.
func preselect(lines []*RcvdLine, call string) {
	var open, req, reqExact []*RcvdLine
	for _, l := range lines {
		if l.Received || store.BaseCall(l.QSO.Call) != store.BaseCall(call) {
			continue
		}
		open = append(open, l)
		if l.Status == "requested" {
			req = append(req, l)
			if strings.EqualFold(l.QSO.Call, call) {
				reqExact = append(reqExact, l)
			}
		}
	}
	tick := func(ls []*RcvdLine) {
		for _, l := range ls {
			l.Checked = true
		}
	}
	switch {
	case len(reqExact) > 0:
		tick(reqExact)
	case len(req) > 0:
		for _, l := range req {
			if !strings.EqualFold(l.QSO.Call, req[0].QSO.Call) {
				return // requests under several calls: the operator picks
			}
		}
		tick(req)
	case len(open) == 1:
		tick(open)
	}
}

// prefetchStations starts QRZ lookups for the calls a reply may need, so the
// reply panel has the station's data (QSOs never queued were never looked up).
func (s *Server) prefetchStations(lines []*RcvdLine) {
	if s.refresher == nil {
		return
	}
	seen := map[string]bool{}
	for _, l := range lines {
		c := strings.ToUpper(l.QSO.Call)
		if !l.Reply.IsZero() || seen[c] {
			continue
		}
		seen[c] = true
		if info, _ := s.store.GetStation(c); info == nil || s.refresher.IsStale(info) {
			go s.refresher.Get(context.Background(), c)
		}
	}
}

func (s *Server) pageReceive(w http.ResponseWriter, r *http.Request) {
	expected, err := s.expectedCards()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Expected": expected, "OverdueWeeks": s.config().Receive.OverdueWeeks}
	if call := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("call"))); call != "" {
		lines, truncated, err := s.rcvdLines(call)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.prefetchStations(lines)
		data["Call"], data["Lines"], data["Truncated"] = call, lines, truncated
	}
	s.render(w, r, "receive.html", data)
}

// htmxReceiveLookup lists the station's QSOs for booking.
func (s *Server) htmxReceiveLookup(w http.ResponseWriter, r *http.Request) {
	call := strings.ToUpper(strings.TrimSpace(r.FormValue("call")))
	if call == "" {
		s.render(w, r, "receive_results.html", map[string]any{"Call": ""})
		return
	}
	lines, truncated, err := s.rcvdLines(call)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prefetchStations(lines)
	s.render(w, r, "receive_results.html", map[string]any{"Call": call, "Lines": lines, "Truncated": truncated})
}

// ReplyCard is one reply to write: the QSOs with one worked callsign that
// need your card - the booked ones plus those already at the Desk (one card,
// B9; a /P call is a card of its own).
type ReplyCard struct {
	N       int // panel index (element ids)
	Call    string
	Lines   []*RcvdLine // oldest first
	Keys    []string
	Row     *QueueRow // research panel
	Route   string
	Manager string
	Mgr     MgrBlock
}

// htmxReceiveBook books the ticked QSOs as received and answers with the
// reply question (C2): which of them still need your card, and the ways to
// answer (C3). Ticking QSOs already booked just asks the question again.
func (s *Server) htmxReceiveBook(w http.ResponseWriter, r *http.Request) {
	call := strings.ToUpper(strings.TrimSpace(r.FormValue("call")))
	keys := deskKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Tick the QSO(s) the card confirms.")
		return
	}
	today := s.now().UTC().Format("20060102")
	for _, key := range keys {
		q, err := s.store.GetQSO(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if q == nil {
			s.fail(w, r, http.StatusNotFound, "QSO not found: %s", key)
			return
		}
		if rcvd, _ := q.EffectiveRcvd(); rcvd {
			continue // booked before: nothing to change
		}
		if err := s.store.SetQSLRcvdLocal(key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.store.AppendEvent(&store.Event{QSLKey: key, Direction: "rcvd", Date: today, Source: "manual"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if call == "" {
		call = callFromKey(keys[0])
	}
	lines, _, err := s.rcvdLines(call)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	booked := map[string]bool{}
	for _, k := range keys {
		booked[k] = true
	}
	var mine []*RcvdLine
	expectedCard := false
	for _, l := range lines {
		if booked[l.QSO.QSLKey] {
			mine = append(mine, l)
			expectedCard = expectedCard || l.Status == "requested"
		}
	}
	// A card that answers your request (C5) is the expected card: the
	// station wants no card from you, for none of the QSOs it confirms.
	if expectedCard {
		for _, l := range mine {
			if l.Reply.IsZero() {
				l.Reply, l.AtDesk = i18n.M("their card answers your request - nothing to send back"), false
			}
		}
	}
	cards, err := s.replyCards(mine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	expected, _ := s.expectedCards()
	s.render(w, r, "receive_reply.html", map[string]any{
		"Call": call, "Booked": mine, "Cards": cards,
		// the Expected list on the page, refreshed out of band
		"Expected": expected, "OverdueWeeks": s.config().Receive.OverdueWeeks, "OOB": true,
	})
}

// replyCards groups the booked QSOs that need your card by worked callsign
// and adds each call's QSOs already at the Desk, so one card answers them.
func (s *Server) replyCards(booked []*RcvdLine) ([]*ReplyCard, error) {
	desk, err := s.store.QueueList("decided")
	if err != nil {
		return nil, err
	}
	var cards []*ReplyCard
	byCall := map[string]*ReplyCard{}
	for _, l := range booked {
		if !l.Reply.IsZero() {
			continue
		}
		c := strings.ToUpper(l.QSO.Call)
		if byCall[c] == nil {
			byCall[c] = &ReplyCard{N: len(cards), Call: c}
			cards = append(cards, byCall[c])
		}
		byCall[c].Lines = append(byCall[c].Lines, l)
	}
	for _, rc := range cards {
		have := map[string]bool{}
		for _, l := range rc.Lines {
			have[l.QSO.QSLKey] = true
		}
		for _, it := range desk {
			if have[it.QSLKey] || !strings.EqualFold(callFromKey(it.QSLKey), rc.Call) {
				continue
			}
			if q, _ := s.store.GetQSO(it.QSLKey); q != nil {
				rc.Lines = append(rc.Lines, &RcvdLine{QSO: q, Status: "decided", Yours: i18n.M("at the Desk"), AtDesk: true})
			}
		}
		sort.SliceStable(rc.Lines, func(i, j int) bool {
			a, b := rc.Lines[i].QSO, rc.Lines[j].QSO
			if a.QSODate != b.QSODate {
				return a.QSODate < b.QSODate
			}
			return a.TimeOn < b.TimeOn
		})
		for _, l := range rc.Lines {
			rc.Keys = append(rc.Keys, l.QSO.QSLKey)
		}
		lead := rc.Lines[len(rc.Lines)-1].QSO.QSLKey // newest, like a Desk card
		rc.Row = s.queueRowFor(lead)
		s.researchFor(rc.Row, rc.Keys...)
		rc.Route, rc.Manager = suggestedRoute(rc.Row)
		rc.Mgr = s.mgrBlock(rc.Manager)
	}
	return cards, nil
}

// suggestedRoute is the route a reply starts with: the QRZ suggestion.
func suggestedRoute(row *QueueRow) (code, manager string) {
	switch row.Suggested {
	case "B", "D":
		return row.Suggested, row.MgrPrefill
	case "M":
		if row.MgrPrefill != "" {
			return "MD", row.MgrPrefill
		}
	}
	return "", row.MgrPrefill
}

// htmxReceiveResearch renders the research panel of a reply card again
// (live refresh when the station's QRZ data lands).
func (s *Server) htmxReceiveResearch(w http.ResponseWriter, r *http.Request) {
	keys := deskKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, "missing key")
		return
	}
	row := s.queueRowFor(keys[len(keys)-1])
	if row.QSO == nil {
		s.fail(w, r, http.StatusNotFound, "QSO not found")
		return
	}
	s.researchFor(row, keys...)
	s.render(w, r, "research_panel", row)
}

// htmxReceiveReply answers a received card (C3): how=written (written now,
// with its route), print (printed right here), or later (to the Desk as
// "yes, card"). The answer replaces the reply panel.
func (s *Server) htmxReceiveReply(w http.ResponseWriter, r *http.Request) {
	keys := deskKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, errNoKeys.Error())
		return
	}
	how := r.FormValue("how")
	var rt store.Route
	switch how {
	case "written", "print":
		var err error
		if rt, err = parseRoute(r.FormValue("route"), r.FormValue("manager")); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		for _, k := range keys[1:] { // one card = one worked callsign
			if !strings.EqualFold(callFromKey(k), callFromKey(keys[0])) {
				s.fail(w, r, http.StatusBadRequest, "One card answers one callsign - these QSOs have different calls.")
				return
			}
		}
	case "later":
	default:
		s.fail(w, r, http.StatusBadRequest, "unknown reply")
		return
	}
	if err := s.store.QueueReply(keys); err != nil {
		s.queueErr(w, r, err)
		return
	}
	route := i18n.M(routeName(routeCode(rt.Method, rt.Via)))
	msg, to := i18n.M("Your card to %s is at the Desk (%d QSO(s)).", callFromKey(keys[0]), len(keys)), "decided"
	switch how {
	case "written":
		if err := s.store.QueueWritten(keys, rt); err != nil {
			s.queueErr(w, r, err)
			return
		}
		msg, to = i18n.M("Your card to %s written (%s) - done.", callFromKey(keys[0]), route), "sent"
	case "print":
		if err := s.printCard(keys, rt); err != nil {
			if errors.Is(err, store.ErrConflict) {
				s.queueErr(w, r, err)
				return
			}
			// The reply is at the Desk now: say so in place of the panel.
			for _, k := range keys {
				s.publishQueueChanged(k, "decided")
			}
			s.render(w, r, "receive_msg", map[string]any{"Msg": i18n.M("Printing failed (%s) - your card to %s waits at the Desk.", err.Error(), callFromKey(keys[0])), "Err": true})
			return
		}
		msg, to = i18n.M("Your card to %s printed (%s) - done.", callFromKey(keys[0]), route), "sent"
	}
	for _, k := range keys {
		s.publishQueueChanged(k, to)
	}
	s.render(w, r, "receive_msg", map[string]any{"Msg": msg})
}
