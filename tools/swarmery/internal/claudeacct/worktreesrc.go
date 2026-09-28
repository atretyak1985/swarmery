package claudeacct

// Mapping a daemon worktree back to its SOURCE checkout.
//
// Daemon worktrees live at <home>/.swarmery/worktrees/<slug>/<taskID> — outside
// every project tree, so an upward walk from one finds no binding and no estate.
// Resolve therefore maps such a path to the checkout it was cut from and walks
// from THERE.
//
// The slug is ingest.SlugForPath — "/" → "-" and nothing else — and it does NOT
// decode: real directory names contain "-", so replacing "-" with "/" yields
// several candidate paths with no way to choose between them. The primary
// mapping is the worktree's own `.git` FILE instead, which names the source
// unambiguously and needs neither the daemon nor its database:
//
//	gitdir: /Users/me/projects/acme/tools/some-repo/.git/worktrees/phase-7
//
// — accepted only when that admin dir's own `gitdir` file points back at the
// worktree's .git, the pairing git itself maintains.
//
// The slug is a bounded fallback for a worktree whose .git is missing, and it
// refuses to guess: zero or several surviving candidates both mean "".
//
// claudeacct must not import internal/worktree (worktree imports store), so the
// root is duplicated here — keep it in step with worktree.DefaultRoot, the same
// way internal/ingest's worktreeBase already does.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// worktreesRel mirrors worktree.DefaultRoot (".swarmery/worktrees"), relative to
// the home directory.
const worktreesRel = ".swarmery/worktrees"

// gitWorktreesSep is where a linked worktree's gitdir line splits into the
// source checkout and the per-worktree admin dir.
const gitWorktreesSep = "/.git/worktrees/"

// maxSlugDashes bounds the slug fallback. Each "-" is a binary choice ("/" or a
// literal "-"), and the search is pruned by directory existence, but a slug with
// more separators than this is refused outright rather than searched.
const maxSlugDashes = 24

// sourceCheckout maps a path under <home>/.swarmery/worktrees/<slug>/<task>[/rest]
// to <source>[/rest], or "" when path is not under the worktree root or its
// source cannot be named unambiguously. Pure filesystem.
func sourceCheckout(path string) string {
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	root := filepath.Join(filepath.Clean(home), filepath.FromSlash(worktreesRel))
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "" // the slug directory itself is no worktree
	}
	slug, task, rest := parts[0], parts[1], parts[2:]
	wt := filepath.Join(root, slug, task)

	src := sourceFromGitFile(wt)
	if src == "" {
		src = sourceFromSlug(slug)
	}
	if src == "" {
		return ""
	}
	return filepath.Join(append([]string{src}, rest...)...)
}

// maxGitPointerBytes caps a read of a .git pointer file or a gitdir back-pointer:
// each is one line holding one path.
const maxGitPointerBytes = 4096

// sourceFromGitFile reads <wt>/.git — a FILE in a linked worktree — and returns
// the checkout <X> its `gitdir: <X>/.git/worktrees/<n>` line names, but ONLY when
// git itself agrees: <X>/.git is a directory AND <X>/.git/worktrees/<n>/gitdir
// points back at <wt>/.git. A .git file is just bytes in the worktree; without
// the back-pointer, whoever wrote it could name any directory on the machine as
// this worktree's "source" and so choose the account and the credential store
// its spawns run under.
func sourceFromGitFile(wt string) string {
	dotGit := filepath.Join(wt, ".git")
	raw, ok := readGitPointer(dotGit)
	if !ok {
		return "" // absent, or a directory (a full clone, not a linked worktree)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !ok {
			continue
		}
		v = filepath.ToSlash(strings.TrimSpace(v))
		i := strings.Index(v, gitWorktreesSep)
		if i <= 0 {
			return ""
		}
		name := strings.TrimSuffix(v[i+len(gitWorktreesSep):], "/")
		if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
			return ""
		}
		src := filepath.FromSlash(v[:i])
		if !filepath.IsAbs(src) || !isDir(filepath.Join(src, ".git")) {
			return ""
		}
		backFile := filepath.Join(src, ".git", "worktrees", name, "gitdir")
		back, ok := readGitPointer(backFile)
		if !ok {
			return ""
		}
		b := strings.TrimSpace(string(back))
		if b == "" {
			return ""
		}
		if !filepath.IsAbs(b) {
			b = filepath.Join(filepath.Dir(backFile), b)
		}
		if resolvedPath(b) != resolvedPath(dotGit) {
			return ""
		}
		return filepath.Clean(src)
	}
	return ""
}

// readGitPointer reads a small regular file, bounded; ok=false for anything
// else (missing, a directory, a FIFO, over maxGitPointerBytes).
func readGitPointer(path string) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	raw, ok, err := readCapped(f, maxGitPointerBytes)
	if err != nil || !ok {
		return nil, false
	}
	return raw, true
}

// resolvedPath is p with every symlink resolved, or p cleaned where it cannot be
// (a comparison key, never a path to open).
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// sourceFromSlug reconstructs an absolute path from a "/"→"-" slug by trying
// every placement of "/" that yields an EXISTING directory, pruning each prefix
// that does not exist. Exactly one survivor is returned; zero or several give "".
func sourceFromSlug(slug string) string {
	if !strings.HasPrefix(slug, "-") || strings.Count(slug, "-") > maxSlugDashes {
		return ""
	}
	tokens := strings.Split(slug[1:], "-") // slug[0] is the leading "/"
	var found []string
	var search func(prefix, comp string, i int) bool
	// search extends component comp with tokens[i:], below prefix. It returns
	// false once a second candidate is found, to stop the whole search.
	search = func(prefix, comp string, i int) bool {
		if i == len(tokens) {
			p := filepath.Join(prefix, comp)
			if comp != "" && isDir(p) {
				found = append(found, p)
			}
			return len(found) <= 1
		}
		tok := tokens[i]
		if comp == "" {
			return search(prefix, tok, i+1)
		}
		// "-" was a real "/": close comp, which must then exist as a directory.
		if p := filepath.Join(prefix, comp); isDir(p) {
			if !search(p, tok, i+1) {
				return false
			}
		}
		// "-" was a literal "-" inside the component.
		return search(prefix, comp+"-"+tok, i+1)
	}
	search(string(filepath.Separator), "", 0)
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
