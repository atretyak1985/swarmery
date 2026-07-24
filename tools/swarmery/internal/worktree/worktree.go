package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sentinel errors — errors.Is-able. The dispatcher (Phase 3) maps these to
// dispatch_error messages on the task row.
var (
	// ErrBranchBusy: swarm/<taskID> already exists and is checked out in a
	// different worktree. No silent rename (Fusion lesson) — the caller must
	// resolve the conflict.
	ErrBranchBusy = errors.New("worktree: task branch is busy in another worktree")
	// ErrRepoRootRefused: the computed worktree path equals or contains repoRoot
	// (or vice versa). A runtime invariant — never hand a task the repo root.
	ErrRepoRootRefused = errors.New("worktree: refusing to use a path inside the repo root")
	// ErrNotARepo: repoRoot is not a git repository / git could not operate on it.
	ErrNotARepo = errors.New("worktree: repoRoot is not a git repository")
)

// staleLockAge is how old a .git/worktrees/*/index.lock must be before the
// pre-acquisition sweep deletes it (Fusion FN-6988: a crashed run leaves a lock
// that blocks all future acquisitions).
const staleLockAge = 10 * time.Minute

// DefaultRoot is the base directory for worktrees when Manager.Root is empty.
const DefaultRoot = ".swarmery/worktrees"

// Manager owns worktree lifecycle for a set of tasks. Git is the (mockable)
// git boundary; Root is the base dir under which per-project/per-task worktrees
// are created.
type Manager struct {
	Git  Git
	Root string // base dir; default: <home>/.swarmery/worktrees (see resolveRoot)
	// now is injected in tests to make the stale-lock age check deterministic;
	// nil means time.Now.
	now func() time.Time
}

