package archmap

// Map freshness for BOTH shapes of project the dashboard indexes.
//
// A single-repo project is the easy half: its root IS a git checkout, so
// "is the map behind the code" is one HEAD read against the commit the map
// recorded. A multi-repo workspace has no .git at its root at all — the code
// lives in member checkouts listed under `repos[]` in .claude/project.json —
// and every consumer that resolved HEAD from the root alone silently answered
// "unknown" for it. That is the worst possible failure mode for a freshness
// signal: it does not read as stale, it reads as fine.
//
// ResolveFreshness is the ONE answer both the /api/tools DTO and advisor R7
// consume, so the page and the advisor cannot disagree about the same project.
//
// Three degradations, all deliberate:
//
//   - A member repo that does not exist, or exists but is not a checkout, comes
//     back with OK=false and STAYS in the list. Dropping it would shrink the
//     denominator and make a half-visible workspace read as a fully fresh one.
//   - A map that stamps only the scalar `analyzedAtCommit` cannot say which of
//     eight repos that commit belongs to, so a multi-repo map without the
//     per-repo `analyzedAtCommits` object is Comparable=false — unknown, not
//     fresh. Callers that also hold the map's age (R7 does) fall back to age.
//   - A stamp shorter than the schema's 7-char minimum is ignored rather than
//     compared, because a 3-char "sha" is a typo, not a commit.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
)

// minStampLen is the shortest analyzed-commit stamp treated as a commit at all
// — the same floor the architecture-map schema enforces (minLength 7).
const minStampLen = 7

// RepoHead is one member checkout of a multi-repo workspace: what the project
// declared, where it landed on disk, what its HEAD is now, and what the map
// recorded for it.
type RepoHead struct {
	// Name is the entry as written in project.json `repos[]` — workspace
	// relative, and also the prefix module paths in a multi-repo map carry.
	Name string
	// Path is the absolute checkout path (projectPath + Name).
	Path string
	// Head is the resolved HEAD commit, "" when OK is false.
	Head string
	// Analyzed is the commit the map recorded FOR THIS REPO, "" when the map
	// carries no per-repo stamps.
	Analyzed string
	// OK reports that Path is a readable git checkout. False keeps the repo in
	// the list as an explicit unknown.
	OK bool
}

// Measurable reports that both halves of this repo's comparison are known.
func (r RepoHead) Measurable() bool { return r.OK && r.Analyzed != "" }

// Stale reports that this repo's HEAD has moved past the commit the map
// recorded for it. Never true on a half-known repo — see Measurable.
func (r RepoHead) Stale() bool { return r.Measurable() && r.Analyzed != r.Head }

// Freshness is the whole answer for one project.
type Freshness struct {
	// Single is the root HEAD when the project root is itself a checkout; nil
	// when it is not one, or when its .git could not be read. Repos is empty
	// in that case and the wire shape is byte-identical to what single-repo
	// projects have always sent.
	Single *string
	// Repos is the per-member answer for a multi-repo workspace, in the order
	// project.json declares them. Empty for a single-repo project.
	Repos []RepoHead
	// Analyzed is the map's scalar `analyzedAtCommit` stamp ("" when absent or
	// shorter than the 7-char floor).
	Analyzed string
	// Stale reports that a commit comparison PROVED the map trails the code.
	// False on its own means "not proven", which is why Comparable exists.
	Stale bool
	// Comparable reports that at least one repo had both halves known, i.e.
	// that Stale is a measurement rather than a default.
	Comparable bool
}

// MultiRepo reports whether this answer describes a multi-repo workspace.
func (f Freshness) MultiRepo() bool { return len(f.Repos) > 0 }

// ResolvedRepos counts member checkouts whose HEAD was readable — the repos
// this answer actually saw, as opposed to the ones project.json promised.
func (f Freshness) ResolvedRepos() int {
	n := 0
	for _, r := range f.Repos {
		if r.OK {
			n++
		}
	}
	return n
}

// StaleRepos counts member checkouts proven to have moved past the map.
func (f Freshness) StaleRepos() int {
	n := 0
	for _, r := range f.Repos {
		if r.Stale() {
			n++
		}
	}
	return n
}

