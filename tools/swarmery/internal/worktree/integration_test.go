package worktree

import (
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
