package desktop

import (
	"errors"
	"log"

	"github.com/crgimenes/native/openurl"
	"github.com/crgimenes/native/singleinstance"
)

// ErrAlreadyRunning: another qslotter instance holds the single-instance lock.
var ErrAlreadyRunning = errors.New("qslotter is already running")

// SingleInstance makes this process the only qslotter, or - if one is
// already running - asks that one to show itself (onShow runs there) and
// returns ErrAlreadyRunning. release drops the lock at exit.
func SingleInstance(id string, onShow func()) (release func(), err error) {
	inst, err := singleinstance.Acquire(id, singleinstance.Options{
		OnMessage: func(args []string) {
			if len(args) > 0 && args[0] == "show" {
				onShow()
			}
		},
	})
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		if serr := singleinstance.Send(id, []string{"show"}); serr != nil {
			log.Printf("single instance: notify the running one: %v", serr)
		}
		return func() {}, ErrAlreadyRunning
	}
	if err != nil {
		return func() {}, err
	}
	return func() { _ = inst.Release() }, nil
}

// OpenExternal opens url in the system browser (links that leave qslotter,
// e.g. a QRZ page, must not replace the app window's content).
func OpenExternal(url string) error { return openurl.Open(url) }