// ResolveFreshness answers the freshness question for the project rooted at
// projectPath. It never fails: everything it cannot read becomes an explicit
// unknown on the result.
//
// (The plan writes this as `Freshness(projectPath) Freshness`; Go will not let
// a package hold a func and a type of the same name, so the TYPE keeps the
// plan's name and the constructor is spelled out.)
func ResolveFreshness(projectPath string) Freshness {
	analyzed, perRepo := analyzedCommits(projectPath)
	f := Freshness{Analyzed: analyzed}

	// A .git entry at the root — directory (normal checkout) or file (linked
	// worktree / submodule) — settles the shape on its own. Presence, not
	// resolvability: a corrupt .git makes this an UNREADABLE single repo, not
	// a multi-repo workspace whose members we should go hunting for.
	if _, err := os.Stat(filepath.Join(projectPath, ".git")); err == nil {
		if head, ok := githead.Resolve(projectPath); ok {
			h := head
			f.Single = &h
			f.Comparable = analyzed != ""
			f.Stale = f.Comparable && head != analyzed
		}
		return f
	}

	names := projectRepos(projectPath)
	if len(names) == 0 {
		return f
	}
	f.Repos = make([]RepoHead, 0, len(names))
	for _, name := range names {
		r := RepoHead{
			Name:     name,
			Path:     filepath.Join(projectPath, filepath.FromSlash(name)),
			Analyzed: perRepo[name],
		}
		if head, ok := githead.Resolve(r.Path); ok {
			r.Head, r.OK = head, true
		}
		if r.Measurable() {
			f.Comparable = true
		}
		if r.Stale() {
			f.Stale = true
		}
		f.Repos = append(f.Repos, r)
	}
	return f
}

// analyzedCommits reads the two freshness stamps out of architecture-map.json:
// the scalar `analyzedAtCommit` every map has carried since schemaVersion 1,
// and the optional `analyzedAtCommits` object a multi-repo map adds. Both are
// best-effort — a missing, unreadable or unparseable map yields empty stamps,
// never an error, because "we could not tell" is the honest answer and the
// caller already renders it.
//
// This deliberately does NOT go through Load: Load enforces the full v1
// contract (and refuses a future schemaVersion), while freshness only needs two
// scalars and should keep answering for a map it would not otherwise decode.
func analyzedCommits(projectPath string) (string, map[string]string) {
	raw, err := os.ReadFile(filepath.Join(projectPath, OutDir, MapFileName))
	if err != nil {
		return "", nil
	}
	var meta struct {
		AnalyzedAtCommit  string            `json:"analyzedAtCommit"`
		AnalyzedAtCommits map[string]string `json:"analyzedAtCommits"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return "", nil
	}
	single := stamp(meta.AnalyzedAtCommit)
	var per map[string]string
	for k, v := range meta.AnalyzedAtCommits {
		k, v = normPath(k), stamp(v)
		if k == "" || v == "" {
			continue
		}
		if per == nil {
			per = map[string]string{}
		}
		per[k] = v
	}
	return single, per
}

// stamp normalises a commit stamp, returning "" for anything below the 7-char
// floor — short enough to be a typo, and short enough that slicing it for a
// message would panic.
func stamp(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < minStampLen {
		return ""
	}
	return s
}

// projectRepos reads the member repos a workspace declares in
// .claude/project.json. Entries that could resolve outside the project root are
// dropped: the file is operator-owned, but a "../other-project" entry would
// have this package reading (and later running git in) a checkout the project
// never claimed.
func projectRepos(projectPath string) []string {
	raw, err := os.ReadFile(filepath.Join(projectPath, ".claude", "project.json"))
	if err != nil {
		return nil
	}
	var cfg struct {
		Repos []string `json:"repos"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, name := range cfg.Repos {
		name = normPath(name)
		if !safeRepoRel(name) {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// safeRepoRel reports whether a declared repo entry is a plain relative path
// inside the project root.
func safeRepoRel(name string) bool {
	if name == "" || name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
