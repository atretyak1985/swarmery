package main

// The daemon's change-request status poller (phase-landing plan, phase 7,
// SC-13): every landpollInterval the landpoll.Poller reads the status of each
// phase whose landing opened a PR/MR, stores it, and flips landing_state to
// merged when the change lands. The manual twin is POST …/landing/refresh
// (internal/api/phase_landing_refresh.go).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/landpoll"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
)

const (
	// landpollEnv is the knob: a Go duration, `0` disables the poller.
	landpollEnv = "SWARMERY_LANDPOLL_INTERVAL"
	// landpollDefaultInterval is the tick when the knob is unset or invalid.
	landpollDefaultInterval = 10 * time.Minute
	// landpollFirstPass is how long after boot the first read runs: late enough
	// to stay out of the boot's own I/O, early enough that a PR merged while the
	// daemon was down shows up within the first minute.
	landpollFirstPass = 30 * time.Second
)

// landpollInterval reads SWARMERY_LANDPOLL_INTERVAL. Unset ⇒ the default, on;
// a zero duration ("0", "0s") ⇒ off; a positive Go duration ⇒ that, on; anything
// else (unparseable, negative) ⇒ the default, on, plus a warning for the boot
// log — a typo must not silently switch the poller off.
func landpollInterval(getenv func(string) string) (every time.Duration, on bool, warn error) {
	raw := strings.TrimSpace(getenv(landpollEnv))
	if raw == "" {
		return landpollDefaultInterval, true, nil
	}
	d, err := time.ParseDuration(raw)
	switch {
	case err == nil && d == 0:
		return 0, false, nil
	case err == nil && d > 0:
		return d, true, nil
	default:
		return landpollDefaultInterval, true,
			fmt.Errorf("landpoll: %s=%q is not a positive Go duration (or 0 to disable); using %s",
				landpollEnv, raw, landpollDefaultInterval)
	}
}

// landpollBootLine is the one boot log line, in the `decide:` line's shape.
func landpollBootLine(every time.Duration, on bool) string {
	if !on {
		return "landpoll: disabled"
	}
	return "landpoll: every " + every.String()
}

// newLandPoller is the production poller: providers over the real process
// boundary and the daemon's credential env, the phase's repo from runRoot (the
// live phase-run service's RunRoot), and the api-side hooks.
func newLandPoller(db *sql.DB, runRoot func(phaseID int64) (string, error)) *landpoll.Poller {
	return &landpoll.Poller{
		DB: db,
		Factory: func(k repoprovider.Kind) (repoprovider.Provider, error) {
			return providers.Factory(k, repoprovider.OSExec{}, credstore.Env)
		},
		RepoDir:       runRoot,
		Publish:       api.PublishPlanUpdated,
		OnAuthExpired: api.MarkVcsAuthExpired,
		Logf:          log.Printf,
	}
}

// runLandpoll runs pass first after start, then every interval, until ctx ends.
// A failed tick is logged and the next one still runs; a tick that changed
// something says how much.
func runLandpoll(ctx context.Context, pass func(context.Context) (checked, changed int, err error),
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
	for {
		checked, changed, err := pass(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			logf("warning: landpoll: %v", err)
		case changed > 0:
			logf("landpoll: read %d change requests, %d changed", checked, changed)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
