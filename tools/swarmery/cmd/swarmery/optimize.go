package main

import (
	"context"
	"time"
)

// storeOptimizeEvery is how often the daemon refreshes the planner's
// statistics after the startup pass. The plain `PRAGMA optimize` re-analyzes
// only tables whose row counts drifted, so most ticks are no-ops.
const storeOptimizeEvery = 6 * time.Hour

// runStoreOptimize runs pass every interval until ctx ends. The startup pass
// runs inline in cmdServe before this starts, so the first tick waits a full
// interval. A failed tick is logged and the next one still runs.
func runStoreOptimize(ctx context.Context, pass func(context.Context) error,
	every time.Duration, logf func(string, ...any)) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		start := time.Now()
		if err := pass(ctx); err != nil {
			if ctx.Err() == nil {
				logf("warning: store: optimize (periodic): %v", err)
			}
			continue
		}
		logf("store: optimize (periodic) %s", time.Since(start).Round(time.Millisecond))
	}
}
