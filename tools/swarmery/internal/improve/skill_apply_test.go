package improve

// Phase-5 apply tests for SKILL.md targets.
//
// Two halves, deliberately kept apart:
//   - fakeExec covers the whole Apply pipeline (sha re-check → worktree →
//     gates → bump → PR) for a skill row, which needs an origin/main a throwaway
//     repo cannot cheaply provide;
//   - REAL git covers the one claim that is only worth anything against real
//     git: a diff touching SKILL.md AND a sibling resources/ file is rejected
//     with gate "path scope". That is the security boundary of this phase — a
//     skill's resources look editable and are not — so it is proven with the
//     same commands runApply runs, not with a canned string.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const skillRel = "plugins/core/skills/docker-build/SKILL.md"

// skillBody is the SKILL.md the fixture diff applies against. The frontmatter
// carries more keys than an agent's (version, owner) on purpose: the gate must
// pass a real skill header, not a minimal one invented to suit it.
const skillBody = `---
name: docker-build
version: "1.0.0"
owner: "swarmery-core"
description: "Build and push the project's images."
---

# Rules (never violate)

1. Build from a clean checkout; never reuse a stale layer cache across branches.
`

// loadSkillFixture reads testdata/fixtures/improve/skill-proposal.diff — the
// committed fixture the phase requires, so the test and the artifact cannot
// drift apart.
func loadSkillFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "improve", "skill-proposal.diff"))
	if err != nil {
		t.Fatalf("read skill fixture: %v", err)
	}
	return string(b)
}

// seedApprovedSkillProposal inserts an approved skill proposal whose
// base_sha256 matches skillBody — the shape RouteSkill would have written.
func seedApprovedSkillProposal(t *testing.T, db *sql.DB, id int64, lesson, diff string) {
	t.Helper()
	sum := sha256.Sum256([]byte(skillBody))
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(id, agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, '', 'skill', ?, ?, ?, 'because the fleet re-learned it', 'approved',
		        '2026-09-20T09:08:07.000Z')`,
		id, lesson, skillRel, hex.EncodeToString(sum[:]), diff); err != nil {
		t.Fatal(err)
	}
}

// skillExec is baseExec's skill twin: origin/main serves skillBody, the staged
// numstat reports only the SKILL.md, and the worktree holds a valid skill
// header plus a bumpable core manifest pair.
func skillExec(tmp string) *fakeExec {
	f := newFakeExec()
	f.tmpDir = tmp
	f.runResp["git show"] = struct {
		out string
		err error
	}{out: skillBody}
	f.runResp["bash scan"] = struct {
		out string
		err error
	}{out: "── Flavor scan ──\n✓ clean — no project/domain tokens remain\n"}
	f.runResp["git diff"] = struct {
		out string
		err error
	}{out: "2\t0\t" + skillRel + "\n"}
	f.runResp["gh pr"] = struct {
		out string
		err error
	}{out: "https://github.com/atretyak1985/swarmery/pull/1234\n"}
	f.files[filepath.Join(tmp, skillRel)] = []byte(skillBody)
	f.files[filepath.Join(tmp, "plugins/core/.claude-plugin/plugin.json")] =
		[]byte("{\n  \"name\": \"core\",\n  \"version\": \"3.5.0\"\n}\n")
	f.files[filepath.Join(tmp, ".claude-plugin/marketplace.json")] =
		[]byte("{\n  \"metadata\": { \"version\": \"3.5.0\" }\n}\n")
	return f
}

// TestApplySkillProposalHappyPath: the fixture skill proposal goes end to end —
// sha re-check against origin/main, worktree on a skill-improve branch, all four
// gates, the core semver bump, and a PR whose body cites the lesson metric
// rather than the agent one.
func TestApplySkillProposalHappyPath(t *testing.T) {
	db := applyDB(t)
	seedApprovedSkillProposal(t, db, 1, "sync the plugin cache before building", loadSkillFixture(t))
	f := skillExec("/tmp/wt-skill")
	svc := &Service{DB: db, Repo: "/repo", Exec: f}

	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	status, prURL, errCol := applyRow(t, db, 1)
	if status != "applied" {
		t.Fatalf("status = %q (err %v), want applied", status, deref(errCol))
	}
	if prURL == nil || !strings.Contains(*prURL, "/pull/1234") {
		t.Fatalf("pr_url = %v, want the gh URL", prURL)
	}

	// The branch is named after the SKILL, not after the lesson sentence stored
	// in the `agent` column.
	branch := ""
	for _, r := range f.runs {
		if strings.Contains(r, "skill-improve/") {
			branch = r
		}
	}
	if branch == "" {
		t.Fatalf("no skill-improve branch created; runs=%v", f.runs)
	}
	if !strings.Contains(branch, "skill-improve/docker-build-20260920") {
		t.Errorf("branch = %q, want skill-improve/docker-build-20260920", branch)
	}

	// Core-owned skill ⇒ plugin.json AND the marketplace metadata bump together.
	pj := f.writes[filepath.Join("/tmp/wt-skill", "plugins/core/.claude-plugin/plugin.json")]
	if !strings.Contains(string(pj), "3.5.1") {
		t.Errorf("core plugin.json not bumped: %s", pj)
	}
	mp := f.writes[filepath.Join("/tmp/wt-skill", ".claude-plugin/marketplace.json")]
	if !strings.Contains(string(mp), "3.5.1") {
		t.Errorf("marketplace.json not bumped: %s", mp)
	}

	// The PR must state which claim it is making.
	prArgs := ""
	for _, r := range f.runs {
		if strings.HasPrefix(r, "gh pr ::") {
			prArgs = r
		}
	}
	if !strings.Contains(prArgs, "improve docker-build skill") {
		t.Errorf("PR title does not name the skill: %q", prArgs)
	}
	if !strings.Contains(prArgs, "lesson_task_count") {
		t.Errorf("PR body does not carry the skill verify plan: %q", prArgs)
	}
}

// TestApplySkillRejectsNonSkillTarget: a skill row whose target_path is not a
// SKILL.md (a needs_target row an operator approved by hand, or a tampered row)
// dies at the allow-list, before any git op.
func TestApplySkillRejectsNonSkillTarget(t *testing.T) {
	db := applyDB(t)
	seedApprovedSkillProposal(t, db, 1, "a lesson", "irrelevant")
	if _, err := db.Exec(
		`UPDATE agent_change_proposals SET target_path = 'plugins/core/skills/x/resources/x.md' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	f := skillExec("/tmp/wt-skill-bad")
	svc := &Service{DB: db, Repo: "/repo", Exec: f}
	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply returns nil (outcome on the row): %v", err)
	}
	status, _, errCol := applyRow(t, db, 1)
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if errCol == nil || !strings.Contains(*errCol, "path scope") {
		t.Errorf("error = %v, want the path-scope rejection", deref(errCol))
	}
	if f.ranSig("git fetch") || f.ranSig("git worktree") {
		t.Error("an invalid target must be rejected before any git op")
	}
}

