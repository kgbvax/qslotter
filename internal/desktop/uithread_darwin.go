//go:build darwin

package desktop

import (
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

// dispatch_async_f onto the main dispatch queue, loaded with purego (no cgo).
// The tray's NSApp run loop drains the main queue, so queued functions run on
// the UI thread.

var (
	dispatchAsyncF func(queue, ctx, work uintptr)
	mainQueue      uintptr
	uiWork         uintptr
	uiMu           sync.Mutex
	uiQueue        []func()
	uiReady        atomic.Bool // set once all of the above are in place
)

// initUIThread resolves libdispatch; call on the UI thread.
func initUIThread() error {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&dispatchAsyncF, lib, "dispatch_async_f")
	q, err := purego.Dlsym(lib, "_dispatch_main_q") // dispatch_get_main_queue() is a macro for &_dispatch_main_q
	if err != nil {
		return err
	}
	mainQueue = q
	uiWork = purego.NewCallback(func(_ uintptr) {
		uiMu.Lock()
		q := uiQueue
		uiQueue = nil
		uiMu.Unlock()
		for _, f := range q {
			f()
		}
	})
	uiReady.Store(true)
	return nil
}

// uiDo runs f on the UI thread; false when the dispatcher is not set up.
func uiDo(f func()) bool {
	if !uiReady.Load() {
		return false
	}
	uiMu.Lock()
	uiQueue = append(uiQueue, f)
	uiMu.Unlock()
	dispatchAsyncF(mainQueue, 0, uiWork)
	return true
}
