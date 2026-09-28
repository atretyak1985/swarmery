package wtjanitor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// harnessAgentDir is the basename the Claude Code harness gives an isolated
// subagent's checkout under <repo>/.claude/worktrees.
var harnessAgentDir = regexp.MustCompile(`^agent-[0-9a-f]+$`)

// agentOwned reports whether the janitor may touch the worktree at path. It
// owns exactly the two producers doc.go names: the harness, at
// <repoRoot>/.claude/worktrees/agent-<hex>, and the daemon, anywhere strictly
// below daemonRoot ("" = no daemon checkouts). Everything else — above all an
// operator's feature worktree, wherever it lives — is someone else's, however
// redundant its content looks: a clean worktree whose branch is pushed is
// "redundant" to the classifier and still in use by a person.
//
// Both sides are compared as written and symlink-resolved, because git prints
// the path it stored (often /private/var/… on macOS) while the projects table
// may hold the symlinked spelling (/var/…).
func agentOwned(repoRoot, daemonRoot, path string) bool {
	for _, p := range spellings(path) {
		if harnessAgentDir.MatchString(filepath.Base(p)) {
			for _, r := range spellings(repoRoot) {
				if filepath.Dir(p) == filepath.Join(r, ".claude", "worktrees") {
					return true
				}
			}
		}
		if daemonRoot == "" {
			continue
		}
		for _, d := range spellings(daemonRoot) {
			if strictlyBelow(d, p) {
				return true
			}
		}
	}
	return false
}

// spellings returns p cleaned and, when it resolves to something else, its
// symlink-free form. A path that does not exist has only the first.
func spellings(p string) []string {
	c := filepath.Clean(p)
	out := []string{c}
	if r, err := filepath.EvalSymlinks(c); err == nil && r != c {
		out = append(out, r)
	}
	return out
}

// strictlyBelow reports whether p is a descendant of dir, never dir itself and
// never a sibling that merely shares its prefix.
func strictlyBelow(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
