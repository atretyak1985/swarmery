// Package dispatch is the task dispatcher (fusion phase 3): it picks Todo board
// tasks and runs each as a real headless Claude Code session (`claude -p
// --session-id <uuid>`) inside a dedicated `swarm/<task-id>` git worktree, with
// Fusion's scheduler safety (DESIGN.md §4.1/§4.4): concurrency + worktree caps,
// a file-scope overlap gate, event-driven scheduling with a poll fallback, two
// pause flags + a kill-switch, an honest-exit sentinel contract, and an
// explicit task↔session link (we pre-generate the UUID — proven by the phase-3
// spike that `claude -p --session-id` is honored on this platform).
//
// Every process boundary is an interface (Runner for `claude`, worktree.Git for
// git) so unit tests run against stubs with no real process spawned — mirroring
// the internal/improve and internal/provision idiom. The Service holds *sql.DB
// but every write goes through its own small method set (single-writer
// discipline); no ad-hoc db.Exec leaks into callers.
package dispatch

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config bounds the dispatcher. Defaults are deliberately conservative
// (Fusion's maxConcurrent=2 / maxWorktrees=4) — this program only ever
// dispatches on explicit user action (a task moved to Todo) plus Poke/poll,
// never auto-claim, so cost is bounded by these caps.
type Config struct {
	MaxConcurrent int           // active runs cap (SWARMERY_MAX_CONCURRENT)
	MaxWorktrees  int           // live worktrees cap (SWARMERY_MAX_WORKTREES)
	PollInterval  time.Duration // fallback sweep cadence
	RunTimeout    time.Duration // hard per-run wall clock (SWARMERY_DISPATCH_TIMEOUT_MIN)
	// Enabled is the master kill-switch: SWARMERY_DISPATCH=0 (or false/off)
	// disables ALL admission. Default enabled. Read once at construction; a
	// restart is required to flip it (the durable pause flags are the runtime
	// knob).
	Enabled bool
}

// Config defaults. Exported so tests and docs reference one source of truth.
const (
	DefaultMaxConcurrent = 2
	DefaultMaxWorktrees  = 4
	DefaultPollInterval  = 15 * time.Second
	DefaultRunTimeout    = 45 * time.Minute
)

// ConfigFromEnv builds a Config from the SWARMERY_* env vars, falling back to
// the conservative defaults for any unset/invalid value.
func ConfigFromEnv() Config {
	c := Config{
		MaxConcurrent: DefaultMaxConcurrent,
		MaxWorktrees:  DefaultMaxWorktrees,
		PollInterval:  DefaultPollInterval,
		RunTimeout:    DefaultRunTimeout,
		Enabled:       dispatchEnabled(),
	}
	if v := envPositiveInt("SWARMERY_MAX_CONCURRENT"); v > 0 {
		c.MaxConcurrent = v
	}
	if v := envPositiveInt("SWARMERY_MAX_WORKTREES"); v > 0 {
		c.MaxWorktrees = v
	}
	if v := envPositiveInt("SWARMERY_DISPATCH_TIMEOUT_MIN"); v > 0 {
		c.RunTimeout = time.Duration(v) * time.Minute
	}
	return c
}

// dispatchEnabled reports the kill-switch state: SWARMERY_DISPATCH=0/false/off
// disables the dispatcher entirely. Default (unset) is enabled. Mirrors
// autoProvisionEnabled's parsing.
func dispatchEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMERY_DISPATCH")))
	return v != "0" && v != "false" && v != "off"
}

// envPositiveInt parses a strictly-positive int from env; 0 signals "unset or
// invalid, use the default" (a zero/negative cap is never meaningful here).
func envPositiveInt(key string) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
