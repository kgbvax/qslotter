package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
	"github.com/go-chi/chi/v5"
)

// The card lifecycle (queue status):
//
//	queued   new QSO, no decision yet          -> Inbox (/queue, /decide)
//	decided  "yes, card"; the route is chosen when the card is finished
//	                                           -> Desk (/work, /work/card)
//	sent     card produced (printed or written, with its route; or written
//	         now in the Inbox, bureau/direct) -> /done; pushed to Clublog
//	skipped  "no card" decision (or backlog)   -> /done
//
// Every move goes through one guarded store transition (store.Queue*), which
// answers ErrConflict (HTTP 409) when the card is no longer where the page
// thinks it is.

// --- log page ---

func (s *Server) pageLog(w http.ResponseWriter, r *http.Request) {
	qsos, err := s.store.AllQSOs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lastPull, _ := s.store.MetaGet("clublog_last_pull_at")
	lastPush, _ := s.store.MetaGet("clublog_last_push_at")
	s.render(w, r, "log.html", map[string]any{
		"QSOs":     qsos[:min(len(qsos), 200)],
		"LastPull": lastPull,
		"LastPush": lastPush,
	})
}

// --- queue rows ---

// QueueRow is one card: the queue item plus the QSO and the cached station
// info behind the method suggestion.
type QueueRow struct {
	Item       *store.QueueItem
	QSO        *store.QSO
	Info       *store.StationInfo
	Suggested  string // method suggestion from qsldetermine ("N" when paper refused); never a decision
	Chosen     string // the operator's recorded decision (empty while undecided)
	MgrPrefill string // manager callsign for the manager routes (recorded, else a valid suggested route)

	// Research, filled by researchFor for the card views only (lists stay light).
	Research *Research
}

// Badge is one fact worth a glance next to the decision: a repeat contact, a
// card already sent or received, LoTW confirmation...
type Badge struct {
	Kind string // "info", "sent", "rcvd", "warn"
	Text i18n.Msg
}

// HistoryLine is one earlier QSO with the station, with the state of its card.
type HistoryLine struct {
	Date, Time, Call, Band, Mode string
	Sent                         i18n.Msg // "sent 2024-03-02 via Bureau" / zero
	Rcvd                         i18n.Msg // "received 2024-03-05" / zero
	Queue                        i18n.Msg // where the card stands: "awaiting decision", "at the Desk", "no card" / zero
	LoTW                         bool
}

// Research is what the operator wants next to a decision: the station's own
// QSL statements, the history with the station and what happened to its cards.
type Research struct {
	Badges     []Badge
	History    []HistoryLine
	Others     int      // other cards of this station awaiting a decision or production
	BioExcerpt string   // the QSL-relevant lines of the QRZ bio
	QRZState   string   // "off", "pending", "notfound", "ok"
	QRZAge     i18n.Msg // how old the cached entry is ("3h")
	WhyQueued  string   // override reason, when a normally filtered QSO was forced in
}

// suggestFor maps cached station info to the suggestion value.
func suggestFor(info *store.StationInfo) string {
	if info == nil {
		return ""
	}
	if info.RefusePaper {
		return "N"
	}
	return info.QSLMethod
}

// queueRowFor assembles the full row data for one QSO. Best-effort: missing
// station info or queue item yields a row with empty suggestion. QSO is nil
// when the QSO row is gone.
func (s *Server) queueRowFor(key string) *QueueRow { return s.buildRow(key, true) }

// buildRow is queueRowFor; refresh=false skips the background QRZ lookup (for
// scans over many items that only need the cached suggestion).
func (s *Server) buildRow(key string, refresh bool) *QueueRow {
	item, _ := s.store.QueueGet(key)
	qso, _ := s.store.GetQSO(key)
	var info *store.StationInfo
	if qso != nil {
		info, _ = s.store.GetStation(qso.Call)
		// Best-effort refresh: if the station info is missing or stale, look it
		// up asynchronously (cache TTL, in-flight dedup and failure cooldown
		// apply). The row is updated via the station_updated SSE event.
		if refresh && s.refresher != nil && (info == nil || s.refresher.IsStale(info)) {
			go s.refresher.Get(context.Background(), qso.Call)
		}
	}
	row := &QueueRow{Item: item, QSO: qso, Info: info}
	row.Suggested = suggestFor(info)
	if item != nil {
		row.Chosen = item.DesiredMethod
		row.MgrPrefill = item.Manager
	}
	if row.MgrPrefill == "" && row.Suggested == "M" && info != nil && qsldetermine.LooksLikeCallsign(info.QSLRoute) {
		row.MgrPrefill = strings.ToUpper(info.QSLRoute)
	}
	return row
}

