package intake

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/store"
)

// Class is the outcome of matching one photographed card.
type Class string

const (
	// ClassAuto: one callsign and one clearly best QSO; safe to pre-select
	// for a one-tap confirmation.
	ClassAuto Class = "auto"
	// ClassPick: the callsign was found but the QSO (or the callsign) is
	// ambiguous; show the operator a pick list.
	ClassPick Class = "pick"
	// ClassMiss: no callsign on the card matches the log.
	ClassMiss Class = "miss"
)

// Candidate is a QSO that might be the one the card confirms.
type Candidate struct {
	QSO     *store.QSO
	Score   int
	Reasons []string
}

// Result is the outcome for one page.
type Result struct {
	Class      Class
	Hits       []CallHit   // all callsigns found, best first
	Candidates []Candidate // QSOs of the best-tier callsigns, best first
	Fields     Fields
}

// Resolve matches one OCR page against the log. qsos maps each callsign in
// pool to the QSOs that may be confirmed (the caller decides whether already
// received QSOs are included). myCall is excluded from matching.
func Resolve(p Page, pool *Pool, qsos map[string][]*store.QSO, myCall string) Result {
	f := ExtractFields(p)
	hits := pool.Match(CallTokens(p), myCall)
	res := Result{Class: ClassMiss, Hits: hits, Fields: f}
	if len(hits) == 0 {
		return res
	}
	top := hits[0].Tier
	calls := 0
	for _, h := range hits {
		if h.Tier != top {
			break
		}
		calls++
		for _, q := range qsos[h.Call] {
			res.Candidates = append(res.Candidates, ScoreQSO(q, f))
		}
	}
	if len(res.Candidates) == 0 {
		return res
	}
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		a, b := res.Candidates[i], res.Candidates[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.QSO.QSODate > b.QSO.QSODate
	})
	res.Class = ClassPick
	if top >= TierSkeleton && calls == 1 {
		c := res.Candidates
		if len(c) == 1 || (c[0].Score >= 3 && c[0].Score-c[1].Score >= 2) {
			res.Class = ClassAuto
		}
	}
	return res
}

func parseQSODate(s string) (time.Time, bool) {
	s = strings.ReplaceAll(s, "-", "")
	t, err := time.Parse("20060102", s)
	return t, err == nil
}

func parseTimeOn(s string) (int, bool) {
	s = strings.ReplaceAll(s, ":", "")
	if len(s) < 4 {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[2:4])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return h*60 + m, true
}

// ScoreQSO scores how well a QSO fits the fields read from the card: exact
// date +3, date off by one day +2 (UTC versus local), band +1, mode +1, time
// within 30 minutes +1.
func ScoreQSO(q *store.QSO, f Fields) Candidate {
	c := Candidate{QSO: q}
	if qd, ok := parseQSODate(q.QSODate); ok {
		date := 0
		for _, d := range f.Dates {
			switch diff := qd.Sub(d); {
			case diff == 0:
				date = 3
			case (diff == 24*time.Hour || diff == -24*time.Hour) && date < 2:
				date = 2
			}
		}
		if date > 0 {
			c.Score += date
			if date == 3 {
				c.Reasons = append(c.Reasons, "date")
			} else {
				c.Reasons = append(c.Reasons, "date±1d")
			}
		}
	}
	for _, b := range f.Bands {
		if strings.EqualFold(b, q.Band) {
			c.Score++
			c.Reasons = append(c.Reasons, "band")
			break
		}
	}
	for _, m := range f.Modes {
		if modeMatches(m, q.Mode) {
			c.Score++
			c.Reasons = append(c.Reasons, "mode")
			break
		}
	}
	if qt, ok := parseTimeOn(q.TimeOn); ok {
		for _, t := range f.Times {
			d := qt - t
			if d < 0 {
				d = -d
			}
			if d > 720 {
				d = 1440 - d
			}
			if d <= 30 {
				c.Score++
				c.Reasons = append(c.Reasons, "time")
				break
			}
		}
	}
	return c
}
