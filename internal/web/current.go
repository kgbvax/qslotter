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
// shows the station on top of the Inbox with its research, and a card
// written during the QSO is remembered and booked once the QSO is logged.

// PendingView is a card written during a QSO not logged yet.
type PendingView struct {
	Call      string
	RouteName string // English, translated in the template
}

// CurrentView is the "QSO in progress" box.
type CurrentView struct {
	Enabled     bool
	Compact     bool
	Cur         *contact.Current
	Info        *store.StationInfo
	Row         *QueueRow // research for the call
	Mine        *PendingView
	Others      []PendingView
	Applied     i18n.Msg // the last card booked from the box (recent only)
	AppliedLeft int      // seconds until the banner is gone (the box reloads itself then)
	Failed      bool
}

// appliedFor is how long the box reports a card it booked.
const appliedFor = 10 * time.Minute

func (s *Server) currentView(compact bool) CurrentView {
	v := CurrentView{Enabled: s.Contacts != nil, Compact: compact}
	if s.Contacts == nil {
		return v
	}
	v.Cur = s.Contacts.Current()
	for _, p := range s.Contacts.Pending() {
		pv := PendingView{Call: p.Call, RouteName: routeName(routeCode(p.Route.Method, p.Route.Via))}
		if v.Cur != nil && p.Call == v.Cur.Call {
			v.Mine = &pv
		} else {
			v.Others = append(v.Others, pv)
		}
	}
	if a := s.Contacts.LastApplied(); a != nil && s.now().Sub(a.At) < appliedFor {
		route := i18n.M(routeName(routeCode(a.Route.Method, a.Route.Via)))
		v.AppliedLeft = int((appliedFor-s.now().Sub(a.At))/time.Second) + 2
		if a.Err != "" {
			v.Applied, v.Failed = i18n.M("The card written during the QSO with %s could not be recorded: %s", a.Call, a.Err), true
		} else {
			v.Applied = i18n.M("Card written during the QSO with %s recorded on the logged QSO (%s).", a.Call, route)
		}
	}
	if v.Cur != nil {
		v.Info, _ = s.store.GetStation(v.Cur.Call)
		row := &QueueRow{QSO: &store.QSO{Call: v.Cur.Call}, Info: v.Info}
		row.Suggested = suggestFor(v.Info)
		s.researchFor(row)
		// The decide card's "with a yes they share one card" badges do not
		// fit here: a card written now books only the QSO being logged.
		if res := row.Research; res != nil {
			var kept []Badge
			for _, b := range res.Badges {
				if b.Kind != "warn" {
					kept = append(kept, b)
				}
			}
			if n := res.Others; n > 0 {
				kept = append(kept, Badge{Kind: "warn", Text: i18n.M("%d open QSO(s) with this station in New QSOs or at the Desk - a card written during the QSO is recorded only on the QSO being logged", n)})
			}
			res.Badges = kept
		}
		v.Row = row
	}
	return v
}

// htmxCurrent renders the box (live refresh).
func (s *Server) htmxCurrent(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "current_contact", s.currentView(r.FormValue("compact") == "1"))
}

// htmxCurrentWritten remembers "written now" (bureau or direct) for the QSO
// in progress; it is booked when the QSO is logged.
func (s *Server) htmxCurrentWritten(w http.ResponseWriter, r *http.Request) {
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
	var rt store.Route
	switch r.FormValue("route") {
	case "B", "D":
		rt = store.Route{Method: r.FormValue("route")}
	default:
		s.fail(w, r, http.StatusBadRequest, "A card written during the QSO goes bureau or direct.")
		return
	}
	s.Contacts.MarkWritten(call, rt)
	s.htmxCurrent(w, r)
}

// htmxCurrentCancel forgets a card remembered for a QSO not logged yet.
func (s *Server) htmxCurrentCancel(w http.ResponseWriter, r *http.Request) {
	if s.Contacts != nil {
		s.Contacts.CancelWritten(r.FormValue("call"))
	}
	s.htmxCurrent(w, r)
}
