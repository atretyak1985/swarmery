package main

import (
	"context"
	"time"
)

// storeOptimizeFirstDelay is how long after boot the first planner-statistics
// pass waits. It used to run inline before the API listened: on the operator
// store its first-ever pass (no sqlite_stat1 yet, cold cache right after a
// migration) took 12 s, all of it a daemon that was down. Statistics persist
// in the store, so a pass on a store that already has them costs ~0.03 s and
// nothing at boot depends on it; the delay lets ingest's startup replay settle
// before the pass takes the single connection, as the retention prune does.
const storeOptimizeFirstDelay = 2 * time.Minute

// storeOptimizeEvery is how often the daemon refreshes the planner's
// statistics after the first pass. The plain `PRAGMA optimize` re-analyzes
// only tables whose row counts drifted, so most ticks are no-ops.
const storeOptimizeEvery = 6 * time.Hour

// runStoreOptimize runs the startup pass (startup=true) once first elapses,
// then the periodic pass every interval, until ctx ends. A failed pass is
// logged and the next one still runs.
func runStoreOptimize(ctx context.Context, pass func(ctx context.Context, startup bool) error,
	first, every time.Duration, logf func(string, ...any)) {
	wait := time.NewTimer(first)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return
	case <-wait.C:
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for startup := true; ; startup = false {
		kind := "periodic"
		if startup {
			kind = "startup"
		}
		start := time.Now()
		if err := pass(ctx, startup); err != nil {
			if ctx.Err() == nil {
				logf("warning: store: optimize (%s): %v", kind, err)
			}
		} else {
			logf("store: optimize (%s) %s", kind, time.Since(start).Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
