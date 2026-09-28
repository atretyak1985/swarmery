package wtjanitor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// stubGit answers BlobInGit from a fixed map and counts calls, so the tests can
// assert BOTH the verdict and that the classifier stopped at the first miss.
type stubGit struct {
	inGit map[string]bool
	err   error
	calls int
}

func (s *stubGit) BlobInGit(_, _, repoRelPath string) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.inGit[repoRelPath], nil
}

// old is a timestamp comfortably past any minIdle the tests use.
func old(now time.Time) time.Time { return now.Add(-2 * time.Hour) }

const minIdle = 30 * time.Minute

// removable is a worktree that would be swept if no veto fired — the baseline
// every veto test mutates exactly one field of.
func removable(now time.Time) Worktree {
	return Worktree{Path: "/wt/a", Branch: "worktree-agent-a", NewestMTime: old(now)}
}

func TestClassify_MainCheckoutIsNeverTouched(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	// Everything else says "remove me": only IsMain must save it.
	wt.IsMain = true
	wt.Dirty = nil
	got, err := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Verdict != VerdictSkip {
		t.Errorf("verdict = %q, want %q (main checkout)", got.Verdict, VerdictSkip)
	}
}

// An operator's `git worktree lock` is a veto of its own: the lock says "keep
// this", so the sweep must not get as far as a removal git would refuse.
func TestClassify_LockedWorktreeIsSkipped(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.Locked = true
	got, err := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Verdict != VerdictSkip || !strings.Contains(got.Reason, "locked") {
		t.Errorf("decision = %+v, want a skip naming the lock", got)
	}
}

func TestClassify_LiveWorktreeIsSkipped(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.Live = true
	got, _ := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if got.Verdict != VerdictSkip {
		t.Errorf("verdict = %q, want skip for a live worktree", got.Verdict)
	}
}

func TestClassify_FreshLockIsSkipped(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.LockFresh = true
	got, _ := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if got.Verdict != VerdictSkip {
		t.Errorf("verdict = %q, want skip while a git op is in flight", got.Verdict)
	}
}

func TestClassify_YoungerThanMinIdleIsSkipped(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.NewestMTime = now.Add(-5 * time.Minute)
	got, _ := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if got.Verdict != VerdictSkip {
		t.Errorf("verdict = %q, want skip below the idle floor", got.Verdict)
	}
}

func TestClassify_UnknownMTimeIsSkipped(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.NewestMTime = time.Time{}
	got, _ := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if got.Verdict != VerdictSkip {
		t.Errorf("verdict = %q, want skip when the mtime is unknown", got.Verdict)
	}
}

func TestClassify_OwnCommitsAreKept(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.HasOwnCommits = true
	got, _ := Classify("/repo", wt, &stubGit{}, now, minIdle)
	if got.Verdict != VerdictKeepUnmerged {
		t.Errorf("verdict = %q, want %q", got.Verdict, VerdictKeepUnmerged)
	}
}

// The swarm/plan-147 shape: unmerged commits AND content absent from git. Own
// commits must outrank the content check — the branch is work either way.
func TestClassify_OwnCommitsOutrankDirtyContent(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.HasOwnCommits = true
	wt.Dirty = []string{"a.txt"}
	g := &stubGit{inGit: map[string]bool{}} // a.txt is NOT in git
	got, _ := Classify("/repo", wt, g, now, minIdle)
	if got.Verdict != VerdictKeepUnmerged {
		t.Errorf("verdict = %q, want %q", got.Verdict, VerdictKeepUnmerged)
	}
	if g.calls != 0 {
		t.Errorf("BlobInGit called %d times; own commits must decide before any content check", g.calls)
	}
}

func TestClassify_CleanWorktreeIsRedundant(t *testing.T) {
	now := time.Now()
	got, _ := Classify("/repo", removable(now), &stubGit{}, now, minIdle)
	if got.Verdict != VerdictRedundant {
		t.Errorf("verdict = %q, want %q for a clean worktree", got.Verdict, VerdictRedundant)
	}
}

// The four real worktrees' happy case: dirty, but every byte already committed.
func TestClassify_AllDirtyPathsInGitIsRedundant(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.Dirty = []string{"a.txt", "b/c.md"}
	g := &stubGit{inGit: map[string]bool{"a.txt": true, "b/c.md": true}}
	got, _ := Classify("/repo", wt, g, now, minIdle)
	if got.Verdict != VerdictRedundant {
		t.Errorf("verdict = %q, want %q", got.Verdict, VerdictRedundant)
	}
	if g.calls != 2 {
		t.Errorf("BlobInGit calls = %d, want 2 (every path must be proven)", g.calls)
	}
}

func TestClassify_OnePathAbsentForcesSalvage(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.Dirty = []string{"missing.txt", "b.txt", "c.txt"}
	g := &stubGit{inGit: map[string]bool{"b.txt": true, "c.txt": true}}
	got, _ := Classify("/repo", wt, g, now, minIdle)
	if got.Verdict != VerdictSalvage {
		t.Fatalf("verdict = %q, want %q", got.Verdict, VerdictSalvage)
	}
	if !strings.Contains(got.Reason, "missing.txt") {
		t.Errorf("reason = %q, want it to name the offending path", got.Reason)
	}
	// Short-circuit: proving the rest buys nothing once salvage is forced.
	if g.calls != 1 {
		t.Errorf("BlobInGit calls = %d, want 1 (short-circuit on the first miss)", g.calls)
	}
}

func TestClassify_BlobCheckErrorPropagates(t *testing.T) {
	now := time.Now()
	wt := removable(now)
	wt.Dirty = []string{"a.txt"}
	boom := errors.New("git exploded")
	got, err := Classify("/repo", wt, &stubGit{err: boom}, now, minIdle)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
	if got != (Decision{}) {
		t.Errorf("decision = %+v, want the zero value on error", got)
	}
}
