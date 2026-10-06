package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/template"
)

// The Desk (VISION 2.2): cards with "yes, card", worked through away from the
// radio. One card covers every open QSO with the same worked callsign (B9; a
// /P operation is a separate card). The route is chosen when the card is
// written or sent to printing (B4); instead of sending, the station's card
// can be requested (B4b), or the card dropped after all (B6). Printing is
// two-step (docs/STATES.md): cards collect in the print queue at the top of
// the Desk, a print run sends them as one job, and the run is confirmed as a
// whole (or single cards reprinted) before they count as sent.

// DeskCard is one card at the Desk: the open QSOs with one callsign.
type DeskCard struct {
	Call  string
	Lead  *QueueRow   // newest QSO: station info, suggestion, research
	Rows  []*QueueRow // every QSO on the card, oldest first
	Keys  []string    // the QSOs' keys, oldest first
	Extra int         // QSOs beyond the lead (list display)

	Name   string // the newest non-empty NAME among the card's QSOs (printed)
	QTH    string // likewise for QTH
	Prints int    // physical cards a print produces (QSO rows per card from the template)

	Route      string // route offered first: B, D, MD (manager direct), MB (manager bureau), "" = none
	RouteFrom  string // where it comes from: "chosen earlier", "by QRZ"
	MgrPrefill string // manager callsign for the manager routes
	OQRS       bool   // QRZ mentions OQRS: "requested" is the likely outcome
	Note       string // card note offered: one set earlier, else the log's QSLMSG
}

// cardTemplate is the card layout in use: the configured template file, else
// the built-in default when none is set or the file does not exist. A file
// that exists but does not load is an error - printing the default onto
// pre-printed stock would waste the card.
func (s *Server) cardTemplate() (*template.Template, error) {
	path := s.config().Card.Template
	if path == "" {
		return template.Default(), nil
	}
	t, err := template.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return template.Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("card template %s: %w", path, err)
	}
	return t, nil
}

// deskCards groups the Desk's QSOs into cards, newest card first. With
// refresh, the lead QSO triggers a background QRZ lookup (page loads, not
// the live list reloads).
func (s *Server) deskCards(refresh bool) ([]*DeskCard, error) {
	items, err := s.store.QueueList("decided") // newest QSO first
	if err != nil {
		return nil, err
	}
	var cards []*DeskCard
	byCall := map[string]*DeskCard{}
	for _, it := range items {
		call := strings.ToUpper(callFromKey(it.QSLKey))
		c := byCall[call]
		row := s.buildRow(it.QSLKey, refresh && c == nil)
		if row.QSO == nil || row.Item == nil {
			continue
		}
		if c == nil {
			c = &DeskCard{Call: call, Lead: row}
			byCall[call] = c
			cards = append(cards, c)
		}
		c.Rows = append([]*QueueRow{row}, c.Rows...) // oldest first
	}
	perCard := 1
	if t, err := s.cardTemplate(); err == nil {
		perCard = printer.MaxRows(t)
	}
	for _, c := range cards {
		c.Extra = len(c.Rows) - 1
		for _, r := range c.Rows {
			c.Keys = append(c.Keys, r.Item.QSLKey)
			c.Name = cmpOr(r.QSO.Name, c.Name) // newest non-empty wins (rows are oldest first)
			c.QTH = cmpOr(r.QSO.QTH, c.QTH)
		}
		c.Prints = (len(c.Rows) + perCard - 1) / perCard
		c.Route, c.RouteFrom, c.MgrPrefill = preselectRoute(c)
		c.OQRS = mentionsOQRS(c.Lead.Info)
		c.Note = cardNote(c.Rows)
	}
	return cards, nil
}

// cardNote is the note a card starts with: one recorded earlier on any of its
// QSOs (a card taken back from the print queue), else the newest QSLMSG from
// the log.
func cardNote(rows []*QueueRow) string {
	for _, r := range rows {
		if r.Item.CardNote != "" {
			return r.Item.CardNote
		}
	}
	note := ""
	for _, r := range rows { // oldest first: the newest non-empty wins
		note = cmpOr(r.QSO.QSLMsg, note)
	}
	return note
}