// routeCode is the form value of a route: B, D, MD (via manager, direct) or MB
// (via manager, bureau); "" when there is none.
func routeCode(method, via string) string {
	switch strings.ToUpper(method) {
	case "B", "D":
		return strings.ToUpper(method)
	case "M":
		if strings.ToUpper(via) == "B" {
			return "MB"
		}
		return "MD"
	}
	return ""
}

// routeName spells out a route code.
func routeName(code string) string {
	switch code {
	case "B":
		return "Bureau"
	case "D":
		return "Direct"
	case "MD":
		return "Via manager, direct"
	case "MB":
		return "Via manager, bureau"
	}
	return "Route open"
}

// rowsFor builds rows for the listed items, skipping those whose QSO is gone.
// refresh starts background QRZ lookups for missing/stale station info (page
// loads; not the live list reloads, which run on every event in every window).
func (s *Server) rowsFor(items []*store.QueueItem, refresh bool) []*QueueRow {
	var rows []*QueueRow
	for _, it := range items {
		if row := s.buildRow(it.QSLKey, refresh); row.QSO != nil && row.Item != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// isFragmentRequest reports an htmx-driven request (swap target wants the
// fragment, not the page shell).
func isFragmentRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// bioKeywordRe picks the bio lines that say something about QSL routes.
var bioKeywordRe = regexp.MustCompile(`(?i)qsl|bureau|buro|b\x{fc}ro|direct|direkt|sae|irc|\$|\x{20ac}|eur|manager|via|lotw|eqsl|e-qsl|card|paper|postage|stamp`)

// bioExcerpt returns up to six short QSL-relevant lines of a cleaned bio.
func bioExcerpt(bio string) string {
	var out []string
	for _, ln := range strings.Split(bio, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || !bioKeywordRe.MatchString(ln) {
			continue
		}
		if len(ln) > 220 {
			ln = ln[:220] + "..."
		}
		out = append(out, ln)
		if len(out) == 6 {
			break
		}
	}
	return strings.Join(out, "\n")
}

// queueStateText spells out where an earlier QSO's card stands.
func queueStateText(status, method, manager string) i18n.Msg {
	switch status {
	case "queued":
		return i18n.M("awaiting decision")
	case "decided":
		if c := routeCode(method, ""); c != "" {
			return i18n.M("at the Desk (%s)", i18n.M(routeName(c)))
		}
		return i18n.M("at the Desk")
	case "skipped":
		return i18n.M("no card")
	case "requested":
		return i18n.M("their card requested")
	}
	return i18n.Msg{}
}

// sentMsg spells out a sent card: "sent 2024-03-02 via Bureau, manager K2ABC"
// (date YYYYMMDD, method B/D/M; manager only for a manager route).
func sentMsg(date, method, desired, manager string) i18n.Msg {
	format, args := "sent", []any{}
	if date != "" {
		format += " %s"
		args = append(args, fmtDate(date))
	}
	if method != "" {
		format += " via %s"
		args = append(args, i18n.M(methodName(method)))
	}
	if desired == "M" && manager != "" && method != "M" {
		format += ", manager %s"
		args = append(args, manager)
	}
	return i18n.M(format, args...)
}

// rcvdMsg spells out a received card: "received 2024-03-05".
func rcvdMsg(date string) i18n.Msg {
	if date == "" {
		return i18n.M("received")
	}
	return i18n.M("received %s", fmtDate(date))
}

// researchFor gathers the research panel for one card: what QRZ says about the
// station's QSL habits (already on the row), and what happened between the
// operator and this station so far (earlier QSOs, cards sent/received).
func (s *Server) researchFor(row *QueueRow, sameCard ...string) {
	if row == nil || row.QSO == nil {
		return
	}
	onCard := map[string]bool{row.QSO.QSLKey: true}
	for _, k := range sameCard {
		onCard[k] = true
	}
	res := &Research{}
	row.Research = res
	if row.Item != nil {
		res.WhyQueued = row.Item.OverrideReason
	}

	// QRZ state.
	switch {
	case row.Info != nil && row.Info.NotFound:
		res.QRZState = "notfound"
	case row.Info != nil:
		res.QRZState = "ok"
		res.QRZAge = sinceMsg(row.Info.FetchedAt)
	case s.refresher == nil || !s.refresher.Configured():
		res.QRZState = "off"
	default:
		res.QRZState = "pending"
	}
	if row.Info != nil {
		res.BioExcerpt = bioExcerpt(row.Info.BioText)
	}

	hist, err := s.store.CallHistory(row.QSO.Call, 12)
	if err != nil {
		return
	}
	var (
		prior                            int
		sentBadge, rcvdBad               *Badge
		lotw                             bool
		sameDesk, sameInbox, otherCallOp int
		otherCalls                       []string
	)
	for _, h := range hist {
		q := h.QSO
		if onCard[q.QSLKey] { // the card's own QSOs are shown on the card
			continue
		}
		prior++
		line := HistoryLine{Date: fmtDate(q.QSODate), Time: fmtTime(q.TimeOn), Call: q.Call, Band: q.Band, Mode: q.Mode,
			LoTW: q.LoTWQSLRcvd == "Y", Queue: queueStateText(h.QueueStatus, h.DesiredMethod, h.Manager)}
		if sent, method, date := q.EffectiveSent(); sent {
			line.Sent = sentMsg(date, method, h.DesiredMethod, h.Manager)
			if sentBadge == nil {
				sentBadge = &Badge{Kind: "sent", Text: i18n.M("card already %s", line.Sent)}
			}
		}
		if rcvd, date := q.EffectiveRcvd(); rcvd {
			line.Rcvd = rcvdMsg(date)
			if rcvdBad == nil {
				rcvdBad = &Badge{Kind: "rcvd", Text: i18n.M("their card %s - a reply is due unless you already sent one", line.Rcvd)}
			}
		}
		lotw = lotw || line.LoTW
		if len(res.History) < 8 {
			res.History = append(res.History, line)
		}
		if h.QueueStatus == "queued" || h.QueueStatus == "decided" {
			res.Others++
			switch {
			case !strings.EqualFold(q.Call, row.QSO.Call):
				otherCallOp++
				if !slices.Contains(otherCalls, q.Call) {
					otherCalls = append(otherCalls, q.Call)
				}
			case h.QueueStatus == "decided":
				sameDesk++
			default:
				sameInbox++
			}
		}
	}
	if prior == 0 {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: i18n.M("first QSO with this station")})
	} else {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: i18n.M("%d earlier QSO(s) with this station", prior)})
	}
	if sentBadge != nil {
		res.Badges = append(res.Badges, *sentBadge)
	}
	if rcvdBad != nil {
		res.Badges = append(res.Badges, *rcvdBad)
	}
	if lotw {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: i18n.M("LoTW confirmed")})
	}
	// One card covers the open QSOs with the same call at the Desk (B9); a
	// /P or prefixed call is a separate card.
	if sameDesk > 0 {
		res.Badges = append(res.Badges, Badge{Kind: "warn", Text: i18n.M("%d QSO(s) with %s already at the Desk - a yes puts this one on the same card", sameDesk, row.QSO.Call)})
	}
	if sameInbox > 0 {
		res.Badges = append(res.Badges, Badge{Kind: "warn", Text: i18n.M("%d more QSO(s) with %s wait in the Inbox - with a yes they share one card", sameInbox, row.QSO.Call)})
	}
	if otherCallOp > 0 {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: i18n.M("%d open QSO(s) under other calls of this station (%s) - separate card(s)", otherCallOp, strings.Join(otherCalls, ", "))})
	}
}

