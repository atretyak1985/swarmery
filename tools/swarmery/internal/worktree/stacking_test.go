package worktree

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stacking: a run that starts on an unmerged dependency branch instead of on the
// repo's current branch tip (AcquireAt), and the reclaim that has to tell that
// dependency's commits apart from the run's own (ReclaimEmptyBranchAt).
//
// The real-git tests run in TEMP repositories only. Every claim they make is one a
// scripted stub could only assert by assumption: that `worktree add -b` really does
// pin to the SHA it is handed, that a branch stacked on another really does read as
// "ahead of main", and that `rev-list` really does exclude two tips at once.

// stackRepo is a temp repository on `main` with one commit, plus the helpers the
// stacking tests share.
type stackRepo struct {
	t    *testing.T
	dir  string
	git  ExecGit
	mgr  *Manager
	seq  int
	main string // SHA of the initial commit
}

func newStackRepo(t *testing.T) *stackRepo {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := &stackRepo{t: t, dir: t.TempDir(), git: ExecGit{}}
	r.run("init", "-q", "-b", "main")
	r.run("config", "user.email", "test@example.com")
	r.run("config", "user.name", "Test")
	r.run("config", "commit.gpgsign", "false")
	mustWrite(t, filepath.Join(r.dir, "README.md"), "hello\n")
	r.run("add", "README.md")
	r.run("commit", "-q", "-m", "init")
	r.main = r.tip("main")
	r.mgr = &Manager{Git: r.git, Root: filepath.Join(t.TempDir(), "wts")}
	return r
}

func (r *stackRepo) run(args ...string) string {
	r.t.Helper()
	return r.runIn(r.dir, args...)
}

