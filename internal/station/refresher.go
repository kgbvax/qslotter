// Package station ties together the QRZ XML client, the QSL-method
// determination ladder, and the local station_info cache. It is the single
// entry point the rest of qslotter uses to (re)fetch and cache station info
// for a callsign.
package station

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
)

// Refresher fetches station info from QRZ (on cache miss or TTL expiry),
// runs the QSL-method determination ladder, writes the result to the store
// cache, and publishes a "station_updated" event.
type Refresher struct {
	store    store.Store
	qrz      *qrz.Client
	broker   *events.Broker
	cacheTTL time.Duration

	// inFlight dedups concurrent refreshes for the same callsign so the UDP
	// burst of N QSOs with one callsign triggers only one QRZ lookup. Waiters
	// block on the per-callsign channel until the lookup completes.
	mu       sync.Mutex
	inFlight map[string]chan struct{}
	failedAt map[string]time.Time // last failed lookup per call (cooldown)
}

// A station QRZ has no record of is remembered (negative cache) so pages don't
// re-query it on every render, but only briefly: the operator may register later.
const notFoundTTL = 24 * time.Hour

// After a failed lookup (QRZ outage, bad credentials) the same call is not
// retried by the automatic paths for a while; the Refresh button ignores this.
const failCooldown = 2 * time.Minute

// New returns a Refresher. qrz may be nil (Refresh becomes a no-op); this is
// used during testing or when QRZ creds are not configured.
func New(st store.Store, q *qrz.Client, broker *events.Broker, cacheTTL time.Duration) *Refresher {
	return &Refresher{
		store: st, qrz: q, broker: broker, cacheTTL: cacheTTL,
		inFlight: make(map[string]chan struct{}),
		failedAt: make(map[string]time.Time),
	}
}

// Get returns the cached station info for a callsign, refreshing from QRZ if
// the cache is missing or stale (older than TTL). Safe to call concurrently;
// duplicate refreshes for the same callsign are coalesced.
//
// If QRZ is not configured or the lookup fails, the cached entry (if any) is
// returned with no error; the caller sees stale info and a re-refresh will be
// attempted on the next Get.
func (r *Refresher) Get(ctx context.Context, callsign string) (*store.StationInfo, error) {
	callsign = strings.ToUpper(strings.TrimSpace(callsign))
	si, err := r.store.GetStation(callsign)
	if err != nil {
		return nil, err
	}
	if si != nil && !r.isStale(si) {
		return si, nil
	}
	if r.recentlyFailed(callsign) {
		return si, nil // stale cache (or nothing); don't hammer a failing QRZ
	}
	// Cache miss or stale. Refresh (deduped).
	if err := r.refresh(ctx, callsign); err != nil {
		// Refresh failed; return stale cache if we have one.
		if si != nil {
			return si, nil
		}
		return nil, err
	}
	return r.store.GetStation(callsign)
}

// Refresh forces a re-fetch from QRZ regardless of cache age. It is the
// "Refresh" button entry point. Returns the updated info (or the stale cache
// if the lookup failed).
func (r *Refresher) Refresh(ctx context.Context, callsign string) (*store.StationInfo, error) {
	callsign = strings.ToUpper(strings.TrimSpace(callsign))
	if err := r.refresh(ctx, callsign); err != nil {
		// Fall back to cache.
		return r.store.GetStation(callsign)
	}
	return r.store.GetStation(callsign)
}

// IsStale reports whether cached station info is due for a re-lookup.
func (r *Refresher) IsStale(si *store.StationInfo) bool { return r.isStale(si) }

func (r *Refresher) isStale(si *store.StationInfo) bool {
	ttl := r.cacheTTL
	if si.NotFound && (ttl == 0 || ttl > notFoundTTL) {
		ttl = notFoundTTL
	}
	if ttl == 0 {
		return false
	}
	t, err := time.Parse(time.RFC3339, si.FetchedAt)
	if err != nil {
		return true
	}
	return time.Since(t) > ttl
}

func (r *Refresher) recentlyFailed(callsign string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.failedAt[callsign]
	return ok && time.Since(t) < failCooldown
}

// RecentlyFailed reports a lookup for the call that failed within the
// cooldown (no new attempt is made until it passes).
func (r *Refresher) RecentlyFailed(callsign string) bool {
	return r.recentlyFailed(strings.ToUpper(callsign))
}

// Configured reports whether a QRZ client is set, i.e. lookups can happen.
func (r *Refresher) Configured() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.qrz != nil
}

// SetClient swaps the QRZ client (after the credentials were changed in the
// settings UI) without restarting. New lookups use the new client; existing
// cached station info stays until its TTL expires.
func (r *Refresher) SetClient(q *qrz.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.qrz = q
}

func (r *Refresher) refresh(ctx context.Context, callsign string) error {
	r.mu.Lock()
	qrzClient := r.qrz
	r.mu.Unlock()
	if qrzClient == nil {
		return nil
	}
	// Dedup: if a refresh for this call is already running, wait for it to
	// complete (the cache will then be populated) rather than returning early.
	r.mu.Lock()
	if ch, ok := r.inFlight[callsign]; ok {
		r.mu.Unlock()
		<-ch // wait for the in-flight refresh to finish
		return nil
	}
	ch := make(chan struct{})
	r.inFlight[callsign] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.inFlight, callsign)
		r.mu.Unlock()
		close(ch)
	}()

	// Respect context cancellation.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	cs, err := qrzClient.Lookup(callsign)
	if err != nil {
		r.mu.Lock()
		r.failedAt[callsign] = time.Now()
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	delete(r.failedAt, callsign)
	r.mu.Unlock()
	var bio string
	if cs != nil {
		bio, err = qrzClient.FetchBio(callsign)
		if err != nil {
			log.Printf("station: fetch bio for %s: %v (continuing)", callsign, err)
			bio = ""
		}
	}
	res := qsldetermine.Determine(cs, bio)
	si := &store.StationInfo{
		Callsign:      callsign,
		QSLMgr:        fieldStr(cs, func() string { return cs.QSLMgr }),
		EQSL:          fieldStr(cs, func() string { return cs.EQSL }),
		MQSL:          fieldStr(cs, func() string { return cs.MQSL }),
		LoTW:          fieldStr(cs, func() string { return cs.LoTW }),
		Email:         fieldStr(cs, func() string { return cs.Email }),
		Addr1:         fieldStr(cs, func() string { return cs.Addr1 }),
		Addr2:         fieldStr(cs, func() string { return cs.Addr2 }),
		State:         fieldStr(cs, func() string { return cs.State }),
		Zip:           fieldStr(cs, func() string { return cs.Zip }),
		Country:       fieldStr(cs, func() string { return cs.Country }),
		DXCC:          fieldStr(cs, func() string { return cs.DXCC }),
		Name:          fieldStr(cs, func() string { return strings.TrimSpace(cs.FName + " " + cs.Name) }),
		Attn:          fieldStr(cs, func() string { return cs.Attn }),
		NotFound:      cs == nil,
		BioText:       bio,
		QSLMethod:     res.Method,
		QSLRoute:      res.Manager,
		RefusePaper:   res.RefusePaper,
		QSLConfidence: res.Confidence,
		QSLReason:     res.Reason,
	}
	if err := r.store.PutStation(si); err != nil {
		return err
	}
	if r.broker != nil {
		r.broker.Publish(events.Event{Type: "station_updated", Data: callsign})
	}
	return nil
}

// fieldStr returns the field value from a non-nil Callsign, or "" if cs is nil.
func fieldStr(cs *qrz.Callsign, get func() string) string {
	if cs == nil {
		return ""
	}
	return get()
}
