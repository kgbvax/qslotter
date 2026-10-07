//go:build windows

package printer

import "testing"

// TestSumatraSettings checks the print settings: never duplex, the paper
// and tray by the driver's number, the card turned only when it lies
// across the paper as fed or when the config says so.
func TestSumatraSettings(t *testing.T) {
	card := Options{PaperWMM: 140, PaperHMM: 90, Copies: 2}
	wide := &Media{Name: "QSL", ID: 260, WMM: 140, HMM: 90}
	tall := &Media{Name: "QSL hoch", ID: 261, WMM: 90, HMM: 140}
	manual := &Media{Name: "Manueller Einzug", ID: 4}
	cw, ccw := card, card
	cw.Rotate, ccw.Rotate = 90, 270
	for _, c := range []struct {
		opts        Options
		paper, tray *Media
		landscape   bool
		want        string
	}{
		{card, nil, nil, false, "noscale,simplex,disable-auto-rotation,2x"},
		{card, wide, manual, false, "noscale,simplex,disable-auto-rotation,portrait,paperkind=260,bin=4,2x"},
		{card, tall, nil, false, "noscale,simplex,disable-auto-rotation,landscape,paperkind=261,2x"},
		{card, wide, nil, true, "noscale,simplex,disable-auto-rotation,landscape,paperkind=260,2x"},
		{card, tall, nil, true, "noscale,simplex,disable-auto-rotation,portrait,paperkind=261,2x"},
		{cw, nil, manual, false, "noscale,simplex,disable-auto-rotation,landscape,bin=4,2x"},
		{ccw, tall, nil, false, "noscale,simplex,portrait,paperkind=261,2x"},
	} {
		if got := sumatraSettings(c.opts, c.paper, c.tray, c.landscape); got != c.want {
			t.Errorf("sumatraSettings(%+v, %v, %v, %v) = %q, want %q", c.opts, c.paper, c.tray, c.landscape, got, c.want)
		}
	}
}
