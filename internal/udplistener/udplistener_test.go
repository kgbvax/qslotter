package udplistener

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dl9et/qslotter/internal/contact"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qualify"
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

// TestUDPQueuesRepeatContactsAndAnnounces: with the default rules a repeat
// contact enters the decision queue (the operator decides with the history in
// front of them), a forced-in QSO records why, digital QSOs stay out, and every
// queued QSO is announced to open windows.
func TestUDPQueuesRepeatContactsAndAnnounces(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmp, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	tmpConn, _ := net.ListenUDP("udp", tmp)
	addr := tmpConn.LocalAddr().String()
	tmpConn.Close()

	broker := events.New()
	ch, unsub := broker.Subscribe()
	defer unsub()
	rules := &qualify.Rules{ExcludeModes: []string{"FT8"}, OverrideMarker: "QSL!"}
	l := New(addr, st, broker, rules, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	conn, _ := net.Dial("udp", addr)
	defer conn.Close()
	for _, d := range []string{
		"<QSO_DATE:8>20240101<TIME_ON:6>120000<CALL:5>DL1AB<BAND:3>20m<MODE:3>SSB<EOR>",
		"<QSO_DATE:8>20240102<TIME_ON:6>130000<CALL:5>DL1AB<BAND:3>40m<MODE:3>SSB<EOR>", // repeat contact
		"<QSO_DATE:8>20240103<TIME_ON:6>140000<CALL:5>JA1XY<BAND:3>20m<MODE:3>FT8<EOR>", // digital
		"<QSO_DATE:8>20240104<TIME_ON:6>150000<CALL:5>OK1XY<BAND:3>20m<MODE:3>FT8<NOTES:6>QSL! !<EOR>",
	} {
		if _, err := conn.Write([]byte(d)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond) // keep datagram order deterministic
	}

	var changed []string
	deadline := time.After(2 * time.Second)
	for len(changed) < 3 {
		select {
		case ev := <-ch:
			if ev.Type == "queue_changed" {
				changed = append(changed, ev.Data)
			}
		case <-deadline:
			t.Fatalf("queue_changed events = %v, want 3", changed)
		}
	}
	queued, _ := st.QueueList("queued")
	if len(queued) != 3 {
		t.Fatalf("queued = %d, want 3 (2x DL1AB + the forced-in FT8)", len(queued))
	}
	for _, it := range queued {
		switch {
		case strings.HasPrefix(it.QSLKey, "JA1XY"):
			t.Fatalf("digital QSO was queued: %+v", it)
		case strings.HasPrefix(it.QSLKey, "OK1XY"):
			if it.OverrideReason != "override: QSL! in notes" {
				t.Fatalf("forced-in QSO must record why: %+v", it)
			}
		}
	}
}

// TestCurrentContactFeed: the logger's current-contact broadcast (bare
// callsign, N1MM lookupinfo) sets the QSO in progress, an empty datagram
// clears it, and a logged QSO books the card written during it.
func TestCurrentContactFeed(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l := New("127.0.0.1:0", st, events.New(), nil, nil)
	tr := contact.NewTracker(st, nil, nil)
	l.Contacts = tr

	l.handleDatagram([]byte("VU2ATN"))
	if cur := tr.Current(); cur == nil || cur.Call != "VU2ATN" {
		t.Fatalf("bare callsign: %+v", cur)
	}
	l.handleDatagram([]byte(`<lookupinfo><call>EA8XYZ</call><mode>CW</mode></lookupinfo>`))
	if cur := tr.Current(); cur == nil || cur.Call != "EA8XYZ" || cur.Mode != "CW" {
		t.Fatalf("lookupinfo: %+v", cur)
	}
	l.handleDatagram([]byte(""))
	if tr.Current() != nil {
		t.Fatal("an empty datagram clears the QSO in progress")
	}
	l.handleDatagram([]byte(`<RadioInfo><Freq>1402500</Freq></RadioInfo>`))
	l.handleDatagram([]byte("garbage text here"))
	if tr.Current() != nil {
		t.Fatal("other datagrams change nothing")
	}

	l.handleDatagram([]byte("VU2ATN"))
	tr.MarkDecision("VU2ATN", contact.Yes)
	now := time.Now().UTC()
	l.handleDatagram([]byte("<CALL:6>VU2ATN<QSO_DATE:8>" + now.Format("20060102") + "<TIME_ON:6>" + now.Format("150405") + "<BAND:3>20m<MODE:3>SSB<EOR>"))
	qsos, _ := st.RecentQSOsByCall("VU2ATN", 5)
	if len(qsos) != 1 {
		t.Fatalf("QSO not stored: %d", len(qsos))
	}
	it, _ := st.QueueGet(qsos[0].QSLKey)
	if it == nil || it.Status != "decided" {
		t.Fatalf("the card decided during the QSO must be booked: %+v", it)
	}
	if tr.Current() != nil {
		t.Fatal("logging the QSO ends it")
	}
}
