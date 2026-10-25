package api

// Staleness probe for the friction Source: a recurring error whose source no
// longer exists (a removed worktree, a temp dir, branches deleted since) is not
// worth a fix card. The probe is deterministic and fails open — anything it
// cannot read counts as live, so it only ever withholds a card on evidence.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/archmap"
)

const (
	// stalenessGitTimeout bounds each `git rev-parse` the branch signal runs.
	stalenessGitTimeout = 5 * time.Second
	// errExampleRunes is errorGroups' cut on errGroup.Example: a path running
	// into a cut example may be truncated, so it is not probed.
	errExampleRunes = 160
)

// frictionStaleness is the probe's answer for one error group.
type frictionStaleness struct {
	Stale   bool
	Reasons []string // one human-readable line per fired signal
}

// probeFrictionStaleness decides whether g's source still exists in the repo
// of projectID. Stale when every absolute path the example names is gone from
// an ephemeral location, or when every sample session ran on a non-default
// branch that has since been deleted (or whose cwd is gone).
func (h *Handler) probeFrictionStaleness(g errGroup, projectID int64) frictionStaleness {
	root := h.stalenessRoot(projectID)
	if root == "" {
		return frictionStaleness{}
	}
	var out frictionStaleness
	if missing, ok := ephemeralMissingPaths(g.Example, root); ok {
		out.Reasons = append(out.Reasons,
			"path "+strings.Join(missing, ", ")+" no longer exists (ephemeral worktree/temp location)")
	}
	if branches, ok := h.transientBranches(g.Samples, root); ok {
		out.Reasons = append(out.Reasons,
			"every sample session ran on a deleted or abandoned non-default branch: "+strings.Join(branches, ", "))
	}
	out.Stale = len(out.Reasons) > 0
	return out
}

// stalenessRoot is the project's repo root, or "" when the project is unknown
// or its path is empty or gone (the probe then fails open).
func (h *Handler) stalenessRoot(projectID int64) string {
	if projectID == 0 {
		return ""
	}
	var path sql.NullString
	if err := h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, projectID).Scan(&path); err != nil {
		return ""
	}
	root := filepath.Clean(strings.TrimSpace(path.String))
	if path.String == "" || !filepath.IsAbs(root) {
		return ""
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return ""
	}
	return root
}

// ephemeralMissingPaths reports whether the path signal fires for example: at
// least one absolute path was extracted, none exists, and every one sits in an
// ephemeral location. It returns the paths it judged.
func ephemeralMissingPaths(example, root string) ([]string, bool) {
	paths := extractAbsPaths(example)
	if len(paths) == 0 {
		return nil, false
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return nil, false // the source is still there
		}
	}
	for _, p := range paths {
		if !isEphemeralPath(p, root) {
			return nil, false
		}
	}
	return paths, true
}

// isEphemeralPath: a path in a worktree container, or anywhere outside the
// repo root (a sibling -wt dir, a temp dir, another checkout). A path inside
// the root but outside a worktree container is the repo's own — not ephemeral,
// even when the root itself lives under a temp dir.
func isEphemeralPath(p, root string) bool {
	if strings.Contains(p, "/.claude/worktrees/") || strings.Contains(p, "/worktrees/") {
		return true
	}
	if strings.HasPrefix(p, root+"/") {
		return false
	}
	// Outside the root: temp locations (os.TempDir, /tmp, /private/var/folders)
	// are covered here too, since a temp path is never under a real repo root.
	return true
}

// extractAbsPaths pulls absolute paths out of an error message: a token that
// starts with "/" at a word boundary and runs to a quote, whitespace or ": ",
// with trailing quotes, commas and colons stripped. Tokens of fewer than two
// segments are ignored, and so is one that runs into a cut example's end.
func extractAbsPaths(example string) []string {
	cut := utf8.RuneCountInString(example) >= errExampleRunes
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(example); i++ {
		if example[i] != '/' || (i > 0 && !isPathBoundary(example[i-1])) {
			continue
		}
		end := i
		for end < len(example) {
			c := example[end]
			if c == '\'' || c == '"' || c == '`' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				break
			}
			if c == ':' && end+1 < len(example) && example[end+1] == ' ' {
				break
			}
			end++
		}
		tok := example[i:end]
		atEnd := end == len(example)
		i = end
		if cut && atEnd {
			continue // possibly truncated mid-path
		}
		tok = strings.TrimRight(tok, `'",:`)
		p := filepath.Clean(tok)
		if strings.Count(strings.Trim(p, "/"), "/") < 1 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// isPathBoundary: a "/" preceded by one of these starts a path; one preceded by
// a name character is the middle of a relative path ("internal/api/x.go").
func isPathBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\'', '"', '`', '(', '[', '=', ':', ',':
		return true
	}
	return false
}

// transientBranches reports whether the branch signal fires: every sample
// session ran on a non-empty, non-default branch whose ref is gone from root
// or whose cwd is gone. It returns the distinct branches, in sample order.
func (h *Handler) transientBranches(samples []errGroupSample, root string) ([]string, bool) {
	if len(samples) == 0 {
		return nil, false
	}
	def := archmap.DefaultBranch(root)
	if def == "" {
		return nil, false
	}
	var branches []string
	seen := map[string]bool{}
	for _, sm := range samples {
		var branch, cwd sql.NullString
		if err := h.DB.QueryRow(`SELECT git_branch, cwd FROM sessions WHERE id = ?`, sm.SessionID).
			Scan(&branch, &cwd); err != nil {
			return nil, false // unreadable or gone: fail open
		}
		b := strings.TrimSpace(branch.String)
		if b == "" || b == "HEAD" || b == def {
			return nil, false
		}
		refGone, ok := branchRefGone(root, b)
		if !ok {
			return nil, false // git could not run: fail open
		}
		if !refGone && !cwdGone(cwd.String) {
			return nil, false // live ref and live cwd
		}
		if !seen[b] {
			seen[b] = true
			branches = append(branches, b)
		}
	}
	return branches, true
}

// branchRefGone asks git whether refs/heads/<branch> is missing from root. ok
// is false when git itself could not run (missing binary, timeout).
func branchRefGone(root, branch string) (gone, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), stalenessGitTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run()
	if err == nil {
		return false, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return true, true
	}
	return false, false
}

// cwdGone: a recorded absolute cwd that no longer exists. An empty or
// placeholder cwd is unknown, which counts as live.
func cwdGone(cwd string) bool {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return false
	}
	_, err := os.Stat(cwd)
	return errors.Is(err, os.ErrNotExist)
}
