package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrBranchGone: the run's branch no longer exists, so there is nothing left to
// re-attach to. Distinct from a missing DIRECTORY, which is the normal end state
// of a finished run and recoverable — this one means the work itself is gone
// (merged and deleted, or reclaimed as empty), and the caller must say so rather
// than silently checking out something else.
var ErrBranchGone = errors.New("worktree: the run's branch no longer exists")

// ReattachPath re-creates the worktree that used to live at path, checking out
// the branch it already had. It exists for the one case Acquire cannot serve: a
// run ENDED, the janitor removed its directory and kept its branch (Remove with
// keepBranch), and now somebody wants back into that run — to resume its session,
// to ask it a question, to continue where it stopped. The commits are all still
// on swarm/<taskID>; only the checkout was disposable.
//
// It is the deliberate opposite of Acquire in the one respect that matters here.
// Acquire pins a NEW branch to an explicit start point and refuses a name that is
// already taken (ErrBranchExists), because for a fresh run an existing branch is
// a conflict. ReattachPath REQUIRES the branch and moves nothing: no start point,
// no reset, no `branch -D`, and no force-removal of a directory that is present.
// Every failure leaves the repository exactly as it was found.
func (m *Manager) ReattachPath(repoRoot, path string) (Acquired, error) {
	path = filepath.Clean(path)
	taskID := filepath.Base(path)
	branch := branchName(taskID)

	// The namespace guard first: this function checks out a branch at a path, and
	// deriving both from a caller-supplied string is only safe while the string is
	// provably one of ours. taskIDForBranch rejects a multi-component id, which is
	// what makes ownsWorktreePath's single-component reasoning sound.
	if taskIDForBranch(branch) == "" || !m.ownsWorktreePath(path, taskID) {
		return Acquired{}, fmt.Errorf("%w: %s (not a worktree this manager owns)", ErrPathOccupied, path)
	}
	if err := guardRepoRoot(repoRoot, path); err != nil {
		return Acquired{}, err
	}

	// Drop the registration the deleted directory left behind, or `worktree add`
	// below dies on a path git still believes is in use. prune only forgets
	// worktrees whose directory is already gone, so it can never discard a live one.
	_, _ = m.Git.Run(repoRoot, "worktree", "prune")

	list, err := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return Acquired{}, fmt.Errorf("worktree: list worktrees: %w", err)
	}
	entries := parseWorktreeList(list)

	// Somebody else holds the branch — a concurrent run, a manual checkout. Two
	// checkouts of one branch is exactly what git refuses, and yanking theirs is
	// not this function's call to make.
	if other, ok := entries.pathForBranch(branch); ok && !samePath(other, path) {
		return Acquired{}, fmt.Errorf("%w: %s is on %s", ErrBranchBusy, other, branch)
	}

	// Already there and on the right branch: idempotent success, so a caller may
	// call this unconditionally before every resume.
	if reg, ok := entries.byPath(path); ok && reg.branch == branch && dirExists(path) {
		if err := m.CheckComplete(path); err != nil {
			return Acquired{}, err
		}
		syncUntrackedConfig(repoRoot, path)
		lendDependencies(repoRoot, path)
		return m.acquiredFor(repoRoot, path, branch)
	}

	exists, err := m.branchExists(repoRoot, branch)
	if err != nil {
		return Acquired{}, err
	}
	if !exists {
		return Acquired{}, fmt.Errorf("%w: %s", ErrBranchGone, branch)
	}

	// A directory sitting on the path that git does not know about is somebody
	// else's business: Acquire may quarantine such a leftover because it is about
	// to run a task there, but a re-attach has no mandate to move a stranger's
	// files.
	if dirExists(path) {
		return Acquired{}, fmt.Errorf("%w: %s", ErrPathOccupied, path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Acquired{}, fmt.Errorf("worktree: mkdir base %s: %w", filepath.Dir(path), err)
	}
	// No -b and no start point: check the branch out where it already points.
	if out, addErr := m.Git.Run(repoRoot, "worktree", "add", path, branch); addErr != nil {
		return Acquired{}, fmt.Errorf("worktree: re-attach %s to %s: %w: %s",
			path, branch, addErr, strings.TrimSpace(out))
	}
	// A resumed agent needs the same environment the run had: the project's
	// untracked .claude/ config and the gitignored dependency tree, without which
	// its own verification command cannot execute.
	syncUntrackedConfig(repoRoot, path)
	lendDependencies(repoRoot, path)
	return m.acquiredFor(repoRoot, path, branch)
}

// acquiredFor reports the re-attached worktree with StartPoint set to where the
// branch actually points — the tip of the recovered work, not a fresh base. An
// unreadable SHA is not fatal: the checkout is real either way, and no caller of
// a re-attach pins anything to this value.
func (m *Manager) acquiredFor(repoRoot, path, branch string) (Acquired, error) {
	sha, err := m.Git.Run(repoRoot, "rev-parse", branch)
	if err != nil {
		return Acquired{Path: path, Branch: branch}, nil
	}
	return Acquired{Path: path, Branch: branch, StartPoint: strings.TrimSpace(sha)}, nil
}