// initSkillRepo builds a throwaway git repo with a committed SKILL.md plus a
// sibling resources/ file, and returns the repo dir.
func initSkillRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	ex := OSExec{}
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if out, err := ex.Run(ctx, repo, "git", args...); err != nil {
			t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")

	dir := filepath.Join(repo, "plugins/core/skills/docker-build")
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, skillRel), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resources", "x.md"),
		[]byte("# procedure detail\n\nstep one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	return repo
}

// TestRealGitSkillFixtureApplies is the positive control for the committed
// fixture: it applies cleanly to a real SKILL.md and passes the scope gate and
// the line cap.
func TestRealGitSkillFixtureApplies(t *testing.T) {
	repo := initSkillRepo(t)
	patch := filepath.Join(repo, ".improve.patch")
	if err := os.WriteFile(patch, []byte(loadSkillFixture(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := (OSExec{}).Run(context.Background(), repo, "git", "apply", ".improve.patch"); err != nil {
		t.Fatalf("the committed fixture does not apply to the SKILL.md: %v (%s)", err, out)
	}
	if err := os.Remove(patch); err != nil {
		t.Fatal(err)
	}
	if err := stageAndGate(t, repo, skillRel); err != nil {
		t.Fatalf("an in-scope SKILL.md edit must pass the gate, got: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repo, skillRel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Sync the plugin cache before building") {
		t.Errorf("the fixture did not land its rule:\n%s", body)
	}
	if !frontmatterOK(string(body)) {
		t.Error("the fixture broke the SKILL.md frontmatter contract")
	}
}

// TestRealGitSkillResourcesSiblingRejected is THE gate of this phase: a diff
// that edits the SKILL.md and also a sibling resources/x.md is rejected with
// gate "path scope". Skills referencing resources/ cannot be edited by the loop
// in this phase, and "cannot" has to mean the pipeline refuses, not that the
// prompt asks nicely.
func TestRealGitSkillResourcesSiblingRejected(t *testing.T) {
	repo := initSkillRepo(t)
	if err := os.WriteFile(filepath.Join(repo, skillRel),
		[]byte(strings.Replace(skillBody, "1. Build from", "1. Always build from", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(repo, "plugins/core/skills/docker-build/resources/x.md")
	if err := os.WriteFile(sibling, []byte("# procedure detail\n\nstep one\nstep two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := stageAndGate(t, repo, skillRel)
	assertRejected(t, err, "path scope")
	if !strings.Contains(err.Error(), "resources/x.md") {
		t.Errorf("rejection = %q, want it to name the offending sibling", err.Error())
	}
}

// TestRealGitSkillCreatedResourceRejected: creating a NEW resources file is the
// same bypass in create clothing — untracked after `git apply`, visible only
// because the gate reads the STAGED tree.
func TestRealGitSkillCreatedResourceRejected(t *testing.T) {
	repo := initSkillRepo(t)
	if err := os.WriteFile(filepath.Join(repo, skillRel),
		[]byte(strings.Replace(skillBody, "1. Build from", "1. Always build from", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(repo, "plugins/core/skills/docker-build/resources/new.md")
	if err := os.WriteFile(created, []byte("# smuggled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, stageAndGate(t, repo, skillRel), "path scope")
}
