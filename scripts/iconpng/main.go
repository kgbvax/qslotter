// Command iconpng draws qslotter's app icon - a QSL card with an airmail
// border and a stamp - as a PNG of any size (macOS .icns, Linux desktop icon).
//
//	go run ./scripts/iconpng -size 1024 -o icon.png
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

func main() {
	size := flag.Int("size", 512, "edge length in pixels")
	out := flag.String("o", "icon.png", "output file")
	flag.Parse()

	s := float64(*size)
	img := image.NewRGBA(image.Rect(0, 0, *size, *size))
	card := color.RGBA{250, 247, 238, 255}
	ink := color.RGBA{30, 36, 48, 255}
	red := color.RGBA{200, 40, 40, 255}
	blue := color.RGBA{30, 70, 160, 255}

	// Card: landscape, rounded corners, centred.
	x0, x1 := 0.06*s, 0.94*s
	y0, y1 := 0.20*s, 0.80*s
	r := 0.05 * s
	border := math.Max(1, 0.012*s)
	stripe := 0.06 * s // airmail band width
	for py := 0; py < *size; py++ {
		for px := 0; px < *size; px++ {
			x, y := float64(px)+0.5, float64(py)+0.5
			if !inRoundRect(x, y, x0, y0, x1, y1, r) {
				continue
			}
			c := card
			if !inRoundRect(x, y, x0+border, y0+border, x1-border, y1-border, math.Max(0, r-border)) {
				c = ink
			} else if !inRoundRect(x, y, x0+border+stripe, y0+border+stripe, x1-border-stripe, y1-border-stripe, 0) {
				// Diagonal red/blue airmail stripes around the edge.
				if int((x+y)/(0.07*s))%2 == 0 {
					c = red
				} else {
					c = blue
				}
			}
			// Stamp: a small ink square top right.
			if x > 0.66*s && x < 0.80*s && y > 0.32*s && y < 0.48*s {
				c = ink
				if x > 0.68*s && x < 0.78*s && y > 0.34*s && y < 0.46*s {
					c = red
				}
			}
			// Address lines.
			for _, ly := range []float64{0.56, 0.62, 0.68} {
				if y > ly*s && y < ly*s+0.018*s && x > 0.22*s && x < 0.62*s {
					c = ink
				}
			}
			img.Set(px, py, c)
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Min(math.Max(x, x0+r), x1-r)
	cy := math.Min(math.Max(y, y0+r), y1-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}
