package wtjanitor

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// RepoGit implements Git over a real repository. Decisions are made from
// plumbing output only (hash-object, rev-list, rev-parse); porcelain is parsed
// for ENUMERATION alone, and through internal/worktree's own parser at that.
type RepoGit struct{}

// run executes git in dir and returns trimmed stdout. Stderr is deliberately
// not captured: every caller here treats a non-zero exit as "no" or "unknown",
// never as something to show a user.
func run(dir string, args ...string) (string, error) {
	out, err := runRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// runRaw is run without the trim. NUL-separated output must never be trimmed:
// the first record of `status -z` starts with the status code's leading space
// (" M path"), and trimming it shifts every subsequent field by one — which
// silently truncates the first dirty path's name.
func runRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// BlobInGit reports whether repoRelPath's current content in worktreePath already
// exists in git at that same path, in any commit reachable from any ref.
//
// Byte identity, never a heuristic: hash the working-tree file (hash-object
// WITHOUT -w writes nothing), then walk only the commits that ever touched that
// path and compare the blob sha recorded there. Bounded by that path's history,
// which is short in practice — and identity is (path, content) together, so a
// file merely MOVED into place does not count as saved.
func (RepoGit) BlobInGit(repoRoot, worktreePath, repoRelPath string) (bool, error) {
	want, err := run(worktreePath, "hash-object", "--", repoRelPath)
	if err != nil {
		return false, err
	}
	commits, err := run(repoRoot, "log", "--all", "--format=%H", "--", repoRelPath)
	if err != nil {
		return false, err
	}
	if commits == "" {
		return false, nil // the path has no history at all
	}
	for _, c := range strings.Split(commits, "\n") {
		got, err := run(repoRoot, "rev-parse", "--verify", "--quiet", c+":"+repoRelPath)
		if err != nil {
			continue // path absent in that commit — rev-parse --quiet exits 1
		}
		if got == want {
			return true, nil
		}
	}
	return false, nil
}

// List enumerates the repository's worktrees WITHOUT observing anything about
// them: one `git worktree list` and nothing else. It is what a reader wants
// (the inventory endpoint) as opposed to what the sweeper needs — observation
// costs a full directory walk per worktree, which is fine every 15 minutes and
// absurd on every page load.
func (RepoGit) List(repoRoot string) ([]Worktree, error) {
	out, err := run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	entries := worktree.ParseWorktreeList(out)
	res := make([]Worktree, 0, len(entries))
	for i, e := range entries {
		// `git worktree list` always prints the MAIN checkout first.
		res = append(res, Worktree{Path: e.Path, Branch: e.Branch, IsMain: i == 0, Locked: e.Locked})
	}
	return res, nil
}

// Inspect enumerates the repository's worktrees and observes the facts Classify
// needs. It never decides anything and never writes.
//
// The main checkout is enumerated but NOT observed: it is vetoed by IsMain
// before any other field is read, and walking it would mean statting an entire
// working repository (node_modules and all) to compute an mtime nothing looks
// at. Same for a registered path that is gone from disk — every observation
// would fail, and prune is what fixes that, not inspection.
func (g RepoGit) Inspect(repoRoot string, live Liveness, owned func(path string) bool) ([]Worktree, error) {
	entries, err := g.List(repoRoot)
	if err != nil {
		return nil, err
	}
	res := make([]Worktree, 0, len(entries))
	for _, e := range entries {
		wt := Worktree{Path: e.Path, Branch: e.Branch, IsMain: e.IsMain, Locked: e.Locked}
		if wt.IsMain {
			res = append(res, wt)
			continue
		}
		// Not the janitor's: listed so its branch counts as checked out, but
		// never observed — `git status` would take its index.lock under an
		// operator's feet every tick.
		if owned != nil && !owned(e.Path) {
			wt.Foreign = true
			res = append(res, wt)
			continue
		}
		// Locked by the operator: Classify vetoes it before reading anything
		// else, so observing it would only take its index.lock for nothing.
		if wt.Locked {
			res = append(res, wt)
			continue
		}
		if !dirExists(e.Path) {
			continue
		}
		if wt.Dirty, err = g.dirty(e.Path); err != nil {
			return nil, err
		}
		if e.Branch != "" {
			if wt.HasOwnCommits, err = g.hasOwnCommits(repoRoot, e.Branch); err != nil {
				return nil, err
			}
		}
		wt.NewestMTime = newestMTime(e.Path)
		wt.LockFresh = lockFresh(repoRoot, e.Path)
		if wt.Live, err = live.Busy(e.Path); err != nil {
			return nil, err
		}
		res = append(res, wt)
	}
	return res, nil
}

// dirty lists modified + untracked paths, NUL-separated so a path with a space
// or a newline cannot be mis-split (git QUOTES such names in default porcelain
// output; -z makes them literal).
func (RepoGit) dirty(worktreePath string) ([]string, error) {
	out, err := runRaw(worktreePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 4 { // "XY " plus at least one character of path
			continue
		}
		paths = append(paths, rec[3:])
	}
	return paths, nil
}

// hasOwnCommits reports commits on branch reachable from NO other ref — the
// exact question "would deleting this branch lose anything".
//
// The --exclude pattern is the BARE branch name, not refs/heads/<branch>: git
// matches it relative to the ref space the following --branches walks. Spelling
// it with the refs/heads/ prefix excludes nothing, the branch excludes itself
// through --branches, and the count is then always 0 — i.e. every branch looks
// safe to delete. That failure is silent and unrecoverable, so it is pinned by
// TestHasOwnCommits rather than left to review.
func (RepoGit) hasOwnCommits(repoRoot, branch string) (bool, error) {
	n, err := run(repoRoot, "rev-list", "--count", branch, "--not",
		"--exclude="+branch, "--branches", "--tags", "--remotes")
	if err != nil {
		return false, err
	}
	return n != "0", nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// newestMTime is the newest mtime among the worktree's files, skipping .git
// (git's own metadata churns constantly and would keep every worktree looking
// freshly touched, so the idle floor would never elapse). A walk error yields
// the zero time, which Classify treats as a veto.
func newestMTime(root string) time.Time {
	var newest time.Time
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil // a linked worktree's .git is a FILE, not a directory
		}
		if d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Time{}
	}
	return newest
}

// lockFresh reports an index.lock in this worktree's git dir younger than
// worktree.StaleLockAge() — a git operation is in flight and the worktree must
// not be touched. An ABANDONED lock (older than the threshold) is not a veto:
// internal/worktree sweeps those, and treating them as live would freeze a
// worktree forever.
func lockFresh(repoRoot, worktreePath string) bool {
	lock := filepath.Join(repoRoot, ".git", "worktrees", filepath.Base(worktreePath), "index.lock")
	fi, err := os.Stat(lock)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < worktree.StaleLockAge()
}
