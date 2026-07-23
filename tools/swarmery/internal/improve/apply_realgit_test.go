package improve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise the path-scope + line-cap gate against REAL git via
// OSExec, not the canned-string fakeExec used by the happy-path tests. They
// prove the P0 bypass is closed: a diff that CREATES an untracked file (or
// renames the agent away, or injects a large new file) is visible to the gate
// because we stage (`git add -A`) BEFORE gating and read the staged diff
// (`git -c core.quotepath=false diff --cached --numstat --no-renames HEAD`).
//
// The full Apply pipeline needs an `origin/main` to fetch + worktree from, which
// a throwaway repo can't cheaply provide, so these drive the security-critical
// sequence directly: apply patch → git add -A → changedPaths → checkPathScope,
// with the exact same commands runApply uses. If checkPathScope rejects, runApply
// fails the row and never commits/pushes/opens a PR — asserted at the sequence
// level here and end-to-end by the fakeExec TestApplyPathScopeGate.

// initAgentRepo creates a throwaway git repo with a committed core agent file and
// returns the repo dir and the repo-relative agent path.
func initAgentRepo(t *testing.T) (repo, agentRel string) {
	t.Helper()
	repo = t.TempDir()
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

	agentRel = "plugins/core/agents/tech-lead.md"
	if err := os.MkdirAll(filepath.Join(repo, "plugins/core/agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, agentRel),
		[]byte("---\nname: tech-lead\ndescription: orchestrator\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	return repo, agentRel
}

// stageAndGate replays runApply's exact security-critical sequence against a real
// repo: git add -A (stage untracked creates/renames), then the numstat gate, then
// checkPathScope. It returns the gate error (nil = passed).
func stageAndGate(t *testing.T, repo, agentRel string) error {
	t.Helper()
	svc := &Service{Repo: repo, Exec: OSExec{}}
	ctx := context.Background()
	if out, err := svc.Exec.Run(ctx, repo, "git", "add", "-A"); err != nil {
		t.Fatalf("git add -A: %v (%s)", err, out)
	}
	changed, total, err := svc.changedPaths(ctx, repo)
	if err != nil {
		return err
	}
	if err := checkPathScope(changed, agentRel); err != nil {
		return err
	}
	if total > maxChangedLines {
		return &gateErr{gate: "changed lines", msg: "over cap"}
	}
	return nil
}

// assertRejected fails unless err is a *gateErr naming the wanted gate.
func assertRejected(t *testing.T, err error, wantGate string) {
	t.Helper()
	if err == nil {
		t.Fatalf("gate passed; want rejection with gate %q", wantGate)
	}
	if !strings.Contains(err.Error(), wantGate) {
		t.Fatalf("rejection = %q, want gate %q named", err.Error(), wantGate)
	}
}

// TestRealGitUntrackedCreateRejected is the direct P0 regression: the model edits
// the agent file AND creates .github/workflows/evil.yml. The create is untracked
// after apply; `git diff HEAD` would omit it (the bypass). Staging first makes it
// a staged add row the scope gate catches → rejected with gate "path scope".
func TestRealGitUntrackedCreateRejected(t *testing.T) {
	repo, agentRel := initAgentRepo(t)

	// (a) edit the target agent file.
	if err := os.WriteFile(filepath.Join(repo, agentRel),
		[]byte("---\nname: tech-lead\ndescription: orchestrator\n---\nbody edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// (b) create an out-of-scope CI workflow (untracked at gate time).
	if err := os.MkdirAll(filepath.Join(repo, ".github/workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github/workflows/evil.yml"),
		[]byte("on: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sanity: the OLD data source (tracked-only) would MISS the create.
	old, err := OSExec{}.Run(context.Background(), repo, "git", "diff", "--numstat", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(old, "evil.yml") {
		t.Fatalf("precondition: untracked create should be invisible to tracked-only diff, got:\n%s", old)
	}

	assertRejected(t, stageAndGate(t, repo, agentRel), "path scope")

	// Nothing was committed — HEAD still points at the base commit only.
	logOut, _ := OSExec{}.Run(context.Background(), repo, "git", "log", "--oneline")
	if strings.Count(strings.TrimSpace(logOut), "\n") != 0 {
		t.Fatalf("expected a single base commit, got:\n%s", logOut)
	}
}

// TestRealGitRenameAwayRejected: the model renames the agent file to an
// out-of-scope path. --no-renames splits it into a delete of the agent + an add
// of the destination, so the destination is its own entry the scope gate catches.
func TestRealGitRenameAwayRejected(t *testing.T) {
	repo, agentRel := initAgentRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := (OSExec{}).Run(context.Background(), repo, "git", "mv",
		agentRel, "scripts/evil.sh"); err != nil {
		t.Fatalf("git mv: %v (%s)", err, out)
	}
	assertRejected(t, stageAndGate(t, repo, agentRel), "path scope")
}

// TestRealGitLargeInjectedFileRejected proves the ≤120-line cap now counts a
// large INJECTED new file. The agent edit itself is tiny; a 500-line created file
// pushes total over the cap. Because it also violates path scope, we assert on
// the scope gate (which fires first) — either way it is rejected before commit.
func TestRealGitLargeInjectedFileRejected(t *testing.T) {
	repo, agentRel := initAgentRepo(t)
	// tiny edit to the in-scope agent file.
	if err := os.WriteFile(filepath.Join(repo, agentRel),
		[]byte("---\nname: tech-lead\ndescription: orchestrator\n---\nbody v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 500-line injected file (untracked create) — evades the tracked-only cap.
	big := strings.Repeat("junk line\n", 500)
	if err := os.WriteFile(filepath.Join(repo, "scripts.sh"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	err := stageAndGate(t, repo, agentRel)
	if err == nil {
		t.Fatal("gate passed; a 500-line injected file must be rejected")
	}
	// The staged numstat must count the injected 500 lines (proving the cap now
	// sees them); scope gate fires first, so accept either gate name.
	if !strings.Contains(err.Error(), "path scope") && !strings.Contains(err.Error(), "changed lines") {
		t.Fatalf("rejection = %q, want path scope or changed lines", err.Error())
	}
	// Independent proof the cap counts it: with the injected file the ONLY
	// out-of-scope change, remove scope ambiguity by checking the staged total.
	total := stagedTotal(t, repo)
	if total < 500 {
		t.Fatalf("staged total = %d, want ≥500 (injected file counted)", total)
	}
}

// stagedTotal returns the added+deleted line count from the staged numstat gate.
func stagedTotal(t *testing.T, repo string) int {
	t.Helper()
	svc := &Service{Repo: repo, Exec: OSExec{}}
	_, total, err := svc.changedPaths(context.Background(), repo)
	if err != nil {
		t.Fatalf("changedPaths: %v", err)
	}
	return total
}

// TestRealGitInScopeEditPasses is the positive control: an edit confined to the
// target agent file passes both the scope gate and the line cap against real git.
func TestRealGitInScopeEditPasses(t *testing.T) {
	repo, agentRel := initAgentRepo(t)
	if err := os.WriteFile(filepath.Join(repo, agentRel),
		[]byte("---\nname: tech-lead\ndescription: orchestrator v2\n---\nbody edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stageAndGate(t, repo, agentRel); err != nil {
		t.Fatalf("in-scope edit must pass the gate, got: %v", err)
	}
}