// Head is the card's head: callsign, name, history, and how many QSOs the
// card covers when more than one.
func (c *DeskCard) Head() CallHead {
	var extra i18n.Msg
	switch {
	case c.Prints > 1:
		extra = i18n.M("%d QSOs on this card, prints as %d cards", len(c.Rows), c.Prints)
	case c.Extra > 0:
		extra = i18n.M("%d QSOs on this card", len(c.Rows))
	}
	return headFor(c.Call, c.Name, c.Lead.Info, c.Lead.Research, extra)
}

// cmpOr returns a unless it is empty, else b.
func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// preselectRoute picks the route the Desk offers first: one recorded on any
// of the card's QSOs (cards decided before the Inbox stopped asking for
// routes carry one), else the QRZ suggestion. The operator confirms it by
// printing or writing.
func preselectRoute(c *DeskCard) (code, from, manager string) {
	manager = c.Lead.MgrPrefill
	for _, r := range c.Rows {
		if code := routeCode(r.Item.DesiredMethod, r.Item.SendVia); code != "" {
			if r.Item.Manager != "" {
				manager = r.Item.Manager
			}
			return code, "your earlier choice", manager
		}
	}
	if code, m := routeFromAssessment(c.Lead); code != "" {
		return code, "by QRZ", m
	}
	return "", "", manager
}

var oqrsRe = regexp.MustCompile(`(?i)\boqrs\b`)

// mentionsOQRS reports whether the station's QRZ entry (manager field or bio)
// talks about OQRS.
func mentionsOQRS(info *store.StationInfo) bool {
	return info != nil && (oqrsRe.MatchString(info.QSLMgr) || oqrsRe.MatchString(info.BioText))
}

// routeGroup maps a route code to its Desk group / filter value.
func routeGroup(code string) string {
	if code == "" {
		return "O"
	}
	return code[:1]
}

// WorkGroup is one slice of the Desk list, by the route offered first.
type WorkGroup struct {
	Method string // filter value: O (route open), D, M, B
	Title  string
	Cards  []*DeskCard
}

var workGroupOrder = []WorkGroup{
	{Method: "O", Title: "Not chosen yet"},
	{Method: "D", Title: "Direct"},
	{Method: "M", Title: "Via manager"},
	{Method: "B", Title: "Bureau"},
}

