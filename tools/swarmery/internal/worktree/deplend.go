package worktree

// `git worktree add` materializes only what is COMMITTED. An installed
// dependency tree is by construction NOT committed — node_modules, .venv and
// friends sit in .gitignore — so every fresh worktree starts with no toolchain
// at all, and the verification command a run's contract names fails on the
// environment instead of on the work.
//
// Observed 2026-08-10 (project handwrytten, phase-1 of an approved plan): the
// phase's own verification command, `npx jest tests/unit/<file>`, died in the
// fresh worktree with "Cannot find module '@babel/preset-env'". The phase could
// not have gone green no matter what the executor wrote.
//
// Installing per run is not the fix: it costs minutes of the run's wall clock
// every time, and a headless run's sandbox may refuse the network it needs.
// Lending the source checkout's ALREADY installed tree costs one symlink.
//
// Symlink, not copy, and the trade-off is deliberate:
//
//   - A dependency tree is routinely gigabytes; copying it per worktree would
//     dwarf the checkout it serves. Node and Python both resolve through
//     symlinked dependency roots natively (npm and pnpm build such links
//     themselves), so this is the boring path, not a trick.
//   - The tree is SHARED with the source checkout, so a run that reinstalls
//     (`npm ci`, `pip install`) mutates the developer's working copy. The run
//     prompts say so and tell executors not to reinstall — this file is the
//     reason that sentence exists.
//   - The link is only ever created when the worktree has nothing at that path,
//     so a project that genuinely commits its vendor directory keeps git's copy.
//
// A gitignored symlink does not show up in `git status --porcelain`, so a
// contract asserting a clean tree still holds.

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LendEnv overrides which gitignored dependency directories get lent, as a
// comma-separated list of paths relative to the checkout root (e.g.
// "node_modules,web/node_modules"). Set it to "off" (or "none") to lend nothing.
const LendEnv = "SWARMERY_WORKTREE_LEND"

// defaultLendPaths covers the ecosystems whose dependency root is a single
// gitignored directory at the checkout root. Deliberately short: each entry is
// a no-op for a project that does not have it, and an operator with an unusual
// layout sets LendEnv instead of waiting for a default to grow. Nested npm
// packages are found by nestedNodeModules, not listed here.
var defaultLendPaths = []string{"node_modules", ".venv", "vendor"}

// nestedScanDepth bounds how deep nestedNodeModules looks for a package.json.
// A sub-package sits a few directories down (tools/<app>/web); a walk without a
// floor would read the whole tree on every Acquire.
const nestedScanDepth = 4

// lendDependencies links each configured dependency directory from repoRoot into
// worktreePath. Best-effort by design, exactly like syncUntrackedConfig: this
// runs at the tail of Acquire, and a failure to lend a convenience tree must not
// fail an acquisition that is otherwise sound — the run then fails its own
// verification command with a message about the toolchain, which is a far better
// outcome than a worktree nobody gets.
func lendDependencies(repoRoot, worktreePath string) {
	for _, rel := range lentPaths(worktreePath) {
		linked, err := linkMissingDir(repoRoot, worktreePath, rel)
		if err != nil {
			log.Printf("warning: worktree: lend %s into %s: %v", rel, worktreePath, err)
			continue
		}
		if linked {
			log.Printf("worktree: %s is not tracked by git — symlinked %s's copy into %s so the run's build/test commands have a toolchain",
				rel, repoRoot, worktreePath)
		}
	}
}

// lentPaths is every path lendDependencies tries for worktreePath: the
// configured list plus, only with the defaults, the nested node_modules found
// under worktreePath. An explicit LendEnv is the operator's exact list.
func lentPaths(worktreePath string) []string {
	paths := lendPaths()
	if strings.TrimSpace(os.Getenv(LendEnv)) == "" {
		paths = append(paths, nestedNodeModules(worktreePath)...)
	}
	return paths
}

