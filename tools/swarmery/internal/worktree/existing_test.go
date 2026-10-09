package worktree

import (
	"errors"
	"strings"
	"testing"
)

// AcquireExisting: continue an existing swarm/<taskID> branch as it stands — the
// path a returned phase run takes. Real git, temp repositories only.

// TestAcquireExistingChecksOutTheBranchAsItStands: the worktree is ON the branch
// (not detached), at its tip, with its commit in the tree; the ref is untouched;
// a commit made in the worktree advances the branch itself; and a second call
// warm-reuses the same worktree.
func TestAcquireExistingChecksOutTheBranchAsItStands(t *testing.T) {
	r := newStackRepo(t)
	tip := r.depBranch("swarm/phase-7", 2)

	a, err := r.mgr.AcquireExisting(r.dir, "proj", "phase-7")
	if err != nil {
		t.Fatalf("AcquireExisting: %v", err)
	}
	if a.Branch != "swarm/phase-7" || a.StartPoint != tip {
		t.Errorf("Acquired = %+v, want branch swarm/phase-7 at its tip %s", a, tip)
	}
	if got := strings.TrimSpace(r.runIn(a.Path, "symbolic-ref", "--short", "HEAD")); got != "swarm/phase-7" {
		t.Errorf("worktree HEAD is on %q, want the branch itself (not detached)", got)
	}
	if got := r.tip("refs/heads/swarm/phase-7"); got != tip {
		t.Errorf("branch moved to %s by the acquire, want %s untouched", got, tip)
	}

	next := r.commitIn(a.Path, "continued")
	if got := r.tip("refs/heads/swarm/phase-7"); got != next {
		t.Errorf("branch tip = %s after a commit in the worktree, want %s", got, next)
	}
	if n := strings.TrimSpace(r.run("rev-list", "--count", r.main+"..refs/heads/swarm/phase-7")); n != "3" {
		t.Errorf("commits on the branch past main = %s, want 3 (two earlier + one continued)", n)
	}

	again, err := r.mgr.AcquireExisting(r.dir, "proj", "phase-7")
	if err != nil {
		t.Fatalf("second AcquireExisting: %v", err)
	}
	if again.Path != a.Path || again.StartPoint != next {
		t.Errorf("warm reuse = %+v, want the same path %s at the branch tip %s", again, a.Path, next)
	}
}

// TestAcquireExistingRefusesAMissingBranch: nothing to continue ⇒ refused, and no
// branch is minted for it.
func TestAcquireExistingRefusesAMissingBranch(t *testing.T) {
	r := newStackRepo(t)

	_, err := r.mgr.AcquireExisting(r.dir, "proj", "phase-8")
	if !errors.Is(err, ErrStartRefUnresolved) {
		t.Fatalf("err = %v, want ErrStartRefUnresolved", err)
	}
	if out := strings.TrimSpace(r.run("branch", "--list", "swarm/phase-8")); out != "" {
		t.Errorf("a refused AcquireExisting left a branch behind: %q", out)
	}
}

// TestAcquireExistingRefusesABusyBranch: the branch is checked out in another
// worktree ⇒ ErrBranchBusy, exactly as AcquireAt answers it.
func TestAcquireExistingRefusesABusyBranch(t *testing.T) {
	r := newStackRepo(t)
	r.depBranch("swarm/phase-9", 1)
	other := t.TempDir() + "/elsewhere"
	r.run("worktree", "add", other, "swarm/phase-9")

	if _, err := r.mgr.AcquireExisting(r.dir, "proj", "phase-9"); !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("err = %v, want ErrBranchBusy", err)
	}
}
