// Package repopath answers ONE question: which git repository root should a run
// execute in?
//
// projects.path is the project ROOT, and for a multi-repo project that root is an
// umbrella directory holding N checkouts with no .git of its own. Handing it to git
// is what made every plan and phase run in such a project die during admission with
// "fatal: not a git repository (or any parent up to mount point …)" — before a
// worktree was ever acquired, so nothing about the failure named the real cause
// (2026-07-30, task 48 / project Skygor).
//
// The plan format already names the repo per phase: the README sequencing table's
// `Repo` column and each phase doc's `| **Repo** | … |` header row, both stored raw
// in epic_phases.repo. This package turns those cells — plus project.json's
// mainApp/repos — into an absolute, validated repo root, or explains what it tried.
//
// It reads the filesystem and nothing else: no DB, no git subprocess (running git
// to find out whether git works is how we got the unusable error message), no
// config. Callers decide the priority order of the cells they pass.
package repopath

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNoRepoRoot: no candidate passed validation. Always wrapped with the candidates
// that were tried, so the API can say WHAT was checked instead of echoing git's
// stderr at the user.
var ErrNoRepoRoot = errors.New("no git repository to run in")

// ErrRepoOutsideProject: a declared Repo cell named a path that IS a git repository
// but lies outside the project root and outside every trusted root. Always wraps
// ErrNoRepoRoot as well, so callers that only know the older sentinel still see a
// refusal — but this one is refused LOUDLY, at admission, instead of falling through
// to the project path: a doc that says "run in /abs/other-repo" and a run that then
// quietly executes in the project checkout is a run doing the wrong work in the
// wrong place, and every retry burns another one.
var ErrRepoOutsideProject = errors.New("declared repo is outside the project")

// backtickRe pulls the `wrapped` fragments out of a declared Repo cell.
var backtickRe = regexp.MustCompile("`([^`]+)`")

// placeholder cells that declare nothing.
func isPlaceholder(tok string) bool {
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "", "—", "-", "–", "n/a", "na", "none", "tbd":
		return true
	}
	return false
}

// Tokens splits a declared Repo cell into candidate tokens, most specific first.
// It handles every shape the plan format actually produces:
//
//	"`sk-next` (`/Volumes/Work/Skygor/sk-next`)" → ["/Volumes/Work/Skygor/sk-next", "sk-next"]
//	"sk-next (+ helm)"                           → ["sk-next"]
//	"`sk-next` (+ Helm in `sk-k8s-next` / `dk-infrastructure`)"
//	                                             → ["sk-next", "sk-k8s-next", "dk-infrastructure"]
//
// Absolute paths sort first because they are the least ambiguous thing a doc can
// say; the relative order of everything else is preserved. Pure.
func Tokens(cell string) []string {
	var abs, rel []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.TrimSpace(strings.Trim(strings.TrimSpace(tok), "*"))
		if isPlaceholder(tok) || seen[tok] {
			return
		}
		seen[tok] = true
		if filepath.IsAbs(tok) {
			abs = append(abs, tok)
			return
		}
		rel = append(rel, tok)
	}

	if m := backtickRe.FindAllStringSubmatch(cell, -1); len(m) > 0 {
		for _, g := range m {
			add(g[1])
		}
		return append(abs, rel...)
	}

	// No backticks: take the text up to the first separator that starts a
	// parenthetical or a list ("sk-next (+ helm)", "sk-next, sk-controlbox").
	// "/" is NOT a separator here — a declared repo may legitimately be a nested
	// path ("tools/swarmery"), and cutting at the slash would silently resolve the
	// run to the wrong directory rather than failing.
	head := cell
	if i := strings.IndexAny(head, "(,·|"); i >= 0 {
		head = head[:i]
	}
	add(head)
	return append(abs, rel...)
}

// Primary is the cell's declared repo NAME — Tokens' first non-absolute token, or
// the basename of its first absolute one. "" when the cell declares nothing.
//
// It is the identity planrun compares across phases to decide whether a plan spans
// repos: comparing raw cells would call "`sk-next`" and "sk-next (+ helm)" two
// different repositories and refuse a plan that lives in exactly one. Pure.
func Primary(cell string) string {
	for _, tok := range Tokens(cell) {
		if !filepath.IsAbs(tok) {
			return tok
		}
	}
	if toks := Tokens(cell); len(toks) > 0 {
		return filepath.Base(toks[0])
	}
	return ""
}

