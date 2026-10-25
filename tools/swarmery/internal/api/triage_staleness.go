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
	"regexp"
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
// least one absolute path was extracted, and every one is gone from an
// ephemeral location whose worktree root (if it has one) is gone too. Any path
// that exists, sits in a live worktree, or is not ephemeral keeps the group
// live. It returns the paths it judged.
func ephemeralMissingPaths(example, root string) ([]string, bool) {
	paths := extractAbsPaths(example)
	if len(paths) == 0 {
		return nil, false
	}
	for _, p := range paths {
		if !ephemeralAndGone(p, root) {
			return nil, false
		}
	}
	return paths, true
}

// ephemeralAndGone: p does not exist, sits in an ephemeral location, and the
// worktree that held it (when it is in one) does not exist either. Every error
// other than "not found" counts as present — the probe fails open.
func ephemeralAndGone(p, root string) bool {
	if !pathGone(p) {
		return false // the source is still there, or unreadable
	}
	wtRoot, ok := ephemeralLocation(p, root)
	if !ok {
		return false
	}
	return wtRoot == "" || pathGone(wtRoot)
}

// ephemeralLocation reports whether p sits in an ephemeral location and, when
// that location is a worktree, the worktree's root (container segment plus the
// next one). Ephemeral is ONLY: a `worktrees` container segment (which covers
// `.claude/worktrees`), a directory segment ending in "-wt" (a sibling
// worktree container), or a temp dir. Being outside the repo root is not
// ephemeral on its own. Inside the root only a `worktrees` container counts:
// the repo's own files are not temp files even when the root lives in one.
func ephemeralLocation(p, root string) (wtRoot string, ok bool) {
	if rel, in := strings.CutPrefix(p, root+"/"); in {
		if wt, found := worktreeRoot(rel, false); found {
			return root + "/" + wt, true
		}
		return "", false
	}
	if wt, found := worktreeRoot(strings.TrimPrefix(p, "/"), true); found {
		return "/" + wt, true
	}
	for _, tmp := range []string{filepath.Clean(os.TempDir()) + "/", "/tmp/", "/private/var/folders/"} {
		if strings.HasPrefix(p, tmp) {
			return "", true
		}
	}
	return "", false
}

// worktreeRoot finds the innermost worktree container in the relative path rel
// (a `worktrees` segment, or with wtSuffix a directory segment ending in "-wt")
// and returns rel up to and including the segment after it.
func worktreeRoot(rel string, wtSuffix bool) (string, bool) {
	segs := strings.Split(rel, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if s != "worktrees" && (!wtSuffix || i == len(segs)-1 || !strings.HasSuffix(s, "-wt")) {
			continue
		}
		end := min(i+2, len(segs))
		return strings.Join(segs[:end], "/"), true
	}
	return "", false
}

// pathGone: os.Stat says p does not exist. Any other outcome (exists, permission
// denied, I/O error) is not evidence that it is gone.
func pathGone(p string) bool {
	_, err := os.Stat(p)
	return errors.Is(err, os.ErrNotExist)
}

// reLineCol is a compiler-style `:line` or `:line:col` suffix on a path token.
var reLineCol = regexp.MustCompile(`:\d+(?::\d+)?$`)

// extractAbsPaths pulls absolute paths out of an error message: a token that
// starts with "/" at a word boundary and runs to a quote, whitespace or ": ",
// with trailing quotes, commas and colons stripped and then a `:line[:col]`
// suffix. A URL (a token starting "//", or one right after "scheme:") is not
// a path. Tokens of fewer than two segments are ignored, and so is one that
// runs into a cut example's end.
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
		url := strings.HasPrefix(tok, "//") || followsScheme(example, i)
		i = end
		if url || (cut && atEnd) {
			continue // a URL, or possibly truncated mid-path
		}
		tok = strings.TrimRight(tok, `'",:`)
		tok = reLineCol.ReplaceAllString(tok, "")
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

// followsScheme: the "/" at i comes right after a URL scheme — "scheme:" where
// scheme is a letter followed by letters, digits, '+', '-' or '.', itself at a
// word boundary ("http:", "file:", "git+ssh:").
func followsScheme(s string, i int) bool {
	if i == 0 || s[i-1] != ':' {
		return false
	}
	j := i - 1
	for j > 0 && isSchemeChar(s[j-1]) {
		j--
	}
	if j == i-1 || !isASCIILetter(s[j]) {
		return false // empty scheme, or one starting with a digit ("x.go:21:/…")
	}
	return j == 0 || isPathBoundary(s[j-1])
}

func isSchemeChar(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'
}

func isASCIILetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

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
