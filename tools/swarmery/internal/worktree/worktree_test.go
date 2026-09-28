package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubGit is a scripted Git: it records every invocation and returns a canned
// (output, error) per matched arg-prefix. The most-recently-added matching
// script wins, so tests can register a default and override a specific verb.
type stubGit struct {
	calls   []string
	scripts []scriptEntry
}

type scriptEntry struct {
	match  func(args []string) bool
	output string
	err    error
	// fn, when set, is called (after recording) to mutate stub state or return
	// a dynamic result — used to make "list" reflect a prior "add".
	fn func(args []string) (string, error)
}

func (g *stubGit) Run(dir string, args ...string) (string, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	for i := len(g.scripts) - 1; i >= 0; i-- {
		s := g.scripts[i]
		if s.match(args) {
			if s.fn != nil {
				return s.fn(args)
			}
			return s.output, s.err
		}
	}
	return "", nil // unscripted verbs succeed with empty output
}

// on registers a script matching when args starts with the given verb tokens.
func (g *stubGit) on(verb string, output string, err error) *stubGit {
	toks := strings.Fields(verb)
	g.scripts = append(g.scripts, scriptEntry{
		match:  func(args []string) bool { return hasPrefix(args, toks) },
		output: output, err: err,
	})
	return g
}

func (g *stubGit) onFn(verb string, fn func(args []string) (string, error)) *stubGit {
	toks := strings.Fields(verb)
	g.scripts = append(g.scripts, scriptEntry{
		match: func(args []string) bool { return hasPrefix(args, toks) },
		fn:    fn,
	})
	return g
}

func hasPrefix(args, toks []string) bool {
	if len(args) < len(toks) {
		return false
	}
	for i, t := range toks {
		if args[i] != t {
			return false
		}
	}
	return true
}

func (g *stubGit) called(substr string) bool {
	for _, c := range g.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// baseStub is a Git that answers the resolveStartPoint handshake (default
// branch "main" @ SHA1) and an empty worktree list — the happy prelude every
// Acquire runs before its decision logic.
func baseStub() *stubGit {
	g := &stubGit{}
	g.on("symbolic-ref --short HEAD", "main\n", nil)
	g.on("rev-parse refs/heads/main", "aaaa1111\n", nil)
	g.on("rev-parse HEAD", "aaaa1111\n", nil)
	g.on("worktree list --porcelain", "", nil)
	g.on("worktree prune", "", nil)
	return g
}

// newMgr builds a Manager rooted at a temp dir with the given stub.
func newMgr(t *testing.T, g Git) *Manager {
	t.Helper()
	return &Manager{Git: g, Root: filepath.Join(t.TempDir(), "wts")}
}

// ---- Invariant 1: explicit startPoint ------------------------------------

func TestAcquirePinsExplicitStartPoint(t *testing.T) {
	g := baseStub()
	m := newMgr(t, g)
	a, err := m.Acquire("/tmp/repo", "proj", "T-abc123")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if a.StartPoint != "aaaa1111" {
		t.Errorf("StartPoint = %q, want the default-branch tip aaaa1111", a.StartPoint)
	}
	if a.Branch != "swarm/T-abc123" {
		t.Errorf("Branch = %q, want swarm/T-abc123", a.Branch)
	}
	// The add MUST include the -b branch AND the explicit start SHA, never a
	// bare `worktree add <path>` (Fusion FNXC:WorktreeIsolation).
	var addCall string
	for _, c := range g.calls {
		if strings.HasPrefix(c, "worktree add") {
			addCall = c
		}
	}
	if addCall == "" {
		t.Fatal("no `worktree add` call recorded")
	}
	if !strings.Contains(addCall, "-b swarm/T-abc123") || !strings.HasSuffix(addCall, "aaaa1111") {
		t.Errorf("add call = %q, want `-b swarm/T-abc123 <path> aaaa1111`", addCall)
	}
}

// resolveStartPoint pins to the DEFAULT branch tip even when the checkout sits
// on a sibling branch during recovery.
func TestAcquirePinsDefaultBranchNotAmbientHead(t *testing.T) {
	g := &stubGit{}
	g.on("symbolic-ref --short HEAD", "recovery-branch\n", nil) // ambient HEAD elsewhere
	g.on("rev-parse refs/heads/recovery-branch", "bbbb2222\n", nil)
	// symbolic-ref returns recovery-branch, so we pin to refs/heads/recovery-branch.
	// (The default-branch resolution follows symbolic-ref; the invariant is that
	// we resolve a NAMED ref explicitly, never the raw checkout HEAD blob.)
	g.on("worktree list --porcelain", "", nil)
	m := newMgr(t, g)
	a, err := m.Acquire("/tmp/repo", "proj", "T-x")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if a.StartPoint != "bbbb2222" {
		t.Errorf("StartPoint = %q, want bbbb2222 (resolved named ref)", a.StartPoint)
	}
	if g.called("rev-parse HEAD") {
		t.Error("Acquire used raw `rev-parse HEAD` when a symbolic ref was available")
	}
}

// ---- Invariant 2: repo-root guard ----------------------------------------

func TestAcquireRefusesPathInsideRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	// Root maliciously set inside the repo → computed path nests in repoRoot.
	m := &Manager{Git: baseStub(), Root: filepath.Join(repoRoot, "wts")}
	_, err := m.Acquire(repoRoot, "proj", "T-x")
	if !errors.Is(err, ErrRepoRootRefused) {
		t.Fatalf("err = %v, want ErrRepoRootRefused", err)
	}
}

func TestGuardRepoRootDirections(t *testing.T) {
	// equal, path-inside-repo, repo-inside-path all refuse.
	cases := [][2]string{
		{"/a/b", "/a/b"},   // equal
		{"/a/b", "/a/b/c"}, // path inside repo
		{"/a/b/c", "/a/b"}, // repo inside path
	}
	for _, c := range cases {
		if err := guardRepoRoot(c[0], c[1]); !errors.Is(err, ErrRepoRootRefused) {
			t.Errorf("guardRepoRoot(%q,%q) = %v, want ErrRepoRootRefused", c[0], c[1], err)
		}
	}
	// Disjoint siblings are fine.
	if err := guardRepoRoot("/a/repo", "/a/worktrees/T-x"); err != nil {
		t.Errorf("guardRepoRoot(disjoint) = %v, want nil", err)
	}
}

