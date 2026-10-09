// Package qualify decides whether a QSO is eligible for a QSL card.
package qualify

import (
	"log"
	"slices"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/store"
)

type Rules struct {
	ExcludeModes   []string
	OverrideMarker string // e.g. "QSL!" - if present in notes, the QSO is force-included
	Since          string // YYYYMMDD: QSOs before this date are not queued ("" = no cutoff)

	// filter is what the operator chose under Settings. Atomic: Settings
	// switches it while the UDP feed and the sync loop read.
	filter atomic.Pointer[Filter]
}

// Filter is the operator's choice of which QSOs ask for a card decision: a
// matrix of rows (how the QSO was made) and columns (how new the contact
// is). Every QSO lands in one cell; a true cell asks, a false one skips.
type Filter [len(Rows)][len(Cols)]bool

// Rows and columns of the Filter, by their config names.
var (
	Rows = [...]string{GroupPhone, GroupCW, GroupKeyboard, GroupDigital, RowSatellite}
	Cols = [...]string{ColNew, ColBand, ColRepeat}
)

const (
	RowSatellite = "satellite"

	ColNew    = "new"    // the station was never worked before
	ColBand   = "band"   // worked, but not on this band (satellite row: not via this satellite)
	ColRepeat = "repeat" // worked on this band (via this satellite) before
)

// DefaultFilter: every cell asks but the FT8 & co. row.
var DefaultFilter = func() Filter {
	var f Filter
	for r := range Rows {
		for c := range Cols {
			f[r][c] = Rows[r] != GroupDigital
		}
	}
	return f
}()

// FilterFrom reads the filter from the configuration: qualify.ask, else
// the older include_digital / first_contact_only switches.
func FilterFrom(c config.QualifyCfg) Filter {
	f := DefaultFilter
	if c.Ask == nil {
		for c2 := range Cols {
			f[slices.Index(Rows[:], GroupDigital)][c2] = c.IncludeDigital
		}
		if c.FirstContactOnly {
			for r := range Rows {
				f[r][1], f[r][2] = false, false
			}
		}
		return f
	}
	for r, row := range Rows {
		cols, ok := c.Ask[row]
		if !ok {
			continue // a row the config does not name keeps its default
		}
		for ci, col := range Cols {
			f[r][ci] = slices.ContainsFunc(cols, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), col) })
		}
	}
	return f
}

// Ask is the filter as qualify.ask: per row the columns that ask.
func (f Filter) Ask() map[string][]string {
	out := make(map[string][]string, len(Rows))
	for r, row := range Rows {
		out[row] = []string{}
		for c, col := range Cols {
			if f[r][c] {
				out[row] = append(out[row], col)
			}
		}
	}
	return out
}

func New(rules *Rules) *Rules { return rules }

// Filter is the filter in force (DefaultFilter until one is set).
func (r *Rules) Filter() Filter {
	if f := r.filter.Load(); f != nil {
		return *f
	}
	return DefaultFilter
}

// SetFilter switches the filter, effective at once.
func (r *Rules) SetFilter(f Filter) { r.filter.Store(&f) }

