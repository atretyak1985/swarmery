package worktree

// Auto-memory scope for headless runs in a daemon-managed worktree.
//
// THE QUESTION. Claude Code keeps a per-project auto-memory directory under
// `<home>/.claude/projects/<slug>/memory/`, where `<slug>` encodes an absolute
// path. A run dispatched by the daemon does not execute in the project's
// checkout — it executes in a worktree of it, under a different absolute path,
// which encodes to a different slug. If auto-memory were resolved from the
// session's cwd, every headless run would start amnesiac: the operator's
// accumulated project memory lives under the CHECKOUT's slug, and the run would
// look for it under the WORKTREE's.
//
// THE ANSWER: it is resolved from the canonical project, so there is nothing to
// fix. Measured by `scripts/tests/worktree-memory-probe.sh` (2026-09-20), three
// independent ways:
//
//  1. A headless session whose cwd was a daemon-managed worktree of this repo
//     loaded its memory index from the CANONICAL checkout's slug directory, not
//     from the worktree slug directory its own transcripts were written under.
//     Read off the attachment record the harness writes when it loads memory
//     (`"path":"…/memory/MEMORY.md","type":"AutoMem"`), not off a path
//     appearing anywhere in the transcript — prose, tool output and the probe's
//     own stdout all mention such paths, and matching those made the probe
//     confirm itself.
//  2. That worktree slug directory holds transcripts but no `memory/` entry at
//     all — Claude Code never created one for it.
//  3. Across every slug directory on the probe machine, the ones carrying a
//     `memory/` entry were canonical checkout paths without exception; not one
//     worktree-shaped slug had one.
//
// All three gate the verdict — 3 is a corroboration gate: it cannot raise the
// answer, but a worktree-shaped slug carrying `memory/` anywhere on the machine
// downgrades MATCH to INCONCLUSIVE. When the load cannot be observed at all the
// probe says INCONCLUSIVE; it never rounds up to MATCH.
//
// WHY THE HELPERS EXIST ANYWAY. `ProjectSlug`, `LinkMemory` and `UnlinkMemory`
// below are complete and tested, but DORMANT — nothing in this package calls
// them, by design. They are the remedy for the opposite finding, kept ready
// because the behaviour they compensate for is Claude Code's, not ours: it is
// undocumented, unversioned, and can change under us without a signal. Shipping
// the fix cold costs one file; discovering the regression with no fix in hand
// costs a fleet of runs that silently forgot the project.
//
// WHAT WOULD FLIP `linkMemory` TO TRUE. Re-run the probe. It reports MISMATCH
// when a worktree session resolves memory to its OWN slug — concretely, when
// `<home>/.claude/projects/<worktree-slug>/memory/` starts existing, or when a
// worktree session's transcript references that path instead of the canonical
// one. On a MISMATCH:
//
//   - set `linkMemory = true` here;
//   - call `LinkMemory(repoRoot, a.Path)` from `Manager.Acquire` once the
//     worktree exists, alongside the other post-creation grafts (`configsync`,
//     `deplend`) — a failure must warn, never abort the acquisition;
//   - call `UnlinkMemory(a.Path)` from `Manager.Remove` AND from the janitor's
//     removal path in `internal/wtjanitor`, which deletes worktrees the manager
//     never sees. Skipping the janitor leaves a slug directory per reaped
//     worktree, each holding a dangling symlink.
//
// Until then the const stays false and the call sites stay absent, because a
// symlink that duplicates behaviour the tool already provides is not neutral:
// it is a second source of truth for where a run's memory lives.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
)

// linkMemory records the probe's verdict in code: false means a headless run in
// a worktree already reads the canonical project's auto-memory, so the linking
// helpers below stay uncalled. Pinned by TestMemoryLinkDisabledByProbe so the
// value cannot drift away from the documented finding unnoticed.
const linkMemory = false

// ErrMemoryTargetNotSymlink: the worktree slug's `memory` path is occupied by
// something we did not create — a real directory or a regular file. Both the
// link and the unlink refuse it rather than replace or delete it. A real
// directory there means Claude Code (or an operator) is keeping genuine memory
// under the worktree slug, and that content is not ours to destroy.
var ErrMemoryTargetNotSymlink = errors.New("worktree: memory path exists and is not a symlink")

// memoryHome pins the home directory the memory helpers resolve
// `<home>/.claude/projects` against. Empty means os.UserHomeDir(). Tests set it
// to a t.TempDir() so they can never touch the operator's real memory — the
// same seam-over-environment choice the rest of the daemon makes.
var memoryHome string

// ProjectSlug encodes an absolute path the way Claude Code names the
// per-project directory under `<configDir>/projects`: every character outside
// [A-Za-z0-9] becomes '-', and a result past 200 characters is truncated and
// hash-suffixed.
//
// It is a thin alias for claudeproj.Slug, which is the single authority for
// that encoding — the rule is read out of the shipped binary there, along with
// the ground truth behind it and why it differs from ingest.SlugForPath (the DB
// slug, which encodes '/' only because it is project identity). It stays here
// because this file's prose is written in terms of it and callers of the memory
// helpers expect it.
func ProjectSlug(path string) string { return claudeproj.Slug(path) }

