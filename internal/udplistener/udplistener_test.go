package udplistener

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/store"
)

// TestListenerRoundTrip sends a fake ADIF datagram to the listener's UDP port
// and verifies that the QSO is upserted into the store and a new_qso event is
// published on the broker.
func TestListenerRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	broker := events.New()
	// Find a free UDP port by binding to :0, reading the chosen port, then
	// closing and handing it to the listener (small race window, acceptable
	// for a test).
	tmp, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tmpConn, err := net.ListenUDP("udp", tmp)
	if err != nil {
		t.Fatal(err)
	}
	addr := tmpConn.LocalAddr().String()
	tmpConn.Close()

	l := New(addr, st, broker, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	// Subscribe to the broker so we can see the new_qso event.
	ch, unsub := broker.Subscribe()
	defer unsub()

	// Send a fake ADIF datagram (one QSO).
	adif := "<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<RST_SENT:2>59<EOR>"
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(adif)); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// Wait for the event (or timeout).
	select {
	case ev := <-ch:
		if ev.Type != "new_qso" {
			t.Fatalf("event type = %q, want new_qso", ev.Type)
		}
		if ev.Data == "" {
			t.Fatal("event data (qsl_key) is empty")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for new_qso event")
	}

	// Verify the QSO landed in the store.
	qsos, err := st.RecentQSOsByCall("DL1AB", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(qsos) != 1 {
		t.Fatalf("store has %d QSOs, want 1", len(qsos))
	}
	if qsos[0].Band != "20m" {
		t.Fatalf("band = %q, want 20m", qsos[0].Band)
	}
}

// TestListenerIgnoresN1MMXML verifies that the listener doesn't choke on a
// datagram containing N1MM <contact> XML; it logs and drops. (Configuring
// Log4OM to use ADIF format is the supported path.)
func TestListenerIgnoresN1MMXML(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	broker := events.New()
	tmp, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	tmpConn, _ := net.ListenUDP("udp", tmp)
	addr := tmpConn.LocalAddr().String()
	tmpConn.Close()

	l := New(addr, st, broker, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = l.Start(ctx)
	defer l.Stop()

	conn, _ := net.Dial("udp", addr)
	_, _ = conn.Write([]byte(`<?xml version="1.0"?><contact><call>DL1AB</call></contact>`))
	conn.Close()

	// Give the listener a moment to process, then confirm nothing landed.
	time.Sleep(100 * time.Millisecond)
	qsos, _ := st.AllQSOs()
	if len(qsos) != 0 {
		t.Fatalf("expected 0 QSOs after N1MM XML datagram, got %d", len(qsos))
	}
}