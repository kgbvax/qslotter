// Package events is a tiny in-process pub/sub broker for qslotter.
// It decouples producers (UDP listener, sync orchestrator) from consumers
// (the SSE endpoint in the web layer) without pulling in a dependency.
//
// The broker is fan-out: every subscriber receives every event. Subscribers
// that fall behind (buffer full) drop events rather than block producers.
package events

import "sync"

// Event is a single notification. Type is a short string ("new_qso",
// "qsl_rcvd", ...); Data is type-specific (e.g. a QSL key).
type Event struct {
	Type string
	Data string
}

// Broker fans out Events to all subscribers.
type Broker struct {
	mu     sync.RWMutex
	subs   map[chan Event]struct{}
}

func New() *Broker {
	return &Broker{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of Events. The caller must call Unsubscribe
// when done to avoid leaking the channel (and stop the Broker from sending
// into it after the consumer is gone).
func (b *Broker) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}
}

// Publish broadcasts ev to all subscribers. Slow subscribers drop the event.
func (b *Broker) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// drop - subscriber is too slow
		}
	}
}