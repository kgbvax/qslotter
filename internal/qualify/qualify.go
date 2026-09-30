// Package qualify decides whether a QSO is eligible for a QSL card.
package qualify

import (
	"strings"

	"github.com/dl9et/qslotter/internal/store"
)

type Rules struct {
	ExcludeModes     []string
	FirstContactOnly bool
	OverrideMarker   string // e.g. "QSL!" - if present in notes, the QSO is force-included
}

func New(rules *Rules) *Rules { return rules }

// Eligible returns true if the QSO should be queued for a QSL card.
// allQSOs is the full log (newest-first) used to detect prior contacts; it
// may be nil, in which case the first-contact check uses the QSO's
// priorContact flag (see EligibleForNewQSO for the UDP fast path).
func (r *Rules) Eligible(q *store.QSO, allQSOs []*store.QSO) (bool, string) {
	// Override: if the notes contain the override marker, force-include.
	if r.OverrideMarker != "" && q.Notes != "" &&
		strings.Contains(strings.ToUpper(q.Notes), strings.ToUpper(r.OverrideMarker)) {
		return true, "override: " + r.OverrideMarker + " in notes"
	}

	// Already sent? Skip.
	if q.QSLSent == "Y" {
		return false, "QSL already sent (per Clublog)"
	}
	if q.QSLSentLocal.Valid && q.QSLSentLocal.String == "Y" {
		return false, "QSL already sent (local)"
	}

	// Mode filter.
	mode := strings.ToUpper(q.Mode)
	for _, ex := range r.ExcludeModes {
		if mode == strings.ToUpper(ex) {
			return false, "mode " + q.Mode + " excluded"
		}
	}
	// Catch FT* family generically if the rule list missed one.
	if strings.HasPrefix(mode, "FT") || strings.HasPrefix(mode, "JS8") ||
		strings.HasPrefix(mode, "WSPR") || strings.HasPrefix(mode, "MSK") ||
		strings.HasPrefix(mode, "FST") {
		return false, "mode " + q.Mode + " excluded (digital prefix)"
	}

	// First-contact-only: is there an *older* QSO with the same call?
	// (Semantics: send a card only for the first-ever QSO with a station.
	// A later QSO with the same call is not eligible; the first one is.)
	if r.FirstContactOnly {
		if allQSOs == nil {
			// Caller asked for first-contact but provided no log slice; can't
			// decide. Fail open (don't enqueue) by reporting ineligible.
			return false, "first-contact check skipped (no log slice)"
		}
		for _, p := range allQSOs {
			if p == q {
				continue
			}
			if p.Call != q.Call {
				continue
			}
			// p is another QSO with the same call. Is it older than q?
			// allQSOs is ordered newest-first by (qso_date DESC, time_on DESC),
			// so "older" means it appears *after* q in the slice. But we can't
			// rely on slice position alone because the same-date edge case is
			// ambiguous; compare dates directly.
			if isOlder(p, q) {
				return false, "prior QSO with " + q.Call + " exists"
			}
		}
	}

	return true, "eligible"
}

// EligibleForNewQSO is the UDP fast path: it checks mode + already-sent +
// override (the cheap rules), and uses priorQSOs (the QSOs already in the
// store for this call, newest-first) for the first-contact check. Pass an
// empty slice to allow the QSO through the first-contact check (no priors).
func (r *Rules) EligibleForNewQSO(q *store.QSO, priorQSOs []*store.QSO) (bool, string) {
	// Override: if the notes contain the override marker, force-include.
	if r.OverrideMarker != "" && q.Notes != "" &&
		strings.Contains(strings.ToUpper(q.Notes), strings.ToUpper(r.OverrideMarker)) {
		return true, "override: " + r.OverrideMarker + " in notes"
	}
	if q.QSLSent == "Y" {
		return false, "QSL already sent (per Clublog)"
	}
	if q.QSLSentLocal.Valid && q.QSLSentLocal.String == "Y" {
		return false, "QSL already sent (local)"
	}
	mode := strings.ToUpper(q.Mode)
	for _, ex := range r.ExcludeModes {
		if mode == strings.ToUpper(ex) {
			return false, "mode " + q.Mode + " excluded"
		}
	}
	if strings.HasPrefix(mode, "FT") || strings.HasPrefix(mode, "JS8") ||
		strings.HasPrefix(mode, "WSPR") || strings.HasPrefix(mode, "MSK") ||
		strings.HasPrefix(mode, "FST") {
		return false, "mode " + q.Mode + " excluded (digital prefix)"
	}
	// First-contact: is there an older QSO with the same call?
	// priorQSOs is newest-first and includes q itself (since UpsertQSO ran
	// before this check). Any QSO that is not q and is older => prior.
	if r.FirstContactOnly {
		for _, p := range priorQSOs {
			if p == q {
				continue
			}
			if p.Call == q.Call && isOlder(p, q) {
				return false, "prior QSO with " + q.Call + " exists"
			}
		}
	}
	return true, "eligible"
}

// isOlder returns true if p is strictly older than q (by QSO date, then time).
func isOlder(p, q *store.QSO) bool {
	if p.QSODate != q.QSODate {
		return p.QSODate < q.QSODate
	}
	return p.TimeOn < q.TimeOn
}

// EnqueueAll scans the full log, applies the rules to each QSO, and enqueues
// newly-eligible ones into the work queue. Already-queued QSOs are left alone
// (the queue's ON CONFLICT clause preserves their existing status / method).
// Returns the number of newly-enqueued QSOs.
//
// This is the bridge between the log (Clublog pull or UDP feed) and the
// actionable "Send Queue" view. It is idempotent: re-running after a sync that
// changes nothing enqueues nothing new.
func (r *Rules) EnqueueAll(st store.Store) (int, error) {
	qsos, err := st.AllQSOs()
	if err != nil {
		return 0, err
	}
	// Load the existing queue keys so we don't re-enqueue (and thus overwrite)
	// items the user has already acted on (printed/sent/skipped).
	existing, err := st.QueueByStatus("queued")
	if err != nil {
		return 0, err
	}
	existingKeys := make(map[string]struct{}, len(existing))
	for _, it := range existing {
		existingKeys[it.QSLKey] = struct{}{}
	}
	// Also skip items already in a terminal status, so a re-compute doesn't
	// resurrect a skipped/sent QSO back to "queued".
	for _, status := range []string{"decided", "printed", "sent", "skipped"} {
		items, err := st.QueueByStatus(status)
		if err != nil {
			return 0, err
		}
		for _, it := range items {
			existingKeys[it.QSLKey] = struct{}{}
		}
	}

	enqueued := 0
	for _, q := range qsos {
		if _, ok := existingKeys[q.QSLKey]; ok {
			continue
		}
		ok, reason := r.Eligible(q, qsos)
		if !ok {
			continue
		}
		err := st.Enqueue(&store.QueueItem{
			QSLKey:        q.QSLKey,
			DesiredMethod: "", // will be filled later by the QSL-determination pass
			Status:        "queued",
			OverrideReason: reasonFor(q, reason),
		})
		if err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

// reasonFor returns the override reason if the QSO was force-included via the
// override marker; otherwise empty (normal eligibility has no reason to log).
func reasonFor(q *store.QSO, reason string) string {
	if strings.HasPrefix(reason, "override:") {
		return reason
	}
	return ""
}