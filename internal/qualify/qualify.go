// Package qualify decides whether a QSO is eligible for a QSL card.
package qualify

import (
	"log"
	"strings"
	gosync "sync"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/store"
)

type Rules struct {
	ExcludeModes     []string
	FirstContactOnly bool
	OverrideMarker   string // e.g. "QSL!" - if present in notes, the QSO is force-included
	Since            string // YYYYMMDD: QSOs before this date are not queued ("" = no cutoff)
}

func New(rules *Rules) *Rules { return rules }

// NewRules builds the rules from the configuration. The default cutoff is the
// day qslotter first ran (remembered in the store), so only QSOs from then on
// enter the decision queue; qualify.since: all lifts it.
func NewRules(c config.QualifyCfg, st store.Store) *Rules {
	r := &Rules{
		ExcludeModes:     c.ExcludeModes,
		FirstContactOnly: c.FirstContactOnly,
		OverrideMarker:   c.OverrideMarker,
	}
	switch v := strings.ToLower(strings.TrimSpace(c.Since)); v {
	case "all", "none":
	case "":
		r.Since = firstRunDate(st)
	default:
		if t, err := time.Parse("2006-01-02", v); err == nil {
			r.Since = t.Format("20060102")
		} else if t, err := time.Parse("20060102", v); err == nil {
			r.Since = t.Format("20060102")
		} else {
			log.Printf("qualify: ignoring invalid qualify.since %q (want YYYY-MM-DD, empty or \"all\")", c.Since)
			r.Since = firstRunDate(st)
		}
	}
	return r
}

// firstRunDate returns (and on first call records) the day qslotter first ran.
func firstRunDate(st store.Store) string {
	today := time.Now().UTC().Format("20060102")
	if st == nil {
		return today
	}
	if v, err := st.MetaGet("first_run_date"); err == nil && v != "" {
		return v
	}
	_ = st.MetaSet("first_run_date", today)
	return today
}

// hasOverride reports whether the operator forced this QSO in via the marker.
func (r *Rules) hasOverride(q *store.QSO) bool {
	return r.OverrideMarker != "" && q.Notes != "" &&
		strings.Contains(strings.ToUpper(q.Notes), strings.ToUpper(r.OverrideMarker))
}

// isDigital reports the digital families that never get a paper card.
func isDigital(mode string) bool {
	mode = strings.ToUpper(mode)
	for _, p := range []string{"FT", "JS8", "WSPR", "MSK", "FST"} {
		if strings.HasPrefix(mode, p) {
			return true
		}
	}
	return false
}

// check is the single eligibility decision. priors are the other QSOs with
// the same call (newest-first, may include q itself); havePriors=false means
// the caller could not supply them, which fails the first-contact check open.
func (r *Rules) check(q *store.QSO, priors []*store.QSO, havePriors bool) (bool, string) {
	// A card that already went out is never queued again - not even by the
	// override marker on an old, already-handled QSO.
	if q.QSLSent == "Y" {
		return false, "QSL already sent (per Clublog)"
	}
	if q.QSLSentLocal.Valid && q.QSLSentLocal.String == "Y" {
		return false, "QSL already sent (local)"
	}
	// The override marker force-includes the QSO despite mode, cutoff and
	// first-contact rules.
	if r.hasOverride(q) {
		return true, "override: " + r.OverrideMarker + " in notes"
	}
	if r.Since != "" && q.QSODate < r.Since {
		return false, "before qualify.since (" + r.Since + ")"
	}
	mode := strings.ToUpper(q.Mode)
	for _, ex := range r.ExcludeModes {
		if mode == strings.ToUpper(ex) {
			return false, "mode " + q.Mode + " excluded"
		}
	}
	if isDigital(mode) {
		return false, "mode " + q.Mode + " excluded (digital prefix)"
	}
	// First-contact-only: only the first-ever QSO with a station is eligible;
	// a later QSO with the same call is not.
	if r.FirstContactOnly {
		if !havePriors {
			return false, "first-contact check skipped (no log slice)"
		}
		for _, p := range priors {
			if p == q || p.QSLKey == q.QSLKey || p.Call != q.Call {
				continue
			}
			if isOlder(p, q) {
				return false, "prior QSO with " + q.Call + " exists"
			}
		}
	}
	return true, "eligible"
}

