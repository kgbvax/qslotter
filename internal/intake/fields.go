package intake

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Fields are the date, time, band and mode fragments found on a card.
// Every occurrence is kept, not one guess: they only score candidate QSOs.
type Fields struct {
	Dates []time.Time // UTC midnight
	Times []int       // minutes since midnight
	Bands []string    // ADIF spelling, e.g. 20M, 70CM
	Modes []string    // normalized: SSB, CW, FT8, PSK, DIGI, ...
}

var (
	reDMY  = regexp.MustCompile(`\b(\d{1,2})\s*[./\- ]\s*(\d{1,2})\s*[./\- ]\s*(\d{4}|\d{2})\b`)
	reISO  = regexp.MustCompile(`\b(\d{4})\s*[./\-]\s*(\d{1,2})\s*[./\-]\s*(\d{1,2})\b`)
	reDMon = regexp.MustCompile(`(?i)\b(\d{1,2})\s*[./\- ]?\s*(JAN|FEB|MAR|MRZ|APR|MAY|MAI|JUN|JUL|AUG|SEP|OCT|OKT|NOV|DEC|DEZ)[A-Z]*\.?\s*[./\- ]?\s*(\d{4}|\d{2})\b`)

	reTimeColon = regexp.MustCompile(`\b([01]?\d|2[0-3])\s*:\s*([0-5]\d)\b`)
	reTimeZ     = regexp.MustCompile(`(?i)\b([01]\d|2[0-3])([0-5]\d)\s*(?:Z|UTC|GMT)\b`)

	reBandM   = regexp.MustCompile(`(?i)\b(160|80|60|40|30|20|17|15|12|10|6|4|2)\s*M\b`)
	reBandCM  = regexp.MustCompile(`(?i)\b(70|23|13)\s*CM\b`)
	reMHz     = regexp.MustCompile(`(?i)\b(\d{1,3}(?:[.,]\d{1,4})?)\s*MHZ\b`)
	reFreq    = regexp.MustCompile(`\b(\d{1,3})[.,](\d{2,4})\b`)
	reModeAny = regexp.MustCompile(`(?i)\b(SSB|LSB|USB|CW|FT8|FT4|FST4|RTTY|PSK\d*|JT65|JT9|SSTV|MFSK|DIGI|DATA)\b`)
	reModeCap = regexp.MustCompile(`\b(AM|FM)\b`) // upper-case only: "I am" is not a mode
)

var monthNum = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "MRZ": 3, "APR": 4, "MAY": 5, "MAI": 5,
	"JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "OKT": 10,
	"NOV": 11, "DEC": 12, "DEZ": 12,
}

// ExtractFields scans all readings of all lines. Date patterns also run over
// the best readings joined with spaces, so a table with day, month and year
// in separate cells still yields a date. Times are only taken in HH:MM or
// HHMMZ form; a bare four-digit number is too often a year or a frequency.
func ExtractFields(p Page) Fields {
	var f Fields
	var texts, joined []string
	for _, l := range p.Lines {
		texts = append(texts, l.Cands...)
		if len(l.Cands) > 0 {
			joined = append(joined, l.Cands[0])
		}
	}
	dates := map[time.Time]bool{}
	times := map[int]bool{}
	bands := map[string]bool{}
	modes := map[string]bool{}
	for _, t := range append(texts, strings.Join(joined, " ")) {
		for _, d := range findDates(t) {
			if !dates[d] {
				dates[d] = true
				f.Dates = append(f.Dates, d)
			}
		}
	}
	for _, t := range texts {
		for _, m := range findTimes(t) {
			if !times[m] {
				times[m] = true
				f.Times = append(f.Times, m)
			}
		}
		for _, b := range findBands(t) {
			if !bands[b] {
				bands[b] = true
				f.Bands = append(f.Bands, b)
			}
		}
		for _, m := range findModes(t) {
			if !modes[m] {
				modes[m] = true
				f.Modes = append(f.Modes, m)
			}
		}
	}
	return f
}

func mkDate(y, m, d int) (time.Time, bool) {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}

func fullYear(s string) int {
	y, _ := strconv.Atoi(s)
	if len(s) == 2 {
		if y <= 69 {
			return 2000 + y
		}
		return 1900 + y
	}
	return y
}

