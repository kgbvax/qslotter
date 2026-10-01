package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-chi/chi/v5"
)

// The card lifecycle (queue status):
//
//	queued   new QSO, no decision yet          -> decision queue (/queue, /decide)
//	decided  Bureau/Direct/Manager chosen, card not produced yet
//	                                           -> work queue (/work, /work/card)
//	sent     card produced (printed or written) -> /done; pushed to Clublog
//	skipped  "no card" decision                -> /done
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
	s.render(w, "log.html", map[string]any{
		"QSOs":     qsos[:min(len(qsos), 200)],
		"LastPull": lastPull,
		"LastPush": lastPush,
	})
}

// --- queue rows ---

// QueueRow is one card: the queue item plus the QSO and the cached station
// info behind the method suggestion.
type QueueRow struct {
	Item         *store.QueueItem
	QSO          *store.QSO
	Info         *store.StationInfo
	Suggested    string // method suggestion from qsldetermine ("N" when paper refused); never a decision
	Chosen       string // the operator's recorded decision (empty while undecided)
	MgrPrefill   string // manager callsign offered next to the M button (a valid suggested route)
	ShowCheckbox bool   // batch-selection checkbox (queue/work list pages only)

	// Research, filled by researchFor for the card views only (lists stay light).
	Research *Research
}

// Badge is one fact worth a glance next to the decision: a repeat contact, a
// card already sent or received, LoTW confirmation...
type Badge struct {
	Kind string // "info", "sent", "rcvd", "warn"
	Text string
}

// HistoryLine is one earlier QSO with the station, with the state of its card.
type HistoryLine struct {
	Date, Time, Call, Band, Mode string
	Sent                         string // "sent 2024-03-02 via Bureau" / ""
	Rcvd                         string // "received 2024-03-05" / ""
	Queue                        string // where the card stands: "awaiting decision", "work queue", "no card", ""
	LoTW                         bool
}