// TestEvalOrCleanKeepsDeepestComponent guards against a regression where
// evalOrClean dropped the basename of the deepest non-existent directory when
// its parent resolved directly — collapsing e.g. "<real>/repo" to "<real>" and
// making the repo-root guard over-match. Reproduces the Linux-CI condition
// where repoRoot and the worktree root share a symlinked real parent.
func TestEvalOrCleanKeepsDeepestComponent(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}

	// Direct child of an existing (symlinked) dir must be preserved.
	if got, want := evalOrClean(filepath.Join(link, "repo")), filepath.Join(resolvedReal, "repo"); got != want {
		t.Errorf("evalOrClean(link/repo) = %q, want %q", got, want)
	}
	// Multi-level non-existent tail under a symlinked existing dir.
	if got, want := evalOrClean(filepath.Join(link, "wts", "proj", "T-x")), filepath.Join(resolvedReal, "wts", "proj", "T-x"); got != want {
		t.Errorf("evalOrClean(link/wts/proj/T-x) = %q, want %q", got, want)
	}
	// A sibling repoRoot under the same symlinked parent must NOT be seen as
	// containing the worktree path (the exact false-positive fixed here).
	repoRoot := filepath.Join(link, "repo")
	wtPath := filepath.Join(link, "wts", "proj", "T-x")
	if err := guardRepoRoot(repoRoot, wtPath); err != nil {
		t.Errorf("guardRepoRoot(sibling under symlink) = %v, want nil", err)
	}
}

// ---- Invariant 3: branch-exists conflict fails loudly --------------------

func TestAcquireBranchBusyElsewhere(t *testing.T) {
	g := baseStub()
	// The branch is checked out at a DIFFERENT path than ours.
	g.on("worktree list --porcelain",
		"worktree /some/other/place\nbranch refs/heads/swarm/T-busy\n\n", nil)
	m := newMgr(t, g)
	_, err := m.Acquire("/tmp/repo", "proj", "T-busy")
	if !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("err = %v, want ErrBranchBusy", err)
	}
	// No rename attempted, no destructive add.
	if g.called("worktree add") {
		t.Error("Acquire attempted an add despite a busy branch")
	}
}

func TestAcquireBranchExistsButNotCheckedOut(t *testing.T) {
	g := baseStub()
	// list is empty (branch not checked out), but `add` reports the branch exists.
	g.on("worktree add", "fatal: a branch named 'swarm/T-x' already exists", errors.New("exit 128"))
	m := newMgr(t, g)
	_, err := m.Acquire("/tmp/repo", "proj", "T-x")
	if !errors.Is(err, ErrBranchExists) {
		t.Fatalf("err = %v, want ErrBranchExists (branch exists on add)", err)
	}
	// The message must not send the reader looking for a worktree: the list probe
	// just proved none holds this branch.
	if strings.Contains(err.Error(), "another worktree") {
		t.Errorf("err = %q, must not claim another worktree holds the branch", err)
	}
}

// The two conflicts have different remedies — release a checkout vs merge-or-delete a
// leftover — so they must never satisfy each other's errors.Is. Collapsing them is the
// bug this pair of sentinels exists to prevent.
func TestBranchConflictSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrBranchExists, ErrBranchBusy) {
		t.Error("ErrBranchExists matches ErrBranchBusy — a mere name collision would read as a live conflict")
	}
	if errors.Is(ErrBranchBusy, ErrBranchExists) {
		t.Error("ErrBranchBusy matches ErrBranchExists — a live checkout would read as a deletable leftover")
	}
}

// The busy path must stay busy: a branch checked out elsewhere is not a leftover, and
// telling the operator to delete it would destroy a running task's checkout.
func TestAcquireBusyIsNotReportedAsExists(t *testing.T) {
	g := baseStub()
	g.on("worktree list --porcelain",
		"worktree /some/other/place\nbranch refs/heads/swarm/T-busy\n\n", nil)
	m := newMgr(t, g)
	_, err := m.Acquire("/tmp/repo", "proj", "T-busy")
	if errors.Is(err, ErrBranchExists) {
		t.Fatalf("err = %v, want ErrBranchBusy only", err)
	}
	if !strings.Contains(err.Error(), "/some/other/place") {
		t.Errorf("err = %q, want it to name the holding worktree", err)
	}
}

// ---- Invariant 4: reuse-or-reset -----------------------------------------

func TestAcquireWarmReuseBranchMatched(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-reuse")
	g := baseStub()
	// Our exact path is registered on our exact branch → reuse as-is.
	g.on("worktree list --porcelain",
		fmt.Sprintf("worktree %s\nbranch refs/heads/swarm/T-reuse\n\n", path), nil)
	m := &Manager{Git: g, Root: root}
	a, err := m.Acquire("/tmp/repo", "proj", "T-reuse")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if a.Path != path {
		t.Errorf("Path = %q, want %q", a.Path, path)
	}
	// Warm reuse: no remove, no add.
	if g.called("worktree remove") || g.called("worktree add") {
		t.Errorf("warm reuse must not remove/add; calls=%v", g.calls)
	}
}

func TestAcquireReclaimsForeignBranchAtPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-foreign")
	g := baseStub()
	// Our path is registered but on a FOREIGN branch → remove + recreate.
	g.on("worktree list --porcelain",
		fmt.Sprintf("worktree %s\nbranch refs/heads/some-other-branch\n\n", path), nil)
	m := &Manager{Git: g, Root: root}
	if _, err := m.Acquire("/tmp/repo", "proj", "T-foreign"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !g.called("worktree remove --force " + path) {
		t.Errorf("expected reclaim remove of %s; calls=%v", path, g.calls)
	}
	if !g.called("worktree add -b swarm/T-foreign") {
		t.Error("expected recreate add after reclaim")
	}
}

func TestAcquireTransientListFailureRetriesNeverDestroys(t *testing.T) {
	g := baseStub()
	// First list call errors, second succeeds (empty). Must retry, not destroy.
	var listCalls int
	g.onFn("worktree list --porcelain", func(args []string) (string, error) {
		listCalls++
		if listCalls == 1 {
			return "", errors.New("transient: could not lock")
		}
		return "", nil
	})
	m := newMgr(t, g)
	if _, err := m.Acquire("/tmp/repo", "proj", "T-x"); err != nil {
		t.Fatalf("Acquire after retry: %v", err)
	}
	if listCalls < 2 {
		t.Errorf("list retried %d times, want >= 2", listCalls)
	}
}

func TestAcquireTransientListFailsBothTimes(t *testing.T) {
	g := baseStub()
	g.on("worktree list --porcelain", "", errors.New("still locked"))
	m := newMgr(t, g)
	_, err := m.Acquire("/tmp/repo", "proj", "T-x")
	if err == nil || errors.Is(err, ErrBranchBusy) {
		t.Fatalf("err = %v, want a non-busy list error after two failures", err)
	}
	// Crucially, no destructive remove happened on a flaky probe.
	if g.called("worktree remove") {
		t.Error("Acquire removed a worktree on a transient list failure")
	}
}

// ---- Invariant 5: stale-lock sweep ---------------------------------------

