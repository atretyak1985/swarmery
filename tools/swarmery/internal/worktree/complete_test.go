package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// partialRepo is a real repo shaped like the 2026-10-19 incident: a module root
// whose go.mod and cmd/ sit next to an internal/ tree, with one acquired worktree.
type partialRepo struct {
	repo string
	git  ExecGit
	m    *Manager
	acq  Acquired
}

func newPartialRepo(t *testing.T, taskID string) partialRepo {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := partialRepo{repo: t.TempDir(), git: ExecGit{}}
	r.run(t, r.repo, "init", "-q", "-b", "main")
	r.run(t, r.repo, "config", "user.email", "test@example.com")
	r.run(t, r.repo, "config", "user.name", "Test")
	for rel, body := range map[string]string{
		"go.mod":               "module example.com/x\n",
		"cmd/x/main.go":        "package main\n",
		"internal/pkg/pkg.go":  "package pkg\n",
		"web/embed.go":         "package web\n",
		"web/dist/.gitkeep":    "",
		"web/src/App.tsx":      "export {}\n",
		"zz-untouched/keep.md": "keep\n",
	} {
		p := filepath.Join(r.repo, rel)
		mustMkdir(t, filepath.Dir(p))
		mustWrite(t, p, body)
	}
	r.run(t, r.repo, "add", "-A")
	r.run(t, r.repo, "commit", "-q", "-m", "init")

	r.m = &Manager{Git: r.git, Root: filepath.Join(t.TempDir(), "wts")}
	acq, err := r.m.Acquire(r.repo, "proj", taskID)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	r.acq = acq
	return r
}

func (r partialRepo) run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := r.git.Run(dir, args...)
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return out
}

// interruptTeardown leaves the worktree in the state a `git worktree remove
// --force` killed part-way through leaves it: git deletes the working tree
// depth-first in readdir order BEFORE it deletes .git/worktrees/<id>, so a kill
// (the git timeout, a daemon restart) strands a checkout that is still
// registered on its branch, survives `git worktree prune`, and has lost some of
// its tracked files — the index still lists them, so `git status` shows ` D`.
// Killing a real remove at the right moment is timing-dependent; deleting the
// same files produces the identical end state deterministically.
func (r partialRepo) interruptTeardown(t *testing.T) {
	t.Helper()
	for _, rel := range []string{"go.mod", "cmd", "web/embed.go", "web/dist"} {
		if err := os.RemoveAll(filepath.Join(r.acq.Path, rel)); err != nil {
			t.Fatalf("remove %s: %v", rel, err)
		}
	}
	r.run(t, r.repo, "worktree", "prune")
}

// TestAcquireRefusesPartiallyTornDownLeftoverIntegration replays phase-38543: a
// checkout registered on its own run branch with 222 tracked files gone was
// warm-reused as-is and handed to an executor, whose build then failed for reasons
// that had nothing to do with the code. Every hand-out path must refuse it.
func TestAcquireRefusesPartiallyTornDownLeftoverIntegration(t *testing.T) {
	const taskID = "phase-38543"
	r := newPartialRepo(t, taskID)
	r.interruptTeardown(t)

	// phaserun.Start's own prelude: the self-owned checkout is not a dirty branch.
	if ahead, err := r.m.ReclaimEmptyBranch(r.repo, r.acq.Branch); err != nil || ahead != 0 {
		t.Fatalf("ReclaimEmptyBranch = (%d, %v), want (0, nil)", ahead, err)
	}

	if _, err := r.m.Acquire(r.repo, "proj", taskID); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("Acquire on a partially torn-down leftover = %v, want ErrIncompleteCheckout", err)
	} else if !strings.Contains(err.Error(), "4 tracked file(s)") {
		t.Errorf("error %q does not report the count of missing files (4)", err)
	}
	if _, err := r.m.AcquireAt(r.repo, "proj", taskID, "main"); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("AcquireAt on a partially torn-down leftover = %v, want ErrIncompleteCheckout", err)
	}
	if _, err := r.m.AcquireExisting(r.repo, "proj", taskID); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("AcquireExisting on a partially torn-down leftover = %v, want ErrIncompleteCheckout", err)
	}
	if _, err := r.m.ReattachPath(r.repo, r.acq.Path); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("ReattachPath on a partially torn-down leftover = %v, want ErrIncompleteCheckout", err)
	}

	// A refusal destroys nothing: the leftover and whatever survived in it stay.
	if !fileExists(filepath.Join(r.acq.Path, "internal/pkg/pkg.go")) {
		t.Error("the refused leftover was modified — a refusal must leave it as found")
	}
}

// TestAcquireRefusesIndexlessLeftoverIntegration: a `git worktree add` killed
// during its checkout leaves the registration but no index. `ls-files --deleted`
// reads the index, so it sees nothing missing; the probe must not.
func TestAcquireRefusesIndexlessLeftoverIntegration(t *testing.T) {
	const taskID = "phase-571"
	r := newPartialRepo(t, taskID)
	if err := os.Remove(filepath.Join(r.acq.Path, "go.mod")); err != nil {
		t.Fatal(err)
	}
	index := strings.TrimSpace(r.run(t, r.acq.Path, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	if err := os.Remove(index); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if _, err := r.m.Acquire(r.repo, "proj", taskID); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("Acquire on an index-less leftover = %v, want ErrIncompleteCheckout", err)
	}
}

// TestAcquireWarmReuseKeepsCommittedDeletionsIntegration: the probe compares
// against the checkout's own HEAD, not the run's start point, so deletions the
// executor COMMITTED are its work, not damage, and warm reuse still succeeds.
func TestAcquireWarmReuseKeepsCommittedDeletionsIntegration(t *testing.T) {
	const taskID = "phase-900"
	r := newPartialRepo(t, taskID)
	r.run(t, r.acq.Path, "rm", "-q", "-r", "cmd", "go.mod")
	r.run(t, r.acq.Path, "commit", "-q", "-m", "drop the binary")

	again, err := r.m.Acquire(r.repo, "proj", taskID)
	if err != nil {
		t.Fatalf("Acquire after committed deletions = %v, want warm reuse", err)
	}
	if !samePath(again.Path, r.acq.Path) {
		t.Fatalf("warm reuse path = %s, want %s", again.Path, r.acq.Path)
	}
	if _, err := r.m.ReattachPath(r.repo, r.acq.Path); err != nil {
		t.Fatalf("ReattachPath after committed deletions = %v, want idempotent success", err)
	}
}

// TestAcquireRefusesWhenCompletenessProbeFails: a probe git cannot answer is not
// proof of a complete checkout — warm reuse refuses rather than hands it out.
func TestAcquireRefusesWhenCompletenessProbeFails(t *testing.T) {
	g := baseStub()
	m := newMgr(t, g)
	path, err := m.Path("proj", "T-probe")
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, path)
	g.on("worktree list --porcelain", "worktree "+path+"\nHEAD aaaa1111\nbranch refs/heads/swarm/T-probe\n\n", nil)
	g.on("diff-index", "fatal: index file corrupt", errors.New("exit 128"))

	if _, err := m.Acquire("/tmp/repo", "proj", "T-probe"); err == nil {
		t.Fatal("Acquire handed out a warm worktree whose completeness probe failed")
	}
	if g.called("worktree remove") {
		t.Error("a failed probe removed the leftover — a refusal must leave it as found")
	}
}
