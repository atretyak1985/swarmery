package improve

// Target vocabulary of the improve loop (agent-memory phase 5, migration 0074).
//
// Until phase 5 the loop could rewrite exactly one kind of file — an agent
// definition — and "the target" was implicit in a single column. R11 adds a
// second kind: a SKILL.md that should have carried a lesson the fleet keeps
// re-learning. Every gate in apply.go branches on the kind, so the vocabulary
// and the path predicates live here rather than being re-derived at each call
// site.
//
// The path predicates are deliberately EXACT shapes, not prefixes. They are the
// allow-list the hard path-scope gate is built on: "is this repo-relative path
// something the loop is permitted to edit at all". A prefix test would admit
// plugins/core/skills/x/resources/evil.md, which this phase explicitly does not
// let the loop touch.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Target kinds stored in agent_change_proposals.target_kind (migration 0074).
const (
	TargetAgent = "agent"
	TargetSkill = "skill"
)

// StatusNeedsTarget is the proposal status for a routed skill recommendation
// whose lesson did not name a resolvable SKILL.md: the evidence is real, the
// file is unknown, and an operator picks it on the Retro page. It counts as OPEN
// for the one-open-per-target invariant (migration 0074's partial index), so one
// unresolvable lesson cannot pile up duplicate rows.
const StatusNeedsTarget = "needs_target"

// skillPathRe matches a repo-relative skill definition path
// plugins/<pack>/skills/<name>/SKILL.md and captures <pack> and <name>.
var skillPathRe = regexp.MustCompile(`^plugins/([^/]+)/skills/([a-z0-9][a-z0-9-]*)/SKILL\.md$`)

// actionSkillRe pulls the skill name out of a lesson's `**Action**:` line: the
// routing key is the literal `skills/<name>` token the fleet writes when it says
// which procedure should have carried the lesson.
var actionSkillRe = regexp.MustCompile(`skills/([a-z0-9][a-z0-9-]*)`)

// isSkillFile matches plugins/<pack>/skills/<name>/SKILL.md. It delegates to
// skillPathRe so the admission test and skillNameOf agree BY CONSTRUCTION: a
// hand-rolled segment count was looser than the regexp, so
// plugins/core/skills/My_Skill/SKILL.md was admitted as a target while
// skillNameOf returned "" and the branch name and PR label silently fell back to
// the lesson sentence.
//
// A sibling resources/x.md under the same skill is NOT a skill file: the loop
// may not edit it in this phase, and the path-scope gate rejects a diff that
// touches one.
func isSkillFile(p string) bool { return skillPathRe.MatchString(p) }

// isEditableTarget is the allow-list the apply pipeline validates its stored
// target against before any git op: an agent definition or a SKILL.md, nothing
// else. It is the only relaxation phase 5 makes to the path gate.
func isEditableTarget(p string) bool { return isAgentFile(p) || isSkillFile(p) }

// skillNameOf returns the <name> of a plugins/<pack>/skills/<name>/SKILL.md path.
func skillNameOf(p string) string {
	if m := skillPathRe.FindStringSubmatch(p); m != nil {
		return m[2]
	}
	return ""
}

// packOf returns the <pack> owning a repo-relative plugins/<pack>/… path.
func packOf(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[0] != "plugins" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// packOnly returns the single plugin pack every changed path belongs to, or
// ok=false when the set is empty or spans more than one pack. It generalizes the
// pre-phase-5 coreOnly test: the semver bump follows whichever pack owns the
// edited file, and plugins/core additionally mirrors the marketplace version.
func packOnly(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	pack := ""
	for _, p := range paths {
		got, ok := packOf(p)
		if !ok {
			return "", false
		}
		if pack == "" {
			pack = got
			continue
		}
		if got != pack {
			return "", false
		}
	}
	return pack, pack != ""
}

// bumpPackSemver patch-bumps plugins/<pack>/.claude-plugin/plugin.json in the
// worktree, and — for the core plugin only — keeps the marketplace
// metadata.version in lockstep (the marketplace version tracks core by
// convention; a domain pack's bump is its own).
func (s *Service) bumpPackSemver(tmp, pack string) error {
	pjPath := filepath.Join(tmp, "plugins", pack, ".claude-plugin", "plugin.json")
	pjRaw, err := s.Exec.ReadFile(pjPath)
	if err != nil {
		return fmt.Errorf("read %s plugin.json: %w", pack, err)
	}
	loc := versionFieldRe.FindSubmatch(pjRaw)
	if loc == nil {
		return fmt.Errorf("%s plugin.json: no version field", pack)
	}
	next, err := bumpPatch(string(loc[2]))
	if err != nil {
		return fmt.Errorf("%s plugin.json: %w", pack, err)
	}
	pjNew, _, err := bumpVersionField(pjRaw, next)
	if err != nil {
		return err
	}
	if err := s.Exec.WriteFile(pjPath, pjNew); err != nil {
		return err
	}
	if pack != corePack {
		return nil
	}

	mpPath := filepath.Join(tmp, ".claude-plugin/marketplace.json")
	mpRaw, err := s.Exec.ReadFile(mpPath)
	if err != nil {
		return fmt.Errorf("read marketplace.json: %w", err)
	}
	mpNew, _, err := bumpVersionField(mpRaw, next)
	if err != nil {
		return fmt.Errorf("marketplace.json: %w", err)
	}
	return s.Exec.WriteFile(mpPath, mpNew)
}