// Resolve picks the git repository root a run executes in.
//
// projectPath is projects.path; cells are declared Repo cells in priority order
// (phase doc header, then plan README row, then project.json-derived hints — the
// caller owns that order, this package does not read the DB or any config).
//
// A candidate is accepted only when it exists, holds a .git entry, and resolves
// INSIDE projectPath (or equals it). Rejected candidates fall through to the next
// one, and projectPath itself is always the final candidate — so a single-repo
// project resolves exactly as it did before this package existed, even when a doc
// declares a repo that is not on disk. That fallback is the backward-compatibility
// guarantee, not a nicety: every other project in the registry depends on it.
//
// The one candidate that does NOT fall through is a declared path that is a real
// git repository outside the project (ErrRepoOutsideProject): that is an explicit
// instruction the guard refused, and running somewhere else instead is the bug
// ResolveTrusted exists to close. Resolve is ResolveTrusted with no trusted roots.
func Resolve(projectPath string, cells ...string) (string, error) {
	return ResolveTrusted(projectPath, nil, cells...)
}

// ResolveTrusted is Resolve with an allow-list: a declared candidate that resolves
// inside one of the trusted roots is accepted even though it lies outside
// projectPath. The engines pass the registered projects' paths, so a phase in one
// project's plan may declare `**Repo:** /abs/other-project` and run there — but only
// if the operator has already brought that checkout under the daemon. A Repo cell is
// untrusted text out of a markdown file; the registry is the boundary that keeps it
// from placing a worktree anywhere on the disk.
func ResolveTrusted(projectPath string, trusted []string, cells ...string) (string, error) {
	if strings.TrimSpace(projectPath) == "" {
		return "", fmt.Errorf("%w: no project path", ErrNoRepoRoot)
	}
	roots := containmentRoots(projectPath, trusted)
	var tried []string
	try := func(cand string) (string, acceptance) {
		for _, t := range tried {
			if t == cand {
				return "", rejectedNotRepo // already rejected — do not re-stat or re-report it
			}
		}
		tried = append(tried, cand)
		return accept(roots, cand)
	}

	for _, cell := range cells {
		for _, tok := range Tokens(cell) {
			cand := tok
			if !filepath.IsAbs(cand) {
				cand = filepath.Join(projectPath, cand)
			}
			real, verdict := try(cand)
			switch verdict {
			case accepted:
				return real, nil
			case rejectedOutside:
				return "", fmt.Errorf("%w: %s is a git repository outside project %s and is not a registered project — register it as a project, or move the phase to the plan of the project that owns it (%w)",
					ErrRepoOutsideProject, real, projectPath, ErrNoRepoRoot)
			}
		}
	}
	if real, verdict := try(projectPath); verdict == accepted {
		return real, nil
	}
	return "", fmt.Errorf("%w: %s is not a git repository and no declared repo resolved (tried: %s)",
		ErrNoRepoRoot, projectPath, strings.Join(tried, ", "))
}

// acceptance is accept's verdict on one candidate.
type acceptance int

const (
	accepted        acceptance = iota
	rejectedNotRepo            // missing, unreadable, or no .git entry — falls through
	rejectedOutside            // a real repository, but outside every containment root
)

// containmentRoots resolves projectPath plus the trusted roots to their symlink-free
// forms, dropping any that do not exist. projectPath is always first.
func containmentRoots(projectPath string, trusted []string) []string {
	roots := make([]string, 0, 1+len(trusted))
	seen := map[string]bool{}
	for _, r := range append([]string{projectPath}, trusted...) {
		if strings.TrimSpace(r) == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(r)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		roots = append(roots, real)
	}
	return roots
}

