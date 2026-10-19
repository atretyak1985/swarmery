package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshAddStub scripts a fresh `worktree add -b` that succeeds, with the branch
// ref absent before the add and present after it (the add minted it), and the
// post-add completeness probe answering with probeOut/probeErr.
func freshAddStub(probeOut string, probeErr error) *stubGit {
	g := baseStub()
	added := false
	g.onFn("worktree add", func([]string) (string, error) { added = true; return "", nil })
	g.onFn("rev-parse --verify --quiet", func([]string) (string, error) {
		if added {
			return "bbbb2222\n", nil
		}
		return "", errors.New("exit 1")
	})
	g.on("diff-index", probeOut, probeErr)
	return g
}

func callIndex(g *stubGit, substr string) int {
	for i, c := range g.calls {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

// TestAcquireFreshAddIncompleteRefusesAndRollsBack: a fresh add that exits 0 but
// leaves tracked files missing is torn down (worktree first, then the branch this
// call minted) and refused — never handed out.
func TestAcquireFreshAddIncompleteRefusesAndRollsBack(t *testing.T) {
	g := freshAddStub("go.mod\ncmd/x/main.go\n", nil)
	m := newMgr(t, g)
	_, err := m.Acquire("/tmp/repo", "proj", "T-fresh")
	if !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("Acquire = %v, want ErrIncompleteCheckout", err)
	}
	if !strings.Contains(err.Error(), "2 tracked file(s)") {
		t.Errorf("error %q does not name the missing count (2)", err)
	}
	rm, del := callIndex(g, "worktree remove --force"), callIndex(g, "branch -D swarm/T-fresh")
	if rm < 0 || del < 0 {
		t.Fatalf("refusal did not tear down: remove@%d branch-D@%d, calls=%v", rm, del, g.calls)
	}
	if rm > del {
		t.Errorf("branch -D ran before worktree remove (git refuses to delete a checked-out branch)")
	}
}

// TestAcquireFreshAddProbeErrorFailsClosed: a post-add probe git cannot answer is
// a refusal, not a pass.
func TestAcquireFreshAddProbeErrorFailsClosed(t *testing.T) {
	g := freshAddStub("fatal: bad object HEAD", errors.New("exit 128"))
	m := newMgr(t, g)
	if _, err := m.Acquire("/tmp/repo", "proj", "T-fresh"); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("Acquire with a failing post-add probe = %v, want ErrIncompleteCheckout", err)
	}
	if !g.called("worktree remove --force") || !g.called("branch -D swarm/T-fresh") {
		t.Errorf("fail-closed refusal did not tear down the fresh add: %v", g.calls)
	}
}

// TestAcquireExistingFreshAddRefusalKeepsBranch: an existing-branch add mints
// nothing, so its refusal removes the checkout but never deletes the branch (it
// may hold the only copy of a crashed run's commits).
func TestAcquireExistingFreshAddRefusalKeepsBranch(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet", "bbbb2222\n", nil)
	g.on("rev-parse refs/heads/swarm/T-keep", "bbbb2222\n", nil)
	g.on("diff-index", "go.mod\n", nil)
	m := newMgr(t, g)
	if _, err := m.AcquireExisting("/tmp/repo", "proj", "T-keep"); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("AcquireExisting = %v, want ErrIncompleteCheckout", err)
	}
	if g.called("branch -D") {
		t.Errorf("refusal deleted a branch this call did not mint: %v", g.calls)
	}
}

// TestRefusalKeepsUncommittedWorkIntegration: the review finding against the
// self-heal — a branch-matched tree with an executor's uncommitted edit and an
// untracked file, plus a missing tracked file, is refused and left exactly as
// found; nothing is force-removed.
func TestRefusalKeepsUncommittedWorkIntegration(t *testing.T) {
	const taskID = "phase-work"
	r := newPartialRepo(t, taskID)
	edited := filepath.Join(r.acq.Path, "internal/pkg/pkg.go")
	untracked := filepath.Join(r.acq.Path, "notes.txt")
	mustWrite(t, edited, "package pkg\n\n// uncommitted executor work\n")
	mustWrite(t, untracked, "untracked work\n")
	if err := os.Remove(filepath.Join(r.acq.Path, "go.mod")); err != nil {
		t.Fatal(err)
	}

	if _, err := r.m.Acquire(r.repo, "proj", taskID); !errors.Is(err, ErrIncompleteCheckout) {
		t.Fatalf("Acquire = %v, want ErrIncompleteCheckout", err)
	}
	if b, err := os.ReadFile(edited); err != nil || !strings.Contains(string(b), "uncommitted executor work") {
		t.Errorf("uncommitted edit lost after refusal: %q, %v", b, err)
	}
	if !fileExists(untracked) {
		t.Error("untracked file lost after refusal")
	}
}
