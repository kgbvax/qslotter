// Package udplistener receives Log4OM's UDP ADIF broadcast and upserts each
// QSO into the store. Log4OM fires this sub-second on QSO added. Each UDP
// datagram is one ADIF record terminated by <EOR>.
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

	"github.com/dl9et/qslotter/internal/adif"
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
	if len(trimmed) == 0 {
		return
	}
	// Detect N1MM <contact> XML envelope and skip with a warning.
	if bytes.HasPrefix(trimmed, []byte("<contact")) || bytes.HasPrefix(trimmed, []byte("<?xml")) {
		log.Printf("udplistener: ignoring N1MM XML datagram - set Log4OM UDP format to ADIF")
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
			priorQSOs, _ := l.store.RecentQSOsByCall(q.Call, 50)
			ok, _ := l.rules.EligibleForNewQSO(q, priorQSOs)
			if ok {
				_ = l.store.Enqueue(&store.QueueItem{
					QSLKey: q.QSLKey,
					Status: "queued",
				})
			}
		}
		// Trigger an async QRZ station-info refresh so the Method/Manager
		// columns populate shortly after the QSO appears in the queue. The
		// station_updated SSE event then refreshes the affected rows in place.
		if l.refresher != nil {
			go l.refresher.Refresh(context.Background(), q.Call)
		}
		if l.broker != nil {
			l.broker.Publish(events.Event{Type: "new_qso", Data: q.QSLKey})
		}
	}
}