func TestAcquireSweepsStaleLocks(t *testing.T) {
	repoRoot := t.TempDir()
	// Build a fake registered worktree dir with an OLD index.lock and a fresh one.
	wtDir := filepath.Join(repoRoot, ".git", "worktrees", "old")
	mustMkdir(t, wtDir)
	oldLock := filepath.Join(wtDir, "index.lock")
	mustWrite(t, oldLock, "lock")
	freshDir := filepath.Join(repoRoot, ".git", "worktrees", "fresh")
	mustMkdir(t, freshDir)
	freshLock := filepath.Join(freshDir, "index.lock")
	mustWrite(t, freshLock, "lock")

	g := baseStub()
	m := &Manager{
		Git:  g,
		Root: filepath.Join(t.TempDir(), "wts"),
		now:  func() time.Time { return time.Now() },
	}
	// Age the old lock past the threshold.
	ageFile(t, oldLock, time.Now().Add(-staleLockAge-time.Minute))

	if _, err := m.Acquire(repoRoot, "proj", "T-x"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if fileExists(oldLock) {
		t.Error("stale index.lock was not swept")
	}
	if !fileExists(freshLock) {
		t.Error("fresh index.lock must be preserved")
	}
}

// ---- Invariant 6: trailer format -----------------------------------------

func TestTrailerFormat(t *testing.T) {
	if got := Trailer("T-abc123"); got != "Swarm-Task-Id: T-abc123" {
		t.Errorf("Trailer = %q, want Swarm-Task-Id: T-abc123", got)
	}
}

func TestRegexpEscapeMetachars(t *testing.T) {
	// A hypothetical id with ERE metacharacters must be escaped so the grep
	// matches it literally (defensive — real ids are T-<base36>).
	got := regexpEscape("a.b+c*")
	if got != `a\.b\+c\*` {
		t.Errorf("regexpEscape = %q, want a\\.b\\+c\\*", got)
	}
	// Hyphen stays literal.
	if regexpEscape("T-x") != "T-x" {
		t.Errorf("regexpEscape(T-x) escaped a hyphen")
	}
}

func TestCommitsForTaskGrepsExactTrailer(t *testing.T) {
	g := &stubGit{}
	g.on("log", "deadbeef\ncafef00d\n", nil)
	m := &Manager{Git: g}
	shas, err := m.CommitsForTask("/tmp/repo", "T-abc123")
	if err != nil {
		t.Fatalf("CommitsForTask: %v", err)
	}
	if len(shas) != 2 || shas[0] != "deadbeef" || shas[1] != "cafef00d" {
		t.Fatalf("shas = %v", shas)
	}
	// The grep must reference the exact trailer line, anchored (^…$). Hyphens
	// are not ERE metacharacters, so they are not escaped.
	last := g.calls[len(g.calls)-1]
	if !strings.Contains(last, "--grep") || !strings.Contains(last, "^Swarm-Task-Id: T-abc123$") {
		t.Errorf("log call = %q, want anchored exact-trailer grep", last)
	}
}

// ---- Remove / Prune ------------------------------------------------------

func TestRemoveKeepBranch(t *testing.T) {
	g := &stubGit{}
	m := &Manager{Git: g}
	a := Acquired{Path: "/wt/T-x", Branch: "swarm/T-x"}
	if err := m.Remove("/tmp/repo", a, true); err != nil {
		t.Fatalf("Remove(keep): %v", err)
	}
	if !g.called("worktree remove --force /wt/T-x") {
		t.Error("Remove did not force-remove the worktree")
	}
	if g.called("branch -D") {
		t.Error("keepBranch=true must NOT delete the branch")
	}
}

func TestRemoveDeleteBranch(t *testing.T) {
	g := &stubGit{}
	m := &Manager{Git: g}
	a := Acquired{Path: "/wt/T-x", Branch: "swarm/T-x"}
	if err := m.Remove("/tmp/repo", a, false); err != nil {
		t.Fatalf("Remove(delete): %v", err)
	}
	if !g.called("branch -D swarm/T-x") {
		t.Error("keepBranch=false must delete the branch")
	}
}

// Remove is the third `git branch -D` door, and the only one that does not go
// through checkBranchReclaimable. Acquired is an exported struct any caller can
// construct with a hand-built Branch, so the namespace class is closed here too:
// the worktree still comes off (that part is unconditionally safe), but the
// branch delete is refused before the git call is issued.
func TestRemoveRefusesForeignNamespace(t *testing.T) {
	for _, branch := range []string{"dev", "main", "feature/x", "swarm/", "swarm/a/b"} {
		g := &stubGit{}
		m := &Manager{Git: g}
		a := Acquired{Path: "/wt/T-x", Branch: branch}
		err := m.Remove("/tmp/repo", a, false)
		if !errors.Is(err, ErrRefusedBranch) {
			t.Errorf("Remove(%q, keepBranch=false) err = %v, want ErrRefusedBranch", branch, err)
		}
		if !strings.Contains(err.Error(), branch) {
			t.Errorf("err = %v, want it to name the branch %q", err, branch)
		}
		if g.called("branch -D") {
			t.Errorf("Remove(%q) issued `branch -D` despite the refusal", branch)
		}
	}
}

// taskIDForBranch is the inverse that authorizes ownsWorktreePath's
// single-component reasoning (Base(p) == taskID && Dir(Dir(p)) == Root). A
// multi-component remainder fails BOTH of those silently, so the leftover would
// be read as a foreign checkout and the crash-leftover warm reuse would quietly
// stop working for it. Refusing "/" here makes the swarm/plan- reuse provably
// safe instead of incidentally safe.
func TestTaskIDForBranch(t *testing.T) {
	for _, tc := range []struct {
		branch string
		want   string
	}{
		{"swarm/phase-714", "phase-714"},
		{"swarm/plan-71", "plan-71"},
		{"swarm/T-abc123", "T-abc123"},
		// Multi-component remainders — the precondition being asserted.
		{"swarm/a/b", ""},
		{"swarm/phase-1/x", ""},
		{"swarm/plan-71/nested", ""},
		// Outside the namespace entirely.
		{"swarm/", ""},
		{"dev", ""},
		{"main", ""},
		{"feature/x", ""},
		{"swarmish/x", ""},
		{"", ""},
	} {
		if got := taskIDForBranch(tc.branch); got != tc.want {
			t.Errorf("taskIDForBranch(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

func TestPruneSweepsAndPrunes(t *testing.T) {
	repoRoot := t.TempDir()
	wtDir := filepath.Join(repoRoot, ".git", "worktrees", "gone")
	mustMkdir(t, wtDir)
	lock := filepath.Join(wtDir, "index.lock")
	mustWrite(t, lock, "x")
	ageFile(t, lock, time.Now().Add(-staleLockAge-time.Minute))

	g := &stubGit{}
	m := &Manager{Git: g, Root: filepath.Join(t.TempDir(), "wts")}
	if err := m.Prune(repoRoot); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if fileExists(lock) {
		t.Error("Prune did not sweep the stale lock")
	}
	if !g.called("worktree prune") {
		t.Error("Prune did not call `git worktree prune`")
	}
}

func TestPrunePropagatesGitError(t *testing.T) {
	g := &stubGit{}
	g.on("worktree prune", "boom", errors.New("exit 1"))
	m := &Manager{Git: g, Root: t.TempDir()}
	if err := m.Prune(t.TempDir()); err == nil {
		t.Error("Prune should propagate a git prune failure")
	}
}

func TestRemovePropagatesErrors(t *testing.T) {
	// worktree remove fails on a path git DOES track → a real failure: surfaced, and
	// the branch is not deleted while that checkout still holds it. The registration
	// is what makes the failure real, so the list probe must show it — a remove
	// failure on a path git tracks nothing for is recoverable, and
	// TestRemoveFinishesTeardownWhenGitTracksNothing pins that opposite case.
	g := &stubGit{}
	g.on("worktree remove", "cannot remove", errors.New("exit 1"))
	g.on("worktree list --porcelain", "worktree /wt/T-x\nbranch refs/heads/swarm/T-x\n", nil)
	m := &Manager{Git: g}
	if err := m.Remove("/tmp/repo", Acquired{Path: "/wt/T-x", Branch: "swarm/T-x"}, false); err == nil {
		t.Error("Remove should propagate a worktree-remove failure")
	}
	if g.called("branch -D") {
		t.Error("branch delete must not run after a failed remove")
	}
	// remove ok but branch -D fails → error surfaced.
	g2 := &stubGit{}
	g2.on("branch -D", "no such branch", errors.New("exit 1"))
	m2 := &Manager{Git: g2}
	if err := m2.Remove("/tmp/repo", Acquired{Path: "/wt/T-x", Branch: "swarm/T-x"}, false); err == nil {
		t.Error("Remove should propagate a branch-delete failure")
	}
}

// ---- orphaned worktree paths (the 2026-07-30 retry dead end) --------------
//
// The incident: an external `git worktree prune` dropped a run's registration while
// its DIRECTORY survived, non-empty. Teardown could not remove it (git refuses
// `worktree remove` on a path it does not track), so every later acquisition ran
// `git worktree add -b` against an occupied path — which mints the branch BEFORE it
// validates the path, then fails with `fatal: '<path>' already exists`. Both that
// message and the branch-collision one contain "already exists", so the failure was
// reported as ErrBranchExists: the operator kept deleting a branch the retry itself
// had just created, four times over, while the real blocker sat on disk untouched.

// fixedClock makes quarantine names deterministic.
func fixedClock() time.Time { return time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC) }

// quarantineOf returns the single .orphaned-* sibling of path, failing if the
// leftover was not moved aside exactly once.
func quarantineOf(t *testing.T, path string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir %s: %v", filepath.Dir(path), err)
	}
	var found []string
	for _, e := range entries {
		if strings.Contains(e.Name(), orphanSuffix) {
			found = append(found, filepath.Join(filepath.Dir(path), e.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("quarantined dirs = %v, want exactly 1", found)
	}
	return found[0]
}

func TestRemoveFinishesTeardownWhenGitTracksNothing(t *testing.T) {
	g := &stubGit{}
	g.on("worktree remove", "fatal: '/wt/T-x' is not a working tree", errors.New("exit 128"))
	m := &Manager{Git: g}
	if err := m.Remove("/tmp/repo", Acquired{Path: "/wt/T-x", Branch: "swarm/T-x"}, false); err != nil {
		t.Fatalf("Remove = %v, want nil — git had nothing to remove and nothing is left on disk", err)
	}
	if !g.called("branch -D swarm/T-x") {
		t.Error("teardown must still delete the branch it was asked to delete")
	}
}

func TestRemoveQuarantinesUntrackedLeftover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-x")
	mustMkdir(t, path)
	mustWrite(t, filepath.Join(path, "uncommitted.go"), "package x\n")

	g := &stubGit{}
	g.on("worktree remove", "fatal: not a working tree", errors.New("exit 128"))
	m := &Manager{Git: g, Root: root, now: fixedClock}
	if err := m.Remove("/tmp/repo", Acquired{Path: path, Branch: "swarm/T-x"}, true); err != nil {
		t.Fatalf("Remove = %v, want the leftover cleared", err)
	}
	if dirExists(path) {
		t.Fatal("the path is still occupied — the next Acquire would collide on it")
	}
	got, err := os.ReadFile(filepath.Join(quarantineOf(t, path), "uncommitted.go"))
	if err != nil {
		t.Fatalf("read quarantined file: %v", err)
	}
	if string(got) != "package x\n" {
		t.Errorf("quarantined content = %q, want the dead run's work preserved verbatim", got)
	}
}

func TestRemoveRefusesToClearAPathItDoesNotOwn(t *testing.T) {
	foreign := t.TempDir() // not <Root>/<slug>/<taskID>
	mustWrite(t, filepath.Join(foreign, "someone-elses.txt"), "keep me\n")

	g := &stubGit{}
	g.on("worktree remove", "fatal: not a working tree", errors.New("exit 128"))
	m := &Manager{Git: g, Root: filepath.Join(t.TempDir(), "wts")}
	if err := m.Remove("/tmp/repo", Acquired{Path: foreign, Branch: "swarm/T-x"}, false); !errors.Is(err, ErrPathOccupied) {
		t.Fatalf("Remove = %v, want ErrPathOccupied for a path outside Root", err)
	}
	if !fileExists(filepath.Join(foreign, "someone-elses.txt")) {
		t.Fatal("a foreign directory was touched — the namespace guard is what makes this safe")
	}
}

func TestAcquireFreesUntrackedNonEmptyLeftover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-x")
	mustMkdir(t, path)
	mustWrite(t, filepath.Join(path, "leftover.go"), "package x\n")

	g := baseStub()
	g.on("worktree remove", "fatal: '"+path+"' is not a working tree", errors.New("exit 128"))
	m := &Manager{Git: g, Root: root, now: fixedClock}
	if _, err := m.Acquire("/tmp/repo", "proj", "T-x"); err != nil {
		t.Fatalf("Acquire = %v, want the leftover freed and the worktree created", err)
	}
	if !g.called("worktree add -b swarm/T-x " + path) {
		t.Error("Acquire never reached `worktree add` — the path was not freed")
	}
	if got, err := os.ReadFile(filepath.Join(quarantineOf(t, path), "leftover.go")); err != nil || string(got) != "package x\n" {
		t.Errorf("quarantined content = %q (err %v), want it preserved", got, err)
	}
}

func TestAcquireDeletesEmptyLeftoverInsteadOfQuarantiningIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-x")
	mustMkdir(t, path)

	g := baseStub()
	g.on("worktree remove", "fatal: not a working tree", errors.New("exit 128"))
	m := &Manager{Git: g, Root: root, now: fixedClock}
	if _, err := m.Acquire("/tmp/repo", "proj", "T-x"); err != nil {
		t.Fatalf("Acquire = %v, want nil", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	// An empty leftover holds nothing to preserve; quarantining one per retry would
	// litter Root for ever.
	if len(entries) != 0 {
		t.Errorf("siblings after acquiring over an EMPTY leftover = %d, want 0", len(entries))
	}
}

func TestFreeOrphanPathSurvivesAQuarantineNameCollision(t *testing.T) {
	// Two frees inside the same second — the stamp alone is not unique, and the
	// earlier quarantine must not be overwritten (it holds work too).
	root := filepath.Join(t.TempDir(), "wts")
	path := filepath.Join(root, "proj", "T-x")
	mustMkdir(t, path)
	mustWrite(t, filepath.Join(path, "second.go"), "second\n")
	taken := path + orphanSuffix + fixedClock().Format("20060102-150405")
	mustMkdir(t, taken)
	mustWrite(t, filepath.Join(taken, "first.go"), "first\n")

	m := &Manager{Git: &stubGit{}, Root: root, now: fixedClock}
	if err := m.freeOrphanPath(path, "T-x"); err != nil {
		t.Fatalf("freeOrphanPath = %v, want a sibling name picked", err)
	}
	if got, err := os.ReadFile(filepath.Join(taken, "first.go")); err != nil || string(got) != "first\n" {
		t.Fatalf("earlier quarantine = %q (err %v), want it untouched", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(taken+"-2", "second.go")); err != nil || string(got) != "second\n" {
		t.Fatalf("second quarantine = %q (err %v), want it beside the first", got, err)
	}
}

func TestAcquireDiscriminatesGitsTwoAlreadyExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want error
	}{
		{"branch name taken", "fatal: a branch named 'swarm/T-x' already exists", ErrBranchExists},
		{"target path taken", "fatal: '/wts/proj/T-x' already exists", ErrPathOccupied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := baseStub()
			g.on("worktree add", tc.out, errors.New("exit 128"))
			m := newMgr(t, g)
			_, err := m.Acquire("/tmp/repo", "proj", "T-x")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Acquire = %v, want %v — the two blockers need different remedies", err, tc.want)
			}
		})
	}
}

func TestAcquireRollsBackTheBranchItsFailedAddMinted(t *testing.T) {
	g := baseStub()
	probes := 0
	g.onFn("rev-parse --verify --quiet refs/heads/swarm/T-x", func([]string) (string, error) {
		probes++
		if probes == 1 {
			return "", errors.New("exit 1") // absent before the add
		}
		return "cccc3333\n", nil // the failed add left it behind
	})
	g.on("worktree add", "fatal: '/wts/proj/T-x' already exists", errors.New("exit 128"))
	m := newMgr(t, g)
	if _, err := m.Acquire("/tmp/repo", "proj", "T-x"); !errors.Is(err, ErrPathOccupied) {
		t.Fatalf("Acquire = %v, want ErrPathOccupied", err)
	}
	if !g.called("branch -D swarm/T-x") {
		t.Error("a failed add left its freshly minted branch behind — one orphan per retry")
	}
}

func TestAcquireNeverRollsBackAPreexistingBranch(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet refs/heads/swarm/T-x", "cccc3333\n", nil)
	g.on("worktree add", "fatal: a branch named 'swarm/T-x' already exists", errors.New("exit 128"))
	m := newMgr(t, g)
	if _, err := m.Acquire("/tmp/repo", "proj", "T-x"); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("Acquire = %v, want ErrBranchExists", err)
	}
	if g.called("branch -D") {
		t.Error("a branch that predates the add may hold the only copy of a crashed run's commits")
	}
}

func TestBlockerSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrPathOccupied, ErrBranchExists) || errors.Is(ErrBranchExists, ErrPathOccupied) {
		t.Fatal("ErrPathOccupied and ErrBranchExists must not alias — the whole point is telling them apart")
	}
}

