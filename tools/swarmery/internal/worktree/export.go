package worktree

import "time"

// Exported surface for internal/wtjanitor — the out-of-band sweeper that has to
// see the same worktrees, and judge the same locks, that this package acts on.
//
// It lives in its own file rather than at the end of worktree.go so the
// acquire/release logic stays one readable unit, and it is deliberately thin:
// every symbol here forwards to the unexported original. Re-implementing either
// the porcelain parser or the lock threshold in the janitor is how the two would
// drift apart and start disagreeing about what exists.

// Entry is one parsed `git worktree list --porcelain` record. It carries the
// fields the parser keeps: the checkout path, the short branch name ("" for a
// detached HEAD), and whether `git worktree lock` holds the checkout.
type Entry struct {
	Path   string
	Branch string
	Locked bool
}

// ParseWorktreeList parses `git worktree list --porcelain` output through this
// package's own parser. Records come back in git's order, which always puts the
// repository's MAIN checkout first — the janitor relies on that to tell the
// main worktree apart without a second git call.
func ParseWorktreeList(out string) []Entry {
	parsed := parseWorktreeList(out)
	entries := make([]Entry, 0, len(parsed))
	for _, e := range parsed {
		entries = append(entries, Entry{Path: e.path, Branch: e.branch, Locked: e.locked})
	}
	return entries
}

// StaleLockAge is the age at which a worktree's index.lock is considered
// abandoned (this package sweeps such locks before acquiring). The janitor uses
// the INVERSE: a lock YOUNGER than this means a git operation is in flight, and
// that worktree must not be touched at all.
func StaleLockAge() time.Duration { return staleLockAge }