// NewRules builds the rules from the configuration. The default cutoff is the
// day qslotter first ran (remembered in the store), so only QSOs from then on
// enter the decision queue; qualify.since: all lifts it.
func NewRules(c config.QualifyCfg, st store.Store) *Rules {
	r := &Rules{
		ExcludeModes:   c.ExcludeModes,
		OverrideMarker: c.OverrideMarker,
	}
	r.SetFilter(FilterFrom(c))
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

// IsDigital reports the machine digital families (FT8/FT4/FT2, FST4, JS8,
// WSPR, MSK144): the mode group "digital".
func IsDigital(mode string) bool {
	mode = strings.ToUpper(mode)
	for _, p := range []string{"FT", "JS8", "WSPR", "MSK", "FST"} {
		if strings.HasPrefix(mode, p) {
			return true
		}
	}
	return false
}

// Mode groups (ModeGroup).
const (
	GroupPhone    = "phone"
	GroupCW       = "cw"
	GroupKeyboard = "keyboard"
	GroupDigital  = "digital"
)

// ModeGroup sorts an ADIF mode into phone (SSB, FM, AM, digital voice), CW,
// digital (IsDigital) or keyboard - RTTY and every other mode.
func ModeGroup(mode string) string {
	switch m := strings.ToUpper(strings.TrimSpace(mode)); {
	case IsDigital(m):
		return GroupDigital
	case m == "CW":
		return GroupCW
	case m == "SSB" || m == "USB" || m == "LSB" || m == "FM" || m == "AM" || m == "DIGITALVOICE" ||
		m == "DSTAR" || m == "C4FM" || m == "DMR" || m == "FREEDV" || m == "M17":
		return GroupPhone
	}
	return GroupKeyboard
}

// IsSatellite: the QSO went via a satellite (ADIF PROP_MODE SAT).
func IsSatellite(q *store.QSO) bool { return strings.EqualFold(strings.TrimSpace(q.PropMode), "SAT") }

// Row is q's row in the Filter: satellite whatever the mode, else the mode
// group.
func Row(q *store.QSO) int {
	if IsSatellite(q) {
		return slices.Index(Rows[:], RowSatellite)
	}
	return slices.Index(Rows[:], ModeGroup(q.Mode))
}

// Column is q's column in the Filter: how new the contact is, from the
// older QSOs among priors (the QSOs with the same call; may include q).
func Column(q *store.QSO, priors []*store.QSO) int {
	before, same := false, false
	sat := IsSatellite(q)
	for _, p := range priors {
		if p == q || p.QSLKey == q.QSLKey || !strings.EqualFold(p.Call, q.Call) || !isOlder(p, q) {
			continue
		}
		before = true
		if sat {
			if IsSatellite(p) && strings.EqualFold(strings.TrimSpace(p.SatName), strings.TrimSpace(q.SatName)) {
				same = true
			}
		} else if !IsSatellite(p) && strings.EqualFold(p.Band, q.Band) {
			same = true
		}
	}
	switch {
	case !before:
		return 0
	case !same:
		return 1
	}
	return 2
}

// check is the single eligibility decision. priors are the other QSOs with
// the same call (newest-first, may include q itself); without them (nil)
// the QSO counts as a new station.
func (r *Rules) check(q *store.QSO, priors []*store.QSO) (bool, string) {
	// A card that already went out is never queued again - not even by the
	// override marker on an old, already-handled QSO.
	if q.SentPerLog() {
		return false, "QSL already sent (per Clublog)"
	}
	if q.QSLSentLocal.Valid && q.QSLSentLocal.String == "Y" {
		return false, "QSL already sent (local)"
	}
	// The override marker force-includes the QSO despite the filter and
	// the cutoff.
	if r.hasOverride(q) {
		return true, "override: " + r.OverrideMarker + " in notes"
	}
	if r.Since != "" && q.QSODate < r.Since {
		return false, "before qualify.since (" + r.Since + ")"
	}
	// exclude_modes (exact names) skips further modes outside the digital
	// row (older configs listed FT4/FT8/... there - the matrix alone
	// decides for those).
	if ModeGroup(q.Mode) != GroupDigital {
		for _, ex := range r.ExcludeModes {
			if strings.EqualFold(q.Mode, ex) {
				return false, "mode " + q.Mode + " excluded"
			}
		}
	}
	row, col := Row(q), Column(q, priors)
	if !r.Filter()[row][col] {
		return false, "filter: " + Rows[row] + " / " + Cols[col]
	}
	return true, "eligible"
}

// Eligible returns true if the QSO should be queued for a QSL card.
// allQSOs is the full log (newest-first) used to tell how new the contact
// is; nil counts it as a new station.
func (r *Rules) Eligible(q *store.QSO, allQSOs []*store.QSO) (bool, string) {
	return r.check(q, allQSOs)
}

// EligibleForNewQSO is the UDP fast path: the same rules, with priorQSOs (the
// QSOs already in the store for this call, newest-first, including q itself)
// for the first-contact check instead of the whole log.
func (r *Rules) EligibleForNewQSO(q *store.QSO, priorQSOs []*store.QSO) (bool, string) {
	return r.check(q, priorQSOs)
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
	for _, status := range []string{"queued", "decided", "toprint", "printing", "sent", "skipped", "requested"} {
		items, err := st.QueueByStatus(status)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			existingKeys[it.QSLKey] = struct{}{}
		}
	}

	byCall := make(map[string][]*store.QSO)
	for _, q := range qsos {
		c := strings.ToUpper(q.Call)
		byCall[c] = append(byCall[c], q)
	}
	var enqueued []string
	for _, q := range qsos {
		if _, ok := existingKeys[q.QSLKey]; ok {
			continue
		}
		ok, reason := r.check(q, byCall[strings.ToUpper(q.Call)])
		if !ok {
			continue
		}
		err := st.Enqueue(&store.QueueItem{
			QSLKey:         q.QSLKey,
			Status:         "queued",
			OverrideReason: ReasonFor(reason),
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

// ReasonFor is what the queue item keeps of an eligibility reason: why a
// QSO the filter would skip is here (the override marker); "" for a plain
// eligible QSO.
func ReasonFor(reason string) string {
	if strings.HasPrefix(reason, "override:") {
		return reason
	}
	return ""
}