// --- (a) decision queue: list ---

// pageQueue is the Inbox: the master-detail view (VISION A6: the list only
// selects, the detail pane decides), or ?compact=1 the compact list for
// operating (A5).
func (s *Server) pageQueue(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("compact") == "1" {
		s.render(w, r, "queue_compact.html", map[string]any{
			"Current": s.currentView(true),
			"Rows":    s.rowsFor(items, true),
		})
		return
	}
	data := s.decideData(r, items, true)
	data["Rows"] = s.rowsFor(items, true)
	data["Current"] = s.currentView(false)
	s.render(w, r, "queue.html", data)
}

// htmxQueueList renders the Inbox master list alone (live refresh).
func (s *Server) htmxQueueList(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "queue_md_rows", map[string]any{"Rows": s.rowsFor(items, false)})
}

// firstPresent returns the first of keys that is still among items: after an
// action the view moves to the card that was below the handled one (else the
// one above it).
func firstPresent(items []*store.QueueItem, keys ...string) string {
	for _, k := range keys {
		for _, it := range items {
			if k != "" && it.QSLKey == k {
				return k
			}
		}
	}
	return ""
}

// htmxQueueRow renders a single decision-queue row (SSE-driven insert and
// refresh). ?compact=1 renders the compact variant. Items no longer awaiting a
// decision answer 204: nothing to insert.
func (s *Server) htmxQueueRow(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		s.fail(w, r, http.StatusBadRequest, "missing key")
		return
	}
	row := s.queueRowFor(key)
	if row.QSO == nil {
		s.fail(w, r, http.StatusNotFound, "QSO not found")
		return
	}
	if row.Item == nil || row.Item.Status != "queued" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	name := "queue_row_full.html"
	if r.URL.Query().Get("compact") == "1" {
		name = "queue_row_compact.html"
	}
	s.render(w, r, name, row)
}