func (r *stackRepo) runIn(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.git.Run(dir, args...)
	if err != nil {
		r.t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return out
}

func (r *stackRepo) tip(ref string) string {
	r.t.Helper()
	return strings.TrimSpace(r.run("rev-parse", ref))
}

// commitIn adds one new file and commits it in dir (the repo or a worktree),
// returning the new HEAD.
func (r *stackRepo) commitIn(dir, msg string) string {
	r.t.Helper()
	r.seq++
	name := "file-" + string(rune('a'+r.seq)) + ".txt"
	mustWrite(r.t, filepath.Join(dir, name), msg+"\n")
	r.runIn(dir, "add", name)
	r.runIn(dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(r.runIn(dir, "rev-parse", "HEAD"))
}

// depBranch creates `name` off main carrying n commits of its own, WITHOUT leaving
// the repo checked out on it, and returns its tip.
func (r *stackRepo) depBranch(name string, n int) string {
	r.t.Helper()
	r.run("checkout", "-q", "-b", name, "main")
	for i := 0; i < n; i++ {
		r.commitIn(r.dir, name+" work")
	}
	tip := r.tip("HEAD")
	r.run("checkout", "-q", "main")
	return tip
}

// ---- AcquireAt -------------------------------------------------------------

// TestAcquireAtPinsToTheExplicitRef: the worktree starts on the dependency's tip,
// contains its commit, and reports that tip — not main's — as its start point.
func TestAcquireAtPinsToTheExplicitRef(t *testing.T) {
	r := newStackRepo(t)
	depTip := r.depBranch("swarm/phase-1", 1)

	a, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", "refs/heads/swarm/phase-1")
	if err != nil {
		t.Fatalf("AcquireAt: %v", err)
	}
	if a.StartPoint != depTip {
		t.Errorf("StartPoint = %s, want the dependency tip %s", a.StartPoint, depTip)
	}
	if a.StartPoint == r.main {
		t.Error("StartPoint is main's tip — the explicit start ref was ignored")
	}
	if a.Branch != "swarm/phase-2" {
		t.Errorf("Branch = %q, want swarm/phase-2", a.Branch)
	}
	if head := strings.TrimSpace(r.runIn(a.Path, "rev-parse", "HEAD")); head != depTip {
		t.Errorf("worktree HEAD = %s, want the dependency tip %s", head, depTip)
	}
	// The dependency's commit is IN the tree the run starts on.
	if out, err := r.git.Run(a.Path, "merge-base", "--is-ancestor", depTip, "HEAD"); err != nil {
		t.Errorf("the dependency commit is not an ancestor of the worktree HEAD: %v\n%s", err, out)
	}
}

// TestAcquireAtAcceptsASHA: the start ref a caller resolved itself is a SHA, and
// that is the form phaserun passes (no window between resolving and acquiring).
func TestAcquireAtAcceptsASHA(t *testing.T) {
	r := newStackRepo(t)
	depTip := r.depBranch("swarm/phase-1", 2)

	a, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", depTip)
	if err != nil {
		t.Fatalf("AcquireAt(sha): %v", err)
	}
	if a.StartPoint != depTip {
		t.Errorf("StartPoint = %s, want %s", a.StartPoint, depTip)
	}
}

// TestAcquireAtEmptyRefIsAcquire: "" is Acquire, byte for byte — the same start
// point and the same git calls in the same order, which is what keeps dispatch
// and planrun behaving exactly as before.
func TestAcquireAtEmptyRefIsAcquire(t *testing.T) {
	plain, at := baseStub(), baseStub()
	a1, err := newMgr(t, plain).Acquire("/tmp/repo", "proj", "T-x")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	a2, err := newMgr(t, at).AcquireAt("/tmp/repo", "proj", "T-x", "")
	if err != nil {
		t.Fatalf("AcquireAt: %v", err)
	}
	if a1.StartPoint != a2.StartPoint || a1.Branch != a2.Branch {
		t.Errorf("Acquire = %+v, AcquireAt(\"\") = %+v — they must agree", a1, a2)
	}
	if len(plain.calls) != len(at.calls) {
		t.Fatalf("git calls differ:\n  Acquire   %v\n  AcquireAt %v", plain.calls, at.calls)
	}
	for i := range plain.calls {
		// The worktree path embeds each manager's own temp Root, so compare the verb.
		if strings.Fields(plain.calls[i])[0] != strings.Fields(at.calls[i])[0] {
			t.Errorf("call[%d]: Acquire ran %q, AcquireAt ran %q", i, plain.calls[i], at.calls[i])
		}
	}
	for _, c := range at.calls {
		if strings.Contains(c, "^{commit}") || strings.HasPrefix(c, "merge-base") {
			t.Errorf("AcquireAt(\"\") ran %q — the stacking probes must not run without a start ref", c)
		}
	}
}

// TestAcquireAtRefusesAnUnresolvableRef: a start ref that names nothing is
// refused. Falling back to the default tip would start the run on a tree without
// the dependency's work — the failure an explicit ref exists to prevent.
func TestAcquireAtRefusesAnUnresolvableRef(t *testing.T) {
	r := newStackRepo(t)
	_, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", "refs/heads/swarm/phase-gone")
	if !errors.Is(err, ErrStartRefUnresolved) {
		t.Fatalf("err = %v, want ErrStartRefUnresolved", err)
	}
	// Nothing was created: no worktree, and no branch minted for a refused run.
	if out := strings.TrimSpace(r.run("for-each-ref", "--format=%(refname:short)", "refs/heads/swarm/")); out != "" {
		t.Errorf("swarm refs after a refused AcquireAt = %q, want none", out)
	}
}

// TestAcquireAtRefusesAnOptionShapedRef: the ref reaches an argv slot, so a value
// that would parse as an option never gets to git.
func TestAcquireAtRefusesAnOptionShapedRef(t *testing.T) {
	g := baseStub()
	m := newMgr(t, g)
	for _, ref := range []string{"--upload-pack=x", "-D"} {
		if _, err := m.AcquireAt("/tmp/repo", "proj", "T-x", ref); !errors.Is(err, ErrStartRefUnresolved) {
			t.Errorf("AcquireAt(%q) = %v, want ErrStartRefUnresolved", ref, err)
		}
	}
	for _, c := range g.calls {
		if strings.Contains(c, "upload-pack") || strings.HasPrefix(c, "worktree add") {
			t.Errorf("git call %q issued for a refused start ref", c)
		}
	}
}

// TestAcquireAtNotARepoStillFailsAsNotARepo: the repo probe runs before the ref is
// resolved, so a non-repo keeps its own sentinel.
func TestAcquireAtNotARepoStillFailsAsNotARepo(t *testing.T) {
	g := &stubGit{}
	g.on("symbolic-ref --short HEAD", "", errors.New("fatal: not a git repository"))
	g.on("rev-parse HEAD", "", errors.New("fatal: not a git repository"))
	if _, err := newMgr(t, g).AcquireAt("/not/a/repo", "proj", "T-x", "abc123"); !errors.Is(err, ErrNotARepo) {
		t.Fatalf("err = %v, want ErrNotARepo", err)
	}
}

// TestAcquireAtWarmReuseReportsTheForkPoint: a crashed stacked run left its
// worktree behind with a commit of its own, and the dependency branch has moved on
// since. The retry warm-reuses the worktree (invariant 4) — and must report where
// that branch ACTUALLY forks from the dependency, not the dependency's new tip,
// which the reused branch does not contain. With the new tip as the base, the
// verifier's base...HEAD would be measured against a commit the run never saw.
func TestAcquireAtWarmReuseReportsTheForkPoint(t *testing.T) {
	r := newStackRepo(t)
	oldDepTip := r.depBranch("swarm/phase-1", 1)

	first, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", oldDepTip)
	if err != nil {
		t.Fatalf("first AcquireAt: %v", err)
	}
	r.commitIn(first.Path, "phase 2 partial work")
	// …the daemon dies here: no Remove. Meanwhile the dependency gains a commit.
	r.run("checkout", "-q", "swarm/phase-1")
	newDepTip := r.commitIn(r.dir, "phase 1 follow-up")
	r.run("checkout", "-q", "main")
	if newDepTip == oldDepTip {
		t.Fatal("setup: the dependency branch did not move")
	}

	retry, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", newDepTip)
	if err != nil {
		t.Fatalf("retry AcquireAt = %v, want warm reuse", err)
	}
	if !samePath(retry.Path, first.Path) {
		t.Fatalf("retry path = %s, want the reused %s", retry.Path, first.Path)
	}
	if retry.StartPoint != oldDepTip {
		t.Errorf("warm-reuse StartPoint = %s, want the fork point %s (the freshly resolved tip is %s)",
			retry.StartPoint, oldDepTip, newDepTip)
	}
	// The base is honest: exactly this run's one commit sits after it.
	if n := strings.TrimSpace(r.runIn(retry.Path, "rev-list", "--count", retry.StartPoint+"..HEAD")); n != "1" {
		t.Errorf("commits after the reported start point = %s, want 1", n)
	}
}

// TestAcquireAtWarmReuseSurfacesAMergeBaseFailure: a start point the reused branch
// shares no history with is an error, never a guessed base.
func TestAcquireAtWarmReuseSurfacesAMergeBaseFailure(t *testing.T) {
	g := baseStub()
	m := newMgr(t, g)
	path := filepath.Join(m.Root, "proj", "phase-2")
	g.on("worktree list --porcelain", "worktree "+path+"\nbranch refs/heads/swarm/phase-2\n\n", nil)
	g.on("rev-parse --verify --quiet", "bbbb2222\n", nil)
	g.on("merge-base", "", errors.New("exit 1"))
	if _, err := m.AcquireAt("/tmp/repo", "proj", "phase-2", "bbbb2222"); err == nil ||
		!strings.Contains(err.Error(), "merge base") {
		t.Fatalf("err = %v, want the merge-base failure surfaced", err)
	}
	// …and an empty answer is refused the same way.
	g.on("merge-base", "\n", nil)
	if _, err := m.AcquireAt("/tmp/repo", "proj", "phase-2", "bbbb2222"); err == nil ||
		!strings.Contains(err.Error(), "share no history") {
		t.Fatalf("err = %v, want the empty merge base refused", err)
	}
	if g.called("worktree remove") {
		t.Error("a warm-reuse probe failure destroyed the worktree")
	}
}

// ---- ReclaimEmptyBranchAt ---------------------------------------------------

// TestReclaimEmptyBranchAtStackedEmptyBranchIsReclaimable is the reason the
// function exists. A run stacked on an unmerged dependency made no commits of its
// own; its branch is therefore the dependency's tip. Measured against main alone
// that is "1 commit ahead" and the retry is refused as a dirty branch. Measured
// with its recorded start point it is empty, and the name is reclaimed.
func TestReclaimEmptyBranchAtStackedEmptyBranchIsReclaimable(t *testing.T) {
	r := newStackRepo(t)
	depTip := r.depBranch("swarm/phase-1", 1)

	acq, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", depTip)
	if err != nil {
		t.Fatalf("AcquireAt: %v", err)
	}
	if err := r.mgr.Remove(r.dir, acq, true /* keepBranch */); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The premise: without the start point this branch reads as holding work.
	ahead, err := r.mgr.ReclaimEmptyBranch(r.dir, acq.Branch)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch: %v", err)
	}
	if ahead != 1 {
		t.Fatalf("ReclaimEmptyBranch ahead = %d, want 1 — the premise of this test is that a stacked "+
			"empty branch LOOKS dirty against main", ahead)
	}

	ahead, err = r.mgr.ReclaimEmptyBranchAt(r.dir, acq.Branch, acq.StartPoint)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	if ahead != 0 {
		t.Fatalf("ReclaimEmptyBranchAt ahead = %d, want 0 — the branch holds none of its own commits", ahead)
	}
	if exists, _ := r.mgr.branchExists(r.dir, acq.Branch); exists {
		t.Error("the empty stacked branch was not reclaimed")
	}
	// The dependency's branch — the one that really holds those commits — is untouched.
	if got := r.tip("refs/heads/swarm/phase-1"); got != depTip {
		t.Errorf("dependency branch = %s, want it intact at %s", got, depTip)
	}
}

// TestReclaimEmptyBranchAtKeepsAStackedBranchWithItsOwnWork: the start point
// excludes the dependency's commits and nothing else.
func TestReclaimEmptyBranchAtKeepsAStackedBranchWithItsOwnWork(t *testing.T) {
	r := newStackRepo(t)
	depTip := r.depBranch("swarm/phase-1", 2)

	acq, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", depTip)
	if err != nil {
		t.Fatalf("AcquireAt: %v", err)
	}
	own := r.commitIn(acq.Path, "phase 2 work")
	if err := r.mgr.Remove(r.dir, acq, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	ahead, err := r.mgr.ReclaimEmptyBranchAt(r.dir, acq.Branch, acq.StartPoint)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	if ahead != 1 {
		t.Errorf("ahead = %d, want 1 — this run's own commit, not the dependency's two", ahead)
	}
	if got := r.tip("refs/heads/" + acq.Branch); got != own {
		t.Errorf("branch = %s, want the run's commit %s preserved", got, own)
	}
}

// TestReclaimEmptyBranchAtMergedWorkIsStillReclaimable guards the OTHER direction.
// A run pinned to main made a commit, and that commit has since merged into main.
// Counting `startPoint..branch` alone would report it again (it is not reachable
// from the old start point) and a finished, merged phase could never be re-run.
func TestReclaimEmptyBranchAtMergedWorkIsStillReclaimable(t *testing.T) {
	r := newStackRepo(t)
	acq, err := r.mgr.Acquire(r.dir, "proj", "phase-3")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	r.commitIn(acq.Path, "phase 3 work")
	if err := r.mgr.Remove(r.dir, acq, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	r.run("merge", "-q", "--ff-only", acq.Branch) // the work lands on main

	ahead, err := r.mgr.ReclaimEmptyBranchAt(r.dir, acq.Branch, acq.StartPoint)
	if err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	if ahead != 0 {
		t.Fatalf("ahead = %d, want 0 — the branch's commit is already on main", ahead)
	}
	if exists, _ := r.mgr.branchExists(r.dir, acq.Branch); exists {
		t.Error("a fully merged run branch was not reclaimed")
	}
}

// TestReclaimEmptyBranchAtEmptyRefIsReclaimEmptyBranch: "" runs exactly the probe
// sequence ReclaimEmptyBranch always ran.
func TestReclaimEmptyBranchAtEmptyRefIsReclaimEmptyBranch(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "0\n", nil)
	if _, err := newMgr(t, g).ReclaimEmptyBranchAt("/tmp/repo", "swarm/phase-714", ""); err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-714",
		"symbolic-ref --short HEAD",
		"worktree prune",
		"worktree list --porcelain",
		"symbolic-ref --short HEAD",
		"rev-parse refs/heads/main",
		"rev-list --count aaaa1111..refs/heads/swarm/phase-714",
		"branch -D swarm/phase-714",
	)
}

// TestReclaimEmptyBranchAtExcludesBothTips pins the exact count command: the
// branch, minus the base tip, minus the recorded start point — and every guard
// that precedes it still runs, in the same order.
func TestReclaimEmptyBranchAtExcludesBothTips(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet dddd4444^{commit}", "dddd4444\n", nil)
	g.on("rev-list --count", "0\n", nil)
	ahead, err := newMgr(t, g).ReclaimEmptyBranchAt("/tmp/repo", "swarm/phase-714", "dddd4444")
	if err != nil || ahead != 0 {
		t.Fatalf("ReclaimEmptyBranchAt = (%d, %v), want (0, nil)", ahead, err)
	}
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-714",
		"symbolic-ref --short HEAD",
		"worktree prune",
		"worktree list --porcelain",
		"symbolic-ref --short HEAD",
		"rev-parse refs/heads/main",
		"rev-parse --verify --quiet dddd4444^{commit}",
		"rev-list --count refs/heads/swarm/phase-714 ^aaaa1111 ^dddd4444",
		"branch -D swarm/phase-714",
	)
}

