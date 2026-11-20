package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAcquireIntegration exercises the full lifecycle against a REAL temp git
// repo: init → acquire (pinned worktree on swarm/<id>) → commit with the
// trailer inside the worktree → CommitsForTask finds it → remove → prune
// leaves the source repo clean. Skipped in -short (unit runs); it needs the
// `git` binary.
func TestAcquireIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	// Fresh repo with one commit on the default branch.
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	taskID := "T-int001"

	a, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if a.Branch != "swarm/"+taskID {
		t.Fatalf("branch = %q", a.Branch)
	}
	if !dirExists(a.Path) {
		t.Fatalf("worktree path %s not created", a.Path)
	}
	// The worktree must be pinned to the default-branch tip.
	tip := strings.TrimSpace(run("rev-parse", "refs/heads/main"))
	if a.StartPoint != tip {
		t.Fatalf("StartPoint = %q, want main tip %q", a.StartPoint, tip)
	}

	// Commit inside the worktree with the task trailer.
	wtGit := func(args ...string) string {
		t.Helper()
		out, err := git.Run(a.Path, args...)
		if err != nil {
			t.Fatalf("git -C worktree %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	mustWrite(t, filepath.Join(a.Path, "feature.txt"), "work\n")
	wtGit("add", "feature.txt")
	wtGit("commit", "-q", "-m", "add feature\n\n"+Trailer(taskID))

	// CommitsForTask (run against the repo root, --all) finds the trailer commit.
	shas, err := m.CommitsForTask(repo, taskID)
	if err != nil {
		t.Fatalf("CommitsForTask: %v", err)
	}
	if len(shas) != 1 {
		t.Fatalf("CommitsForTask = %v, want exactly 1", shas)
	}
	// A different task id finds nothing.
	if other, _ := m.CommitsForTask(repo, "T-nope"); len(other) != 0 {
		t.Errorf("CommitsForTask(other) = %v, want empty", other)
	}

	// Second Acquire of the SAME task while its worktree is live → warm reuse
	// as-is (Invariant 4), returning the same path/branch idempotently. Real
	// git reports canonicalized paths (macOS /var → /private/var); samePath
	// resolves symlinks so warm reuse is detected identically on every OS.
	// ErrBranchBusy is reserved for the branch checked out at a DIFFERENT path
	// (Invariant 3) — covered by TestAcquireBranchBusyElsewhere.
	a2, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("second Acquire (warm reuse) should succeed, got %v", err)
	}
	if !samePath(a2.Path, a.Path) || a2.Branch != a.Branch {
		t.Errorf("warm reuse mismatch: got {%s,%s}, want {%s,%s}", a2.Path, a2.Branch, a.Path, a.Branch)
	}

	// Remove (delete the branch) then prune → source repo clean.
	if err := m.Remove(repo, a, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if dirExists(a.Path) {
		t.Errorf("worktree dir %s survived Remove", a.Path)
	}
	if err := m.Prune(repo); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	status := strings.TrimSpace(run("status", "--porcelain"))
	if status != "" {
		t.Errorf("repo not clean after remove+prune:\n%s", status)
	}
	// The branch is gone.
	if _, err := git.Run(repo, "rev-parse", "--verify", "swarm/"+taskID); err == nil {
		t.Error("swarm branch survived Remove(keepBranch=false)")
	}
}

// TestCrashLeftoverRetryIntegration reproduces the daemon-crash path against a
// REAL git repo: runAndHandle's defer never fires, so the worktree stays
// REGISTERED at the run's own deterministic path, checked out on swarm/<taskID>,
// with `git worktree prune` unable to clear it (the directory still exists). The
// retry — phaserun.Start's ReclaimEmptyBranch followed by Acquire — must recover
// it. The guard added in d7dcb64 returned ErrBranchCheckedOut here and killed
// every retry after a restart.
func TestCrashLeftoverRetryIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	taskID := "phase-714"
	branch := "swarm/" + taskID

	first, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	// …the daemon dies here. No Remove, no prune-able registration.
	if _, err := git.Run(repo, "worktree", "prune"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	ahead, err := m.ReclaimEmptyBranch(repo, branch)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch on a crash leftover = %v, want nil", err)
	}
	if ahead != 0 {
		t.Fatalf("ahead = %d, want 0 — a self-owned checkout is not a dirty branch", ahead)
	}
	retry, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("retry Acquire = %v, want warm reuse of the leftover worktree", err)
	}
	if !samePath(retry.Path, first.Path) || retry.Branch != branch {
		t.Fatalf("retry = {%s,%s}, want {%s,%s}", retry.Path, retry.Branch, first.Path, branch)
	}

	// Same recovery when the crashed run had already COMMITTED: warm reuse
	// continues on top of the work, and reclaim must not report it as dirty (which
	// would make Start refuse with a BranchDirtyError the user cannot resolve).
	wtGit := func(args ...string) string {
		t.Helper()
		out, err := git.Run(retry.Path, args...)
		if err != nil {
			t.Fatalf("git -C worktree %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	mustWrite(t, filepath.Join(retry.Path, "work.txt"), "partial\n")
	wtGit("add", "work.txt")
	wtGit("commit", "-q", "-m", "partial work")

	ahead, err = m.ReclaimEmptyBranch(repo, branch)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch (leftover with commits) = %v, want nil", err)
	}
	if ahead != 0 {
		t.Fatalf("ahead = %d, want 0 — our own live checkout is never reclaimed or counted", ahead)
	}
	if _, err := m.Acquire(repo, "proj", taskID); err != nil {
		t.Fatalf("Acquire after a committing crash-leftover = %v, want warm reuse", err)
	}
	// The commit is still there — nothing was destroyed.
	if out, err := git.Run(repo, "rev-list", "--count", "main..refs/heads/"+branch); err != nil {
		t.Fatalf("rev-list: %v\n%s", err, out)
	} else if strings.TrimSpace(out) != "1" {
		t.Fatalf("commits on %s = %s, want the 1 commit preserved", branch, strings.TrimSpace(out))
	}

	// DeleteBranch, by contrast, must NOT pretend it succeeded while the branch is
	// checked out — git itself would refuse the `branch -D`.
	if _, err := m.DeleteBranch(repo, branch); !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("DeleteBranch on a live checkout = %v, want ErrBranchCheckedOut", err)
	}
}

// TestOrphanedPathRetryIntegration replays the 2026-07-30 incident against real git,
// because every link of that dead end was a real-git behaviour a stub can only
// assert by assumption: `worktree remove` refuses a path git does not track,
// `worktree add -b` mints the branch BEFORE it validates the target path, and git
// says "already exists" for both a taken branch name and an occupied path.
//
// The state to recover: an external prune dropped the registration, the directory
// survived with uncommitted work in it, and the run branch is still there.
func TestOrphanedPathRetryIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts"), now: fixedClock}
	taskID := "plan-70"
	branch := "swarm/" + taskID

	first, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// The incident, exactly: the directory is wiped and recreated by a process
	// outside the daemon, so `worktree prune` legitimately drops the registration —
	// and then files appear in it again (in the real case, one uncommitted test).
	if err := os.RemoveAll(first.Path); err != nil {
		t.Fatalf("simulate external wipe: %v", err)
	}
	run("worktree", "prune")
	mustMkdir(t, filepath.Join(first.Path, ".serena"))
	mustWrite(t, filepath.Join(first.Path, "uncommitted_test.go"), "package economics\n")

	// Nothing holds the branch and it carries no commits, so reclaim frees the name…
	ahead, err := m.ReclaimEmptyBranch(repo, branch)
	if err != nil || ahead != 0 {
		t.Fatalf("ReclaimEmptyBranch = (%d, %v), want (0, nil)", ahead, err)
	}
	// …and acquisition must now SUCCEED. Before the fix this returned
	// ErrBranchExists — advice that could not work, since the retry recreated that
	// branch itself and the actual blocker was the directory.
	retry, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("retry Acquire = %v, want the orphaned path freed and a fresh worktree", err)
	}
	if !samePath(retry.Path, first.Path) {
		t.Fatalf("retry path = %s, want the deterministic %s", retry.Path, first.Path)
	}
	if out, err := git.Run(retry.Path, "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
		t.Fatalf("probe retry worktree: %v\n%s", err, out)
	} else if strings.TrimSpace(out) != branch {
		t.Fatalf("retry worktree is on %q, want %q", strings.TrimSpace(out), branch)
	}

	// The dead run's uncommitted work survived the recovery.
	got, err := os.ReadFile(filepath.Join(quarantineOf(t, first.Path), "uncommitted_test.go"))
	if err != nil || string(got) != "package economics\n" {
		t.Fatalf("quarantined file = %q (err %v), want the work preserved", got, err)
	}

	// No branch accretion: exactly one swarm/ ref, the live one.
	if out := run("for-each-ref", "--format=%(refname:short)", "refs/heads/swarm/"); strings.TrimSpace(out) != branch {
		t.Fatalf("swarm refs = %q, want only %q", strings.TrimSpace(out), branch)
	}

	// The opposite case must stay loud: a branch holding commits, with the path free,
	// is still ErrBranchExists — the operator's decision, never auto-destroyed.
	wtGit := func(args ...string) string {
		t.Helper()
		out, err := git.Run(retry.Path, args...)
		if err != nil {
			t.Fatalf("git -C worktree %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	mustWrite(t, filepath.Join(retry.Path, "work.txt"), "real work\n")
	wtGit("add", "work.txt")
	wtGit("commit", "-q", "-m", "work that must not be discarded")
	if err := m.Remove(repo, retry, true); err != nil {
		t.Fatalf("Remove(keepBranch): %v", err)
	}
	if _, err := m.Acquire(repo, "proj", taskID); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("Acquire over a branch with commits = %v, want ErrBranchExists", err)
	}
	if out := run("rev-list", "--count", "main..refs/heads/"+branch); strings.TrimSpace(out) != "1" {
		t.Fatalf("commits on %s = %s, want the 1 commit intact", branch, strings.TrimSpace(out))
	}
}

// TestBranchProbeDistinguishesMissingFromBrokenIntegration pins the I6 contract to
// real git behaviour: `rev-parse --verify --quiet` on an absent ref exits non-zero
// with NO output, while a repo git cannot read prints a fatal: diagnostic. The
// heuristic that separates "missing" from "git is unhappy" is only sound if that
// holds for the git binary actually installed.
func TestBranchProbeDistinguishesMissingFromBrokenIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if out, err := git.Run(repo, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	if out, err := git.Run(repo, "add", "README.md"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := git.Run(repo, "commit", "-q", "-m", "init"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}

	// Absent ref → (false, nil): nothing to do, not an error.
	exists, err := m.branchExists(repo, "swarm/phase-nope")
	if err != nil || exists {
		t.Fatalf("branchExists(absent) = (%v, %v), want (false, nil)", exists, err)
	}
	// Present ref → (true, nil).
	if out, err := git.Run(repo, "branch", "swarm/phase-1"); err != nil {
		t.Fatalf("branch: %v\n%s", err, out)
	}
	if exists, err := m.branchExists(repo, "swarm/phase-1"); err != nil || !exists {
		t.Fatalf("branchExists(present) = (%v, %v), want (true, nil)", exists, err)
	}
	// Not a repo at all → an ERROR, never a confident "missing".
	if _, err := m.branchExists(t.TempDir(), "swarm/phase-1"); err == nil {
		t.Fatal("branchExists in a non-repo = nil error, want the git failure surfaced")
	}
	// …so DeleteBranch there refuses instead of reporting a delete that never ran.
	if _, err := m.DeleteBranch(t.TempDir(), "swarm/phase-1"); err == nil {
		t.Fatal("DeleteBranch in a non-repo = nil, want an error")
	}
}

// TestAcquireCopiesUntrackedClaudeSettingsIntegration reproduces issue #192
// against a real git repo: a project onboarded into swarmery gets
// .claude/settings.json written but never committed (`git status` shows
// `??`), so a plain `git worktree add` — proven against real git here, not a
// stub — never materializes it in the fresh worktree, and a headless run
// spawned there loses enabledPlugins with no warning. Acquire must lend the
// untracked file from the source checkout.
func TestAcquireCopiesUntrackedClaudeSettingsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	// The exact repro: onboarding wrote .claude/settings.json (and
	// project.json) but they were never committed. `git status --porcelain`
	// shows them as untracked ("??"), same as the reporter's repro steps.
	mustMkdir(t, filepath.Join(repo, ".claude"))
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"),
		`{"enabledPlugins":{"core":"swarmery"}}`)
	mustWrite(t, filepath.Join(repo, ".claude", "settings.local.json"),
		`{"permissions":{"allow":["Bash(git *)"]}}`)
	if status := strings.TrimSpace(run("status", "--porcelain")); !strings.Contains(status, "??") {
		t.Fatalf("setup: expected untracked .claude files, git status was:\n%s", status)
	}

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	a, err := m.Acquire(repo, "proj", "phase-192")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(a.Path, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf(".claude/settings.json missing from the fresh worktree — issue #192 regressed: %v", err)
	}
	if string(got) != `{"enabledPlugins":{"core":"swarmery"}}` {
		t.Fatalf("worktree settings.json = %q, want the source repo's untracked content", got)
	}
	got, err = os.ReadFile(filepath.Join(a.Path, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf(".claude/settings.local.json missing from the fresh worktree: %v", err)
	}
	if string(got) != `{"permissions":{"allow":["Bash(git *)"]}}` {
		t.Fatalf("worktree settings.local.json = %q, want the source repo's content", got)
	}

	// The worktree itself must still show clean per git — the copied files are
	// plain filesystem writes outside git's own view of a linked worktree's
	// tracked content, not a git operation, so this also pins that the copy
	// does not confuse git about what the worktree checked out.
	if out, err := git.Run(a.Path, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || strings.TrimSpace(out) != "swarm/phase-192" {
		t.Fatalf("worktree HEAD = %q (err %v), want swarm/phase-192", strings.TrimSpace(out), err)
	}
}

// TestExecGitRealError confirms ExecGit surfaces a real git failure with output.
func TestExecGitRealError(t *testing.T) {
	if testing.Short() {
		t.Skip("needs git binary")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	_, err := ExecGit{}.Run(t.TempDir(), "rev-parse", "HEAD")
	if err == nil {
		t.Error("rev-parse HEAD in an empty dir should error")
	}
}

// TestReclaimRefusesDetachedHeadIntegration pins the one shape in which reclaim
// could destroy commits that exist nowhere else. With the repo on a DETACHED HEAD
// there is no base branch to measure against; resolving the base through the
// acquire-oriented fallback (rev-parse HEAD) answers with the detached SHA, and
// when that SHA IS the run branch's own tip the count comes back 0 — "empty" —
// so the branch is force-deleted and its commits become unreachable.
//
// Reclaim must refuse instead: no base, no count, no delete.
func TestReclaimRefusesDetachedHeadIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	// A run branch carrying one commit that lives on no other ref.
	branch := "swarm/phase-9"
	run("checkout", "-q", "-b", branch)
	mustWrite(t, filepath.Join(repo, "work.txt"), "real work\n")
	run("add", "work.txt")
	run("commit", "-q", "-m", "the only copy of this work")
	tip := strings.TrimSpace(run("rev-parse", "refs/heads/"+branch))

	// The repo is left detached at that very tip (a rebase/bisect/checkout --detach
	// the user walked away from).
	run("checkout", "-q", "--detach", branch)
	if _, err := git.Run(repo, "symbolic-ref", "--short", "HEAD"); err == nil {
		t.Fatal("setup: HEAD is not detached")
	}

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	ahead, err := m.ReclaimEmptyBranch(repo, branch)
	// Errorf, not Fatalf: the wrong error and the destroyed branch are two separate
	// facts, and the second one is the whole point of this test.
	if !errors.Is(err, ErrDetachedHead) {
		t.Errorf("ReclaimEmptyBranch on a detached HEAD = (%d, %v), want ErrDetachedHead", ahead, err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0 alongside the refusal", ahead)
	}
	// The commit must still be reachable from its branch.
	if out, err := git.Run(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		t.Fatalf("branch %s was DELETED — its commit %s is now unreachable", branch, tip)
	} else if strings.TrimSpace(out) != tip {
		t.Fatalf("%s = %s, want the preserved tip %s", branch, strings.TrimSpace(out), tip)
	}
}

// TestDiffFileCountUsesMergeBaseIntegration pins the three-dot range against real
// git. A two-dot range (base..HEAD) counts every file the BASE branch moved on since
// the work forked — files this work never touched — and would trip a scope bound on
// an innocent branch. Only real git can prove the two ranges differ.
func TestDiffFileCountUsesMergeBaseIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) string {
		t.Helper()
		out, err := git.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "base.txt"), "base\n")
	run("add", "base.txt")
	run("commit", "-q", "-m", "init")

	// Work branch touches ONE file.
	run("checkout", "-q", "-b", "work")
	mustWrite(t, filepath.Join(repo, "mine.txt"), "mine\n")
	run("add", "mine.txt")
	run("commit", "-q", "-m", "my one file")

	// Meanwhile main moves on by three files this work never saw.
	run("checkout", "-q", "main")
	for _, f := range []string{"other1.txt", "other2.txt", "other3.txt"} {
		mustWrite(t, filepath.Join(repo, f), "x\n")
		run("add", f)
	}
	run("commit", "-q", "-m", "unrelated churn on main")
	run("checkout", "-q", "work")

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	n, err := m.DiffFileCount(repo, "main")
	if err != nil {
		t.Fatalf("DiffFileCount: %v", err)
	}
	if n != 1 {
		t.Errorf("DiffFileCount = %d, want 1 — the work touched one file; a two-dot range "+
			"would report 4 by counting main's own churn", n)
	}

	// A blank base is refused rather than silently meaning "everything".
	if _, err := m.DiffFileCount(repo, "  "); err == nil {
		t.Error("DiffFileCount with an empty base = nil error, want a refusal: " +
			"an unmeasurable base must not be reported as a count")
	}
}

// TestBranchPresentIntegration: a run branch counts as present while it is a
// local branch OR a remote-tracking origin/<branch>, absent only when neither
// ref is there, and a repo git cannot read is an error, not "absent".
func TestBranchPresentIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git := ExecGit{}
	run := func(args ...string) {
		t.Helper()
		if out, err := git.Run(repo, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	mustWrite(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}

	check := func(what string, want bool) {
		t.Helper()
		got, err := m.BranchPresent(repo, "swarm/phase-1")
		if err != nil || got != want {
			t.Fatalf("%s: BranchPresent = %v, %v; want %v, nil", what, got, err, want)
		}
	}
	check("never created", false)
	run("branch", "swarm/phase-1")
	check("local branch", true)
	run("update-ref", "refs/remotes/origin/swarm/phase-1", "HEAD")
	run("branch", "-D", "swarm/phase-1")
	check("remote-tracking only", true)
	run("update-ref", "-d", "refs/remotes/origin/swarm/phase-1")
	check("both deleted", false)

	if _, err := m.BranchPresent(t.TempDir(), "swarm/phase-1"); err == nil {
		t.Fatal("BranchPresent in a non-repo: err = nil, want git's failure")
	}
}
