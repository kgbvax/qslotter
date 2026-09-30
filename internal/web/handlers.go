package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
	"github.com/dl9et/qslotter/internal/template"
	"github.com/go-chi/chi/v5"
)

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

// --- queue page ---

// QueueRow is one row of the send-queue table: the queue item plus the QSO
// and the cached station info behind the method suggestion.
type QueueRow struct {
	Item         *store.QueueItem
	QSO          *store.QSO
	Info         *store.StationInfo
	Suggested    string // method suggestion from qsldetermine ("N" when paper refused)
	Chosen       string // preselected in the chooser: user decision > suggestion
	ShowCheckbox bool   // batch-selection checkbox (queue pages only)
}

// suggestFor maps cached station info to the chooser's suggestion value.
func suggestFor(info *store.StationInfo) string {
	if info == nil {
		return ""
	}
	if info.RefusePaper {
		return "N"
	}
	return info.QSLMethod
}

// chosenFor returns the method preselected in the chooser: the operator's
// recorded decision wins over the suggestion.
func chosenFor(item *store.QueueItem, info *store.StationInfo) string {
	if item != nil && item.DesiredMethod != "" {
		return item.DesiredMethod
	}
	return suggestFor(info)
}

// queueRowFor assembles the full row data for one queued QSO. Best-effort:
// missing station info or queue item yields a row with empty suggestion. QSO
// is nil when the QSO row is gone.
func (s *Server) queueRowFor(key string) *QueueRow {
	item, _ := s.store.QueueGet(key)
	qso, _ := s.store.GetQSO(key)
	var info *store.StationInfo
	if qso != nil {
		info, _ = s.store.GetStation(qso.Call)
		// Best-effort refresh: if the station info is missing and a refresher
		// is configured, trigger an async refresh. The row will be updated
		// via the station_updated SSE event once the lookup completes.
		if info == nil && s.refresher != nil {
			go s.refresher.Refresh(context.Background(), qso.Call)
		}
	}
	row := &QueueRow{Item: item, QSO: qso, Info: info}
	row.Suggested = suggestFor(info)
	row.Chosen = chosenFor(item, info)
	return row
}

// withCheckbox marks rows as batch-selectable (queue pages; the station page
// embeds the same row template without the checkbox).
func withCheckbox(rows []*QueueRow) []*QueueRow {
	for _, r := range rows {
		r.ShowCheckbox = true
	}
	return rows
}