// Eligible returns true if the QSO should be queued for a QSL card.
// allQSOs is the full log (newest-first) used to detect prior contacts; it
// may be nil, in which case the first-contact check (if enabled) fails closed.
func (r *Rules) Eligible(q *store.QSO, allQSOs []*store.QSO) (bool, string) {
	return r.check(q, allQSOs, allQSOs != nil)
}

// EligibleForNewQSO is the UDP fast path: the same rules, with priorQSOs (the
// QSOs already in the store for this call, newest-first, including q itself)
// for the first-contact check instead of the whole log.
func (r *Rules) EligibleForNewQSO(q *store.QSO, priorQSOs []*store.QSO) (bool, string) {
	return r.check(q, priorQSOs, true)
}

// isOlder returns true if p is strictly older than q (by QSO date, then time).
func isOlder(p, q *store.QSO) bool {
	if p.QSODate != q.QSODate {
		return p.QSODate < q.QSODate
	}
	return p.TimeOn < q.TimeOn
}

// enqueueMu serializes full scans (Recompute button, Clublog pulls, the
// background loop) so two of them never decide on the same QSOs at once.
var enqueueMu gosync.Mutex

// EnqueueAll scans the full log, applies the rules to each QSO, and enqueues
// newly-eligible ones into the decision queue. Items already present in any
// status keep their state (Enqueue never overwrites), so a recompute cannot
// resurrect or reset work the operator did. Returns the number enqueued.
func (r *Rules) EnqueueAll(st store.Store) (int, error) {
	keys, err := r.EnqueueAllKeys(st)
	return len(keys), err
}

// EnqueueAllKeys is EnqueueAll returning the keys it enqueued, so callers can
// announce them to open windows.
func (r *Rules) EnqueueAllKeys(st store.Store) ([]string, error) {
	enqueueMu.Lock()
	defer enqueueMu.Unlock()
	qsos, err := st.AllQSOs()
	if err != nil {
		return nil, err
	}
	// Every QSO that already has a queue item (any status) is left alone.
	existingKeys := make(map[string]struct{})
	for _, status := range []string{"queued", "decided", "printed", "sent", "skipped", "requested"} {
		items, err := st.QueueByStatus(status)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			existingKeys[it.QSLKey] = struct{}{}
		}
	}

	var enqueued []string
	for _, q := range qsos {
		if _, ok := existingKeys[q.QSLKey]; ok {
			continue
		}
		ok, reason := r.Eligible(q, qsos)
		if !ok {
			continue
		}
		err := st.Enqueue(&store.QueueItem{
			QSLKey:         q.QSLKey,
			Status:         "queued",
			OverrideReason: reasonFor(q, reason),
		})
		if err != nil {
			return enqueued, err
		}
		enqueued = append(enqueued, q.QSLKey)
	}
	return enqueued, nil
}

// DiscardBacklog files the Inbox backlog: QSOs dated before the cutoff
// (r.Since) that still wait for a decision become "no card" (note "backlog",
// reopenable from Done). It runs once per cutoff - again only when the cutoff
// moves later - so a backlog card the operator reopened stays in the Inbox.
func (r *Rules) DiscardBacklog(st store.Store) (int, error) {
	if r.Since == "" {
		return 0, nil // qualify.since: all - there is no backlog
	}
	done, err := st.MetaGet("backlog_discarded_before")
	if err != nil {
		return 0, err
	}
	if done != "" && done >= r.Since {
		return 0, nil
	}
	n, err := st.QueueDiscardBacklog(r.Since)
	if err != nil {
		return 0, err
	}
	return n, st.MetaSet("backlog_discarded_before", r.Since)
}

// reasonFor returns the override reason if the QSO was force-included via the
// override marker; otherwise empty (normal eligibility has no reason to log).
func reasonFor(q *store.QSO, reason string) string {
	if strings.HasPrefix(reason, "override:") {
		return reason
	}
	return ""
}
