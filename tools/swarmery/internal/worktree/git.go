// Package worktree is a reusable git-worktree lifecycle manager, generalizing
// the ad-hoc worktree usage in internal/improve/apply.go with Fusion's
// hard-won invariants baked in (see the phase doc / DESIGN.md §4.5): explicit
// startPoint pinning, a repo-root guard as a runtime invariant, stale-lock and
// orphan recovery, deterministic branch naming (swarm/<taskID>), and the
// Swarm-Task-Id commit-trailer convention for deterministic task↔commit
// attribution.
//
// The dispatcher (Phase 3) and auto-verification (Phase 6) consume it;
// internal/improve migrates to it opportunistically (not in this phase).
//
// All git invocation goes through the Git interface so unit tests run against a
// scripted stub with no process spawned — mirroring the improve.Runner /
// improve.Exec idiom.
package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Git is the git command boundary. ExecGit is the real implementation; tests
// substitute a stub that records calls and returns canned output.
type Git interface {
	// Run executes `git -C <dir> <args...>` and returns combined stdout+stderr.
	// A non-zero exit is a non-nil error whose message includes an output tail.
	Run(dir string, args ...string) (string, error)
}

// gitTimeout bounds a single git invocation. Worktree operations are local and
// fast; a hang here means a stuck lock or a wedged filesystem, not slow work.
const gitTimeout = 30 * time.Second

// checkoutTimeout bounds the commands that write or delete a whole working tree
// (`worktree add` checks out every file, `worktree remove` deletes them). On a
// busy external volume a ~3k-file checkout took longer than gitTimeout: the add
// finished on disk, but the run start had already failed with a 500.
const checkoutTimeout = 5 * time.Minute

// outputTailBytes caps how much combined output lands in an error message.
const outputTailBytes = 2048

// ExecGit is the production Git: it shells out to the `git` binary (resolved via
// PATH, like improve.ClaudeRunner resolves `claude`).
type ExecGit struct {
	// Timeout overrides gitTimeout when > 0 (tests shrink it; unused by the
	// stub-based unit tests).
	Timeout time.Duration
}

func (g ExecGit) Run(dir string, args ...string) (string, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout(args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// Optional locks off: a read like `status` otherwise takes index.lock to
	// write back its refreshed stat cache, and a run killed at its deadline
	// leaves that 0-byte lock behind with no holder. Commands that must write
	// the index (add, commit, worktree add) still take their lock.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	combined := out.String()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return combined, fmt.Errorf("git %s timed out after %s: %s",
				strings.Join(args, " "), timeout, tail(combined, outputTailBytes))
		}
		return combined, fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, tail(combined, outputTailBytes))
	}
	return combined, nil
}

// defaultTimeout picks the bound for a git invocation: checkoutTimeout for the
// whole-tree commands, gitTimeout for everything else.
func defaultTimeout(args []string) time.Duration {
	if len(args) >= 2 && args[0] == "worktree" && (args[1] == "add" || args[1] == "remove") {
		return checkoutTimeout
	}
	return gitTimeout
}

// tail returns the last <= n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
