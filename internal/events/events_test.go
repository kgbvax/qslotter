package events

import (
	"testing"
	"time"
)

func TestBrokerPubSub(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	defer unsub()

	b.Publish(Event{Type: "new_qso", Data: "DL1AB|20240101|120000|20M"})
	select {
	case ev := <-ch:
		if ev.Type != "new_qso" || ev.Data != "DL1AB|20240101|120000|20M" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestBrokerUnsubscribe(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	unsub()
	// Publish after unsubscribe should not block and should not deliver.
	b.Publish(Event{Type: "x"})
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("received unexpected event after unsubscribe: %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		// Good - channel was closed and no event arrived.
	}
}

func TestBrokerDropOnSlowSubscriber(t *testing.T) {
	b := New()
	// Subscriber with a tiny buffer; we won't drain it.
	ch, unsub := b.Subscribe()
	defer unsub()
	for i := 0; i < 20; i++ {
		b.Publish(Event{Type: "new_qso", Data: "x"})
	}
	// The buffer is 16, so at most 16 events were delivered; the rest dropped.
	// Verify Publish did not block (we reached here) and the channel has
	// <= 16 events buffered.
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			if n > 16 {
				t.Fatalf("buffered %d events, expected <= 16", n)
			}
			return
		}
	}
}