// TestReclaimEmptyBranchAtUnresolvableStartPointFallsBack: a recorded start point
// git can no longer resolve is dropped, leaving the base-branch count — the larger
// number, so the branch is kept rather than deleted on a guess.
func TestReclaimEmptyBranchAtUnresolvableStartPointFallsBack(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet gone^{commit}", "", errors.New("exit 1"))
	g.on("rev-list --count", "2\n", nil)
	ahead, err := newMgr(t, g).ReclaimEmptyBranchAt("/tmp/repo", "swarm/phase-714", "gone")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	if ahead != 2 {
		t.Errorf("ahead = %d, want the base-branch count 2", ahead)
	}
	if !g.called("rev-list --count aaaa1111..refs/heads/swarm/phase-714") {
		t.Errorf("git calls = %v, want the base-only count", g.calls)
	}
	if g.called("branch -D") {
		t.Error("a branch was deleted on a count taken without its start point")
	}
}

// TestReclaimEmptyBranchAtStartPointEqualToBase: a run pinned to the base tip adds
// no second exclusion — the plain range is the same set.
func TestReclaimEmptyBranchAtStartPointEqualToBase(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet aaaa1111^{commit}", "aaaa1111\n", nil)
	g.on("rev-list --count", "0\n", nil)
	if _, err := newMgr(t, g).ReclaimEmptyBranchAt("/tmp/repo", "swarm/phase-714", "aaaa1111"); err != nil {
		t.Fatalf("ReclaimEmptyBranchAt: %v", err)
	}
	if !g.called("rev-list --count aaaa1111..refs/heads/swarm/phase-714") {
		t.Errorf("git calls = %v, want the plain base range", g.calls)
	}
}

