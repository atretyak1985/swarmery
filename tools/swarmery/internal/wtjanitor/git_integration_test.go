package wtjanitor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// testRepo builds a real git repository with one commit on main and returns its
// path plus a runner. Mirrors internal/worktree/integration_test.go's bootstrap
// (same skip conditions) so both suites need the same environment and no more.
func testRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs a real git binary; skipped in -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	write(t, filepath.Join(repo, "README.md"), "hello\n")
	run("add", "README.md")
	run("commit", "-qm", "init")
	return repo, run
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

/* ---------- BlobInGit ---------- */

func TestBlobInGit_CommittedOnAnotherBranch(t *testing.T) {
	repo, run := testRepo(t)
	// Commit the content on a branch that is NOT checked out — the janitor must
	// still find it, because "already in git" means any ref, not HEAD.
	run("checkout", "-qb", "side")
	write(t, filepath.Join(repo, "note.md"), "kept\n")
	run("add", "note.md")
	run("commit", "-qm", "add note")
	run("checkout", "-q", "main")
	// Same content, in the working tree only.
	write(t, filepath.Join(repo, "note.md"), "kept\n")

	ok, err := RepoGit{}.BlobInGit(repo, repo, "note.md")
	if err != nil {
		t.Fatalf("BlobInGit: %v", err)
	}
	if !ok {
		t.Error("BlobInGit = false, want true for content committed on another branch")
	}
}

// The case that decided the four real worktrees: the file IS in git at this
// path, but the working copy is an OLDER (or newer) revision of it. Not a match.
func TestBlobInGit_DifferentRevisionOfSamePath(t *testing.T) {
	repo, run := testRepo(t)
	write(t, filepath.Join(repo, "note.md"), "v2\n")
	run("add", "note.md")
	run("commit", "-qm", "v2")
	write(t, filepath.Join(repo, "note.md"), "v1 — never committed\n")

	ok, err := RepoGit{}.BlobInGit(repo, repo, "note.md")
	if err != nil {
		t.Fatalf("BlobInGit: %v", err)
	}
	if ok {
		t.Error("BlobInGit = true, want false: this revision was never committed")
	}
}

func TestBlobInGit_PathWithNoHistory(t *testing.T) {
	repo, _ := testRepo(t)
	write(t, filepath.Join(repo, "brand-new.md"), "unique\n")

	ok, err := RepoGit{}.BlobInGit(repo, repo, "brand-new.md")
	if err != nil {
		t.Fatalf("BlobInGit: %v", err)
	}
	if ok {
		t.Error("BlobInGit = true, want false for a path with no history")
	}
}

// Identity is (path, content) — the same bytes at a DIFFERENT path do not
// count, or moving a file would look like it was already saved.
func TestBlobInGit_SameContentDifferentPath(t *testing.T) {
	repo, run := testRepo(t)
	write(t, filepath.Join(repo, "a.md"), "shared bytes\n")
	run("add", "a.md")
	run("commit", "-qm", "a")
	write(t, filepath.Join(repo, "b.md"), "shared bytes\n")

	ok, err := RepoGit{}.BlobInGit(repo, repo, "b.md")
	if err != nil {
		t.Fatalf("BlobInGit: %v", err)
	}
	if ok {
		t.Error("BlobInGit = true, want false: same content at a different path")
	}
}

/* ---------- hasOwnCommits ---------- */

func TestHasOwnCommits(t *testing.T) {
	repo, run := testRepo(t)
	g := RepoGit{}

	// A branch pointing at main has nothing of its own.
	run("branch", "same-as-main")
	if own, err := g.hasOwnCommits(repo, "same-as-main"); err != nil || own {
		t.Errorf("hasOwnCommits(same-as-main) = %v, %v; want false", own, err)
	}

	// One commit ahead → its own.
	run("checkout", "-qb", "feature")
	write(t, filepath.Join(repo, "f.md"), "work\n")
	run("add", "f.md")
	run("commit", "-qm", "feature work")
	if own, err := g.hasOwnCommits(repo, "feature"); err != nil || !own {
		t.Errorf("hasOwnCommits(feature) = %v, %v; want true", own, err)
	}

	// After merging into main, the same commits are reachable elsewhere.
	run("checkout", "-q", "main")
	run("merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	if own, err := g.hasOwnCommits(repo, "feature"); err != nil || own {
		t.Errorf("hasOwnCommits(feature) after merge = %v, %v; want false", own, err)
	}
}

