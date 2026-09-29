package handoff

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
)

const (
	// anchorGitTimeout bounds each git invocation the anchor makes. A handoff
	// must never hang on a wedged repository.
	anchorGitTimeout = 5 * time.Second
	// maxDirtyPaths caps the uncommitted-paths list; the overflow is counted.
	maxDirtyPaths = 50
	// anchorHeading is the literal section heading, shared by Digest and the
	// written file so both carry the same facts under the same name.
	anchorHeading = "## Repository anchor (recorded by swarmery, not generated)"
	// reasonNotRepo is the default unknown reason.
	reasonNotRepo = "cwd is not inside a git repository"
)

// anchor is the repository state recorded by Go at handoff time — facts the
// next session can check against the repo rather than trust from the model's
// retelling. Known=false means none of the fields are trustworthy.
type anchor struct {
	Root   string
	Head   string
	Branch string
	// Dirty holds at most maxDirtyPaths paths, followed by a "… (+N more)"
	// marker when capped. DirtyTotal is the uncapped count.
	Dirty      []string
	DirtyTotal int
	Known      bool
	// Reason says why the anchor is unknown (only meaningful when !Known).
	Reason string
}

// repoAnchor resolves the repository containing cwd. It never returns an
// error: any failure yields Known=false with a reason, and the handoff goes on.
// An empty Branch on a Known anchor means HEAD was unreadable; the caller may
// fill it from the session row.
func repoAnchor(cwd string) anchor {
	if strings.TrimSpace(cwd) == "" {
		return anchor{Reason: "session has no recorded cwd"}
	}
	// A deleted cwd (a removed worktree) must not walk up into the parent
	// checkout and anchor the wrong repository.
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return anchor{Reason: "cwd no longer exists"}
	}
	root, ok := findRepoRoot(cwd)
	if !ok {
		return anchor{Reason: reasonNotRepo}
	}
	head, ok := githead.Resolve(root)
	if !ok {
		// githead reads refs from the per-worktree gitdir only; a `git worktree
		// add` checkout keeps its branch refs in the common dir. Ask git.
		out, err := runGit(root, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return anchor{Root: root, Reason: "HEAD is unresolvable"}
		}
		head = strings.TrimSpace(out)
	}
	status, err := runGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return anchor{Root: root, Reason: "git status failed"}
	}
	paths := parsePorcelainZ(status)
	a := anchor{
		Root:       root,
		Head:       head,
		Branch:     readBranch(root),
		DirtyTotal: len(paths),
		Known:      true,
	}
	if len(paths) > maxDirtyPaths {
		paths = append(paths[:maxDirtyPaths:maxDirtyPaths], fmt.Sprintf("… (+%d more)", len(paths)-maxDirtyPaths))
	}
	a.Dirty = paths
	return a
}

// findRepoRoot walks up from dir to the first directory containing a .git
// entry — a directory, or a "gitdir:" file for worktrees and submodules.
func findRepoRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// gitDirOf returns the git directory for root, following a "gitdir:" file.
func gitDirOf(root string) (string, bool) {
	p := filepath.Join(root, ".git")
	fi, err := os.Stat(p)
	if err != nil {
		return "", false
	}
	if fi.IsDir() {
		return p, true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	d := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if d == "" {
		return "", false
	}
	if !filepath.IsAbs(d) {
		d = filepath.Join(root, d)
	}
	return d, true
}

// readBranch reads <gitDir>/HEAD: "ref: refs/heads/X" gives X, a bare sha
// gives "(detached)". Unreadable or unrecognised gives "".
func readBranch(root string) string {
	gitDir, ok := gitDirOf(root)
	if !ok {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(h, "ref: "); ok {
		return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	}
	if h != "" && !strings.ContainsFunc(h, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) {
		return "(detached)"
	}
	return ""
}

// runGit runs one read-only git command in root with a bounded timeout.
// --no-optional-locks keeps `status` from rewriting the index under a live
// session that may be using it.
func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), anchorGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	// A grandchild (an fsmonitor hook) holding stdout open must not stretch the
	// timeout: after the kill, Output gives up on the pipe within WaitDelay.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parsePorcelainZ extracts paths from `git status --porcelain=v1 -z`. Each
// record is "XY <path>"; a rename/copy record is followed by one extra record
// holding the ORIGINAL path, which is skipped so the new name is kept. -z makes
// paths literal (no quoting of spaces or non-ASCII).
func parsePorcelainZ(out string) []string {
	recs := strings.Split(out, "\x00")
	var paths []string
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 { // "XY " plus at least one character of path
			continue
		}
		paths = append(paths, rec[3:])
		if rec[0] == 'R' || rec[0] == 'C' || rec[1] == 'R' || rec[1] == 'C' {
			i++ // skip the original path record
		}
	}
	return paths
}

// Markdown renders the anchor as the section appended to the digest and to
// the written brief.
func (a anchor) Markdown() string {
	var b strings.Builder
	b.WriteString(anchorHeading + "\n")
	if !a.Known {
		fmt.Fprintf(&b, "- unknown (%s)\n", nz(a.Reason, reasonNotRepo))
		return b.String()
	}
	fmt.Fprintf(&b, "- Root: %s\n", a.Root)
	fmt.Fprintf(&b, "- HEAD: %s\n", a.Head)
	fmt.Fprintf(&b, "- Branch: %s\n", nz(a.Branch, "unknown"))
	if a.DirtyTotal == 0 {
		b.WriteString("- Uncommitted (0): none\n")
		return b.String()
	}
	fmt.Fprintf(&b, "- Uncommitted (%d):\n", a.DirtyTotal)
	for _, p := range a.Dirty {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	return b.String()
}