// TestReclaimEmptyBranchAtStillRefusesADetachedHead: an explicit start point does
// not relax "no base, no count, no delete".
func TestReclaimEmptyBranchAtStillRefusesADetachedHead(t *testing.T) {
	r := newStackRepo(t)
	depTip := r.depBranch("swarm/phase-1", 1)
	acq, err := r.mgr.AcquireAt(r.dir, "proj", "phase-2", depTip)
	if err != nil {
		t.Fatalf("AcquireAt: %v", err)
	}
	if err := r.mgr.Remove(r.dir, acq, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	r.run("checkout", "-q", "--detach", "main")

	ahead, err := r.mgr.ReclaimEmptyBranchAt(r.dir, acq.Branch, acq.StartPoint)
	if !errors.Is(err, ErrDetachedHead) {
		t.Errorf("ReclaimEmptyBranchAt on a detached HEAD = (%d, %v), want ErrDetachedHead", ahead, err)
	}
	if exists, _ := r.mgr.branchExists(r.dir, acq.Branch); !exists {
		t.Error("the branch was deleted despite the refusal")
	}
}

// TestReclaimEmptyBranchAtRefusesForeignNamespace: the namespace guard is the
// first thing either entry point runs.
func TestReclaimEmptyBranchAtRefusesForeignNamespace(t *testing.T) {
	g := baseStub()
	if _, err := newMgr(t, g).ReclaimEmptyBranchAt("/tmp/repo", "dev", "aaaa1111"); !errors.Is(err, ErrRefusedBranch) {
		t.Fatalf("err = %v, want ErrRefusedBranch", err)
	}
	if len(g.calls) != 0 {
		t.Errorf("git calls = %v, want none for a refused branch", g.calls)
	}
}