/* ---------- dirty ---------- */

func TestDirty(t *testing.T) {
	repo, run := testRepo(t)
	write(t, filepath.Join(repo, "tracked-clean.md"), "clean\n")
	write(t, filepath.Join(repo, "tracked-dirty.md"), "before\n")
	run("add", ".")
	run("commit", "-qm", "two tracked files")

	write(t, filepath.Join(repo, "tracked-dirty.md"), "after\n")
	write(t, filepath.Join(repo, "untracked.md"), "new\n")
	// A space in the name: git quotes such paths in default porcelain output,
	// which is exactly what -z parsing exists to avoid.
	write(t, filepath.Join(repo, "with space.md"), "spaced\n")

	got, err := RepoGit{}.dirty(repo)
	if err != nil {
		t.Fatalf("dirty: %v", err)
	}
	set := map[string]bool{}
	for _, p := range got {
		set[p] = true
	}
	for _, want := range []string{"tracked-dirty.md", "untracked.md", "with space.md"} {
		if !set[want] {
			t.Errorf("dirty missing %q; got %v", want, got)
		}
	}
	if set["tracked-clean.md"] {
		t.Errorf("dirty listed a clean tracked file; got %v", got)
	}
}

/* ---------- Inspect ---------- */

// noLive is the liveness stub for tests that are not about liveness.
type noLive struct{}

func (noLive) Busy(string) (bool, error) { return false, nil }

func TestInspect_MainPlusAddedWorktree(t *testing.T) {
	repo, run := testRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-a")
	run("worktree", "add", "-q", "-b", "worktree-agent-a", wtPath)

	got, err := RepoGit{}.Inspect(repo, noLive{}, nil)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Inspect returned %d entries, want 2: %+v", len(got), got)
	}
	mains := 0
	for _, wt := range got {
		if wt.IsMain {
			mains++
		}
	}
	if mains != 1 {
		t.Errorf("IsMain count = %d, want exactly 1", mains)
	}
	var added *Worktree
	for i := range got {
		if !got[i].IsMain {
			added = &got[i]
		}
	}
	if added.Branch != "worktree-agent-a" {
		t.Errorf("added worktree branch = %q, want worktree-agent-a", added.Branch)
	}
	if added.NewestMTime.IsZero() {
		t.Error("added worktree NewestMTime is zero; the walk found nothing")
	}
}

// A directory deleted behind git's back stays in `worktree list` until prune.
// Inspecting it would fail on every call, so it is skipped, not reported.
func TestInspect_SkipsVanishedDirectory(t *testing.T) {
	repo, run := testRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-gone")
	run("worktree", "add", "-q", "-b", "worktree-agent-gone", wtPath)
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}

	got, err := RepoGit{}.Inspect(repo, noLive{}, nil)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got) != 1 || !got[0].IsMain {
		t.Errorf("Inspect = %+v, want only the main checkout", got)
	}
}