// groupCards sorts the Desk cards into the list's route groups.
func groupCards(cards []*DeskCard) []WorkGroup {
	var groups []WorkGroup
	for _, g := range workGroupOrder {
		for _, c := range cards {
			if routeGroup(c.Route) == g.Method {
				g.Cards = append(g.Cards, c)
			}
		}
		if g.Method == "B" { // bureau cards are sorted for the parcel: by call
			sort.SliceStable(g.Cards, func(i, j int) bool { return g.Cards[i].Call < g.Cards[j].Call })
		}
		if len(g.Cards) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}

// listOrder is the cards in the order the master list shows them.
func listOrder(groups []WorkGroup) []*DeskCard {
	var out []*DeskCard
	for _, g := range groups {
		out = append(out, g.Cards...)
	}
	return out
}

// pageWork is the Desk master-detail view (VISION B2): the cards grouped by
// the route offered first on one side, the selected card on the other;
// finishing a card moves to the one below.
func (s *Server) pageWork(w http.ResponseWriter, r *http.Request) {
	cards, err := s.deskCards(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups := groupCards(cards)
	data := s.workCardData(r, listOrder(groups), "", true)
	data["Groups"] = groups
	if data["PrintQ"], err = s.printQueueData(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "worklist.html", data)
}

// htmxWorkList renders the Desk master list alone (live refresh).
func (s *Server) htmxWorkList(w http.ResponseWriter, r *http.Request) {
	cards, err := s.deskCards(false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "work_md_rows", map[string]any{"Groups": groupCards(cards)})
}

// pageWorkCard shows one Desk card at a time (?filter=O|B|D|M narrows the
// stack to one route group); finishing a card answers with the next one.
func (s *Server) pageWorkCard(w http.ResponseWriter, r *http.Request) {
	s.renderWorkCard(w, r, !isFragmentRequest(r))
}

// MgrBlock is the manager part of a work card: who the card goes to.
type MgrBlock struct {
	Call  string
	Info  *store.StationInfo
	State string // "none" (no valid call typed), "off" (no QRZ), "failed", "pending", "notfound", "ok"
}

// mgrBlock looks up the manager's cached QRZ entry (and refreshes it in the
// background; station_updated reloads the card when it lands).
func (s *Server) mgrBlock(call string) MgrBlock {
	call = strings.ToUpper(strings.TrimSpace(call))
	b := MgrBlock{Call: call, State: "none"}
	if !qsldetermine.LooksLikeCallsign(call) {
		return b
	}
	b.Info, _ = s.store.GetStation(call)
	if s.refresher != nil && (b.Info == nil || s.refresher.IsStale(b.Info)) {
		go s.refresher.Get(context.Background(), call)
	}
	switch {
	case b.Info != nil && b.Info.NotFound:
		b.State = "notfound"
	case b.Info != nil:
		b.State = "ok"
	case s.refresher == nil || !s.refresher.Configured():
		b.State = "off"
	case s.refresher.RecentlyFailed(call):
		b.State = "failed"
	default:
		b.State = "pending"
	}
	return b
}

// htmxWorkManager renders the manager block for the typed manager callsign.
func (s *Server) htmxWorkManager(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "mgr_block", s.mgrBlock(r.FormValue("manager")))
}

func (s *Server) renderWorkCard(w http.ResponseWriter, r *http.Request, fullPage bool) {
	cards, err := s.deskCards(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	md := r.FormValue("md") == "1"
	filter := strings.ToUpper(r.FormValue("filter"))
	switch {
	case md: // the master-detail pane follows the list's order
		cards, filter = listOrder(groupCards(cards)), ""
	case filter == "B", filter == "D", filter == "M", filter == "O":
		var kept []*DeskCard // in the list's order (bureau: by call)
		for _, c := range listOrder(groupCards(cards)) {
			if routeGroup(c.Route) == filter {
				kept = append(kept, c)
			}
		}
		cards = kept
	default:
		filter = ""
	}
	data := s.workCardData(r, cards, filter, md)
	if fullPage {
		s.render(w, r, "workcard.html", data)
		return
	}
	s.render(w, r, "workcard_content.html", data)
}

// workCardData picks the Desk card to show from cards: ?key= on GET
// (browsing, the master list's selection, or a reload that keeps the
// operator's unsaved choices: route, manager, unticked QSOs), else after an
// action the card that was below the finished one (next=, else prev=).
func (s *Server) workCardData(r *http.Request, cards []*DeskCard, filter string, md bool) map[string]any {
	data := map[string]any{"Total": len(cards), "Filter": filter, "MD": md, "Channels": store.RequestChannels, "Down": "", "Up": "",
		"NoteField": s.templateHasNote()}
	want := r.URL.Query().Get("key")
	if r.Method != http.MethodGet {
		want = ""
		for _, k := range []string{r.FormValue("next"), r.FormValue("prev")} {
			for _, c := range cards {
				if want == "" && k != "" && slices.Contains(c.Keys, k) {
					want = k
				}
			}
		}
	}
	if len(cards) > 0 {
		i, matched := 0, false
		for j, c := range cards {
			for _, k := range c.Keys {
				if want != "" && k == want {
					i, matched = j, true
				}
			}
		}
		c := cards[i]
		skip := map[string]bool{}
		// Choices carried through a reload apply only to the card they were
		// made on (the key may have left the Desk meanwhile).
		if q := r.URL.Query(); matched {
			switch code := strings.ToUpper(q.Get("route")); code {
			case "B", "D", "MD", "MB":
				if code != c.Route {
					c.Route, c.RouteFrom = code, ""
				}
			}
			if m := strings.ToUpper(strings.TrimSpace(q.Get("manager"))); m != "" {
				c.MgrPrefill = m
			}
			for _, k := range strings.Split(q.Get("skip"), ",") {
				skip[k] = k != ""
			}
		}
		s.researchFor(c.Lead, c.Keys...)
		data["Card"] = c
		data["Skip"] = skip
		data["Mgr"] = s.mgrBlock(c.MgrPrefill)
		data["Pos"] = i18n.M("card %d of %d", i+1, len(cards))
		if len(cards) > 1 {
			data["Prev"] = cards[(i+len(cards)-1)%len(cards)].Lead.Item.QSLKey
			data["Next"] = cards[(i+1)%len(cards)].Lead.Item.QSLKey
		}
		if i+1 < len(cards) { // neighbours without wrap-around, for "next one down"
			data["Down"] = cards[i+1].Lead.Item.QSLKey
		}
		if i > 0 {
			data["Up"] = cards[i-1].Lead.Item.QSLKey
		}
	}
	return data
}

// --- Desk actions ---

// deskKeys returns the QSOs an action applies to: the card's key fields (the
// ticked QSOs on the card view, every QSO of the row in the list).
func deskKeys(r *http.Request) []string {
	_ = r.ParseForm()
	seen := map[string]bool{}
	var keys []string
	for _, k := range r.Form["key"] {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

var errNoKeys = errors.New("No QSO selected for this card - tick at least one.")

// deskAction runs one Desk transition for the card in the request and answers
// like every card move: tell the other windows, then the next card (view=work)
// or an empty row.
func (s *Server) deskAction(w http.ResponseWriter, r *http.Request, to string, act func(keys []string) error) {
	keys := deskKeys(r)
	if len(keys) == 0 {
		s.fail(w, r, http.StatusBadRequest, errNoKeys.Error())
		return
	}
	if err := act(keys); err != nil {
		switch {
		case errors.Is(err, errNeedManager), errors.Is(err, errNeedRoute), errors.Is(err, errBadRoute):
			s.fail(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrBadRoute):
			s.queueErr(w, r, err)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	for _, k := range keys {
		s.publishQueueChanged(k, to)
	}
	if viewOf(r) == viewWork {
		s.renderWorkCard(w, r, false)
		return
	}
	w.WriteHeader(http.StatusOK) // the list row goes away
}

// cardRoute reads the card's route: the list row's route:<key> field (keyed by
// one of the card's QSOs), else the card view's route field.
func cardRoute(r *http.Request, keys []string) (store.Route, error) {
	for _, k := range keys {
		if r.FormValue("route:"+k) != "" || r.FormValue("manager:"+k) != "" {
			return routeFrom(r, k)
		}
	}
	return routeFrom(r, keys[0])
}

// htmxWorkPrint sends the card to printing with the chosen route and note: it
// waits in the print queue for the next print run.
func (s *Server) htmxWorkPrint(w http.ResponseWriter, r *http.Request) {
	s.deskAction(w, r, "toprint", func(keys []string) error {
		rt, err := cardRoute(r, keys)
		if err != nil {
			return err
		}
		return s.store.QueueToPrint(keys, rt, r.FormValue("cardnote"))
	})
}

// htmxWorkWritten records the card written by hand with the chosen route.
func (s *Server) htmxWorkWritten(w http.ResponseWriter, r *http.Request) {
	s.deskAction(w, r, "sent", func(keys []string) error {
		rt, err := cardRoute(r, keys)
		if err != nil {
			return err
		}
		return s.store.QueueWritten(keys, rt)
	})
}

// htmxWorkRequested records "requested (OQRS)": no own card, the station's
// card is requested via the given channel.
func (s *Server) htmxWorkRequested(w http.ResponseWriter, r *http.Request) {
	s.deskAction(w, r, "requested", func(keys []string) error {
		return s.store.QueueRequested(keys, store.Request{Channel: r.FormValue("channel"), Note: r.FormValue("note")})
	})
}

// htmxWorkNone: no card after all (B6).
func (s *Server) htmxWorkNone(w http.ResponseWriter, r *http.Request) {
	s.deskAction(w, r, "skipped", func(keys []string) error { return s.store.QueueDeskDecline(keys...) })
}
