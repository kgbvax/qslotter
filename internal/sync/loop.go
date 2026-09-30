package sync

import (
	"context"
	"log"
	"time"
)

// Loop runs the reconciliation pull and the push-back on tickers until ctx is
// cancelled. An interval <= 0 disables the respective loop (a nil ticker
// channel never fires). The manual "Pull from Clublog" / "Push back to
// Clublog" buttons on the log page remain available regardless.
//
// Returns a channel that is closed once the loop goroutine has fully stopped;
// main waits on it (bounded) during shutdown so a pull is not torn down
// mid-import.
func Loop(ctx context.Context, o *Orchestrator, pullEvery, pushEvery time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pullC, pushC <-chan time.Time
		if pullEvery > 0 {
			t := time.NewTicker(pullEvery)
			defer t.Stop()
			pullC = t.C
		}
		if pushEvery > 0 {
			t := time.NewTicker(pushEvery)
			defer t.Stop()
			pushC = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-pullC:
				inserted, updated, err := o.PullAndUpsert()
				if err != nil {
					log.Printf("sync: background pull: %v", err)
					continue
				}
				if inserted+updated > 0 {
					log.Printf("sync: background pull: %d new, %d updated", inserted, updated)
				}
			case <-pushC:
				pushed, err := o.PushBack()
				if err != nil {
					log.Printf("sync: background push: %v", err)
					continue
				}
				if pushed > 0 {
					log.Printf("sync: background push: %d QSO(s) pushed to Clublog", pushed)
				}
			}
		}
	}()
	return done
}
