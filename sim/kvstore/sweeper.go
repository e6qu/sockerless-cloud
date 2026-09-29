package kvstore

import (
	"context"
	"time"
)

// Background is the part of sim.Server a sweeper runs on: a worker the server
// cancels and drains before its stores close.
type Background interface {
	StartBackground(name string, worker func(context.Context))
}

// StartSweeper runs sweep every interval until the server stops. Stores use it
// to delete what their time-to-live settings expired; sweep receives the
// tick's time so it judges every item against one instant.
func StartSweeper(srv Background, name string, interval time.Duration, sweep func(now time.Time)) {
	srv.StartBackground(name, func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				// A tick and a stop can be ready together; select picks at random.
				if ctx.Err() != nil {
					return
				}
				sweep(now)
			}
		}
	})
}