// projectsDir is `<home>/.claude/projects`.
func projectsDir() (string, error) {
	home := memoryHome
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("worktree: resolve home for the memory directory: %w", err)
		}
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// MemoryDir returns the auto-memory directory Claude Code would use for a
// session whose cwd is the given absolute path. It reports where memory WOULD
// live; it does not assert that the directory exists.
func MemoryDir(cwd string) (string, error) {
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("worktree: memory dir needs an absolute path, got %q", cwd)
	}
	projects, err := projectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(projects, ProjectSlug(cwd), "memory"), nil
}

// LinkMemory points the worktree's slug directory at the canonical checkout's
// auto-memory, so a session running in the worktree reads the project's real
// memory instead of an empty one.
//
// Dormant — see the file comment. Enable only when the probe reports MISMATCH.
//
// It is safe to call repeatedly and safe to call on a machine that has no
// memory yet:
//
//   - the canonical `memory/` missing is not an error. There is nothing to
//     share, so it creates nothing and says so once, then returns nil. Failing
//     here would fail worktree acquisition over an absent convenience. Only
//     ACTUAL absence is forgiven, though: an unreadable canonical path (EACCES,
//     ELOOP, EIO) is returned as an error, because "cannot tell" is not "not
//     there" and the caller deserves the real cause.
//   - the two paths encoding to the same slug is a no-op: a checkout cannot
//     usefully link to itself, and the symlink would be its own target.
//   - an existing link to the same target is a no-op, which is what makes a
//     retry of a half-finished acquisition harmless.
//   - an existing link to a DIFFERENT target is repointed, because the worktree
//     slug belongs to exactly one checkout and a stale link is the expected
//     residue of a moved or re-based checkout. Removing a symlink destroys no
//     content.
//   - anything else at that path — a real directory, a regular file — is
//     refused with ErrMemoryTargetNotSymlink. That content is not ours.
func LinkMemory(canonicalCwd, worktreeCwd string) error {
	canonical, err := MemoryDir(canonicalCwd)
	if err != nil {
		return err
	}
	target, err := MemoryDir(worktreeCwd)
	if err != nil {
		return err
	}
	if canonical == target {
		return nil
	}

	// "Absent" is the only benign reason the canonical memory cannot be read.
	// EACCES, ELOOP or EIO are not "no memory yet" — they are a machine that
	// cannot answer the question, and reporting them as absence would leave the
	// worktree silently unlinked under a log line naming the wrong cause.
	switch fi, serr := os.Stat(canonical); {
	case serr == nil && !fi.IsDir():
		log.Printf("worktree: %s is not a directory — %s left unlinked", canonical, worktreeCwd)
		return nil
	case os.IsNotExist(serr):
		log.Printf("worktree: %s has no auto-memory directory yet — %s left unlinked",
			canonicalCwd, worktreeCwd)
		return nil
	case serr != nil:
		return fmt.Errorf("worktree: inspect the canonical memory %s: %w", canonical, serr)
	}

	switch fi, lerr := os.Lstat(target); {
	case lerr == nil && fi.Mode()&os.ModeSymlink != 0:
		dest, rerr := os.Readlink(target)
		if rerr == nil && dest == canonical {
			return nil // already ours, pointing where it should
		}
		if rerr := os.Remove(target); rerr != nil {
			return fmt.Errorf("worktree: replace stale memory link %s: %w", target, rerr)
		}
		log.Printf("worktree: repointed the memory link %s at %s", target, canonical)
	case lerr == nil:
		return fmt.Errorf("%w: %s", ErrMemoryTargetNotSymlink, target)
	case !os.IsNotExist(lerr):
		return fmt.Errorf("worktree: inspect memory path %s: %w", target, lerr)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("worktree: create the worktree project dir %s: %w", filepath.Dir(target), err)
	}
	if err := os.Symlink(canonical, target); err != nil {
		return fmt.Errorf("worktree: link %s to %s: %w", target, canonical, err)
	}
	log.Printf("worktree: linked %s to the canonical project memory at %s", target, canonical)
	return nil
}

// UnlinkMemory removes the link LinkMemory created for a worktree, so a reaped
// worktree does not leave a dangling entry behind.
//
// Dormant — see the file comment. It removes a SYMLINK and nothing else: an
// absent path is success (nothing to clean), and a real directory or file is
// refused with ErrMemoryTargetNotSymlink rather than deleted. A cleanup path
// that can delete a real memory directory is worse than an orphan.
func UnlinkMemory(worktreeCwd string) error {
	target, err := MemoryDir(worktreeCwd)
	if err != nil {
		return err
	}
	fi, lerr := os.Lstat(target)
	switch {
	case os.IsNotExist(lerr):
		return nil
	case lerr != nil:
		return fmt.Errorf("worktree: inspect memory path %s: %w", target, lerr)
	case fi.Mode()&os.ModeSymlink == 0:
		return fmt.Errorf("%w: %s", ErrMemoryTargetNotSymlink, target)
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("worktree: remove memory link %s: %w", target, err)
	}
	return nil
}