// ---- ReclaimEmptyBranch / DeleteBranch -----------------------------------

// wantCalls asserts the EXACT recorded git command sequence. Reclaim's safety
// depends on the ORDER of its probes (existence → HEAD → prune → list → count →
// delete), so asserting only the return value would let a reordering that skips
// a guard pass.
func wantCalls(t *testing.T, g *stubGit, want ...string) {
	t.Helper()
	if len(g.calls) != len(want) {
		t.Fatalf("git calls =\n  %v\nwant\n  %v", g.calls, want)
	}
	for i := range want {
		if g.calls[i] != want[i] {
			t.Fatalf("git call[%d] = %q, want %q (full: %v)", i, g.calls[i], want[i], g.calls)
		}
	}
}

func TestReclaimEmptyBranchMissingBranchIsNoop(t *testing.T) {
	g := baseStub()
	// `rev-parse --verify --quiet` on an absent ref: non-zero exit, NO output.
	g.on("rev-parse --verify --quiet", "", errors.New("exit 1"))
	m := newMgr(t, g)
	ahead, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch: %v", err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0 for a missing branch", ahead)
	}
	// Nothing beyond the existence probe: no prune, no list, no delete.
	wantCalls(t, g, "rev-parse --verify --quiet refs/heads/swarm/phase-714")
}

