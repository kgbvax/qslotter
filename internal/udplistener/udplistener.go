// Package udplistener receives Log4OM's UDP ADIF broadcast and upserts each
// QSO into the store. Log4OM fires this sub-second on QSO added. Each UDP
// datagram is one ADIF record terminated by <EOR>. The same port takes the
// logger's "current contact" broadcast (Log4OM CALLSIGN service: the bare
// callsign; N1MM-family <lookupinfo>): the QSO in progress (package contact).
//
// Log4OM configuration (Settings → Program Configuration → UDP Functions):
//   - Outbound destination: 127.0.0.1:1273 (or whatever qslotter's udp.listen
//     is set to in config.yaml)
//   - Format: ADIF
//   - Enable on QSO added
//
// See https://www.log4om.com/integrated/ for the UDP integration overview.
package udplistener

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	gosync "sync"
	"time"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/station"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/internal/sync"
)

// Listener is the UDP receiver. Call Start to begin reading; Stop to shut down.
type Listener struct {
	addr      string
	store     store.Store
	broker    *events.Broker
	rules     *qualify.Rules
	refresher *station.Refresher
	conn      *net.UDPConn

	// Contacts, when set, receives the QSO in progress and is told about
	// every newly logged QSO (a card written during it gets booked).
	Contacts *contact.Tracker

	ignoreOnce  gosync.Once
	lastUnknown time.Time
}

// New returns a Listener bound to addr (e.g. "127.0.0.1:1273").
// rules may be nil to skip auto-enqueuing; refresher may be nil to skip QRZ
// lookups (e.g. when QRZ credentials are not configured).
func New(addr string, st store.Store, broker *events.Broker, rules *qualify.Rules, refresher *station.Refresher) *Listener {
	return &Listener{addr: addr, store: st, broker: broker, rules: rules, refresher: refresher}
}

// Start opens the UDP socket and begins reading in a goroutine. It returns
// immediately. The socket is closed when ctx is cancelled or Stop is called.
func (l *Listener) Start(ctx context.Context) error {
	a, err := net.ResolveUDPAddr("udp", l.addr)
	if err != nil {
		return fmt.Errorf("resolve udp %s: %w", l.addr, err)
	}
	conn, err := net.ListenUDP("udp", a)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", l.addr, err)
	}
	l.conn = conn
	go l.readLoop(ctx)
	return nil
}

// Stop closes the UDP socket. It is safe to call after Start.
func (l *Listener) Stop() {
	if l.conn != nil {
		_ = l.conn.Close()
	}
}

func (l *Listener) readLoop(ctx context.Context) {
	buf := make([]byte, 8192) // a single QSO ADIF record is well under 1KB
	for {
		if ctx.Err() != nil {
			return
		}
		n, _, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "closed") {
				return
			}
			log.Printf("udplistener read: %v", err)
			continue
		}
		l.handleDatagram(buf[:n])
	}
}

// handleDatagram parses one ADIF record from the datagram and upserts it.
// If the datagram contains N1MM <contact> XML instead of ADIF, it is skipped
// with a warning (configure Log4OM to use ADIF format, not N1MM).
func (l *Listener) handleDatagram(data []byte) {
	trimmed := bytes.TrimSpace(data)
	switch kind, c := contact.Classify(trimmed); kind {
	case contact.KindContact:
		if l.Contacts != nil {
			l.Contacts.Set(c)
		}
		return
	case contact.KindClear:
		if l.Contacts != nil {
			l.Contacts.Clear()
		}
		return
	case contact.KindIgnore:
		l.ignoreOnce.Do(func() {
			log.Printf("udplistener: ignoring N1MM XML datagrams other than <lookupinfo> - logged QSOs must come as ADIF")
		})
		return
	case contact.KindPartial:
		return // a call still being typed
	case contact.KindUnknown:
		if now := time.Now(); now.Sub(l.lastUnknown) > time.Minute { // at most one line a minute
			l.lastUnknown = now
			snippet := string(trimmed)
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			log.Printf("udplistener: ignoring datagram - neither ADIF nor a callsign (%q)", snippet)
		}
		return
	}
	rec, err := adif.NewReader(bytes.NewReader(trimmed)).Read()
	if err != nil {
		log.Printf("udplistener: parse: %v (datagram=%q)", err, string(trimmed))
		return
	}
	if rec == nil {
		return
	}
	q, err := sync.ToQSOFromRecord(rec)
	if err != nil {
		log.Printf("udplistener: to qso: %v", err)
		return
	}
	isNew, _, err := l.store.UpsertQSO(q)
	if err != nil {
		log.Printf("udplistener: upsert: %v", err)
		return
	}
	if isNew {
		// Auto-enqueue the single new QSO if it passes the qualifier rules.
		// Uses the fast path (mode + override + first-contact via the call's
		// recent QSOs) rather than re-scanning the whole log on every datagram.
		if l.rules != nil {
			priorQSOs, err := l.store.RecentQSOsByCall(q.Call, 100000) // all: first on this band / satellite
			if err != nil {
				// Fail open: better to show a card the operator can decide on
				// than to lose the QSO to a transient store error.
				log.Printf("udplistener: prior QSOs for %s: %v", q.Call, err)
			}
			ok, reason := l.rules.EligibleForNewQSO(q, priorQSOs)
			if ok {
				item := &store.QueueItem{QSLKey: q.QSLKey, Status: "queued"}
				item.OverrideReason = qualify.ReasonFor(reason) // why a normally-filtered QSO is here
				if err := l.store.Enqueue(item); err != nil {
					log.Printf("udplistener: enqueue %s: %v", q.QSLKey, err)
				} else if l.broker != nil {
					l.broker.Publish(events.QueueChanged(q.QSLKey, "queued"))
				}
			}
		}
		// Trigger an async QRZ station-info refresh so the Method/Manager
		// columns populate shortly after the QSO appears in the queue. The
		// station_updated SSE event then refreshes the affected rows in place.
		if l.refresher != nil {
			go l.refresher.Get(context.Background(), q.Call)
		}
		if l.broker != nil {
			l.broker.Publish(events.Event{Type: "new_qso", Data: q.QSLKey})
		}
		if l.Contacts != nil {
			l.Contacts.QSOLogged(q)
		}
	}
}
