package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/store"
)

// The QSO in progress (VISION A1b): the logger's current-contact broadcast
// shows the station on top of the Inbox with its research, and a decision
// made during the QSO (card / no card) is remembered and booked
// once the QSO is logged. The QSO may never be logged: the decision then
// stays listed with Undo and is dropped after contact.PendingTTL.

// PendingView is a decision made during a QSO not logged yet.
type PendingView struct {
	Call     string
	Decision string // contact.Decision: yes, no
}

// CurrentView is the "QSO in progress" box.
type CurrentView struct {
	Enabled     bool
	Compact     bool
	Cur         *contact.Current
	Info        *store.StationInfo
	Row         *QueueRow // research for the call
	Worked      i18n.Msg  // "first QSO" / "2 earlier QSOs, last 3 days ago": the chip under the callsign
	WorkedKind  string    // chip kind: first, earlier
	Head        CallHead  // the strip's head: callsign, name, country, the Worked pill
	Mine        *PendingView
	Others      []PendingView
	Applied     i18n.Msg // the last card booked from the box (recent only)
	AppliedLeft int      // seconds until the banner is gone (the box reloads itself then)
	Failed      bool
	WaitHours   int // a pending decision is dropped after this many hours
}

// appliedFor is how long the box reports a card it booked.
const appliedFor = 10 * time.Minute

func (s *Server) currentView(compact bool) CurrentView {
	v := CurrentView{Enabled: s.Contacts != nil, Compact: compact, WaitHours: int(contact.PendingTTL / time.Hour)}
	if s.Contacts == nil {
		return v
	}
	v.Cur = s.Contacts.Current()
	for _, p := range s.Contacts.Pending() {
		pv := PendingView{Call: p.Call, Decision: string(p.Decision)}
		if v.Cur != nil && p.Call == v.Cur.Call {
			v.Mine = &pv
		} else {
			v.Others = append(v.Others, pv)
		}
	}
	if a := s.Contacts.LastApplied(); a != nil && s.now().Sub(a.At) < appliedFor {
		v.AppliedLeft = int((appliedFor-s.now().Sub(a.At))/time.Second) + 2
		switch {
		case a.Err != "":
			v.Applied, v.Failed = i18n.M("The decision made during the QSO with %s could not be recorded: %s", a.Call, a.Err), true
		case a.Decision == contact.Yes:
			v.Applied = i18n.M("Card wanted, decided during the QSO with %s: the logged QSO is at the Desk.", a.Call)
		default:
			v.Applied = i18n.M("No card, decided during the QSO with %s: recorded on the logged QSO.", a.Call)
		}
	}
	if v.Cur != nil {
		v.Info, _ = s.store.GetStation(v.Cur.Call)
		row := &QueueRow{QSO: &store.QSO{Call: v.Cur.Call}, Info: v.Info}
		s.applyAssessment(row)
		s.researchFor(row)
		// The decide card's "with a yes they share one card" badges do not
		// fit here: a decision made now books only the QSO being logged. The
		// history chip is shown under the callsign instead.
		if res := row.Research; res != nil {
			var kept []Badge
			for _, b := range res.Badges {
				switch b.Kind {
				case "warn":
				case "first", "earlier":
					v.Worked, v.WorkedKind = b.Text, b.Kind
				default:
					kept = append(kept, b)
				}
			}
			if n := res.Others; n > 0 {
				kept = append(kept, Badge{Kind: "warn", Text: i18n.M("%d open QSO(s) with this station in New QSOs or at the Desk - a decision made during the QSO is recorded only on the QSO being logged", n)})
			}
			res.Badges = kept
		}
		v.Row = row
		v.Head = headFor(v.Cur.Call, "", v.Info, nil, i18n.Msg{})
		if !v.Worked.IsZero() {
			v.Head.Pills = []Badge{{Kind: v.WorkedKind, Text: v.Worked}}
		}
	}
	return v
}

// htmxCurrent renders the box (live refresh).
func (s *Server) htmxCurrent(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "current_contact", s.currentView(r.FormValue("compact") == "1"))
}

// htmxCurrentDecide remembers "card" (yes) or "no card" for the QSO in
// progress; it is booked when the QSO is logged. The call may never be logged
// - Undo drops the decision, and it expires by itself.
func (s *Server) htmxCurrentDecide(w http.ResponseWriter, r *http.Request) {
	if s.Contacts == nil {
		s.fail(w, r, http.StatusNotImplemented, "the QSO in progress is not available")
		return
	}
	call := strings.ToUpper(strings.TrimSpace(r.FormValue("call")))
	cur := s.Contacts.Current()
	if call == "" || cur == nil || cur.Call != call {
		s.fail(w, r, http.StatusConflict, "The QSO in progress has changed - look again.")
		return
	}
	switch r.FormValue("decision") {
	case "yes":
		s.Contacts.MarkDecision(call, contact.Yes)
	case "no":
		s.Contacts.MarkDecision(call, contact.No)
	default:
		s.fail(w, r, http.StatusBadRequest, "The decision is yes (card) or no (no card).")
		return
	}
	s.htmxCurrent(w, r)
}

// htmxCurrentCancel forgets a decision remembered for a QSO not logged yet.
func (s *Server) htmxCurrentCancel(w http.ResponseWriter, r *http.Request) {
	if s.Contacts != nil {
		s.Contacts.Cancel(r.FormValue("call"))
	}
	s.htmxCurrent(w, r)
}