func TestReclaimEmptyBranchDeletesEmptyLeftover(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "0\n", nil) // zero commits ahead of the base
	m := newMgr(t, g)
	ahead, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch: %v", err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0", ahead)
	}
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-714",
		"symbolic-ref --short HEAD",
		"worktree prune",
		"worktree list --porcelain",
		// reclaimBase — symbolic-ref only, no detached-HEAD fallback.
		"symbolic-ref --short HEAD",
		"rev-parse refs/heads/main",
		"rev-list --count aaaa1111..refs/heads/swarm/phase-714",
		"branch -D swarm/phase-714",
	)
}

func TestReclaimEmptyBranchKeepsBranchWithCommits(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "3\n", nil)
	m := newMgr(t, g)
	ahead, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-715")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch: %v", err)
	}
	if ahead != 3 {
		t.Errorf("ahead = %d, want 3", ahead)
	}
	// Identical prelude, but the sequence STOPS at the count — a branch holding
	// work is never destroyed implicitly.
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-715",
		"symbolic-ref --short HEAD",
		"worktree prune",
		"worktree list --porcelain",
		"symbolic-ref --short HEAD",
		"rev-parse refs/heads/main",
		"rev-list --count aaaa1111..refs/heads/swarm/phase-715",
	)
}

func TestReclaimEmptyBranchRefusesCheckedOutBranch(t *testing.T) {
	g := baseStub()
	g.on("worktree list --porcelain",
		"worktree /wt/proj/phase-716\nbranch refs/heads/swarm/phase-716\n\n", nil)
	m := newMgr(t, g)
	_, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-716")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("err = %v, want ErrBranchCheckedOut", err)
	}
	if !strings.Contains(err.Error(), "/wt/proj/phase-716") {
		t.Errorf("err = %v, want it to name the holding worktree path", err)
	}
	if g.called("branch -D") {
		t.Error("a checked-out branch must never be deleted")
	}
	if g.called("rev-list") {
		t.Error("the guard must short-circuit before counting commits")
	}
}

