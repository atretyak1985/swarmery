package handoff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// requireGit skips the test when git is not on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// gitT runs git in dir with the operator's global/system config masked (no
// signing hooks, no default-branch surprises) and returns trimmed stdout.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-C", dir,
	}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newDirtyRepo builds a repo on branch main with one commit, then modifies a
// tracked file and adds an untracked one. Returns the repo root.
func newDirtyRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitT(t, root, "init", "-q", "-b", "main")
	writeT(t, filepath.Join(root, "tracked.txt"), "v1\n")
	writeT(t, filepath.Join(root, "sub", "keep.txt"), "keep\n")
	gitT(t, root, "add", ".")
	gitT(t, root, "commit", "-q", "-m", "init")
	writeT(t, filepath.Join(root, "tracked.txt"), "v2\n")
	writeT(t, filepath.Join(root, "new file.txt"), "untracked\n")
	return root
}

func TestRepoAnchorRoot(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)

	a := repoAnchor(root)
	if !a.Known {
		t.Fatalf("anchor must be known in a git repo; got %+v", a)
	}
	if a.Root != root {
		t.Errorf("Root = %q, want %q", a.Root, root)
	}
	if want := gitT(t, root, "rev-parse", "HEAD"); a.Head != want {
		t.Errorf("Head = %q, want %q", a.Head, want)
	}
	if a.Branch != "main" {
		t.Errorf("Branch = %q, want main", a.Branch)
	}
	wantDirty := []string{"new file.txt", "tracked.txt"}
	got := append([]string(nil), a.Dirty...)
	if len(got) == 2 && got[0] > got[1] {
		got[0], got[1] = got[1], got[0]
	}
	if !reflect.DeepEqual(got, wantDirty) || a.DirtyTotal != 2 {
		t.Errorf("Dirty = %q (total %d), want %q (total 2)", a.Dirty, a.DirtyTotal, wantDirty)
	}
}

func TestRepoAnchorSubdirResolvesRoot(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)

	a := repoAnchor(filepath.Join(root, "sub"))
	if !a.Known || a.Root != root {
		t.Fatalf("subdir cwd must resolve to root %q; got %+v", root, a)
	}
	if want := gitT(t, root, "rev-parse", "HEAD"); a.Head != want {
		t.Errorf("Head = %q, want %q", a.Head, want)
	}
}

func TestRepoAnchorNonGitUnknown(t *testing.T) {
	a := repoAnchor(t.TempDir())
	if a.Known {
		t.Fatalf("a non-git dir must give Known=false; got %+v", a)
	}
	if md := a.Markdown(); !strings.Contains(md, "- unknown (cwd is not inside a git repository)") {
		t.Errorf("unknown anchor markdown = %q", md)
	}
}

func TestRepoAnchorMissingOrEmptyCwdUnknown(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)
	// A deleted cwd inside a repo must NOT walk up and anchor the parent.
	if a := repoAnchor(filepath.Join(root, "gone")); a.Known || a.Reason != "cwd no longer exists" {
		t.Errorf("deleted cwd must be unknown; got %+v", a)
	}
	if a := repoAnchor(""); a.Known {
		t.Errorf("empty cwd must be unknown; got %+v", a)
	}
}

func TestRepoAnchorWorktreeResolvesOwnHead(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, root, "worktree", "add", "-q", "-b", "feat/wt", wt)
	// Diverge the worktree so its HEAD differs from the main checkout's.
	writeT(t, filepath.Join(wt, "wt-only.txt"), "wt\n")
	gitT(t, wt, "add", "wt-only.txt")
	gitT(t, wt, "commit", "-q", "-m", "wt commit")

	a := repoAnchor(wt)
	if !a.Known {
		t.Fatalf("worktree anchor must be known; got %+v", a)
	}
	if a.Root != wt {
		t.Errorf("Root = %q, want the worktree %q", a.Root, wt)
	}
	wtHead := gitT(t, wt, "rev-parse", "HEAD")
	if a.Head != wtHead {
		t.Errorf("Head = %q, want the worktree's own HEAD %q", a.Head, wtHead)
	}
	if mainHead := gitT(t, root, "rev-parse", "HEAD"); a.Head == mainHead {
		t.Errorf("worktree Head must differ from the main checkout's %q", mainHead)
	}
	if a.Branch != "feat/wt" {
		t.Errorf("Branch = %q, want feat/wt", a.Branch)
	}
	if a.DirtyTotal != 0 || !strings.Contains(a.Markdown(), "- Uncommitted (0): none") {
		t.Errorf("clean worktree must report no uncommitted paths; got %+v", a)
	}
}

func TestRepoAnchorDetachedBranch(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)
	gitT(t, root, "checkout", "-q", "--detach")
	a := repoAnchor(root)
	if !a.Known || a.Branch != "(detached)" {
		t.Errorf("detached HEAD must report (detached); got %+v", a)
	}
}

func TestRepoAnchorCapsDirtyPaths(t *testing.T) {
	requireGit(t)
	root := newDirtyRepo(t)
	for i := range maxDirtyPaths + 3 {
		writeT(t, filepath.Join(root, fmt.Sprintf("extra-%02d.txt", i)), "x\n")
	}
	a := repoAnchor(root)
	total := maxDirtyPaths + 3 + 2 // extras + the two from newDirtyRepo
	if a.DirtyTotal != total {
		t.Errorf("DirtyTotal = %d, want %d", a.DirtyTotal, total)
	}
	if len(a.Dirty) != maxDirtyPaths+1 {
		t.Fatalf("len(Dirty) = %d, want %d paths + the overflow marker", len(a.Dirty), maxDirtyPaths)
	}
	if last, want := a.Dirty[maxDirtyPaths], fmt.Sprintf("… (+%d more)", total-maxDirtyPaths); last != want {
		t.Errorf("overflow marker = %q, want %q", last, want)
	}
	if md := a.Markdown(); !strings.Contains(md, fmt.Sprintf("- Uncommitted (%d):\n", total)) {
		t.Errorf("markdown must carry the uncapped count; got %q", md)
	}
}

func TestAnchorParsePorcelainZ(t *testing.T) {
	out := " M tracked.txt\x00R  new name.txt\x00old name.txt\x00?? untracked.txt\x00"
	got := parsePorcelainZ(out)
	want := []string{"tracked.txt", "new name.txt", "untracked.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelainZ = %q, want %q (rename keeps the new name)", got, want)
	}
}

func TestAnchorMarkdownKnown(t *testing.T) {
	a := anchor{
		Root: "/r", Head: "abc123", Branch: "main",
		Dirty: []string{"a.go", "b.go"}, DirtyTotal: 2, Known: true,
	}
	want := anchorHeading + "\n" +
		"- Root: /r\n" +
		"- HEAD: abc123\n" +
		"- Branch: main\n" +
		"- Uncommitted (2):\n" +
		"  - a.go\n" +
		"  - b.go\n"
	if got := a.Markdown(); got != want {
		t.Errorf("Markdown =\n%s\nwant\n%s", got, want)
	}
}