// Research is what the operator wants next to a decision: the station's own
// QSL statements, the history with the station and what happened to its cards.
type Research struct {
	Badges     []Badge
	History    []HistoryLine
	Others     int    // other cards of this station awaiting a decision or production
	BioExcerpt string // the QSL-relevant lines of the QRZ bio
	QRZState   string // "off", "pending", "notfound", "ok"
	QRZAge     string
	WhyQueued  string // override reason, when a normally filtered QSO was forced in
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
func (s *Server) queueRowFor(key string) *QueueRow {
	item, _ := s.store.QueueGet(key)
	qso, _ := s.store.GetQSO(key)
	var info *store.StationInfo
	if qso != nil {
		info, _ = s.store.GetStation(qso.Call)
		// Best-effort refresh: if the station info is missing or stale, look it
		// up asynchronously (cache TTL, in-flight dedup and failure cooldown
		// apply). The row is updated via the station_updated SSE event.
		if s.refresher != nil && (info == nil || s.refresher.IsStale(info)) {
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

// rowsFor builds rows for the listed items, skipping those whose QSO is gone.
func (s *Server) rowsFor(items []*store.QueueItem) []*QueueRow {
	var rows []*QueueRow
	for _, it := range items {
		if row := s.queueRowFor(it.QSLKey); row.QSO != nil && row.Item != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// withCheckbox marks rows as batch-selectable (list pages; the station page
// embeds the same row template without the checkbox).
func withCheckbox(rows []*QueueRow) []*QueueRow {
	for _, r := range rows {
		r.ShowCheckbox = true
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
func queueStateText(status, method, manager string) string {
	switch status {
	case "queued":
		return "awaiting decision"
	case "decided":
		return "work queue: " + strings.ToLower(methodName(method))
	case "skipped":
		return "no card"
	case "sent":
		return ""
	}
	return ""
}

// researchFor gathers the research panel for one card: what QRZ says about the
// station's QSL habits (already on the row), and what happened between the
// operator and this station so far (earlier QSOs, cards sent/received).
func (s *Server) researchFor(row *QueueRow) {
	if row == nil || row.QSO == nil {
		return
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
		res.QRZAge = since(row.Info.FetchedAt)
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
		prior              int
		sentBadge, rcvdBad *Badge
		lotw               bool
	)
	for _, h := range hist {
		q := h.QSO
		if q.QSLKey == row.QSO.QSLKey {
			continue
		}
		prior++
		line := HistoryLine{Date: fmtDate(q.QSODate), Time: fmtTime(q.TimeOn), Call: q.Call, Band: q.Band, Mode: q.Mode,
			LoTW: q.LoTWQSLRcvd == "Y", Queue: queueStateText(h.QueueStatus, h.DesiredMethod, h.Manager)}
		if sent, method, date := q.EffectiveSent(); sent {
			line.Sent = "sent"
			if date != "" {
				line.Sent += " " + fmtDate(date)
			}
			if method != "" {
				line.Sent += " via " + methodName(method)
			}
			if sentBadge == nil {
				sentBadge = &Badge{Kind: "sent", Text: "card already " + line.Sent}
			}
		}
		if rcvd, date := q.EffectiveRcvd(); rcvd {
			line.Rcvd = "received"
			if date != "" {
				line.Rcvd += " " + fmtDate(date)
			}
			if rcvdBad == nil {
				rcvdBad = &Badge{Kind: "rcvd", Text: "their card " + line.Rcvd + " - a reply is due unless you already sent one"}
			}
		}
		lotw = lotw || line.LoTW
		if len(res.History) < 8 {
			res.History = append(res.History, line)
		}
		if (h.QueueStatus == "queued" || h.QueueStatus == "decided") && h.QSO.QSLKey != row.QSO.QSLKey {
			res.Others++
		}
	}
	if prior == 0 {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: "first QSO with this station"})
	} else {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: fmt.Sprintf("%d earlier QSO(s) with this station", prior)})
	}
	if sentBadge != nil {
		res.Badges = append(res.Badges, *sentBadge)
	}
	if rcvdBad != nil {
		res.Badges = append(res.Badges, *rcvdBad)
	}
	if lotw {
		res.Badges = append(res.Badges, Badge{Kind: "info", Text: "LoTW confirmed"})
	}
	if res.Others > 0 {
		res.Badges = append(res.Badges, Badge{Kind: "warn", Text: fmt.Sprintf("%d other card(s) for this station still pending - one card could cover them", res.Others)})
	}
}

// --- (a) decision queue: list ---

func (s *Server) pageQueue(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := "queue.html"
	if r.URL.Query().Get("compact") == "1" {
		name = "queue_compact.html"
	}
	s.render(w, name, map[string]any{
		"Rows":   withCheckbox(s.rowsFor(items)),
		"Done":   r.URL.Query().Get("done"),
		"Failed": r.URL.Query().Get("failed"),
	})
}

// htmxQueueRow renders a single decision-queue row (SSE-driven insert and
// refresh). ?compact=1 renders the compact variant. Items no longer awaiting a
// decision answer 204: nothing to insert.
func (s *Server) htmxQueueRow(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	row := s.queueRowFor(key)
	if row.QSO == nil {
		http.Error(w, "QSO not found", http.StatusNotFound)
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
	row.ShowCheckbox = true
	s.render(w, name, row)
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
	data := map[string]any{"Total": len(items)}
	// ?key= selects a card only on GET (browsing); actions always advance.
	want := ""
	if r.Method == http.MethodGet {
		want = r.URL.Query().Get("key")
	}
	if cur, prev, next, pos := cardWindow(items, want); cur != nil {
		if row := s.queueRowFor(cur.QSLKey); row.QSO != nil {
			s.researchFor(row)
			data["Row"] = row
			data["Pos"] = fmt.Sprintf("card %d of %d", pos, len(items))
			data["Prev"], data["Next"] = prev, next
		}
	}
	if fullPage {
		s.render(w, "decide.html", data)
		return
	}
	s.render(w, "decide_content.html", data)
}

// --- (b) work queue ---

// WorkGroup is one route's slice of the work queue.
type WorkGroup struct {
	Method string // B / D / M
	Title  string
	Rows   []*QueueRow
}

var workGroupOrder = []WorkGroup{
	{Method: "D", Title: "Direct"},
	{Method: "M", Title: "Via manager"},
	{Method: "B", Title: "Bureau"},
}

// pageWork lists the decided cards awaiting production, grouped by route.
func (s *Server) pageWork(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("decided")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := withCheckbox(s.rowsFor(items))
	var groups []WorkGroup
	for _, g := range workGroupOrder {
		for _, row := range rows {
			if row.Item.DesiredMethod == g.Method {
				g.Rows = append(g.Rows, row)
			}
		}
		if g.Method == "B" { // bureau cards are sorted for the parcel: by call
			sort.SliceStable(g.Rows, func(i, j int) bool { return g.Rows[i].QSO.Call < g.Rows[j].QSO.Call })
		}
		if len(g.Rows) > 0 {
			groups = append(groups, g)
		}
	}
	s.render(w, "worklist.html", map[string]any{
		"Groups": groups, "Total": len(rows),
		"Done": r.URL.Query().Get("done"), "Failed": r.URL.Query().Get("failed"),
	})
}

// pageWorkCard shows one decided card at a time (?filter=B|D|M narrows it to
// one route); Print/Written/Back answer with the next card.
func (s *Server) pageWorkCard(w http.ResponseWriter, r *http.Request) {
	s.renderWorkCard(w, r, !isFragmentRequest(r))
}

func (s *Server) renderWorkCard(w http.ResponseWriter, r *http.Request, fullPage bool) {
	filter := strings.ToUpper(r.FormValue("filter"))
	items, err := s.store.QueueList("decided")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if filter == "B" || filter == "D" || filter == "M" {
		var kept []*store.QueueItem
		for _, it := range items {
			if it.DesiredMethod == filter {
				kept = append(kept, it)
			}
		}
		items = kept
	} else {
		filter = ""
	}
	data := map[string]any{"Total": len(items), "Filter": filter}
	want := ""
	if r.Method == http.MethodGet {
		want = r.URL.Query().Get("key")
	}
	if cur, prev, next, pos := cardWindow(items, want); cur != nil {
		if row := s.queueRowFor(cur.QSLKey); row.QSO != nil {
			data["Row"] = row
			data["Pos"] = fmt.Sprintf("card %d of %d", pos, len(items))
			data["Prev"], data["Next"] = prev, next
		}
	}
	if fullPage {
		s.render(w, "workcard.html", data)
		return
	}
	s.render(w, "workcard_content.html", data)
}

// --- done ---

// DoneRow is one finished card with its outcome spelled out.
type DoneRow struct {
	Row     *QueueRow
	Outcome string
	When    string
}

func outcomeOf(it *store.QueueItem) string {
	switch {
	case it.Status == "skipped":
		return "no card"
	case it.DesiredMethod == "W":
		return "written on the spot"
	}
	how := "written"
	if it.PrintedAt.Valid {
		how = "printed"
	}
	name := map[string]string{"B": "Bureau", "D": "Direct", "M": "via manager"}[it.DesiredMethod]
	if name == "" {
		name = "sent"
	}
	if it.DesiredMethod == "M" && it.Manager != "" {
		name += " " + it.Manager
	}
	return name + ", " + how
}

// pageDone lists recently finished cards (sent, declined) with Reopen.
func (s *Server) pageDone(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueList("sent", "skipped")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	shown := items[:min(len(items), 200)]
	var done []DoneRow
	for _, row := range s.rowsFor(shown) {
		when := ""
		if row.Item.SentAt.Valid {
			when = row.Item.SentAt.String
		}
		done = append(done, DoneRow{Row: row, Outcome: outcomeOf(row.Item), When: when})
	}
	s.render(w, "done.html", map[string]any{"Rows": done, "Total": len(items)})
}

// externalHosts may be opened in the system browser from the app window. The
// app window itself only ever shows qslotter; everything else leaves it.
var externalHosts = []string{"qrz.com", "clublog.org", "lotw.arrl.org", "eqsl.cc"}

// apiOpenExternal opens an allow-listed URL in the system browser: in the
// desktop app window a target=_blank link has nowhere to go (a WebView opens
// no second browser window), so static/app.js posts it here.
func (s *Server) apiOpenExternal(w http.ResponseWriter, r *http.Request) {
	if s.OpenExternal == nil {
		http.Error(w, "not running as the desktop app", http.StatusNotImplemented)
		return
	}
	u, err := url.Parse(r.FormValue("url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !allowedExternal(u.Hostname()) {
		http.Error(w, "this link cannot be opened from qslotter", http.StatusForbidden)
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

// htmxNav renders the nav bar alone (badge counts), for live refresh.
func (s *Server) htmxNav(w http.ResponseWriter, r *http.Request) {
	s.render(w, "nav", nil)
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
	s.render(w, "station.html", map[string]any{
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
		http.Error(w, "missing call", http.StatusBadRequest)
		return
	}
	if s.refresher == nil {
		http.Error(w, "QRZ not configured", http.StatusServiceUnavailable)
		return
	}
	info, err := s.refresher.Refresh(r.Context(), call)
	if err != nil || info == nil {
		s.render(w, "station_info.html", map[string]any{"Call": call, "Info": nil})
		return
	}
	s.render(w, "station_info.html", map[string]any{"Call": call, "Info": info})
}

// --- receive page + htmx handlers ---

func (s *Server) pageReceive(w http.ResponseWriter, r *http.Request) {
	s.render(w, "receive.html", nil)
}

func (s *Server) htmxReceiveLookup(w http.ResponseWriter, r *http.Request) {
	call := strings.ToUpper(strings.TrimSpace(r.FormValue("call")))
	if call == "" {
		s.render(w, "receive_results.html", map[string]any{"Call": call, "QSOs": nil})
		return
	}
	qsos, err := s.store.RecentQSOsByCall(call, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "receive_results.html", map[string]any{"Call": call, "QSOs": qsos})
}

func (s *Server) htmxReceiveMark(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	if err := s.store.SetQSLRcvdLocal(key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.AppendEvent(&store.Event{
		QSLKey: key, Direction: "rcvd", Date: time.Now().UTC().Format("20060102"), Source: "manual",
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", "refreshQueue")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<span class="ok">received</span>`))
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

// queueErr maps a store error to an HTTP answer; a conflict is a stale page.
func (s *Server) queueErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrConflict) {
		http.Error(w, "This card was already handled (stale page?) - reload the list.", http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// decideOne records a Bureau/Direct/Via-manager decision. Via manager needs
// the manager's callsign: the form value, else a valid suggested route.
func (s *Server) decideOne(key, method, managerInput string) error {
	method = strings.ToUpper(strings.TrimSpace(method))
	manager := ""
	switch method {
	case "B", "D":
	case "M":
		manager = strings.ToUpper(strings.TrimSpace(managerInput))
		if manager == "" {
			manager = s.queueRowFor(key).MgrPrefill
		}
		if !qsldetermine.LooksLikeCallsign(manager) {
			return errNeedManager
		}
	default:
		return errBadMethod
	}
	return s.store.QueueDecide(key, method, manager)
}

var (
	errNeedManager = errors.New("Via manager needs the manager's callsign - type it next to the M button.")
	errBadMethod   = errors.New("method must be B, D or M (None = no card, Written = card written by hand)")
)

// htmxQueueDecide: Bureau / Direct / Via manager. The card leaves the decision
// queue and enters the work queue.
func (s *Server) htmxQueueDecide(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.decideOne(key, r.FormValue("method"), r.FormValue("manager")); err != nil {
		if errors.Is(err, errNeedManager) || errors.Is(err, errBadMethod) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.queueErr(w, err)
		return
	}
	s.afterTransition(w, r, key, "decided")
}

// htmxQueueNone: the first-class "no paper card" decision.
func (s *Server) htmxQueueNone(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueDecline(key); err != nil {
		s.queueErr(w, err)
		return
	}
	s.afterTransition(w, r, key, "skipped")
}

// htmxQueueWritten: the card was filled in by hand. From the decision queue
// it is its own outcome (no route); from the work queue it keeps the route.
func (s *Server) htmxQueueWritten(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueWritten(key); err != nil {
		s.queueErr(w, err)
		return
	}
	s.afterTransition(w, r, key, "sent")
}

// htmxQueueBack takes a decided card back to the decision queue.
func (s *Server) htmxQueueBack(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.store.QueueBack(key); err != nil {
		s.queueErr(w, err)
		return
	}
	s.afterTransition(w, r, key, "queued")
}

// htmxQueueReopen puts a finished card back into the decision queue.
func (s *Server) htmxQueueReopen(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	pushed, err := s.store.QueueReopen(key)
	if err != nil {
		s.queueErr(w, err)
		return
	}
	if pushed {
		w.Header().Set("HX-Trigger", `{"qslNotice":"Reopened. Clublog already has this card as sent; reopening does not undo that there."}`)
	}
	s.afterTransition(w, r, key, "queued")
}

// htmxWorkPrint renders and prints a decided card; only a successful print
// completes it (a printer error leaves it in the work queue).
func (s *Server) htmxWorkPrint(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if err := s.printOne(key); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.queueErr(w, err)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.afterTransition(w, r, key, "sent")
}

// printOne prints the card for a decided QSO and marks it sent.
func (s *Server) printOne(key string) error {
	item, err := s.store.QueueGet(key)
	if err != nil {
		return err
	}
	if item == nil || item.Status != "decided" {
		return store.ErrConflict
	}
	qso, err := s.store.GetQSO(key)
	if err != nil {
		return err
	}
	if qso == nil {
		return fmt.Errorf("QSO not found")
	}
	cfg := s.config()
	tmpl := template.Default()
	if cfg.Card.Template != "" {
		if t, err := template.Load(cfg.Card.Template); err == nil {
			tmpl = t
		}
	}
	fields := printer.QSOFields{
		Call: qso.Call, Name: qso.Name, QTH: qso.QTH,
		QSODate: qso.QSODate, TimeOn: qso.TimeOn,
		Band: qso.Band, Mode: qso.Mode,
		RSTSent: qso.RSTSent, RSTRcvd: qso.RSTRcvd,
		MyCall: cfg.Clublog.Call,
		MyName: cfg.Station.Name,
	}
	pdfPath := printer.TempPDFPath()
	if err := printer.RenderPDF(pdfPath, tmpl, fields); err != nil {
		return err
	}
	if err := s.printer.PrintPDF(pdfPath, cfg.Printer.Name, printer.Options{
		PaperWMM: cfg.Printer.PaperSizeMM[0],
		PaperHMM: cfg.Printer.PaperSizeMM[1],
		Copies:   1,
	}); err != nil {
		return err
	}
	return s.store.QueuePrinted(key)
}

// batchQueue applies one action to every checked row of a list page and
// redirects back with a done/failed count. list=work selects the work queue's
// action set; the default is the decision queue's.
func (s *Server) batchQueue(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	list := r.FormValue("list")
	keys := r.Form["keys"]

	var apply func(key string) (to string, err error)
	switch {
	case list != "work" && (action == "B" || action == "D"):
		apply = func(k string) (string, error) { return "decided", s.decideOne(k, action, "") }
	case action == "none":
		apply = func(k string) (string, error) { return "skipped", s.store.QueueDecline(k) }
	case action == "written":
		apply = func(k string) (string, error) { return "sent", s.store.QueueWritten(k) }
	case list == "work" && action == "print":
		apply = func(k string) (string, error) { return "sent", s.printOne(k) }
	case list == "work" && action == "back":
		apply = func(k string) (string, error) { return "queued", s.store.QueueBack(k) }
	default:
		http.Error(w, "unknown batch action for this list", http.StatusBadRequest)
		return
	}
	done, failed := 0, 0
	for _, key := range keys {
		to, err := apply(key)
		if err != nil {
			failed++
			continue
		}
		done++
		s.publishQueueChanged(key, to)
	}

	target := "/queue"
	q := url.Values{"done": {fmt.Sprint(done)}, "failed": {fmt.Sprint(failed)}}
	if list == "work" {
		target = "/work"
	} else if r.FormValue("compact") == "1" {
		q.Set("compact", "1")
	}
	http.Redirect(w, r, target+"?"+q.Encode(), http.StatusSeeOther)
}

// --- sync handlers ---

func (s *Server) htmxSyncPull(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	o := &sync.Orchestrator{Store: s.store, Clublog: clublog.New(cfg.Clublog.Email, cfg.Clublog.AppPassword,
		cfg.Clublog.Call, cfg.Clublog.APIKey), Rules: s.rules, Broker: s.broker}
	_, _, err := o.PullAndUpsert()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	last, _ := s.store.MetaGet("clublog_last_pull_at")
	s.render(w, "sync_status.html", map[string]any{"LastPull": last, "PullErr": ""})
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
	s.render(w, "sync_status.html", map[string]any{"LastPush": last, "PushErr": ""})
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
		http.Error(w, "qualifier rules not configured", http.StatusInternalServerError)
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
		_, _ = w.Write([]byte(`<span class="muted">no new QSOs to enqueue</span>`))
	} else {
		_, _ = w.Write([]byte(`<span class="ok">enqueued ` + fmt.Sprintf("%d", n) + ` new QSO(s)</span>`))
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
