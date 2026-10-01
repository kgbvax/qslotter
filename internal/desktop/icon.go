package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// trayIcon draws a small QSL-card-like icon (red/blue airmail stripes on a
// card) so the tray needs no asset file.
func trayIcon() []byte {
	const s = 44
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	card := color.RGBA{R: 250, G: 248, B: 240, A: 255}
	ink := color.RGBA{R: 30, G: 36, B: 48, A: 255}
	red := color.RGBA{R: 200, G: 40, B: 40, A: 255}
	blue := color.RGBA{R: 30, G: 70, B: 160, A: 255}
	for y := 6; y < s-6; y++ {
		for x := 2; x < s-2; x++ {
			c := card
			switch {
			case y == 6 || y == s-7 || x == 2 || x == s-3:
				c = ink
			case y < 12:
				if ((x+y)/4)%2 == 0 {
					c = red
				} else {
					c = blue
				}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
