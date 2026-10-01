// Package intake turns OCR output from a photographed incoming QSL card into
// a match against the local log.
//
// qslotter does not need a faithful transcription of the card. It needs to
// find one QSO it already knows about: pick the OCR token that is (modulo
// OCR-confusable characters) a callsign in the log, take that call's few
// QSOs, and disambiguate them with whatever date/band/mode fragments OCR
// produced. Everything here is pure functions over engine-neutral OCR
// output, so the same matcher serves Apple Vision, Windows OCR or an LLM.
package intake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Line is one recognized line of text with its alternative readings.
type Line struct {
	// Cands holds alternative readings, best first.
	Cands []string `json:"cands"`
	// Conf is the engine's confidence for the best reading, 0..1.
	Conf float64 `json:"conf"`
	// Box is x, y, width, height, normalized to the image.
	Box [4]float64 `json:"box"`
}

// Page is the OCR result for one photo, from one engine configuration.
type Page struct {
	File   string `json:"file"`
	Engine string `json:"engine"`
	Lines  []Line `json:"lines"`
}

// ParseOCR reads JSON Lines, one Page per line, as written by
// tools/ocr-dump.swift. Blank lines are skipped.
func ParseOCR(r io.Reader) ([]Page, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var pages []Page
	n := 0
	for sc.Scan() {
		n++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var p Page
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			return nil, fmt.Errorf("ocr line %d: %w", n, err)
		}
		pages = append(pages, p)
	}
	return pages, sc.Err()
}
