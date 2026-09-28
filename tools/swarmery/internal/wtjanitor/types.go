package wtjanitor

import "time"

// Verdict is what a sweep decided about ONE worktree.
type Verdict string

const (
	// VerdictSkip: a veto fired (main checkout, live process, too young, or a
	// fresh index.lock). Nothing was touched and nothing is wrong.
	VerdictSkip Verdict = "skip"
	// VerdictKeepUnmerged: the branch carries commits reachable from no other
	// ref. Never removed — this is the swarm/plan-147 case.
	VerdictKeepUnmerged Verdict = "keep-unmerged"
	// VerdictRedundant: clean, or every dirty path's blob is already in git at
	// that same path. Safe to remove.
	VerdictRedundant Verdict = "redundant"
	// VerdictSalvage: holds content that exists nowhere in git. Commit it to a
	// salvage branch FIRST, then remove.
	VerdictSalvage Verdict = "salvage"
)

// Worktree is one entry of `git worktree list --porcelain`, enriched with the
// facts the classifier needs. Everything here is observation, no decisions.
type Worktree struct {
	Path   string // absolute path of the checkout
	Branch string // "" for a detached HEAD
	IsMain bool   // the repository's primary checkout
	// Dirty lists repo-relative paths that are modified or untracked.
	Dirty []string
	// HasOwnCommits reports commits on Branch reachable from no other ref.
	HasOwnCommits bool
	// NewestMTime is the newest modification time among the worktree's files,
	// excluding its .git link. Zero time means "unknown" and vetoes the sweep.
	NewestMTime time.Time
	// LockFresh reports an index.lock younger than the stale threshold — a git
	// operation is in flight in this worktree.
	LockFresh bool
	// Live reports a process cwd'd inside, or a non-terminal session bound to it.
	Live bool
	// Foreign marks a worktree the janitor does not own (owner.go). It is listed
	// only so its branch counts as checked out; nothing inside it is observed,
	// and it is never classified, journalled or removed.
	Foreign bool
}

// Decision pairs a verdict with the reason string the sweep journal records.
type Decision struct {
	Verdict Verdict
	Reason  string
}

// Git is the read side the classifier depends on. The real implementation lives
// in git.go; tests use a stub.
type Git interface {
	// BlobInGit reports whether repoRelPath's CURRENT content in worktreePath
	// already exists in git at that same path, in any commit reachable from any
	// ref. Byte identity via blob sha — never a heuristic.
	BlobInGit(repoRoot, worktreePath, repoRelPath string) (bool, error)
}

// Liveness answers "is anyone using this checkout right now".
type Liveness interface {
	// Busy reports a live process cwd'd inside path, or a non-terminal session
	// row bound to it.
	Busy(path string) (bool, error)
}