// --- (a) decision queue: one card at a time ---

// pageDecide renders the decide view. Browser navigations get the full page
// (htmx, CSS and the keyboard handler live in its shell); htmx requests and
// work=1 action responses get just the swappable card fragment, so every
// decision advances to the next card.
func (s *Server) pageDecide(w http.ResponseWriter, r *http.Request) {
	s.renderDecideCard(w, r, !isFragmentRequest(r))
}

// renderWork answers a work=1 action with the next decision card.
func (s *Server) renderWork(w http.ResponseWriter, r *http.Request) {
	s.renderDecideCard(w, r, false)
}

// cardWindow picks the card to show from items: the one named by ?key= when
// present (browsing), else the first. It also returns the neighbours for
// prev/next links (wrapping), and the 1-based position.
func cardWindow(items []*store.QueueItem, key string) (cur *store.QueueItem, prev, next string, pos int) {
	if len(items) == 0 {
		return nil, "", "", 0
	}
	i := 0
	for j, it := range items {
		if it.QSLKey == key {
			i = j
			break
		}
	}
	if len(items) > 1 {
		prev = items[(i+len(items)-1)%len(items)].QSLKey
		next = items[(i+1)%len(items)].QSLKey
	}
	return items[i], prev, next, i + 1
}

func (s *Server) renderDecideCard(w http.ResponseWriter, r *http.Request, fullPage bool) {
	items, err := s.store.QueueList("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := s.decideData(r, items, r.FormValue("md") == "1")
	if fullPage {
		s.render(w, r, "decide.html", data)
		return
	}
	s.render(w, r, "decide_content.html", data)
}

// decideData picks the Inbox card to show: ?key= on GET (browsing, the
// master list's selection), else after an action the card that was below the
// handled one (next=, else prev= above it, else the newest). md marks the
// master-detail detail pane (no browse buttons; the list navigates).
func (s *Server) decideData(r *http.Request, items []*store.QueueItem, md bool) map[string]any {
	data := map[string]any{"Total": len(items), "MD": md, "Down": "", "Up": ""}
	want := r.URL.Query().Get("key")
	if r.Method != http.MethodGet {
		want = firstPresent(items, r.FormValue("next"), r.FormValue("prev"))
	}
	if cur, prev, next, pos := cardWindow(items, want); cur != nil {
		if row := s.queueRowFor(cur.QSLKey); row.QSO != nil {
			s.researchFor(row)
			data["Row"] = row
			data["Pos"] = i18n.M("card %d of %d", pos, len(items))
			data["Prev"], data["Next"] = prev, next
			if pos < len(items) { // neighbours without wrap-around, for "next one down"
				data["Down"] = items[pos].QSLKey
			}
			if pos > 1 {
				data["Up"] = items[pos-2].QSLKey
			}
		}
	}
	return data
}

// --- done ---

// DoneRow is one finished card with its outcome spelled out.
type DoneRow struct {
	Row     *QueueRow
	Outcome i18n.Msg
	When    string
}

func outcomeOf(it *store.QueueItem) i18n.Msg {
	switch {
	case it.Status == "skipped" && it.Note == "backlog":
		return i18n.M("no card (backlog)")
	case it.Status == "skipped":
		return i18n.M("no card")
	case it.Status == "requested":
		if it.Note != "" {
			return i18n.M("their card requested via %s: %s", i18n.M(it.Channel), it.Note)
		}
		return i18n.M("their card requested via %s", i18n.M(it.Channel))
	case it.Note == "sent elsewhere":
		return i18n.M("sent elsewhere (per Clublog)")
	case it.DesiredMethod == "W":
		return i18n.M("written on the spot")
	}
	how := i18n.M("written")
	if it.PrintedAt.Valid {
		how = i18n.M("printed")
	}
	if it.Note == "written now" {
		how = i18n.M("written now")
	}
	var name i18n.Msg
	switch it.DesiredMethod {
	case "B":
		name = i18n.M("Bureau")
	case "D":
		name = i18n.M("Direct")
	case "M":
		switch {
		case it.Manager != "" && it.SendVia == "B":
			name = i18n.M("via manager %s (bureau)", it.Manager)
		case it.Manager != "" && it.SendVia == "D":
			name = i18n.M("via manager %s (direct)", it.Manager)
		case it.Manager != "":
			name = i18n.M("via manager %s", it.Manager)
		default:
			name = i18n.M("via manager")
		}
	default:
		name = i18n.M("sent")
	}
	return i18n.M("%s, %s", name, how)
}

// pageDone lists recently finished cards (sent, declined) with Reopen.
func (s *Server) pageDone(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("sent", "skipped", "requested")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	shown := items[:min(len(items), 200)]
	var done []DoneRow
	for _, row := range s.rowsFor(shown, false) {
		when := ""
		if row.Item.SentAt.Valid {
			when = row.Item.SentAt.String
		}
		done = append(done, DoneRow{Row: row, Outcome: outcomeOf(row.Item), When: when})
	}
	s.render(w, r, "done.html", map[string]any{"Rows": done, "Total": len(items)})
}

// externalHosts may be opened in the system browser from the app window. The
// app window itself only ever shows qslotter; everything else leaves it.
var externalHosts = []string{"qrz.com", "clublog.org", "lotw.arrl.org", "eqsl.cc"}

// apiOpenExternal opens an allow-listed URL in the system browser: in the
// desktop app window a target=_blank link has nowhere to go (a WebView opens
// no second browser window), so static/app.js posts it here.
func (s *Server) apiOpenExternal(w http.ResponseWriter, r *http.Request) {
	if s.OpenExternal == nil {
		s.fail(w, r, http.StatusNotImplemented, "not running as the desktop app")
		return
	}
	u, err := url.Parse(r.FormValue("url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !allowedExternal(u.Hostname()) {
		s.fail(w, r, http.StatusForbidden, "this link cannot be opened from qslotter")
		return
	}
	if err := s.OpenExternal(u.String()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func allowedExternal(host string) bool {
	host = strings.ToLower(host)
	for _, h := range externalHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// apiQuit ends the desktop app - the way out when the tray icon is not
// available (e.g. Windows hid it, or Explorer was not ready at logon).
func (s *Server) apiQuit(w http.ResponseWriter, r *http.Request) {
	if s.Quit == nil {
		s.fail(w, r, http.StatusNotImplemented, "not running as the desktop app")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<p class="ok">` + template.HTMLEscapeString(s.tr(r, "qslotter is shutting down.")) + `</p>`))
	go s.Quit()
}

// htmxNav renders the nav bar alone (badge counts), for live refresh.
func (s *Server) htmxNav(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "nav", nil)
}

// --- station page ---

func (s *Server) pageStation(w http.ResponseWriter, r *http.Request) {
	// Route is /station/*: portable calls contain "/" (EA8/DL1ABC).
	call := strings.ToUpper(strings.Trim(strings.TrimPrefix(r.URL.Path, "/station/"), "/"))
	// Best-effort: fill a missing or stale cache entry before rendering (the
	// "Refresh from QRZ" button forces a lookup). If QRZ isn't configured, show
	// the cache.
	if s.refresher != nil {
		_, _ = s.refresher.Get(r.Context(), call)
	}
	info, _ := s.store.GetStation(call)
	qsos, _ := s.store.RecentQSOsByCall(call, 20)
	// Cards awaiting a decision for this call: the expanded (deliberation)
	// view embeds the same decision controls as the queue rows.
	queued, _ := s.store.QueueList("queued")
	var rows []*QueueRow
	for _, it := range queued {
		if strings.EqualFold(callFromKey(it.QSLKey), call) {
			rows = append(rows, s.queueRowFor(it.QSLKey))
		}
	}
	s.render(w, r, "station.html", map[string]any{
		"Call": call, "Info": info, "QSOs": qsos, "Rows": rows,
	})
}

// htmxStationRefresh forces a QRZ re-lookup for a callsign and returns a
// fresh station-info HTML fragment for htmx swap. The call comes from the
// ?call= query (works for portable calls containing "/").
func (s *Server) htmxStationRefresh(w http.ResponseWriter, r *http.Request) {
	call := strings.ToUpper(strings.TrimSpace(r.FormValue("call")))
	if call == "" {
		call = strings.ToUpper(chi.URLParam(r, "call"))
	}
	if call == "" {
		s.fail(w, r, http.StatusBadRequest, "missing call")
		return
	}
	if s.refresher == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "QRZ not configured")
		return
	}
	info, err := s.refresher.Refresh(r.Context(), call)
	if err != nil || info == nil {
		s.render(w, r, "station_info.html", map[string]any{"Call": call, "Info": nil})
		return
	}
	s.render(w, r, "station_info.html", map[string]any{"Call": call, "Info": info})
}

// --- card actions (htmx) ---

// viewMode is the surface an action came from, which decides what the
// response swaps in.
type viewMode int

const (
	viewRow    viewMode = iota // a table row: the card left the list, remove the row
	viewDecide                 // the decide card view: answer with the next card
	viewWork                   // the work card view: answer with the next work card
)

func viewOf(r *http.Request) viewMode {
	switch {
	case r.FormValue("work") == "1", r.FormValue("view") == "decide":
		return viewDecide
	case r.FormValue("view") == "work":
		return viewWork
	}
	return viewRow
}

// publishQueueChanged tells every open window that a card moved.
func (s *Server) publishQueueChanged(key, to string) {
	if s.broker != nil {
		s.broker.Publish(events.QueueChanged(key, to))
	}
}

// afterTransition answers a successful card move: tell other windows, then
// swap in what the originating surface needs next.
func (s *Server) afterTransition(w http.ResponseWriter, r *http.Request, key, to string) {
	s.publishQueueChanged(key, to)
	switch viewOf(r) {
	case viewDecide:
		s.renderWork(w, r)
	case viewWork:
		s.renderWorkCard(w, r, false)
	default:
		// Empty 200: htmx swaps the row out for nothing, i.e. removes it.
		w.WriteHeader(http.StatusOK)
	}
}

// queueErr maps a store error to an HTTP answer in the request's language; a
// conflict is a stale page.
func (s *Server) queueErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, r, http.StatusConflict, "This card was already handled (stale page?) - reload the list.")
		return
	}
	if errors.Is(err, store.ErrBadRoute) {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), store.ErrBadRoute.Error()+": "))
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// parseRoute turns a route code (B, D, MD, MB) and a manager callsign into a
// store route.
func parseRoute(code, manager string) (store.Route, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	switch code {
	case "B", "D":
		return store.Route{Method: code}, nil
	case "MD", "MB":
		manager = strings.ToUpper(strings.TrimSpace(manager))
		if !qsldetermine.LooksLikeCallsign(manager) {
			return store.Route{}, errNeedManager
		}
		return store.Route{Method: "M", Via: code[1:], Manager: manager}, nil
	case "":
		return store.Route{}, errNeedRoute
	}
	return store.Route{}, errBadRoute
}

// routeFrom reads one card's route from the form: the per-row fields
// route:<key> / manager:<key> (Desk list, also posted with its batch form),
// else route= / manager=.
func routeFrom(r *http.Request, key string) (store.Route, error) {
	code, mgr := r.FormValue("route:"+key), r.FormValue("manager:"+key)
	if code == "" {
		code = r.FormValue("route")
	}
	if mgr == "" {
		mgr = r.FormValue("manager")
	}
	return parseRoute(code, mgr)
}

var (
	errNeedManager = errors.New("Via manager needs the manager's callsign - type it in the manager field.")
	errNeedRoute   = errors.New("Choose a route first: bureau, direct, or via manager (direct or bureau).")
	errBadRoute    = errors.New("route must be B, D, MD or MB")
)

// htmxQueueYes: "yes, card". The card leaves the Inbox for the Desk, where
// its route is chosen.
func (s *Server) htmxQueueYes(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueAccept(key); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.afterTransition(w, r, key, "decided")
}

// htmxQueueNone: the first-class "no paper card" decision.
func (s *Server) htmxQueueNone(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueDecline(key); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.afterTransition(w, r, key, "skipped")
}

// htmxQueueWritten: the card was filled in by hand, with its route - written
// now in the Inbox (bureau or direct) or written at the Desk.
func (s *Server) htmxQueueWritten(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	rt, err := routeFrom(r, key)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.QueueWrittenNow([]string{key}, rt); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.afterTransition(w, r, key, "sent")
}

// htmxQueueBack takes a decided card back to the decision queue.
func (s *Server) htmxQueueBack(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueBack(key); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.afterTransition(w, r, key, "queued")
}

// htmxQueueReopen puts a finished card back into the decision queue.
func (s *Server) htmxQueueReopen(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	inClublog, err := s.store.QueueReopen(key)
	if err != nil {
		s.queueErr(w, r, err)
		return
	}
	switch inClublog {
	case "sent":
		s.notice(w, r, "Reopened. Clublog already has this card as sent; reopening does not undo that there.")
	case "requested":
		s.notice(w, r, "Reopened. Clublog already has their card as requested (QSL_RCVD=R); reopening does not undo that there.")
	}
	s.afterTransition(w, r, key, "queued")
}

// batchQueue applies one action to every ticked card of the Desk list and
// redirects back with a done/failed count. The Inbox has no batch: deciding
// a QSO is as quick as ticking it.
func (s *Server) batchQueue(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("list") != "work" {
		s.fail(w, r, http.StatusBadRequest, "unknown batch action for this list")
		return
	}
	s.batchDesk(w, r, r.FormValue("action"), r.Form["keys"])
}

// --- sync handlers ---

func (s *Server) htmxSyncPull(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	o := &sync.Orchestrator{Store: s.store, Clublog: clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword,
		cfg.Clublog.Call, cfg.Clublog.APIKey), Rules: s.rules, Broker: s.broker}
	if s.Contacts != nil {
		o.OnNewQSO = s.Contacts.QSOLogged
	}
	_, _, err := o.PullAndUpsert()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	last, _ := s.store.MetaGet("clublog_last_pull_at")
	s.render(w, r, "sync_status.html", map[string]any{"LastPull": last, "PullErr": ""})
}

func (s *Server) htmxSyncPush(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	o := &sync.Orchestrator{Store: s.store,
		Clublog: clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword,
			cfg.Clublog.Call, cfg.Clublog.APIKey)}
	_, err := o.PushBack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	last, _ := s.store.MetaGet("clublog_last_push_at")
	s.render(w, r, "sync_status.html", map[string]any{"LastPush": last, "PushErr": ""})
}

// --- helpers ---

func callFromKey(key string) string {
	if i := strings.Index(key, "|"); i > 0 {
		return key[:i]
	}
	return ""
}

func qrzClientFromCfg(cfg *config.Config) *qrz.Client {
	return qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// htmxQueueRecompute runs the qualifier over the whole log and enqueues any
// newly-eligible QSOs. Triggered by the "Recompute queue" button on the queue
// page. Useful after editing qualifier rules or after a manual Clublog pull.
func (s *Server) htmxQueueRecompute(w http.ResponseWriter, r *http.Request) {
	if s.rules == nil {
		s.fail(w, r, http.StatusInternalServerError, "qualifier rules not configured")
		return
	}
	keys, err := s.rules.EnqueueAllKeys(s.store)
	for _, k := range keys {
		s.publishQueueChanged(k, "queued")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n := len(keys)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if n == 0 {
		_, _ = w.Write([]byte(`<span class="muted">` + template.HTMLEscapeString(s.tr(r, "no new QSOs to enqueue")) + `</span>`))
	} else {
		_, _ = w.Write([]byte(`<span class="ok">` + template.HTMLEscapeString(s.tr(r, "enqueued %d new QSO(s)", n)) + `</span>`))
	}
}

// --- SSE endpoint ---

// sseEvents is a Server-Sent Events stream. Clients subscribe with
// `new EventSource('/events')`. Events: "queue_changed" (a card moved; JSON
// {key,to}), "station_updated" (QRZ lookup finished for a call), "new_qso".
func (s *Server) sseEvents(w http.ResponseWriter, r *http.Request) {
	if s.broker == nil {
		http.Error(w, "event broker not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Send an initial hello so the client knows the stream is alive.
	_, _ = fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
	flusher.Flush()
	ch, unsub := s.broker.Subscribe()
	defer unsub()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
			flusher.Flush()
		}
	}
}
