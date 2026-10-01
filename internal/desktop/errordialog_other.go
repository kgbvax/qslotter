//go:build !windows

package desktop

import (
	"log"

	"github.com/crgimenes/native/alert"
)

// ShowError shows a modal error message (startup failures: an app started
// from the Dock or a desktop entry has no console to print to).
func ShowError(title, msg string) {
	if _, err := alert.Show(alert.Options{Title: title, Message: msg, Buttons: []alert.Button{{Title: "OK"}}}); err != nil {
		log.Printf("desktop: error dialog: %v", err)
	}
}