// Acquired describes a worktree handed to a task.
type Acquired struct {
	Path       string // <Root>/<projectSlug>/<taskID>
	Branch     string // "swarm/<taskID>"
	StartPoint string // resolved SHA the worktree was pinned to
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// resolveRoot returns Root or the default under $HOME.
func (m *Manager) resolveRoot() (string, error) {
	if m.Root != "" {
		return m.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("worktree: resolve home for default root: %w", err)
	}
	return filepath.Join(home, DefaultRoot), nil
}

// branchName is the deterministic branch for a task.
func branchName(taskID string) string { return "swarm/" + taskID }

// Acquire creates (or safely reuses) a worktree for taskID pinned to an
// explicit start point. It enforces invariants 1–6 (see the phase doc):
//  1. explicit startPoint — never ambient HEAD;
//  2. repo-root guard — runtime, not caller-trusted;
//  3. branch-busy conflict fails loudly (ErrBranchBusy);
//  4. reuse-or-reset — reuse only a branch-matched worktree, recreate on any
//     proven mismatch, never destroy on a transient probe failure;
//  5. stale-lock sweep before acquisition;
//  6. (trailer format lives in trailer.go).
func (m *Manager) Acquire(repoRoot, projectSlug, taskID string) (Acquired, error) {
	root, err := m.resolveRoot()
	if err != nil {
		return Acquired{}, err
	}
	path := filepath.Join(root, projectSlug, taskID)
	branch := branchName(taskID)

	// Invariant 2: repo-root guard (runtime). Evaluate symlinks on both sides so
	// a symlinked Root that resolves inside the repo is still caught.
	if err := guardRepoRoot(repoRoot, path); err != nil {
		return Acquired{}, err
	}

	// repoRoot must be a git repo — resolve the default-branch tip up front so a
	// non-repo fails fast with ErrNotARepo (invariant 1 needs this SHA anyway).
	startSHA, err := m.resolveStartPoint(repoRoot)
	if err != nil {
		return Acquired{}, err
	}

	// Invariant 5: stale-lock sweep + prune before touching worktrees.
	m.sweepStaleLocks(repoRoot)
	// prune is best-effort: a failure here should not abort acquisition, but a
	// non-repo would already have failed above.
	_, _ = m.Git.Run(repoRoot, "worktree", "prune")

	// Invariant 4 + 3: decide reuse / reclaim / conflict from the worktree list.
	list, listErr := m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if listErr != nil {
		// Transient probe failure: one retry, then error — NEVER destroy on a
		// flaky signal (Fusion lines 185-191).
		list, listErr = m.Git.Run(repoRoot, "worktree", "list", "--porcelain")
		if listErr != nil {
			return Acquired{}, fmt.Errorf("worktree: list worktrees: %w", listErr)
		}
	}
	entries := parseWorktreeList(list)

	// Invariant 3: the branch is checked out in some OTHER path → busy.
	if other, ok := entries.pathForBranch(branch); ok && !samePath(other, path) {
		return Acquired{}, fmt.Errorf("%w: %s is on %s", ErrBranchBusy, other, branch)
	}

	// Invariant 4: our path already registered?
	if reg, ok := entries.byPath(path); ok {
		if reg.branch == branch {
			// Branch-matched → warm reuse as-is.
			return Acquired{Path: path, Branch: branch, StartPoint: startSHA}, nil
		}
		// Foreign branch / detached at our path → reclaim in place: remove then
		// recreate below.
		if _, err := m.Git.Run(repoRoot, "worktree", "remove", "--force", path); err != nil {
			return Acquired{}, fmt.Errorf("worktree: reclaim %s: %w", path, err)
		}
	} else if dirExists(path) {
		// Path exists on disk but is not a registered worktree (crash leftover,
		// archive→restore). Reclaim: prune already ran; force-remove clears any
		// stale registration, then recreate.
		_, _ = m.Git.Run(repoRoot, "worktree", "remove", "--force", path)
	}

	// Invariant 1: create pinned to the explicit start SHA. Never a bare
	// `git worktree add <path>` (ambient HEAD may sit on a sibling task branch).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Acquired{}, fmt.Errorf("worktree: mkdir base %s: %w", filepath.Dir(path), err)
	}
	if out, err := m.Git.Run(repoRoot, "worktree", "add", "-b", branch, path, startSHA); err != nil {
		// A branch that exists but is not checked out anywhere collides on add;
		// surface it as busy rather than a raw git error.
		if strings.Contains(out, "already exists") {
			return Acquired{}, fmt.Errorf("%w: branch %s already exists", ErrBranchBusy, branch)
		}
		return Acquired{}, fmt.Errorf("worktree: add %s: %w", path, err)
	}
	return Acquired{Path: path, Branch: branch, StartPoint: startSHA}, nil
}

// resolveStartPoint returns the SHA of the repo's DEFAULT-branch tip, resolved
// in repoRoot's context — NOT the ambient checkout HEAD (invariant 1). If the
// repo sits on another branch during recovery, we still pin to the default
// branch tip. Falls back to HEAD only when the default branch cannot be
// determined (bare repo edge cases).
func (m *Manager) resolveStartPoint(repoRoot string) (string, error) {
	// Determine the default branch name from the current symbolic HEAD; if the
	// checkout is detached or the ref is missing, fall back to HEAD's SHA.
	def, err := m.Git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		// Not a repo at all? rev-parse will confirm; otherwise detached HEAD.
		sha, headErr := m.Git.Run(repoRoot, "rev-parse", "HEAD")
		if headErr != nil {
			return "", fmt.Errorf("%w: %v", ErrNotARepo, headErr)
		}
		return strings.TrimSpace(sha), nil
	}
	def = strings.TrimSpace(def)
	sha, err := m.Git.Run(repoRoot, "rev-parse", "refs/heads/"+def)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve tip of %s: %w", def, err)
	}
	return strings.TrimSpace(sha), nil
}