// The HEAD guard is reachable only for a swarm/ branch now that the namespace
// guard runs first, so the repo is scripted as sitting ON the run branch — the
// state a user lands in by checking a run branch out to inspect it.
func TestReclaimEmptyBranchRefusesHeadBranch(t *testing.T) {
	g := baseStub()
	g.on("symbolic-ref --short HEAD", "swarm/phase-716\n", nil)
	m := newMgr(t, g)
	_, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-716")
	if !errors.Is(err, ErrBranchIsHead) {
		t.Fatalf("err = %v, want ErrBranchIsHead", err)
	}
	if g.called("branch -D") {
		t.Error("the repo's HEAD branch must never be deleted")
	}
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-716",
		"symbolic-ref --short HEAD",
	)
}

// ---- I2: the swarm/ namespace guard --------------------------------------

// A branch outside the swarm/ namespace is refused by BOTH reclaim paths before
// any git command runs. Without it, ReclaimEmptyBranch("dev") on a repo whose
// HEAD is a feature branch that already contains dev computes ahead == 0 and
// deletes dev — the guard closes the class at the boundary rather than relying on
// every caller to build the name correctly.
func TestReclaimEmptyBranchRefusesForeignNamespace(t *testing.T) {
	for _, branch := range []string{"dev", "main", "feature/x", "swarm/", "swarmish/x", "swarm/a/b"} {
		g := baseStub()
		m := newMgr(t, g)
		_, err := m.ReclaimEmptyBranch("/tmp/repo", branch)
		if !errors.Is(err, ErrRefusedBranch) {
			t.Errorf("ReclaimEmptyBranch(%q) err = %v, want ErrRefusedBranch", branch, err)
		}
		if !strings.Contains(err.Error(), branch) {
			t.Errorf("err = %v, want it to name the branch %q", err, branch)
		}
		if len(g.calls) != 0 {
			t.Errorf("ReclaimEmptyBranch(%q) issued git calls %v, want none", branch, g.calls)
		}
	}
}

func TestDeleteBranchRefusesForeignNamespace(t *testing.T) {
	for _, branch := range []string{"dev", "main", "feature/x", "swarm/", "swarm/a/b"} {
		g := baseStub()
		m := newMgr(t, g)
		_, err := m.DeleteBranch("/tmp/repo", branch)
		if !errors.Is(err, ErrRefusedBranch) {
			t.Errorf("DeleteBranch(%q) err = %v, want ErrRefusedBranch", branch, err)
		}
		if len(g.calls) != 0 {
			t.Errorf("DeleteBranch(%q) issued git calls %v, want none", branch, g.calls)
		}
	}
}

func TestReclaimEmptyBranchNonNumericCount(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "not-a-number\n", nil)
	m := newMgr(t, g)
	_, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-717")
	if err == nil {
		t.Fatal("err = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), "swarm/phase-717") {
		t.Errorf("err = %v, want it to name the branch", err)
	}
	if g.called("branch -D") {
		t.Error("an unparseable count must not authorize a delete")
	}
}

func TestReclaimEmptyBranchPropagatesListFailure(t *testing.T) {
	g := baseStub()
	g.on("worktree list --porcelain", "", errors.New("could not lock"))
	m := newMgr(t, g)
	if _, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-1"); err == nil {
		t.Fatal("err = nil, want the worktree-list failure surfaced")
	}
	if g.called("branch -D") {
		t.Error("a failed list probe must not authorize a delete")
	}
}

func TestDeleteBranchDeletesBranchWithCommits(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "3\n", nil) // has work — DeleteBranch discards it anyway
	m := newMgr(t, g)
	existed, err := m.DeleteBranch("/tmp/repo", "swarm/phase-715")
	if err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if !existed {
		t.Error("existed = false, want true — the branch was there and was deleted")
	}
	wantCalls(t, g,
		"rev-parse --verify --quiet refs/heads/swarm/phase-715",
		"symbolic-ref --short HEAD",
		"worktree prune",
		"worktree list --porcelain",
		// No rev-list: the explicit decision has already been made.
		"branch -D swarm/phase-715",
	)
}

func TestDeleteBranchMissingIsIdempotent(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet", "", errors.New("exit 1"))
	m := newMgr(t, g)
	existed, err := m.DeleteBranch("/tmp/repo", "swarm/phase-999")
	if err != nil {
		t.Fatalf("DeleteBranch on a missing branch = %v, want nil", err)
	}
	// The whole point of the second return value: idempotent is not the same as
	// "deleted something", and a caller that cannot tell them apart reports a no-op
	// as a deletion.
	if existed {
		t.Error("existed = true on a missing branch, want false")
	}
	if g.called("branch -D") {
		t.Error("nothing to delete, yet a delete was issued")
	}
}

func TestDeleteBranchRefusesCheckedOut(t *testing.T) {
	g := baseStub()
	g.on("worktree list --porcelain",
		"worktree /wt/proj/phase-716\nbranch refs/heads/swarm/phase-716\n\n", nil)
	m := newMgr(t, g)
	_, err := m.DeleteBranch("/tmp/repo", "swarm/phase-716")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("err = %v, want ErrBranchCheckedOut", err)
	}
	if g.called("branch -D") {
		t.Error("DeleteBranch deleted a branch that is checked out")
	}
}

func TestDeleteBranchRefusesHead(t *testing.T) {
	g := baseStub()
	g.on("symbolic-ref --short HEAD", "swarm/phase-716\n", nil)
	m := newMgr(t, g)
	if _, err := m.DeleteBranch("/tmp/repo", "swarm/phase-716"); !errors.Is(err, ErrBranchIsHead) {
		t.Fatalf("err = %v, want ErrBranchIsHead", err)
	}
	if g.called("branch -D") {
		t.Error("DeleteBranch deleted the repo's HEAD branch")
	}
}

func TestDeleteBranchPropagatesGitError(t *testing.T) {
	g := baseStub()
	g.on("branch -D", "error: cannot delete", errors.New("exit 1"))
	m := newMgr(t, g)
	if _, err := m.DeleteBranch("/tmp/repo", "swarm/phase-1"); err == nil {
		t.Error("DeleteBranch should propagate a git branch -D failure")
	}
}

// ---- C1: a crash-leftover worktree at the run's OWN path -------------------

