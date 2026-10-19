package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAcquireSelfHealsPartialCheckoutIntegration reproduces the daemon-side
// defect behind card T-tcs71j / #571: a `git worktree add` interrupted mid-
// checkout (checkoutTimeout, git.go) registers the branch and the worktree
// directory before every tracked file lands on disk, and the next Acquire for
// the same task found that registration branch-matched and warm-reused it
// as-is — handing an executor a worktree with tracked files missing.
//
// Actually killing `git` mid-checkout is flaky to engineer deterministically,
// so this simulates the resulting ON-DISK state directly: delete a handful of
// tracked files from a GOOD worktree without touching git's index. `git
// status --porcelain` on that worktree shows exactly the " D" signature the
// incident reported (222 tracked files, here a handful is enough).
func TestAcquireSelfHealsPartialCheckoutIntegration(t *testing.T) {
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
	for _, f := range []string{"README.md", "a.txt", "b.txt", "c.txt"} {
		mustWrite(t, filepath.Join(repo, f), "content of "+f+"\n")
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	m := &Manager{Git: git, Root: filepath.Join(t.TempDir(), "wts")}
	taskID := "phase-571"

	first, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	for _, f := range []string{"README.md", "a.txt", "b.txt", "c.txt"} {
		if !fileExists(filepath.Join(first.Path, f)) {
			t.Fatalf("setup: %s missing from the fresh worktree", f)
		}
	}

	// Simulate the interrupted-checkout failure mode: tracked files vanish from
	// disk, git's index is left untouched — the exact " D" porcelain signature.
	deleted := []string{"a.txt", "b.txt", "c.txt"}
	for _, f := range deleted {
		if err := os.Remove(filepath.Join(first.Path, f)); err != nil {
			t.Fatalf("simulate partial checkout: remove %s: %v", f, err)
		}
	}
	status := runIn(t, git, first.Path, "status", "--porcelain")
	if n := countWorktreeDeleted(status); n != len(deleted) {
		t.Fatalf("setup: git status after simulated kill =\n%s\nwant %d worktree-deleted entries, got %d", status, len(deleted), n)
	}

	// The next Acquire for the same task must self-heal rather than silently
	// warm-reuse the broken tree.
	retry, err := m.Acquire(repo, "proj", taskID)
	if err != nil {
		t.Fatalf("retry Acquire on a partial checkout = %v, want self-heal (no error)", err)
	}
	if !samePath(retry.Path, first.Path) || retry.Branch != first.Branch {
		t.Fatalf("retry = {%s,%s}, want the same {%s,%s}", retry.Path, retry.Branch, first.Path, first.Branch)
	}
	for _, f := range deleted {
		p := filepath.Join(retry.Path, f)
		if !fileExists(p) {
			t.Errorf("%s was not restored by self-heal", p)
		}
	}
	finalStatus := runIn(t, git, retry.Path, "status", "--porcelain")
	if n := countWorktreeDeleted(finalStatus); n != 0 {
		t.Errorf("worktree still shows %d deleted tracked files after self-heal:\n%s", n, finalStatus)
	}
}

// countWorktreeDeleted counts porcelain `status --porcelain` records whose
// WORKTREE-status column (the second character of each "XY path" line) is
// 'D' — mirrors deletedTrackedCount's own column selection against the
// non-`-z` output this test's assertions read.
func countWorktreeDeleted(porcelain string) int {
	n := 0
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) >= 2 && line[1] == 'D' {
			n++
		}
	}
	return n
}

// TestDeletedTrackedCountUnit pins deletedTrackedCount's parsing against a
// stub Git, independent of the real-git integration test above: only the
// WORKTREE-status column (porcelain byte index 1) counts, never the index
// column — an executor's own uncommitted `git rm` must not read as a broken
// checkout.
func TestDeletedTrackedCountUnit(t *testing.T) {
	g := &stubGit{}
	g.on("status --porcelain=v1 -z --untracked-files=no",
		" D a.txt\x00D  staged-delete.txt\x00 M edited.txt\x00?? new.txt\x00", nil)
	m := &Manager{Git: g}
	n, err := m.deletedTrackedCount("/tmp/some/worktree")
	if err != nil {
		t.Fatalf("deletedTrackedCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("deletedTrackedCount = %d, want 1 (only the worktree-column D entry counts)", n)
	}
}

// TestDeletedTrackedCountUnitClean pins the empty case: nothing deleted.
func TestDeletedTrackedCountUnitClean(t *testing.T) {
	g := &stubGit{}
	g.on("status --porcelain=v1 -z --untracked-files=no", "", nil)
	m := &Manager{Git: g}
	n, err := m.deletedTrackedCount("/tmp/some/worktree")
	if err != nil {
		t.Fatalf("deletedTrackedCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("deletedTrackedCount = %d, want 0", n)
	}
}

func runIn(t *testing.T, git Git, dir string, args ...string) string {
	t.Helper()
	out, err := git.Run(dir, args...)
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return out
}