// accept validates one candidate: it must exist, carry a .git entry, and live
// inside one of the (already symlink-resolved) containment roots. Returns the
// symlink-resolved path with the verdict; the path is set for rejectedOutside too,
// so the refusal can name what it refused.
//
// EvalSymlinks runs BEFORE the containment check on purpose. A string-prefix test
// on the declared path would pass for a symlink that sits inside the project and
// points anywhere on the disk, and the cell it came from is untrusted text out of a
// markdown file — that is the one input that must not be able to place a worktree
// outside the project.
func accept(roots []string, cand string) (string, acceptance) {
	real, err := filepath.EvalSymlinks(cand)
	if err != nil {
		return "", rejectedNotRepo
	}
	// A .git DIRECTORY is a normal checkout; a .git FILE is a linked worktree or a
	// submodule. Both are repositories git can run in, so both are accepted.
	if _, err := os.Stat(filepath.Join(real, ".git")); err != nil {
		return "", rejectedNotRepo
	}
	for _, root := range roots {
		if within(root, real) {
			return real, accepted
		}
	}
	return real, rejectedOutside
}

// within reports whether path equals root or sits underneath it (both already
// symlink-resolved).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// overlayProject is the subset of a consumer's project.json this package reads.
type overlayProject struct {
	MainApp string   `json:"mainApp"`
	Repos   []string `json:"repos"`
}

// FileHints reads a project.json (a workspace's overlay/project.json or a
// checkout's .claude/project.json) and returns its declared repo hints in priority
// order: mainApp first, then repos[] when it holds exactly ONE entry.
//
// A longer repos[] is deliberately ignored: it lists what agents may search, not
// where a run belongs, and picking one of seven would be a guess presented as a
// decision. Missing or unparseable file ⇒ nil, no error — a hint source is
// advisory, and a broken overlay must not block a run whose phase doc already says
// the answer.
func FileHints(jsonPath string) []string {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil
	}
	var p overlayProject
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	var out []string
	if !isPlaceholder(p.MainApp) {
		out = append(out, strings.TrimSpace(p.MainApp))
	}
	if len(p.Repos) == 1 && !isPlaceholder(p.Repos[0]) {
		out = append(out, strings.TrimSpace(p.Repos[0]))
	}
	return out
}

// SameDir reports whether two paths name the same directory, comparing them
// AFTER symlink resolution.
//
// filepath.Clean is not enough and the difference is not cosmetic: Resolve returns
// an EvalSymlinks'd path while projects.path is stored raw, so on macOS (/var →
// /private/var, and any project reached through a symlinked mount) a single-repo
// project would compare as "run root ≠ project root" and get treated as multi-repo
// — inheriting settings it should not and being told, wrongly, that its worktree is
// a checkout inside the project. Falls back to Clean for paths that cannot be
// resolved, which is the best available answer, not a guess about equality.
func SameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	resolve := func(p string) string {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}

// InheritedSettings names the project settings file a run should be handed on
// the command line, or "" when it needs none.
//
// A worktree is never the project directory: Claude Code discovers
// .claude/settings.json by walking up from cwd, and a worktree lives under
// ~/.swarmery/worktrees/…, so it inherits nothing from the project. That is
// invisible while a project keeps its .claude/ committed INSIDE the repo the
// worktree is cut from — the checkout carries it. It stops being invisible the
// moment the run root is a sub-repo: project Skygor declares core@swarmery in
// /Volumes/Work/Skygor/.claude/settings.json, the run happens in a checkout of
// sk-next, and the plan run died with "--agent 'tech-lead' not found" because the
// plugin that ships that agent was never enabled for the session (2026-07-30).
//
// Rules, in order:
//   - repoRoot == projectPath ⇒ "". The run IS a checkout of the project repo;
//     whatever it carries is what the project chose to commit, and lending it a
//     second copy would change behaviour for every existing project.
//   - the worktree already has .claude/settings.json ⇒ "". The repo made its own
//     statement, and it is the more specific one — same precedence rule the phase
//     doc gets over the plan README.
//   - otherwise the project's settings file, when it exists.
func InheritedSettings(projectPath, repoRoot, worktreePath string) string {
	if projectPath == "" || repoRoot == "" || SameDir(projectPath, repoRoot) {
		return ""
	}
	if worktreePath != "" {
		if _, err := os.Stat(filepath.Join(worktreePath, ".claude", "settings.json")); err == nil {
			return ""
		}
	}
	settings := filepath.Join(projectPath, ".claude", "settings.json")
	if _, err := os.Stat(settings); err != nil {
		return ""
	}
	return settings
}
