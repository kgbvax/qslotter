package web

// The print queue of the Desk: every card printed (one at a time, in a batch,
// or as a reply on Incoming QSLs) is an item here. Handing the PDF to the
// print system is not printing it: where the system can follow the job
// (CUPS, printer.Watcher) the card leaves the Desk while it prints and is
// marked sent only when the job completed; a failed job brings it back with
// the reason. The panel on the Desk pages (/work/printq) lists the recent
// items with Retry / "it did print" / Dismiss.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/printer"
	"github.com/dl9et/qslotter/internal/store"
)

// Print item states.
const (
	printPrinting = "printing" // handed over, the print system is on it
	printPrinted  = "printed"  // done: the card is sent
	printFailed   = "failed"   // not printed: the card is at the Desk again
)

// printItem is one card sent to the printer.
type printItem struct {
	ID        int
	Call      string
	Keys      []string
	Route     store.Route
	State     string
	Reason    i18n.Msg // why it failed, or a note on a printed card
	Verified  bool     // the print system confirmed the job completed
	Job       printer.Job
	Started   time.Time
	StartedAt string // RFC 3339, for the template's "since"
}

// printQueue holds the recent print items (newest first) and the QSOs being
// printed right now.
type printQueue struct {
	mu    sync.Mutex
	seq   int
	items []*printItem
	busy  map[string]int // QSO key -> item ID while its card prints

	// How the watch polls the print system and when it gives up on a job
	// that does not finish (fields so tests can shorten them).
	pollEvery time.Duration
	giveUp    time.Duration
}

const keepPrintItems = 30

func newPrintQueue() *printQueue {
	return &printQueue{busy: map[string]int{}, pollEvery: 1500 * time.Millisecond, giveUp: 3 * time.Minute}
}

// add records a new item (newest first, the oldest finished ones dropped).
func (q *printQueue) add(it *printItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	it.ID = q.seq
	it.Started = time.Now()
	it.StartedAt = it.Started.UTC().Format(time.RFC3339)
	q.items = append([]*printItem{it}, q.items...)
	if len(q.items) > keepPrintItems {
		var kept []*printItem
		for i, x := range q.items {
			if i < keepPrintItems || x.State == printPrinting {
				kept = append(kept, x)
			}
		}
		q.items = kept
	}
	if it.State == printPrinting {
		for _, k := range it.Keys {
			q.busy[k] = it.ID
		}
	}
}

// finish sets the outcome of an item and frees its QSOs.
func (q *printQueue) finish(it *printItem, state string, reason i18n.Msg, verified bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	it.State, it.Reason, it.Verified = state, reason, verified
	for _, k := range it.Keys {
		if q.busy[k] == it.ID {
			delete(q.busy, k)
		}
	}
}

// printing reports whether a QSO's card is being printed right now.
func (q *printQueue) printing(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.busy[key]
	return ok
}

// get returns a copy of the item with this ID.
func (q *printQueue) get(id int) (printItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		if it.ID == id {
			return *it, true
		}
	}
	return printItem{}, false
}

// remove drops a finished item from the list.
func (q *printQueue) remove(id int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, it := range q.items {
		if it.ID == id && it.State != printPrinting {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
}

// snapshot copies the items for rendering.
func (q *printQueue) snapshot() []printItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]printItem, len(q.items))
	for i, it := range q.items {
		out[i] = *it
	}
	return out
}

// lastFailure is the reason the newest item holding key failed (zero when
// the newest one did not fail): shown on the card back at the Desk.
func (q *printQueue) lastFailure(key string) i18n.Msg {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		for _, k := range it.Keys {
			if k == key {
				if it.State == printFailed {
					return it.Reason
				}
				return i18n.Msg{}
			}
		}
	}
	return i18n.Msg{}
}

// errPrinting: the card is already being printed (a second press, another
// window).
var errPrinting = errors.New("this card is being printed right now - its result appears in the print list")

// publishPrint tells open Desk pages that the print list changed.
func (s *Server) publishPrint() {
	if s.broker != nil {
		s.broker.Publish(events.Event{Type: "print_changed", Data: "{}"})
	}
}

