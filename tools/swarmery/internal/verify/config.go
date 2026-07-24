// Package verify is auto-verification (fusion phase 6). After a dispatched
// session ends in_review WITHOUT an honest-exit sentinel, a bounded READ-ONLY
// headless `claude -p` run grades the task's acceptance criteria against the
// work on its swarm/<id> branch and stamps tasks.verify_verdict = pass | fail |
// inconclusive automatically. FAIL spawns at most `retry budget (3,
// root-inherited)` fix tasks; INCONCLUSIVE is first-class and spawns NOTHING
// (verification could not conclude != criteria unmet — DESIGN.md §4.6). A
// tree-hash cache skips re-verifying an unchanged worktree; a stale-run reaper
// unsticks zombie runs; startup heal reclaims rows a crashed daemon left running.
//
// Every process boundary is an interface (Runner for `claude`, worktree.Trees
// for git) so unit tests run against stubs with no real process spawned —
// mirroring internal/dispatch and internal/provision. The Service holds *sql.DB
// but every write goes through its own small method set (single-writer
// discipline); no ad-hoc db.Exec leaks into callers.
package verify

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config bounds the verifier.
type Config struct {
	// Enabled is the master kill-switch for the AUTO trigger: SWARMERY_AUTOVERIFY=0
	// (or false/off) disables auto-verification on dispatched-run exit. The manual
	// POST /api/tasks/{id}/verify endpoint still works when disabled. Default
	// enabled. Read once at construction (mirrors dispatch's dispatchEnabled).
	Enabled bool
	// Concurrency is the verification semaphore size (default 1) so parallel tasks
	// don't stack compiles/test runs. SWARMERY_VERIFY_CONCURRENCY.
	Concurrency int
	// RunTimeout is the hard per-run wall clock. SWARMERY_VERIFY_TIMEOUT_MIN.
	RunTimeout time.Duration
	// RetryBudget is the max fix tasks per ROOT task (Fusion default 3).
	RetryBudget int
	// StaleAfter is how long a `running` verification_runs row may live before the
	// reaper marks it error + stamps the task inconclusive (Fusion 6h; ours 2h for
	// headless runs).
	StaleAfter time.Duration
}

// Config defaults. Exported so tests and docs reference one source of truth.
const (
	DefaultConcurrency = 1
	DefaultRunTimeout  = 15 * time.Minute
	DefaultRetryBudget = 3
	DefaultStaleAfter  = 2 * time.Hour
)

// ConfigFromEnv builds a Config from SWARMERY_* env, falling back to the
// conservative defaults for any unset/invalid value.
func ConfigFromEnv() Config {
	c := Config{
		Enabled:     autoVerifyEnabled(),
		Concurrency: DefaultConcurrency,
		RunTimeout:  DefaultRunTimeout,
		RetryBudget: DefaultRetryBudget,
		StaleAfter:  DefaultStaleAfter,
	}
	if v := envPositiveInt("SWARMERY_VERIFY_CONCURRENCY"); v > 0 {
		c.Concurrency = v
	}
	if v := envPositiveInt("SWARMERY_VERIFY_TIMEOUT_MIN"); v > 0 {
		c.RunTimeout = time.Duration(v) * time.Minute
	}
	return c
}

// autoVerifyEnabled reports the kill-switch state: SWARMERY_AUTOVERIFY=0/false/off
// disables the auto trigger. Default (unset) is enabled. Mirrors dispatchEnabled.
func autoVerifyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMERY_AUTOVERIFY")))
	return v != "0" && v != "false" && v != "off"
}

// envPositiveInt parses a strictly-positive int from env; 0 signals "unset or
// invalid, use the default".
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