// A real repository with an operator worktree and a harness agent worktree side
// by side under .claude/worktrees. The agent one is observed; the operator's is
// listed as Foreign — so its branch still counts as checked out — but nothing
// is observed inside it (no `git status`, no walk). The repository comes from
// t.TempDir(), which on macOS is a symlinked path — the live machine's shape.
func TestInspect_OwnershipFilterSkipsForeignWorktrees(t *testing.T) {
	repo, run := testRepo(t)
	foreign := filepath.Join(repo, ".claude", "worktrees", "feature-x")
	agent := filepath.Join(repo, ".claude", "worktrees", "agent-ab12")
	run("worktree", "add", "-q", "-b", "feature-x", foreign)
	run("worktree", "add", "-q", "-b", "worktree-agent-ab12", agent)
	write(t, filepath.Join(foreign, "scratch.txt"), "operator work\n")

	owned := func(p string) bool { return agentOwned(repo, "", p) }
	got, err := RepoGit{}.Inspect(repo, noLive{}, owned)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	byBranch := map[string]Worktree{}
	for _, wt := range got {
		if !wt.IsMain {
			byBranch[wt.Branch] = wt
		}
	}
	if len(byBranch) != 2 {
		t.Fatalf("non-main worktrees = %+v, want feature-x and worktree-agent-ab12", byBranch)
	}
	if f := byBranch["feature-x"]; !f.Foreign || f.Dirty != nil || !f.NewestMTime.IsZero() {
		t.Errorf("operator worktree = %+v, want Foreign with nothing observed", f)
	}
	if a := byBranch["worktree-agent-ab12"]; a.Foreign || a.NewestMTime.IsZero() {
		t.Errorf("agent worktree = %+v, want owned and observed", a)
	}
}

// A worktree held by `git worktree lock` is listed Locked and, like the main
// checkout, not observed: no `git status` (and so no index.lock) inside it.
func TestInspect_LockedWorktreeIsListedNotObserved(t *testing.T) {
	repo, run := testRepo(t)
	agent := filepath.Join(repo, ".claude", "worktrees", "agent-cd34")
	run("worktree", "add", "-q", "-b", "worktree-agent-cd34", agent)
	run("worktree", "lock", "--reason", "kept on purpose", agent)
	write(t, filepath.Join(agent, "scratch.txt"), "work\n")

	owned := func(p string) bool { return agentOwned(repo, "", p) }
	got, err := RepoGit{}.Inspect(repo, noLive{}, owned)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var wt *Worktree
	for i := range got {
		if got[i].Branch == "worktree-agent-cd34" {
			wt = &got[i]
		}
	}
	if wt == nil {
		t.Fatalf("Inspect = %+v, want the locked agent worktree listed", got)
	}
	if !wt.Locked || wt.Dirty != nil || !wt.NewestMTime.IsZero() {
		t.Errorf("locked worktree = %+v, want Locked with nothing observed", *wt)
	}
}

/* ---------- lockFresh / newestMTime ---------- */

func TestLockFresh(t *testing.T) {
	repo, run := testRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-lock")
	run("worktree", "add", "-q", "-b", "worktree-agent-lock", wtPath)

	if lockFresh(repo, wtPath) {
		t.Error("lockFresh = true with no lock present")
	}

	lock := filepath.Join(repo, ".git", "worktrees", filepath.Base(wtPath), "index.lock")
	write(t, lock, "")
	if !lockFresh(repo, wtPath) {
		t.Error("lockFresh = false for a lock just created")
	}

	// Backdate it past the threshold: an abandoned lock must not veto forever.
	stale := time.Now().Add(-worktree.StaleLockAge() - time.Minute)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	if lockFresh(repo, wtPath) {
		t.Error("lockFresh = true for a lock older than StaleLockAge")
	}
}

// git churns files inside .git constantly; counting them would keep every
// worktree looking freshly touched and the idle floor would never elapse.
func TestNewestMTime_IgnoresGitDir(t *testing.T) {
	_, run := testRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-mtime")
	run("worktree", "add", "-q", "-b", "worktree-agent-mtime", wtPath)

	past := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(filepath.Join(wtPath, "README.md"), past, past); err != nil {
		t.Fatal(err)
	}
	before := newestMTime(wtPath)
	if before.IsZero() {
		t.Fatal("newestMTime = zero for a populated worktree")
	}

	// Touch something inside the worktree's .git link target and re-measure.
	gitLink := filepath.Join(wtPath, ".git")
	now := time.Now()
	_ = os.Chtimes(gitLink, now, now)

	after := newestMTime(wtPath)
	if !after.Equal(before) {
		t.Errorf("newestMTime moved from %v to %v after touching .git", before, after)
	}
}