// Remove tears down an acquired worktree. keepBranch preserves swarm/<taskID>
// (e.g. so its commits remain reachable for merge); otherwise the branch is
// deleted too.
func (m *Manager) Remove(repoRoot string, a Acquired, keepBranch bool) error {
	if _, err := m.Git.Run(repoRoot, "worktree", "remove", "--force", a.Path); err != nil {
		return fmt.Errorf("worktree: remove %s: %w", a.Path, err)
	}
	if !keepBranch && a.Branch != "" {
		if _, err := m.Git.Run(repoRoot, "branch", "-D", a.Branch); err != nil {
			return fmt.Errorf("worktree: delete branch %s: %w", a.Branch, err)
		}
	}
	return nil
}

// Prune runs `git worktree prune` and sweeps stale index.lock files — the same
// recovery the acquisition path does, exposed for a periodic reaper.
func (m *Manager) Prune(repoRoot string) error {
	m.sweepStaleLocks(repoRoot)
	if _, err := m.Git.Run(repoRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("worktree: prune: %w", err)
	}
	return nil
}

// sweepStaleLocks deletes <repoRoot>/.git/worktrees/*/index.lock files older
// than staleLockAge (Fusion FN-6988). Best-effort: unreadable dirs are skipped.
func (m *Manager) sweepStaleLocks(repoRoot string) {
	base := filepath.Join(repoRoot, ".git", "worktrees")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return // no worktrees registered yet, or .git is a file (submodule) — fine
	}
	cutoff := m.clock().Add(-staleLockAge)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		lock := filepath.Join(base, d.Name(), "index.lock")
		info, err := os.Stat(lock)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(lock)
		}
	}
}

// guardRepoRoot implements invariant 2: reject a worktree path that equals or
// nests with repoRoot in either direction, after resolving symlinks.
func guardRepoRoot(repoRoot, path string) error {
	rr := evalOrClean(repoRoot)
	p := evalOrClean(path)
	if samePath(rr, p) || isSubpath(rr, p) || isSubpath(p, rr) {
		return fmt.Errorf("%w: path=%s repoRoot=%s", ErrRepoRootRefused, path, repoRoot)
	}
	return nil
}

// evalOrClean resolves symlinks in p. The worktree path usually does not exist
// yet, so EvalSymlinks would fail on the whole path; instead we resolve the
// deepest EXISTING ancestor (so a symlinked repoRoot like macOS /var →
// /private/var is canonicalized consistently on both sides of the guard) and
// re-append the not-yet-created tail lexically.
func evalOrClean(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	dir := p
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the root; nothing existed
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Clean(filepath.Join(parts...))
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
	}
	return p
}

// isSubpath reports whether child is strictly inside parent.
func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// --- git worktree list --porcelain parsing ---------------------------------

type worktreeEntry struct {
	path   string
	branch string // short branch name ("swarm/T-x"), empty if detached
}

type worktreeEntries []worktreeEntry

// parseWorktreeList parses `git worktree list --porcelain`. Each record is a
// "worktree <path>" line optionally followed by "branch refs/heads/<name>"
// (absent for a detached HEAD), records separated by blank lines.
func parseWorktreeList(out string) worktreeEntries {
	var entries worktreeEntries
	var cur *worktreeEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				entries = append(entries, *cur)
			}
			cur = &worktreeEntry{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				ref := strings.TrimPrefix(line, "branch ")
				cur.branch = strings.TrimPrefix(ref, "refs/heads/")
			}
		case line == "":
			if cur != nil {
				entries = append(entries, *cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		entries = append(entries, *cur)
	}
	return entries
}

func (e worktreeEntries) byPath(path string) (worktreeEntry, bool) {
	for _, w := range e {
		if samePath(w.path, path) {
			return w, true
		}
	}
	return worktreeEntry{}, false
}

func (e worktreeEntries) pathForBranch(branch string) (string, bool) {
	for _, w := range e {
		if w.branch == branch {
			return w.path, true
		}
	}
	return "", false
}