// watchPrint follows a submitted job until it completed (the card is sent),
// failed or did not finish in time (cancelled; the card returns to the Desk).
func (s *Server) watchPrint(it *printItem, w printer.Watcher) {
	q := s.pq
	ctx, cancel := context.WithTimeout(context.Background(), q.giveUp)
	defer cancel()
	tick := time.NewTicker(q.pollEvery)
	defer tick.Stop()
	var last printer.JobStatus
	errs := 0
	for {
		st, err := w.JobStatus(ctx, it.Job)
		switch {
		case err != nil && strings.Contains(err.Error(), "0x0406"):
			// CUPS forgets finished jobs when it keeps no history: gone
			// means done.
			s.printDone(it, false, i18n.Msg{})
			return
		case err != nil:
			errs++
			if errs >= 10 {
				s.printFail(it, i18n.M("the print system does not answer (%s) - check whether the card printed", err.Error()))
				return
			}
		case st.Failed():
			s.printFail(it, i18n.M("%s", st.Reason()))
			return
		case st.State == printer.JobCompleted:
			s.printDone(it, true, i18n.Msg{})
			return
		default:
			errs, last = 0, st
		}
		select {
		case <-ctx.Done():
			reason := i18n.M("the printer did not finish the card in time")
			if last.State != 0 {
				reason = i18n.M("the printer did not finish the card in time (%s)", last.Reason())
			}
			// Do not let it print later on its own: the operator decides.
			cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := w.CancelJob(cctx, it.Job); err != nil {
				log.Printf("print: cancelling job %d on %s: %v", it.Job.ID, it.Job.Printer, err)
			}
			ccancel()
			s.printFail(it, reason)
			return
		case <-tick.C:
		}
	}
}

// printDone marks the item's QSOs sent.
func (s *Server) printDone(it *printItem, verified bool, note i18n.Msg) {
	if err := s.store.QueuePrinted(it.Keys, it.Route); err != nil {
		// Handled elsewhere meanwhile (another window): it printed anyway.
		note = i18n.M("printed, but the card was changed in the meantime (%s)", err.Error())
		log.Printf("print: %s: %v", it.Call, err)
	}
	s.pq.finish(it, printPrinted, note, verified)
	for _, k := range it.Keys {
		s.publishQueueChanged(k, "sent")
	}
	s.publishPrint()
}

// printFail records the failure; the card is at the Desk again.
func (s *Server) printFail(it *printItem, reason i18n.Msg) {
	log.Printf("print: card to %s failed: %s", it.Call, reason.String())
	s.pq.finish(it, printFailed, reason, false)
	for _, k := range it.Keys {
		s.publishQueueChanged(k, "decided")
	}
	s.publishPrint()
}

// htmxPrintQueue renders the print list (Desk pages, live refresh).
func (s *Server) htmxPrintQueue(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "printq", nil)
}

// htmxPrintClear drops every finished item.
func (s *Server) htmxPrintClear(w http.ResponseWriter, r *http.Request) {
	s.pq.mu.Lock()
	var kept []*printItem
	for _, it := range s.pq.items {
		if it.State == printPrinting {
			kept = append(kept, it)
		}
	}
	s.pq.items = kept
	s.pq.mu.Unlock()
	s.publishPrint()
	s.htmxPrintQueue(w, r)
}

func (s *Server) printItemParam(w http.ResponseWriter, r *http.Request) (printItem, bool) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	it, ok := s.pq.get(id)
	if !ok {
		s.fail(w, r, http.StatusNotFound, "this print job is no longer in the list")
	}
	return it, ok
}

// htmxPrintRetry prints a failed card again.
func (s *Server) htmxPrintRetry(w http.ResponseWriter, r *http.Request) {
	it, ok := s.printItemParam(w, r)
	if !ok {
		return
	}
	if it.State != printFailed {
		s.fail(w, r, http.StatusConflict, "only a failed card can be printed again")
		return
	}
	if _, err := s.printCard(it.Keys, it.Route); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.queueErr(w, r, err)
			return
		}
		// The new attempt is in the list with its reason.
		s.notice(w, r, "Printing failed again: %s", err.Error())
	} else {
		s.pq.remove(it.ID)
	}
	s.htmxPrintQueue(w, r)
}

// htmxPrintMarkSent records a failed card as printed after all (it came out
// of the printer although the print system reported a problem).
func (s *Server) htmxPrintMarkSent(w http.ResponseWriter, r *http.Request) {
	it, ok := s.printItemParam(w, r)
	if !ok {
		return
	}
	if it.State != printFailed {
		s.fail(w, r, http.StatusConflict, "only a failed card can be marked as printed")
		return
	}
	if err := s.store.QueuePrinted(it.Keys, it.Route); err != nil {
		s.queueErr(w, r, err)
		return
	}
	s.pq.mu.Lock()
	for _, x := range s.pq.items {
		if x.ID == it.ID {
			x.State, x.Reason, x.Verified = printPrinted, i18n.M("marked as printed by hand"), false
		}
	}
	s.pq.mu.Unlock()
	for _, k := range it.Keys {
		s.publishQueueChanged(k, "sent")
	}
	s.publishPrint()
	s.htmxPrintQueue(w, r)
}

// htmxPrintDismiss removes a finished item from the list.
func (s *Server) htmxPrintDismiss(w http.ResponseWriter, r *http.Request) {
	it, ok := s.printItemParam(w, r)
	if !ok {
		return
	}
	s.pq.remove(it.ID)
	s.publishPrint()
	s.htmxPrintQueue(w, r)
}
