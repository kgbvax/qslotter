package contact

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/store"
)

// PendingTTL is how long a "written now" for a QSO in progress waits for the
// QSO to be logged.
const PendingTTL = 12 * time.Hour

// loggedSlack: a QSO counts as the one in progress when it started no
// earlier than this before the call was entered (TIME_ON may be set when the
// call is typed; clocks drift) - an older QSO with the station arriving
// through a Clublog pull neither takes the card nor ends the QSO.
const loggedSlack = 30 * time.Minute

// staleAfter / currentTTL: a QSO in progress the logger never clears (no
// "cleared" datagram) is shown as stale, then dropped.
const (
	staleAfter = 5 * time.Minute
	currentTTL = 15 * time.Minute
)

// Queue reasons of a QSO booked from a decision made during it that the
// qualifier would not have queued.
const (
	ReasonWritten = "written during the QSO"
	ReasonDecided = "decided during the QSO"
)

// Decision is what the operator settled for the QSO in progress.
type Decision string

const (
	Written Decision = "written" // card filled in during the QSO, with its route
	Yes     Decision = "yes"     // card wanted: goes to the Desk, route chosen there
	No      Decision = "no"      // no card
)

// Queue status a booked decision ends in.
func (d Decision) status() string {
	switch d {
	case Yes:
		return "decided"
	case No:
		return "skipped"
	}
	return "sent"
}

// lookupDelay lets the entry field settle before QRZ is asked (a logger may
// broadcast while the call is still being typed).
const lookupDelay = 1500 * time.Millisecond

// Current is the QSO in progress.
type Current struct {
	Contact
	Since    time.Time // the call was entered
	LastSeen time.Time // the logger last sent it
	Stale    bool      // not seen for a while (the logger may not send "cleared")
}

// Pending is a decision made during a QSO that is not logged yet. The QSO may
// never be logged: nothing is stored until it is, and the decision is
// dropped (Cancel, or PendingTTL).
type Pending struct {
	Call     string
	Decision Decision
	Route    store.Route // Written only
	At       time.Time   // marked
	Since    time.Time   // the QSO in progress started (call entered)
}

// Applied is a pending decision that was booked when its QSO was logged.
type Applied struct {
	Call     string
	QSLKey   string
	Decision Decision
	Route    store.Route
	At       time.Time
	Err      string // set when booking failed
}

// Lookup starts a background QRZ lookup for a call (station.Refresher.Get).
type Lookup func(ctx context.Context, call string)

// Tracker holds the QSO in progress and the decisions made during QSOs that
// are not logged yet. Safe for concurrent use.
type Tracker struct {
	store  store.Store
	broker *events.Broker
	lookup Lookup
	now    func() time.Time

	mu      sync.Mutex
	current *Current
	pending map[string]Pending // by upper-case call
	applied *Applied           // the last booking, for the Inbox to report
}

// NewTracker returns a tracker; broker and lookup may be nil.
func NewTracker(st store.Store, broker *events.Broker, lookup Lookup) *Tracker {
	return &Tracker{store: st, broker: broker, lookup: lookup, now: time.Now, pending: map[string]Pending{}}
}

// changed tells open windows that the QSO in progress (or a pending card)
// changed.
func (t *Tracker) changed(call string) {
	if t.broker != nil {
		t.broker.Publish(events.Event{Type: "current_contact", Data: call})
	}
}

// Set records the station now in the logger's entry field.
func (t *Tracker) Set(c Contact) {
	c.Call = strings.ToUpper(strings.TrimSpace(c.Call))
	now := t.now()
	t.mu.Lock()
	same := t.current != nil && t.current.Call == c.Call && now.Sub(t.current.LastSeen) <= currentTTL
	unchanged := same && t.current.Contact == c
	if same {
		t.current = &Current{Contact: c, Since: t.current.Since, LastSeen: now}
	} else {
		t.current = &Current{Contact: c, Since: now, LastSeen: now}
	}
	t.mu.Unlock()
	if unchanged {
		return // a repeat (Space/Tab, field exit): nothing to show anew
	}
	t.changed(c.Call)
	if same || t.lookup == nil {
		return
	}
	go func() { // only when the call stays: no lookups for half-typed calls
		time.Sleep(lookupDelay)
		if cur := t.Current(); cur != nil && cur.Call == c.Call {
			t.lookup(context.Background(), c.Call)
		}
	}()
}

// Clear forgets the QSO in progress (the entry field was emptied). Pending
// decisions stay until their QSO is logged (or they are cancelled or expire:
// the QSO may never be logged).
func (t *Tracker) Clear() {
	t.mu.Lock()
	had := t.current != nil
	t.current = nil
	t.mu.Unlock()
	if had {
		t.changed("")
	}
}

// Current returns the QSO in progress, or nil (also once the logger has not
// sent it for currentTTL).
func (t *Tracker) Current() *Current {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	idle := t.now().Sub(t.current.LastSeen)
	if idle > currentTTL {
		t.current = nil
		return nil
	}
	c := *t.current
	c.Stale = idle > staleAfter
	return &c
}

// MarkWritten remembers a card written now (bureau or direct) for the QSO in
// progress with call; it is booked on the QSO as soon as the QSO is logged.
func (t *Tracker) MarkWritten(call string, rt store.Route) {
	t.mark(call, Written, rt)
}