func findDates(s string) []time.Time {
	var out []time.Time
	add := func(y, m, d int) {
		if t, ok := mkDate(y, m, d); ok {
			out = append(out, t)
		}
	}
	for _, m := range reISO.FindAllStringSubmatch(s, -1) {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		add(y, mo, d)
	}
	for _, m := range reDMY.FindAllStringSubmatch(s, -1) {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		y := fullYear(m[3])
		add(y, b, a) // DD.MM.YY (European, the default)
		if a <= 12 && b <= 12 && a != b {
			add(y, a, b) // MM/DD/YY (US) as an alternative reading
		}
	}
	for _, m := range reDMon.FindAllStringSubmatch(s, -1) {
		d, _ := strconv.Atoi(m[1])
		add(fullYear(m[3]), monthNum[strings.ToUpper(m[2])], d)
	}
	return out
}

func findTimes(s string) []int {
	var out []int
	for _, re := range []*regexp.Regexp{reTimeColon, reTimeZ} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			out = append(out, h*60+mi)
		}
	}
	return out
}

type bandEdge struct {
	lo, hi float64
	name   string
}

var bandEdges = []bandEdge{
	{1.8, 2.0, "160M"}, {3.5, 4.0, "80M"}, {5.25, 5.45, "60M"},
	{7.0, 7.3, "40M"}, {10.1, 10.15, "30M"}, {14.0, 14.35, "20M"},
	{18.068, 18.168, "17M"}, {21.0, 21.45, "15M"}, {24.89, 24.99, "12M"},
	{28.0, 29.7, "10M"}, {50.0, 54.0, "6M"}, {70.0, 71.0, "4M"},
	{144.0, 148.0, "2M"}, {430.0, 440.0, "70CM"}, {1240.0, 1300.0, "23CM"},
}

func bandFromMHz(f float64) (string, bool) {
	for _, e := range bandEdges {
		if f >= e.lo && f <= e.hi {
			return e.name, true
		}
	}
	// "14 MHz" style: whole-MHz band names.
	for _, e := range bandEdges {
		if f == float64(int(f)) && f >= e.lo-0.01 && f <= e.hi {
			return e.name, true
		}
	}
	return "", false
}

func parseMHz(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	return v, err == nil
}

func findBands(s string) []string {
	var out []string
	for _, m := range reBandM.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ToUpper(m[1])+"M")
	}
	for _, m := range reBandCM.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ToUpper(m[1])+"CM")
	}
	for _, m := range reMHz.FindAllStringSubmatch(s, -1) {
		if v, ok := parseMHz(m[1]); ok {
			if b, ok := bandFromMHz(v); ok {
				out = append(out, b)
			}
		}
	}
	// Bare frequencies such as 14.250, but not parts of a date (14.07.24).
	for _, loc := range reFreq.FindAllStringSubmatchIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && strings.ContainsRune("./,-", rune(s[start-1])) {
			continue
		}
		if end+1 < len(s) && strings.ContainsRune("./,-", rune(s[end])) && s[end+1] >= '0' && s[end+1] <= '9' {
			continue
		}
		if v, ok := parseMHz(s[start:end]); ok {
			if b, ok := bandFromMHz(v); ok {
				out = append(out, b)
			}
		}
	}
	return out
}

func findModes(s string) []string {
	var out []string
	for _, m := range reModeAny.FindAllString(s, -1) {
		out = append(out, normMode(m))
	}
	for _, m := range reModeCap.FindAllString(s, -1) {
		out = append(out, m)
	}
	return out
}

// normMode folds sideband and PSK variants: LSB/USB -> SSB, PSK31 -> PSK,
// DATA -> DIGI.
func normMode(m string) string {
	m = strings.ToUpper(strings.TrimSpace(m))
	switch {
	case m == "LSB" || m == "USB":
		return "SSB"
	case strings.HasPrefix(m, "PSK"):
		return "PSK"
	case m == "DATA":
		return "DIGI"
	}
	return m
}

var digitalModes = map[string]bool{
	"FT8": true, "FT4": true, "FST4": true, "RTTY": true, "PSK": true,
	"JT65": true, "JT9": true, "MFSK": true, "SSTV": true, "DIGI": true,
	"JS8": true, "WSPR": true, "MSK144": true, "OLIVIA": true,
}

// modeMatches compares a mode read off the card with a logged QSO mode. DIGI
// on a card matches any digital mode in the log.
func modeMatches(card, qso string) bool {
	card, qso = normMode(card), normMode(qso)
	if card == qso {
		return true
	}
	return card == "DIGI" && digitalModes[qso]
}