// ownLeftoverStub scripts the exact state a daemon crash leaves behind: the run's
// worktree is still REGISTERED at the deterministic path Acquire computes
// (<Root>/<slug>/<taskID>), still checked out on its own branch. `git worktree
// prune` cannot clear it because the directory still exists.
func ownLeftoverStub(t *testing.T, slug, taskID string) (*stubGit, *Manager, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "wts")
	own := filepath.Join(root, slug, taskID)
	g := baseStub()
	g.on("worktree list --porcelain",
		fmt.Sprintf("worktree %s\nbranch refs/heads/swarm/%s\n\n", own, taskID), nil)
	return g, &Manager{Git: g, Root: root}, own
}

// The regression this fixes: checkBranchReclaimable saw the branch checked out
// ANYWHERE — including at our own path — and returned ErrBranchCheckedOut, so
// phaserun.Start bailed before reaching the Acquire that recovers this exact state
// by warm reuse (invariant 4). Retrying a phase after a daemon crash was
// impossible.
func TestReclaimEmptyBranchToleratesOwnLeftoverWorktree(t *testing.T) {
	g, m, own := ownLeftoverStub(t, "proj", "phase-714")

	ahead, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch on our own leftover worktree = %v, want nil", err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0 — the checkout is ours, there is nothing to reclaim", ahead)
	}
	if g.called("branch -D") {
		t.Fatalf("a branch checked out in a live worktree must never be deleted (calls: %v)", g.calls)
	}
	// Reclaim stops at the list — counting commits would be pointless work, and
	// a non-zero count must NOT surface as BranchDirty: warm reuse continues the
	// work rather than destroying it.
	if g.called("rev-list") {
		t.Errorf("reclaim counted commits for a branch it will not touch (calls: %v)", g.calls)
	}

	// …and Start's next step, Acquire, recovers the run by warm-reusing that very
	// worktree.
	a, err := m.Acquire("/tmp/repo", "proj", "phase-714")
	if err != nil {
		t.Fatalf("Acquire after reclaim = %v, want warm reuse", err)
	}
	if !samePath(a.Path, own) || a.Branch != "swarm/phase-714" {
		t.Errorf("Acquire = {%s,%s}, want warm reuse of {%s,swarm/phase-714}", a.Path, a.Branch, own)
	}
	if g.called("worktree add") {
		t.Error("warm reuse must not re-add the worktree")
	}
}

// The branch checked out at a FOREIGN path is still a hard conflict — Acquire
// would reject it as ErrBranchBusy, and `git branch -D` refuses it anyway.
func TestReclaimEmptyBranchStillRefusesForeignWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	g := baseStub()
	g.on("worktree list --porcelain",
		"worktree /elsewhere/phase-714\nbranch refs/heads/swarm/phase-714\n\n", nil)
	m := &Manager{Git: g, Root: root}
	_, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("err = %v, want ErrBranchCheckedOut for a foreign checkout", err)
	}
}

// A worktree under our Root but for a DIFFERENT task id is not ours either.
func TestReclaimEmptyBranchForeignTaskUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wts")
	g := baseStub()
	g.on("worktree list --porcelain",
		"worktree "+filepath.Join(root, "proj", "phase-999")+"\nbranch refs/heads/swarm/phase-714\n\n", nil)
	m := &Manager{Git: g, Root: root}
	if _, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714"); !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("err = %v, want ErrBranchCheckedOut", err)
	}
}

// The case ownsWorktreePath's comment is actually defending, and the one the
// sibling tests miss: same Root, same task LEAF, but a DIFFERENT project slug.
//
// ownsWorktreePath checks Base(p) == taskID and Dir(Dir(p)) == Root; it does not
// pin the slug component, so a leftover minted under a previous slug reads as
// "ours" and reclaim answers (0, nil) — it will not delete a branch it believes a
// live run of ours holds. Acquire then computes a path under the NEW slug, finds
// the branch checked out at a path that is not it, and refuses with ErrBranchBusy.
//
// That is the accepted behaviour, not a defect: slug churn is real here (the
// attribution canonicalization collapsed 15 project rows to 9), and failing loudly
// on a mapped 409 beats silently reusing a worktree under a stale slug. This test
// pins it so the next reader does not "fix" the missing slug pin without noticing
// it converts a 409 into a wrong warm reuse.
func TestReclaimThenAcquireIsBusyAcrossSlugChurn(t *testing.T) {
	g, m, own := ownLeftoverStub(t, "old-slug", "phase-714")

	ahead, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714")
	if err != nil {
		t.Fatalf("ReclaimEmptyBranch = %v, want nil — the leaf matches, so it reads as ours", err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0 — reclaim stops at the list for an own checkout", ahead)
	}
	if g.called("branch -D") {
		t.Fatalf("reclaim deleted a branch held by a live worktree (calls: %v)", g.calls)
	}

	// Same task, NEW slug ⇒ a different deterministic path, so warm reuse cannot
	// apply and the branch is genuinely busy elsewhere.
	_, err = m.Acquire("/tmp/repo", "new-slug", "phase-714")
	if !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("Acquire under a new slug = %v, want ErrBranchBusy (the mapped 409 path)", err)
	}
	if !strings.Contains(err.Error(), own) {
		t.Errorf("err = %v, want it to name the holding worktree %s", err, own)
	}
	if g.called("worktree add") {
		t.Error("Acquire must not create a second worktree for a branch already checked out")
	}
}

// DeleteBranch deliberately does NOT get the own-path tolerance: it ends in a
// `git branch -D`, which git itself refuses while the branch is checked out. A
// sentinel naming the holding worktree is a better answer than a raw git failure
// — and far better than the silent success I6 is about.
func TestDeleteBranchRefusesOwnLeftoverWorktree(t *testing.T) {
	g, m, own := ownLeftoverStub(t, "proj", "phase-714")
	_, err := m.DeleteBranch("/tmp/repo", "swarm/phase-714")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("err = %v, want ErrBranchCheckedOut", err)
	}
	if !strings.Contains(err.Error(), own) {
		t.Errorf("err = %v, want it to name the holding worktree %s", err, own)
	}
	if g.called("branch -D") {
		t.Error("DeleteBranch issued a delete git would refuse")
	}
}

// ---- I6: "missing" vs "git is unhappy" ------------------------------------

// A probe that FAILS WITH OUTPUT (unreadable repo, locked ref, bad permissions)
// is not proof the branch is gone. Reporting it as missing made DeleteBranch
// return nil, DeleteRunBranch log "deleted run branch", and the UI clear a
// dirty-branch banner on a no-op.
func TestDeleteBranchSurfacesProbeFailure(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet",
		"fatal: not a git repository (or any of the parent directories): .git",
		errors.New("exit 128"))
	m := newMgr(t, g)
	_, err := m.DeleteBranch("/tmp/repo", "swarm/phase-714")
	if err == nil {
		t.Fatal("DeleteBranch = nil on a broken probe, want an error — the branch is still there")
	}
	if !strings.Contains(err.Error(), "swarm/phase-714") {
		t.Errorf("err = %v, want it to name the branch", err)
	}
	if g.called("branch -D") {
		t.Error("a failed probe must not authorize a delete")
	}
}

