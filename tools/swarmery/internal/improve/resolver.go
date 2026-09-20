package improve

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// agentNameRe is the sanitizer for a normalized agent name before it is
// interpolated into a repo path. It MUST be a single path segment: lowercase
// alphanumerics and hyphens only, no '/', '.', or '..' — so traversal
// (plugins/core/agents/../../evil.md) is impossible.
var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// agentPathRe matches a repo-relative agent definition path
// plugins/<pack>/agents/<name>.md and captures <pack> and <name>.
var agentPathRe = regexp.MustCompile(`^plugins/([^/]+)/agents/([a-z0-9][a-z0-9-]*)\.md$`)

// resolveAgentInRepo resolves the target agent file from the APPLY REPO at
// origin/main (the marketplace clone), NOT from the global DB registry. This is
// the fix for the cross-project resolver bug: the DB registry can hold
// project-local copies of same-named agents from OTHER checkouts, whose
// absolute paths lie outside the apply repo and fail `git apply`. Resolving
// from origin/main guarantees the returned relPath is repo-relative and the
// generated diff header (a/<relPath> b/<relPath>) applies at the worktree root.
//
//   - agent is the normalized (advisor.NormAgent-folded) name; it is sanitized
//     against agentNameRe so it can never inject path traversal.
//   - A best-effort `git fetch origin main` refreshes the ref (failures are
//     non-fatal — we fall back to whatever origin/main already exists).
//   - The file is located by listing origin/main and matching
//     plugins/<pack>/agents/<name>.md; plugins/core wins a collision, otherwise
//     the lexicographically smallest pack is chosen (deterministic).
//   - Content is `git show origin/main:<relPath>`.
//
// All git calls go through the Exec boundary so tests can fake them. exec may be
// nil only in a generation-only test Service; production always wires OSExec.
func resolveAgentInRepo(ex Exec, repo, agent string) (relPath, content string, err error) {
	if !agentNameRe.MatchString(agent) {
		return "", "", fmt.Errorf("%w: %q (invalid agent name)", ErrAgentNotFound, agent)
	}
	if repo == "" {
		return "", "", fmt.Errorf("%w: %q (no apply repo configured)", ErrAgentNotFound, agent)
	}
	if ex == nil {
		ex = OSExec{}
	}
	ctx := context.Background()

	// Best-effort refresh; a fetch failure (offline, no remote) is tolerated —
	// we resolve against whatever origin/main ref already exists.
	_, _ = ex.Run(ctx, repo, "git", "fetch", "origin", "main")

	relPath, err = locateAgentPath(ctx, ex, repo, agent)
	if err != nil {
		return "", "", err
	}

	out, err := ex.Run(ctx, repo, "git", "show", "origin/main:"+relPath)
	if err != nil {
		return "", "", fmt.Errorf("%w: %q (git show %s: %v)", ErrAgentNotFound, agent, relPath, err)
	}
	return relPath, out, nil
}

// locateAgentPath lists origin/main and returns the repo-relative path of the
// agent's definition file: plugins/core/agents/<name>.md when present, else the
// lexicographically smallest plugins/<pack>/agents/<name>.md. ErrAgentNotFound
// when no pack ships the agent.
func locateAgentPath(ctx context.Context, ex Exec, repo, agent string) (string, error) {
	out, err := ex.Run(ctx, repo, "git", "ls-tree", "-r", "--name-only", "origin/main")
	if err != nil {
		return "", fmt.Errorf("%w: %q (git ls-tree: %v)", ErrAgentNotFound, agent, err)
	}
	suffix := "/agents/" + agent + ".md"
	var matches []string
	for _, line := range strings.Split(out, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		m := agentPathRe.FindStringSubmatch(p)
		if m == nil || m[2] != agent {
			continue
		}
		if strings.HasSuffix(p, suffix) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: %q", ErrAgentNotFound, agent)
	}
	core := "plugins/core/agents/" + agent + ".md"
	for _, p := range matches {
		if p == core {
			return core, nil
		}
	}
	sort.Strings(matches)
	return matches[0], nil
}

// resolveSkillInRepo is resolveAgentInRepo's twin for a SKILL.md: it resolves
// plugins/<pack>/skills/<name>/SKILL.md from the APPLY REPO at origin/main, for
// exactly the same reason — the generated diff header (a/<relPath> b/<relPath>)
// must apply at the worktree root, and only origin/main can promise that.
//
// The skill name is sanitized against the same single-segment pattern the agent
// path uses, so `../../evil` can never be interpolated into a repo path.
// plugins/core wins a collision; otherwise the lexicographically smallest pack,
// so two packs shipping a same-named skill resolve deterministically.
func resolveSkillInRepo(ex Exec, repo, skill string) (relPath, content string, err error) {
	if !agentNameRe.MatchString(skill) {
		return "", "", fmt.Errorf("%w: %q (invalid skill name)", ErrSkillNotFound, skill)
	}
	if repo == "" {
		return "", "", fmt.Errorf("%w: %q (no apply repo configured)", ErrSkillNotFound, skill)
	}
	if ex == nil {
		ex = OSExec{}
	}
	ctx := context.Background()
	_, _ = ex.Run(ctx, repo, "git", "fetch", "origin", "main")

	out, err := ex.Run(ctx, repo, "git", "ls-tree", "-r", "--name-only", "origin/main")
	if err != nil {
		return "", "", fmt.Errorf("%w: %q (git ls-tree: %v)", ErrSkillNotFound, skill, err)
	}
	var matches []string
	for _, line := range strings.Split(out, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if m := skillPathRe.FindStringSubmatch(p); m != nil && m[2] == skill {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("%w: %q", ErrSkillNotFound, skill)
	}
	core := "plugins/" + corePack + "/skills/" + skill + "/SKILL.md"
	sort.Strings(matches)
	relPath = matches[0]
	for _, p := range matches {
		if p == core {
			relPath = core
			break
		}
	}
	body, err := ex.Run(ctx, repo, "git", "show", "origin/main:"+relPath)
	if err != nil {
		return "", "", fmt.Errorf("%w: %q (git show %s: %v)", ErrSkillNotFound, skill, relPath, err)
	}
	return relPath, body, nil
}

// repoAgentSet returns the set of agent names that ship at
// plugins/*/agents/*.md in origin/main of the apply repo — the agents the
// rewriter can act on. A missing repo or exec (generation disabled) yields an
// empty set (never a panic): the Improve button then hides for every agent.
func repoAgentSet(ex Exec, repo string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if repo == "" || ex == nil {
		return out, nil
	}
	ctx := context.Background()
	_, _ = ex.Run(ctx, repo, "git", "fetch", "origin", "main")
	lst, err := ex.Run(ctx, repo, "git", "ls-tree", "-r", "--name-only", "origin/main")
	if err != nil {
		return nil, fmt.Errorf("repo agent set: git ls-tree: %w", err)
	}
	for _, line := range strings.Split(lst, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if m := agentPathRe.FindStringSubmatch(p); m != nil {
			out[m[2]] = struct{}{}
		}
	}
	return out, nil
}
