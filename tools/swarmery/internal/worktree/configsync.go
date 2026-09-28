package worktree

// `git worktree add` only ever materializes files that are committed to git.
// A project onboarded into swarmery gets its .claude/settings.json (where
// enabledPlugins lives) written by onboarding or hand-edited, and it is
// entirely normal for that file to sit untracked (`git status` shows `??`) —
// nothing in the onboarding flow requires a commit. Every fresh worktree cut
// for that project then starts with NO .claude/ at all: zero plugins, zero
// project.json, and a headless `--agent <pack>:<agent>` run fails with
// Claude Code's built-in zero-plugin agent list and no hint the cause was an
// untracked file back in the source checkout (issue #192).
//
// syncUntrackedConfig closes that gap for every Acquire caller (dispatch,
// planrun, phaserun — verify reuses whatever worktree dispatch already
// acquired) by lending the source checkout's copies of the files git left
// behind, whenever the worktree does not already have its own.
//
// ONE key is never lent: the `swarmery` object of settings.local.json, which
// holds the project's account binding and estate declaration. A worktree lives
// under ~/.swarmery/worktrees, outside every project tree, and resolves its
// account and estate from its SOURCE checkout (claudeacct/worktreesrc.go); a
// copy frozen into the worktree would be read as the worktree's own pin and
// would keep answering after the source's binding changed. Every other key of
// that file is still lent. StripLentBindings removes the object from copies
// lent before this rule existed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// bindingNamespace is the settings.local.json key claudeacct's binding lives
// under (claudeacct.bindingNamespace; not imported, to keep this package free
// of the account layer).
const bindingNamespace = "swarmery"

// localSettings is the lent file whose binding object is never lent.
var localSettings = filepath.Join(".claude", "settings.local.json")

// configFilesToSync are relative to a checkout root. settings.local.json is
// gitignored by convention in virtually every project (it holds per-machine
// overrides), so it is copied unconditionally here — there is no "commit it
// instead" fix available for that one the way there is for settings.json.
var configFilesToSync = []string{
	filepath.Join(".claude", "settings.json"),
	filepath.Join(".claude", "settings.local.json"),
	filepath.Join(".claude", "project.json"),
}

// syncUntrackedConfig copies each of configFilesToSync from repoRoot into
// worktreePath when the source checkout has it and the fresh worktree does
// not. Best-effort: a copy failure is logged, never returned — this runs at
// the tail of Acquire, and a permissions hiccup on a convenience file must
// not fail the whole acquisition (a caller can still lend settings explicitly
// via repopath.InheritedSettings / --settings, which this does not replace).
func syncUntrackedConfig(repoRoot, worktreePath string) {
	for _, rel := range configFilesToSync {
		src := filepath.Join(repoRoot, rel)
		dst := filepath.Join(worktreePath, rel)
		var filter func([]byte) ([]byte, bool)
		if rel == localSettings {
			filter = stripBindingNamespace
		}
		copied, err := copyMissingFiltered(src, dst, filter)
		if err != nil {
			log.Printf("warning: worktree: copy %s into %s: %v", rel, worktreePath, err)
			continue
		}
		if copied {
			log.Printf("worktree: %s is untracked in %s — copied it into %s so the worktree keeps the project's plugin config",
				rel, repoRoot, worktreePath)
		}
	}
}

// copyMissing copies src to dst when src exists as a regular file and dst
// does not exist yet. copied=false with err=nil covers every "nothing to do"
// case: no source (the project genuinely has no such file), a non-regular
// source (symlink/dir — not ours to reinterpret), or a dst that already
// exists (git materialized it because it IS tracked — the worktree's own
// copy is the more specific answer and is left untouched).
func copyMissing(src, dst string) (copied bool, err error) {
	return copyMissingFiltered(src, dst, nil)
}

// copyMissingFiltered is copyMissing with the bytes passed through filter
// (when non-nil) on the way; a filter that reports no change leaves the copy
// byte-for-byte identical to the source.
func copyMissingFiltered(src, dst string, filter func([]byte) ([]byte, bool)) (copied bool, err error) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !srcInfo.Mode().IsRegular() {
		return false, nil
	}
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	if filter != nil {
		if out, changed := filter(data); changed {
			data = out
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(dst, data, srcInfo.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

// stripBindingNamespace returns data without its top-level `swarmery` object,
// re-marshalled with two-space indent and a trailing newline (the shape
// claudeacct's writer produces). changed=false — and data untouched — for
// input that is not a JSON object or carries no such key: an unparseable file
// is lent byte-for-byte exactly as before, never "repaired".
func stripBindingNamespace(data []byte) ([]byte, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return data, false
	}
	if _, ok := root[bindingNamespace]; !ok {
		return data, false
	}
	delete(root, bindingNamespace)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return data, false
	}
	return append(out, '\n'), true
}

// gitUntrackedMarker is git's wording for "that path is not in the index".
const gitUntrackedMarker = "did not match any file(s) known to git"

// errTrackedOrUnknown marks a lent file StripLentBindings must not rewrite.
var errTrackedOrUnknown = errors.New("tracked by git in the worktree, or git could not tell")

// StripLentBindings removes the `swarmery` object from every lent
// .claude/settings.local.json under the worktree root
// (<Root>/<projectSlug>/<taskID>/.claude/settings.local.json) — the copies
// lent before syncUntrackedConfig stopped lending it. Every other key is kept;
// a file that is not a regular file, is unparseable, carries no such object,
// or that git tracks in its worktree (or cannot classify) is left byte-for-byte.
// The rewrite is atomic and keeps the file's mode. Best-effort by design: it
// returns how many files it stripped, and an error only when the root itself
// cannot be listed.
func (m *Manager) StripLentBindings() (int, error) {
	root, err := m.resolveRoot()
	if err != nil {
		return 0, err
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", localSettings))
	if err != nil {
		return 0, err
	}
	stripped := 0
	for _, path := range matches {
		wt := filepath.Dir(filepath.Dir(path))
		switch err := m.stripOne(wt, path); {
		case err == nil:
			stripped++
			log.Printf("worktree: removed the lent %q binding object from %s (the worktree resolves its account from its source checkout)",
				bindingNamespace, path)
		case errors.Is(err, errNothingToStrip):
		default:
			log.Printf("warning: worktree: left %s as it is: %v", path, err)
		}
	}
	return stripped, nil
}

var errNothingToStrip = errors.New("nothing to strip")

func (m *Manager) stripOne(wt, path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errNothingToStrip
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed := stripBindingNamespace(data)
	if !changed {
		return errNothingToStrip
	}
	// A file git tracks in the worktree belongs to the repository, not to the
	// lending: never rewrite it. Only git's own "not in the index" answer
	// clears the way; any other outcome (tracked, not a repo, a git failure)
	// leaves the file alone.
	if outGit, gerr := m.Git.Run(wt, "ls-files", "--error-unmatch", "--", filepath.ToSlash(localSettings)); gerr == nil ||
		!strings.Contains(outGit, gitUntrackedMarker) {
		return errTrackedOrUnknown
	}
	return writeFileAtomic(path, out, fi.Mode().Perm())
}

// writeFileAtomic replaces path with data via a temp file in the same
// directory, keeping mode.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