func (s *Server) pageQueue(w http.ResponseWriter, r *http.Request) {
	queued, err := s.store.QueueByStatus("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	decided, err := s.store.QueueByStatus("decided")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := append(queued, decided...)
	var rows []*QueueRow
	for _, it := range items {
		row := s.queueRowFor(it.QSLKey)
		if row.QSO == nil {
			continue // QSO row gone; nothing to render
		}
		rows = append(rows, row)
	}
	name := "queue.html"
	if r.URL.Query().Get("compact") == "1" {
		name = "queue_compact.html"
	}
	s.render(w, name, map[string]any{"Rows": withCheckbox(rows)})
}

// --- decide view: one card at a time ---

// pageDecide renders the first queued card. Actions posted with work=1 land
// back here, so every decision advances to the next card.
func (s *Server) pageDecide(w http.ResponseWriter, r *http.Request) {
	s.renderWork(w, r)
}

// renderWork renders the decide fragment with the first queued card (or the
// empty state when nothing is queued).
func (s *Server) renderWork(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.QueueByStatus("queued")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Pos": "", "Total": len(items)}
	if len(items) > 0 {
		row := s.queueRowFor(items[0].QSLKey)
		if row.QSO != nil {
			data["Row"] = row
			data["Pos"] = fmt.Sprintf("card 1 of %d", len(items))
		}
	}
	s.render(w, "decide_content.html", data)
}

// workMode reports whether the request came from the decide view, in which
// case actions respond with the next card instead of a table row.
func workMode(r *http.Request) bool {
	return r.FormValue("work") == "1"
}

// --- station page ---

func (s *Server) pageStation(w http.ResponseWriter, r *http.Request) {
	// Route is /station/*: portable calls contain "/" (EA8/DL1ABC).
	call := strings.ToUpper(strings.Trim(strings.TrimPrefix(r.URL.Path, "/station/"), "/"))
	// Best-effort refresh: if the station info is missing or stale, refresh
	// from QRZ before rendering. If QRZ isn't configured, show the cache.
	if s.refresher != nil {
		_, _ = s.refresher.Refresh(r.Context(), call)
	}
	info, _ := s.store.GetStation(call)
	qsos, _ := s.store.RecentQSOsByCall(call, 20)
	// Queued items for this call: the expanded (deliberation) view embeds the
	// same decision controls as the queue rows.
	queued, _ := s.store.QueueByStatus("queued")
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

// --- queue actions (htmx) ---

func (s *Server) htmxQueuePrint(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	qsos, _ := s.store.RecentQSOsByCall(callFromKey(key), 50)
	var qso *store.QSO
	for _, q := range qsos {
		if q.QSLKey == key {
			qso = q
			break
		}
	}
	if qso == nil {
		http.Error(w, "QSO not found", http.StatusNotFound)
		return
	}
	tmpl := template.Default()
	if s.cfg.Card.Template != "" {
		if t, err := template.Load(s.cfg.Card.Template); err == nil {
			tmpl = t
		}
	}
	fields := printer.QSOFields{
		Call: qso.Call, Name: qso.Name, QTH: qso.QTH,
		QSODate: qso.QSODate, TimeOn: qso.TimeOn,
		Band: qso.Band, Mode: qso.Mode,
		RSTSent: qso.RSTSent, RSTRcvd: qso.RSTRcvd,
		MyCall: s.cfg.Clublog.Call,
		MyName: s.cfg.Station.Name,
	}
	pdfPath := printer.TempPDFPath()
	if err := printer.RenderPDF(pdfPath, tmpl, fields); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err := s.printer.PrintPDF(pdfPath, s.cfg.Printer.Name, printer.Options{
		PaperWMM: s.cfg.Printer.PaperSizeMM[0],
		PaperHMM: s.cfg.Printer.PaperSizeMM[1],
		Copies:   1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.store.QueueSetStatus(key, "printed")
	if workMode(r) {
		s.renderWork(w, r)
		return
	}
	s.render(w, "queue_row.html", map[string]any{"Key": key, "Status": "printed", "Msg": "sent to printer"})
}

func (s *Server) htmxQueueSkip(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	_ = s.store.QueueSetStatus(key, "skipped")
	if workMode(r) {
		s.renderWork(w, r)
		return
	}
	s.render(w, "queue_row.html", map[string]any{"Key": key, "Status": "skipped", "Msg": "skipped"})
}

// htmxQueueMethod records the operator's method decision for a queued QSO.
// Choosing "N" (none) is the first-class "no paper card" decision: it skips
// the QSO with the decision recorded in the event log, and recompute will not
// resurrect it.
func (s *Server) htmxQueueMethod(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	method := strings.ToUpper(strings.TrimSpace(r.FormValue("method")))
	switch method {
	case "B", "D", "M":
		manager := strings.ToUpper(strings.TrimSpace(r.FormValue("manager")))
		if err := s.store.QueueSetMethod(key, method, manager); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if workMode(r) {
			// A stamp is the decision: mark the card decided and advance to
			// the next one. Decided cards stay visible in the queue list.
			_ = s.store.QueueSetStatus(key, "decided")
			s.renderWork(w, r)
			return
		}
		s.renderQueueRow(w, r, key, "")
	case "N":
		if err := s.store.QueueSetMethod(key, "N", ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.store.QueueSetStatus(key, "skipped"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = s.store.AppendEvent(&store.Event{
			QSLKey: key, Direction: "sent", Method: "N",
			Date: time.Now().UTC().Format("20060102"), Source: "manual",
			Note: "decision: no paper QSL",
		})
		if workMode(r) {
			s.renderWork(w, r)
			return
		}
		s.render(w, "queue_row.html", map[string]any{"Key": key, "Status": "skipped", "Msg": "decision: no paper QSL"})
	default:
		http.Error(w, "method must be B, D, E, M or N", http.StatusBadRequest)
	}
}

// htmxQueueNone is the one-click "no card" decision (same as choosing N in
// the method chooser).
func (s *Server) htmxQueueNone(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	r.Form.Set("method", "N")
	s.htmxQueueMethod(w, r)
}

// htmxQueueSend marks the card sent with the chosen method. Resolution order:
// explicit form value > recorded decision (desired_method) > station
// suggestion > bureau (legacy default). A resolved "N" is rejected: declining
// a card is the None action, not a send.
func (s *Server) htmxQueueSend(w http.ResponseWriter, r *http.Request) {
	s.doQueueSend(w, r, "marked sent")
}

// htmxQueueHandwrite is the handwritten-card path: the operator wrote the card
// by hand; record it as sent with the chosen method.
func (s *Server) htmxQueueHandwrite(w http.ResponseWriter, r *http.Request) {
	s.doQueueSend(w, r, "handwritten card marked sent")
}

// errDeclined marks a card the operator explicitly declined ("none"); batch
// actions skip such rows instead of failing.
var errDeclined = errors.New("card declined (none)")

// sendOne marks one card sent with the resolved method (form value >
// recorded decision > station suggestion > bureau). Returns the method used.
func (s *Server) sendOne(key, formMethod string) (string, error) {
	row := s.queueRowFor(key)
	if row.QSO == nil {
		return "", fmt.Errorf("QSO not found")
	}
	method := strings.ToUpper(strings.TrimSpace(formMethod))
	if method == "" && row.Item != nil {
		method = strings.ToUpper(row.Item.DesiredMethod)
	}
	// An explicit decline - in the form or recorded on the item - is never a
	// send. Declining is the None action.
	if method == "N" {
		return "", errDeclined
	}
	if method == "" {
		method = row.Suggested
	}
	if method == "N" {
		return "", fmt.Errorf("station prefers no paper QSL - use None to decline the card")
	}
	if method == "" {
		method = "B" // legacy default when nothing suggests otherwise
	}
	// Manager: keep the recorded decision; only fill it in when missing.
	manager := ""
	if row.Item != nil {
		manager = row.Item.Manager
	}
	if method == "M" && manager == "" && row.Info != nil {
		manager = row.Info.QSLRoute
		_ = s.store.QueueSetMethod(key, "M", manager)
	}
	if err := s.store.SetQSLSentLocal(key, method); err != nil {
		return "", err
	}
	if err := s.store.QueueSetStatus(key, "sent"); err != nil {
		return "", err
	}
	note := ""
	if method == "M" && manager != "" {
		note = "via " + manager
	}
	_ = s.store.AppendEvent(&store.Event{
		QSLKey: key, Direction: "sent", Method: method,
		Date: time.Now().UTC().Format("20060102"), Source: "manual", Note: note,
	})
	return method, nil
}

func (s *Server) doQueueSend(w http.ResponseWriter, r *http.Request, verb string) {
	key := r.FormValue("key")
	method, err := s.sendOne(key, r.FormValue("method"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errDeclined) {
			http.Error(w, "this card was declined (none) - use None/skip, not Send", status)
			return
		}
		http.Error(w, err.Error(), status)
		return
	}
	if workMode(r) {
		s.renderWork(w, r)
		return
	}
	s.render(w, "queue_row.html", map[string]any{
		"Key": key, "Status": "sent",
		"Msg": fmt.Sprintf("%s via %s (queued for push-back)", verb, method),
	})
}

// batchQueue applies one action to every checked queue row and redirects back
// to the queue page it came from. Declined ("none") cards are left untouched
// by sent/handwrite batches.
func (s *Server) batchQueue(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	keys := r.Form["keys"]
	if action != "sent" && action != "handwrite" && action != "none" && action != "skip" {
		http.Error(w, "action must be sent, handwrite, none or skip", http.StatusBadRequest)
		return
	}
	for _, key := range keys {
		switch action {
		case "sent", "handwrite":
			if _, err := s.sendOne(key, ""); err == nil {
				// counted implicitly by the queue state after redirect
			}
		case "none":
			_ = s.store.QueueSetMethod(key, "N", "")
			_ = s.store.QueueSetStatus(key, "skipped")
			_ = s.store.AppendEvent(&store.Event{
				QSLKey: key, Direction: "sent", Method: "N",
				Date: time.Now().UTC().Format("20060102"), Source: "manual",
				Note: "decision: no paper QSL (batch)",
			})
		case "skip":
			_ = s.store.QueueSetStatus(key, "skipped")
		}
	}
	target := "/queue"
	if r.FormValue("compact") == "1" {
		target += "?compact=1"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderQueueRow re-renders the row fragment after a decision, in the variant
// (full/compact) the client is using. htmx sends HX-Current-URL: the page the
// action came from, which is how compact vs full is detected.
func (s *Server) renderQueueRow(w http.ResponseWriter, r *http.Request, key, msg string) {
	if msg != "" {
		s.render(w, "queue_row.html", map[string]any{"Key": key, "Status": "", "Msg": msg})
		return
	}
	row := s.queueRowFor(key)
	if row.QSO == nil {
		// QSO gone (e.g. stale page): nothing useful to swap in.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	name := "queue_row_full.html"
	if strings.Contains(r.Header.Get("HX-Current-URL"), "compact=1") {
		name = "queue_row_compact.html"
	}
	s.render(w, name, row)
}

// --- sync handlers ---

func (s *Server) htmxSyncPull(w http.ResponseWriter, r *http.Request) {
	o := &sync.Orchestrator{Store: s.store, Clublog: clublog.New(s.cfg.Clublog.Email, s.cfg.Clublog.AppPassword,
		s.cfg.Clublog.Call, s.cfg.Clublog.APIKey), Rules: s.rules}
	_, _, err := o.PullAndUpsert()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	last, _ := s.store.MetaGet("clublog_last_pull_at")
	s.render(w, "sync_status.html", map[string]any{"LastPull": last, "PullErr": ""})
}

func (s *Server) htmxSyncPush(w http.ResponseWriter, r *http.Request) {
	o := &sync.Orchestrator{Store: s.store,
		Clublog: clublog.New(s.cfg.Clublog.Email, s.cfg.Clublog.AppPassword,
			s.cfg.Clublog.Call, s.cfg.Clublog.APIKey)}
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
	n, err := s.rules.EnqueueAll(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if n == 0 {
		_, _ = w.Write([]byte(`<span class="muted">no new QSOs to enqueue</span>`))
	} else {
		_, _ = w.Write([]byte(`<span class="ok">enqueued ` + fmt.Sprintf("%d", n) + ` new QSO(s)</span>`))
	}
}

// --- SSE endpoint ---

// sseEvents is a Server-Sent Events stream. Clients subscribe with
// `new EventSource('/events')`. On each "new_qso" event from the broker, the
// server writes an SSE message. The queue page listens and fetches the new row.
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

// htmxQueueRow renders a single queue row for the given QSO key. Used by the
// queue page's SSE handler to fetch the new row HTML when a "new_qso" event
// arrives. ?compact=1 renders the compact variant to match the compact page.
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
	if row.Item == nil {
		// The QSO exists but is not in the work queue (new_qso fires for every
		// UDP-ingested QSO, eligible or not). Nothing to insert.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	name := "queue_row_full.html"
	if r.URL.Query().Get("compact") == "1" {
		name = "queue_row_compact.html"
	}
	row.ShowCheckbox = true // rows fetched by the queue pages are batch-selectable
	s.render(w, name, row)
}
