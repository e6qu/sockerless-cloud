package main

import (
	"context"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// storeSweepInterval is how often a sweeper runs: far shorter than any
// retention it enforces, and far longer than a request.
const storeSweepInterval = time.Minute

// startStoreSweeper runs sweep once per storeSweepInterval for as long as the
// server runs, so a retention the simulator reports is also a bound on what it
// holds.
func startStoreSweeper(srv *sim.Server, sweep func(now time.Time) int) {
	startStoreSweeperEvery(srv, storeSweepInterval, sweep)
}

// startStoreSweeperEvery is startStoreSweeper for a sweep the service itself
// runs less often.
func startStoreSweeperEvery(srv *sim.Server, interval time.Duration, sweep func(now time.Time) int) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep(time.Now())
			}
		}
	})
}