func TestReclaimEmptyBranchSurfacesProbeFailure(t *testing.T) {
	g := baseStub()
	g.on("rev-parse --verify --quiet", "fatal: not a git repository", errors.New("exit 128"))
	m := newMgr(t, g)
	if _, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-714"); err == nil {
		t.Fatal("ReclaimEmptyBranch = nil on a broken probe, want an error")
	}
}

// ---- M7: a negative commit count ------------------------------------------

// strconv.Atoi("-1") succeeds and `ahead > 0` is false, so a negative count used
// to authorize `branch -D` — the one outcome a nonsensical count must never buy.
func TestReclaimEmptyBranchNegativeCount(t *testing.T) {
	g := baseStub()
	g.on("rev-list --count", "-1\n", nil)
	m := newMgr(t, g)
	_, err := m.ReclaimEmptyBranch("/tmp/repo", "swarm/phase-717")
	if err == nil {
		t.Fatal("err = nil for a negative commit count, want an error")
	}
	if !strings.Contains(err.Error(), "swarm/phase-717") {
		t.Errorf("err = %v, want it to name the branch", err)
	}
	if g.called("branch -D") {
		t.Error("a negative count must not authorize a delete")
	}
}

func TestResolveRootDefaultsToHome(t *testing.T) {
	// Empty Root resolves under $HOME/.swarmery/worktrees.
	m := &Manager{Git: baseStub()}
	root, err := m.resolveRoot()
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	if !strings.HasSuffix(root, filepath.FromSlash(DefaultRoot)) {
		t.Errorf("default root = %q, want it to end with %q", root, DefaultRoot)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("default root = %q, want absolute", root)
	}
}

// ---- porcelain parser ----------------------------------------------------

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /a\nHEAD 1111\nbranch refs/heads/main\n\n" +
		"worktree /b\nHEAD 2222\ndetached\n\n" +
		"worktree /c\nHEAD 3333\nbranch refs/heads/swarm/T-9\n"
	e := parseWorktreeList(out)
	if len(e) != 3 {
		t.Fatalf("entries = %d, want 3", len(e))
	}
	if p, ok := e.pathForBranch("swarm/T-9"); !ok || p != "/c" {
		t.Errorf("pathForBranch(swarm/T-9) = %q,%v", p, ok)
	}
	if _, ok := e.pathForBranch("nope"); ok {
		t.Error("pathForBranch(nope) should be false")
	}
	if w, ok := e.byPath("/b"); !ok || w.branch != "" {
		t.Errorf("byPath(/b) detached entry = %+v,%v", w, ok)
	}
}

// `git worktree lock` shows up as a bare "locked" line, or "locked <reason>";
// the exported Entry carries it so the janitor can honour the lock instead of
// finding out from a refused `worktree remove`.
func TestParseWorktreeListLocked(t *testing.T) {
	out := "worktree /a\nHEAD 1111\nbranch refs/heads/main\n\n" +
		"worktree /b\nHEAD 2222\nbranch refs/heads/feature\nlocked\n\n" +
		"worktree /c\nHEAD 3333\ndetached\nlocked PR in progress\n\n" +
		"worktree /d\nHEAD 4444\nbranch refs/heads/other\nprunable gitdir file points to non-existent location\n"
	got := ParseWorktreeList(out)
	want := map[string]bool{"/a": false, "/b": true, "/c": true, "/d": false}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %d", got, len(want))
	}
	for _, e := range got {
		if e.Locked != want[e.Path] {
			t.Errorf("%s: Locked = %v, want %v", e.Path, e.Locked, want[e.Path])
		}
	}
}

// ---- non-repo -------------------------------------------------------------

func TestAcquireNonRepo(t *testing.T) {
	g := &stubGit{}
	// symbolic-ref fails AND rev-parse HEAD fails → ErrNotARepo.
	g.on("symbolic-ref --short HEAD", "fatal: not a git repository", errors.New("exit 128"))
	g.on("rev-parse HEAD", "fatal: not a git repository", errors.New("exit 128"))
	m := newMgr(t, g)
	_, err := m.Acquire("/not/a/repo", "proj", "T-x")
	if !errors.Is(err, ErrNotARepo) {
		t.Fatalf("err = %v, want ErrNotARepo", err)
	}
}

// ---- OwnCheckoutOf: the read-only ownership probe diagnosis borrows ---------

func TestOwnCheckoutOf(t *testing.T) {
	// Our own crash leftover: registered at <Root>/<slug>/<taskID> on its branch.
	g, m, own := ownLeftoverStub(t, "proj", "phase-714")
	path, ok := m.OwnCheckoutOf("/tmp/repo", "swarm/phase-714")
	if !ok || !samePath(path, own) {
		t.Errorf("OwnCheckoutOf = (%q,%v), want (%q,true)", path, ok, own)
	}
	// Read-only: it must not prune, delete or add anything — a diagnosis probe
	// that mutated the repo would be a very unpleasant surprise.
	for _, bad := range []string{"branch -D", "worktree prune", "worktree add", "worktree remove"} {
		if g.called(bad) {
			t.Errorf("OwnCheckoutOf issued %q (calls: %v)", bad, g.calls)
		}
	}

	// A checkout OUTSIDE Root is not ours.
	g2 := baseStub()
	g2.on("worktree list --porcelain",
		"worktree /elsewhere/phase-714\nbranch refs/heads/swarm/phase-714\n\n", nil)
	m2 := &Manager{Git: g2, Root: filepath.Join(t.TempDir(), "wts")}
	if _, ok := m2.OwnCheckoutOf("/tmp/repo", "swarm/phase-714"); ok {
		t.Error("a foreign checkout was reported as ours")
	}

	// No checkout at all, a branch outside swarm/, and an unreadable git all
	// answer "not ours" rather than erroring — the probe has no error channel by
	// design, so every unknown must degrade to false.
	g3, m3, _ := ownLeftoverStub(t, "proj", "phase-714")
	if _, ok := m3.OwnCheckoutOf("/tmp/repo", "swarm/phase-999"); ok {
		t.Error("an unheld branch was reported as ours")
	}
	if _, ok := m3.OwnCheckoutOf("/tmp/repo", "dev"); ok {
		t.Error("a branch outside swarm/ was reported as ours")
	}
	if len(g3.calls) == 0 {
		t.Error("expected the list probe to have run")
	}
	g4 := baseStub()
	g4.on("worktree list --porcelain", "fatal: not a git repository", errors.New("exit 128"))
	m4 := &Manager{Git: g4, Root: filepath.Join(t.TempDir(), "wts")}
	if _, ok := m4.OwnCheckoutOf("/tmp/repo", "swarm/phase-714"); ok {
		t.Error("an unreadable git was reported as ours")
	}
}