// lendPaths resolves the configured list. An unset knob means the defaults; an
// explicit "off"/"none" means lend nothing (an operator who wants every run to
// install its own tree).
func lendPaths() []string {
	raw := strings.TrimSpace(os.Getenv(LendEnv))
	if raw == "" {
		return defaultLendPaths
	}
	switch strings.ToLower(raw) {
	case "off", "none", "-":
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ManagerPlacedPaths lists the untracked paths Acquire itself may put into a
// worktree it hands out: the lent dependency directories (lentPaths, honouring
// LendEnv and the nested node_modules found under worktreePath) and the copied
// config files (configFilesToSync). Paths are relative to the worktree root,
// slash-separated and cleaned — the shape `git status --porcelain` prints.
//
// It exists for callers that read "untracked files in a worktree" as "someone's
// uncommitted work". A lent symlink is NOT matched by a dir-only ignore pattern
// such as `node_modules/`, so git reports `?? node_modules` in every worktree
// this manager lent it to, and an untracked settings file copied in by
// syncUntrackedConfig shows the same way. Neither is work; a caller that skips
// exactly these entries (untracked only, these exact paths) keeps every other
// signal of a dirty tree.
func ManagerPlacedPaths(worktreePath string) []string {
	lent := lentPaths(worktreePath)
	out := make([]string, 0, len(lent)+len(configFilesToSync))
	for _, rel := range lent {
		if safeRelPath(rel) {
			out = append(out, filepath.ToSlash(filepath.Clean(rel)))
		}
	}
	for _, rel := range configFilesToSync {
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

// nestedNodeModules returns "<dir>/node_modules" for every directory below the
// worktree root that holds a committed package.json. The root itself is covered
// by defaultLendPaths.
//
// Observed 2026-10-14 (board card T-ybm5i4): the root node_modules was lent but
// tools/swarmery/web/node_modules was not, so every run that built the web app
// died on "ls: tools/swarmery/web/node_modules: No such file or directory", and
// the run contract forbade the npm ci that would have fixed it.
//
// The walk reads the WORKTREE, not the source checkout: the worktree holds only
// committed files, while the source can hold untracked nested checkouts
// (.claude/worktrees/*) whose package.json files are not this project's.
// Hidden and node_modules directories are skipped. linkMissingDir already
// handles a candidate whose source was never installed.
func nestedNodeModules(worktreePath string) []string {
	var out []string
	_ = filepath.WalkDir(worktreePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree loses only its own candidates.
			if d != nil && d.IsDir() && path != worktreePath {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(worktreePath, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			name := d.Name()
			if name == "node_modules" || strings.HasPrefix(name, ".") ||
				strings.Count(rel, string(filepath.Separator)) >= nestedScanDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "package.json" {
			if dir := filepath.Dir(rel); dir != "." {
				out = append(out, filepath.Join(dir, "node_modules"))
			}
		}
		return nil
	})
	return out
}

// linkMissingDir symlinks repoRoot/rel to worktreePath/rel when the source is a
// real directory and the destination is free. linked=false with err=nil covers
// every "nothing to do" case: a rejected path, no such directory in the source
// (the project does not use that ecosystem), or a destination that already
// exists (git materialized it because it IS tracked — that copy is the more
// specific answer and is left untouched).
func linkMissingDir(repoRoot, worktreePath, rel string) (linked bool, err error) {
	if !safeRelPath(rel) {
		log.Printf("warning: worktree: refusing to lend %q: %s takes paths relative to the checkout root, with no .. segments", rel, LendEnv)
		return false, nil
	}
	src := filepath.Join(repoRoot, rel)
	// Stat, not Lstat: a source that is itself a symlink to a real tree (a
	// developer pointing node_modules elsewhere) is still a usable dependency
	// root, and linking to the link works.
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !srcInfo.IsDir() {
		return false, nil
	}
	dst := filepath.Join(worktreePath, rel)
	// Lstat here: a dangling symlink at the destination (a previous lend whose
	// target moved) is an existing name os.Symlink would fail on, and it is not
	// ours to reinterpret.
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := os.Symlink(src, dst); err != nil {
		return false, err
	}
	return true, nil
}

// safeRelPath keeps the knob from being read as "link anything anywhere": only a
// relative path that stays inside the checkout is a dependency directory.
func safeRelPath(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) {
		return false
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
