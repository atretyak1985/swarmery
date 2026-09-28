package wtjanitor

import (
	"fmt"
	"time"
)

// Classify decides what to do with ONE worktree. now and minIdle are passed in
// (never read from the clock here) so the decision is a pure function of its
// inputs — the property the safety tests rely on.
//
// Veto order matters: a live worktree must never be inspected further, and a
// young one must never be removed just because its content happens to be in
// git. Only after every veto has passed does the verdict get computed, and only
// then is a single unproven path enough to force salvage.
func Classify(repoRoot string, wt Worktree, g Git, now time.Time, minIdle time.Duration) (Decision, error) {
	if wt.IsMain {
		return Decision{VerdictSkip, "main checkout"}, nil
	}
	// `git worktree lock` is the operator saying "keep this". Without the veto
	// the sweep reached `git worktree remove`, git refused, and the journal got
	// a failed "redundant" row every tick.
	if wt.Locked {
		return Decision{VerdictSkip, "locked (git worktree lock)"}, nil
	}
	if wt.Live {
		return Decision{VerdictSkip, "live process or session"}, nil
	}
	if wt.LockFresh {
		return Decision{VerdictSkip, "git operation in flight (fresh index.lock)"}, nil
	}
	if wt.NewestMTime.IsZero() {
		return Decision{VerdictSkip, "mtime unknown"}, nil
	}
	if age := now.Sub(wt.NewestMTime); age < minIdle {
		return Decision{VerdictSkip, fmt.Sprintf("idle %s < %s", age.Truncate(time.Second), minIdle)}, nil
	}

	// A branch with commits of its own is work, whatever its worktree looks
	// like. Deciding this BEFORE the content check is what keeps a branch like
	// swarm/plan-147 out of the sweep entirely.
	if wt.HasOwnCommits {
		return Decision{VerdictKeepUnmerged, "branch has unmerged commits"}, nil
	}

	// Short-circuit: ONE path absent from git already forces salvage, so stop at
	// the first miss rather than proving the whole set.
	for _, p := range wt.Dirty {
		inGit, err := g.BlobInGit(repoRoot, wt.Path, p)
		if err != nil {
			return Decision{}, fmt.Errorf("wtjanitor: blob check %s: %w", p, err)
		}
		if !inGit {
			return Decision{VerdictSalvage, fmt.Sprintf("%s not in git", p)}, nil
		}
	}
	if len(wt.Dirty) == 0 {
		return Decision{VerdictRedundant, "clean worktree, no own commits"}, nil
	}
	return Decision{VerdictRedundant, fmt.Sprintf("all %d dirty path(s) already in git", len(wt.Dirty))}, nil
}
