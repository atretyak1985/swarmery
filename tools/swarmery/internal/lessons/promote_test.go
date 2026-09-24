package lessons

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// gitRepo makes a fixture consumer repo on branch main with one committed file
// under internal/store.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(dir, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "store", "store.go"), []byte("package store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestAreaDir(t *testing.T) {
	for in, want := range map[string]string{
		"internal/store/**": "internal/store",
		"internal/store/":   "internal/store",
		"web/src/x.tsx":     "web/src",
		"*":                 ".",
		".github/workflows": ".github/workflows",
	} {
		if got, err := AreaDir(in); err != nil || got != want {
			t.Errorf("AreaDir(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"../elsewhere", "a/../../b", "-rf", "a/-x", "a b", "a;b", "a\\b", "a/..b"} {
		if _, err := AreaDir(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("AreaDir(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestPromoteWritesNestedClaudeMDOnANewBranch(t *testing.T) {
	db := openDB(t)
	repo := gitRepo(t)
	phaseID := seedRun(t, db, "promo", 0.2, "")
	id := seedActive(t, db, phaseID, 1, "Index every child column before a prune.", "internal/store/**", "2026-09-20T00:00:00Z")
	resolve := func(string, ...string) (string, error) { return repo, nil }
	headBefore := gitOut(t, repo, "rev-parse", "HEAD")
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	l, err := Promote(db, worktree.ExecGit{}, resolve, id, PromoteInput{}, now)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	branch := PromoteBranch(id)
	if l.PromotedBranch != branch {
		t.Errorf("PromotedBranch = %q, want %q", l.PromotedBranch, branch)
	}
	// The main checkout is untouched: same branch, same HEAD, clean tree, no
	// leftover worktree.
	if b := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); b != "main" {
		t.Errorf("checked-out branch = %q, want main", b)
	}
	if h := gitOut(t, repo, "rev-parse", "HEAD"); h != headBefore {
		t.Errorf("HEAD moved: %s → %s", headBefore, h)
	}
	if st := gitOut(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("main checkout dirty: %q", st)
	}
	if wts := gitOut(t, repo, "worktree", "list"); strings.Count(wts, "\n") != 0 {
		t.Errorf("leftover worktrees:\n%s", wts)
	}
	// The branch carries exactly one new file with the bullet, id kept.
	body := gitOut(t, repo, "show", branch+":internal/store/CLAUDE.md")
	if !strings.Contains(body, "- ["+Ref(id)+"] Index every child column before a prune.") {
		t.Errorf("CLAUDE.md on %s = %q", branch, body)
	}
	if files := gitOut(t, repo, "diff", "--name-only", "main", branch); files != "internal/store/CLAUDE.md" {
		t.Errorf("branch diff = %q, want only the nested CLAUDE.md", files)
	}
	var logged int
	_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_promotions WHERE lesson_id = ? AND error = '' AND commit_sha != ''`, id).Scan(&logged)
	if logged != 1 {
		t.Errorf("promotion log rows = %d, want 1", logged)
	}

	// Again: the branch exists → a state error, logged as a failed attempt.
	if _, err := Promote(db, worktree.ExecGit{}, resolve, id, PromoteInput{}, now); !errors.Is(err, ErrState) {
		t.Errorf("second Promote err = %v, want ErrState", err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_promotions WHERE lesson_id = ? AND error != ''`, id).Scan(&logged)
	if logged != 1 {
		t.Errorf("failed attempts logged = %d, want 1", logged)
	}

	// A missing area directory is refused, and leaves no branch behind.
	other := seedActive(t, db, phaseID, 2, "Something about docs.", "docs/**", "2026-09-21T00:00:00Z")
	if _, err := Promote(db, worktree.ExecGit{}, resolve, other, PromoteInput{}, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing dir err = %v, want ErrInvalid", err)
	}
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+PromoteBranch(other)).CombinedOutput(); err == nil {
		t.Errorf("refused promotion left branch behind: %s", out)
	}

	// A candidate cannot be promoted.
	cand := seedCandidate(t, db, "promo-cand", "Not yet")
	if _, err := Promote(db, worktree.ExecGit{}, resolve, cand, PromoteInput{}, now); !errors.Is(err, ErrState) {
		t.Errorf("candidate Promote err = %v, want ErrState", err)
	}
}
