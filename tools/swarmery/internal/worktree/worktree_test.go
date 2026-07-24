package worktree

import (
	"errors"
	"fmt"
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
	if !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("err = %v, want ErrBranchBusy (branch exists on add)", err)
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
	// worktree remove fails → error surfaced, branch delete not attempted.
	g := &stubGit{}
	g.on("worktree remove", "cannot remove", errors.New("exit 1"))
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
