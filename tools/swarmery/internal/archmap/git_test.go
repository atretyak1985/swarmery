package archmap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tempRepo builds a throwaway git repo with two commits and returns its path
// plus the two shas. Committer identity and the initial branch are pinned so
// the test does not depend on the machine's git config.
func tempRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "--initial-branch=main", ".")
	write("README.md", "one\n")
	write("internal/api/routes.go", "package api\n")
	run("add", "-A")
	run("commit", "-m", "first")
	first = run("rev-parse", "HEAD")

	write("web/src/pages/Home.tsx", "export const Home = () => null;\n")
	write("internal/api/routes.go", "package api // touched\n")
	run("add", "-A")
	run("commit", "-m", "second")
	second = run("rev-parse", "HEAD")

	return dir, first, second
}

func TestDiff(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	files, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := []string{"internal/api/routes.go", "web/src/pages/Home.tsx"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("Diff = %v, want %v", files, want)
	}

	// An empty range is an empty, non-nil slice — "nothing changed", not "unknown".
	same, err := Diff(context.Background(), dir, second, second)
	if err != nil {
		t.Fatalf("Diff(second..second): %v", err)
	}
	if same == nil || len(same) != 0 {
		t.Errorf("Diff(second..second) = %v, want an empty non-nil slice", same)
	}
}

func TestBehind(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	n, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Behind: %v", err)
	}
	if n != 1 {
		t.Errorf("Behind(first..second) = %d, want 1", n)
	}

	if n, err := Behind(context.Background(), dir, second, second); err != nil || n != 0 {
		t.Errorf("Behind(second..second) = %d, %v; want 0, nil", n, err)
	}
	// The range is directional: second..first walks backwards and finds nothing.
	if n, err := Behind(context.Background(), dir, second, first); err != nil || n != 0 {
		t.Errorf("Behind(second..first) = %d, %v; want 0, nil", n, err)
	}
}

// TestMemo proves the second call for the same (repo, from, to) is served from
// the cache: git is removed from the equation by deleting the repo between the
// two calls — a re-fork would fail, the memo cannot.
func TestMemo(t *testing.T) {
	ResetMemo()
	dir, first, second := tempRepo(t)

	files, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	behind, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("Behind: %v", err)
	}
	if got := memoStats(); got != 2 {
		t.Errorf("memo size = %d after one Diff + one Behind, want 2", got)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	files2, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("memoised Diff after the repo vanished: %v", err)
	}
	if !reflect.DeepEqual(files2, files) {
		t.Errorf("memoised Diff = %v, want the first result %v", files2, files)
	}
	behind2, err := Behind(context.Background(), dir, first, second)
	if err != nil {
		t.Fatalf("memoised Behind after the repo vanished: %v", err)
	}
	if behind2 != behind {
		t.Errorf("memoised Behind = %d, want %d", behind2, behind)
	}

	// The returned slice is a copy — mutating it must not poison the cache.
	files2[0] = "MUTATED"
	files3, err := Diff(context.Background(), dir, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if files3[0] == "MUTATED" {
		t.Error("mutating a returned slice changed the memoised value")
	}

	ResetMemo()
	if got := memoStats(); got != 0 {
		t.Errorf("memo size = %d after ResetMemo, want 0", got)
	}
}

func TestDiffRejectsOptionLikeRevisions(t *testing.T) {
	ResetMemo()
	dir, _, second := tempRepo(t)

	for _, bad := range []string{"", "--upload-pack=touch /tmp/pwned", "-x", "a b"} {
		if _, err := Diff(context.Background(), dir, bad, second); err == nil {
			t.Errorf("Diff accepted from=%q, want a refusal", bad)
		}
		if _, err := Behind(context.Background(), dir, second, bad); err == nil {
			t.Errorf("Behind accepted to=%q, want a refusal", bad)
		}
	}
}

func TestDiffOnNonRepo(t *testing.T) {
	ResetMemo()
	if _, err := Diff(context.Background(), t.TempDir(), "a1b2c3d", "e4f5a6b"); err == nil {
		t.Error("Diff on a non-repo returned nil error")
	}
}

func TestDefaultBranch(t *testing.T) {
	ResetMemo()
	dir, _, _ := tempRepo(t)

	// No origin: falls through to the local `main` the fixture repo was
	// initialised with.
	if got := DefaultBranch(dir); got != "main" {
		t.Errorf("DefaultBranch = %q, want \"main\"", got)
	}

	// Not a repo at all: the empty string, meaning "no baseline" — never a
	// confident "main" the caller would then diff against and 500 on.
	if got := DefaultBranch(t.TempDir()); got != "" {
		t.Errorf("DefaultBranch(non-repo) = %q, want \"\"", got)
	}
}

func TestDefaultBranchPrefersOriginHEAD(t *testing.T) {
	ResetMemo()
	dir, _, _ := tempRepo(t)

	// Fabricate origin/HEAD → origin/trunk without a network remote: a packed
	// symbolic ref file is exactly what `git remote set-head` writes.
	gitDir := filepath.Join(dir, ".git", "refs", "remotes", "origin")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/remotes/origin/trunk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "trunk"), []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := DefaultBranch(dir); got != "trunk" {
		t.Errorf("DefaultBranch = %q, want \"trunk\" from refs/remotes/origin/HEAD", got)
	}
}