// MarkDecision remembers the card / no card decision (Yes, No) for the QSO in
// progress with call; it is booked on the QSO as soon as the QSO is logged.
func (t *Tracker) MarkDecision(call string, d Decision) {
	t.mark(call, d, store.Route{})
}

func (t *Tracker) mark(call string, d Decision, rt store.Route) {
	call = strings.ToUpper(strings.TrimSpace(call))
	t.mu.Lock()
	p := Pending{Call: call, Decision: d, Route: rt, At: t.now(), Since: t.now()}
	if t.current != nil && t.current.Call == call {
		p.Since = t.current.Since
	}
	t.pending[call] = p
	t.mu.Unlock()
	t.changed(call)
}

// startedAt is a QSO's start (QSO_DATE + TIME_ON, UTC).
func startedAt(q *store.QSO) (time.Time, bool) {
	layout := "20060102150405"
	if len(q.TimeOn) == 4 {
		layout = "200601021504"
	}
	ts, err := time.Parse(layout, q.QSODate+q.TimeOn)
	return ts, err == nil
}

// isThatQSO reports whether q plausibly is the QSO that was in progress since
// since: it started no earlier than since (minus slack) and not in the future.
func (t *Tracker) isThatQSO(q *store.QSO, since time.Time) bool {
	start, ok := startedAt(q)
	return ok && !start.Before(since.Add(-loggedSlack)) && !start.After(t.now().Add(loggedSlack))
}

// Cancel forgets a pending decision (also the way out when the QSO will never
// be logged).
func (t *Tracker) Cancel(call string) {
	call = strings.ToUpper(strings.TrimSpace(call))
	t.mu.Lock()
	delete(t.pending, call)
	t.mu.Unlock()
	t.changed(call)
}

// Pending lists the decisions waiting for their QSO, oldest first (expired
// ones are dropped).
func (t *Tracker) Pending() []Pending {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Pending
	for k, p := range t.pending {
		if t.now().Sub(p.At) > PendingTTL {
			delete(t.pending, k)
			continue
		}
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// LastApplied returns the last booking of a pending decision, or nil.
func (t *Tracker) LastApplied() *Applied {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.applied == nil {
		return nil
	}
	a := *t.applied
	return &a
}

// QSOLogged is called for every newly stored QSO (UDP feed, Clublog pull).
// A decision made during that QSO is booked on it (card: to the Desk, no
// card, or "written now" with its route); the QSO in progress with that call
// is over.
func (t *Tracker) QSOLogged(q *store.QSO) {
	call := strings.ToUpper(q.Call)
	t.mu.Lock()
	p, ok := t.pending[call]
	if !ok { // logged under a portable form of the call: the same station
		for k, cand := range t.pending {
			if store.BaseCall(k) == store.BaseCall(call) {
				if ok { // two candidates: do not guess
					ok = false
					break
				}
				p, ok = cand, true
			}
		}
	}
	if ok && t.now().Sub(p.At) > PendingTTL {
		delete(t.pending, p.Call)
		ok = false
	}
	// Only the QSO that was in progress takes the decision: an older QSO with
	// the station (e.g. back-filled by a Clublog pull) leaves it waiting.
	if ok && !t.isThatQSO(q, p.Since) {
		ok = false
	}
	if ok {
		delete(t.pending, p.Call)
	}
	endsCurrent := t.current != nil && store.BaseCall(t.current.Call) == store.BaseCall(call) && t.isThatQSO(q, t.current.Since)
	if endsCurrent {
		t.current = nil
	}
	t.mu.Unlock()

	if ok {
		a := &Applied{Call: q.Call, QSLKey: q.QSLKey, Decision: p.Decision, Route: p.Route, At: t.now()}
		if err := t.book(q, p); err != nil {
			a.Err = err.Error()
			log.Printf("contact: booking the %s decision made during the QSO with %s: %v", p.Decision, q.Call, err)
		} else if t.broker != nil {
			t.broker.Publish(events.QueueChanged(q.QSLKey, p.Decision.status()))
		}
		t.mu.Lock()
		t.applied = a
		t.mu.Unlock()
	}
	if ok || endsCurrent {
		t.changed(call)
	}
}

// book records the decision on the logged QSO. A QSO without a queue item
// (digital mode, before the cutoff, ...) gets one first - the operator did
// decide - and for "no card" this is what keeps the qualifier (a Clublog pull
// runs it after this) from putting the QSO into the Inbox.
func (t *Tracker) book(q *store.QSO, p Pending) error {
	item, err := t.store.QueueGet(q.QSLKey)
	if err != nil {
		return err
	}
	if item == nil {
		reason := ReasonDecided
		if p.Decision == Written {
			reason = ReasonWritten
		}
		if err := t.store.Enqueue(&store.QueueItem{QSLKey: q.QSLKey, Status: "queued", OverrideReason: reason}); err != nil {
			return err
		}
	}
	switch p.Decision {
	case Yes:
		return t.store.QueueAccept(q.QSLKey)
	case No:
		return t.store.QueueDecline(q.QSLKey)
	}
	return t.store.QueueWrittenNow([]string{q.QSLKey}, p.Route)
}
